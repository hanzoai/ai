// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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
	"encoding/json"
	"testing"

	"github.com/hanzoai/ai/router"
)

// stubPreferCeiling installs per-org + "*" lookups backed by in-memory maps. The
// reserved "*" row is just another key. Returns a restore func.
func stubPreferCeiling(t *testing.T, prefer map[string]map[string][]string, ceiling map[string]float64) {
	t.Helper()
	prevP, prevC := orgRouterPreferLookup, orgRouterCostCeilingLookup
	orgRouterPreferLookup = func(owner string) map[string][]string { return prefer[owner] }
	orgRouterCostCeilingLookup = func(owner string) float64 { return ceiling[owner] }
	t.Cleanup(func() {
		orgRouterPreferLookup = prevP
		orgRouterCostCeilingLookup = prevC
	})
}

// TestEffectiveRouterPreferFold proves the three-level per-key precedence: the
// org's own row wins per task key, else the "*" row, else the conf baseline — so
// an org customizing one task keeps the conf defaults for the rest.
func TestEffectiveRouterPreferFold(t *testing.T) {
	conf := map[string][]string{
		"code":      {"zen4-coder"},
		"reasoning": {"zen4-ultra"},
		"default":   {"zen4"},
	}
	stubPreferCeiling(
		t,
		map[string]map[string][]string{
			"*":    {"math": {"zen4-math"}, "default": {"gpt-4o"}}, // "*" overrides default + adds math
			"acme": {"code": {"acme-coder"}},                       // org overrides only code
		},
		nil,
	)
	got := effectiveRouterPrefer("acme", conf)
	if got["code"] == nil || got["code"][0] != "acme-coder" {
		t.Errorf("code: org row must win, got %v", got["code"])
	}
	if got["reasoning"] == nil || got["reasoning"][0] != "zen4-ultra" {
		t.Errorf("reasoning: conf must survive (no org/* override), got %v", got["reasoning"])
	}
	if got["math"] == nil || got["math"][0] != "zen4-math" {
		t.Errorf("math: '*' row must add it, got %v", got["math"])
	}
	if got["default"] == nil || got["default"][0] != "gpt-4o" {
		t.Errorf("default: '*' must override conf, got %v", got["default"])
	}
	// The conf map must NOT be mutated (the fold copies).
	if conf["default"][0] != "zen4" {
		t.Errorf("conf map mutated: default=%v", conf["default"])
	}
}

// TestEffectiveRouterCostCeilingFold proves org > "*" > conf for the ceiling.
func TestEffectiveRouterCostCeilingFold(t *testing.T) {
	stubPreferCeiling(t, nil, map[string]float64{"*": 5.0, "acme": 2.0})
	if c := effectiveRouterCostCeiling("acme", 10.0); c != 2.0 {
		t.Errorf("org wins: got %v want 2.0", c)
	}
	if c := effectiveRouterCostCeiling("other", 10.0); c != 5.0 {
		t.Errorf("'*' wins over conf: got %v want 5.0", c)
	}
	if c := effectiveRouterCostCeiling("other", 10.0); c == 10.0 {
		// (covered above: '*' fills the gap) — conf only wins when '*' is unset
	}
	stubPreferCeiling(t, nil, nil)
	if c := effectiveRouterCostCeiling("anyone", 10.0); c != 10.0 {
		t.Errorf("conf fallback: got %v want 10.0", c)
	}
}

// TestMergeCostCeiling proves a caller header ceiling wins over the policy
// ceiling, and the policy fills the gap when the caller omits one.
func TestMergeCostCeiling(t *testing.T) {
	if c := mergeCostCeiling(router.Slo{MaxCost: 8.0}, 5.0); c.MaxCost != 8.0 {
		t.Errorf("caller ceiling must win: got %v want 8.0", c.MaxCost)
	}
	if c := mergeCostCeiling(router.Slo{}, 5.0); c.MaxCost != 5.0 {
		t.Errorf("policy fills the gap: got %v want 5.0", c.MaxCost)
	}
	if c := mergeCostCeiling(router.Slo{}, 0.0); c.MaxCost != 0.0 {
		t.Errorf("both unset stays unset: got %v want 0.0", c.MaxCost)
	}
}

// TestResolvedRouterPolicySelfScopes proves the resolved read folds the effective
// table without a live DB, and reports the override presence honestly.
func TestResolvedRouterPolicySelfScopes(t *testing.T) {
	prevCfg := globalModelConfig
	prevP, prevC := orgRouterPreferLookup, orgRouterCostCeilingLookup
	t.Cleanup(func() {
		globalModelConfig = prevCfg
		orgRouterPreferLookup = prevP
		orgRouterCostCeilingLookup = prevC
	})
	globalModelConfig = &ModelConfig{
		routes:  map[string]modelRoute{},
		pricing: map[string]modelPrice{},
		router: RouterConfigDef{
			Enabled:     true,
			Prefer:      map[string][]string{"code": {"zen4-coder"}, "default": {"zen4"}},
			CostCeiling: 10.0,
		},
	}
	orgRouterPreferLookup = func(owner string) map[string][]string {
		if owner == "acme" {
			return map[string][]string{"code": {"acme-coder"}}
		}
		return nil
	}
	orgRouterCostCeilingLookup = func(owner string) float64 {
		if owner == "acme" {
			return 3.0
		}
		return 0
	}

	p, err := resolvedRouterPolicy("acme")
	if err != nil {
		t.Fatal(err)
	}
	if p.Prefer["code"][0] != "acme-coder" {
		t.Errorf("org override lost: got %v", p.Prefer["code"])
	}
	if p.Prefer["default"][0] != "zen4" {
		t.Errorf("conf default lost: got %v", p.Prefer["default"])
	}
	if p.CostCeiling != 3.0 {
		t.Errorf("org ceiling lost: got %v want 3.0", p.CostCeiling)
	}
	if !p.HasOverride {
		t.Error("HasOverride must be true when the org has its own row")
	}

	plain, _ := resolvedRouterPolicy("someone-else")
	if plain.HasOverride {
		t.Error("HasOverride must be false for an org with no row")
	}
	if plain.CostCeiling != 10.0 {
		t.Errorf("conf ceiling must fill the gap: got %v want 10.0", plain.CostCeiling)
	}
}

