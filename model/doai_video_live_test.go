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
	"encoding/base64"
	"os"
	"testing"
	"time"
)

// TestGenerateVideoDOAI_Live is a REAL end-to-end text-to-video round-trip
// against DigitalOcean's Sora-style /v1/videos API. It is skipped unless
// DO_AI_API_KEY is set (the same credential the chat/image routes resolve), so
// it never runs — or leaks a key — in an environment without one (CI has none).
// Video generation is minutes-long, so run it explicitly where the key exists:
//
//	DO_AI_API_KEY=… go test ./model/ -run TestGenerateVideoDOAI_Live -v -timeout 360s
//
// It proves create → poll → download returns actual MP4 bytes. The key is never
// printed; only the resulting media size/type is logged.
func TestGenerateVideoDOAI_Live(t *testing.T) {
	apiKey := os.Getenv("DO_AI_API_KEY")
	if apiKey == "" {
		t.Skip("DO_AI_API_KEY not set — skipping live do-ai text-to-video round-trip")
	}

	base := os.Getenv("DO_AI_BASE_URL")
	if base == "" {
		base = "https://inference.do-ai.run/v1"
	}
	upstream := os.Getenv("DO_AI_VIDEO_MODEL")
	if upstream == "" {
		upstream = "wan2-2-t2v-a14b"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 320*time.Second)
	defer cancel()

	res, err := GenerateVideoDOAI(ctx, base, apiKey, VideoGenRequest{
		UpstreamModel: upstream,
		Prompt:        "a red panda eating bamboo in a misty forest, cinematic short clip",
		N:             1,
	})
	if err != nil {
		t.Fatalf("live GenerateVideoDOAI: %v", err)
	}
	if len(res.Videos) != 1 {
		t.Fatalf("videos = %d, want 1", len(res.Videos))
	}
	v := res.Videos[0]

	raw, err := base64.StdEncoding.DecodeString(v.B64JSON)
	if err != nil {
		t.Fatalf("decode b64: %v", err)
	}
	if len(raw) < 1000 {
		t.Fatalf("video too small (%d bytes) — not a real clip", len(raw))
	}
	// MP4 files carry an "ftyp" box at bytes [4:8].
	if len(raw) < 12 || string(raw[4:8]) != "ftyp" {
		t.Fatalf("not an MP4 (missing ftyp box)")
	}
	t.Logf("LIVE OK: mime=%s bytes=%d (real MP4, key never printed)", v.MimeType, len(raw))
}
