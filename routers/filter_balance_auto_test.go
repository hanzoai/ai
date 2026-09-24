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
	"time"

	"github.com/hanzoai/ai/controllers"
	"github.com/hanzoai/ai/object"
	"github.com/zap-proto/zip"
)

// routesAutoTo stands in for the router: `auto` resolves to sku, rewritten into the
// request exactly as controllers.RouteAuto rewrites it.
func routesAutoTo(t *testing.T, sku string) {
	t.Helper()
	prev := routeAuto
	routeAuto = func(c *zip.Ctx) {
		var b struct{ Model string }
		if json.Unmarshal(c.Body(), &b) != nil || !controllers.IsAutoModel(b.Model) {
			return
		}
		if body, ok := controllers.WithModel(c.Body(), sku); ok {
			c.Fiber().Request().SetBody(body)
			c.SetHeader(controllers.RoutedModelHeader, sku)
		}
	}
	t.Cleanup(func() { routeAuto = prev })
}

// The virtual model is resolved before the gate reads the request, so the gate and
// the plan limits judge the SKU that will serve it, never the virtual id.
func TestTheGateJudgesTheSKUAutoRoutesTo(t *testing.T) {
	bg := newTestGate("http://unused", "", balanceCacheTTL)
	bg.setUserKeyCache("tok", "", "acme", "acme", "acme/ann")
	prev := balanceGate
	balanceGate = bg
	t.Cleanup(func() { balanceGate = prev; object.SetLimits(nil) })

	var asked []object.LimitAsk
	post := func(model string) probe {
		asked = nil
		return ask(http.MethodPost, "/v1/chat/completions").
			with("Authorization", "Bearer tok").
			body([]byte(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)).
			through(AutoRouteFilter, BalanceGateFilter)
	}
	servedAs := func(p probe) string {
		var b struct{ Model string }
		_ = json.Unmarshal([]byte(p.handed()), &b)
		return b.Model
	}

	// A free org whose `auto` routes to a free SKU is served at $0: no plan, no
	// credit, and nothing asks it for either.
	routesAutoTo(t, "enso-free")
	bg.ledger.SetBalance("acme", 0)
	object.SetLimits(func(_ stdcontext.Context, q object.LimitAsk) (*object.LimitHit, error) {
		asked = append(asked, q)
		return &object.LimitHit{Name: "plan"}, nil
	})
	for _, id := range []string{"auto", "zen-router"} {
		p := post(id)
		if p.status() != http.StatusOK || servedAs(p) != "enso-free" || len(asked) != 0 {
			t.Errorf("free org on %s: status %d, served %q, limits asked %+v (%s)", id, p.status(), servedAs(p), asked, p.said())
		}
	}

	// A plan holder with a full window and no credit, whose `auto` routes to a priced
	// SKU, is refused at the window for that SKU. Month headroom does not admit it.
	routesAutoTo(t, "vendor/priced")
	bg.ledger.SetBalance("acme", 900) // bought 0 + what the month still covers
	reset := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	object.SetLimits(func(_ stdcontext.Context, q object.LimitAsk) (*object.LimitHit, error) {
		asked = append(asked, q)
		return &object.LimitHit{Name: "weekly", ResetsAt: reset}, nil
	})
	p := post("auto")
	if p.status() != http.StatusTooManyRequests {
		t.Fatalf("full-window plan holder on auto: status %d, want 429 (%s)", p.status(), p.said())
	}
	if len(asked) != 1 || asked[0].Model != "vendor/priced" {
		t.Errorf("limits asked %+v, want one ask for the routed SKU", asked)
	}
}
