// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package audio

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSpeechBodyWireShape(t *testing.T) {
	full := SpeechBody(SpeechModel, "hello", "nova", "opus", 1.3)
	for key, want := range map[string]any{
		"model": SpeechModel, "input": "hello", "voice": "nova",
		"response_format": "opus", "speed": 1.3,
	} {
		if got := full[key]; got != want {
			t.Errorf("field %q = %v, want %v", key, got, want)
		}
	}
	minimal := SpeechBody(SpeechModel, "hello", "", "", 0)
	if len(minimal) != 2 {
		t.Errorf("defaults must be omitted, not sent as empty/zero: %v", minimal)
	}
}

// speechServer serves canned audio at /v1/audio/speech and echoes what it
// received so each test can assert on the exact wire shape.
func speechServer(t *testing.T, audio []byte, capture func(map[string]any, http.Header)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/speech" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("speech body is not JSON: %v", err)
		}
		if capture != nil {
			capture(body, r.Header)
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write(audio)
	}))
}

func TestSpeechClient(t *testing.T) {
	want := mkMP3(10, 10)
	var got map[string]any
	var hdr http.Header
	srv := speechServer(t, want, func(b map[string]any, h http.Header) { got, hdr = b, h })
	defer srv.Close()

	c := &Client{Base: srv.URL + "/v1", Key: "k-123", HTTP: srv.Client()}
	reply, err := c.Speech(context.Background(), SpeechModel, "speak this", "", MP3, 0)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Status != http.StatusOK || string(reply.Body) != string(want) {
		t.Fatalf("reply = %d %d bytes, want 200 and the canned audio", reply.Status, len(reply.Body))
	}
	if got["model"] != SpeechModel || got["input"] != "speak this" || got["response_format"] != "mp3" {
		t.Errorf("server saw body %v", got)
	}
	if _, hasVoice := got["voice"]; hasVoice {
		t.Error(`empty voice must not appear in the body`)
	}
	if hdr.Get("Authorization") != "Bearer k-123" {
		t.Errorf("Authorization = %q, want the bearer token", hdr.Get("Authorization"))
	}
}

func TestSpeechStreamedReassembles(t *testing.T) {
	want := Sine(440, 1.5) // wav bytes — the reassembly must survive 97-byte reads
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		// Write in awkward pieces; flushing is what a streaming response does.
		for off := 0; off < len(want); off += 97 {
			end := off + 97
			if end > len(want) {
				end = len(want)
			}
			w.Write(want[off:end])
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
		}
	}))
	defer srv.Close()

	c := &Client{Base: srv.URL + "/v1", HTTP: srv.Client()}
	reply, err := c.SpeechStreamed(context.Background(), SpeechModel, "stream me", WAV, 97)
	if err != nil {
		t.Fatal(err)
	}
	if string(reply.Body) != string(want) {
		t.Fatalf("chunked reassembly differs: %d streamed bytes vs %d whole", len(reply.Body), len(want))
	}
	if _, err := Validate(reply.Body); err != nil {
		t.Errorf("reassembled stream failed container validation: %v", err)
	}
}

func TestTranscribeFormWireShape(t *testing.T) {
	audio := Sine(440, 0.5)
	var fields map[string][]string
	var fileBytes []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Fatalf("multipart content-type: %v", err)
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		fields = map[string][]string{}
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			val, _ := io.ReadAll(p)
			if p.FormName() == "file" {
				fields["file"] = []string{p.FileName()}
				fileBytes = val
				continue
			}
			fields[p.FormName()] = append(fields[p.FormName()], string(val))
		}
		w.Header().Set("Content-Type", "application/json")
		raw, _ := json.Marshal(conforming())
		w.Write(raw)
	}))
	defer srv.Close()

	c := &Client{Base: srv.URL + "/v1", Key: "k", HTTP: srv.Client()}
	reply, err := c.Transcribe(context.Background(), TranscribeOpts{
		Model: ScribeModel, Audio: audio, Filename: "sample.wav",
		Language: "en", Format: "verbose_json",
		Granularities: []string{TimingWord, TimingSegment},
	})
	if err != nil {
		t.Fatal(err)
	}
	if fields["model"][0] != ScribeModel || fields["language"][0] != "en" || fields["response_format"][0] != "verbose_json" {
		t.Errorf("form fields = %v", fields)
	}
	if strings.Join(fields["timestamp_granularities[]"], ",") != "word,segment" {
		t.Errorf(`granularities must arrive as repeated timestamp_granularities[] parts, got %v`, fields["timestamp_granularities[]"])
	}
	if fields["file"][0] != "sample.wav" || string(fileBytes) != string(audio) {
		t.Errorf("file part corrupted: name %v, %d bytes", fields["file"], len(fileBytes))
	}

	tr, err := DecodeTranscript(reply.Body)
	if err != nil {
		t.Fatalf("transcription response did not decode: %v", err)
	}
	if tr.Text == "" || len(tr.Words) != 4 {
		t.Errorf("decoded transcript = %+v", tr)
	}
}

func TestPublicTranscribeSendsNoCredential(t *testing.T) {
	var sawAuth, path, ct string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, ct, sawAuth = r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get("Authorization")
		w.Write([]byte(`{"text":""}`))
	}))
	defer srv.Close()

	c := &Client{Base: srv.URL + "/v1", HTTP: srv.Client()} // no Key: the public door takes none
	if _, err := c.PublicTranscribe(context.Background(), Sine(440, 1)); err != nil {
		t.Fatal(err)
	}
	if path != "/v1"+PublicTranscriptionsPath {
		t.Errorf("path = %q, want the public endpoint", path)
	}
	if sawAuth != "" {
		t.Errorf("public call carried Authorization %q", sawAuth)
	}
	if !strings.HasPrefix(ct, "multipart/form-data") {
		t.Errorf("content-type = %q", ct)
	}
}
