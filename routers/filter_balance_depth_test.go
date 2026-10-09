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

// The free default id is served as the priced SKU its depth row names exactly when
// the caller pays for that SKU: with no plans installed, a balance that admits it;
// with plans installed, a paid plan whose included usage, or past it the payer's own
// funded wallet, pays, on a seat of the paid lane. A free user — no plan, the free
// plan, a free cap, granted credit — and a caller past its windows are answered as the
// free id is today.
func TestTheDefaultIdTakesThePaidLadderOnlyWhenTheCallerFundsIt(t *testing.T) {
	bg := newTestGate("http://unused", "", balanceCacheTTL)
	bg.setUserKeyCache("tok", "", "acme", "acme", "acme/ann")

	prevGate, prevRoute := balanceGate, depthRoute
	balanceGate = bg
	depthRoute = func(model string, body []byte) (string, bool) {
		if model != "enso-free" {
			return "", false
		}
		return "vendor/priced", true
	}
	t.Cleanup(func() { balanceGate, depthRoute = prevGate, prevRoute; object.SetLimits(nil) })

	const sent = `{"model":"enso-free","stream":true,"reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`
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

	// No plans installed, funded: the free id is served as the priced SKU.
	bg.ledger.SetBalance("acme", 500)
	p := post()
	if p.status() != http.StatusOK || servedAs(p) != "vendor/priced" || p.replied(controllers.RoutedModelHeader) != "vendor/priced" {
		t.Fatalf("a funded caller: status %d, served %q, header %q (%s)", p.status(), servedAs(p), p.replied(controllers.RoutedModelHeader), p.said())
	}
	var b map[string]any
	if err := json.Unmarshal([]byte(p.handed()), &b); err != nil || b["stream"] != true || b["reasoning_effort"] != "high" {
		t.Errorf("the rewrite changed more than the model: %s", p.handed())
	}

	// An empty wallet keeps the free id.
	bg.ledger.SetBalance("acme", 0)
	if p := post(); p.status() != http.StatusOK || servedAs(p) != "enso-free" {
		t.Errorf("an empty wallet: status %d, served %q (%s)", p.status(), servedAs(p), p.said())
	}

	// Plans installed: the policy and the paid lane decide the lift, in the one ask
	// that serves the call. Each case says what the policy answers for the priced SKU
	// and for the free id. Free callers get free models, whatever a cap or a grant
	// says.
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
	}{
		{"a paid plan within its windows", 0, answer{grant: g("max-20x", object.PaysPlan)}, free, "vendor/priced", http.StatusOK, 1, 0},
		{"a paid plan past its usage, its funded wallet paying", 500, answer{grant: g("dev", object.PaysPrepaid)}, free, "vendor/priced", http.StatusOK, 1, 0},
		{"a paid plan past its usage, its wallet empty", 0, answer{grant: g("dev", object.PaysPrepaid)}, free, "enso-free", http.StatusOK, 2, 1},
		{"a paid plan paying with granted credit", 500, answer{grant: g("dev", object.PaysCredits)}, free, "enso-free", http.StatusOK, 2, 1},
		{"a free user, no plan, nothing to pay", 0, answer{hit: &object.LimitHit{Code: object.CodePaidPlan, Message: "a paid plan or prepaid balance is required"}}, answer{}, "enso-free", http.StatusOK, 2, 0},
		{"a free user with a daily cap on the paid tier", 0, answer{grant: g("", object.PaysFree)}, answer{}, "enso-free", http.StatusOK, 2, 1},
		{"a free user on the free plan with a cap", 0, answer{grant: g("free", object.PaysFree)}, answer{}, "enso-free", http.StatusOK, 2, 1},
		{"a free user holding promo credit", 0, answer{grant: g("", object.PaysCredits)}, answer{}, "enso-free", http.StatusOK, 2, 1},
		{"a funded wallet with no plan", 500, answer{grant: g("", object.PaysPrepaid)}, answer{}, "enso-free", http.StatusOK, 2, 1},
		{"a policy with nothing to say", 500, answer{}, answer{}, "enso-free", http.StatusOK, 2, 0},
		{"a paid plan past its windows", 500, answer{hit: overWindows}, answer{hit: overWindows}, "", http.StatusTooManyRequests, 2, 0},
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
		lifted := tc.served == "vendor/priced"
		if tc.served != "" && servedAs(p) != tc.served {
			t.Errorf("%s: served %q, want %q (asked %v)", tc.name, servedAs(p), tc.served, asked)
		}
		if got := p.replied(controllers.RoutedModelHeader); (got == "vendor/priced") != lifted {
			t.Errorf("%s: routed header %q, lifted %v", tc.name, got, lifted)
		}
		if lifted && (p.replied(controllers.LaneHeader) != "paid" || controllers.FreeOnlyFor(p.left())) {
			t.Errorf("%s: lifted onto lane %q", tc.name, p.replied(controllers.LaneHeader))
		}
		if !lifted && tc.status == http.StatusOK && !controllers.FreeOnlyFor(p.left()) {
			t.Errorf("%s: a call that was not lifted holds a paid seat", tc.name)
		}
		if len(asked) != tc.asked || released != tc.givenBack {
			t.Errorf("%s: the policy was asked %v and gave back %d, want %d asks and %d give-backs", tc.name, asked, released, tc.asked, tc.givenBack)
		}
	}

	// A paid plan with no room for the call keeps the free id: its grant is given
	// back, and the answer says nothing of the paid lane it was not seated on.
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
		if p.status() != http.StatusOK || servedAs(p) != "enso-free" || p.replied(controllers.RoutedModelHeader) != "" {
			t.Errorf("a plan with no room: status %d served %q (%s)", p.status(), servedAs(p), p.said())
		}
		if len(asked) != 2 || released != 1 || p.replied(controllers.LaneReasonHeader) != "" {
			t.Errorf("a plan with no room: asked %v, gave back %d, lane reason %q", asked, released, p.replied(controllers.LaneReasonHeader))
		}
	}

	// With the platform's switch off nothing is lifted, and the policy is asked about
	// the free id alone.
	controllers.FreeOnly = func() bool { return true }
	{
		bg.ledger.SetBalance("acme", 500)
		var asked []string
		object.SetLimits(func(_ stdcontext.Context, q object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
			asked = append(asked, q.Model)
			return g("max-20x", object.PaysPlan), nil, nil
		})
		if p := post(); servedAs(p) != "enso-free" || len(asked) != 1 || asked[0] != "enso-free" {
			t.Errorf("the switch off: served %q, asked %v", servedAs(p), asked)
		}
	}
	controllers.FreeOnly = func() bool { return false }

	// A program's call for a customer is metered by that program: never lifted, and
	// the policy is not asked about the lift.
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

	// A free user who names the paid tier is admitted on its cap but never seated on
	// the paid lane: the family then answers from free models (zen_client.go), never
	// the paid rung.
	object.SetLimits(func(stdcontext.Context, object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
		return g("", object.PaysFree), nil, nil
	})
	p = ask(http.MethodPost, "/v1/chat/completions").
		with("Authorization", "Bearer tok").
		body([]byte(`{"model":"vendor/priced","messages":[{"role":"user","content":"hi"}]}`)).
		through(BalanceGateFilter)
	if p.status() != http.StatusOK || p.replied(controllers.LaneHeader) != "free" || !controllers.FreeOnlyFor(p.left()) {
		t.Errorf("a free user naming the paid tier: status %d, lane %q (%s)", p.status(), p.replied(controllers.LaneHeader), p.said())
	}

	// A caller the gate cannot name is never routed.
	object.SetLimits(nil)
	p = ask(http.MethodPost, "/v1/chat/completions").body([]byte(sent)).through(BalanceGateFilter)
	if servedAs(p) == "vendor/priced" {
		t.Error("an anonymous request was routed onto the priced SKU")
	}
}
