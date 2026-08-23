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

	prompt, completion := spent(nil, spentModel, 120, partial, apiErr(500, "internal server error"))

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

	prompt, completion := spent(res, spentModel, 999, "text that must not be counted", apiErr(500, "boom"))

	if prompt != 11 || completion != 22 {
		t.Errorf("spent = (%d, %d), want (11, 22) from the reported result", prompt, completion)
	}
}

// A result the pipeline left empty is not a count of zero. TotalTokenCount == 0 means
// nothing was reported, so the fallback applies rather than billing a blank.
func TestAnEmptyResultFallsBackRatherThanBillingZero(t *testing.T) {
	prompt, completion := spent(&model.ModelResult{}, spentModel, 77, "some generated text", apiErr(500, "boom"))

	if prompt != 77 {
		t.Errorf("prompt = %d, want 77", prompt)
	}
	if completion == 0 {
		t.Error("completion = 0 though the writer holds generated text")
	}
}

// Nothing generated is genuinely nothing to bill for the answer. The prompt still
// counts: it was sent, and a vendor that read it did the work of reading it. That
// premise is the 500 below — a vendor that broke while running the request, not
// one that never started it.
func TestNothingGeneratedBillsOnlyThePrompt(t *testing.T) {
	prompt, completion := spent(nil, spentModel, 42, "", apiErr(500, "internal server error"))

	if prompt != 42 {
		t.Errorf("prompt = %d, want 42", prompt)
	}
	if completion != 0 {
		t.Errorf("completion = %d, want 0 — no answer was generated", completion)
	}
}

// A refusal at the edge bills NOTHING, and the amount it would otherwise bill
// grows with the prompt: the longer the request, the more it costs to be turned
// away for being too long. There is no invoice behind any of these — the vendor
// read the envelope and stopped — so there is nothing to pass on.
func TestARefusalAtTheDoorBillsNothing(t *testing.T) {
	for _, refusal := range []struct {
		what string
		err  error
	}{
		{"the prompt is longer than the context", apiErr(413, "payload too large")},
		{"our key is not honoured", apiErr(401, "invalid api key")},
		{"the account has no money", apiErr(402, "insufficient credits")},
		{"this vendor has not got the model", apiErr(404, "no such model")},
		{"turned away before a slot", apiErr(429, "rate limited")},
		{"nothing was ever dialled", errUnrecognised{}},
	} {
		prompt, completion := spent(nil, spentModel, 400000, "", refusal.err)
		if prompt != 0 || completion != 0 {
			t.Errorf("%s: spent = (%d, %d), want (0, 0) — nobody ran this",
				refusal.what, prompt, completion)
		}
	}
}

// A local failure: a provider row that resolves to nothing, so no vendor is ever
// dialled and the error carries no status to read.
type errUnrecognised struct{}

func (errUnrecognised) Error() string { return "failed to get model provider: unsupported type" }

// The same thing through the whole path, because the unit above only proves the
// arithmetic. serve() names a provider even when the request is refused at the
// edge, and a named provider is what reached() reports — so the record files, and
// what it files has to be nothing.
func TestARefusalAtTheDoorFilesNoCharge(t *testing.T) {
	for _, refusal := range []struct {
		what   string
		answer error
		prompt int
	}{
		{"413, the prompt is longer than the context", apiErr(413, "payload too large"), 400000},
		{"a local failure that never dialled", errUnrecognised{}, 5000},
	} {
		cooled.forget()
		quick(t)

		f := &fleet{answer: map[string]error{"do-ai": refusal.answer}}
		f.install(t)

		var w buffer
		res, by, _, err := newAsk(route("do-ai", "anthropic"), &w, nil).serve()
		if err == nil {
			t.Fatalf("%s: want the refusal to surface", refusal.what)
		}

		p, c := spent(res, spentModel, refusal.prompt, w.text, err)
		rec := &usageRecord{
			Model: spentModel, Provider: by.name, Status: "error",
			PromptTokens: p, CompletionTokens: c, TotalTokens: p + c,
		}
		if cost := usageCostNano(rec); cost > 0 {
			t.Errorf("%s: debits %d nano-USD for work no vendor did (reached=%v, spent=(%d,%d))",
				refusal.what, cost, rec.reached(), p, c)
		}
	}
	cooled.forget()
}

// Evidence outranks the classification. If text came back, a vendor produced it
// and we owe for it, whatever status arrived alongside.
func TestTextThatCameBackIsBilledWhateverTheStatus(t *testing.T) {
	_, completion := spent(nil, spentModel, 120, "half an answer the vendor sent", apiErr(413, "payload too large"))
	if completion == 0 {
		t.Error("completion = 0 for text the vendor actually sent")
	}
}

// A meter the vendor reported is the vendor saying what it ran, which settles the
// question before any classification is consulted.
func TestAReportedMeterOutranksTheRefusal(t *testing.T) {
	res := &model.ModelResult{PromptTokenCount: 7, ResponseTokenCount: 3, TotalTokenCount: 10}
	prompt, completion := spent(res, spentModel, 999, "", apiErr(413, "payload too large"))
	if prompt != 7 || completion != 3 {
		t.Errorf("spent = (%d, %d), want (7, 3) — the vendor reported its own meter", prompt, completion)
	}
}
