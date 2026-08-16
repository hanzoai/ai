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
	"context"
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
)

// A FREE CALL IS COUNTED WHERE A CALL IS RECORDED, AND A CALL IS RECORDED ONLY WHEN
// ONE ANSWERED.
//
// The ceiling exists to bound SPEND. Spend is incurred when a model is reached, so a
// request that reaches none — an unresolvable route, a vendor that timed out, a pod
// being rolled — must cost a caller nothing. It used to cost them a call: the count
// was taken at the door, by the gate, before route selection had even run.
//
// The position is what makes that impossible rather than merely fixed. The count is a
// FIELD on the usage event (object.UsageEvent.Allowance), recordUsage is its only
// producer, and recordUsage returns before it builds one for anything that is not a
// success. There is no verb left that counts a call on its own, so no future edit can
// move counting back in front of the answer without first inventing one.

// counted runs record through the chokepoint and reports every allowance subject the
// events carried. An empty result is a call that counted nothing.
func counted(t *testing.T, record *usageRecord) []string {
	t.Helper()
	var subjects []string
	prev := object.UsageRecorder()
	object.SetUsageRecorder(func(_ context.Context, u object.UsageEvent) error {
		if u.Allowance != "" {
			subjects = append(subjects, u.Allowance)
		}
		return nil
	})
	t.Cleanup(func() { object.SetUsageRecorder(prev) })

	recordUsage(record)
	return subjects
}

// free builds a served call on a route the VENDOR charges nothing for — the free pool
// and the spare routes, which state their own zero (usageCostNano reads record.Free
// first). It is the shape the public lane and every downgrade record carry, and it
// needs no price table, which no unit test has.
func free(owner, user string) *usageRecord {
	zero := int64(0)
	return &usageRecord{
		Owner: owner, User: user, Model: "enso-free", Provider: "hanzo",
		Currency: "USD", Status: "success", RequestID: "req-free",
		Free: true, BilledNanoExact: &zero,
	}
}

// TestNothingIsCountedWithoutAnAnswer is the defect, stated as a property. Every one
// of these is a request that died before a model: the caller must keep every free
// call they arrived with.
func TestNothingIsCountedWithoutAnAnswer(t *testing.T) {
	for _, c := range []struct {
		name   string
		status string
		why    string
	}{
		{"the route did not resolve", "error", "a 404 on a route we had not shipped yet emptied a visitor's day"},
		{"the vendor never answered", "error", "an upstream timeout is our outage, not the caller's usage"},
		{"the call was abandoned", "cancelled", "nothing was served, so nothing was spent"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := free("acme", "acme/alice")
			rec.Status = c.status
			if got := counted(t, rec); len(got) != 0 {
				t.Fatalf("a call that reached no model counted %v — %s", got, c.why)
			}
		})
	}
}

// A served free call IS counted, once, against the caller's own subject. The fix must
// not have quietly removed the ceiling it moved.
func TestAServedFreeCallIsCounted(t *testing.T) {
	got := counted(t, free("acme", "acme/alice"))
	if len(got) != 1 {
		t.Fatalf("a served free call counted %d times, want exactly 1: %v", len(got), got)
	}
	if got[0] != "acme" {
		t.Fatalf("counted against %q, want the caller's own subject acme", got[0])
	}
}

// EXACTLY ONE PER REQUEST. Two call sites would charge one request twice, and the
// caller would be short a call with nothing to show for it — the same defect arriving
// from the opposite side. One request records once, so it counts once.
func TestOneRequestCountsOnce(t *testing.T) {
	var events int
	prev := object.UsageRecorder()
	object.SetUsageRecorder(func(_ context.Context, u object.UsageEvent) error {
		if u.Allowance != "" {
			events++
		}
		return nil
	})
	t.Cleanup(func() { object.SetUsageRecorder(prev) })

	recordUsage(free("acme", "acme/alice"))
	if events != 1 {
		t.Fatalf("one request produced %d counts, want exactly 1", events)
	}
}

// A PRICED CALL IS NEVER COUNTED. Money bounds those, and counting them as well would
// refuse work a subscriber has already paid for.
func TestAPricedCallSpendsMoneyAndNoAllowance(t *testing.T) {
	cent := int64(10_000_000) // $0.01 in nano
	rec := free("acme", "acme/alice")
	rec.Model, rec.Free, rec.BilledNanoExact = "gpt-4o", false, &cent

	if got := counted(t, rec); len(got) != 0 {
		t.Fatalf("a priced call counted %v against the free allowance; the wallet bounds those", got)
	}
}

