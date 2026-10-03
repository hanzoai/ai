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
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/ai/object"
)

var scribeSeq atomic.Int64

// speechFake is the speech service: it records what each request asked for and
// answers a file that says LONG the way the service answers audio past max_seconds.
type speechFake struct {
	mu   sync.Mutex
	asks []map[string]string
}

func (f *speechFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseMultipartForm(1 << 20)
	file, _, _ := r.FormFile("file")
	data, _ := io.ReadAll(file)
	f.mu.Lock()
	f.asks = append(f.asks, map[string]string{
		"path": r.URL.Path, "model": r.FormValue("model"), "max_seconds": r.FormValue("max_seconds"),
		"language": r.FormValue("language"),
	})
	f.mu.Unlock()
	if string(data) == "LONG" {
		http.Error(w, `{"error":"the audio runs past 60 s"}`, http.StatusRequestEntityTooLarge)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"text":"What is the capital of France?","duration":2.4}`)
}

// scribeLane stands up the lane against the fake: the speech provider row points
// at it, and the lane is open at daily transcriptions a visitor.
func scribeLane(t *testing.T, daily int, provider *object.Provider) *speechFake {
	t.Helper()
	restore, err := object.UseMemoryDB(fmt.Sprintf("file:scribe_%d?mode=memory&cache=shared", scribeSeq.Add(1)), &object.Provider{})
	if err != nil {
		t.Fatalf("UseMemoryDB: %v", err)
	}
	t.Cleanup(restore)
	fake := &speechFake{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	if provider == nil {
		provider = &object.Provider{Owner: "admin", Name: "speech", Category: "Model", Type: "OpenAI", State: "Active"}
	}
	provider.ProviderUrl = srv.URL + "/v1"
	if _, err := object.AddProvider(provider); err != nil {
		t.Fatalf("seed speech provider: %v", err)
	}
	object.InvalidateProviderNameCache("")
	t.Cleanup(func() { object.InvalidateProviderNameCache("") })
	t.Setenv("PUBLIC_SCRIBE_DAILY", fmt.Sprint(daily))
	publicScribes.mu.Lock()
	publicScribes.day, publicScribes.seen = "", nil
	publicScribes.mu.Unlock()
	return fake
}

// dictate posts audio to the lane from addr, as a visitor with no credential.
func dictate(t *testing.T, addr, audio string, fields map[string]string) (int, string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fw, _ := w.CreateFormFile("file", "turn.webm")
	_, _ = fw.Write([]byte(audio))
	for k, v := range fields {
		_ = w.WriteField(k, v)
	}
	_ = w.Close()
	c := from(visit(http.MethodPost, "/v1/audio/transcriptions/public"), addr)
	c.Fiber().Request().Header.SetContentType(w.FormDataContentType())
	c.Fiber().Request().SetBody(body.Bytes())
	status := answering(t, c, c.AudioTranscriptionsPublic)
	return status, sent(c)
}

// A visitor dictates on our own transcriber, held to a minute, within a day's
// allowance — and whatever model they name, they get the one the lane assigns.
func TestAVisitorDictatesWithinTheirDay(t *testing.T) {
	fake := scribeLane(t, 2, nil)

	status, body := dictate(t, "203.0.113.7:1234", "opus", map[string]string{"model": "whisper-1", "language": "en"})
	if status != http.StatusOK || !strings.Contains(body, "capital of France") {
		t.Fatalf("first dictation = %d %s", status, body)
	}
	ask := fake.asks[0]
	if ask["path"] != "/v1/audio/transcriptions" || ask["model"] != "parakeet" || ask["max_seconds"] != "60" || ask["language"] != "en" {
		t.Fatalf("the speech service was asked %v; want parakeet held to 60 s", ask)
	}

	if status, _ := dictate(t, "203.0.113.7:1234", "opus", nil); status != http.StatusOK {
		t.Fatalf("second dictation = %d", status)
	}
	status, body = dictate(t, "203.0.113.7:1234", "opus", nil)
	if status != http.StatusTooManyRequests || !strings.Contains(body, "public_allowance_spent") {
		t.Fatalf("a third dictation on a day of two = %d %s; want 429", status, body)
	}
	if len(fake.asks) != 2 {
		t.Fatalf("the spent visitor reached the transcriber: %d calls", len(fake.asks))
	}
	// Another visitor has a day of their own.
	if status, _ := dictate(t, "198.51.100.9:1234", "opus", nil); status != http.StatusOK {
		t.Fatalf("another visitor = %d", status)
	}
}

// Audio past the minute is refused by the speech service, and the refusal costs
// the visitor nothing.
func TestTooLongIsRefusedAndNotCounted(t *testing.T) {
	scribeLane(t, 1, nil)
	status, body := dictate(t, "203.0.113.8:1234", "LONG", nil)
	if status != http.StatusRequestEntityTooLarge || !strings.Contains(body, "public_audio_too_long") {
		t.Fatalf("over-long audio = %d %s; want 413", status, body)
	}
	if status, _ := dictate(t, "203.0.113.8:1234", "opus", nil); status != http.StatusOK {
		t.Fatalf("the refusal spent the visitor's day: %d", status)
	}
}

// Closed by default, and closed to a caller with no address.
func TestTheScribeLaneIsClosedUntilOpened(t *testing.T) {
	scribeLane(t, 0, nil)
	if status, _ := dictate(t, "203.0.113.9:1234", "opus", nil); status != http.StatusNotFound {
		t.Fatalf("a closed lane answered %d; want 404", status)
	}
	t.Setenv("PUBLIC_SCRIBE_DAILY", "5")
	c := visit(http.MethodPost, "/v1/audio/transcriptions/public")
	if status := answering(t, c, c.AudioTranscriptionsPublic); status != http.StatusForbidden {
		t.Fatalf("a caller with no address answered %d; want 403", status)
	}
}

// The paid lane cannot be reached anonymously: were zen-scribe served by a vendor,
// the lane closes rather than spend on it.
func TestTheScribeLaneNeverReachesAVendor(t *testing.T) {
	fake := scribeLane(t, 5, &object.Provider{Owner: "admin", Name: "openai-direct", Category: "Model", Type: "OpenAI", State: "Active"})
	modelRoutes["zen-scribe"], modelRoutes["zen-scribe-was"] = modelRoute{providerName: "openai-direct", upstreamModel: "whisper-1"}, modelRoutes["zen-scribe"]
	t.Cleanup(func() {
		modelRoutes["zen-scribe"] = modelRoutes["zen-scribe-was"]
		delete(modelRoutes, "zen-scribe-was")
	})
	status, body := dictate(t, "203.0.113.10:1234", "opus", nil)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("a vendor-served zen-scribe answered %d %s; want 503", status, body)
	}
	if len(fake.asks) != 0 {
		t.Fatal("a stranger's audio reached a vendor")
	}
}
