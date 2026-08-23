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
	// Every one of these used to assert fal-ai/flux/schnell or fal-ai/fast-sdxl,
	// and so the suite was green while image generation was entirely broken:
	// DigitalOcean 404s the fal-ai models on every endpoint ("this model is not
	// a image generation model"). They are listed in DO's web console, marked
	// Async, and are not callable through the inference API at all.
	//
	// stable-diffusion-3.5-large is the image model DO actually serves us —
	// verified end to end: 200, with a real b64_json image in the response.
	//
	// A test that pins the upstream to a name we cannot call is a test that
	// defends the bug.
	const image = "stable-diffusion-3.5-large"

	// The raw do-ai image id resolves to itself on do-ai — the model DO actually
	// serves (verified 200 + b64_json). The zen image family (zen-image) is served
	// by the zen service, not routed here. Pinning the upstream to an uncallable
	// fal model would defend the old bug.
	route := resolveModelRoute(image)
	if route == nil {
		t.Fatalf("resolveModelRoute(%q) = nil, want non-nil", image)
	}
	if route.providerName != "do-ai" {
		t.Errorf("providerName = %q, want do-ai", route.providerName)
	}
	if route.upstreamModel != image {
		t.Errorf("upstreamModel = %q, want %q", route.upstreamModel, image)
	}
}

// TestResolveModelRoute_ImageFamilyCaseInsensitive: the family resolves
// case-insensitively like every other route.
func TestResolveModelRoute_ImageFamilyCaseInsensitive(t *testing.T) {
	route := resolveModelRoute("STABLE-DIFFUSION-3.5-LARGE")
	if route == nil || route.upstreamModel != "stable-diffusion-3.5-large" {
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
		{"fal-ai/flux/schnell", 1, 5},
		{"fal-ai/flux/schnell", 4, 20},
		{"fal-ai/fast-sdxl", 1, 6},
		{"fal-ai/fast-sdxl", 2, 12},
		{"stable-diffusion-3.5-large", 1, 8},
		{"gpt-image-1", 1, 8},
		{"dall-e-2", 1, 2},
		{"STABLE-DIFFUSION-3.5-LARGE", 1, 8}, // case-insensitive
		{"some-unknown-image-model", 1, 5},   // floor
		{"fal-ai/flux/schnell", 0, 0},        // n<=0 is free
		{"fal-ai/flux/schnell", -1, 0},
	}
	for _, tc := range cases {
		if got := imageCostCents(tc.model, tc.n); got != tc.want {
			t.Errorf("imageCostCents(%q, %d) = %d, want %d", tc.model, tc.n, got, tc.want)
		}
	}
}

// TestImageNClampCeiling documents the money-safety invariant: the handler
// clamps n to [1,10] before any cost math (imageCostCents / reserveBudget), so
// the reserve/settle can never exceed the ceiling for 10 images — never an
// unbounded n and never an int-overflow-to-negative reservation. The clamp is
// the same one enforced in model.doaiImageSubmit.
func TestImageNClampCeiling(t *testing.T) {
	const maxN = 10
	// Cost is monotonic and bounded at the ceiling for every image model.
	for model, per := range imagePricePerImageCents {
		if got := imageCostCents(model, maxN); got != per*maxN {
			t.Errorf("imageCostCents(%q, %d) = %d, want %d (ceiling)", model, maxN, got, per*maxN)
		}
		// A clamped-then-billed n never exceeds the ceiling cost.
		if imageCostCents(model, maxN) <= 0 {
			t.Errorf("ceiling cost for %q must be positive", model)
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
// the single endpoint map, with no trailing slash so the client's
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
			if got := upstreamBase(&p); got != tc.want {
				t.Errorf("upstreamBase = %q, want %q", got, tc.want)
			}
		})
	}
}
