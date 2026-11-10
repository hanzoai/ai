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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
)

// Client speaks just enough of the OpenAI audio contract to drive the live
// lanes: one JSON body in, one response out (audio bytes or a transcript), plus
// the multipart transcription form. It is deliberately tiny — the suite's job
// is the audio verdicts, not being an SDK.
type Client struct {
	Base string // gateway root ending in /v1
	Key  string // Bearer credential; empty means send no Authorization at all
	HTTP *http.Client
}

// NewClient builds a client from the environment (see BaseURL and KeyEnv).
func NewClient() *Client {
	return &Client{Base: BaseURL(), Key: os.Getenv(KeyEnv), HTTP: &http.Client{Timeout: RequestTimeout}}
}

// Reply is one HTTP exchange, buffered whole. Length carries the transport's
// own Content-Length (-1 unknown) so a streamed body can be checked against
// what the response promised — the cheap truncation detector.
type Reply struct {
	Status      int
	ContentType string
	Body        []byte
	Length      int64
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, contentType string) (*Reply, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return &Reply{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: raw, Length: resp.ContentLength}, nil
}

// PostJSON sends one JSON body to base+path.
func (c *Client) PostJSON(ctx context.Context, path string, v any) (*Reply, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", path, err)
	}
	return c.do(ctx, http.MethodPost, path, bytes.NewReader(raw), "application/json")
}

// SpeechBody renders the OpenAI /v1/audio/speech request; split out so the
// offline wire-shape test pins every field without a server. speed==0 and an
// empty voice/format omit the field entirely — the endpoint's defaults apply.
func SpeechBody(model, input, voice, format string, speed float64) map[string]any {
	m := map[string]any{"model": model, "input": input}
	if voice != "" {
		m["voice"] = voice
	}
	if format != "" {
		m["response_format"] = format
	}
	if speed > 0 {
		m["speed"] = speed
	}
	return m
}

// Speech posts one synthesis request and returns the audio bytes whole.
func (c *Client) Speech(ctx context.Context, model, input, voice string, format Format, speed float64) (*Reply, error) {
	return c.PostJSON(ctx, "/audio/speech", SpeechBody(model, input, voice, string(format), speed))
}

// SpeechStreamed issues the same request but consumes the response body in
// repeated ≤chunk-sized reads — how a real playback client eats a live stream.
// A gateway that corrupts bytes at chunk boundaries shows up here as a body
// that fails Validate even though the buffered call passes.
func (c *Client) SpeechStreamed(ctx context.Context, model, input string, format Format, chunk int) (*Reply, error) {
	raw, err := json.Marshal(SpeechBody(model, input, "", string(format), 0))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+"/audio/speech", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var buf []byte
	tmp := make([]byte, chunk)
	for {
		n, rerr := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, fmt.Errorf("stream aborted after %d of %d promised bytes: %w", len(buf), resp.ContentLength, rerr)
		}
	}
	return &Reply{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: buf, Length: resp.ContentLength}, nil
}

// TranscribeOpts is one OpenAI transcription form: audio + model, plus the
// optional language hint, response_format and timestamp_granularities[].
type TranscribeOpts struct {
	Model         string
	Audio         []byte
	Filename      string   // sent as the file part's name; defaults to audio.wav
	Language      string   // ISO-639-1 hint, omitted when empty
	Format        string   // "" | text | json | verbose_json
	Granularities []string // word / segment — only meaningful with verbose_json
}

// transcribeBody encodes the multipart form; split out so the offline test can
// read it back with mime/multipart.Reader and pin every field.
func transcribeBody(o TranscribeOpts) (*bytes.Buffer, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	name := o.Filename
	if name == "" {
		name = "audio.wav"
	}
	fw, err := w.CreateFormFile("file", name)
	if err != nil {
		return nil, "", err
	}
	if _, err := fw.Write(o.Audio); err != nil {
		return nil, "", err
	}
	fields := []struct{ key, val string }{
		{"model", o.Model},
		{"language", o.Language},
		{"response_format", o.Format},
	}
	for _, f := range fields {
		if f.val != "" {
			if err := w.WriteField(f.key, f.val); err != nil {
				return nil, "", err
			}
		}
	}
	for _, g := range o.Granularities {
		if err := w.WriteField("timestamp_granularities[]", g); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return &buf, w.FormDataContentType(), nil
}

// Transcribe posts the multipart form to /audio/transcriptions.
func (c *Client) Transcribe(ctx context.Context, o TranscribeOpts) (*Reply, error) {
	buf, ct, err := transcribeBody(o)
	if err != nil {
		return nil, err
	}
	return c.do(ctx, http.MethodPost, "/audio/transcriptions", buf, ct)
}

// PublicTranscribe posts only the file part to the key-free public door — the
// caller is expected to hold an empty Key so no Authorization rides at all.
func (c *Client) PublicTranscribe(ctx context.Context, audio []byte) (*Reply, error) {
	buf, ct, err := transcribeBody(TranscribeOpts{Audio: audio, Filename: "tone.wav"})
	if err != nil {
		return nil, err
	}
	return c.do(ctx, http.MethodPost, PublicTranscriptionsPath, buf, ct)
}

// Models reads the catalog, used by the live lanes to confirm the two pinned
// SKUs are still listed before anything is billed against them.
func (c *Client) Models(ctx context.Context) (*Reply, error) {
	return c.do(ctx, http.MethodGet, "/models", nil, "")
}
