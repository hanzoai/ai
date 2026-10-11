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
	"math/big"
	"net/http"
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
)

func withGrant(g *object.LimitGrant) context.Context {
	return context.WithValue(context.Background(), planKey{}, g)
}

// A request the plan or a free cap pays for holds nothing against the wallet, so an
// empty one admits it; a request the wallet pays is held as before.
func TestACoveredRequestHoldsNothingAgainstTheWallet(t *testing.T) {
	const subject = "policy-hold"
	object.GlobalBalanceLedger.SetBalance(subject, 0)
	t.Cleanup(func() { object.GlobalBalanceLedger.SetBalance(subject, 0) })

	for _, pays := range []string{object.PaysPlan, object.PaysFree} {
		if _, ok := reserveFor(withGrant(&object.LimitGrant{Pays: pays}), subject, 5); !ok {
			t.Errorf("a request %s pays for was refused at an empty wallet", pays)
		}
	}
	if _, ok := reserveFor(withGrant(&object.LimitGrant{Pays: object.PaysPrepaid}), subject, 5); ok {
		t.Error("a request prepaid pays for was admitted at an empty wallet")
	}
	if _, ok := reserveFor(context.Background(), subject, 5); ok {
		t.Error("a request nothing covers was admitted at an empty wallet")
	}
}

// The in-controller gate leaves a covered request alone and refuses an uncovered
// one at an empty wallet.
func TestTheControllerGateLeavesACoveredRequestAlone(t *testing.T) {
	user := &iam.User{Owner: "policy-gate", Name: "ann"}
	object.GlobalBalanceLedger.SetBalance(user.PayerSubject("policy-gate"), 0)
	if err := enforceBalanceGate(withGrant(&object.LimitGrant{Pays: object.PaysPlan}), user, "policy-gate", "kai"); err != nil {
		t.Fatalf("a covered decision was refused: %v", err)
	}
}

// A usage record carries who the host said pays: a covered call settles against its
// grant and debits no wallet; a wallet-paid call carries whether only cash may pay.
func TestARecordCarriesWhoPays(t *testing.T) {
	user := &iam.User{Owner: "acme", Name: "ann"}

	covered := &object.LimitGrant{Pays: object.PaysPlan, Settle: func(int64) {}}
	r := &usageRecord{Owner: "acme"}
	r.bind(withGrant(covered), user)
	if r.plan != covered || r.cash {
		t.Errorf("covered: plan %v cash %v", r.plan, r.cash)
	}

	r = &usageRecord{Owner: "acme"}
	r.bind(withGrant(&object.LimitGrant{Pays: object.PaysPrepaid, Cash: true}), user)
	if r.plan != nil || !r.cash {
		t.Errorf("prepaid, cash only: plan %v cash %v", r.plan, r.cash)
	}

	r = &usageRecord{Owner: "acme"}
	r.bind(withGrant(&object.LimitGrant{Pays: object.PaysCredits}), user)
	if r.plan != nil || r.cash {
		t.Errorf("credits: plan %v cash %v", r.plan, r.cash)
	}
}

// A covered call settles its list price against the plan — what the caller would
// have paid — and a model sold at zero settles what its paid upstream cost. The
// debit names the plan, so the host draws no wallet; a cash-only call says so.
func TestACoveredCallSettlesItsListPrice(t *testing.T) {
	var events []object.UsageEvent
	prev := object.UsageRecorder()
	object.SetUsageRecorder(func(_ context.Context, u object.UsageEvent) error { events = append(events, u); return nil })
	t.Cleanup(func() { object.SetUsageRecorder(prev) })

	var settled []int64
	g := &object.LimitGrant{Pays: object.PaysPlan, Settle: func(n int64) { settled = append(settled, n) }}
	rec := &usageRecord{Owner: "acme", Model: "kai", Provider: object.KaiName, PromptTokens: 1_000_000, Status: "success", Currency: "USD"}
	rec.bind(withGrant(g), &iam.User{Owner: "acme", Name: "ann"})
	if err := recordUsage(rec); err != nil {
		t.Fatal(err)
	}
	if len(settled) != 1 || settled[0] != 21_000_000 {
		t.Fatalf("kai, a million tokens, settled %v nano, want [21000000] ($0.021, its list price)", settled)
	}
	if len(events) != 1 || !events[0].Plan {
		t.Fatalf("debit %+v, want one naming the plan", events)
	}

	events = nil
	rec = &usageRecord{Owner: "acme", Model: "typesafe/jev-1.13", Provider: object.KaiName, PromptTokens: 1_000_000, Status: "success", Currency: "USD"}
	rec.bind(withGrant(&object.LimitGrant{Pays: object.PaysPrepaid, Cash: true}), &iam.User{Owner: "acme", Name: "ann"})
	if err := recordUsage(rec); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Plan || !events[0].Cash || events[0].USD != "0.042" {
		t.Fatalf("jev paid in cash: %+v, want a cash-only debit of $0.042", events)
	}
}

