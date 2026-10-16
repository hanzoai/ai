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

// A router id is served as the tier its depth names exactly when something pays for
// that tier: with no plans installed, a balance that admits it; with plans installed,
// a paid plan's included usage, or prepaid cash the wallet covers, on a seat of the
// paid lane. Whatever else is answered on the family's free SKU at nothing, never
// refused for want of payment, and the answer says so: X-Routed-Model names what
// served, and a refusal for want of payment is said as limited mode, with its code.
func TestARouterIdIsServedOnItsTierOrItsFreeSKU(t *testing.T) {
	bg := newTestGate("http://unused", "", balanceCacheTTL)
	bg.setUserKeyCache("tok", "", "acme", "acme", "acme/ann")

	prevGate, prevRoute := balanceGate, depthRoute
	balanceGate = bg
	depthRoute = func(model string, body []byte) (string, string, bool) {
		if model != "enso-auto" {
			return "", "", false
		}
		return "vendor/priced", "enso-free", true
	}
	t.Cleanup(func() { balanceGate, depthRoute = prevGate, prevRoute; object.SetLimits(nil) })

	const sent = `{"model":"enso-auto","stream":true,"reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`
	post := func() probe {
		return ask(http.MethodPost, "/v1/chat/completions").
			with("Authorization", "Bearer tok").
			body([]byte(sent)).
			through(BalanceGateFilter)
	}
	servedAs := func(p probe) string {
		var b struct{ Model string }
		_ = json.Unmarshal([]byte(p.handed()), &b)
		return b.Model
	}
	// says checks what the answer says of the route: the model it names, and the
	// limited-mode reason when a refusal for want of payment moved it ("" for none).
	says := func(name string, p probe, routed, reason string) {
		t.Helper()
		if got := p.replied(controllers.RoutedModelHeader); got != routed {
			t.Errorf("%s: X-Routed-Model %q, want %q", name, got, routed)
		}
		fell := ""
		if reason != "" {
			fell = "enso-free"
		}
		if got := p.replied("X-Hanzo-Fallback"); got != fell {
			t.Errorf("%s: X-Hanzo-Fallback %q, want %q", name, got, fell)
		}
		if got := p.replied("X-Hanzo-Usage-Reason"); got != reason {
			t.Errorf("%s: X-Hanzo-Usage-Reason %q, want %q", name, got, reason)
		}
		if reason != "" && p.replied("X-Hanzo-Usage") != "limited" {
			t.Errorf("%s: X-Hanzo-Usage %q, want limited", name, p.replied("X-Hanzo-Usage"))
		}
	}

	// No plans installed, funded: the tier.
	bg.ledger.SetBalance("acme", 500)
	p := post()
	if p.status() != http.StatusOK || servedAs(p) != "vendor/priced" {
		t.Fatalf("a funded caller: status %d, served %q (%s)", p.status(), servedAs(p), p.said())
	}
	says("a funded caller", p, "vendor/priced", "")
	var b map[string]any
	if err := json.Unmarshal([]byte(p.handed()), &b); err != nil || b["stream"] != true || b["reasoning_effort"] != "high" {
		t.Errorf("the rewrite changed more than the model: %s", p.handed())
	}

	// An empty wallet: the free SKU, saying the balance is why.
	bg.ledger.SetBalance("acme", 0)
	p = post()
	if p.status() != http.StatusOK || servedAs(p) != "enso-free" {
		t.Errorf("an empty wallet: status %d, served %q (%s)", p.status(), servedAs(p), p.said())
	}
	says("an empty wallet", p, "enso-free", object.CodeInsufficientBalance)

	// Plans installed: the policy and the paid lane decide, in the one ask that serves
	// the call. Each case says what the policy answers for the tier and for the free SKU.
	paidSwitch(t)
	type answer struct {
		grant *object.LimitGrant
		hit   *object.LimitHit
	}
	g := func(plan, pays string) *object.LimitGrant {
		return &object.LimitGrant{Plan: plan, Pays: pays, Class: object.ClassOurs, Spend: 5_000_000_000, Settle: func(int64) {}}
	}
	free := answer{grant: &object.LimitGrant{Plan: "max-20x", Pays: object.PaysPlan, Class: object.ClassFree, Settle: func(int64) {}}}
	overWindows := &object.LimitHit{Code: object.CodeUsageCap, Name: "session", Message: "this session's requests are used"}
	for _, tc := range []struct {
		name         string
		balance      int64
		priced, free answer
		served       string
		status       int
		asked        int
		givenBack    int
		reason       string
	}{
		{"a paid plan within its windows", 0, answer{grant: g("max-20x", object.PaysPlan)}, free, "vendor/priced", http.StatusOK, 1, 0, ""},
		{"a paid plan past its usage, its funded wallet paying", 500, answer{grant: g("dev", object.PaysPrepaid)}, free, "vendor/priced", http.StatusOK, 1, 0, ""},
		{"a paid plan past its usage, its wallet empty", 0, answer{grant: g("dev", object.PaysPrepaid)}, free, "enso-free", http.StatusOK, 2, 1, object.CodeInsufficientBalance},
		{"a paid plan paying with granted credit", 500, answer{grant: g("dev", object.PaysCredits)}, free, "enso-free", http.StatusOK, 2, 1, object.CodePaidPlan},
		{"a funded wallet with no plan", 500, answer{grant: g("", object.PaysPrepaid)}, answer{}, "vendor/priced", http.StatusOK, 1, 0, ""},
		{"an empty wallet with no plan", 0, answer{grant: g("", object.PaysPrepaid)}, answer{}, "enso-free", http.StatusOK, 2, 1, object.CodeInsufficientBalance},
		{"a free user, no plan, nothing to pay", 0, answer{hit: &object.LimitHit{Code: object.CodePaidPlan, Message: "a paid plan or prepaid balance is required"}}, answer{}, "enso-free", http.StatusOK, 2, 0, object.CodePaidPlan},
		{"a free user with a daily cap on the paid tier", 0, answer{grant: g("", object.PaysFree)}, answer{}, "enso-free", http.StatusOK, 2, 1, object.CodePaidPlan},
		{"a free user on the free plan with a cap", 0, answer{grant: g("free", object.PaysFree)}, answer{}, "enso-free", http.StatusOK, 2, 1, object.CodePaidPlan},
		{"a free user holding promo credit", 0, answer{grant: g("", object.PaysCredits)}, answer{}, "enso-free", http.StatusOK, 2, 1, object.CodePaidPlan},
		{"a policy with nothing to say", 500, answer{}, answer{}, "enso-free", http.StatusOK, 2, 0, object.CodePaidPlan},
		{"a paid plan past its windows", 500, answer{hit: overWindows}, answer{hit: overWindows}, "", http.StatusTooManyRequests, 2, 0, ""},
	} {
		bg.ledger.SetBalance("acme", tc.balance)
		var asked []string
		released := 0
		object.SetLimits(func(_ stdcontext.Context, q object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
			asked = append(asked, q.Model)
			a := tc.free
			if q.Model == "vendor/priced" {
				a = tc.priced
			}
			if a.grant == nil {
				return nil, a.hit, nil
			}
			g := *a.grant
			g.Release = func() { released++ }
			return &g, nil, nil
		})
		p := post()
		if p.status() != tc.status {
			t.Errorf("%s: status %d, want %d (%s)", tc.name, p.status(), tc.status, p.said())
			continue
		}
		if len(asked) != tc.asked || released != tc.givenBack {
			t.Errorf("%s: the policy was asked %v and gave back %d, want %d asks and %d give-backs", tc.name, asked, released, tc.asked, tc.givenBack)
		}
		if tc.served == "" {
			continue
		}
		if servedAs(p) != tc.served {
			t.Errorf("%s: served %q, want %q (asked %v)", tc.name, servedAs(p), tc.served, asked)
		}
		says(tc.name, p, tc.served, tc.reason)
		tiered := tc.served == "vendor/priced"
		if tiered && (p.replied(controllers.LaneHeader) != "paid" || controllers.FreeOnlyFor(p.left())) {
			t.Errorf("%s: served the tier on lane %q", tc.name, p.replied(controllers.LaneHeader))
		}
		if !tiered && !controllers.FreeOnlyFor(p.left()) {
			t.Errorf("%s: a call served on the free SKU holds a paid seat", tc.name)
		}
	}

	// A paid plan with no room for the call: the free SKU, its grant given back, and
	// the answer names the lane's reason as limited mode's, not as the lane's own.
	{
		bg.ledger.SetBalance("acme", 500)
		var asked []string
		released := 0
		object.SetLimits(func(_ stdcontext.Context, q object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
			asked = append(asked, q.Model)
			if q.Model != "vendor/priced" {
				return free.grant, nil, nil
			}
			gr := g("max-20x", object.PaysPlan)
			gr.Spend = 0
			gr.Release = func() { released++ }
			return gr, nil, nil
		})
		p := post()
		if p.status() != http.StatusOK || servedAs(p) != "enso-free" {
			t.Errorf("a plan with no room: status %d served %q (%s)", p.status(), servedAs(p), p.said())
		}
		says("a plan with no room", p, "enso-free", controllers.ReasonFull)
		if len(asked) != 2 || released != 1 || p.replied(controllers.LaneReasonHeader) != "" {
			t.Errorf("a plan with no room: asked %v, gave back %d, lane reason %q", asked, released, p.replied(controllers.LaneReasonHeader))
		}
	}

	// With the platform's switch off the free SKU serves, the policy is asked about it
	// alone, and nothing is said as limited mode: no one's payment was refused.
	controllers.FreeOnly = func() bool { return true }
	{
		bg.ledger.SetBalance("acme", 500)
		var asked []string
		object.SetLimits(func(_ stdcontext.Context, q object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
			asked = append(asked, q.Model)
			return g("max-20x", object.PaysPlan), nil, nil
		})
		p := post()
		if servedAs(p) != "enso-free" || len(asked) != 1 || asked[0] != "enso-free" {
			t.Errorf("the switch off: served %q, asked %v", servedAs(p), asked)
		}
		says("the switch off", p, "enso-free", "")
	}
	controllers.FreeOnly = func() bool { return false }

	// A program's call for a customer is metered by that program: the free SKU, and
	// the policy is not asked about the tier.
	asked := 0
	object.SetLimits(func(stdcontext.Context, object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
		asked++
		return &object.LimitGrant{Plan: "max-20x", Pays: object.PaysPlan, Spend: 100, Settle: func(int64) {}}, nil, nil
	})
	p = ask(http.MethodPost, "/v1/chat/completions").
		with("Authorization", "Bearer tok").
		body([]byte(sent)).
		through(stated(object.Caller{Org: "globex", Person: "globex/ada"}), CallerFilter, BalanceGateFilter)
	if p.status() != http.StatusOK || servedAs(p) != "enso-free" || asked != 0 {
		t.Errorf("a program's call for a customer: status %d, served %q, policy asked %d (%s)", p.status(), servedAs(p), asked, p.said())
	}
	says("a program's call", p, "enso-free", "")

	// A tier named outright keeps its own behaviour: a free user is admitted on its cap
	// but never seated on the paid lane, and the family then answers from free models
	// (zen_client.go), never the paid rung; one nothing pays for is refused, not
	// handed to the free SKU.
	object.SetLimits(func(stdcontext.Context, object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
		return g("", object.PaysFree), nil, nil
	})
	named := func() probe {
		return ask(http.MethodPost, "/v1/chat/completions").
			with("Authorization", "Bearer tok").
			body([]byte(`{"model":"vendor/priced","messages":[{"role":"user","content":"hi"}]}`)).
			through(BalanceGateFilter)
	}
	p = named()
	if p.status() != http.StatusOK || p.replied(controllers.LaneHeader) != "free" || !controllers.FreeOnlyFor(p.left()) || servedAs(p) != "vendor/priced" {
		t.Errorf("a free user naming the paid tier: status %d, lane %q, served %q (%s)", p.status(), p.replied(controllers.LaneHeader), servedAs(p), p.said())
	}
	object.SetLimits(func(stdcontext.Context, object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
		return nil, &object.LimitHit{Code: object.CodePaidPlan, Message: "a paid plan or prepaid balance is required"}, nil
	})
	if p = named(); p.status() != http.StatusPaymentRequired || p.replied("X-Hanzo-Fallback") != "" {
		t.Errorf("the paid tier named outright, nothing paying: status %d, fallback %q (%s)", p.status(), p.replied("X-Hanzo-Fallback"), p.said())
	}

	// A caller the gate cannot name is never routed onto the tier.
	object.SetLimits(nil)
	p = ask(http.MethodPost, "/v1/chat/completions").body([]byte(sent)).through(BalanceGateFilter)
	if servedAs(p) == "vendor/priced" {
		t.Error("an anonymous request was routed onto the priced SKU")
	}
}
