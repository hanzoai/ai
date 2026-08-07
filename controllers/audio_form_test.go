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
	"io"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"
)

// openAITranscribeBody builds the multipart body an OpenAI client sends: the
// audio as `file`, everything else as sibling FORM FIELDS.
func openAITranscribeBody(t *testing.T, audio []byte, fields map[string]string) (body *bytes.Buffer, contentType string) {
	t.Helper()
	body = &bytes.Buffer{}
	w := multipart.NewWriter(body)
	fw, err := w.CreateFormFile("file", "audio.wav")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(audio); err != nil {
		t.Fatal(err)
	}
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return body, w.FormDataContentType()
}

// TestReadTranscribeRequestReadsFormFields is the regression guard for the
// defect that made /v1/audio/transcriptions unusable by every standard client.
//
// `model` was read with beego's GetString one line ABOVE the file read. beego
// resolves GetString through r.Form, and for multipart/form-data Go fills r.Form
// only inside ParseMultipartForm — which the FILE read is what triggered. So the
// field read happened before anything had parsed the body and came back empty,
// and the endpoint answered `requires a "model" field` to a request carrying
// exactly that. Measured against production before the fix: a form-field model
// 400'd while the same call with ?model= in the query string went through.
func TestReadTranscribeRequestReadsFormFields(t *testing.T) {
	audio := []byte("RIFF....WAVEfmt ")
	body, ct := openAITranscribeBody(t, audio, map[string]string{
		"model":           "whisper",
		"language":        "de",
		"response_format": "text",
	})
	r := httptest.NewRequest("POST", "/v1/audio/transcriptions", body)
	r.Header.Set("Content-Type", ct)

	form, _, err := readTranscribeRequest(r)
	if err != nil {
		t.Fatalf("readTranscribeRequest: %v", err)
	}
	if form.model != "whisper" {
		t.Errorf("model = %q, want whisper — a form field the OpenAI clients all send", form.model)
	}
	if form.language != "de" {
		t.Errorf("language = %q, want de", form.language)
	}
	if form.responseFormat != "text" {
		t.Errorf("response_format = %q, want text", form.responseFormat)
	}
	if form.file == nil {
		t.Fatal("file part not read")
	}
	got, _ := io.ReadAll(form.file)
	if !bytes.Equal(got, audio) {
		t.Errorf("file bytes = %q, want %q", got, audio)
	}
}

// TestReadTranscribeRequestReadsQueryFields asserts the query-string spelling
// keeps working. ParseMultipartForm merges the URL query into r.Form, so a
// caller who passed ?model= (the only spelling that worked while the bug was
// live) is not broken by the fix.
func TestReadTranscribeRequestReadsQueryFields(t *testing.T) {
	body, ct := openAITranscribeBody(t, []byte("x"), nil)
	r := httptest.NewRequest("POST", "/v1/audio/transcriptions?model=whisper&language=fr", body)
	r.Header.Set("Content-Type", ct)

	form, _, err := readTranscribeRequest(r)
	if err != nil {
		t.Fatalf("readTranscribeRequest: %v", err)
	}
	if form.model != "whisper" {
		t.Errorf("model = %q, want whisper (from the query string)", form.model)
	}
	if form.language != "fr" {
		t.Errorf("language = %q, want fr (from the query string)", form.language)
	}
}

// TestReadTranscribeRequestFormFieldBeatsNothing pins that the fields survive a
// body with NO file part: the caller distinguishes "no file" from "no model",
// so a missing file must not also blank the model (which would report the wrong
// one of the two 400s).
func TestReadTranscribeRequestFieldsSurviveMissingFile(t *testing.T) {
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	if err := w.WriteField("model", "whisper"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/v1/audio/transcriptions", body)
	r.Header.Set("Content-Type", w.FormDataContentType())

	form, _, err := readTranscribeRequest(r)
	if err == nil {
		t.Fatal("a body with no file part returned no error")
	}
	if form.model != "whisper" {
		t.Errorf("model = %q, want whisper — the caller reports the FILE as missing, not the model", form.model)
	}
}

// TestReadTranscribeRequestNonMultipart asserts a body that is not a multipart
// form fails as a missing file rather than panicking on a nil r.Form.
func TestReadTranscribeRequestNonMultipart(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/audio/transcriptions", strings.NewReader(`{"model":"whisper"}`))
	r.Header.Set("Content-Type", "application/json")

	form, _, err := readTranscribeRequest(r)
	if err == nil {
		t.Fatal("a JSON body was accepted as a transcription form")
	}
	if form.model != "" {
		t.Errorf("model = %q, want empty", form.model)
	}
}

// TestAudioResponseLabelTellsTheTruth is the regression guard for the API
// lying about what it produced. /v1/audio/speech answered
// `Content-Type: audio/opus` carrying an MP3, because the relay hardcoded a
// request for mp3 while the handler labelled the response from the caller's
// REQUEST. Measured against production before the fix.
func TestAudioResponseLabelTellsTheTruth(t *testing.T) {
	// The exact production case: caller asked opus, upstream made mp3.
	ct, name := audioResponseLabel("audio/mpeg", "opus")
	if ct != "audio/mpeg" {
		t.Errorf("content type = %q, want audio/mpeg — the bytes are an MP3 whatever was asked for", ct)
	}
	if name != "speech.mp3" {
		t.Errorf("filename = %q, want speech.mp3", name)
	}

	// An upstream that honours the request is reported as itself.
	if ct, _ := audioResponseLabel("audio/wav", "wav"); ct != "audio/wav" {
		t.Errorf("content type = %q, want audio/wav", ct)
	}

	// A provider that reports nothing falls back to the requested format: it is
	// the only information available, and it was the whole contract before.
	if ct, _ := audioResponseLabel("", "wav"); ct != "audio/wav" {
		t.Errorf("fallback content type = %q, want audio/wav", ct)
	}
	if ct, _ := audioResponseLabel("", ""); ct != "audio/mpeg" {
		t.Errorf("default content type = %q, want audio/mpeg", ct)
	}
}