// Kai is sold at half of Jev for the same work.
func TestKaiIsHalfOfJev(t *testing.T) {
	kai, jev := decisionCostNano("kai", 1_000_000, 0), decisionCostNano("typesafe/jev-1.13", 1_000_000, 0)
	if kai <= 0 || kai*2 != jev || decisionCostNano("~typesafe/jev-latest", 1_000_000, 0) != jev {
		t.Fatalf("a million tokens: kai %d nano, jev %d nano; want kai at half of jev", kai, jev)
	}
}

// An LLM decision's completion bills at the model's output price; Kai's and Jev's
// output is priced at zero, so the same tokens bill them exactly as before. A
// negative count credits nothing.
func TestADecisionBillsItsOutputWhereItIsPriced(t *testing.T) {
	for _, id := range []string{"zenlm/zen6", "zenlm/zen6-flash"} {
		in := decisionCostNano(id, 1_000_000, 0)
		if out := decisionCostNano(id, 1_000_000, decisionOutputTokens) - in; in != 2_280_000_000 || out != decisionOutputTokens*7260 {
			t.Errorf("%s: a million in %d nano, %d out %d more; want $2.28 a million and $7.26 a million", id, in, decisionOutputTokens, out)
		}
		// Output past what a completion is asked for bills the ceiling, and a count
		// whose product would overflow bills a bounded, positive amount.
		if got, ceil := decisionCostNano(id, 1000, 1_000_000_000), decisionCostNano(id, 1000, decisionOutputTokens); got != ceil {
			t.Errorf("%s: 1e9 output tokens bill %d nano, want the ceiling's %d", id, got, ceil)
		}
		// 1<<62 times 2,280 wraps to exactly 0, so an odd count is what shows the bound,
		// and the ceiling is held to the exact product, which int64 must hold.
		ceil := new(big.Int).Add(new(big.Int).Mul(big.NewInt(decisionInputTokens), big.NewInt(2280)),
			big.NewInt(decisionOutputTokens*7260))
		if got := decisionCostNano(id, 1<<62+1, 1<<62+1); !ceil.IsInt64() || got != ceil.Int64() {
			t.Errorf("%s: an overflowing count bills %d nano, want the ceiling's %s", id, got, ceil)
		}
		if got := decisionCostNano(id, 1000, -1000); got != 1000*2280 {
			t.Errorf("%s: negative output moved the bill to %d nano", id, got)
		}
	}
	for id, perToken := range map[string]int64{"kai": 21, "typesafe/jev-1.13": 42, "~typesafe/jev-latest": 42} {
		if got := decisionCostNano(id, 1000, decisionOutputTokens); got != 1000*perToken {
			t.Errorf("%s: 1000 in and %d out bill %d nano, want %d: its output is free", id, decisionOutputTokens, got, 1000*perToken)
		}
	}
}

// A Free plan is never handed an LLM decision for nothing. The model is priced, so
// the host's policy is asked it as a priced call of class ours, never the free
// class, and no decision model reads free to the balance gate. A plan that covers
// it settles its list price, output included; an empty wallet nothing covers is
// refused.
func TestAnLLMDecisionIsNeverFree(t *testing.T) {
	const org = "policy-llm"
	for _, id := range []string{"zenlm/zen6", "zenlm/zen6-flash"} {
		if costsNothing(id, org) || ClassOf(id) != object.ClassOurs {
			t.Errorf("%s: free %v, class %q; want priced, ours", id, costsNothing(id, org), ClassOf(id))
		}
	}
	if DecisionFree(org) {
		t.Fatal("a decision model reads free to the balance gate")
	}

	var events []object.UsageEvent
	prevUsage := object.UsageRecorder()
	object.SetUsageRecorder(func(_ context.Context, u object.UsageEvent) error { events = append(events, u); return nil })
	t.Cleanup(func() { object.SetUsageRecorder(prevUsage) })
	var settled []int64
	g := &object.LimitGrant{Pays: object.PaysPlan, Settle: func(n int64) { settled = append(settled, n) }}
	user := &iam.User{Owner: org, Name: "ann"}
	rec := &usageRecord{Owner: org, Model: "zenlm/zen6", Provider: object.KaiName, PromptTokens: 319, CompletionTokens: 23,
		DecisionCount: 1, Status: "success", Currency: "USD"}
	rec.bind(withGrant(g), user)
	if err := recordUsage(rec); err != nil {
		t.Fatal(err)
	}
	if want := int64(319*2280 + 23*7260); len(settled) != 1 || settled[0] != want {
		t.Fatalf("a plan-covered zenlm/zen6 decision settled %v nano, want [%d]: its list price, output included", settled, want)
	}

	prevBalance := object.BalanceReader()
	object.SetBalanceReader(balReader(0, nil))
	t.Cleanup(func() { object.SetBalanceReader(prevBalance) })
	object.GlobalBalanceLedger.SetBalance(user.PayerSubject(org), 0)
	err := enforceBalanceGate(context.Background(), user, org, "zenlm/zen6")
	if statusOf(err) != http.StatusPaymentRequired {
		t.Fatalf("an empty wallet nothing covers asked zenlm/zen6 => %v, want 402", err)
	}
}
