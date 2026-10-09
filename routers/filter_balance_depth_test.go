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
// with plans installed, whatever the usage policy admits — a plan within its windows
// and included usage, or a wallet that pays. A caller nothing pays for, or one past
// its windows, is answered as the free id is today.
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

	// Plans installed: the policy decides the lift as it decides the call. Each case
	// says what the policy answers for the priced SKU and for the free id.
	type answer struct {
		grant *object.LimitGrant
		hit   *object.LimitHit
	}
	covered := func() *object.LimitGrant {
		return &object.LimitGrant{Plan: "max-20x", Pays: object.PaysPlan, Spend: 100, Settle: func(int64) {}}
	}
	overWindows := &object.LimitHit{Code: object.CodeUsageCap, Name: "session", Message: "this session's requests are used"}
	for _, tc := range []struct {
		name          string
		balance       int64
		priced, free  answer
		served        string
		status        int
		givenBackOnce bool
	}{
		{"an unfunded caller", 0,
			answer{hit: &object.LimitHit{Code: object.CodePaidPlan, Message: "a paid plan or prepaid balance is required"}}, answer{},
			"enso-free", http.StatusOK, false},
		{"a funded wallet with no plan", 500,
			answer{grant: &object.LimitGrant{Pays: object.PaysPrepaid, Settle: func(int64) {}}}, answer{},
			"vendor/priced", http.StatusOK, true},
		{"a plan whose wallet cannot pay what the plan does not cover", 0,
			answer{grant: &object.LimitGrant{Plan: "dev", Pays: object.PaysPrepaid, Settle: func(int64) {}}}, answer{grant: covered()},
			"enso-free", http.StatusOK, true},
		{"a plan subscriber within its windows", 0,
			answer{grant: covered()}, answer{grant: covered()},
			"vendor/priced", http.StatusOK, true},
		{"a plan subscriber over its windows", 500,
			answer{hit: overWindows}, answer{hit: overWindows},
			"", http.StatusTooManyRequests, false},
		{"a policy with nothing to say, and a funded wallet", 500,
			answer{}, answer{},
			"vendor/priced", http.StatusOK, false},
	} {
		bg.ledger.SetBalance("acme", tc.balance)
		var asked []string
		released := 0
		object.SetLimits(func(_ stdcontext.Context, q object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
			asked = append(asked, q.Model)
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
		if tc.givenBackOnce != (released == 1) {
			t.Errorf("%s: the policy's grant for the lift's question was given back %d times (asked %v)", tc.name, released, asked)
		}
		if tc.status == http.StatusTooManyRequests && servedAs(p) != "" {
			t.Errorf("%s: a caller past its windows was served %q", tc.name, servedAs(p))
		}
	}

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

	// A caller the gate cannot name is never routed.
	object.SetLimits(nil)
	p = ask(http.MethodPost, "/v1/chat/completions").body([]byte(sent)).through(BalanceGateFilter)
	if servedAs(p) == "vendor/priced" {
		t.Error("an anonymous request was routed onto the priced SKU")
	}
}
