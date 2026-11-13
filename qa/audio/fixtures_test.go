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
	"math"
	"strings"
	"testing"
)

func TestGeneratedFixturesDecode(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		dur  float64
	}{
		{"sine", Sine(440, 1.5), 1.5},
		{"silence", Silence(2), 2},
		{"speech-like", SpeechLike(10), 10},
		{"noise", Noise(3), 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, err := Validate(tc.data)
			if err != nil {
				t.Fatalf("generated %s fixture failed validation: %v", tc.name, err)
			}
			if info.SampleRate != SynthRate || info.BitDepth != 16 || info.Channels != 1 {
				t.Errorf("fixture params = %+v, want %d Hz mono s16", info, SynthRate)
			}
			if math.Abs(info.DurationSeconds-tc.dur) > 0.005 {
				t.Errorf("duration = %.4f, want %.4f", info.DurationSeconds, tc.dur)
			}
		})
	}
}

func TestSpeechLikeCarriesSignal(t *testing.T) {
	rms, peak, ok := SignalLevel(WAV, SpeechLike(5))
	if !ok {
		t.Fatal("wav signal extraction failed")
	}
	if peak == 0 || rms < 0.05 || rms > 0.30 {
		t.Errorf("speech-like rms = %.4f peak = %d — envelope render is off", rms, peak)
	}
	if _, _, ok := SignalLevel(MP3, mkMP3(10, 10)); ok {
		t.Error("compressed formats must report ok=false, not fake a level")
	}
}

func TestConcatWAVSumsDurations(t *testing.T) {
	long := ConcatWAV(Silence(2), SpeechLike(10), Silence(5), SpeechLike(10))
	info, err := Validate(long)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(info.DurationSeconds-27) > 0.01 {
		t.Errorf("spliced duration = %.4f, want 27", info.DurationSeconds)
	}
}

func TestPhrasesCorpusIsSelfConsistent(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Phrases {
		if seen[p.Lang] {
			t.Errorf("duplicate language row %q", p.Lang)
		}
		seen[p.Lang] = true
		if len(p.Keywords) == 0 || strings.TrimSpace(p.Text) == "" {
			t.Fatalf("%s: empty text or no keywords", p.Lang)
		}
		for _, k := range p.Keywords {
			if NormalizeText(k) != k {
				t.Errorf("%s: keyword %q is not normalized — live matching would silently miss it", p.Lang, k)
			}
		}
		// The corpus must contain its own keywords: a sentence that cannot
		// match its pinned words is an impossible acceptance bar.
		if got := KeywordHits(p.Text, p.Keywords); got < 1 {
			t.Errorf("%s: source sentence matches none of its own keywords (%v)", p.Lang, p.Keywords)
		}
	}
	for _, want := range []string{"en", "ru", "uk", "es", "fr", "de", "zh", "ja"} {
		if !seen[want] {
			t.Errorf("corpus is missing the %s row #133 asks for", want)
		}
	}
}

func TestLongSpeechTextWithinBound(t *testing.T) {
	// MaxSpeechInput in controllers/audio_api.go — 4096 bytes of UTF-8. Keep
	// this suite's own bound so a fixture rewrite cannot silently outgrow it.
	const maxSpeechInputBytes = 4096
	if n := len(LongSpeechText); n > maxSpeechInputBytes {
		t.Errorf("LongSpeechText is %d bytes, endpoint bound is %d", n, maxSpeechInputBytes)
	}
	if n := len([]rune(LongSpeechText)); n < 700 {
		t.Errorf("LongSpeechText shrank to %d runes — long-recording coverage needs the duration", n)
	}
}
