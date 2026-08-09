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

// The SIZE bounds on the speech endpoints, asserted where they BITE: an upload
// that outgrows the pod, and a synthesis request that buys unbounded CPU with one
// string. Each test states the abuse it refuses, not the branch it covers.
//
// The bound on the WORK — who may run how much of it at once — is a different
// question and lives with the mechanism, in speech_admission_test.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/ai/object"
)

// transcribeBody builds an OpenAI transcription form carrying fileBytes of audio.
func transcribeBody(t *testing.T, fileBytes int) (body *bytes.Buffer, contentType string) {
	t.Helper()
	body = &bytes.Buffer{}
	w := multipart.NewWriter(body)
	fw, err := w.CreateFormFile("file", "audio.webm")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	// Written in chunks so the test does not itself allocate the whole payload twice.
	chunk := bytes.Repeat([]byte("A"), 1<<20)
	for written := 0; written < fileBytes; {
		n := len(chunk)
		if remaining := fileBytes - written; remaining < n {
			n = remaining
		}
		if _, err := fw.Write(chunk[:n]); err != nil {
			t.Fatalf("write audio: %v", err)
		}
		written += n
	}
	if err := w.WriteField("model", "whisper"); err != nil {
		t.Fatalf("WriteField: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return body, w.FormDataContentType()
}

// TestTranscribeUploadOverLimitRefused proves the upload bound bites: a body
// past MaxTranscribeUpload is reported as oversize, so the handler answers 413
// instead of spending the parse, the copies and the upstream call on it.
func TestTranscribeUploadOverLimitRefused(t *testing.T) {
	body, ctype := transcribeBody(t, MaxTranscribeUpload+(1<<20))
	r := httptest.NewRequest("POST", "/v1/audio/transcriptions", body)
	r.Header.Set("Content-Type", ctype)

	_, oversize, err := readTranscribeRequest(r)
	if !oversize {
		t.Fatalf("a %d MiB upload was accepted; the %d MiB bound did not bite (err=%v)",
			(MaxTranscribeUpload+(1<<20))>>20, MaxTranscribeUpload>>20, err)
	}
}

// TestTranscribeUploadAtLimitAccepted proves the bound is a CEILING and not an
// off-by-one that refuses the largest legitimate upload. This is the mutation
// that a `>=` in place of `>` would introduce, and the reason the reader takes
// one byte past the bound rather than exactly the bound.
func TestTranscribeUploadAtLimitAccepted(t *testing.T) {
	// A form whose TOTAL is just under the bound: audio plus the MIME framing.
	body, ctype := transcribeBody(t, MaxTranscribeUpload-(1<<16))
	if body.Len() > MaxTranscribeUpload {
		t.Fatalf("test fixture is %d bytes, over the %d bound", body.Len(), MaxTranscribeUpload)
	}
	r := httptest.NewRequest("POST", "/v1/audio/transcriptions", body)
	r.Header.Set("Content-Type", ctype)

	form, oversize, err := readTranscribeRequest(r)
	if oversize {
		t.Fatal("an upload UNDER the bound was refused as oversize")
	}
	if err != nil {
		t.Fatalf("readTranscribeRequest: %v", err)
	}
	if form.model != "whisper" {
		t.Errorf("model = %q, want whisper", form.model)
	}
	form.file.Close()
}

// TestSpeechInputOverLimitRefused drives the real ZAP speech handler with an
// input past MaxSpeechInput and proves it is refused 413 — before any upstream
// synthesis is dispatched. Synthesis cost is linear in this length, so this is
// the bound that makes a TTS request's cost knowable in advance.
func TestSpeechInputOverLimitRefused(t *testing.T) {
	body, err := json.Marshal(audioSpeechRequest{
		Model: "kokoro",
		Input: strings.Repeat("a", MaxSpeechInput+1),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	msgOut, hErr := zapAudioSpeechHandler(context.Background(), "Bearer probe", body)
	status, msg := cloudStatus(t, msgOut, hErr)
	if status != 413 {
		t.Fatalf("status = %d, want 413 for an input of %d chars over the %d limit (msg=%q)",
			status, MaxSpeechInput+1, MaxSpeechInput, msg)
	}
	if !strings.Contains(msg, "4096") {
		t.Errorf("refusal %q does not name the limit — a caller cannot act on it", msg)
	}
}

// TestSpeechInputAtLimitPasses proves the bound is a ceiling, not an off-by-one
// that refuses the largest legitimate request — the mutation a `>=` would
// introduce.
//
// The proof is that the request reaches the AUTH SEAM, which sits directly below
// the size gate: identity resolution reads a database this unit test has no
// business standing up, so it faults there. Faulting below the gate is
// admission; being refused 413 is not. The over-limit twin above never gets
// that far, and that difference is the assertion.
func TestSpeechInputAtLimitPasses(t *testing.T) {
	body, err := json.Marshal(audioSpeechRequest{
		Model: "kokoro",
		Input: strings.Repeat("a", MaxSpeechInput),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	admitted := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				admitted = true // faulted in identity resolution, below the gate
			}
		}()
		msgOut, hErr := zapAudioSpeechHandler(context.Background(), "Bearer probe", body)
		if hErr == nil && msgOut != nil {
			if msgOut.Root().Uint32(object.CloudRespStatus) != 413 {
				admitted = true
			}
		}
	}()

	if !admitted {
		t.Fatalf("an input of exactly %d chars was refused 413; the bound is off by one", MaxSpeechInput)
	}
}

// TestZapTranscribeOverLimitRefused drives the real ZAP transcribe handler with
// a body past MaxTranscribeUpload and proves it is refused 413 BEFORE the
// multipart parse — which is the point, since the parse is what turns one body
// into three more copies of it.
func TestZapTranscribeOverLimitRefused(t *testing.T) {
	body, _ := transcribeBody(t, MaxTranscribeUpload+(1<<20))
	msgOut, hErr := zapAudioTranscribeHandler(context.Background(), "Bearer probe", body.Bytes())
	status, msg := cloudStatus(t, msgOut, hErr)
	if status != 413 {
		t.Fatalf("status = %d, want 413 for a %d MiB body over the %d MiB limit (msg=%q)",
			status, body.Len()>>20, MaxTranscribeUpload>>20, msg)
	}
	if !strings.Contains(msg, "25") {
		t.Errorf("refusal %q does not name the limit", msg)
	}
}
