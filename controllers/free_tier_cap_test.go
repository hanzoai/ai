// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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
	"testing"
	"time"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/router"
	"github.com/hanzoai/decimal"
)

// TestCapKnownToBudget proves the shared cost-narrowing primitive (used by both the
// savings-vs-quality dial and the free-tier flash cap): it drops models over the blended
// $/1M budget, never drops an UNPRICED model (price ≤ 0), never re-admits a base-rejected
// model, tightens slo.MaxCost only when the budget is lower, and is a no-op when disabled.
func TestCapKnownToBudget(t *testing.T) {
	base := func(id string) bool {
		return map[string]bool{"cheap": true, "mid": true, "dear": true, "unpriced": true}[id]
	}
	price := func(m string) float64 {
		return map[string]float64{"cheap": 1, "mid": 4, "dear": 20, "unpriced": 0}[m]
	}

	// budget 3: cheap in, mid/dear out, unpriced (price 0) never dropped; SLO tightened to 3/1M.
	e, slo := capKnownToBudget(base, router.Slo{}, price, 3)
	if !e("cheap") || e("mid") || e("dear") || !e("unpriced") {
		t.Fatalf("budget 3: cheap=%v mid=%v dear=%v unpriced=%v", e("cheap"), e("mid"), e("dear"), e("unpriced"))
	}
	if want := router.PerMillionToPerThousand(3); slo.MaxCost != want {
		t.Fatalf("SLO tighten: got %v want %v", slo.MaxCost, want)
	}
	// tighten-only: a lower existing ceiling wins over the budget.
	if _, s := capKnownToBudget(base, router.Slo{MaxCost: 0.0001}, price, 3); s.MaxCost != 0.0001 {
		t.Fatalf("must not loosen a tighter ceiling: %v", s.MaxCost)
	}
	// never re-admits a model base already rejected.
	if e2, _ := capKnownToBudget(func(string) bool { return false }, router.Slo{}, price, 3); e2("cheap") {
		t.Fatal("must not re-admit a base-rejected model")
	}
	// disabled budget (0) or nil price → pure no-op (a disabled cap can never dead-end routing).
	if e3, s3 := capKnownToBudget(base, router.Slo{}, price, 0); !e3("dear") || s3.MaxCost != 0 {
		t.Fatal("budget 0 must be a no-op")
	}
	if e4, s4 := capKnownToBudget(base, router.Slo{}, nil, 3); !e4("dear") || s4.MaxCost != 0 {
		t.Fatal("nil price must be a no-op")
	}
}

// TestFreeTierFlashCapActive proves the gate that decides WHEN the flash cap fires: only a
// CONFIDENT free tier caps; trial/paid never cap; an UNKNOWN tier ("" — a commerce blip) never
// caps (so a paying caller is never degraded on a hiccup); and the env kill switch disables it.
func TestFreeTierFlashCapActive(t *testing.T) {
	prev := familyTier
	defer func() { familyTier = prev }()

	cases := []struct {
		tier string
		want bool
	}{
		{"free", true},     // enso rank 0 → cap
		{"zen-free", true}, // legacy free (a -free suffix) → cap
		{"starter", false}, // → trial → no cap
		{"", false},        // unknown → NO cap (never degrade a paying caller on a blip)

		// The plans commerce actually SELLS are go, dev, pro, max, team and
		// business. Every one of them is paying, so none may be capped. This
		// list used to score `developer` as free, which is what the shipped
		// mapping did to go/dev/max/team/business as well — we throttled and
		// refused customers who were already paying us.
		{"go", false},
		{"dev", false},
		{"developer", false},
		{"pro", false},
		{"max", false},
		{"team", false},
		{"business", false},
		{"enterprise", false},
	}
	for _, tc := range cases {
		familyTier = func(string) string { return tc.tier }
		if got := freeTierFlashCapActive("acme", nil); got != tc.want {
			t.Errorf("tier %q: got %v want %v", tc.tier, got, tc.want)
		}
	}

	// The env kill switch disables the cap even for a confirmed free tier (reversible, no rebuild).
	familyTier = func(string) string { return "free" }
	t.Setenv("ROUTER_FREE_TIER_CEILING_PER_MILLION", "0")
	if freeTierFlashCapActive("acme", nil) {
		t.Error("ceiling 0 must disable the cap")
	}
}

