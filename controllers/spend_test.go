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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

// WHAT THE LEDGER OWES AN ANSWER, IT ALSO OWES AN ATTEMPT.
//
// A request that reached a vendor was executed by that vendor and invoiced to us,
// whatever came back down the wire. The row is the only place that fact survives, so
// the row exists — and it carries the same number the o11y span carries, because both
// ask usageCostCents. A request that never reached one spent nothing and files
// nothing: there is no amount to invent for it.
//
// These drive the chokepoint itself (recordUsage) rather than a handler, because the
// chokepoint is where the rule lives and every surface — chat, messages, embeddings,
// audio, the ZAP twins — arrives through it.

// spend runs one record through the chokepoint against the NATIVE recorder — the
// in-proc finance debit the cloud unified binary installs — and reports every event
// the ledger saw.
func spend(t *testing.T, record *usageRecord) []object.UsageEvent {
	t.Helper()
	var events []object.UsageEvent
	prev := object.UsageRecorder()
	object.SetUsageRecorder(func(_ context.Context, u object.UsageEvent) error {
		events = append(events, u)
		return nil
	})
	t.Cleanup(func() { object.SetUsageRecorder(prev) })

	recordUsage(record)
	return events
}

// enqueued runs one record through the chokepoint against the OTHER writer — the HTTP
// billing queue standalone ai falls back to when no native recorder is installed — and
// reports the bodies Commerce actually received. It is a real POST to a real listener:
// the payload is hand-built inside recordUsage, so only reading it off the wire proves
// which fields survived.
func enqueued(t *testing.T, record *usageRecord) []map[string]any {
	t.Helper()
	bodies := make(chan []byte, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies <- b
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	prevRec := object.UsageRecorder()
	object.SetUsageRecorder(nil) // force the fallback; the native hook short-circuits it
	prevQ := billingQueue
	billingQueue = util.NewBillingQueue(srv.URL, "")
	t.Cleanup(func() {
		billingQueue.Shutdown()
		billingQueue = prevQ
		object.SetUsageRecorder(prevRec)
	})

	recordUsage(record)

	// Both writers are in-process, so a body that is coming arrives immediately. Waiting
	// out a fixed settling time would spend it on every call whether or not anything is
	// in flight; going quiet is the actual signal, and the longer wait is only the bound
	// on a machine slow enough that "immediately" is not.
	deadline := time.After(2 * time.Second)
	var out []map[string]any
	for {
		select {
		case b := <-bodies:
			var m map[string]any
			if err := json.Unmarshal(b, &m); err != nil {
				t.Fatalf("commerce received a body it cannot read: %v", err)
			}
			out = append(out, m)
		case <-time.After(50 * time.Millisecond):
			return out
		case <-deadline:
			return out
		}
	}
}

// report prints what one record wrote on both paths. It is how the four cases below
// are read side by side rather than as four assertion failures in isolation.
func report(t *testing.T, what string, record *usageRecord) []object.UsageEvent {
	t.Helper()
	events := spend(t, record)
	t.Logf("%s: model=%q provider=%q status=%q tokens=%d/%d",
		what, record.Model, record.Provider, record.Status,
		record.PromptTokens, record.CompletionTokens)
	if len(events) == 0 {
		t.Logf("%s: LEDGER WROTE NOTHING", what)
	}
	for _, e := range events {
		t.Logf("%s: ledger usd=%q allowance=%q subject=%q model=%q provider=%q",
			what, e.USD, e.Allowance, e.Subject, e.Model, e.Provider)
	}
	for _, p := range enqueued(t, record) {
		t.Logf("%s: commerce amount=%v allowance=%v status=%v",
			what, p["amount"], p["allowance"], p["status"])
	}
	return events
}

// priced is a served call on an ordinary paid model, carrying the tokens a vendor
// reported. Nothing about the money is stated: the rate tables answer, exactly as they
// do in production, so what these tests pin is the RULE and not a number that would
// have to be edited the day a rate moves.
func priced(owner, user string) *usageRecord {
	return &usageRecord{
		Owner: owner, User: user, Model: "gpt-4o", Provider: "openai",
		PromptTokens: 900, CompletionTokens: 120, TotalTokens: 1020,
		Currency: "USD", Status: "success", RequestID: "req-priced",
	}
}

// TestAPricedCallIsBilledWhatItCost is the control: the case that already worked, so a
// change to the failing cases below cannot be read as having moved it.
func TestAPricedCallIsBilledWhatItCost(t *testing.T) {
	rec := priced("acme", "acme/alice")
	events := report(t, "priced success", rec)
	if len(events) != 1 {
		t.Fatalf("a served priced call wrote %d ledger rows, want exactly 1", len(events))
	}
	if events[0].USD == "0" {
		t.Fatal("a call on a paid model billed nothing")
	}
	if want := usageBilledUSD(rec); events[0].USD != want {
		t.Fatalf("billed %q, want %q — the ledger and the span read ONE cost", events[0].USD, want)
	}
	if events[0].Allowance != "" {
		t.Fatalf("a priced call counted %q against the free allowance; the wallet bounds those", events[0].Allowance)
	}
}

// TestAFreeCallCarriesTheAllowanceItSpent: on the free pool there is no money to bill,
// so the allowance IS the record of what the call consumed. It has to survive to
// whichever writer is installed — a fact that reaches one of the two and not the other
// is a fact the business cannot ask about.
func TestAFreeCallCarriesTheAllowanceItSpent(t *testing.T) {
	events := report(t, "free success", free("acme", "acme/alice"))
	if len(events) != 1 || events[0].Allowance != "acme" {
		t.Fatalf("the native ledger saw %+v, want one row counting acme", events)
	}

	payloads := enqueued(t, free("acme", "acme/alice"))
	if len(payloads) != 1 {
		t.Fatalf("commerce received %d bodies, want exactly 1", len(payloads))
	}
	if got := payloads[0]["allowance"]; got != "acme" {
		t.Fatalf("commerce received allowance=%v, want acme — a free call whose row does not "+
			"say which allowance it spent leaves free-tier consumption invisible", got)
	}
}

// TestAVendorReachedIsAVendorBilled is the defect. The vendor ran the request and
// invoiced us for the tokens it processed; the answer died on the way back. Recording
// nothing puts that spend on us, silently, once per failure.
func TestAVendorReachedIsAVendorBilled(t *testing.T) {
	rec := priced("acme", "acme/alice")
	rec.Status, rec.ErrorMsg = "error", "upstream closed the stream"
	rec.RequestID = "req-died-late"

	events := report(t, "failed after the vendor answered", rec)
	if len(events) != 1 {
		t.Fatalf("a call the vendor ran and charged us for wrote %d ledger rows, want exactly 1", len(events))
	}
	// The SAME tokens, so the SAME money. Not a penalty and not a discount: the meter
	// is read once and the outcome does not enter into it.
	if want := usageBilledUSD(priced("acme", "acme/alice")); events[0].USD != want {
		t.Fatalf("billed %q for a failure, want %q — what a success on the same tokens bills",
			events[0].USD, want)
	}
}

// TestNothingIsBilledWithoutAVendor is the other half, and it is what keeps the rule
// above from becoming "bill everything". Nothing was sent, so nobody invoiced us, so
// there is no amount — and an amount is not something to invent because a row would
// look tidier with one.
//
// The record says which it was in the field that names the vendor: Provider is set
// from the row that served or refused, and is empty when the request died before any
// vendor was committed to it.
func TestNothingIsBilledWithoutAVendor(t *testing.T) {
	for _, c := range []struct {
		name string
		why  string
	}{
		{"the route did not resolve", "no vendor was ever chosen, so none of them ran anything"},
		{"the caller hung up first", "the request never left, and an empty room costs nothing to not answer"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := priced("acme", "acme/alice")
			rec.Provider, rec.Status = "", "error"
			rec.ErrorMsg, rec.RequestID = c.name, "req-never-sent"

			if events := report(t, c.name, rec); len(events) != 0 {
				t.Fatalf("a call that reached no vendor billed %+v — %s", events, c.why)
			}
		})
	}
}
