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

package router

// DefaultTaskKey is the catch-all preference list consulted when a task has no
// entry of its own. Mirrors hanzo-router policy's General fallback.
const DefaultTaskKey = "default"

// Cost units — the stack works in TWO related token-cost units, and confusing them is
// the classic pricing bug, so the boundary is made explicit here:
//
//   - the MODEL PRICING TABLE is USD per 1M tokens ($/1M): controllers.modelPrice and
//     conf/models.yaml pricing rates;
//   - the ROUTER COST CEILING and Slo.MaxCost are USD per 1K tokens ($/1k, "per-1k"):
//     the operator's per-request budget forwarded to the enso engine SLO.
//
// A $/1M rate is exactly 1000× the $/1k rate for the same tokens. CostCeiling and
// Slo.MaxCost are ALWAYS per-1k and forwarded to the engine verbatim (the engine compares
// them against its own per-1k cost model), so NO conversion happens in the forward path.
// These helpers exist for any caller that needs to relate a $/1M pricing-table rate to
// the $/1k ceiling unit — use them rather than sprinkling (or forgetting) a /1000.

// PerMillionToPerThousand converts a $/1M-token rate (pricing-table unit) to the
// $/1k-token unit of the router CostCeiling / Slo.MaxCost (÷1000).
func PerMillionToPerThousand(perMillion float64) float64 { return perMillion / 1000 }

// PerThousandToPerMillion converts a $/1k-token cost ceiling back to the $/1M-token
// unit of the pricing table (×1000).
func PerThousandToPerMillion(perThousand float64) float64 { return perThousand * 1000 }

// Policy is the per-task model preference table plus a cost knob. It mirrors the
// declarative shape of hanzo-router's router_policy.yaml: ordered model-id
// preference per task, first usable wins, with a General/`default` catch-all.
type Policy struct {
	// Prefer maps a task tag (Task values, plus "default") to an ordered list of
	// model ids. The first entry accepted by the `known` predicate wins.
	Prefer map[string][]string
	// CostCeiling is an optional advisory cost cap in USD PER 1K TOKENS (per-1k — NOT
	// the $/1M unit of the pricing table) forwarded verbatim to the engine SLO as
	// Slo.MaxCost; it does not filter the heuristic table (the table is already
	// curated). See the PerMillionToPerThousand note above for the unit relationship.
	CostCeiling float64
}

// ForTask returns the preferred model id for a task: the first entry in
// Prefer[task] accepted by `known`, else the first in Prefer["default"]. `known`
// (nil = accept all) lets the caller restrict the choice to models it can
// actually serve for this org. Returns "" when neither list yields a match.
func (p Policy) ForTask(task Task, known func(string) bool) string {
	if m := p.pick(string(task), known); m != "" {
		return m
	}
	return p.pick(DefaultTaskKey, known)
}

func (p Policy) pick(key string, known func(string) bool) string {
	for _, id := range p.Prefer[key] {
		if known == nil || known(id) {
			return id
		}
	}
	return ""
}
