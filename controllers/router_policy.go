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
	HasOverride bool                `json:"hasOverride,omitempty"`
}

// resolvedRouterPolicy folds the effective table + ceiling for an org and reports
// whether the org has its own override row. The conf baseline is the live
// ModelConfig router block (read under its lock via ConfRouterPolicy).
func resolvedRouterPolicy(org string) (routerPolicyBody, error) {
	hasPrefer := orgRouterPreferLookup(org) != nil
	hasCeiling := orgRouterCostCeilingLookup(org) > 0

	confPrefer, confCeiling := confRouterPolicy()
	return routerPolicyBody{
		Prefer:      effectiveRouterPrefer(org, confPrefer),
		CostCeiling: effectiveRouterCostCeiling(org, confCeiling),
		HasOverride: hasPrefer || hasCeiling,
	}, nil
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

// GetRouterPolicy returns the effective router policy resolved for the CALLER's
// own org (org > "*" > conf), plus whether the org has its own override. Org
// admin-gated (not super-admin): an org's own admins configure its own router,
// never another org's. Self-scoped via c.GetOrg() — the owner is never taken from
// the request body, so a caller cannot read or write another tenant's policy.
//
// @Title GetRouterPolicy
// @Tag Router API
// @Description get the effective router policy (prefer + cost ceiling) resolved for the caller's org
// @Success 200 {object} controllers.routerPolicyBody The Response object
// @router /get-router-policy [get]
func (c *ApiController) GetRouterPolicy() {
	if !c.RequireAdmin() {
		return
	}
	policy, err := resolvedRouterPolicy(c.GetOrg())
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(policy)
}

// UpdateRouterPolicy upserts the caller's OWN org router policy (RouterPrefer +
// RouterCostCeiling on the OrgSettings row). The owner is forced to c.GetOrg();
// any owner in the body is ignored. An empty Prefer + 0 CostCeiling clears the
// override (writes nil/0, reverting that org to "*" then conf) while preserving
// the org's AutoRouting / DefaultSessionRouting — the router policy is an
// orthogonal concern. Always upsert, never delete: an all-empty row is a
// harmless no-op (all fields unset read as unset), and deleting would clobber
// AutoRouting. Org admin-gated.
//
// @Title UpdateRouterPolicy
// @Tag Router API
// @Description upsert the caller's own org router policy (prefer + cost ceiling)
// @Param body body controllers.routerPolicyBody true "The router policy"
// @Success 200 {object} controllers.routerPolicyBody The resolved policy
// @router /update-router-policy [post]
func (c *ApiController) UpdateRouterPolicy() {
	if !c.RequireAdmin() {
		return
	}
	org := c.GetOrg()
	if org == "" {
		c.ResponseError(c.T("auth:Please sign in first"))
		return
	}

	var body routerPolicyBody
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, &body); err != nil {
		c.ResponseError(err.Error())
		return
	}

	existing, err := object.GetOrgSettings(org)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	row := object.OrgSettings{
		Owner:             org,
		RouterPrefer:      object.JSONMap[[]string](body.Prefer),
		RouterCostCeiling: body.CostCeiling,
	}
	if existing == nil {
		// New row: AutoRouting/SessionRouting stay unset until set on their own
		// endpoint. AddOrgSettings stamps CreatedTime/UpdatedTime.
		if _, err := object.AddOrgSettings(&row); err != nil {
			c.ResponseError(err.Error())
			return
		}
	} else {
		// Preserve AutoRouting / DefaultSessionRouting / CreatedTime: the router
		// policy is an orthogonal concern updated on its own endpoint. Zeroing
		// Prefer/Ceiling here clears the override without touching them.
		row.AutoRouting = existing.AutoRouting
		row.DefaultSessionRouting = existing.DefaultSessionRouting
		row.CreatedTime = existing.CreatedTime
		if _, err := object.UpdateOrgSettings(org, &row); err != nil {
			c.ResponseError(err.Error())
			return
		}
	}

	policy, err := resolvedRouterPolicy(org)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(policy)
}

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
