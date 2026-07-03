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

// TestResolveModelRoute_ImageFamily proves every zen3-image model routes to
// do-ai's fal diffusion upstreams, is premium, and is owned_by hanzo (the
// upstream is never leaked).
func TestResolveModelRoute_ImageFamily(t *testing.T) {
	cases := []struct {
		input        string
		wantUpstream string
	}{
		{"zen3-image", "fal-ai/flux/schnell"},
		{"zen3-image-max", "fal-ai/flux/schnell"},
		{"zen3-image-fast", "fal-ai/flux/schnell"},
		{"zen3-image-dev", "fal-ai/flux/schnell"},
		{"zen3-image-playground", "fal-ai/flux/schnell"},
		{"zen3-image-jp", "fal-ai/flux/schnell"},
		{"zen3-image-sdxl", "fal-ai/fast-sdxl"},
		{"zen3-image-ssd", "fal-ai/fast-sdxl"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			route := resolveModelRoute(tc.input)
			if route == nil {
				t.Fatalf("resolveModelRoute(%q) = nil, want non-nil", tc.input)
			}
			if route.providerName != "do-ai" {
				t.Errorf("providerName = %q, want do-ai", route.providerName)
			}
			if route.upstreamModel != tc.wantUpstream {
				t.Errorf("upstreamModel = %q, want %q", route.upstreamModel, tc.wantUpstream)
			}
			if !route.premium {
				t.Errorf("%q should be premium", tc.input)
			}
			if route.ownedBy != "hanzo" {
				t.Errorf("ownedBy = %q, want hanzo", route.ownedBy)
			}
		})
	}
}

// TestResolveModelRoute_ImageFamilyCaseInsensitive: the family resolves
// case-insensitively like every other route.
func TestResolveModelRoute_ImageFamilyCaseInsensitive(t *testing.T) {
	route := resolveModelRoute("ZEN3-IMAGE")
	if route == nil || route.upstreamModel != "fal-ai/flux/schnell" {
		t.Fatalf("case-insensitive resolve failed: %+v", route)
	}
}

// TestImageCostCents bills per image by user-facing name AND upstream id, with a
// conservative floor for an unknown image model (never $0).
func TestImageCostCents(t *testing.T) {
	cases := []struct {
		model string
		n     int
		want  int64
	}{
		{"zen3-image", 1, 5},
		{"zen3-image", 4, 20},
		{"zen3-image-sdxl", 1, 6},
		{"zen3-image-sdxl", 2, 12},
		{"fal-ai/flux/schnell", 1, 5},
		{"fal-ai/fast-sdxl", 1, 6},
		{"gpt-image-1", 1, 8},
		{"dall-e-2", 1, 2},
		{"ZEN3-IMAGE-MAX", 1, 5},           // case-insensitive
		{"some-unknown-image-model", 1, 5}, // floor
		{"zen3-image", 0, 0},               // n<=0 is free
		{"zen3-image", -1, 0},
	}
	for _, tc := range cases {
		if got := imageCostCents(tc.model, tc.n); got != tc.want {
			t.Errorf("imageCostCents(%q, %d) = %d, want %d", tc.model, tc.n, got, tc.want)
		}
	}
}

// TestImageResponseData maps the model result into the OpenAI images data array:
// url when the upstream returned a url, b64_json when it returned bytes.
func TestImageResponseData(t *testing.T) {
	res := &model.ImageGenResult{Images: []model.GeneratedImage{
		{URL: "https://f/1.jpg"},
		{B64JSON: "aGVsbG8="},
	}}
	data := imageResponseData(res)
	if len(data) != 2 {
		t.Fatalf("len(data) = %d, want 2", len(data))
	}
	if data[0]["url"] != "https://f/1.jpg" {
		t.Errorf("data[0].url = %q", data[0]["url"])
	}
	if _, hasB64 := data[0]["b64_json"]; hasB64 {
		t.Errorf("data[0] must not carry b64_json when only url is set")
	}
	if data[1]["b64_json"] != "aGVsbG8=" {
		t.Errorf("data[1].b64_json = %q", data[1]["b64_json"])
	}
	if _, hasURL := data[1]["url"]; hasURL {
		t.Errorf("data[1] must not carry url when only b64_json is set")
	}
}

// TestImageUpstreamBase derives the async-invoke base from the provider URL via
// the single resolveEndpointForPath map, with no trailing slash so the client's
// "/async-invoke" join is clean.
func TestImageUpstreamBase(t *testing.T) {
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
			if got := imageUpstreamBase(&p); got != tc.want {
				t.Errorf("imageUpstreamBase = %q, want %q", got, tc.want)
			}
		})
	}
}
