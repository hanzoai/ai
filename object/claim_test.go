// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package object

import "testing"

// One turn, one generation, one debit. The claim is what makes that true, and
// these are the four states it has to distinguish — a generation in flight, one
// that failed, one that finished, and one whose process died.
func TestOnlyOneRequestGeneratesAnAnswer(t *testing.T) {
	withStore(t)
	message := &Message{
		Owner: "admin", Name: "m-1", Organization: "acme", Chat: "c", User: "alice",
		Author: "AI", ReplyTo: "q-1", Text: "",
	}
	if _, err := AddMessage(message); err != nil {
		t.Fatal(err)
	}
	claim := func() bool {
		got, err := GetMessage("admin/m-1")
		if err != nil {
			t.Fatal(err)
		}
		took, err := ClaimMessageAnswer(got)
		if err != nil {
			t.Fatal(err)
		}
		return took
	}

	if !claim() {
		t.Fatal("the first request could not take the answer")
	}
	// In flight: the answer is still empty, which is exactly when a second request
	// looks identical to the first.
	if claim() {
		t.Fatal("a second request took an answer already being generated")
	}

	// A generation that ended without an answer is retryable at once.
	ReleaseMessageAnswer(message)
	if !claim() {
		t.Fatal("a released claim could not be retaken")
	}

	// A generation that TERMINATED is never retaken, even having produced nothing —
	// the text is still empty, so only answered_time can say so.
	if err := SettleMessageAnswer(message); err != nil {
		t.Fatal(err)
	}
	if claim() {
		t.Fatal("a settled answer was generated again")
	}
	// And settling is what a release must not undo.
	ReleaseMessageAnswer(message)
	if claim() {
		t.Fatal("releasing after settling reopened the answer")
	}
}
