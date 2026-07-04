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

// TestCreateVideoDOAI_Validation rejects an empty prompt / model without any
// network call (fail-fast at the boundary).
func TestCreateVideoDOAI_Validation(t *testing.T) {
	if _, _, err := CreateVideoDOAI(context.Background(), "https://x/v1", "k", VideoGenRequest{UpstreamModel: "wan2-2-t2v-a14b"}); err == nil {
		t.Errorf("empty prompt must error")
	}
	if _, _, err := CreateVideoDOAI(context.Background(), "https://x/v1", "k", VideoGenRequest{Prompt: "a fox"}); err == nil {
		t.Errorf("empty upstream model must error")
	}
}

// TestVideoDOAIAsync_Lifecycle drives the full ASYNC create → poll → download
// lifecycle against an httptest server that mimics the Sora-style /v1/videos API,
// proving each primitive speaks the contract: CreateVideoDOAI returns an id +
// initial status; RetrieveVideoDOAI reports the status transition; and
// DownloadVideoBytesDOAI returns the raw MP4 bytes (NOT base64 — the handler
// streams them). It also asserts the Authorization header carries the key and the
// key is NEVER placed in the URL.
func TestVideoDOAIAsync_Lifecycle(t *testing.T) {
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

	// CREATE
	id, status, err := CreateVideoDOAI(context.Background(), srv.URL+"/v1", wantKey, VideoGenRequest{
		UpstreamModel: "wan2-2-t2v-a14b",
		Prompt:        "a red fox in snow",
	})
	if err != nil {
		t.Fatalf("CreateVideoDOAI: %v", err)
	}
	if id != "video_abc" {
		t.Fatalf("create id = %q, want video_abc", id)
	}
	if status != "queued" {
		t.Errorf("create status = %q, want queued", status)
	}

	// POLL — the first poll is in_progress, the second completed.
	st1, _, err := RetrieveVideoDOAI(context.Background(), srv.URL+"/v1", wantKey, id)
	if err != nil {
		t.Fatalf("RetrieveVideoDOAI #1: %v", err)
	}
	if st1 != "in_progress" {
		t.Errorf("poll #1 status = %q, want in_progress", st1)
	}
	st2, _, err := RetrieveVideoDOAI(context.Background(), srv.URL+"/v1", wantKey, id)
	if err != nil {
		t.Fatalf("RetrieveVideoDOAI #2: %v", err)
	}
	if st2 != "completed" {
		t.Errorf("poll #2 status = %q, want completed", st2)
	}

	// DOWNLOAD — raw MP4 bytes, not base64.
	data, mime, err := DownloadVideoBytesDOAI(context.Background(), srv.URL+"/v1", wantKey, id)
	if err != nil {
		t.Fatalf("DownloadVideoBytesDOAI: %v", err)
	}
	if mime != "video/mp4" {
		t.Errorf("mime = %q, want video/mp4", mime)
	}
	if string(data) != string(mp4) {
		t.Errorf("downloaded bytes mismatch")
	}
	if polls < 2 {
		t.Errorf("expected two polls, got %d", polls)
	}
}

// TestRetrieveVideoDOAI_Failed proves a failed job is surfaced as status "failed"
// with the (scrubbed) upstream reason — NOT as a transport error (the async
// retrieve reports terminal states in-band so the handler can bill nothing and
// project the reason).
func TestRetrieveVideoDOAI_Failed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"v1","object":"video","status":"failed","error":{"message":"content policy","type":"invalid_request_error"}}`))
	}))
	defer srv.Close()

	status, errMsg, err := RetrieveVideoDOAI(context.Background(), srv.URL, "k", "v1")
	if err != nil {
		t.Fatalf("RetrieveVideoDOAI transport error: %v", err)
	}
	if status != "failed" {
		t.Errorf("status = %q, want failed", status)
	}
	if !strings.Contains(errMsg, "content policy") {
		t.Errorf("errMsg = %q, want it to carry the reason", errMsg)
	}
}

// TestDownloadVideoBytesDOAI_ContentNotReady treats a JSON body from /content (the
// upstream placeholder returned before the job is truly done) as an error, never
// handing the caller a JSON error document as if it were a video.
func TestDownloadVideoBytesDOAI_ContentNotReady(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"in_progress"}`))
	}))
	defer srv.Close()

	_, _, err := DownloadVideoBytesDOAI(context.Background(), srv.URL, "k", "v1")
	if err == nil || !strings.Contains(err.Error(), "not video media") {
		t.Fatalf("want content-not-ready error, got %v", err)
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

// videoContentServer returns an httptest server that serves the given
// content-type + body for the /content download path. It is the shared rig for
// the download-path tests below.
func videoContentServer(t *testing.T, contentType string, body []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		_, _ = w.Write(body)
	}))
}

// TestDownloadVideoBytesDOAI_ContentCapEnforced proves the hard memory ceiling: an
// upstream /content response one byte past doaiVideoContentMax is rejected, never
// buffered. This is the per-request half of the OOM defense (the pod-wide half is
// the handler's download concurrency semaphore).
func TestDownloadVideoBytesDOAI_ContentCapEnforced(t *testing.T) {
	oversized := make([]byte, doaiVideoContentMax+1) // one byte past the hard ceiling
	srv := videoContentServer(t, "video/mp4", oversized)
	defer srv.Close()

	_, _, err := DownloadVideoBytesDOAI(context.Background(), srv.URL, "k", "v1")
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("want content-cap 'exceeds' error, got %v", err)
	}
}

