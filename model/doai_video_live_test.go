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
	"testing"
	"time"
)

// TestVideoDOAIAsync_Live is a REAL end-to-end text-to-video round-trip against
// the Sora-style async /v1/videos API, exercising the SAME three primitives the
// handler uses: CreateVideoDOAI → RetrieveVideoDOAI (poll) → DownloadVideoBytesDOAI.
// It is skipped unless DO_AI_API_KEY is set (the same credential the chat/image
// routes resolve), so it never runs — or leaks a key — in an environment without
// one (CI has none). Video generation is minutes-long, so run it explicitly where
// the key exists:
//
//	DO_AI_API_KEY=… go test ./model/ -run TestVideoDOAIAsync_Live -v -timeout 360s
//
// It proves create returns a job id immediately, the poll reaches "completed",
// and the download returns actual MP4 bytes. The key is never printed; only the
// resulting media size/type is logged.
func TestVideoDOAIAsync_Live(t *testing.T) {
	apiKey := os.Getenv("DO_AI_API_KEY")
	if apiKey == "" {
		t.Skip("DO_AI_API_KEY not set — skipping live async text-to-video round-trip")
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

	// CREATE — must return a job id immediately (no minutes-long block).
	createStart := time.Now()
	id, status, err := CreateVideoDOAI(ctx, base, apiKey, VideoGenRequest{
		UpstreamModel: upstream,
		Prompt:        "a red panda eating bamboo in a misty forest, cinematic short clip",
	})
	if err != nil {
		t.Fatalf("live CreateVideoDOAI: %v", err)
	}
	if id == "" {
		t.Fatal("create returned an empty job id")
	}
	if d := time.Since(createStart); d > 30*time.Second {
		t.Errorf("create took %s — async create must return promptly, not block on generation", d)
	}
	t.Logf("created job id=%s status=%s in %s", id, status, time.Since(createStart))

	// POLL — one status call per iteration, until terminal or the deadline.
	deadline := time.Now().Add(300 * time.Second)
	final := ""
	for {
		st, errMsg, err := RetrieveVideoDOAI(ctx, base, apiKey, id)
		if err != nil {
			t.Fatalf("live RetrieveVideoDOAI: %v", err)
		}
		switch st {
		case "completed", "complete", "succeeded", "success":
			final = "completed"
		case "failed", "error", "canceled", "cancelled":
			t.Fatalf("job failed: %s", errMsg)
		}
		if final != "" || time.Now().After(deadline) {
			break
		}
		time.Sleep(3 * time.Second)
	}
	if final != "completed" {
		t.Fatalf("job did not complete before deadline")
	}

	// DOWNLOAD — raw MP4 bytes.
	raw, mime, err := DownloadVideoBytesDOAI(ctx, base, apiKey, id)
	if err != nil {
		t.Fatalf("live DownloadVideoBytesDOAI: %v", err)
	}
	if len(raw) < 1000 {
		t.Fatalf("video too small (%d bytes) — not a real clip", len(raw))
	}
	// MP4 files carry an "ftyp" box at bytes [4:8].
	if len(raw) < 12 || string(raw[4:8]) != "ftyp" {
		t.Fatalf("not an MP4 (missing ftyp box)")
	}
	t.Logf("LIVE OK: mime=%s bytes=%d (real MP4, key never printed)", mime, len(raw))
}
