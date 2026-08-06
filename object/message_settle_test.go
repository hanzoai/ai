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

package object

import (
	"testing"
	"time"

	"github.com/hanzoai/ai/util"
)

// TestAnEmptyAnswerIsNotGeneratedTwice is the invariant the claim alone could not
// hold. The claim's condition is an empty text column, and a completion that produced
// no text satisfies it forever — a tool-call-only turn, a response the provider
// filtered, a carrier parse that stripped everything, or a persist that failed after
// the debit landed. So the row read as unanswered, the next request won the claim, ran
// the model again and took a second debit. The debit is not idempotent (the ledger
// mints its own entry per call), so every repeat is another invoice for one turn.
//
// Termination is the fact that settles it, and it is recorded separately from the text
// precisely because there is no text. Drop the answered_time clause from
// ClaimMessageAnswer and this sees the second generation.
func TestAnEmptyAnswerIsNotGeneratedTwice(t *testing.T) {
	read := answerable(t, "file:claimsettled?mode=memory&cache=shared")

	held := read()
	if claimed, err := ClaimMessageAnswer(held); err != nil || !claimed {
		t.Fatalf("first claim = %v, %v; want true, nil", claimed, err)
	}
	if err := SettleMessageAnswer(held); err != nil {
		t.Fatalf("SettleMessageAnswer: %v", err)
	}

	// The premise: the row really is still textless. Without this the test could pass
	// for the old reason (a non-empty answer) and prove nothing about termination.
	if got := read().Text; got != "" {
		t.Fatalf("the row under test must still be textless, got %q", got)
	}

	if claimed, _ := ClaimMessageAnswer(read()); claimed {
		t.Error("a terminated generation was claimed again — a second completion AND a second charge for one turn")
	}
}

// TestASettledAnswerOutlivesItsLease is the second window. The lease exists for a
// generator that DIED mid-answer: past answerLease the message is claimable again so a
// crash costs a delay rather than a message nobody can answer. A generation that
// FINISHED is not that, and must not be handed to the next request when its claim
// lapses — otherwise an empty answer is re-billable every fifteen minutes, forever.
func TestASettledAnswerOutlivesItsLease(t *testing.T) {
	read := answerable(t, "file:claimsettledstale?mode=memory&cache=shared")

	held := read()
	if claimed, err := ClaimMessageAnswer(held); err != nil || !claimed {
		t.Fatalf("first claim = %v, %v", claimed, err)
	}
	if err := SettleMessageAnswer(held); err != nil {
		t.Fatalf("SettleMessageAnswer: %v", err)
	}

	stale := read()
	stale.ClaimedTime = util.GetTimeAgo(answerLease + time.Minute)
	if _, err := UpdateMessage(stale.GetId(), stale, false); err != nil {
		t.Fatalf("UpdateMessage: %v", err)
	}

	if claimed, _ := ClaimMessageAnswer(read()); claimed {
		t.Error("an expired lease handed a FINISHED generation to a second request — one turn, another invoice every lease")
	}
}

// TestReleaseCannotUnsettleATerminatedAnswer covers the exit path the answer handler
// actually takes: it settles, then returns, and the deferred release runs. Release is
// conditional on the text still being empty, which a settled EMPTY answer is — so if
// settling lived in the claim column alone, the release would immediately undo it.
func TestReleaseCannotUnsettleATerminatedAnswer(t *testing.T) {
	read := answerable(t, "file:claimsettledrelease?mode=memory&cache=shared")

	held := read()
	if claimed, err := ClaimMessageAnswer(held); err != nil || !claimed {
		t.Fatalf("first claim = %v, %v", claimed, err)
	}
	if err := SettleMessageAnswer(held); err != nil {
		t.Fatalf("SettleMessageAnswer: %v", err)
	}

	ReleaseMessageAnswer(held)

	if claimed, _ := ClaimMessageAnswer(read()); claimed {
		t.Error("the deferred release handed a terminated generation back for a second completion and a second charge")
	}
}

// TestSettlingDoesNotStrandAFailedGeneration is the other side: a generation that
// failed BEFORE it terminated releases and is retryable at once, exactly as before.
// Settling must refuse the repeat, not the retry.
func TestSettlingDoesNotStrandAFailedGeneration(t *testing.T) {
	read := answerable(t, "file:claimsettledretry?mode=memory&cache=shared")

	held := read()
	if claimed, err := ClaimMessageAnswer(held); err != nil || !claimed {
		t.Fatalf("first claim = %v, %v", claimed, err)
	}
	ReleaseMessageAnswer(held)

	if claimed, err := ClaimMessageAnswer(read()); err != nil || !claimed {
		t.Errorf("a generation that failed before terminating must be retryable at once, got %v, %v", claimed, err)
	}
}

// TestUpdateMessageSurvivesAMissingRow pins the nil-deref: getMessage answers
// (nil, nil) for an id no row matches, so the miss arrives as a nil origin and not as
// an error, and reading TextTokenCount off it panics the process. This route is
// reachable without a session, so the panic is an unauthenticated crash.
func TestUpdateMessageSurvivesAMissingRow(t *testing.T) {
	answerable(t, "file:updatemissing?mode=memory&cache=shared")

	ok, err := UpdateMessage("admin/message_nope", &Message{Owner: "admin", Name: "message_nope"}, false)
	if err != nil {
		t.Fatalf("updating a message that does not exist must not error, got %v", err)
	}
	if ok {
		t.Error("updating a message that does not exist reported success")
	}
}
