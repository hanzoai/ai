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
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// OpenAISpeechToTextProvider forwards to any OpenAI-compatible
// /v1/audio/transcriptions endpoint — OpenAI itself, the in-cluster speech
// service, or a BYO node. providerUrl is the OpenAI-compatible /v1 root
// (mirroring AIBaseURL's convention); the provider appends the path. subType is
// the upstream model (whisper family); language, when set, is the audio's
// ISO-639-1 language hint, forwarded as the `language` form field.
type OpenAISpeechToTextProvider struct {
	subType  string
	secret   string
	url      string
	language string
}

func NewOpenAISpeechToTextProvider(subType string, clientSecret string, providerUrl string, language string) (*OpenAISpeechToTextProvider, error) {
	if providerUrl == "" {
		return nil, fmt.Errorf("stt: OpenAI provider requires a provider URL")
	}
	return &OpenAISpeechToTextProvider{
		subType:  subType,
		secret:   clientSecret,
		url:      strings.TrimRight(providerUrl, "/") + "/audio/transcriptions",
		language: language,
	}, nil
}

func (p *OpenAISpeechToTextProvider) GetPricing() string {
	return `URL: ` + p.url + `

Billed by the upstream per audio minute; unpriced here (the usage row is flagged Unpriced).`
}

// transcription is the OpenAI response body. verbose_json (the format this
// provider requests) carries the duration that meters the call, and the timings
// the caller asked for; text is present either way.
type transcription struct {
	Text     string         `json:"text"`
	Duration float64        `json:"duration,omitempty"`
	Words    []TimedWord    `json:"words,omitempty"`
	Segments []TimedSegment `json:"segments,omitempty"`
}

// ProcessAudio posts the audio as an OpenAI multipart transcription request and
// returns the transcribed text. lang is the caller's UI language (error
// localization elsewhere); the audio language hint is p.language.
func (p *OpenAISpeechToTextProvider) ProcessAudio(audioData io.Reader, ctx context.Context, lang string, want []Timing) (*Transcript, *SpeechToTextResult, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fw, err := w.CreateFormFile("file", "audio.webm")
	if err != nil {
		return nil, nil, err
	}
	if _, err := io.Copy(fw, audioData); err != nil {
		return nil, nil, err
	}
	if p.subType != "" {
		if err := w.WriteField("model", p.subType); err != nil {
			return nil, nil, err
		}
	}
	if p.language != "" {
		if err := w.WriteField("language", p.language); err != nil {
			return nil, nil, err
		}
	}
	// verbose_json, not json: `duration` rides ONLY on the verbose body, and that
	// duration is what meters the call (transcription is billed per minute of
	// audio). Asking for the plain body was the first of two reasons audio billed
	// nothing. The response is a superset — `text` is present either way — so the
	// decode below is unchanged and an upstream that ignores the request and
	// answers plain json still transcribes, it just meters 0.
	if err := w.WriteField("response_format", "verbose_json"); err != nil {
		return nil, nil, err
	}
	// Ask for exactly the timings the caller asked for, and no more. Segments
	// cost the upstream nothing — the decode already produced them — but word
	// alignment is a second pass over the cross-attention, so it is bought only
	// when someone wants it. Asking for none leaves the upstream on its own
	// default, which is segments.
	for _, t := range want {
		if err := w.WriteField("timestamp_granularities[]", string(t)); err != nil {
			return nil, nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, nil, err
	}

	hctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(hctx, http.MethodPost, p.url, &body)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if p.secret != "" {
		req.Header.Set("Authorization", "Bearer "+p.secret)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("stt: upstream returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var tr transcription
	if err := json.Unmarshal(respBody, &tr); err != nil {
		return nil, nil, fmt.Errorf("stt: decode upstream response: %s", err.Error())
	}
	return &Transcript{Text: tr.Text, Words: tr.Words, Segments: tr.Segments}, &SpeechToTextResult{
		AudioDurationSeconds: tr.Duration,
		Currency:             "USD",
	}, nil
}