// TestResolveAutoModelFreeTierFlashCap proves the end-to-end behavior through resolveAutoModel:
// a code prompt whose prefer list leads with a PREMIUM model (glm-5.2, blended 6.30) then a FLASH
// model (gpt-4o-mini, blended 0.375). A paid tier and an UNKNOWN tier both keep the premium
// champion (the cap is inert / fail-safe); only a CONFIDENT free tier drops the premium model and
// routes to the flash model. This exercises the real price path (blendedPriceForOrg) + the wiring.
func TestResolveAutoModelFreeTierFlashCap(t *testing.T) {
	prevCfg, prevSink, prevTier := globalModelConfig, routingEventSink, familyTier
	t.Cleanup(func() { globalModelConfig, routingEventSink, familyTier = prevCfg, prevSink, prevTier })
	routingEventSink = func(object.RoutingEvent) {}

	globalModelConfig = &ModelConfig{
		routes: map[string]modelRoute{
			"glm-5.2":     {providerName: "do-ai", upstreamModel: "glm-5.2"},
			"gpt-4o-mini": {providerName: "do-ai", upstreamModel: "gpt-4o-mini"},
		},
		pricing: map[string]modelPrice{
			"glm-5.2":     {InputPerMillion: 3.00, OutputPerMillion: 9.60}, // blended 6.30 (premium)
			"gpt-4o-mini": {InputPerMillion: 0.15, OutputPerMillion: 0.60}, // blended 0.375 (flash)
		},
		router: RouterConfigDef{
			Enabled: true,
			Prefer:  map[string][]string{"code": {"glm-5.2", "gpt-4o-mini"}},
		},
	}
	const codePrompt = "please refactor this function" // classifies to code

	resolve := func() string {
		got, _, ok := resolveAutoModel("auto", "acme", "", "", nil, chatReq("auto", codePrompt), router.Slo{})
		if !ok {
			t.Fatal("resolveAutoModel not ok")
		}
		return got
	}

	// Paid tier → the premium champion wins (cap inert).
	familyTier = func(string) string { return "pro" }
	if got := resolve(); got != "glm-5.2" {
		t.Fatalf("paid: routed to %q, want glm-5.2 (cap must be inert for a paying org)", got)
	}
	// Unknown tier (a commerce blip) → NOT capped; the premium champion still wins (no degrade).
	familyTier = func(string) string { return "" }
	if got := resolve(); got != "glm-5.2" {
		t.Fatalf("unknown: routed to %q, want glm-5.2 (a blip must never degrade routing)", got)
	}
	// Confident free tier → the premium model is dropped; `auto` routes to the flash model.
	familyTier = func(string) string { return "free" }
	if got := resolve(); got != "gpt-4o-mini" {
		t.Fatalf("free: routed to %q, want gpt-4o-mini (premium must be capped to the flash pool)", got)
	}
}

// A model priced at zero does not need a funded wallet.
//
// The balance gate stops a call that cannot be paid for, and a route billing zero
// on both sides has nothing to pay: asking a customer to add funds for a $0.00
// call is how a free plan came to be refused the free routes we publish for it.
// It reads the same price the ledger bills from, and fails closed — a price it
// cannot find, or one it had to synthesize, is not free.
func TestAZeroPricedModelNeedsNoBalance(t *testing.T) {
	fam := openrouterFam
	saved := fam.byID
	savedLoaded, savedAt := fam.loaded, fam.fetchedAt
	t.Cleanup(func() { fam.byID, fam.loaded, fam.fetchedAt = saved, savedLoaded, savedAt })
	fam.byID = map[string]zenModel{
		"vendor/free": {ID: "vendor/free"},
		"vendor/paid": {ID: "vendor/paid", Base: zenTier{In: decimal.New(3, 0), Out: decimal.New(15, 0)}},
	}
	fam.loaded = true
	fam.fetchedAt = time.Now()

	if !costsNothing("vendor/free", "acme") {
		t.Error("a route the vendor charges nothing for is gated on balance")
	}
	if costsNothing("vendor/paid", "acme") {
		t.Error("a priced route skips the balance gate")
	}
	if costsNothing("auto", "acme") {
		t.Error("`auto` skips the gate before it has resolved to anything")
	}
	if costsNothing("no-such-model-anywhere", "acme") {
		t.Error("a model with no price skips the gate — this must fail closed")
	}
}
