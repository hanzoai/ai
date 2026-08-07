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

package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestOpenAIQueryAudio asserts the provider posts the OpenAI JSON shape
// (model + input + voice + response_format) to <root>/audio/speech with the
// bearer secret, and returns the audio bytes verbatim.
func TestOpenAIQueryAudio(t *testing.T) {
	audio := []byte("ID3\x04\x00fake-mp3-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/speech" {
			t.Errorf("path = %q, want /audio/speech", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("authorization = %q, want Bearer sk-test", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("content-type = %q, want application/json", got)
		}
		var req speechRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if req.Model != "kokoro" {
			t.Errorf("model = %q, want kokoro", req.Model)
		}
		if req.Input != "hello there" {
			t.Errorf("input = %q, want hello there", req.Input)
		}
		if req.Voice != "af_heart" {
			t.Errorf("voice = %q, want af_heart", req.Voice)
		}
		if req.ResponseFormat != "mp3" {
			t.Errorf("response_format = %q, want mp3", req.ResponseFormat)
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write(audio)
	}))
	defer srv.Close()

	// The provider URL is the /v1 root; the provider appends the path. The
	// trailing slash must not double.
	p, err := NewOpenAITextToSpeechProvider("kokoro", "sk-test", srv.URL+"/", "af_heart")
	if err != nil {
		t.Fatal(err)
	}
	got, result, err := p.QueryAudio("hello there", context.Background(), "en")
	if err != nil {
		t.Fatalf("QueryAudio: %v", err)
	}
	if !bytes.Equal(got, audio) {
		t.Errorf("audio = %q, want %q", got, audio)
	}
	if result == nil || result.TokenCount != len("hello there") {
		t.Errorf("result = %+v, want TokenCount %d", result, len("hello there"))
	}
}

// TestOpenAIQueryAudioNoSecret asserts an unauthenticated upstream (the
// in-cluster speech service is a ClusterIP with no auth) is called with NO
// Authorization header rather than an empty bearer, which some servers reject.
func TestOpenAIQueryAudioNoSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Header["Authorization"]; ok {
			t.Errorf("Authorization header sent with an empty secret: %q", r.Header.Get("Authorization"))
		}
		w.Write([]byte("audio"))
	}))
	defer srv.Close()

	p, err := NewOpenAITextToSpeechProvider("kokoro", "", srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.QueryAudio("hi", context.Background(), "en"); err != nil {
		t.Fatalf("QueryAudio: %v", err)
	}
}

// TestOpenAIQueryAudioUpstreamError asserts a non-200 upstream is surfaced as an
// error carrying the status + body, never silent empty audio.
func TestOpenAIQueryAudioUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"voice not found"}`, http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	p, err := NewOpenAITextToSpeechProvider("kokoro", "", srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = p.QueryAudio("hi", context.Background(), "en")
	if err == nil {
		t.Fatal("upstream 503 returned no error")
	}
	if !strings.Contains(err.Error(), "503") || !strings.Contains(err.Error(), "voice not found") {
		t.Errorf("error = %q, want status + body", err)
	}
}

// TestOpenAIQueryAudioEmptyBody asserts a 200 carrying no bytes is an error.
// The handlers treat empty audio as a failure; catching it here keeps the
// provider from reporting success for a body nobody can play.
func TestOpenAIQueryAudioEmptyBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p, _ := NewOpenAITextToSpeechProvider("kokoro", "", srv.URL, "")
	if _, _, err := p.QueryAudio("hi", context.Background(), "en"); err == nil {
		t.Fatal("empty 200 body returned no error")
	}
}

// TestOpenAIQueryAudioStream asserts the stream variant writes the same bytes
// QueryAudio returns.
func TestOpenAIQueryAudioStream(t *testing.T) {
	audio := []byte("ID3streamed")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(audio)
	}))
	defer srv.Close()

	p, _ := NewOpenAITextToSpeechProvider("kokoro", "", srv.URL, "")
	var buf bytes.Buffer
	if _, err := p.QueryAudioStream("hi", context.Background(), io.Writer(&buf), "en"); err != nil {
		t.Fatalf("QueryAudioStream: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), audio) {
		t.Errorf("streamed = %q, want %q", buf.Bytes(), audio)
	}
}

// TestOpenAIRequiresURL asserts a provider row with no URL is refused at
// construction (a misconfigured row must fail loudly, not at first use).
func TestOpenAIRequiresURL(t *testing.T) {
	if _, err := NewOpenAITextToSpeechProvider("kokoro", "sk", "", ""); err == nil {
		t.Fatal("empty provider URL accepted")
	}
}

// TestFactoryServesOpenAIType is the regression guard for the gap that made
// /v1/audio/speech unreachable for every OpenAI-compatible upstream: the factory
// knew only "Alibaba Cloud", so any other type fell through as (nil, nil) and
// the caller reported "the TTS provider type: X is not supported". STT had its
// OpenAI branch; TTS did not.
func TestFactoryServesOpenAIType(t *testing.T) {
	p, err := GetTextToSpeechProvider("OpenAI", "kokoro", "", "", "http://speech.hanzo.svc/v1", "", 0, "USD", "af_heart", "en")
	if err != nil {
		t.Fatalf("GetTextToSpeechProvider(OpenAI): %v", err)
	}
	if p == nil {
		t.Fatal("GetTextToSpeechProvider(OpenAI) returned nil — the OpenAI-compatible upstream is unreachable")
	}
}
