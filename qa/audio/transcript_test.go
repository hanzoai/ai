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
	"encoding/json"
	"strings"
	"testing"
)

// f is a float64 pointer helper for Word timestamps.
func f(v float64) *float64 { return &v }

// conforming is a canned verbose_json body in the exact shape OpenAI's docs
// print: two segments, four words inside them, duration matching the audio.
func conforming() *Transcription {
	return &Transcription{
		Task:     "transcribe",
		Duration: 2.4,
		Text:     "Hello there. Nice weather.",
		Segments: []Segment{
			{ID: 0, Start: 0, End: 1.2, Text: "Hello there."},
			{ID: 1, Start: 1.2, End: 2.4, Text: "Nice weather."},
		},
		Words: []Word{
			{Word: "Hello ", Start: f(0), End: f(0.6)},
			{Word: "there.", Start: f(0.7), End: f(1.2)},
			{Word: "Nice ", Start: f(1.2), End: f(1.8)},
			{Word: "weather.", Start: f(1.8), End: f(2.4)},
		},
	}
}

func TestCheckTimingsConformingPasses(t *testing.T) {
	tr := conforming()
	if bad := CheckTimings([]string{TimingWord, TimingSegment}, tr, 2.4); len(bad) != 0 {
		t.Fatalf("conforming body rejected: %v", bad)
	}
	if bad := CheckCoverage(tr, 2.4); len(bad) != 0 {
		t.Fatalf("conforming body failed coverage: %v", bad)
	}
}

func TestCheckTimingsCatchDivergence(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(*Transcription)
		want      []string // granularities requested
		container float64
		claim     string // substring a violation must contain
	}{
		{
			name:   "segments-overlapping",
			mutate: func(tr *Transcription) { tr.Segments[1].Start = 0.5 },
			want:   []string{TimingSegment}, container: 2.4,
			claim: "overlapping spans",
		},
		{
			name:   "word-outside-segments",
			mutate: func(tr *Transcription) { tr.Words[3].Start, tr.Words[3].End = f(5.0), f(5.5) },
			want:   []string{TimingWord}, container: 2.4,
			claim: "outside every segment",
		},
		{
			name:   "word-order-broke",
			mutate: func(tr *Transcription) { tr.Words[2].Start, tr.Words[2].End = f(0.1), f(0.2) },
			want:   []string{TimingWord}, container: 2.4,
			claim: "word order broke",
		},
		{
			name:   "null-word-timestamps",
			mutate: func(tr *Transcription) { tr.Words[0].Start = nil },
			want:   []string{TimingWord}, container: 2.4,
			claim: "null timestamps",
		},
		{
			name:   "words-requested-missing",
			mutate: func(tr *Transcription) { tr.Words = nil },
			want:   []string{TimingWord}, container: 2.4,
			claim: "no words returned",
		},
		{
			name:   "duration-drift",
			mutate: func(tr *Transcription) { tr.Duration = 4.0 },
			want:   []string{TimingSegment}, container: 2.4,
			claim: "disagrees with the container",
		},
		{
			name:   "text-segments-disagree",
			mutate: func(tr *Transcription) { tr.Text = "Goodbye everyone." },
			want:   []string{TimingSegment}, container: 2.4,
			claim: "segment concatenation disagree",
		},
		{
			name:   "words-segments-disagree",
			mutate: func(tr *Transcription) { tr.Words[0].Word = "Halo " },
			want:   []string{TimingWord, TimingSegment}, container: 2.4,
			claim: "word stream and segment text disagree",
		},
		{
			name:   "timestamps-past-audio",
			mutate: func(tr *Transcription) { tr.Duration = 0; tr.Segments[1].End = 9.0 },
			want:   []string{TimingSegment}, container: 2.4,
			claim: "drifted past the recording",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := conforming()
			tc.mutate(tr)
			bad := CheckTimings(tc.want, tr, tc.container)
			if len(bad) == 0 {
				t.Fatal("divergent body passed every check")
			}
			if !containsAny(bad, tc.claim) {
				t.Fatalf("violations %q do not include %q", strings.Join(bad, "; "), tc.claim)
			}
		})
	}
}

func TestCheckCoverageCatchesTruncation(t *testing.T) {
	tr := conforming() // audio really runs 60 s: two seconds of transcript is a mid-stream cut
	if bad := CheckCoverage(tr, 60); !containsAny(bad, "never transcribed (truncated decode?)") {
		t.Errorf("a 2.4 s transcript over 60 s of audio must be flagged, got %v", bad)
	}
	tr = conforming()
	tr.Segments[0].Start = 5 // the recording begins with five untranscribed seconds
	if bad := CheckCoverage(tr, 2.4); !containsAny(bad, "first segment starts") {
		t.Errorf("a late first segment must be flagged, got %v", bad)
	}
}

func TestDecodeTranscriptRejectsWrongShapes(t *testing.T) {
	cases := map[string]string{
		"not json":        `nonsense`,
		"not an object":   `["hello"]`,
		"no text field":   `{"error":"bad file"}`,
		"text not string": `{"text":42}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeTranscript([]byte(body)); err == nil {
				t.Fatal("badly shaped body decoded without error")
			}
		})
	}

	// A conforming wire body must decode with timings intact.
	raw, err := json.Marshal(conforming())
	if err != nil {
		t.Fatal(err)
	}
	tr, err := DecodeTranscript(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Words) != 4 || len(tr.Segments) != 2 || tr.Duration != 2.4 {
		t.Errorf("decoded body lost fields: %+v", tr)
	}
}

func containsAny(lines []string, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}
