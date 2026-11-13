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
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	openai "github.com/hanzoai/go-openai"
)

// TestCrossModalChainsLive is the reproducible harness issue #130 asks for: it
// runs three real multi-step cross-modal chains against a live gateway, feeding
// each stage's actual output artifact into the next stage's input —
//
//	chain 1: text → /v1/images/generations → vision QA on /v1/chat/completions
//	chain 2: /v1/audio/speech → /v1/audio/transcriptions → LLM → /v1/audio/speech
//	chain 3: /v1/images/generations → /v1/videos/generations → poll → /content
//
// and fails with the exact status code and response payload of the hop that
// broke. It is skipped unless HANZO_API_KEY is set, so CI never runs it and no
// key leaks into logs (only sizes, media types and artifact shapes are printed):
//
//	HANZO_API_KEY=sk-… go test ./controllers/ -run TestCrossModalChainsLive -v -timeout 420s
//
// Model names default to the zen family each endpoint serves and are overridable
// per stage: HANZO_CHAT_MODEL (zen3-nano), HANZO_VISION_MODEL (zen3-vl),
// HANZO_IMAGE_MODEL (zen3-image), HANZO_VIDEO_MODEL (zen3-video),
// HANZO_TTS_MODEL (zen-voice-mini), HANZO_STT_MODEL (zen-scribe). The gateway
// base is HANZO_BASE_URL, default https://api.hanzo.ai/v1.
func TestCrossModalChainsLive(t *testing.T) {
	key := os.Getenv("HANZO_API_KEY")
	if key == "" {
		t.Skip("HANZO_API_KEY not set — skipping the live cross-modal chain harness")
	}
	base := strings.TrimSuffix(os.Getenv("HANZO_BASE_URL"), "/")
	if base == "" {
		base = "https://api.hanzo.ai/v1"
	}
	env := func(name, def string) string {
		if v := os.Getenv(name); v != "" {
			return v
		}
		return def
	}
	chatModel, visionModel := env("HANZO_CHAT_MODEL", "zen3-nano"), env("HANZO_VISION_MODEL", "zen3-vl")
	imageModel, videoModel := env("HANZO_IMAGE_MODEL", "zen3-image"), env("HANZO_VIDEO_MODEL", "zen3-video")
	ttsModel, sttModel := env("HANZO_TTS_MODEL", "zen-voice-mini"), env("HANZO_STT_MODEL", "zen-scribe")

	call := func(t *testing.T, method, path string, ct string, body []byte) (int, http.Header, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, base+path, bytes.NewReader(body))
		if err != nil {
			t.Fatalf("%s %s: build request: %v", method, path, err)
		}
		req.Header.Set("Authorization", "Bearer "+key)
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		resp, err := (&http.Client{Timeout: 150 * time.Second}).Do(req)
		if err != nil {
			t.Fatalf("%s %s: transport failure: %v", method, path, err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header, out
	}

	// ask posts JSON and requires 200 with a non-empty assistant answer.
	ask := func(t *testing.T, model, prompt string, parts ...openai.ChatMessagePart) string {
		t.Helper()
		msg := openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser}
		if len(parts) == 0 {
			msg.Content = prompt
		} else {
			msg.MultiContent = append([]openai.ChatMessagePart{{Type: openai.ChatMessagePartTypeText, Text: prompt}}, parts...)
		}
		reqBody, _ := json.Marshal(openai.ChatCompletionRequest{Model: model, Messages: []openai.ChatCompletionMessage{msg}})
		status, _, out := call(t, http.MethodPost, "/chat/completions", "application/json", reqBody)
		if status != http.StatusOK {
			t.Fatalf("chat(%s) = HTTP %d, want 200. payload=%s", model, status, truncate(out))
		}
		var resp openai.ChatCompletionResponse
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Fatalf("chat(%s) 200 body does not parse: %v. payload=%s", model, err, truncate(out))
		}
		if len(resp.Choices) == 0 || strings.TrimSpace(resp.Choices[0].Message.Content) == "" {
			t.Fatalf("chat(%s) answered an empty completion — silent degradation. payload=%s", model, truncate(out))
		}
		return resp.Choices[0].Message.Content
	}

	// genImage posts the images request and returns the artifact in whatever form
	// it actually arrived in (the response_format the gateway ignores is pinned offline).
	genImage := func(t *testing.T, prompt string) (dataURL, kind string) {
		t.Helper()
		reqBody, _ := json.Marshal(map[string]any{"model": imageModel, "prompt": prompt, "n": 1})
		status, _, out := call(t, http.MethodPost, "/images/generations", "application/json", reqBody)
		if status != http.StatusOK {
			t.Fatalf("images/generations = HTTP %d, want 200. payload=%s", status, truncate(out))
		}
		var img struct {
			Created int64 `json:"created"`
			Data    []struct {
				URL     string `json:"url"`
				B64JSON string `json:"b64_json"`
			} `json:"data"`
		}
		if err := json.Unmarshal(out, &img); err != nil || len(img.Data) == 0 {
			t.Fatalf("images body unreadable as the OpenAI shape: %v. payload=%s", err, truncate(out))
		}
		switch {
		case img.Data[0].B64JSON != "":
			raw, derr := base64.StdEncoding.DecodeString(img.Data[0].B64JSON)
			if derr != nil {
				t.Fatalf("b64_json is not valid base64 (encoding failure at the image→vision hop): %v", derr)
			}
			t.Logf("image artifact: b64_json, %d decoded bytes", len(raw))
			return "data:image/png;base64," + img.Data[0].B64JSON, "b64_json"
		case img.Data[0].URL != "":
			t.Logf("image artifact: url %s", img.Data[0].URL)
			return img.Data[0].URL, "url"
		}
		t.Fatalf("images 200 carried neither url nor b64_json — handoff impossible. payload=%s", truncate(out))
		return "", ""
	}

	speak := func(t *testing.T, text string) ([]byte, string) {
		t.Helper()
		reqBody, _ := json.Marshal(map[string]any{"model": ttsModel, "input": text, "voice": "alloy"})
		status, hdr, out := call(t, http.MethodPost, "/audio/speech", "application/json", reqBody)
		if status != http.StatusOK {
			t.Fatalf("audio/speech = HTTP %d, want 200. payload=%s", status, truncate(out))
		}
		mime := hdr.Get("Content-Type")
		if !strings.HasPrefix(mime, "audio/") {
			t.Errorf("audio/speech answered Content-Type %q, want audio/* — metadata handoff broken. payload=%s", mime, truncate(out))
		}
		if len(out) < 1024 {
			t.Errorf("audio/speech returned only %d bytes — suspiciously short for %q spoken", len(out), text)
		}
		t.Logf("tts artifact: %s, %d bytes", mime, len(out))
		return out, mime
	}

	// ── Chain 1: Text → Image → Vision QA ─────────────────────────────────────
	t.Run("Text_Image_VisionQA", func(t *testing.T) {
		refined := ask(t, chatModel, "Reply with ONE short image prompt for a photo of a red fox in snow. No quotes.")
		artifact, _ := genImage(t, refined)
		answer := ask(t, visionModel, "Describe this image in one short sentence.",
			openai.ChatMessagePart{Type: openai.ChatMessagePartTypeImageURL, ImageURL: &openai.ChatMessageImageURL{URL: artifact}})
		t.Logf("vision QA answer: %q", truncate([]byte(answer)))
	})

	// ── Chain 2: Audio → Transcription → LLM → TTS ────────────────────────────
	t.Run("Speech_STT_LLM_TTS", func(t *testing.T) {
		const sentence = "The capital of France is Paris."
		mp3, _ := speak(t, sentence)

		var form bytes.Buffer
		mw := multipart.NewWriter(&form)
		part, err := mw.CreateFormFile("file", "speech.mp3")
		if err != nil {
			t.Fatalf("multipart file: %v", err)
		}
		if _, err := part.Write(mp3); err != nil {
			t.Fatalf("multipart write: %v", err)
		}
		_ = mw.WriteField("model", sttModel)
		_ = mw.Close()

		status, _, out := call(t, http.MethodPost, "/audio/transcriptions", mw.FormDataContentType(), form.Bytes())
		if status != http.StatusOK {
			t.Fatalf("audio/transcriptions = HTTP %d, want 200. audio=%d bytes payload=%s", status, len(mp3), truncate(out))
		}
		var heard openai.AudioResponse
		if err := json.Unmarshal(out, &heard); err != nil || heard.Text == "" {
			t.Fatalf("transcription body unreadable or empty: %v. payload=%s", err, truncate(out))
		}
		if !strings.Contains(strings.ToLower(heard.Text), "paris") {
			t.Errorf("transcription lost the spoken fact — want to hear \"Paris\", heard %q (payload=%s)", heard.Text, truncate(out))
		}
		t.Logf("stt artifact: %q", heard.Text)

		answer := ask(t, chatModel, "Name only the city mentioned here: "+heard.Text)
		speak(t, answer)
	})

	// ── Chain 3: Text → Image → Video (async lifecycle) ───────────────────────
	t.Run("Text_Image_Video", func(t *testing.T) {
		_, kind := genImage(t, "a red fox trotting through deep snow, cinematic wide shot")
		createBody, _ := json.Marshal(map[string]any{"model": videoModel, "prompt": "a red fox trotting through deep snow, cinematic wide shot", "seconds": 4})
		status, _, out := call(t, http.MethodPost, "/videos/generations", "application/json", createBody)
		if status != http.StatusOK {
			t.Fatalf("videos/generations = HTTP %d, want 200. payload=%s", status, truncate(out))
		}
		var job struct {
			ID       string `json:"id"`
			Object   string `json:"object"`
			Status   string `json:"status"`
			Progress int    `json:"progress"`
		}
		if err := json.Unmarshal(out, &job); err != nil || job.ID == "" {
			t.Fatalf("video create body unreadable (no job id): %v. payload=%s", err, truncate(out))
		}
		if job.Object != "video" {
			t.Errorf("video object = %q, want \"video\" — metadata handoff broken. payload=%s", job.Object, truncate(out))
		}
		t.Logf("video job %s created (%s image artifact: %s)", job.ID, kind, job.Status)

		deadline := time.Now().Add(300 * time.Second)
		for job.Status != "completed" && job.Status != "failed" {
			if time.Now().After(deadline) {
				t.Fatalf("video job %s never settled within 300s — last: %s", job.ID, truncate(out))
			}
			time.Sleep(10 * time.Second)
			status, _, out = call(t, http.MethodGet, "/videos/"+job.ID, "", nil)
			if status != http.StatusOK {
				t.Fatalf("GET /videos/%s = HTTP %d, want 200. payload=%s", job.ID, status, truncate(out))
			}
			if err := json.Unmarshal(out, &job); err != nil {
				t.Fatalf("video poll body unreadable: %v. payload=%s", err, truncate(out))
			}
		}
		if job.Status == "failed" {
			var withErr struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			_ = json.Unmarshal(out, &withErr)
			t.Fatalf("video job failed: %s. payload=%s", withErr.Error.Message, truncate(out))
		}

		status, hdr, out := call(t, http.MethodGet, "/videos/"+job.ID+"/content", "", nil)
		if status != http.StatusOK {
			t.Fatalf("GET /videos/%s/content = HTTP %d, want 200. payload=%s", job.ID, status, truncate(out))
		}
		if !strings.HasPrefix(hdr.Get("Content-Type"), "video/") || len(out) < 10_000 {
			t.Errorf("downloaded video is Content-Type %q with %d bytes — want real video/* bytes", hdr.Get("Content-Type"), len(out))
		}
		fmt.Fprintf(os.Stderr, "video artifact: %s, %d bytes\n", hdr.Get("Content-Type"), len(out))
	})
}

// truncate keeps a failure payload quotable in a test log without flooding it.
func truncate(b []byte) string {
	s := strings.ReplaceAll(string(b), "\n", " ")
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}
