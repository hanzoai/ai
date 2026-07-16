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
		"code":    {"zen4-coder"},
		"reasoning": {"zen4-ultra"},
		"default": {"zen4"},
	}
	stubPreferCeiling(t,
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
