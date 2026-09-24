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
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/hanzoai/ai/controllers"
	"github.com/hanzoai/ai/object"
)

// The free default id is served as the priced SKU its depth row names exactly when
// the caller funds that SKU — a plan that covers it, or bought credit — and stays the
// free id for everyone else, including a caller whose limits cannot be read.
func TestTheDefaultIdTakesThePaidLadderOnlyWhenTheCallerFundsIt(t *testing.T) {
	bg := newTestGate("http://unused", "", balanceCacheTTL)
	bg.setUserKeyCache("tok", "", "acme", "acme", "acme/ann")

	prevGate, prevRoute := balanceGate, depthRoute
	balanceGate = bg
	var routedFrom string
	depthRoute = func(model string, body []byte) (string, bool) {
		if model != "enso-free" {
			return "", false
		}
		routedFrom = model
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

	// Funded: the plan covers the call (no hit) and the balance admits it.
	var asked []object.LimitAsk
	object.SetLimits(func(_ stdcontext.Context, q object.LimitAsk) (*object.LimitHit, error) {
		asked = append(asked, q)
		return nil, nil
	})
	bg.ledger.SetBalance("acme", 500)
	p := post()
	if p.status() != http.StatusOK || servedAs(p) != "vendor/priced" || p.replied(controllers.RoutedModelHeader) != "vendor/priced" {
		t.Fatalf("a funded caller: status %d, served %q, header %q (%s)", p.status(), servedAs(p), p.replied(controllers.RoutedModelHeader), p.said())
	}
	var b map[string]any
	if err := json.Unmarshal([]byte(p.handed()), &b); err != nil || b["stream"] != true || b["reasoning_effort"] != "high" {
		t.Errorf("the rewrite changed more than the model: %s", p.handed())
	}
	if routedFrom != "enso-free" || len(asked) == 0 || asked[0].Model != "vendor/priced" || asked[0].Subject != "acme" || asked[0].Actor != "acme/ann" {
		t.Errorf("limits asked %+v for %q", asked, routedFrom)
	}

	// Not funded, each way the answer can be no: the request runs as the free id.
	for name, limit := range map[string]object.LimitFunc{
		"no plan and no credit": func(stdcontext.Context, object.LimitAsk) (*object.LimitHit, error) {
			return &object.LimitHit{Name: "plan"}, nil
		},
		"a full window": func(stdcontext.Context, object.LimitAsk) (*object.LimitHit, error) {
			return &object.LimitHit{Name: "weekly", ResetsAt: time.Now().Add(time.Hour)}, nil
		},
		"unreadable limits": func(stdcontext.Context, object.LimitAsk) (*object.LimitHit, error) {
			return nil, errors.New("store unreadable")
		},
	} {
		object.SetLimits(limit)
		p := post()
		if p.status() != http.StatusOK || servedAs(p) != "enso-free" || p.replied(controllers.RoutedModelHeader) != "" {
			t.Errorf("%s: status %d, served %q, header %q (%s)", name, p.status(), servedAs(p), p.replied(controllers.RoutedModelHeader), p.said())
		}
	}

	// Limits that admit, and a wallet that does not.
	object.SetLimits(func(stdcontext.Context, object.LimitAsk) (*object.LimitHit, error) { return nil, nil })
	bg.ledger.SetBalance("acme", 0)
	if p := post(); p.status() != http.StatusOK || servedAs(p) != "enso-free" {
		t.Errorf("an empty wallet: status %d, served %q (%s)", p.status(), servedAs(p), p.said())
	}

	// A caller the gate cannot name is never routed.
	p = ask(http.MethodPost, "/v1/chat/completions").body([]byte(sent)).through(BalanceGateFilter)
	if servedAs(p) == "vendor/priced" {
		t.Error("an anonymous request was routed onto the priced SKU")
	}
}
