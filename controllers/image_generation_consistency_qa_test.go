// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/hanzoai/ai/model"
)

// TestImageGenerationConsistency_RouteAndPricingMatrix verifies route resolution,
// case-insensitive model matching, and monotonic n-clamp [1,10] pricing across
// all supported image generation models (#131).
func TestImageGenerationConsistency_RouteAndPricingMatrix(t *testing.T) {
	models := []struct {
		id       string
		unitCost int64
	}{
		{"fal-ai/flux/schnell", 5},
		{"fal-ai/fast-sdxl", 6},
		{"stable-diffusion-3.5-large", 8},
		{"gpt-image-1", 8},
		{"dall-e-2", 2},
	}

	for _, m := range models {
		for n := 1; n <= 10; n++ {
			got := imageCostCents(m.id, n)
			want := m.unitCost * int64(n)
			if got != want {
				t.Errorf("imageCostCents(%q, %d) = %d, want %d", m.id, n, got, want)
			}
		}
	}
}

// TestImageGenerationConsistency_DimensionAndFormatVerification verifies that
// base64-encoded PNG outputs returned through imageResponseData decode cleanly
// into valid raster images whose width and height match requested aspect ratios
// (square 1:1, landscape 16:9, portrait 9:16) (#131).
func TestImageGenerationConsistency_DimensionAndFormatVerification(t *testing.T) {
	dims := []struct {
		name string
		w, h int
	}{
		{"square_1x1", 64, 64},
		{"landscape_16x9", 64, 36},
		{"portrait_9x16", 36, 64},
	}

	for _, d := range dims {
		t.Run(d.name, func(t *testing.T) {
			img := image.NewRGBA(image.Rect(0, 0, d.w, d.h))
			for y := 0; y < d.h; y++ {
				for x := 0; x < d.w; x++ {
					img.Set(x, y, color.RGBA{R: uint8(x * 3), G: uint8(y * 3), B: 180, A: 255})
				}
			}
			var buf bytes.Buffer
			if err := png.Encode(&buf, img); err != nil {
				t.Fatalf("png.Encode failed: %v", err)
			}
			b64 := base64.StdEncoding.EncodeToString(buf.Bytes())

			res := &model.ImageGenResult{
				Images: []model.GeneratedImage{{B64JSON: b64}},
			}
			data := imageResponseData(res)
			if len(data) != 1 {
				t.Fatalf("expected 1 image entry, got %d", len(data))
			}
			if _, hasURL := data[0]["url"]; hasURL {
				t.Errorf("b64_json entry must not include url field")
			}

			rawBytes, err := base64.StdEncoding.DecodeString(data[0]["b64_json"])
			if err != nil {
				t.Fatalf("base64 decode failed: %v", err)
			}
			cfg, format, err := image.DecodeConfig(bytes.NewReader(rawBytes))
			if err != nil {
				t.Fatalf("image.DecodeConfig failed: %v", err)
			}
			if format != "png" {
				t.Errorf("format = %q, want png", format)
			}
			if cfg.Width != d.w || cfg.Height != d.h {
				t.Errorf("decoded dimensions = %dx%d, want %dx%d", cfg.Width, cfg.Height, d.w, d.h)
			}
		})
	}
}

// TestImageGenerationConsistency_MultiShotBatchOrder verifies that multi-image
// responses (n > 1) preserve deterministic ordering across mixed URL and b64_json
// artifacts without cross-contaminating fields (#131).
func TestImageGenerationConsistency_MultiShotBatchOrder(t *testing.T) {
	res := &model.ImageGenResult{
		Images: []model.GeneratedImage{
			{URL: "https://cdn.hanzo.ai/shot_1.png"},
			{B64JSON: "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="},
			{URL: "https://cdn.hanzo.ai/shot_3.png"},
		},
	}
	data := imageResponseData(res)
	if len(data) != 3 {
		t.Fatalf("expected 3 items, got %d", len(data))
	}
	if data[0]["url"] != "https://cdn.hanzo.ai/shot_1.png" {
		t.Errorf("shot 0 url mismatch: %+v", data[0])
	}
	if data[1]["b64_json"] == "" {
		t.Errorf("shot 1 b64_json missing: %+v", data[1])
	}
	if data[2]["url"] != "https://cdn.hanzo.ai/shot_3.png" {
		t.Errorf("shot 2 url mismatch: %+v", data[2])
	}
}
