// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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
	"testing"

	"github.com/hanzoai/ai/stt"
	"github.com/hanzoai/ai/tts"
)

// withAudioRates installs rates for the duration of a test and restores the
// shipped tables after, so an assertion about the arithmetic does not depend on
// what the published rates happen to be.
func withAudioRates(t *testing.T, stt map[string]int64, tts map[string]int64) {
	t.Helper()
	oldSTT, oldTTS := sttNanoPerSecond, ttsNanoPerChar
	sttNanoPerSecond, ttsNanoPerChar = stt, tts
	t.Cleanup(func() { sttNanoPerSecond, ttsNanoPerChar = oldSTT, oldTTS })
}

// TestAudioQuantityReachesTheCostSwitch is the regression guard for why audio
// billed nothing: the emit sites discarded the provider result, so the record
// reached the cost switch carrying no quantity and fell through to token math
// that had 0 tokens to multiply. A rate cannot rescue a record with no quantity,
// so the quantity is the fix and this is the test of it.
func TestAudioQuantityReachesTheCostSwitch(t *testing.T) {
	withAudioRates(t,
		map[string]int64{"whisper": 10_000_000}, // 1¢ a second, so the math is readable
		map[string]int64{"kokoro": 10_000_000},  // 1¢ a character
	)

	transcribe := &usageRecord{Model: "whisper", AudioSeconds: 120}
	if got := usageCostCents(transcribe); got != 120 {
		t.Errorf("120s of whisper = %d¢, want 120¢", got)
	}
	synthesize := &usageRecord{Model: "kokoro", AudioChars: 250}
	if got := usageCostCents(synthesize); got != 250 {
		t.Errorf("250 chars of kokoro = %d¢, want 250¢", got)
	}

	// The defect itself: no quantity, no charge — whatever the rate says.
	empty := &usageRecord{Model: "whisper"}
	if got := usageCostCents(empty); got != 0 {
		t.Errorf("a record with no quantity billed %d¢; that is the bug in reverse", got)
	}
}

// TestAudioBillsThePublishedRate pins the shipped tables to hanzo.ai/pricing:
// Speech-to-Text $0.006 a minute, Text-to-Speech $15 per 1M characters, for every
// id a caller can send to the speech service, so no spelling of a model is
// cheaper than another and the quoted rate is the booked one.
func TestAudioBillsThePublishedRate(t *testing.T) {
	const (
		minute  = 6_000_000      // $0.006 in nano-USD
		million = 15_000_000_000 // $15 in nano-USD
	)
	for _, id := range []string{"zen-scribe", "zen-scribe-mini", "parakeet", "whisper", "whisper-small"} {
		if got := usageCostNano(&usageRecord{Model: id, AudioSeconds: 60}); got != minute {
			t.Errorf("a minute of %s bills %d nano, want %d ($0.006)", id, got, minute)
		}
	}
	for _, id := range []string{"zen-voice-mini", "kokoro"} {
		if got := usageCostNano(&usageRecord{Model: id, AudioChars: 1_000_000}); got != million {
			t.Errorf("1M characters of %s bill %d nano, want %d ($15)", id, got, million)
		}
	}
	// A dictated sentence is a few seconds and bills a few ten-thousandths of a
	// dollar: priced, never free, and never rounded up to a cent per call.
	if got := usageCostNano(&usageRecord{Model: "zen-scribe", AudioSeconds: 3}); got != 300_000 {
		t.Errorf("3s of zen-scribe = %d nano, want 300000 ($0.0003)", got)
	}
	if got := sttCostNano("zen-scribe", 0.0000001); got != 1 {
		t.Errorf("a sliver of audio = %d nano, want 1 (rounded up, never 0)", got)
	}
}

// TestAudioUnpricedFollowsTheRateTable pins that the Unpriced flag is DERIVED.
// Every audio emit site used to hardcode `Unpriced: true`, which would have gone
// on claiming "no price" after a price existed. Now the flag tracks the table,
// so setting a rate prices the traffic with no edit at the call sites.
func TestAudioUnpricedFollowsTheRateTable(t *testing.T) {
	rec := &usageRecord{Model: "whisper", AudioSeconds: 30}

	withAudioRates(t, map[string]int64{}, map[string]int64{})
	if !recordUnpriced(rec) {
		t.Error("no configured rate must report Unpriced — silence about price is not a price")
	}

	withAudioRates(t, map[string]int64{"whisper": 100_000}, map[string]int64{})
	if recordUnpriced(rec) {
		t.Error("a configured rate must NOT report Unpriced; the flag ignored the table")
	}
}

// TestAudioProviderCostIsZero pins that speech has no COGS: it runs on hardware
// we already own, so there is no upstream invoice and the margin on an audio call
// is the whole price.
func TestAudioProviderCostIsZero(t *testing.T) {
	rec := &usageRecord{Model: "zen-scribe", AudioSeconds: 60}
	got := providerCostNano(rec)
	if got == nil {
		t.Fatal("speech COGS is a known zero, not an unknown — we own the hardware")
	}
	if *got != 0 {
		t.Errorf("provider COGS = %d nano, want 0 (our own hardware, no invoice)", *got)
	}
	if got := usageCostNano(rec); got != 6_000_000 {
		t.Errorf("billed = %d nano, want 6000000", got)
	}
}

// TestSTTSecondsOfNeverGuesses asserts an unreported duration meters 0 rather
// than an estimate. Bytes do not imply duration — the same megabyte is minutes
// of PCM or hours of Opus — so a guess here would be a fabricated invoice.
func TestSTTSecondsOfNeverGuesses(t *testing.T) {
	if got := sttSecondsOf(nil); got != 0 {
		t.Errorf("nil result = %v seconds, want 0", got)
	}
	if got := sttSecondsOf(&stt.SpeechToTextResult{}); got != 0 {
		t.Errorf("unreported duration = %v seconds, want 0", got)
	}
	if got := sttSecondsOf(&stt.SpeechToTextResult{AudioDurationSeconds: 12.5}); got != 12.5 {
		t.Errorf("reported duration = %v, want 12.5", got)
	}
}

// TestTTSCharsOfFallsBackToTheInput asserts synthesis DOES fall back to the
// requested text. Unlike a duration, this is not a guess: synthesis cost is
// linear in the input and the input is known before the call, so the fallback is
// the quantity rather than an estimate of it.
func TestTTSCharsOfFallsBackToTheInput(t *testing.T) {
	if got := ttsCharsOf(nil, "hello"); got != 5 {
		t.Errorf("nil result = %d chars, want 5 (the text we asked it to speak)", got)
	}
	if got := ttsCharsOf(&tts.TextToSpeechResult{TokenCount: 42}, "hello"); got != 42 {
		t.Errorf("reported count = %d, want 42 (the provider's own count wins)", got)
	}
	// Multi-byte text counts runes, not bytes: a caller is not charged extra for
	// the encoding of their alphabet.
	if got := ttsCharsOf(nil, "héllo"); got != 5 {
		t.Errorf("multi-byte input = %d, want 5 runes", got)
	}
}

// TestAudioIsNotTokenBilled asserts an audio record never falls through to the
// token table, where an id with no token price would bill at the default chat
// rate.
func TestAudioIsNotTokenBilled(t *testing.T) {
	withAudioRates(t, map[string]int64{}, map[string]int64{})
	rec := &usageRecord{Model: "whisper", AudioSeconds: 600, PromptTokens: 5_000_000}
	if got := usageCostCents(rec); got != 0 {
		t.Errorf("audio record billed %d¢ — it reached the TOKEN table, not the audio one", got)
	}
}
