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
	"sort"
	"strings"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/router"
)

// routerPolicyBody is the wire shape for both the resolved read and the write.
// Prefer is task tag -> ordered model ids ("default" is the catch-all). 0/empty
// means unset -> fall through to the "*" row then the conf file. HasOverride
// (read-only) reports whether the org has its own row. CostCeiling is USD per 1k
// tokens (per-1k), the same unit as OrgSettings.RouterCostCeiling and the SLO.
type routerPolicyBody struct {
	Prefer      map[string][]string `json:"prefer"`
	CostCeiling float64             `json:"costCeiling"` // USD per 1k tokens (per-1k)
	// EnabledModels is the org's ALLOWLIST of model ids the router may select; empty
	// (on write) clears it (all servable models eligible). On read it is the RESOLVED
	// allowlist (org > "*"), sorted.
	EnabledModels []string `json:"enabledModels"`
	// QualityBias is the savings-vs-quality dial in [0,1] (0 = cheapest, 1 = best,
	// 0.5 = balanced). On read it is the RESOLVED value (never nil). On write, nil
	// leaves the org's dial unset (fall through to "*" then the balanced default).
	QualityBias *float64 `json:"qualityBias,omitempty"`
	// Available is the read-only catalog of every servable model id for the caller's
	// org — what the allowlist picker offers. Omitted on write.
	Available   []routerModelItem `json:"available,omitempty"`
	HasOverride bool              `json:"hasOverride,omitempty"`
}

// routerModelItem is one entry in the allowlist picker: the model id plus a display
// label (the id doubles as the label; ownedBy qualifies it).
type routerModelItem struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	OwnedBy string `json:"ownedBy,omitempty"`
}

// resolvedRouterPolicy folds the effective table + ceiling + allowlist + dial for an
// org and reports whether the org has its own override row. The conf baseline is the
// live ModelConfig router block (read under its lock via ConfRouterPolicy).
func resolvedRouterPolicy(org string) (routerPolicyBody, error) {
	hasPrefer := orgRouterPreferLookup(org) != nil
	hasCeiling := orgRouterCostCeilingLookup(org) > 0
	hasModels := org != "" && orgRouterEnabledModelsLookup(org) != nil
	hasBias := org != "" && orgRouterQualityBiasLookup(org) != nil

	confPrefer, confCeiling := confRouterPolicy()
	bias := effectiveRouterQualityBias(org)
	return routerPolicyBody{
		Prefer:        effectiveRouterPrefer(org, confPrefer),
		CostCeiling:   effectiveRouterCostCeiling(org, confCeiling),
		EnabledModels: enabledModelIDs(effectiveRouterEnabledModels(org)),
		QualityBias:   &bias,
		Available:     availableRouterModels(),
		HasOverride:   hasPrefer || hasCeiling || hasModels || hasBias,
	}, nil
}