// TestRouterPolicyBodyJSON proves JSONMap round-trips the wire shape so a stored
// Prefer table loads back unchanged.
func TestRouterPolicyBodyJSON(t *testing.T) {
	body := routerPolicyBody{
		Prefer:      map[string][]string{"code": {"zen4-coder", "zen5-coder"}},
		CostCeiling: 4.5,
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var back routerPolicyBody
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Prefer["code"][0] != "zen4-coder" || back.CostCeiling != 4.5 {
		t.Errorf("round-trip lost data: %+v", back)
	}
}

// stubEnabledBias installs per-org + "*" lookups for the enabled-models allowlist and
// the savings-vs-quality dial, backed by in-memory maps. Returns via t.Cleanup.
func stubEnabledBias(t *testing.T, enabled map[string]map[string]bool, bias map[string]*float64) {
	t.Helper()
	prevE, prevB := orgRouterEnabledModelsLookup, orgRouterQualityBiasLookup
	orgRouterEnabledModelsLookup = func(owner string) map[string]bool { return enabled[owner] }
	orgRouterQualityBiasLookup = func(owner string) *float64 { return bias[owner] }
	t.Cleanup(func() {
		orgRouterEnabledModelsLookup = prevE
		orgRouterQualityBiasLookup = prevB
	})
}

// TestEffectiveRouterEnabledModelsFold: the org's own allowlist wins, else the "*" row,
// else nil (nil = no restriction, every servable model eligible).
func TestEffectiveRouterEnabledModelsFold(t *testing.T) {
	stubEnabledBias(t, map[string]map[string]bool{
		"acme": {"gpt-4o": true},
		"*":    {"enso": true},
	}, nil)
	if got := effectiveRouterEnabledModels("acme"); len(got) != 1 || !got["gpt-4o"] {
		t.Fatalf("acme allowlist = %v, want {gpt-4o} (own row wins)", got)
	}
	if got := effectiveRouterEnabledModels("other"); len(got) != 1 || !got["enso"] {
		t.Fatalf("other allowlist = %v, want {enso} (star row)", got)
	}
	stubEnabledBias(t, nil, nil)
	if got := effectiveRouterEnabledModels("nobody"); got != nil {
		t.Fatalf("no-override allowlist = %v, want nil (all allowed)", got)
	}
}

// TestEffectiveRouterQualityBiasFold: org > "*" > balanced default, clamped to [0,1].
func TestEffectiveRouterQualityBiasFold(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	stubEnabledBias(t, nil, map[string]*float64{"acme": f(0.2), "*": f(0.7)})
	if got := effectiveRouterQualityBias("acme"); got != 0.2 {
		t.Fatalf("acme bias = %v, want 0.2", got)
	}
	if got := effectiveRouterQualityBias("other"); got != 0.7 {
		t.Fatalf("other bias = %v, want 0.7 (star row)", got)
	}
	stubEnabledBias(t, nil, nil)
	if got := effectiveRouterQualityBias("nobody"); got != DefaultRouterQualityBias {
		t.Fatalf("unset bias = %v, want %v (default)", got, DefaultRouterQualityBias)
	}
	stubEnabledBias(t, nil, map[string]*float64{"hi": f(9), "lo": f(-3)})
	if got := effectiveRouterQualityBias("hi"); got != 1 {
		t.Fatalf("bias 9 → %v, want 1 (clamped)", got)
	}
	if got := effectiveRouterQualityBias("lo"); got != 0 {
		t.Fatalf("bias -3 → %v, want 0 (clamped)", got)
	}
}

// TestEnabledModelsRoundTrip: the write-shape id slice ↔ persisted set, lowercased,
// blanks dropped, empty → nil (clears the allowlist), read back sorted.
func TestEnabledModelsRoundTrip(t *testing.T) {
	set := enabledModelsSet([]string{"GPT-4o", " enso ", ""})
	if len(set) != 2 || !set["gpt-4o"] || !set["enso"] {
		t.Fatalf("enabledModelsSet = %v, want {gpt-4o, enso}", set)
	}
	ids := enabledModelIDs(set)
	if len(ids) != 2 || ids[0] != "enso" || ids[1] != "gpt-4o" {
		t.Fatalf("enabledModelIDs = %v, want sorted [enso gpt-4o]", ids)
	}
	if enabledModelsSet(nil) != nil {
		t.Fatalf("empty ids must clear the allowlist (nil set)")
	}
}
