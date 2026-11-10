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
	"fmt"
	"math"
	"strings"
)

// Timing granularity names — OpenAI's two spellings, the same values the
// stt package forwards as timestamp_granularities[].
const (
	TimingWord    = "word"
	TimingSegment = "segment"
)

// Word is one transcribed word and when it was said. Start/End are pointers
// because OpenAI's own contract says they are null when timestamps are off —
// decoding them into a plain float would silently read an untimed word as one
// spoken at second 0, which is the difference between "no timing" and "wrong
// timing", and this suite exists to tell those apart.
type Word struct {
	Word  string   `json:"word"`
	Start *float64 `json:"start"`
	End   *float64 `json:"end"`
}

// Segment is one decoded span. Only the fields every verbose_json body carries
// are modelled; the whisper internals (tokens, logprobs) ride through in raw
// JSON but are nobody's assertion here.
type Segment struct {
	ID    int     `json:"id"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

// Transcription is the OpenAI /v1/audio/transcriptions verbose_json body.
type Transcription struct {
	Task     string    `json:"task"`
	Duration float64   `json:"duration"`
	Text     string    `json:"text"`
	Words    []Word    `json:"words"`
	Segments []Segment `json:"segments"`
}

// DecodeTranscript parses a transcription body, refusing anything whose `text`
// is not a string — an error envelope shaped like JSON must not decode into an
// empty-but-valid transcript.
func DecodeTranscript(raw []byte) (*Transcription, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("not a JSON object: %w (body starts %q)", err, clip(string(raw)))
	}
	if t, ok := probe["text"]; !ok {
		return nil, fmt.Errorf("no \"text\" field in transcription body (keys: %s)", keysOf(probe))
	} else if varr := strings.TrimSpace(string(t)); !strings.HasPrefix(varr, `"`) {
		return nil, fmt.Errorf("\"text\" is %s, want a JSON string", clip(varr))
	}
	var tr Transcription
	if err := json.Unmarshal(raw, &tr); err != nil {
		return nil, fmt.Errorf("decode transcription body: %w", err)
	}
	return &tr, nil
}

