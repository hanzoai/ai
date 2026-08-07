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
// real (empty) tables after. The shipped tables are deliberately empty — pricing
// is a business decision — so every rate-dependent assertion supplies its own.
func withAudioRates(t *testing.T, stt map[string]int64, tts map[string]int64) {
	t.Helper()
	oldSTT, oldTTS := sttPricePerMinuteCents, ttsPricePerMillionCharsCents
	sttPricePerMinuteCents, ttsPricePerMillionCharsCents = stt, tts
	t.Cleanup(func() { sttPricePerMinuteCents, ttsPricePerMillionCharsCents = oldSTT, oldTTS })
}

// TestAudioQuantityReachesTheCostSwitch is the regression guard for why audio
// billed nothing: the emit sites discarded the provider result, so the record
// reached usageCostCents carrying no quantity and fell through to token math
// that had 0 tokens to multiply. A rate cannot rescue a record with no quantity,
// so the quantity is the fix and this is the test of it.
func TestAudioQuantityReachesTheCostSwitch(t *testing.T) {
	withAudioRates(t,
		map[string]int64{"whisper": 60}, // 60¢/min == 1¢/sec, so the math is readable
		map[string]int64{"kokoro": 1_000_000},
	)

	// 120s at 60¢/min = 120¢.
	transcribe := &usageRecord{Model: "whisper", AudioSeconds: 120}
	if got := usageCostCents(transcribe); got != 120 {
		t.Errorf("120s of whisper = %d¢, want 120¢", got)
	}
	// 250_000 chars at $10_000/M... 1_000_000¢ per million chars = 1¢/char.
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

// TestAudioCostRoundsUp asserts a sub-cent call is not free once a rate exists.
// Conversation is many short utterances; truncating each to zero would meter a
// whole product at nothing.
func TestAudioCostRoundsUp(t *testing.T) {
	withAudioRates(t, map[string]int64{"whisper": 6}, map[string]int64{"kokoro": 1500})

	if got := sttCostCents("whisper", 1); got != 1 {
		t.Errorf("1 second at 6¢/min = %d¢, want 1¢ (rounded up, never 0)", got)
	}
	if got := ttsCostCents("kokoro", 10); got != 1 {
		t.Errorf("10 chars at 1500¢/M = %d¢, want 1¢ (rounded up, never 0)", got)
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

	withAudioRates(t, map[string]int64{"whisper": 6}, map[string]int64{})
	if recordUnpriced(rec) {
		t.Error("a configured rate must NOT report Unpriced; the flag ignored the table")
	}
}

// TestAudioShipsWithNoRates pins the deliberate state of the shipped tables.
// Rates are a business decision; the plumbing lands without them so metering can
// be correct before pricing is settled. If this fails, someone set a sell price
// in code — that is a decision, and it should be a visible one.
func TestAudioShipsWithNoRates(t *testing.T) {
	if len(sttPricePerMinuteCents) != 0 || len(ttsPricePerMillionCharsCents) != 0 {
		t.Errorf("audio rates are set in code (stt=%d tts=%d entries); pricing is not a code change",
			len(sttPricePerMinuteCents), len(ttsPricePerMillionCharsCents))
	}
}

// TestAudioProviderCostIsZero pins that speech has no COGS: it runs on hardware
// we already own, so there is no upstream invoice and the margin on an audio call
// is the whole price.
func TestAudioProviderCostIsZero(t *testing.T) {
	withAudioRates(t, map[string]int64{"whisper": 60}, nil)
	rec := &usageRecord{Model: "whisper", AudioSeconds: 60}
	if got := providerCostNano(rec); got != 0 {
		t.Errorf("provider COGS = %d nano, want 0 (our own hardware, no invoice)", got)
	}
	if got := usageCostNano(rec); got != 60*10_000_000 {
		t.Errorf("billed = %d nano, want %d", got, 60*10_000_000)
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
// token table. whisper and kokoro carry catalogue pricing entries, so a record
// that reached token math would silently bill at a chat rate.
func TestAudioIsNotTokenBilled(t *testing.T) {
	withAudioRates(t, map[string]int64{}, map[string]int64{})
	rec := &usageRecord{Model: "whisper", AudioSeconds: 600, PromptTokens: 5_000_000}
	if got := usageCostCents(rec); got != 0 {
		t.Errorf("audio record billed %d¢ — it reached the TOKEN table, not the audio one", got)
	}
}
