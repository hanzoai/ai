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
	"sync/atomic"
	"testing"
)

// falImageServer is a faithful stand-in for DigitalOcean's async fal image API:
// submit → returns request_id; status → COMPLETE after a couple polls; retrieve
// → images[].url. It records the submitted body so tests can assert the exact
// contract (model_id, input.prompt, num_images) and Authorization header.
type falImageServer struct {
	srv         *httptest.Server
	polls       int32
	pollsToDone int32
	lastAuth    string
	lastBody    map[string]any
	imageURL    string
}

func newFalImageServer(pollsToDone int32, imageURL string) *falImageServer {
	f := &falImageServer{pollsToDone: pollsToDone, imageURL: imageURL}
	mux := http.NewServeMux()

	// Submit: POST /v1/async-invoke
	mux.HandleFunc("/v1/async-invoke", func(w http.ResponseWriter, r *http.Request) {
		// The retrieve/status paths are longer; this exact path is only the submit.
		if r.URL.Path != "/v1/async-invoke" {
			http.NotFound(w, r)
			return
		}
		f.lastAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&f.lastBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"request_id":"req-123","status":"QUEUED"}`))
	})

	// Status: GET /v1/async-invoke/{id}/status  — COMPLETE after pollsToDone polls.
	mux.HandleFunc("/v1/async-invoke/req-123/status", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&f.polls, 1)
		w.Header().Set("Content-Type", "application/json")
		if n >= f.pollsToDone {
			_, _ = w.Write([]byte(`{"status":"COMPLETE"}`))
		} else {
			_, _ = w.Write([]byte(`{"status":"IN_PROGRESS"}`))
		}
	})

	// Retrieve: GET /v1/async-invoke/{id}
	mux.HandleFunc("/v1/async-invoke/req-123", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"images":[{"content_type":"image/jpeg","url":"` + f.imageURL + `"}]}`))
	})

	f.srv = httptest.NewServer(mux)
	return f
}

func (f *falImageServer) close() { f.srv.Close() }
func (f *falImageServer) base() string {
	// The provider ProviderUrl is the …/v1 base.
	return f.srv.URL + "/v1"
}

// TestGenerateImageDOAI_HappyPath drives the full submit→poll→retrieve cycle and
// asserts the returned image URL, the exact submitted contract, and that the key
// rides the Authorization header.
func TestGenerateImageDOAI_HappyPath(t *testing.T) {
	f := newFalImageServer(2, "https://v3b.fal.media/files/test-image.jpg")
	defer f.close()

	res, err := GenerateImageDOAI(context.Background(), f.base(), "do-ai-secret", ImageGenRequest{
		UpstreamModel: "fal-ai/flux/schnell",
		Prompt:        "a red fox in snow",
		N:             1,
	})
	if err != nil {
		t.Fatalf("GenerateImageDOAI error: %v", err)
	}
	if len(res.Images) != 1 {
		t.Fatalf("got %d images, want 1", len(res.Images))
	}
	if res.Images[0].URL != "https://v3b.fal.media/files/test-image.jpg" {
		t.Fatalf("image url = %q", res.Images[0].URL)
	}

	// Contract: model_id + input.prompt + input.num_images submitted.
	if f.lastBody["model_id"] != "fal-ai/flux/schnell" {
		t.Errorf("submitted model_id = %v, want fal-ai/flux/schnell", f.lastBody["model_id"])
	}
	input, _ := f.lastBody["input"].(map[string]any)
	if input == nil || input["prompt"] != "a red fox in snow" {
		t.Errorf("submitted input.prompt = %v, want the prompt", input)
	}
	if input["num_images"].(float64) != 1 {
		t.Errorf("submitted input.num_images = %v, want 1", input["num_images"])
	}

	// The do-ai key must ride the Authorization header (never the URL/body).
	if f.lastAuth != "Bearer do-ai-secret" {
		t.Errorf("Authorization = %q, want Bearer do-ai-secret", f.lastAuth)
	}
}

// TestGenerateImageDOAI_MultipleImages proves num_images is honored and all
// returned images are surfaced.
func TestGenerateImageDOAI_MultipleImages(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/async-invoke", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/async-invoke" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		input := body["input"].(map[string]any)
		if input["num_images"].(float64) != 3 {
			t.Errorf("num_images = %v, want 3", input["num_images"])
		}
		_, _ = w.Write([]byte(`{"request_id":"req-123","status":"QUEUED"}`))
	})
	mux.HandleFunc("/v1/async-invoke/req-123/status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"COMPLETE"}`))
	})
	mux.HandleFunc("/v1/async-invoke/req-123", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"images":[{"url":"https://f/1.jpg"},{"url":"https://f/2.jpg"},{"url":"https://f/3.jpg"}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	res, err := GenerateImageDOAI(context.Background(), srv.URL+"/v1", "k", ImageGenRequest{
		UpstreamModel: "fal-ai/fast-sdxl",
		Prompt:        "three cats",
		N:             3,
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(res.Images) != 3 {
		t.Fatalf("got %d images, want 3", len(res.Images))
	}
}

// TestGenerateImageDOAI_FailedJob surfaces an upstream FAILED status as an error
// (never a silent/partial success).
func TestGenerateImageDOAI_FailedJob(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/async-invoke", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/async-invoke" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"request_id":"req-123","status":"QUEUED"}`))
	})
	mux.HandleFunc("/v1/async-invoke/req-123/status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"FAILED"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, err := GenerateImageDOAI(context.Background(), srv.URL+"/v1", "k", ImageGenRequest{
		UpstreamModel: "fal-ai/flux/schnell",
		Prompt:        "x",
	})
	if err == nil {
		t.Fatal("expected an error for a FAILED job, got nil")
	}
	if !strings.Contains(err.Error(), "failed") {
		t.Errorf("error = %q, want it to mention the failure", err.Error())
	}
}

// TestGenerateImageDOAI_SubmitError surfaces a non-2xx submit as an error.
func TestGenerateImageDOAI_SubmitError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"id":"Unauthorized","message":"Unable to authenticate you"}`))
	}))
	defer srv.Close()

	_, err := GenerateImageDOAI(context.Background(), srv.URL+"/v1", "bad", ImageGenRequest{
		UpstreamModel: "fal-ai/flux/schnell",
		Prompt:        "x",
	})
	if err == nil {
		t.Fatal("expected an error on a 401 submit, got nil")
	}
}

