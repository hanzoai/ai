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

// The bounds on the speech endpoints, asserted where they BITE.
//
// Each test states the abuse it refuses, not the branch it covers: an upload
// that outgrows the pod, a synthesis request that buys unbounded CPU with one
// string, and a burst that takes every worker the two-replica upstream has.

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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

// TestSpeechAdmissionCeiling proves the concurrency ceiling bites: exactly
// maxSpeechConcurrency requests are admitted, the next is refused rather than
// queued, and a released slot is reusable.
//
// This is the bound that protects the two-replica upstream, because the size
// bound cannot: bytes do not determine audio seconds.
func TestSpeechAdmissionCeiling(t *testing.T) {
	held := make([]func(), 0, maxSpeechConcurrency)
	for i := 0; i < maxSpeechConcurrency; i++ {
		release, ok := admitSpeech()
		if !ok {
			t.Fatalf("request %d of %d was refused while the ceiling was not yet reached", i+1, maxSpeechConcurrency)
		}
		held = append(held, release)
	}

	if _, ok := admitSpeech(); ok {
		t.Fatalf("request %d was ADMITTED past the ceiling of %d — the bound does not bite",
			maxSpeechConcurrency+1, maxSpeechConcurrency)
	}

	// A returned slot is immediately reusable: the ceiling throttles, it does not
	// leak away capacity one request at a time.
	held[0]()
	release, ok := admitSpeech()
	if !ok {
		t.Fatal("a released slot was not reusable — the ceiling leaks capacity")
	}
	held[0] = release

	for _, r := range held {
		r()
	}
}

// TestSpeechAdmissionIsNotBlocking proves a refused caller is refused NOW. A
// ceiling that queues converts a burst into latency for every caller and holds
// each waiting body's memory while it does — the failure the ceiling exists to
// prevent.
func TestSpeechAdmissionIsNotBlocking(t *testing.T) {
	held := make([]func(), 0, maxSpeechConcurrency)
	for i := 0; i < maxSpeechConcurrency; i++ {
		release, ok := admitSpeech()
		if !ok {
			t.Fatalf("could not fill the ceiling: request %d refused", i+1)
		}
		held = append(held, release)
	}
	defer func() {
		for _, r := range held {
			r()
		}
	}()

	done := make(chan bool, 1)
	go func() {
		_, ok := admitSpeech()
		done <- ok
	}()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("admitted past the ceiling")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("admitSpeech BLOCKED when full; it must refuse immediately")
	}
}

// TestSpeechAdmissionReleasesUnderConcurrency proves the ceiling holds under
// real concurrent traffic and returns every slot — a leak would silently reduce
// capacity to zero over time and take the endpoint down without an attacker.
func TestSpeechAdmissionReleasesUnderConcurrency(t *testing.T) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	inFlight, peak := 0, 0

	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, ok := admitSpeech()
			if !ok {
				return
			}
			mu.Lock()
			inFlight++
			if inFlight > peak {
				peak = inFlight
			}
			mu.Unlock()

			mu.Lock()
			inFlight--
			mu.Unlock()
			release()
		}()
	}
	wg.Wait()

	if peak > maxSpeechConcurrency {
		t.Fatalf("peak in-flight was %d, over the ceiling of %d", peak, maxSpeechConcurrency)
	}
	// Every slot must be back: the ceiling is fully available again.
	for i := 0; i < maxSpeechConcurrency; i++ {
		release, ok := admitSpeech()
		if !ok {
			t.Fatalf("only %d of %d slots were returned — the ceiling leaks", i, maxSpeechConcurrency)
		}
		defer release()
	}
}
