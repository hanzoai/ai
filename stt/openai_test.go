// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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

package stt

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestOpenAIProcessAudio asserts the provider posts the OpenAI multipart shape
// (file + model + language + response_format=json) to <root>/audio/transcriptions
// with the bearer secret, and decodes the {"text"} body.
func TestOpenAIProcessAudio(t *testing.T) {
	payload := []byte("RIFF....WAVEfmt ")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/transcriptions" {
			t.Errorf("path = %q, want /audio/transcriptions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("authorization = %q, want Bearer sk-test", got)
		}
		if err := r.ParseMultipartForm(64 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		if got := r.FormValue("model"); got != "whisper" {
			t.Errorf("model = %q, want whisper", got)
		}
		if got := r.FormValue("language"); got != "de" {
			t.Errorf("language = %q, want de", got)
		}
		if got := r.FormValue("response_format"); got != "json" {
			t.Errorf("response_format = %q, want json", got)
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("file part: %v", err)
		}
		defer f.Close()
		got, _ := io.ReadAll(f)
		if !bytes.Equal(got, payload) {
			t.Errorf("file bytes = %q, want %q", got, payload)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"text":"guten tag","duration":1.5}`)
	}))
	defer srv.Close()

	// The provider URL is the /v1 root; the provider appends the path. The
	// trailing slash must not double.
	p, err := NewOpenAISpeechToTextProvider("whisper", "sk-test", srv.URL+"/", "de")
	if err != nil {
		t.Fatal(err)
	}
	text, result, err := p.ProcessAudio(bytes.NewReader(payload), context.Background(), "en")
	if err != nil {
		t.Fatalf("ProcessAudio: %v", err)
	}
	if text != "guten tag" {
		t.Errorf("text = %q, want guten tag", text)
	}
	if result == nil || result.AudioDurationSeconds != 1.5 {
		t.Errorf("result = %+v, want duration 1.5", result)
	}
}

// TestOpenAIProcessAudioUpstreamError asserts a non-200 upstream is surfaced as
// an error carrying the status + body, never a silent empty transcript.
func TestOpenAIProcessAudioUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"model overloaded"}`, http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	p, err := NewOpenAISpeechToTextProvider("whisper", "", srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = p.ProcessAudio(strings.NewReader("x"), context.Background(), "en")
	if err == nil {
		t.Fatal("upstream 503 returned no error")
	}
	if !strings.Contains(err.Error(), "503") || !strings.Contains(err.Error(), "overloaded") {
		t.Errorf("error = %q, want status + body", err)
	}
}

// TestOpenAIRequiresURL asserts a provider row with no URL is refused at
// construction (a misconfigured row must fail loudly, not at first use).
func TestOpenAIRequiresURL(t *testing.T) {
	if _, err := NewOpenAISpeechToTextProvider("whisper", "sk", "", ""); err == nil {
		t.Fatal("empty provider URL accepted")
	}
}
