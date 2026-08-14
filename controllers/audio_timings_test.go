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

import (
	"encoding/json"
	"testing"

	"github.com/hanzoai/ai/stt"
)

// TestTimingsOfReadsEitherSpelling: the OpenAI SDKs bracket a multipart array
// name and hand-rolled clients send it bare. Reading one leaves half the
// callers silently untimed, which is indistinguishable from the feature being
// absent.
func TestTimingsOfReadsEitherSpelling(t *testing.T) {
	for _, key := range []string{"timestamp_granularities[]", "timestamp_granularities"} {
		got, err := timingsOf(map[string][]string{key: {"word"}})
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if len(got) != 1 || got[0] != stt.Word {
			t.Errorf("%s: got %v, want [word]", key, got)
		}
	}
}

func TestTimingsOfDedupesAcrossSpellings(t *testing.T) {
	got, err := timingsOf(map[string][]string{
		"timestamp_granularities[]": {"word", "segment"},
		"timestamp_granularities":   {"word"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("got %v, want word and segment once each", got)
	}
}

func TestTimingsOfNoneIsNone(t *testing.T) {
	got, err := timingsOf(map[string][]string{"model": {"whisper"}})
	if err != nil || got != nil {
		t.Errorf("got %v, %v; want nil, nil", got, err)
	}
}

// A granularity we cannot serve is named rather than ignored: silently dropping
// it returns a body with no timings and no reason why.
func TestTimingsOfRefusesTheUnknown(t *testing.T) {
	_, err := timingsOf(map[string][]string{"timestamp_granularities[]": {"phoneme"}})
	if err == nil {
		t.Fatal("unknown granularity accepted")
	}
}

// The plain body is exactly {"text"} — a client reading the standard shape must
// not start receiving fields it did not ask for.
func TestTranscriptionBodyPlainJSONIsTextAlone(t *testing.T) {
	heard := &stt.Transcript{
		Text:     "the fox",
		Words:    []stt.TimedWord{{Word: "the", Start: 0, End: 0.24}},
		Segments: []stt.TimedSegment{{Id: 1, End: 0.58, Text: "the fox"}},
	}
	var got map[string]any
	if err := json.Unmarshal(transcriptionBody("json", heard, 1.3), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["text"] != "the fox" {
		t.Errorf("json body = %v, want only text", got)
	}
}

// verbose_json carries the duration that meters the call AND the timings the
// decode measured. Dropping the timings here is the defect that left every
// caption in the estate guessing at word boundaries.
func TestTranscriptionBodyVerboseCarriesTimings(t *testing.T) {
	heard := &stt.Transcript{
		Text:     "the fox",
		Words:    []stt.TimedWord{{Word: "the", Start: 0, End: 0.24}, {Word: "fox", Start: 0.24, End: 0.58}},
		Segments: []stt.TimedSegment{{Id: 1, End: 0.58, Text: "the fox"}},
	}
	var got struct {
		Task     string             `json:"task"`
		Duration float64            `json:"duration"`
		Text     string             `json:"text"`
		Words    []stt.TimedWord    `json:"words"`
		Segments []stt.TimedSegment `json:"segments"`
	}
	if err := json.Unmarshal(transcriptionBody("verbose_json", heard, 1.3), &got); err != nil {
		t.Fatal(err)
	}
	if got.Task != "transcribe" || got.Duration != 1.3 || got.Text != "the fox" {
		t.Errorf("head = %+v", got)
	}
	if len(got.Words) != 2 || got.Words[1].Word != "fox" || got.Words[1].End != 0.58 {
		t.Errorf("words = %+v, want both with timings", got.Words)
	}
	if len(got.Segments) != 1 || got.Segments[0].Text != "the fox" {
		t.Errorf("segments = %+v", got.Segments)
	}
}

// A provider that reports no timings must not grow empty arrays: absent is the
// honest answer, and omitempty is what says it.
func TestTranscriptionBodyOmitsWhatWasNotMeasured(t *testing.T) {
	var got map[string]any
	if err := json.Unmarshal(transcriptionBody("verbose_json", &stt.Transcript{Text: "hi"}, 2), &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["words"]; ok {
		t.Error("words present with nothing measured")
	}
	if _, ok := got["segments"]; ok {
		t.Error("segments present with nothing measured")
	}
}
