// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"strings"
	"testing"
)

// What the model is TOLD to produce and what we then read back out of its answer
// are two halves of one contract, and nothing checks that they still agree.
//
// They are written apart — the instructions in the question, the parsing in the
// answer — so a divider that changes on one side leaves the other reading prose
// that still contains its own markup, or dropping a title into the reply. Both
// fail quietly: the customer sees the separator in their answer, or the title
// simply never appears, and every status on the way is 200.
func TestAnAnswerCarriesItsTitleAndSuggestionsBackOut(t *testing.T) {
	// The shape the instructions ask for: the answer, its follow-ups after the
	// suggestion divider, and the title last after the title divider.
	answer := "The capital is Paris.|||What else is in France?|||How big is Paris?=====Capital of France"

	parsed, suggestions, title, err := parseAnswerWithCarriers(answer, 2, true)
	if err != nil {
		t.Fatalf("parseAnswerWithCarriers: %v", err)
	}
	if parsed != "The capital is Paris." {
		t.Errorf("the answer read back as %q — the markup was left in what the customer sees", parsed)
	}
	if title != "Capital of France" {
		t.Errorf("title = %q, want %q", title, "Capital of France")
	}
	if len(suggestions) != 2 {
		t.Fatalf("suggestions = %v, want 2", suggestions)
	}
	for _, s := range suggestions {
		if strings.TrimSpace(s.Text) == "" {
			t.Errorf("an empty suggestion came back: %v", suggestions)
		}
		if s.IsHit {
			t.Errorf("a fresh suggestion is not a hit: %+v", s)
		}
	}
}

// A model that ignores the instructions is the ordinary case, not an error: the
// answer is its own, whole, and there is simply nothing else to read out.
func TestAnAnswerThatCarriesNothingIsLeftAlone(t *testing.T) {
	const plain = "Paris is the capital of France."
	parsed, suggestions, title, err := parseAnswerWithCarriers(plain, 3, true)
	if err != nil {
		t.Fatalf("parseAnswerWithCarriers: %v", err)
	}
	if parsed != plain {
		t.Errorf("answer = %q, want it untouched", parsed)
	}
	if title != "" {
		t.Errorf("title = %q, want none", title)
	}
	if len(suggestions) != 0 {
		t.Errorf("suggestions = %v, want none", suggestions)
	}
}

// Asked for neither, neither is read: a divider that happens to appear in the
// prose is prose.
func TestNothingAskedForIsNothingParsed(t *testing.T) {
	const withMarkup = "Use ||| to separate columns.=====and ===== for a rule."
	parsed, suggestions, title, err := parseAnswerWithCarriers(withMarkup, 0, false)
	if err != nil {
		t.Fatalf("parseAnswerWithCarriers: %v", err)
	}
	if parsed != withMarkup {
		t.Errorf("answer = %q, want it untouched — nothing was asked for", parsed)
	}
	if title != "" || len(suggestions) != 0 {
		t.Errorf("title=%q suggestions=%v, want neither", title, suggestions)
	}
}

// The question carries the instructions, and the instructions name the dividers
// the parser above splits on. This is the other half of the same contract.
func TestTheQuestionCarriesTheInstructionsTheAnswerIsReadBy(t *testing.T) {
	q, err := getQuestionWithCarriers("What is the capital of France?", 3, true)
	if err != nil {
		t.Fatalf("getQuestionWithCarriers: %v", err)
	}
	if !strings.Contains(q, "What is the capital of France?") {
		t.Error("the question was dropped from the question")
	}
	for _, want := range []string{"|||", "====="} {
		if !strings.Contains(q, want) {
			t.Errorf("the instructions never name %q, which is what the answer is split on", want)
		}
	}

	// Asked for neither, neither divider is named. The question is still wrapped —
	// the language instruction rides along whatever else is asked for — but nothing
	// tells the model to emit a separator, so nothing downstream will split on one.
	bare, err := getQuestionWithCarriers("just this", 0, false)
	if err != nil {
		t.Fatalf("getQuestionWithCarriers: %v", err)
	}
	if !strings.Contains(bare, "just this") {
		t.Errorf("question = %q, want the question in it", bare)
	}
	for _, unwanted := range []string{"|||", "====="} {
		if strings.Contains(bare, unwanted) {
			t.Errorf("question names %q with nothing asked for: %q", unwanted, bare)
		}
	}
}

// A prompt keeps whatever it was given and gains the instructions; an empty one
// is given a default rather than sending the model nothing but markup rules.
func TestAPromptKeepsItselfAndGainsTheInstructions(t *testing.T) {
	got, err := getPromptWithCarrier("You are a cartographer.", 2, true)
	if err != nil {
		t.Fatalf("getPromptWithCarrier: %v", err)
	}
	if !strings.Contains(got, "You are a cartographer.") {
		t.Error("the caller's prompt was dropped")
	}
	if !strings.Contains(got, "|||") {
		t.Error("the instructions did not reach the prompt")
	}

	empty, err := getPromptWithCarrier("", 2, true)
	if err != nil {
		t.Fatalf("getPromptWithCarrier: %v", err)
	}
	if !strings.Contains(strings.ToLower(empty), "expert") {
		t.Errorf("an empty prompt got no default: %q", empty)
	}
}
