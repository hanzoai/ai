// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2025 The OpenAgent Authors. All Rights Reserved.
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
	"context"
	"io"
)

type TextToSpeechResult struct {
	TokenCount int
	Price      float64
	Currency   string

	// ContentType is the media type the provider ACTUALLY produced, which is not
	// always the one the caller asked for: an upstream may ignore an unsupported
	// response_format and answer in its default. Reporting it lets the handler
	// label the bytes with what they are instead of with what was requested —
	// /v1/audio/speech used to answer `Content-Type: audio/opus` carrying MP3,
	// which is the API lying to a player. Empty means the provider did not say.
	ContentType string
}

type TextToSpeechProvider interface {
	GetPricing() string
	QueryAudio(text string, ctx context.Context, lang string) ([]byte, *TextToSpeechResult, error)
	QueryAudioStream(text string, ctx context.Context, writer io.Writer, lang string) (*TextToSpeechResult, error)
}

// GetTextToSpeechProvider creates a new provider instance based on the provider
// type. flavor mirrors the STT factory's parameter: for TTS it is the synthesis
// voice, bound onto the provider row by the caller. format is the requested
// container (mp3, wav, …); it is forwarded to upstreams that honour it, and what
// comes back is reported on the result rather than assumed.
func GetTextToSpeechProvider(typ string, subType string, clientId string, clientSecret string, providerUrl string, apiVersion string, pricePerThousandChars float64, currency string, flavor string, format string, lang string) (TextToSpeechProvider, error) {
	var p TextToSpeechProvider
	var err error

	if typ == "Alibaba Cloud" {
		p, err = NewAlibabacloudTextToSpeechProvider(typ, subType, clientSecret, flavor)
	} else if typ == "OpenAI" {
		p, err = NewOpenAITextToSpeechProvider(subType, clientSecret, providerUrl, flavor, format)
	}

	if err != nil {
		return nil, err
	}
	return p, nil
}
