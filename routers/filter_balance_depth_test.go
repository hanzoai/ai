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

package routers

import (
	stdcontext "context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hanzoai/ai/controllers"
	"github.com/hanzoai/ai/object"
)

// tierGate stands the gate up over one Enso tier, vendor/priced, which enso-auto
// routes to, and one caller, acme/ann.
func tierGate(t *testing.T) *BalanceGate {
	t.Helper()
	bg := newTestGate("http://unused", "", balanceCacheTTL)
	bg.setUserKeyCache("tok", "", "acme", "acme", "acme/ann")
	prevGate, prevRoute, prevEnso := balanceGate, routeDepth, isEnso
	balanceGate = bg
	routeDepth = func(_ stdcontext.Context, model string, _ []byte, _, _, _ string) (string, func(string) *controllers.Routing, bool) {
		if model != "enso-auto" {
			return "", nil, false
		}
		return "vendor/priced", func(why string) *controllers.Routing {
			return &controllers.Routing{Router: "enso", Chosen: "vendor/priced", Reason: why}
		}, true
	}
	isEnso = func(model string) bool { return model == "vendor/priced" }
	t.Cleanup(func() { balanceGate, routeDepth, isEnso = prevGate, prevRoute, prevEnso; object.SetLimits(nil) })
	return bg
}

func tierPost(body string) probe {
	return ask(http.MethodPost, "/v1/chat/completions").
		with("Authorization", "Bearer tok").
		body([]byte(body)).
		through(BalanceGateFilter)
}

func servedAs(p probe) string {
	var b struct{ Model string }
	_ = json.Unmarshal([]byte(p.handed()), &b)
	return b.Model
}