// A ZERO THAT IS NOT A PRICE IS NOT A FREE CALL.
//
// Transcription and synthesis carry no configured rate — that is a revenue decision
// left open on purpose (sttPricePerMinuteCents) — so an audio call computes a cost of
// zero while being neither free nor priced. Reading that zero as a free call would
// spend a paying customer's daily allowance on the audio traffic they are paying for,
// and empty their free tier by using the product normally.
//
// The same guard covers a vendor that answers an ERROR on a priced model: no tokens
// come back, so the cost computes zero, and only asking what the ROUTE costs tells a
// gift apart from a failure.
func TestAZeroThatIsNotAPriceIsNotAFreeCall(t *testing.T) {
	for _, c := range []struct {
		name string
		of   func(*usageRecord)
	}{
		{
			name: "a transcription with no rate set",
			of:   func(r *usageRecord) { r.Model, r.AudioSeconds = "whisper-1", 42 },
		},
		{
			name: "a synthesis with no rate set",
			of:   func(r *usageRecord) { r.Model, r.AudioChars = "tts-1", 900 },
		},
		{
			name: "a priced model whose vendor answered nothing",
			of:   func(r *usageRecord) { r.Model, r.PromptTokens, r.CompletionTokens = "gpt-4o", 0, 0 },
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := free("acme", "acme/alice")
			rec.Free, rec.BilledNanoExact = false, nil // let the real tables answer
			c.of(rec)

			if usageFree(rec) {
				t.Fatalf("%s reads as a free call; its zero is a missing price, not a price", c.name)
			}
			if got := counted(t, rec); len(got) != 0 {
				t.Fatalf("%s counted %v against the caller's free calls", c.name, got)
			}
		})
	}
}

// And the pool still counts. The guard above must not have taken the ceiling off the
// one lane it exists for: the free pool's routes cost nothing BY DECLARATION and
// appear in no table under the id that served them.
func TestTheFreePoolStillCounts(t *testing.T) {
	rec := free("acme", "acme/alice")
	if !usageFree(rec) {
		t.Fatal("a route the vendor charges nothing for does not read as a free call")
	}
	if got := counted(t, rec); len(got) != 1 {
		t.Fatalf("the free pool counted %d times, want exactly 1: %v", len(got), got)
	}
}

// The PUBLIC lane counts a visitor, not the reserved org every visitor shares. One
// shared subject would let a single caller empty every stranger's day.
//
// The visitor reaches the record the way every other billing fact does: bind, the one
// place a record learns whose call this is.
func TestThePublicLaneCountsTheVisitorItServed(t *testing.T) {
	const visitor = "visitor:9f86d081884c7d659a2feaa0c55ad015"

	rec := free(publicOrg, publicOrg+"/visitor")
	rec.bind(withVisitor(context.Background(), visitor),
		&iam.User{Owner: publicOrg, Type: "application"})

	got := counted(t, rec)
	if len(got) != 1 || got[0] != visitor {
		t.Fatalf("the public lane counted %v, want exactly [%s]", got, visitor)
	}

	// And a caller who named no visitor counts against their payer, which is what
	// every other lane does.
	plain := free("acme", "acme/alice")
	plain.bind(context.Background(), &iam.User{Owner: "acme", Name: "alice"})
	if got := counted(t, plain); len(got) != 1 || got[0] != "acme" {
		t.Fatalf("an ordinary caller counted %v, want [acme]", got)
	}
}

// The lane's OWN count — the half kept in this process, so its ceiling holds while
// the host is unreachable — rises at the same moment and nowhere else.
//
// It keys on the VISITOR the record carries, which only the lane can put there. The
// org would be the obvious test and is the wrong one: it is resolved from X-Org-Id, so
// a caller could name an org and step outside the count that bounds them.
func TestThePublicLanesOwnCountRisesWhenTheCallIsServed(t *testing.T) {
	t.Setenv("PUBLIC_CHAT_DAILY", "5")
	const visitor = "visitor:1111111111111111111111111111111"

	saved := publicCount
	publicCount = &dayCount{}
	t.Cleanup(func() { publicCount = saved })

	served := free(publicOrg, publicOrg+"/visitor")
	served.bind(withVisitor(context.Background(), visitor),
		&iam.User{Owner: publicOrg, Type: "application"})
	counted(t, served)
	if n := publicCount.seen[visitor]; n != 1 {
		t.Fatalf("a served public call left the lane's own count at %d, want 1", n)
	}

	// A call that died costs the visitor nothing here either.
	failed := free(publicOrg, publicOrg+"/visitor")
	failed.Status = "error"
	failed.bind(withVisitor(context.Background(), visitor),
		&iam.User{Owner: publicOrg, Type: "application"})
	counted(t, failed)
	if n := publicCount.seen[visitor]; n != 1 {
		t.Fatalf("a failed public call moved the lane's own count to %d, want it left at 1", n)
	}

	// A NAMED ORG DOES NOT STEP OUT OF THE LANE. The tool paths re-derive the ledger
	// from X-Org-Id, so a visitor carrying a credential can put a real org on the
	// record. The visitor is still the visitor, and the count still binds.
	steered := free("acme", "acme/alice")
	steered.bind(withVisitor(context.Background(), visitor),
		&iam.User{Owner: publicOrg, Type: "application"})
	counted(t, steered)
	if n := publicCount.seen[visitor]; n != 2 {
		t.Fatalf("a call recorded against a named org left the visitor's count at %d, want 2 — "+
			"the lane's bound must not be steerable by a header", n)
	}
}
