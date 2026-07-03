// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controllers

import (
	"testing"

	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/object"
)

// TestResolveModelRoute_VideoFamily proves every zen3-video model routes to
// do-ai's wan2-2-t2v-a14b upstream, is premium, and is owned_by hanzo (the
// upstream is never leaked), and that the unbranded catalog id routes too.
func TestResolveModelRoute_VideoFamily(t *testing.T) {
	branded := []string{"zen3-video", "zen3-video-fast", "zen3-video-pro"}
	for _, name := range branded {
		t.Run(name, func(t *testing.T) {
			route := resolveModelRoute(name)
			if route == nil {
				t.Fatalf("resolveModelRoute(%q) = nil, want non-nil", name)
			}
			if route.providerName != "do-ai" {
				t.Errorf("providerName = %q, want do-ai", route.providerName)
			}
			if route.upstreamModel != "wan2-2-t2v-a14b" {
				t.Errorf("upstreamModel = %q, want wan2-2-t2v-a14b", route.upstreamModel)
			}
			if !route.premium {
				t.Errorf("%q should be premium", name)
			}
			if route.ownedBy != "hanzo" {
				t.Errorf("ownedBy = %q, want hanzo", route.ownedBy)
			}
		})
	}

	// Unbranded passthrough: the raw catalog id resolves to itself, no brand.
	route := resolveModelRoute("wan2-2-t2v-a14b")
	if route == nil || route.providerName != "do-ai" || route.upstreamModel != "wan2-2-t2v-a14b" {
		t.Fatalf("wan2-2-t2v-a14b passthrough resolve failed: %+v", route)
	}
	if route.ownedBy != "" {
		t.Errorf("unbranded passthrough ownedBy = %q, want empty", route.ownedBy)
	}
}

// TestResolveModelRoute_VideoFamilyCaseInsensitive: the family resolves
// case-insensitively like every other route.
func TestResolveModelRoute_VideoFamilyCaseInsensitive(t *testing.T) {
	route := resolveModelRoute("ZEN3-VIDEO")
	if route == nil || route.upstreamModel != "wan2-2-t2v-a14b" {
		t.Fatalf("case-insensitive resolve failed: %+v", route)
	}
}

// TestVideoCostCents bills per video by user-facing name AND upstream id, with a
// conservative floor for an unknown video model (never $0), and is higher than
// the image rate.
func TestVideoCostCents(t *testing.T) {
	cases := []struct {
		model string
		n     int
		want  int64
	}{
		{"zen3-video", 1, 40},
		{"zen3-video", 2, 80},
		{"zen3-video-fast", 1, 40},
		{"zen3-video-pro", 3, 120},
		{"wan2-2-t2v-a14b", 1, 40},
		{"WAN2-2-T2V-A14B", 1, 40},          // case-insensitive
		{"some-unknown-video-model", 1, 40}, // floor
		{"zen3-video", 0, 0},                // n<=0 is free
		{"zen3-video", -1, 0},
	}
	for _, tc := range cases {
		if got := videoCostCents(tc.model, tc.n); got != tc.want {
			t.Errorf("videoCostCents(%q, %d) = %d, want %d", tc.model, tc.n, got, tc.want)
		}
	}

	// A video costs strictly more than an image (task invariant: per-video cost
	// is higher than image).
	if videoCostCents("zen3-video", 1) <= imageCostCents("zen3-image", 1) {
		t.Errorf("video per-unit (%d) must exceed image per-unit (%d)",
			videoCostCents("zen3-video", 1), imageCostCents("zen3-image", 1))
	}
}

// TestVideoNClampCeiling documents the money-safety invariant: the handler
// clamps n to [1,4] before any cost math (videoCostCents / reserveBudget), so
// the reserve/settle can never exceed the ceiling for 4 videos — never an
// unbounded n and never an int-overflow-to-negative reservation. The clamp is
// the same one enforced in model.GenerateVideoDOAI (doaiVideoMaxN).
func TestVideoNClampCeiling(t *testing.T) {
	const maxN = 4
	for model, per := range videoPricePerVideoCents {
		if got := videoCostCents(model, maxN); got != per*maxN {
			t.Errorf("videoCostCents(%q, %d) = %d, want %d (ceiling)", model, maxN, got, per*maxN)
		}
		if videoCostCents(model, maxN) <= 0 {
			t.Errorf("ceiling cost for %q must be positive", model)
		}
	}
}

// TestVideoResponseData maps the model result into the OpenAI images-shaped data
// array: b64_json (+ mime_type) for the downloaded MP4, and url only when an
// upstream ever returns a hosted URL.
func TestVideoResponseData(t *testing.T) {
	res := &model.VideoGenResult{Videos: []model.GeneratedVideo{
		{B64JSON: "AAAA", MimeType: "video/mp4"},
		{URL: "https://cdn/x.mp4"},
	}}
	data := videoResponseData(res)
	if len(data) != 2 {
		t.Fatalf("len(data) = %d, want 2", len(data))
	}
	if data[0]["b64_json"] != "AAAA" {
		t.Errorf("data[0].b64_json = %q", data[0]["b64_json"])
	}
	if data[0]["mime_type"] != "video/mp4" {
		t.Errorf("data[0].mime_type = %q", data[0]["mime_type"])
	}
	if _, hasURL := data[0]["url"]; hasURL {
		t.Errorf("data[0] must not carry url when only b64_json is set")
	}
	if data[1]["url"] != "https://cdn/x.mp4" {
		t.Errorf("data[1].url = %q", data[1]["url"])
	}
	if _, hasB64 := data[1]["b64_json"]; hasB64 {
		t.Errorf("data[1] must not carry b64_json when only url is set")
	}
}

// TestVideoUpstreamBase derives the /videos base from the provider URL via the
// single resolveEndpointForPath map, with no trailing slash so the client's
// "/videos" join is clean.
func TestVideoUpstreamBase(t *testing.T) {
	cases := []struct {
		name string
		prov object.Provider
		want string
	}{
		{"do-ai", object.Provider{Type: "DigitalOcean", ProviderUrl: "https://inference.do-ai.run/v1"}, "https://inference.do-ai.run/v1"},
		{"do-ai-no-v1", object.Provider{Type: "DigitalOcean", ProviderUrl: "https://inference.do-ai.run"}, "https://inference.do-ai.run/v1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.prov
			if got := videoUpstreamBase(&p); got != tc.want {
				t.Errorf("videoUpstreamBase = %q, want %q", got, tc.want)
			}
		})
	}
}