// An Enso tier is served to every caller, as the tier: who pays orders its rungs
// (controllers.Kinds). Something paying serves our credits, then prepaid rungs while
// the wallet holds cash, then the no-cost rungs; nothing paying serves the no-cost
// rungs and our credits at no charge, and a refusal for want of payment says so as
// limited mode. The request is never renamed to a free model.
func TestAnEnsoTierIsServedOnTheRungsItsPayerPays(t *testing.T) {
	bg := tierGate(t)
	const sent = `{"model":"enso-auto","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	says := func(name string, p probe, order string, freed bool, reason string) {
		t.Helper()
		if got := servedAs(p); got != "vendor/priced" {
			t.Errorf("%s: served %q, want the tier (%s)", name, got, p.said())
		}
		got, _, free := controllers.Order(p.left())
		if got != order || free != freed {
			t.Errorf("%s: kinds %q free %v, want %q free %v", name, got, free, order, freed)
		}
		if got := p.replied("X-Hanzo-Usage-Reason"); got != reason {
			t.Errorf("%s: X-Hanzo-Usage-Reason %q, want %q", name, got, reason)
		}
		if reason != "" && (p.replied("X-Hanzo-Usage") != "limited" || p.replied("X-Hanzo-Fallback") != "vendor/priced") {
			t.Errorf("%s: limited mode said %q / fallback %q", name, p.replied("X-Hanzo-Usage"), p.replied("X-Hanzo-Fallback"))
		}
	}

	// No plans installed: the wallet decides.
	bg.ledger.SetBalance("acme", 500)
	p := tierPost(sent)
	if p.status() != http.StatusOK {
		t.Fatalf("a funded caller: %d (%s)", p.status(), p.said())
	}
	says("a funded caller", p, controllers.KindsPaying, false, "")
	if p.replied(controllers.RoutedModelHeader) != "vendor/priced" {
		t.Errorf("X-Routed-Model %q", p.replied(controllers.RoutedModelHeader))
	}
	bg.ledger.SetBalance("acme", 0)
	p = tierPost(sent)
	if p.status() != http.StatusOK {
		t.Fatalf("an empty wallet: %d (%s)", p.status(), p.said())
	}
	says("an empty wallet", p, controllers.KindsFree, true, object.CodeInsufficientBalance)

	// Plans installed: the policy and the paid lane decide. The first ask prices the
	// tier; one asked again unpriced is the no-cost lane's.
	paidSwitch(t)
	g := func(plan, pays string) *object.LimitGrant {
		return &object.LimitGrant{Plan: plan, Pays: pays, Class: object.ClassOurs, Spend: 5_000_000_000, Settle: func(int64) {}}
	}
	type answer struct {
		grant *object.LimitGrant
		hit   *object.LimitHit
	}
	free := answer{grant: &object.LimitGrant{Plan: "max-20x", Pays: object.PaysPlan, Class: object.ClassFree, Settle: func(int64) {}}}
	overWindows := &object.LimitHit{Code: object.CodeUsageCap, Name: "session", Message: "this session's requests are used"}
	for _, tc := range []struct {
		name          string
		balance       int64
		priced, free  answer
		status        int
		order         string
		freed         bool
		asked, giveth int
		reason        string
	}{
		{"a paid plan, no cash beside it", 0, answer{grant: g("max-20x", object.PaysPlan)}, free, http.StatusOK, controllers.KindsPlan, false, 1, 0, ""},
		{"a paid plan with cash beside it", 500, answer{grant: g("max-20x", object.PaysPlan)}, free, http.StatusOK, controllers.KindsPaying, false, 1, 0, ""},
		{"a paid plan past its usage, its wallet paying", 500, answer{grant: g("dev", object.PaysPrepaid)}, free, http.StatusOK, controllers.KindsPaying, false, 1, 0, ""},
		{"a paid plan past its usage, its wallet empty", 0, answer{grant: g("dev", object.PaysPrepaid)}, free, http.StatusOK, controllers.KindsFree, true, 2, 1, object.CodeInsufficientBalance},
		{"a paid plan paying with granted credit", 500, answer{grant: g("dev", object.PaysCredits)}, free, http.StatusOK, controllers.KindsFree, true, 2, 1, object.CodePaidPlan},
		{"a funded wallet with no plan", 500, answer{grant: g("", object.PaysPrepaid)}, answer{}, http.StatusOK, controllers.KindsPaying, false, 1, 0, ""},
		{"an empty wallet with no plan", 0, answer{grant: g("", object.PaysPrepaid)}, answer{}, http.StatusOK, controllers.KindsFree, true, 2, 1, object.CodeInsufficientBalance},
		{"a free user, nothing to pay", 0, answer{hit: &object.LimitHit{Code: object.CodePaidPlan, Message: "a paid plan or prepaid balance is required"}}, answer{}, http.StatusOK, controllers.KindsFree, true, 2, 0, object.CodePaidPlan},
		{"a free user on the free plan with a cap", 0, answer{grant: g("free", object.PaysFree)}, answer{}, http.StatusOK, controllers.KindsFree, true, 2, 1, object.CodePaidPlan},
		{"a paid plan past its windows", 500, answer{hit: overWindows}, answer{hit: overWindows}, http.StatusTooManyRequests, "", false, 2, 0, ""},
	} {
		bg.ledger.SetBalance("acme", tc.balance)
		var asked []bool
		released := 0
		object.SetLimits(func(_ stdcontext.Context, q object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
			asked = append(asked, q.Priced)
			a := tc.free
			if q.Priced {
				a = tc.priced
			}
			if a.grant == nil {
				return nil, a.hit, nil
			}
			g := *a.grant
			g.Release = func() { released++ }
			return &g, nil, nil
		})
		p := tierPost(sent)
		if p.status() != tc.status {
			t.Errorf("%s: status %d, want %d (%s)", tc.name, p.status(), tc.status, p.said())
			continue
		}
		if len(asked) != tc.asked || released != tc.giveth || !asked[0] {
			t.Errorf("%s: the policy was asked %v (priced?) and gave back %d, want %d asks, the first priced, and %d give-backs", tc.name, asked, released, tc.asked, tc.giveth)
		}
		if tc.status != http.StatusOK {
			continue
		}
		says(tc.name, p, tc.order, tc.freed, tc.reason)
		if tc.freed == (p.replied(controllers.LaneHeader) == "paid") {
			t.Errorf("%s: lane %q, freed %v", tc.name, p.replied(controllers.LaneHeader), tc.freed)
		}
	}

	// A program's call for a customer is metered by that program: it is served as
	// paying, and the policy is not asked.
	asks := 0
	object.SetLimits(func(stdcontext.Context, object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
		asks++
		return g("max-20x", object.PaysPlan), nil, nil
	})
	p = ask(http.MethodPost, "/v1/chat/completions").
		with("Authorization", "Bearer tok").
		body([]byte(sent)).
		through(stated(object.Caller{Org: "globex", Person: "globex/ada"}), CallerFilter, BalanceGateFilter)
	if p.status() != http.StatusOK || asks != 0 {
		t.Errorf("a program's call: status %d, policy asked %d (%s)", p.status(), asks, p.said())
	}
	says("a program's call", p, controllers.KindsPaying, false, "")

	// A tier named outright is the same tier: nothing paying, its no-cost rungs.
	object.SetLimits(func(_ stdcontext.Context, q object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
		if !q.Priced {
			return nil, nil, nil
		}
		return nil, &object.LimitHit{Code: object.CodePaidPlan, Message: "a paid plan or prepaid balance is required"}, nil
	})
	bg.ledger.SetBalance("acme", 0)
	p = tierPost(`{"model":"vendor/priced","messages":[{"role":"user","content":"hi"}]}`)
	if p.status() != http.StatusOK {
		t.Fatalf("the tier named outright, nothing paying: %d (%s)", p.status(), p.said())
	}
	says("the tier named outright", p, controllers.KindsFree, true, object.CodePaidPlan)

	// The platform's switch off: the no-cost rungs, asked unpriced, and nothing said
	// as limited mode, since no one's payment was refused.
	controllers.FreeOnly = func() bool { return true }
	var asked []bool
	object.SetLimits(func(_ stdcontext.Context, q object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
		asked = append(asked, q.Priced)
		return free.grant, nil, nil
	})
	p = tierPost(sent)
	if len(asked) != 1 || asked[0] {
		t.Errorf("the switch off: asked %v, want one unpriced ask", asked)
	}
	says("the switch off", p, controllers.KindsFree, true, "")
	controllers.FreeOnly = func() bool { return false }

	// A caller the gate cannot name is never marked onto the paid rungs.
	object.SetLimits(nil)
	p = ask(http.MethodPost, "/v1/chat/completions").body([]byte(sent)).through(BalanceGateFilter)
	if order, _, _ := controllers.Order(p.left()); order != controllers.KindsFree {
		t.Errorf("an anonymous request was marked %q", order)
	}
}

// FAST MODE is prepaid cash's: a funded caller asking for it is served premium first
// and fast; a plan with no cash beside it is served as it would be without it.
func TestFastModeIsServedOnlyWhereCashPaysIt(t *testing.T) {
	bg := tierGate(t)
	paidSwitch(t)
	object.SetLimits(func(stdcontext.Context, object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
		return &object.LimitGrant{Plan: "max-20x", Pays: object.PaysPlan, Class: object.ClassOurs, Spend: 5_000_000_000, Settle: func(int64) {}}, nil, nil
	})
	for _, tc := range []struct {
		name    string
		balance int64
		body    string
		header  string
		order   string
		fast    bool
	}{
		{"fast in the body, cash beside the plan", 500, `{"model":"enso-auto","fast":true,"messages":[]}`, "", controllers.KindsFast, true},
		{"X-Fast, cash beside the plan", 500, `{"model":"enso-auto","messages":[]}`, "1", controllers.KindsFast, true},
		{"fast with no cash", 0, `{"model":"enso-auto","fast":true,"messages":[]}`, "", controllers.KindsPlan, false},
		{"no fast asked", 500, `{"model":"enso-auto","messages":[]}`, "", controllers.KindsPaying, false},
	} {
		bg.ledger.SetBalance("acme", tc.balance)
		r := ask(http.MethodPost, "/v1/chat/completions").with("Authorization", "Bearer tok").body([]byte(tc.body))
		if tc.header != "" {
			r = r.with("X-Fast", tc.header)
		}
		p := r.through(BalanceGateFilter)
		order, fast, _ := controllers.Order(p.left())
		if p.status() != http.StatusOK || order != tc.order || fast != tc.fast {
			t.Errorf("%s: status %d kinds %q fast %v, want %q fast %v (%s)", tc.name, p.status(), order, fast, tc.order, tc.fast, p.said())
		}
	}
}

// No caller may name the no-cost pool or one of its models: they answer only as a
// tier's fallback rungs.
func TestTheNoCostPoolIsNoModelACallerNames(t *testing.T) {
	tierGate(t)
	for _, m := range []string{"free", "google/gemma-4-31b-it:free", "Vendor/Any:FREE"} {
		p := tierPost(`{"model":"` + m + `","messages":[]}`)
		if p.status() != http.StatusNotFound {
			t.Errorf("%q: status %d, want 404 (%s)", m, p.status(), p.said())
		}
	}
}

// freeRoute makes zen5 a route the catalog serves at no charge, and Enso's router
// start enso-flash, as discovery and router.depth would.
func freeRoute(t *testing.T) {
	t.Helper()
	prevCost, prevStart := costsNothing, routerStart
	costsNothing = func(model, org string) bool { return model == "zen5" || prevCost(model, org) }
	routerStart = func() string { return "enso-flash" }
	t.Cleanup(func() { costsNothing, routerStart = prevCost, prevStart })
}
