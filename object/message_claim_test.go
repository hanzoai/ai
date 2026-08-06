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
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/ai/util"
)

// answerable seeds one unanswered AI message on a fresh in-memory database and
// returns a reader for it — a real row, so the claim is exercised as the single
// conditional UPDATE it is, against a real SQL engine.
func answerable(t *testing.T, dsn string) func() *Message {
	t.Helper()
	restore, err := UseMemoryDB(dsn, &Message{}, &Chat{})
	if err != nil {
		t.Fatalf("UseMemoryDB: %v", err)
	}
	t.Cleanup(restore)

	// One connection, because shared-cache SQLite takes a TABLE lock and answers a
	// second concurrent writer with "database table is locked" rather than serializing
	// it. That error would decide the race for reasons that have nothing to do with
	// the claim. Pooling to one connection makes the engine queue the writers, which
	// is what a real one does — and it leaves the property under test untouched: the
	// racers still read the row concurrently, and it is the UPDATE's own condition,
	// not the pool, that lets exactly one through. A read-then-write in its place
	// still loses here, because every racer reads an unanswered message before any of
	// them writes.
	adapter.RawDB().SetMaxOpenConns(1)

	seed := &Message{
		Owner: "admin", Name: "message_1", User: "alice", Author: "AI",
		Chat: "chat_1", ReplyTo: "message_0", CreatedTime: util.GetCurrentTime(),
	}
	if _, err := AddMessage(seed); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	return func() *Message {
		got, err := GetMessage(seed.GetId())
		if err != nil {
			t.Fatalf("GetMessage: %v", err)
		}
		return got
	}
}

// TestConcurrentAnswersClaimOnce is the money invariant of a chat answer under
// concurrency: however many requests ask for one message's answer at the same
// instant, exactly ONE may generate it.
//
// The guard this replaced read the answer and acted on it as two steps, so a second
// tab or an SSE reconnect landed in the gap: both requests saw an unanswered message,
// both ran the model, both charged. Nothing downstream collapses that pair — the
// ledger mints its own id per debit — so two claims would be two invoices.
//
// Drop the atomic claim for the read-then-write it replaced and this sees N winners.
func TestConcurrentAnswersClaimOnce(t *testing.T) {
	read := answerable(t, "file:claimonce?mode=memory&cache=shared")

	const racers = 8
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	done.Add(racers)

	won := make([]bool, racers)
	errs := make([]error, racers)
	for i := 0; i < racers; i++ {
		go func(i int) {
			defer done.Done()
			start.Wait()
			won[i], errs[i] = ClaimMessageAnswer(read())
		}(i)
	}
	start.Done()
	done.Wait()

	winners := 0
	for i, ok := range won {
		if errs[i] != nil {
			t.Fatalf("racer %d: %v", i, errs[i])
		}
		if ok {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("%d of %d concurrent requests claimed one message's answer, want exactly 1 — every extra winner is a second completion AND a second charge", winners, racers)
	}
	if got := read().ClaimedTime; got == "" {
		t.Error("the winner left no claim on the row; the next request would generate the same answer again")
	}
}

// TestClaimIsRefusedWhileHeld covers the sequential shape of the same thing: a
// second request arriving while the first is still generating is refused.
func TestClaimIsRefusedWhileHeld(t *testing.T) {
	read := answerable(t, "file:claimheld?mode=memory&cache=shared")

	first, err := ClaimMessageAnswer(read())
	if err != nil || !first {
		t.Fatalf("first claim = %v, %v; want true, nil", first, err)
	}
	second, err := ClaimMessageAnswer(read())
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if second {
		t.Error("a message already being answered was claimed again — two completions, two charges")
	}
}

// TestClaimIsRefusedOnceAnswered pins the condition the claim shares with the guard
// it replaced: an answered message is never generated again.
func TestClaimIsRefusedOnceAnswered(t *testing.T) {
	read := answerable(t, "file:claimanswered?mode=memory&cache=shared")

	answered := read()
	answered.Text = "42"
	if _, err := UpdateMessage(answered.GetId(), answered, false); err != nil {
		t.Fatalf("UpdateMessage: %v", err)
	}

	claimed, err := ClaimMessageAnswer(read())
	if err != nil {
		t.Fatalf("ClaimMessageAnswer: %v", err)
	}
	if claimed {
		t.Error("an answered message was claimed for a second generation")
	}
}

// TestReleaseMakesAFailedAnswerRetryable covers the failure path: a generation that
// produced no answer hands the message back at once, rather than parking it for the
// length of the lease.
func TestReleaseMakesAFailedAnswerRetryable(t *testing.T) {
	read := answerable(t, "file:claimrelease?mode=memory&cache=shared")

	held := read()
	if claimed, err := ClaimMessageAnswer(held); err != nil || !claimed {
		t.Fatalf("first claim = %v, %v", claimed, err)
	}
	ReleaseMessageAnswer(held)

	if got := read().ClaimedTime; got != "" {
		t.Errorf("release left claim %q on the row", got)
	}
	if claimed, err := ClaimMessageAnswer(read()); err != nil || !claimed {
		t.Errorf("a released message must be answerable again, got %v, %v", claimed, err)
	}
}

// TestReleaseCannotDisturbALandedAnswer is why release is safe on every exit path,
// including the one where the answer succeeded: it is conditional on the answer still
// being empty, so it never rewrites a message that has one.
func TestReleaseCannotDisturbALandedAnswer(t *testing.T) {
	read := answerable(t, "file:claimlanded?mode=memory&cache=shared")

	held := read()
	if claimed, err := ClaimMessageAnswer(held); err != nil || !claimed {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	held.Text = "42"
	held.ClaimedTime = ""
	if _, err := UpdateMessage(held.GetId(), held, false); err != nil {
		t.Fatalf("UpdateMessage: %v", err)
	}

	ReleaseMessageAnswer(held)

	after := read()
	if after.Text != "42" {
		t.Errorf("release disturbed the landed answer: text = %q, want %q", after.Text, "42")
	}
	if claimed, _ := ClaimMessageAnswer(read()); claimed {
		t.Error("an answered message became claimable after a release")
	}
}

// TestStaleClaimIsReclaimable pins the lease: a claim older than answerLease no
// longer holds, so a generator that died without releasing costs a delay rather than
// a message nobody can ever answer.
func TestStaleClaimIsReclaimable(t *testing.T) {
	read := answerable(t, "file:claimstale?mode=memory&cache=shared")

	dead := read()
	dead.ClaimedTime = util.GetTimeAgo(answerLease + time.Minute)
	if _, err := UpdateMessage(dead.GetId(), dead, false); err != nil {
		t.Fatalf("UpdateMessage: %v", err)
	}

	claimed, err := ClaimMessageAnswer(read())
	if err != nil {
		t.Fatalf("ClaimMessageAnswer: %v", err)
	}
	if !claimed {
		t.Error("a claim older than the lease still held the message; a crashed generator would strand it forever")
	}
}

// TestFreshClaimIsNotStale is the other side of the lease: a claim taken just now is
// still held, so the lease cannot be the way a racer wins.
func TestFreshClaimIsNotStale(t *testing.T) {
	read := answerable(t, "file:claimfresh?mode=memory&cache=shared")

	recent := read()
	recent.ClaimedTime = util.GetTimeAgo(answerLease - time.Minute)
	if _, err := UpdateMessage(recent.GetId(), recent, false); err != nil {
		t.Fatalf("UpdateMessage: %v", err)
	}

	if claimed, _ := ClaimMessageAnswer(read()); claimed {
		t.Error("a claim inside the lease was taken from its holder")
	}
}
