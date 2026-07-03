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

package model

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestGenerateImageDOAI_Live is a REAL end-to-end image round-trip against
// DigitalOcean's fal image API. It is skipped unless DO_AI_API_KEY is set (the
// same credential the chat routes resolve), so it never runs — or leaks a key —
// in an environment without one. Run it where the do-ai key exists:
//
//	DO_AI_API_KEY=… go test ./model/ -run TestGenerateImageDOAI_Live -v
//
// It proves submit → poll → retrieve returns an actual image URL. The key is
// never printed; only the resulting (public) fal.media URL is logged.
func TestGenerateImageDOAI_Live(t *testing.T) {
	apiKey := os.Getenv("DO_AI_API_KEY")
	if apiKey == "" {
		t.Skip("DO_AI_API_KEY not set — skipping live do-ai image round-trip")
	}

	base := os.Getenv("DO_AI_BASE_URL")
	if base == "" {
		base = "https://inference.do-ai.run/v1"
	}

	model := os.Getenv("DO_AI_IMAGE_MODEL")
	if model == "" {
		model = "fal-ai/flux/schnell"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 130*time.Second)
	defer cancel()

	res, err := GenerateImageDOAI(ctx, base, apiKey, ImageGenRequest{
		UpstreamModel: model,
		Prompt:        "a photorealistic red fox sitting in fresh snow at golden hour",
		N:             1,
	})
	if err != nil {
		t.Fatalf("live image generation failed: %v", err)
	}
	if len(res.Images) == 0 {
		t.Fatal("live image generation returned no images")
	}
	img := res.Images[0]
	if img.URL == "" && img.B64JSON == "" {
		t.Fatal("live image has neither url nor b64_json")
	}
	if img.URL != "" {
		if !strings.HasPrefix(img.URL, "http") {
			t.Fatalf("image url is not an http(s) URL: %q", img.URL)
		}
		t.Logf("LIVE image URL (%s): %s", model, img.URL)
	} else {
		t.Logf("LIVE image returned %d base64 bytes (%s)", len(img.B64JSON), model)
	}
}
