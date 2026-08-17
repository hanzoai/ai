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
	"mime/multipart"
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

// The transcription form is read from the BODY, by parseTranscribeForm, and these
// pin what it takes off the wire. They used to drive a second reader that took an
// *http.Request; there is one reader now, and it takes the slice both transports
// already hold.

// TestTranscribeFormReadsFields pins the fields beside the audio. The endpoint
// once answered `requires a "model" field` to every request that carried one:
// net/http fills r.Form only inside ParseMultipartForm, the field was read before
// anything had parsed the body, and it came back empty. Reading the parse's own
// output is what makes that unrepresentable.
func TestTranscribeFormReadsFields(t *testing.T) {
	audio := []byte("RIFF....WAVEfmt ")
	body, _ := openAITranscribeBody(t, audio, map[string]string{
		"model":           "whisper",
		"language":        "de",
		"response_format": "text",
	})

	form, err := parseTranscribeForm(body.Bytes())
	if err != nil {
		t.Fatalf("parseTranscribeForm: %v", err)
	}
	if form.model != "whisper" {
		t.Errorf("model = %q, want whisper — a form field the OpenAI clients all send", form.model)
	}
	if form.language != "de" {
		t.Errorf("language = %q, want de", form.language)
	}
	if form.responseFormat != "text" {
		t.Errorf("responseFormat = %q, want text", form.responseFormat)
	}
	if form.audio == nil {
		t.Error("no audio part was read")
	}
}

// TestTranscribeFormFieldsSurviveMissingFile pins that the fields survive a body
// with NO file part, because the caller distinguishes "no file" from "no model"
// and reports a different 400 for each. A missing file that also blanked the model
// would report the wrong one.
//
// A body without a file is not an ERROR here — it parses, and says so by leaving
// audio nil. That is the caller's question to ask.
func TestTranscribeFormFieldsSurviveMissingFile(t *testing.T) {
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	if err := w.WriteField("model", "whisper"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	form, err := parseTranscribeForm(body.Bytes())
	if err != nil {
		t.Fatalf("a body with fields and no file did not parse: %v", err)
	}
	if form.audio != nil {
		t.Error("audio is not nil for a body carrying no file part")
	}
	if form.model != "whisper" {
		t.Errorf("model = %q, want whisper — the caller reports the FILE as missing, not the model", form.model)
	}
}

// TestTranscribeFormNonMultipart asserts a body that is not a multipart form is
// refused as one, rather than read as an empty one — which would report a missing
// file to a caller whose real mistake was the content type.
func TestTranscribeFormNonMultipart(t *testing.T) {
	_, err := parseTranscribeForm([]byte(`{"model":"whisper"}`))
	if err == nil {
		t.Fatal("a JSON body was accepted as a transcription form")
	}
	if !strings.Contains(err.Error(), "multipart") {
		t.Errorf("refusal %q does not say what was wrong with the body", err.Error())
	}
}

// The query-string spelling of a form field is GONE, and deliberately. net/http's
// ParseMultipartForm merged the URL query into r.Form, so `?model=whisper` beside
// a multipart body resolved — which was only ever reachable because the form-field
// read above was broken. Nothing crossing ZAP carries a query string, so that
// spelling could not work on both transports, and the OpenAI contract is a form
// field. The test that pinned it is deleted rather than moved.

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