// CheckTimings audits a verbose_json body against what the caller asked for and
// what the audio container actually measures. It returns one sentence per
// violation — the documented reproduction steps the acceptance criteria ask
// for — and an empty slice when every timestamp holds. container is the
// duration measured from the response bytes (0 = unknown, drift check skipped).
func CheckTimings(want []string, tr *Transcription, container float64) []string {
	var bad []string
	add := func(f string, a ...any) { bad = append(bad, fmt.Sprintf(f, a...)) }

	// The requested granularities must actually arrive: a gateway that accepts
	// timestamp_granularities[] and silently drops it is worse than one that
	// refuses, because the caller's captions come back unaligned.
	wantWord, wantSegment := false, false
	for _, w := range want {
		switch w {
		case TimingWord:
			wantWord = true
		case TimingSegment:
			wantSegment = true
		default:
			add("unknown granularity %q requested — suite bug", w)
		}
	}
	if wantWord && len(tr.Words) == 0 {
		add("word granularity requested, no words returned")
	}
	if wantSegment && len(tr.Segments) == 0 {
		add("segment granularity requested, no segments returned")
	}

	// Segments: monotonic, non-overlapping, inside the audio.
	var lastEnd float64 = -1
	for i, s := range tr.Segments {
		if s.End <= s.Start {
			add("segment %d ends at %.3f before it starts at %.3f", s.ID, s.End, s.Start)
		}
		if s.Start < lastEnd-0.05 {
			add("segment %d starts at %.3f, inside segment ending %.3f — overlapping spans", s.ID, s.Start, lastEnd)
		}
		if i > 0 && s.ID <= tr.Segments[i-1].ID {
			add("segment ids not increasing: %d after %d", s.ID, tr.Segments[i-1].ID)
		}
		lastEnd = math.Max(lastEnd, s.End)
	}
	if dur := effectiveDuration(tr, container); dur > 0 && lastEnd > dur+TimingTol {
		add("last segment ends at %.3f but audio is only %.3f long — timestamps drifted past the recording", lastEnd, dur)
	}

	// Words: timed at all, monotonic, and contained by some segment.
	var wordEnd float64 = -1
	for i, w := range tr.Words {
		if w.Start == nil || w.End == nil {
			add("word %d (%q) has null timestamps although word timing was decoded", i, clip(w.Word))
			continue
		}
		if *w.End <= *w.Start {
			add("word %d (%q) ends %.3f before it starts %.3f", i, clip(w.Word), *w.End, *w.Start)
		}
		if *w.Start < wordEnd-TimingTol {
			add("word %d (%q) at %.3f precedes the end of word %d at %.3f — word order broke", i, clip(w.Word), *w.Start, i-1, wordEnd)
		}
		wordEnd = math.Max(wordEnd, *w.End)
		if len(tr.Segments) > 0 && !insideAnySegment(*w.Start, *w.End, tr.Segments) {
			add("word %d (%q) spans %.3f–%.3f, outside every segment — word/segment alignment failed", i, clip(w.Word), *w.Start, *w.End)
		}
	}

	// The two views of the transcript must agree, or one of them is fabricated.
	if len(tr.Segments) > 0 && NormalizeText(tr.Text) != "" {
		var seg strings.Builder
		for _, s := range tr.Segments {
			seg.WriteString(s.Text)
		}
		if NormalizeText(seg.String()) != NormalizeText(tr.Text) {
			add("text and segment concatenation disagree (text %q vs segments %q)", clip(tr.Text), clip(seg.String()))
		}
	}
	if len(tr.Words) > 0 && len(tr.Segments) > 0 {
		var words strings.Builder
		for _, w := range tr.Words {
			words.WriteString(w.Word)
		}
		var seg strings.Builder
		for _, s := range tr.Segments {
			seg.WriteString(s.Text)
		}
		if NormalizeText(words.String()) != NormalizeText(seg.String()) {
			add("word stream and segment text disagree (words %q vs segments %q)", clip(words.String()), clip(seg.String()))
		}
	}

	// Duration drift: the metered duration must match the container's own, or
	// billing and captions are both off — the timestamp-drift finding named in
	// the issue.
	if cd, td := container, tr.Duration; cd > 0 && td > 0 {
		tol := math.Max(2*TimingTol, 0.03*cd)
		if math.Abs(cd-td) > tol {
			add("reported duration %.3f s disagrees with the container's measured %.3f s by more than %.1f s", td, cd, tol)
		}
	}
	return bad
}

// TimingTol absorbs decoder rounding: whisper timestamps quantise to frame
// boundaries, so a violation must clear this before it is real drift.
const TimingTol = 0.25

// CheckCoverage pins that a long recording was transcribed end to end — the
// truncated-audio and chunk-boundary findings named in the issue. The first
// speech must begin near zero and the last must reach near the audio's end; a
// tail left untranscribed is exactly where a mid-stream cut shows up.
func CheckCoverage(tr *Transcription, container float64) []string {
	var bad []string
	if len(tr.Segments) == 0 || container <= 0 {
		return nil // nothing structural to align against
	}
	if s := tr.Segments[0].Start; s > TimingTol {
		bad = append(bad, fmt.Sprintf("first segment starts at %.3f — the first %.1f s of audio were never transcribed", s, s))
	}
	last := tr.Segments[len(tr.Segments)-1].End
	if short := container - last; short > math.Max(2*TimingTol, 0.15*container) {
		bad = append(bad, fmt.Sprintf("segments stop at %.3f of %.3f s — the last %.1f s were never transcribed (truncated decode?)", last, container, short))
	}
	return bad
}

// effectiveDuration prefers the body's reported duration, falling back to the
// container measurement; 0 means neither is known.
func effectiveDuration(tr *Transcription, container float64) float64 {
	if tr.Duration > 0 {
		return tr.Duration
	}
	return container
}

// insideAnySegment reports whether a word span sits within one segment's span
// (widened by TimingTol for boundary rounding).
func insideAnySegment(start, end float64, segs []Segment) bool {
	for _, s := range segs {
		if start >= s.Start-TimingTol && end <= s.End+TimingTol {
			return true
		}
	}
	return false
}

func keysOf(m map[string]json.RawMessage) string {
	var names []string
	for k := range m {
		names = append(names, k)
	}
	return strings.Join(names, ", ")
}

func clip(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= 120 {
		return string(r)
	}
	return string(r[:120]) + "…"
}
