// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"context"
	"testing"

	"github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
)

// A program in this process that meters its own call is told what the call cost:
// the amount the same answer debits any other caller. The usage row still names the
// caller, and no wallet is drawn here — the program charges its customer exactly
// this, so an in-process call has one price, the one every caller pays.
func TestAProgramIsToldWhatItsCallCost(t *testing.T) {
	var events []object.UsageEvent
	prev := object.UsageRecorder()
	object.SetUsageRecorder(func(_ context.Context, u object.UsageEvent) error { events = append(events, u); return nil })
	t.Cleanup(func() { object.SetUsageRecorder(prev) })

	bill := &object.Bill{}
	g := &object.LimitGrant{Plan: object.PaysCaller, Pays: object.PaysCaller, Settle: func(int64) {}, Bill: bill}
	rec := &usageRecord{Owner: "acme", Model: "kai", Provider: object.KaiName, PromptTokens: 1_000_000, Status: "success", Currency: "USD"}
	rec.bind(withGrant(g), &iam.User{Owner: "acme", Name: "ann"})
	if err := recordUsage(rec); err != nil {
		t.Fatal(err)
	}
	want := usageMargin(rec).BilledNano
	if want <= 0 {
		t.Fatalf("kai bills %d nano for a million tokens; the test needs a priced model", want)
	}
	if got, told := bill.Nanos(); !told || got != want {
		t.Fatalf("the program was told %d nano (told %v), want %d — what the same answer debits anyone else", got, told, want)
	}
	if len(events) != 1 || !events[0].Plan || events[0].PaidBy != object.PaysCaller {
		t.Fatalf("usage %+v, want one row the caller pays, drawing no wallet", events)
	}
}

// A grant that is not the caller's tells nobody: a plan, the free cap and a wallet
// are charged here, and a Bill on them would be a second price.
func TestOnlyACallersGrantIsToldItsPrice(t *testing.T) {
	prev := object.UsageRecorder()
	object.SetUsageRecorder(func(context.Context, object.UsageEvent) error { return nil })
	t.Cleanup(func() { object.SetUsageRecorder(prev) })

	bill := &object.Bill{}
	g := &object.LimitGrant{Pays: object.PaysPlan, Settle: func(int64) {}, Bill: bill}
	rec := &usageRecord{Owner: "acme", Model: "kai", Provider: object.KaiName, PromptTokens: 1_000_000, Status: "success", Currency: "USD"}
	rec.bind(withGrant(g), &iam.User{Owner: "acme", Name: "ann"})
	if err := recordUsage(rec); err != nil {
		t.Fatal(err)
	}
	if _, told := bill.Nanos(); told {
		t.Fatal("a plan-covered call told a bill: the plan is charged here")
	}
}

// PriceOf is the price list a program reserves against before it calls: the same
// rows every caller is billed from, nothing invented for a model nobody priced.
func TestPriceOfIsTheBilledRow(t *testing.T) {
	r, ok := PriceOf("kai", "")
	if !ok {
		t.Fatal("kai has a price row")
	}
	p := getModelPrice("kai")
	if r.Input != p.InputPerMillion || r.Output != p.OutputPerMillion {
		t.Fatalf("PriceOf(kai) = %+v, want the billed row %+v", r, p)
	}
	if _, ok := PriceOf("no-such-model-anywhere", ""); ok {
		t.Fatal("an unpriced model read as priced: the conservative default is not a price")
	}
}
