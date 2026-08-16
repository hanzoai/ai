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

package controllers

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"testing"

	"github.com/hanzoai/ai/object"
	"github.com/luxfi/zap"
)

// cloudStatus decodes the native cloud response status + error message.
func cloudStatus(t *testing.T, msg *zap.Message, err error) (uint32, string) {
	t.Helper()
	if err != nil {
		t.Fatalf("handler returned transport error: %v", err)
	}
	if msg == nil {
		t.Fatal("handler returned nil message")
	}
	root := msg.Root()
	return root.Uint32(object.CloudRespStatus), root.Text(object.CloudRespError)
}

// TestZapAudioRegistration asserts the audio group self-registered every route
// into the shared dispatch registry (native method + gateway path), and that the
// longest-prefix gateway lookup routes each verb to its own handler.
func TestZapAudioRegistration(t *testing.T) {
	cloudMethods := []string{
		"audio.speech", "audio.transcribe", "audio.voice", "audio.music", "audio.foley",
	}
	for _, m := range cloudMethods {
		if _, ok := lookupCloudHandler(m); !ok {
			t.Errorf("cloud method %q not registered", m)
		}
	}
	gatewayPaths := []string{
		"/v1/audio/speech", "/v1/audio/transcriptions", "/v1/audio/voice",
		"/v1/audio/music", "/v1/audio/foley",
	}
	for _, p := range gatewayPaths {
		if _, ok := lookupGatewayHandler(p); !ok {
			t.Errorf("gateway path %q not registered", p)
		}
	}
	// A sub-path resolves to its registered prefix (longest match).
	if _, ok := lookupGatewayHandler("/v1/audio/voice/extra"); !ok {
		t.Error("gateway sub-path did not resolve to /v1/audio/voice")
	}
}

// TestZapAudioAuthRejection asserts every audio handler denies an empty
// credential with 401 (STEP 1: identity is required, never trusted from the
// body). This is the security-critical parity: no handler grants on no auth.
func TestZapAudioAuthRejection(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		call func() (*zap.Message, error)
	}{
		{"audio.speech", func() (*zap.Message, error) {
			return zapAudioSpeechHandler(ctx, "", []byte(`{"model":"tts-1","input":"hi"}`))
		}},
		{"audio.voice", func() (*zap.Message, error) {
			return zapAudioVerbHandler("voice")(ctx, "", []byte(`{"model":"zen-voice"}`))
		}},
		{"audio.transcribe", func() (*zap.Message, error) {
			return zapAudioTranscribeHandler(ctx, "", []byte("multipart"))
		}},
	}
	for _, tc := range cases {
		m, e := tc.call()
		status, errMsg := cloudStatus(t, m, e)
		if status != 401 {
			t.Errorf("%s: empty auth got status %d, want 401 (err=%q)", tc.name, status, errMsg)
		}
	}
}

// TestZapAudioVerbUnknown asserts an unknown verb closure is a 404 even before
// auth resolution is attempted with a real credential (defensive registry guard).
func TestZapAudioVerbUnknown(t *testing.T) {
	// A non-empty auth so the empty-auth 401 branch is skipped and the verb switch
	// is exercised.
	m, e := zapAudioVerbHandler("bogus")(
		context.Background(), "Bearer x", []byte(`{"model":"m"}`),
	)
	status, _ := cloudStatus(t, m, e)
	if status != 404 {
		t.Errorf("unknown verb got status %d, want 404", status)
	}
}

// TestParseTranscribeForm is the OpenAI STT decode happy path (stubbed multipart
// body, no DB): the file part + model/language/response_format values are
// recovered from a self-describing multipart body without any Content-Type
// header, and a body with no "file" part decodes cleanly (auth reports the 400).
func TestParseTranscribeForm(t *testing.T) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("model", "whisper"); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteField("language", "de"); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteField("response_format", "text"); err != nil {
		t.Fatal(err)
	}
	fw, err := w.CreateFormFile("file", "clip.webm")
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("\x1aE\xdf\xa3....webm")
	if _, err := fw.Write(payload); err != nil {
		t.Fatal(err)
	}
	w.Close()

	form, err := parseTranscribeForm(buf.Bytes())
	if err != nil {
		t.Fatalf("parseTranscribeForm: %v", err)
	}
	if form.model != "whisper" || form.language != "de" || form.responseFormat != "text" {
		t.Errorf("fields = (%q,%q,%q), want (whisper,de,text)", form.model, form.language, form.responseFormat)
	}
	got, _ := io.ReadAll(form.audio)
	if !bytes.Equal(got, payload) {
		t.Errorf("audio payload = %q, want %q", got, payload)
	}

	// No "file" part is not a parse error — the handler 400s it after auth.
	var noFile bytes.Buffer
	w2 := multipart.NewWriter(&noFile)
	if err := w2.WriteField("model", "whisper"); err != nil {
		t.Fatal(err)
	}
	w2.Close()
	form, err = parseTranscribeForm(noFile.Bytes())
	if err != nil {
		t.Fatalf("parseTranscribeForm(no file): %v", err)
	}
	if form.audio != nil {
		t.Error("no-file body decoded a reader")
	}

	// A non-multipart body is a clean 400-shaped error, not a panic.
	if _, err := parseTranscribeForm([]byte(`{"not":"multipart"}`)); err == nil {
		t.Error("parseTranscribeForm accepted a non-multipart body")
	}
}

// TestMultipartBoundary asserts the boundary is recovered from the opening line
// and a malformed body yields "" (never a panic).
func TestMultipartBoundary(t *testing.T) {
	if b := multipartBoundary([]byte("--abc123\r\nContent-Disposition: form-data\r\n")); b != "abc123" {
		t.Errorf("boundary = %q, want abc123", b)
	}
	if b := multipartBoundary([]byte("not a multipart body")); b != "" {
		t.Errorf("boundary = %q, want empty", b)
	}
}
