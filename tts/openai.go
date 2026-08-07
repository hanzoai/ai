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

package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAITextToSpeechProvider forwards to any OpenAI-compatible /v1/audio/speech
// endpoint — OpenAI itself, the in-cluster speech service, or a BYO node. It is
// the exact mirror of stt.OpenAISpeechToTextProvider: providerUrl is the
// OpenAI-compatible /v1 root (mirroring AIBaseURL's convention) and the provider
// appends the path; subType is the upstream model (kokoro family); voice, when
// set, is the requested voice, carried on the provider row's Flavor.
type OpenAITextToSpeechProvider struct {
	subType string
	secret  string
	url     string
	voice   string
}

func NewOpenAITextToSpeechProvider(subType string, clientSecret string, providerUrl string, voice string) (*OpenAITextToSpeechProvider, error) {
	if providerUrl == "" {
		return nil, fmt.Errorf("tts: OpenAI provider requires a provider URL")
	}
	return &OpenAITextToSpeechProvider{
		subType: subType,
		secret:  clientSecret,
		url:     strings.TrimRight(providerUrl, "/") + "/audio/speech",
		voice:   voice,
	}, nil
}

func (p *OpenAITextToSpeechProvider) GetPricing() string {
	return `URL: ` + p.url + `

Billed by the upstream per synthesized character; unpriced here (the usage row is flagged Unpriced).`
}

// speechRequest is the OpenAI /v1/audio/speech body. response_format is fixed to
// mp3: the format the caller asked for reaches the HANDLER, not the provider
// (TextToSpeechProvider carries no format argument), and mp3 is what the
// handler's audioFormat labels the bytes with by default — so requesting it
// keeps the synthesized bytes and the declared content type in agreement.
type speechRequest struct {
	Model          string `json:"model"`
	Input          string `json:"input"`
	Voice          string `json:"voice,omitempty"`
	ResponseFormat string `json:"response_format"`
}

// QueryAudio posts the text as an OpenAI speech request and returns the
// synthesized audio bytes. lang is the caller's UI language (error localization
// elsewhere); the synthesis voice is p.voice.
func (p *OpenAITextToSpeechProvider) QueryAudio(text string, ctx context.Context, lang string) ([]byte, *TextToSpeechResult, error) {
	res := &TextToSpeechResult{TokenCount: countCharacters(text), Currency: "USD"}

	body, err := json.Marshal(speechRequest{
		Model:          p.subType,
		Input:          text,
		Voice:          p.voice,
		ResponseFormat: "mp3",
	})
	if err != nil {
		return nil, res, err
	}

	hctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(hctx, http.MethodPost, p.url, bytes.NewReader(body))
	if err != nil {
		return nil, res, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.secret != "" {
		req.Header.Set("Authorization", "Bearer "+p.secret)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, res, err
	}
	defer resp.Body.Close()
	audio, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, res, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, res, fmt.Errorf("tts: upstream returned %d: %s", resp.StatusCode, strings.TrimSpace(string(audio)))
	}
	if len(audio) == 0 {
		return nil, res, fmt.Errorf("tts: upstream returned no audio")
	}
	return audio, res, nil
}

// QueryAudioStream writes the synthesized audio to writer. The OpenAI speech
// shape answers with one complete body rather than a chunked stream, so the
// bytes are synthesized and then written — the same result the streaming callers
// consume, without claiming an upstream stream that does not exist.
func (p *OpenAITextToSpeechProvider) QueryAudioStream(text string, ctx context.Context, writer io.Writer, lang string) (*TextToSpeechResult, error) {
	audio, res, err := p.QueryAudio(text, ctx, lang)
	if err != nil {
		return res, err
	}
	_, err = writer.Write(audio)
	return res, err
}