// TestDownloadVideoBytesDOAI_ContentTypeAllowlist proves /content is accepted ONLY
// for video media, a generic binary stream, or a missing header — an allowlist —
// and every other explicit type (JSON placeholder, HTML/text error page) is
// rejected rather than returned as a "clip".
func TestDownloadVideoBytesDOAI_ContentTypeAllowlist(t *testing.T) {
	mp4 := []byte("\x00\x00\x00\x18ftypmp42REALBYTES")

	accept := []struct{ name, ct, wantMime string }{
		{"mp4", "video/mp4", "video/mp4"},
		{"webm", "video/webm", "video/webm"},
		{"octet-stream", "application/octet-stream", "video/mp4"},
		{"video-with-charset-param", "video/mp4; charset=binary", "video/mp4"},
		{"missing-header", "", "video/mp4"},
	}
	for _, tc := range accept {
		t.Run("accept/"+tc.name, func(t *testing.T) {
			srv := videoContentServer(t, tc.ct, mp4)
			defer srv.Close()
			data, mime, err := DownloadVideoBytesDOAI(context.Background(), srv.URL, "k", "v1")
			if err != nil {
				t.Fatalf("content-type %q must be accepted, got %v", tc.ct, err)
			}
			if mime != tc.wantMime {
				t.Fatalf("mime = %q, want %q", mime, tc.wantMime)
			}
			if string(data) != string(mp4) {
				t.Fatalf("downloaded bytes mismatch for %q", tc.ct)
			}
		})
	}

	reject := []struct{ name, ct string }{
		{"json-placeholder", "application/json"},
		{"html-error-page", "text/html"},
		{"plain-text", "text/plain"},
	}
	for _, tc := range reject {
		t.Run("reject/"+tc.name, func(t *testing.T) {
			srv := videoContentServer(t, tc.ct, []byte("this is not a video"))
			defer srv.Close()
			_, _, err := DownloadVideoBytesDOAI(context.Background(), srv.URL, "k", "v1")
			if err == nil || !strings.Contains(err.Error(), "not video media") {
				t.Fatalf("content-type %q must be rejected, got %v", tc.ct, err)
			}
		})
	}
}

// TestVideoSafeReason_ScrubsSecrets proves credential-shaped substrings an
// upstream error body might echo (Authorization header, Bearer token, sk-/hk- API
// key) are redacted before they can reach the client, while a benign reason is
// passed through unchanged (no over-redaction).
func TestVideoSafeReason_ScrubsSecrets(t *testing.T) {
	cases := []struct{ name, in, secret string }{
		{"bearer-token", `{"error":"bad key: Bearer sk-live-ABCDEF123456"}`, "sk-live-ABCDEF123456"},
		{"authorization-header", `unexpected: Authorization: Bearer zzzTOKENzzz here`, "zzzTOKENzzz"},
		{"sk-key-inline", `invalid api key sk-proj-DEADBEEFcafe1234 rejected`, "sk-proj-DEADBEEFcafe1234"},
		{"hk-key-inline", `bad hanzo key hk-abcdef012345 nope`, "hk-abcdef012345"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := videoSafeReason([]byte(tc.in))
			if strings.Contains(got, tc.secret) {
				t.Errorf("videoSafeReason leaked secret %q in %q", tc.secret, got)
			}
			if !strings.Contains(got, "[redacted]") {
				t.Errorf("expected [redacted] marker in %q", got)
			}
		})
	}
	if got := videoSafeReason([]byte("content policy violation")); got != "content policy violation" {
		t.Errorf("benign reason altered (over-redaction): %q", got)
	}
}

// TestVideoUpstreamError_AuthClassNoBody proves an auth-class upstream status
// (401/403) NEVER echoes the response body — which could contain the sent
// credential — surfacing only the status. Non-auth statuses still carry a short,
// scrubbed reason.
func TestVideoUpstreamError_AuthClassNoBody(t *testing.T) {
	body := []byte(`{"error":"invalid Authorization: Bearer sk-SECRETLEAK123456"}`)
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		err := videoUpstreamError("video create", status, body)
		if err == nil {
			t.Fatalf("status %d must error", status)
		}
		if strings.Contains(err.Error(), "sk-SECRETLEAK123456") || strings.Contains(err.Error(), "Bearer") {
			t.Errorf("status %d must not echo upstream body: %q", status, err.Error())
		}
		if !strings.Contains(err.Error(), "video create") {
			t.Errorf("status %d error should name the stage: %q", status, err.Error())
		}
	}
	// A non-auth status still surfaces a reason, but scrubbed.
	e := videoUpstreamError("video status poll", http.StatusBadGateway, body)
	if strings.Contains(e.Error(), "sk-SECRETLEAK123456") {
		t.Errorf("non-auth status must scrub secrets from the reason: %q", e.Error())
	}
}
