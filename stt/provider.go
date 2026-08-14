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

package stt

import (
	"context"
	"io"
)

type SpeechToTextResult struct {
	AudioDurationSeconds float64
	Price                float64
	Currency             string
}

// Timing is a timestamp granularity a caller may ask a transcription for. The
// two OpenAI defines, and the only two an upstream is asked for.
type Timing string

const (
	Word    Timing = "word"
	Segment Timing = "segment"
)

// TimedWord is one word and when it was said. This is what a caption cuts on:
// without it a consumer has to place word boundaries by dividing a line's span
// by its letters, and the error compounds down the line.
type TimedWord struct {
	Word  string  `json:"word"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// TimedSegment is one decoded span, and what the decoder thought of it.
type TimedSegment struct {
	Id               int     `json:"id"`
	Seek             int     `json:"seek"`
	Start            float64 `json:"start"`
	End              float64 `json:"end"`
	Text             string  `json:"text"`
	Tokens           []int   `json:"tokens"`
	Temperature      float64 `json:"temperature"`
	AvgLogprob       float64 `json:"avg_logprob"`
	CompressionRatio float64 `json:"compression_ratio"`
	NoSpeechProb     float64 `json:"no_speech_prob"`
}

// Transcript is what was heard — the text, and the timings the caller asked
// for. The text used to be the whole return, which is why no caption anywhere
// downstream could be timed to a word: the upstream measured every boundary
// and this plane had nowhere to put them.
type Transcript struct {
	Text     string
	Words    []TimedWord
	Segments []TimedSegment
}

type SpeechToTextProvider interface {
	GetPricing() string
	// want names the timings to ask the upstream for. Nil asks for none, and a
	// provider that cannot produce them returns a Transcript of text alone.
	ProcessAudio(audioData io.Reader, ctx context.Context, lang string, want []Timing) (*Transcript, *SpeechToTextResult, error)
}

// GetSpeechToTextProvider creates a new provider instance based on the provider
// type. flavor mirrors the TTS factory's parameter: for STT it is the audio's
// language hint (ISO-639-1), bound onto the provider row by the caller.
func GetSpeechToTextProvider(typ string, subType string, clientSecret string, providerUrl string, flavor string) (SpeechToTextProvider, error) {
	var p SpeechToTextProvider
	var err error

	if typ == "Alibaba Cloud" {
		p, err = NewAlibabacloudSpeechToTextProvider(typ, subType, clientSecret)
	} else if typ == "OpenAI" {
		p, err = NewOpenAISpeechToTextProvider(subType, clientSecret, providerUrl, flavor)
	}

	if err != nil {
		return nil, err
	}
	return p, nil
}
