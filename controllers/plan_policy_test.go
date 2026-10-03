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
	kai, jev := decisionCostNano("kai", 1_000_000), decisionCostNano("typesafe/jev-1.13", 1_000_000)
	if kai <= 0 || kai*2 != jev || decisionCostNano("~typesafe/jev-latest", 1_000_000) != jev {
		t.Fatalf("a million tokens: kai %d nano, jev %d nano; want kai at half of jev", kai, jev)
	}
}
