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

import "context"

// Strategy selects which routing strategy leads for an org or surface. A
// deterministic Override always precedes the strategy, and the heuristic is
// always the final fallback, so a Decision never dead-ends.
type Strategy string

const (
	// StrategyEnso delegates the decision to the learned Enso model — the engine
	// reached at Client.Endpoint. It is the default: the zero Strategy behaves as
	// enso-leaning (try the engine, fall back to the heuristic).
	StrategyEnso Strategy = "enso"
	// StrategyHeuristic uses the rule router only (Classify + Policy) and never
	// calls the engine — the choice for an org that wants deterministic, auditable
	// routing with no learned component.
	StrategyHeuristic Strategy = "heuristic"
)

// SourceOverride labels a Decision produced by a deterministic per-org override,
// alongside SourceEngine and SourceHeuristic.
const SourceOverride = "override"

// RoutingPolicy is the per-org (or per-surface) configuration the Hanzo Router
// resolves a request against — a VALUE, not a place. It composes four orthogonal
// knobs, each independently complete:
//
//   - Strategy  — which strategy leads: StrategyEnso (learned) or StrategyHeuristic
//     (rules only). Overrides always precede it; the heuristic is always the floor.
//   - Overrides — deterministic task→model pins the org sets by hand: Overrides["code"]
//     forces a model for coding tasks, Overrides[DefaultTaskKey] is the catch-all. An
//     override naming a model the org cannot serve (per Enabled) is skipped, never
//     honored blindly.
//   - Enabled   — the org's servable model set (its connected providers + toggled
//     models). This is exactly the Client.Known predicate, sourced per-org rather than
//     per-process. nil means "all models allowed".
//   - Prefer    — the org's heuristic task→model table (+ cost ceiling). An empty table
//     means "use the Client's process-default Policy".
//
// The zero RoutingPolicy is inert — nil Enabled, no Overrides, empty Prefer, empty
// Strategy (enso-leaning) — so it reproduces the historical Client behavior exactly.
// An org with no configured policy routes as it always did.
type RoutingPolicy struct {
	Strategy  Strategy
	Overrides map[string]string
	Enabled   func(string) bool
	Prefer    Policy
}

// overrideModel returns the override pin for task — falling back to the
// DefaultTaskKey catch-all — honored only when the org can serve it. ok=false
// means there is no usable override and resolution should continue to the strategy.
func (rp RoutingPolicy) overrideModel(task Task, enabled func(string) bool) (string, bool) {
	for _, key := range [2]string{string(task), DefaultTaskKey} {
		if m := rp.Overrides[key]; m != "" && (enabled == nil || enabled(m)) {
			return m, true
		}
	}
	return "", false
}

// RouteDecisionFor resolves req to a Decision under an explicit per-org policy, with
// the precedence:
//
//	override → strategy (Enso engine, when selected and reachable) → heuristic
//
// It never errors: any engine failure falls through to the heuristic, and the
// heuristic relaxes servability as a last resort, so `auto` always resolves. The
// policy's Enabled set takes precedence over the Client's own Known for this call
// (per-org enablement belongs to the caller, not the process), and a non-empty
// policy Prefer table shadows the Client's default Policy for both the engine's
// task→model mapping and the heuristic floor.
func (c Client) RouteDecisionFor(ctx context.Context, req Request, slo Slo, rp RoutingPolicy) Decision {
	task := Classify(req)

	enabled := rp.Enabled
	if enabled == nil {
		enabled = c.Known
	}
	pol := c.Policy
	if len(rp.Prefer.Prefer) > 0 {
		pol = rp.Prefer
	}

	// 1. deterministic override — the org's hand-set task pins win first.
	if m, ok := rp.overrideModel(task, enabled); ok {
		return Decision{Model: m, Task: task, Source: SourceOverride}
	}

	// 2. strategy — delegate to the learned Enso engine unless the org pinned the
	// heuristic. Any engine failure falls through to the floor below.
	if rp.Strategy != StrategyHeuristic && c.Endpoint != "" {
		ec := c
		ec.Known = enabled
		ec.Policy = pol
		if d, ok := ec.routeEngine(ctx, req, slo); ok {
			return d
		}
	}

	// 3. heuristic floor — Classify + Policy, servability-first then relaxed so a
	// listed-but-unservable model still beats an empty id.
	m := pol.ForTask(task, enabled)
	if m == "" {
		m = pol.ForTask(task, nil)
	}
	return Decision{Model: m, Task: task, Source: SourceHeuristic}
}