// TestGenerateImageDOAI_EmptyResult errors when the retrieve returns no images.
func TestGenerateImageDOAI_EmptyResult(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/async-invoke", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/async-invoke" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"request_id":"req-123"}`))
	})
	mux.HandleFunc("/v1/async-invoke/req-123/status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"COMPLETE"}`))
	})
	mux.HandleFunc("/v1/async-invoke/req-123", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"images":[]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, err := GenerateImageDOAI(context.Background(), srv.URL+"/v1", "k", ImageGenRequest{
		UpstreamModel: "fal-ai/flux/schnell",
		Prompt:        "x",
	})
	if err == nil {
		t.Fatal("expected an error when no images are returned, got nil")
	}
}

// TestGenerateImageDOAI_Validation rejects an empty prompt / model up front.
func TestGenerateImageDOAI_Validation(t *testing.T) {
	if _, err := GenerateImageDOAI(context.Background(), "http://x/v1", "k", ImageGenRequest{UpstreamModel: "fal-ai/flux/schnell"}); err == nil {
		t.Error("expected error for empty prompt")
	}
	if _, err := GenerateImageDOAI(context.Background(), "http://x/v1", "k", ImageGenRequest{Prompt: "hi"}); err == nil {
		t.Error("expected error for empty upstream model")
	}
}

// TestGetOpenAiModelType_ImageFamily proves the recognition gate: the whole
// zen3-image family and the fal upstream ids resolve to imagesGenerations, while
// chat models stay Chat (no over-broadening).
func TestGetOpenAiModelType_ImageFamily(t *testing.T) {
	images := []string{
		"zen3-image", "zen3-image-max", "zen3-image-fast", "zen3-image-dev",
		"zen3-image-playground", "zen3-image-ssd", "zen3-image-sdxl", "zen3-image-jp",
		"fal-ai/flux/schnell", "fal-ai/fast-sdxl",
		"dall-e-3", "dall-e-2", "gpt-image-1",
	}
	for _, m := range images {
		if got := getOpenAiModelType(m); got != "imagesGenerations" {
			t.Errorf("getOpenAiModelType(%q) = %q, want imagesGenerations", m, got)
		}
	}

	chat := []string{
		"gpt-4o", "gpt-5", "claude-opus-4-8", "zen5", "zen4-coder", "zen3-omni",
		"zen3-vl", "zen3-nano", "deepseek-chat", "qwen3-coder", "llama-3.3-70b",
		// zen3-embedding must NOT be an image model.
		"zen3-embedding",
	}
	for _, m := range chat {
		if got := getOpenAiModelType(m); got != "Chat" {
			t.Errorf("getOpenAiModelType(%q) = %q, want Chat (image gate must not swallow it)", m, got)
		}
	}
}

// TestIsDOAIImageModel gates which image models take the async fal path vs the
// OpenAI SDK path.
func TestIsDOAIImageModel(t *testing.T) {
	for _, m := range []string{"fal-ai/flux/schnell", "fal-ai/fast-sdxl", "zen3-image", "zen3-image-sdxl", "ZEN3-IMAGE-MAX"} {
		if !isDOAIImageModel(m) {
			t.Errorf("isDOAIImageModel(%q) = false, want true", m)
		}
	}
	for _, m := range []string{"gpt-image-1", "dall-e-3", "dall-e-2", "gpt-4o"} {
		if isDOAIImageModel(m) {
			t.Errorf("isDOAIImageModel(%q) = true, want false", m)
		}
	}
}

// TestCalculateOpenAIModelPrice_ImageFamily proves the model-layer price call no
// longer errors for the fal/zen3-image family and bills per image.
func TestCalculateOpenAIModelPrice_ImageFamily(t *testing.T) {
	cases := []struct {
		model     string
		count     int
		wantPrice float64
	}{
		{"zen3-image", 1, 0.05},
		{"fal-ai/flux/schnell", 2, 0.10},
		{"zen3-image-sdxl", 1, 0.06},
		{"fal-ai/fast-sdxl", 3, 0.18},
		{"gpt-image-1", 1, 0.08},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			mr := &ModelResult{ImageCount: tc.count}
			if err := CalculateOpenAIModelPrice(tc.model, mr, "en"); err != nil {
				t.Fatalf("CalculateOpenAIModelPrice(%q) error: %v", tc.model, err)
			}
			if mr.TotalPrice != tc.wantPrice {
				t.Errorf("price = %v, want %v", mr.TotalPrice, tc.wantPrice)
			}
			if mr.Currency != "USD" {
				t.Errorf("currency = %q, want USD", mr.Currency)
			}
		})
	}
}
