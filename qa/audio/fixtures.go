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
	"encoding/binary"
	"math"
	"strings"
	"unicode"
)

// SynthRate is the sample rate every generated fixture carries — the same rate
// OpenAI's pcm/wav responses use, so loudness and duration math lines up with
// what the endpoint promises.
const SynthRate = 24000

// encodePCM16 wraps s16le mono samples in a canonical 44-byte WAVE header,
// hand-built rather than pulled from an encoder: byte-for-byte reproducible
// fixtures, and the exact envelope formats.go's validator then walks.
func encodePCM16(pcm []byte) []byte {
	out := make([]byte, 44+len(pcm))
	copy(out[0:], "RIFF")
	binary.LittleEndian.PutUint32(out[4:], uint32(36+len(pcm)))
	copy(out[8:], "WAVE")
	copy(out[12:], "fmt ")
	binary.LittleEndian.PutUint32(out[16:], 16)
	binary.LittleEndian.PutUint16(out[20:], 1) // PCM
	binary.LittleEndian.PutUint16(out[22:], 1) // mono
	binary.LittleEndian.PutUint32(out[24:], SynthRate)
	binary.LittleEndian.PutUint32(out[28:], SynthRate*2) // byte rate
	binary.LittleEndian.PutUint16(out[32:], 2)           // block align
	binary.LittleEndian.PutUint16(out[34:], 16)          // bits per sample
	copy(out[36:], "data")
	binary.LittleEndian.PutUint32(out[40:], uint32(len(pcm)))
	copy(out[44:], pcm)
	return out
}

// wavEncode renders int16 samples as a WAVE file.
func wavEncode(samples []int16) []byte {
	pcm := make([]byte, 2*len(samples))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(s))
	}
	return encodePCM16(pcm)
}

// toneSample renders one sample of a sine at the given time and frequency.
func toneSample(t float64, freq float64, amp float64) int16 {
	return int16(amp * math.Sin(2*math.Pi*freq*t))
}

// Sine generates secs seconds of a pure tone at freq Hz, amplitude half of
// full scale. The reference signal for the loudness and container checks: its
// RMS is exactly 0.5/√2 of full scale, so a check can assert a number instead
// of "nonzero".
func Sine(freq float64, secs float64) []byte {
	n := int(secs * SynthRate)
	out := make([]int16, n)
	for i := range out {
		out[i] = toneSample(float64(i)/SynthRate, freq, 16384)
	}
	return wavEncode(out)
}

// Silence generates secs seconds of digital silence — the input for the
// interruption and silence-hallucination checks (a transcriber handed nothing
// must not invent a paragraph).
func Silence(secs float64) []byte {
	return wavEncode(make([]int16, int(secs*SynthRate)))
}

// SpeechLike generates secs seconds of deterministic speech-shaped audio:
// alternating 250 ms tones with a raised-cosy attack/decay, separated by 150 ms
// of silence — the syllable rhythm a human hears as "someone talking" while
// every byte stays reproducible. The tone onsets land at known seconds, which
// is what lets the long-recording checks demand alignment instead of vibes.
func SpeechLike(secs float64) []byte {
	n := int(secs * SynthRate)
	out := make([]int16, n)
	syll := int(0.25 * SynthRate)
	gap := int(0.15 * SynthRate)
	i, k := 0, 0
	for i < n {
		end := i + syll
		if end > n {
			end = n
		}
		freq := []float64{220, 330}[k%2]
		span := float64(end - i)
		for j := i; j < end; j++ {
			env := 0.5 * (1 - math.Cos(4*math.Pi*float64(j-i)/span)) // raised-cosy in and out
			out[j] = toneSample(float64(j-i)/SynthRate, freq, 20000*env)
		}
		i = end + gap
		k++
	}
	return wavEncode(out)
}

// Noise generates secs seconds of deterministic pseudo-noise (a 31-bit LCG),
// the background-noise stressor for live STT probes and a loudness reference
// with a predictable RMS band.
func Noise(secs float64) []byte {
	n := int(secs * SynthRate)
	out := make([]int16, n)
	state := uint32(0xACE1)
	for i := range out {
		state = state*1103515245 + 12345
		out[i] = int16(int32((state>>16)&0x3FFF) - 8192) // uniform ±8192 → rms ≈ 0.144 full scale
	}
	return wavEncode(out)
}

