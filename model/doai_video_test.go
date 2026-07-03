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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestIsDOAIVideoModel gates which models take the async /v1/videos path. The
// do-ai upstream id and the zen3-video brand family match; chat/image/embedding
// models must NOT.
func TestIsDOAIVideoModel(t *testing.T) {
	yes := []string{"wan2-2-t2v-a14b", "WAN2-2-T2V-A14B", "zen3-video", "zen3-video-fast", "zen3-video-pro"}
	for _, m := range yes {
		if !isDOAIVideoModel(m) {
			t.Errorf("isDOAIVideoModel(%q) = false, want true", m)
		}
	}
	no := []string{
		"gpt-4o", "claude-opus-4-8", "zen5", "zen3-omni", "zen3-image",
		"zen3-image-sdxl", "fal-ai/flux/schnell", "stable-diffusion-3.5-large",
		"dall-e-3", "zen3-embedding", "qwen3-coder", "deepseek-chat",
	}
	for _, m := range no {
		if isDOAIVideoModel(m) {
			t.Errorf("isDOAIVideoModel(%q) = true, want false", m)
		}
	}
}

// TestGetOpenAiModelType_VideoFamily proves the video models resolve to
// videosGenerations while image and chat models keep their own kind (no
// cross-classification).
func TestGetOpenAiModelType_VideoFamily(t *testing.T) {
	videos := []string{"wan2-2-t2v-a14b", "zen3-video", "zen3-video-fast", "zen3-video-pro"}
	for _, m := range videos {
		if got := getOpenAiModelType(m); got != "videosGenerations" {
			t.Errorf("getOpenAiModelType(%q) = %q, want videosGenerations", m, got)
		}
	}
	// Images stay images; chat stays chat — the video gate must not swallow them.
	if got := getOpenAiModelType("zen3-image"); got != "imagesGenerations" {
		t.Errorf("getOpenAiModelType(zen3-image) = %q, want imagesGenerations", got)
	}
	for _, m := range []string{"gpt-4o", "zen5", "claude-opus-4-8", "qwen3-coder"} {
		if got := getOpenAiModelType(m); got != "Chat" {
			t.Errorf("getOpenAiModelType(%q) = %q, want Chat (video gate must not swallow it)", m, got)
		}
	}
}

// TestGenerateVideoDOAI_Validation rejects an empty prompt / model without any
// network call (fail-fast at the boundary).
func TestGenerateVideoDOAI_Validation(t *testing.T) {
	if _, err := GenerateVideoDOAI(context.Background(), "https://x/v1", "k", VideoGenRequest{UpstreamModel: "wan2-2-t2v-a14b"}); err == nil {
		t.Errorf("empty prompt must error")
	}
	if _, err := GenerateVideoDOAI(context.Background(), "https://x/v1", "k", VideoGenRequest{Prompt: "a fox"}); err == nil {
		t.Errorf("empty upstream model must error")
	}
}

// TestGenerateVideoDOAI_Lifecycle drives the full create → poll → download
// lifecycle against an httptest server that mimics DO's /v1/videos API, proving
// the client speaks the discovered contract: POST /videos returns an id, GET
// /videos/{id} transitions to completed, GET /videos/{id}/content returns MP4
// bytes the client base64-encodes. It also asserts the Authorization header
// carries the key and is never placed in the URL.
func TestGenerateVideoDOAI_Lifecycle(t *testing.T) {
	const wantKey = "test-secret-key"
	mp4 := []byte("\x00\x00\x00\x18ftypmp42FAKEMP4BYTES")
	var polls int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+wantKey {
			t.Errorf("Authorization = %q, want Bearer %s", got, wantKey)
		}
		if strings.Contains(r.URL.RawQuery, wantKey) || strings.Contains(r.URL.Path, wantKey) {
			t.Errorf("key must never appear in URL: %s", r.URL.String())
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/videos":
			var body doaiVideoCreateBody
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Model != "wan2-2-t2v-a14b" || body.Prompt == "" {
				t.Errorf("create body = %+v, want model+prompt set", body)
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"video_abc","object":"video","status":"queued"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/videos/video_abc":
			polls++
			st := "in_progress"
			if polls >= 2 {
				st = "completed"
			}
			_, _ = w.Write([]byte(`{"id":"video_abc","model":"wan2-2-t2v-a14b","object":"video","status":"` + st + `","output":null,"error":null}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/videos/video_abc/content":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(mp4)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	res, err := GenerateVideoDOAI(context.Background(), srv.URL+"/v1", wantKey, VideoGenRequest{
		UpstreamModel: "wan2-2-t2v-a14b",
		Prompt:        "a red fox in snow",
		N:             1,
	})
	if err != nil {
		t.Fatalf("GenerateVideoDOAI: %v", err)
	}
	if len(res.Videos) != 1 {
		t.Fatalf("len(videos) = %d, want 1", len(res.Videos))
	}
	got := res.Videos[0]
	if got.MimeType != "video/mp4" {
		t.Errorf("mime = %q, want video/mp4", got.MimeType)
	}
	if got.B64JSON != base64.StdEncoding.EncodeToString(mp4) {
		t.Errorf("b64 mismatch: got %q", got.B64JSON)
	}
	if polls < 2 {
		t.Errorf("expected the client to poll until completed, polls=%d", polls)
	}
}

// TestGenerateVideoDOAI_ContentNotReady treats a JSON body from /content (the
// upstream placeholder returned before the job is truly done) as an error, never
// handing the caller a base64-encoded error document as if it were a video.
func TestGenerateVideoDOAI_ContentNotReady(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"v1","object":"video","status":"queued"}`))
		case strings.HasSuffix(r.URL.Path, "/content"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"in_progress"}`))
		default:
			_, _ = w.Write([]byte(`{"id":"v1","object":"video","status":"completed","error":null}`))
		}
	}))
	defer srv.Close()

	_, err := GenerateVideoDOAI(context.Background(), srv.URL, "k", VideoGenRequest{
		UpstreamModel: "wan2-2-t2v-a14b", Prompt: "x", N: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("want content-not-ready error, got %v", err)
	}
}

// TestGenerateVideoDOAI_UpstreamFailure surfaces a failed job status as an error
// (never a silent success), including the upstream reason.
func TestGenerateVideoDOAI_UpstreamFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"v1","object":"video","status":"queued"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"v1","object":"video","status":"failed","error":{"message":"content policy","type":"invalid_request_error"}}`))
	}))
	defer srv.Close()

	_, err := GenerateVideoDOAI(context.Background(), srv.URL, "k", VideoGenRequest{
		UpstreamModel: "wan2-2-t2v-a14b", Prompt: "x", N: 1,
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "failed") {
		t.Fatalf("want failure error, got %v", err)
	}
}

// TestVideoUpstreamError_RateLimit annotates a 429 as retryable so clients back
// off rather than treating it as a hard failure.
func TestVideoUpstreamError_RateLimit(t *testing.T) {
	err := videoUpstreamError("video create", http.StatusTooManyRequests, []byte(`{"message":"Rate limit exceeded."}`))
	if err == nil || !strings.Contains(err.Error(), "retry") {
		t.Fatalf("429 must be annotated retryable, got %v", err)
	}
}