// enabledModelIDs renders an allowlist set as a sorted id slice (only the enabled
// entries), so the read shape is deterministic. nil/empty set → empty slice.
func enabledModelIDs(set map[string]bool) []string {
	ids := make([]string, 0, len(set))
	for id, on := range set {
		if on {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// enabledModelsSet turns the write-shape id slice into the persisted set (id → true).
// An empty slice → nil (clears the allowlist: all servable models eligible).
func enabledModelsSet(ids []string) object.JSONMap[bool] {
	if len(ids) == 0 {
		return nil
	}
	set := make(object.JSONMap[bool], len(ids))
	for _, id := range ids {
		if id = strings.ToLower(strings.TrimSpace(id)); id != "" {
			set[id] = true
		}
	}
	return set
}

// availableRouterModels is the allowlist picker's catalog: every servable model id
// (the same list /v1/models surfaces), each as {id, name, ownedBy}.
func availableRouterModels() []routerModelItem {
	models := listAvailableModels()
	out := make([]routerModelItem, 0, len(models))
	for _, m := range models {
		out = append(out, routerModelItem{ID: m.ID, Name: m.ID, OwnedBy: m.OwnedBy})
	}
	return out
}

// confRouterPolicy returns the live conf router Prefer + CostCeiling under the
// ModelConfig read lock. A nil-safe wrapper so callers never touch the lock.
func confRouterPolicy() (map[string][]string, float64) {
	cfg := GetModelConfig()
	if cfg == nil {
		return map[string][]string{}, 0
	}
	return cfg.ConfRouterPolicy()
}

// The router-policy read/write endpoints (GET|PUT /v1/router/policy) are served
// ZAP-native — the ONE implementation — by zapGet/UpdateRouterPolicyHandler in
// controllers/zap_router-policy-stats.go, which call the shared resolvedRouterPolicy
// + enabledModelsSet + the fold helpers below. There is deliberately no controller twin:
// the twin's mutate-vs-replace drift is what silently NULLed the org allowlist/dial.

// ── Per-org fold ────────────────────────────────────────────────────────
//
// The lookups are vars so tests exercise the fold matrix without a live DB;
// production reads the 60s-cached OrgSettings row — the same cache the
// auto-routing preference uses, so one TTL governs all org routing state.

// orgRouterPreferLookup returns an org's own Prefer table (nil = no override).
var orgRouterPreferLookup = func(owner string) map[string][]string {
	if owner == "" {
		return nil
	}
	s, err := object.GetCachedOrgSettings(owner)
	if err != nil || s == nil || len(s.RouterPrefer) == 0 {
		return nil
	}
	return s.RouterPrefer
}

// orgRouterCostCeilingLookup returns an org's own cost ceiling (0 = unset).
var orgRouterCostCeilingLookup = func(owner string) float64 {
	if owner == "" {
		return 0
	}
	s, err := object.GetCachedOrgSettings(owner)
	if err != nil || s == nil {
		return 0
	}
	return s.RouterCostCeiling
}

// effectiveRouterPrefer folds the three-level per-key precedence — org row >
// "*" row > conf — into one table. Copies; never mutates the conf map, so the
// live ModelConfig map is safe to pass straight in.
func effectiveRouterPrefer(org string, conf map[string][]string) map[string][]string {
	out := make(map[string][]string, len(conf))
	for k, v := range conf {
		out[k] = v
	}
	for _, owner := range []string{object.GlobalDefaultOwner, org} {
		if owner == "" {
			continue
		}
		for k, v := range orgRouterPreferLookup(owner) {
			if len(v) > 0 {
				out[k] = v
			}
		}
	}
	return out
}

// effectiveRouterCostCeiling folds org > "*" > conf for the ceiling; a level
// counts as set when its value is > 0.
func effectiveRouterCostCeiling(org string, conf float64) float64 {
	if org != "" {
		if c := orgRouterCostCeilingLookup(org); c > 0 {
			return c
		}
	}
	if c := orgRouterCostCeilingLookup(object.GlobalDefaultOwner); c > 0 {
		return c
	}
	return conf
}

// orgRouterStrategyLookup returns an org's own leading strategy ("" = unset).
// Indirected through a var so tests exercise precedence without a live DB.
var orgRouterStrategyLookup = func(owner string) string {
	if owner == "" {
		return ""
	}
	s, err := object.GetCachedOrgSettings(owner)
	if err != nil || s == nil {
		return ""
	}
	return s.RouterStrategy
}

// orgRouterOverridesLookup returns an org's own task→model overrides (nil = unset).
var orgRouterOverridesLookup = func(owner string) map[string]string {
	if owner == "" {
		return nil
	}
	s, err := object.GetCachedOrgSettings(owner)
	if err != nil || s == nil || len(s.RouterOverrides) == 0 {
		return nil
	}
	return s.RouterOverrides
}

// effectiveRouterStrategy folds org > "*" for the leading strategy. Unset at both
// levels returns "" — the enso-leaning default (try the engine, then the heuristic),
// the historical behavior. An org's "heuristic" therefore deterministically opts it
// out of the learned router even when the engine is configured.
func effectiveRouterStrategy(org string) router.Strategy {
	for _, owner := range []string{org, object.GlobalDefaultOwner} {
		if owner == "" {
			continue
		}
		if s := orgRouterStrategyLookup(owner); s != "" {
			return router.Strategy(s)
		}
	}
	return ""
}

// effectiveRouterOverrides folds org > "*" per-key for the deterministic task→model
// pins (org wins per key). Copies; never mutates a cached map. nil when neither
// level sets any override.
func effectiveRouterOverrides(org string) map[string]string {
	var out map[string]string
	for _, owner := range []string{object.GlobalDefaultOwner, org} {
		if owner == "" {
			continue
		}
		for k, v := range orgRouterOverridesLookup(owner) {
			if v != "" {
				if out == nil {
					out = make(map[string]string)
				}
				out[k] = v
			}
		}
	}
	return out
}

// mergeCostCeiling fills the caller's SLO cost budget from the resolved policy
// ceiling when the caller didn't set one — an explicit X-Max-Cost always wins. Both
// are USD per 1k tokens (per-1k), so the assignment is unit-preserving: the value is
// carried into Slo.MaxCost verbatim and forwarded to the engine as-is (no /1000).
func mergeCostCeiling(slo router.Slo, policyCeiling float64) router.Slo {
	if slo.MaxCost <= 0 && policyCeiling > 0 {
		slo.MaxCost = policyCeiling
	}
	return slo
}

// DefaultRouterQualityBias is the dial applied when an org AND the "*" row both leave it
// unset. It is 1.0 (max quality) on purpose: at bias ≥ 1 applyRouterQualityBias is INERT,
// so an org that never touches the dial routes EXACTLY as before (the bandit's quality-
// leaning pick within the cost ceiling) — the dial is strictly opt-in, never a fleet-wide
// behavior change. An org dials toward savings by explicitly lowering it.
const DefaultRouterQualityBias = 1.0

// orgRouterEnabledModelsLookup returns an org's own model allowlist (nil = no override,
// meaning all servable models are eligible). Same 60s-cached row the rest of the fold uses.
var orgRouterEnabledModelsLookup = func(owner string) map[string]bool {
	if owner == "" {
		return nil
	}
	s, err := object.GetCachedOrgSettings(owner)
	if err != nil || s == nil || len(s.RouterEnabledModels) == 0 {
		return nil
	}
	return s.RouterEnabledModels
}

// orgRouterQualityBiasLookup returns an org's own savings-vs-quality dial (nil = unset).
var orgRouterQualityBiasLookup = func(owner string) *float64 {
	if owner == "" {
		return nil
	}
	s, err := object.GetCachedOrgSettings(owner)
	if err != nil || s == nil {
		return nil
	}
	return s.RouterQualityBias
}

// effectiveRouterEnabledModels folds org > "*" for the allowlist; the org's own set wins
// when present. nil = no restriction (every servable model is eligible for the router).
func effectiveRouterEnabledModels(org string) map[string]bool {
	if org != "" {
		if a := orgRouterEnabledModelsLookup(org); a != nil {
			return a
		}
	}
	return orgRouterEnabledModelsLookup(object.GlobalDefaultOwner)
}

// effectiveRouterQualityBias folds org > "*" > balanced default, clamped to [0,1].
func effectiveRouterQualityBias(org string) float64 {
	for _, owner := range []string{org, object.GlobalDefaultOwner} {
		if owner == "" {
			continue
		}
		if b := orgRouterQualityBiasLookup(owner); b != nil {
			return clamp01(*b)
		}
	}
	return DefaultRouterQualityBias
}