// ConcatWAV splices PCM-carrying WAVE files end to end — the long-recording /
// chunk-boundary builder: Silence(2)+SpeechLike(10)+Silence(5)+SpeechLike(10)
// is a 27-second fixture with known speech onsets at seconds 2 and 17.
func ConcatWAV(parts ...[]byte) []byte {
	var pcm []byte
	for _, p := range parts {
		samples, err := wavPCM(p)
		if err != nil {
			panic("qa/audio: concat: " + err.Error()) // fixture misuse; fail loudly at build time
		}
		pcm = append(pcm, samples...)
	}
	return encodePCM16(pcm)
}

// Phrase is one language sample for the STT round trip: what to synthesize and
// the words a correct transcription must contain. Keywords are matched after
// NormalizeText strips case, spacing and punctuation, so they are bare
// lowercase stems (transcribers add inflection and articles freely).
type Phrase struct {
	Lang     string   // ISO-639-1 code, sent as the `language` hint where supported
	Text     string   // the sentence to synthesize
	Keywords []string // ≥1 must appear in a correct transcription (en rows demand more, see live_test)
}

// Phrases is the multilingual corpus the issue asks for: one pangram-family
// sentence per language plus a technical-terminology row, each pinned to the
// words its transcription has to reproduce. English and Russian are the rows
// held to a strict bar (the engines' strongest languages); the rest prove the
// multilingual path reaches real words at all.
var Phrases = []Phrase{
	{"en", "The quick brown fox jumps over the lazy dog.", []string{"quick", "brown", "fox", "lazy", "dog"}},
	{"en-tech", "Configure the Kubernetes ingress controller with cert-manager and HTTP/2 multiplexing.", []string{"kubernetes", "ingress", "controller", "multiplexing"}},
	{"ru", "Быстрая коричневая лиса прыгает через ленивую собаку.", []string{"быстрая", "коричневая", "лиса", "собаку"}},
	{"uk", "Швидка коричнева лисиця перестрибує через лінивого пса.", []string{"швидка", "коричнева", "лисиця"}},
	{"es", "El zorro marrón rápido salta sobre el perro perezoso.", []string{"zorro", "perro", "perezoso"}}, // keywords avoid accents: transcribers drop or add them freely
	{"fr", "Le renard brun rapide saute par-dessus le chien paresseux.", []string{"renard", "brun", "rapide", "chien"}},
	{"de", "Der schnelle braune Fuchs springt über den faulen Hund.", []string{"schnelle", "braune", "fuchs", "hund"}},
	{"zh", "敏捷的棕色狐狸跳过懒狗。", []string{"狐狸", "棕色"}},
	{"ja", "素早い茶色のキツネが怠け者の上を飛び越えます。", []string{"キツネ", "茶色"}},
}

// NormalizeText lowercases and strips everything that is not a letter or digit,
// so keyword containment survives the case folding, accents, hyphens and
// punctuation transcribers add or drop.
func NormalizeText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// KeywordHits counts how many keywords survive normalized containment in the
// normalized transcription.
func KeywordHits(text string, keywords []string) int {
	norm := NormalizeText(text)
	hits := 0
	for _, k := range keywords {
		if strings.Contains(norm, NormalizeText(k)) {
			hits++
		}
	}
	return hits
}

// LongSpeechText is the long-recording input: ~730 characters of ordinary
// prose (well under MaxSpeechInput's 4,096-byte bound) that synthesizes to
// around a minute of audio — the duration where chunk-boundary and mid-stream
// truncation bugs live. It stays plain ASCII on purpose: Kokoro's multilingual
// coverage is what the Phrases rows test, not this row.
const LongSpeechText = "Good morning. This recording exists to stress the transcription path at the " +
	"places short clips never reach: the boundaries where a long stream is chopped into " +
	"chunks, the silences between sentences where a careless decoder starts inventing " +
	"words, and the moment someone stops talking mid-syllable and starts again. " +
	"We will speak for over a minute now. Listen for the pauses. Each pause is a seam, " +
	"and every seam is a place where truncated audio, drifting timestamps, or a confident " +
	"hallucination would show up on the report. The first half of this message is dense " +
	"on purpose, because truncation loves a long quiet tail. If you can read all of this " +
	"back with word timings that stay inside the audio duration, the pipeline held."
