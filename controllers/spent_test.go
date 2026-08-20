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
	"testing"

	"github.com/hanzoai/ai/model"
)

const spentModel = "gpt-4o"

// A stream that broke after the vendor had already sent text must be billed for that
// text. This is the whole point of reading the meter: reached() names the vendor, spent
// says how much of the request it ran, and a row with the first and not the second
// prices a half-generated answer at nothing.
func TestAPartialAnswerIsBilledForWhatWasGenerated(t *testing.T) {
	partial := "The first half of an answer that the vendor generated and charged us for " +
		"before the connection broke."

	prompt, completion := spent(nil, spentModel, 120, partial)

	if prompt != 120 {
		t.Errorf("prompt = %d, want 120 — the prompt was counted before the call went out", prompt)
	}
	if completion == 0 {
		t.Error("completion = 0 for text the vendor actually sent; the invoice for it is ours")
	}
}

// The pipeline's own count wins whenever it has one: it is what the vendor reported,
// and re-tokenizing the text locally would be a second, worse answer to a question
// already answered.
func TestAReportedCountIsPreferredToOurOwn(t *testing.T) {
	res := &model.ModelResult{
		PromptTokenCount:   11,
		ResponseTokenCount: 22,
		TotalTokenCount:    33,
	}

	prompt, completion := spent(res, spentModel, 999, "text that must not be counted")

	if prompt != 11 || completion != 22 {
		t.Errorf("spent = (%d, %d), want (11, 22) from the reported result", prompt, completion)
	}
}

// A result the pipeline left empty is not a count of zero. TotalTokenCount == 0 means
// nothing was reported, so the fallback applies rather than billing a blank.
func TestAnEmptyResultFallsBackRatherThanBillingZero(t *testing.T) {
	prompt, completion := spent(&model.ModelResult{}, spentModel, 77, "some generated text")

	if prompt != 77 {
		t.Errorf("prompt = %d, want 77", prompt)
	}
	if completion == 0 {
		t.Error("completion = 0 though the writer holds generated text")
	}
}

// Nothing generated is genuinely nothing to bill for the answer. The prompt still
// counts: it was sent, and a vendor that read it did the work of reading it.
func TestNothingGeneratedBillsOnlyThePrompt(t *testing.T) {
	prompt, completion := spent(nil, spentModel, 42, "")

	if prompt != 42 {
		t.Errorf("prompt = %d, want 42", prompt)
	}
	if completion != 0 {
		t.Errorf("completion = %d, want 0 — no answer was generated", completion)
	}
}
