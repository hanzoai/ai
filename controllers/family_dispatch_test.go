package controllers

import "testing"

// EVERY family must be reachable through familyForProviderType, because that resolution is
// what routes a request through pipeToFamily — the one choke point where the funding gate
// runs. A family the dispatch cannot resolve is not merely unrouted: every call site guards
// on `if fam := familyForProviderType(...); fam != nil`, so a nil sends the request down
// the generic relay instead, WITH the provider credential and WITHOUT the gate.
//
// OpenRouter shipped in exactly that state. It was present in modelFamilies, so all 338 of
// its prepaid models were discovered, listed and routable, while a hand-written switch that
// named only Zen and Enso quietly excluded it from the gated path.
//
// This asserts the property over modelFamilies rather than over a fixed list, so a family
// added later is covered the day it is added.
func TestEveryFamilyResolvesToItsGatedPath(t *testing.T) {
	if len(modelFamilies) == 0 {
		t.Fatal("no families registered")
	}
	for _, f := range modelFamilies {
		if f.typ == "" {
			t.Errorf("family %q declares no provider Type, so no request can ever resolve to it", f.name)
			continue
		}
		got := familyForProviderType(f.typ)
		if got == nil {
			t.Errorf("family %q (provider %q) does not resolve — its requests bypass pipeToFamily and the funding gate",
				f.name, f.typ)
			continue
		}
		if got != f {
			t.Errorf("provider %q resolved to family %q, want %q", f.typ, got.name, f.name)
		}
	}
	// A provider Type no family claims must resolve to nil, not to some default family.
	if got := familyForProviderType("NotAFamily"); got != nil {
		t.Errorf("unknown provider Type resolved to %q, want nil", got.name)
	}
}

// The prepaid families must carry the paid floor on every discovered model. OpenRouter is
// resale on a real-cash balance, so a free or trial caller must never reach one.
func TestOpenRouterModelsCarryThePaidFloor(t *testing.T) {
	saved := openrouterFam.byID
	t.Cleanup(func() { openrouterFam.byID = saved })
	openrouterFam.byID = map[string]zenModel{
		"anthropic/claude-opus-4.8": {ID: "anthropic/claude-opus-4.8", MinTier: "paid", Funding: "prepaid"},
	}
	m, ok := openrouterFam.lookup("anthropic/claude-opus-4.8")
	if !ok {
		t.Fatal("discovered model not found")
	}
	if m.Funding != "prepaid" {
		t.Errorf("funding = %q, want prepaid — OpenRouter spends real cash", m.Funding)
	}
	if m.minTier() != "paid" {
		t.Errorf("min_tier = %q, want paid", m.minTier())
	}
}
