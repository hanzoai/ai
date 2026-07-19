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

package controllers

import "testing"

// TestImplicitQuality locks the per-request quality proxy: succeeded-with-output is
// full credit, an empty 2xx is weak, an error is zero — so a failing arm can never
// out-reward a working one no matter how cheap.
func TestImplicitQuality(t *testing.T) {
	cases := []struct {
		name string
		r    *usageRecord
		want float64
	}{
		{"success with output", &usageRecord{Status: "success", CompletionTokens: 42}, 1.0},
		{"success empty completion", &usageRecord{Status: "success"}, 0.2},
		{"success image (per-unit)", &usageRecord{Status: "success", ImageCount: 1}, 1.0},
		{"error with tokens", &usageRecord{Status: "error", CompletionTokens: 10}, 0.0},
		{"non-success status", &usageRecord{Status: "timeout"}, 0.0},
	}
	for _, c := range cases {
		if got := implicitQuality(c.r); got != c.want {
			t.Errorf("%s: quality = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestImplicitRewardErrorIsZero proves an errored request contributes reward 0 — the
// bandit penalizes an arm that fails, independent of its price.
func TestImplicitRewardErrorIsZero(t *testing.T) {
	if got := implicitRoutingReward(&usageRecord{Status: "error", TotalTokens: 1000}); got != 0 {
		t.Fatalf("error reward = %v, want 0", got)
	}
}

// TestCostPenaltyReducesReward proves reward = quality × cost-efficiency: for the
// SAME successful request, a cheaper arm (higher reference rate ⇒ this arm looks
// cheap ⇒ small cost penalty) earns MORE reward than an expensive one. This is the
// gradient that drives the router to the cheapest arm that still succeeds.
func TestCostPenaltyReducesReward(t *testing.T) {
	r := &usageRecord{Status: "success", PromptTokens: 500, CompletionTokens: 500, TotalTokens: 1000, Model: "unknown-model-for-default-rate"}
	t.Setenv("ROUTER_AUTOREWARD_LAMBDA", "0.5")

	// Reference rate WAY above the arm's price → arm is "cheap" → keeps ~full reward.
	t.Setenv("ROUTER_AUTOREWARD_COST_REF_CENTS_PER_1K", "100000")
	cheap := implicitRoutingReward(r)

	// Reference rate WAY below the arm's price → arm is "expensive" → penalized to q·(1−λ).
	t.Setenv("ROUTER_AUTOREWARD_COST_REF_CENTS_PER_1K", "0.0001")
	expensive := implicitRoutingReward(r)

	if !(cheap > expensive) {
		t.Fatalf("cost penalty not applied: cheap=%v must exceed expensive=%v", cheap, expensive)
	}
	if cheap <= 0.9 {
		t.Errorf("a cheap successful arm should keep near-full reward, got %v", cheap)
	}
	if expensive > 0.6 {
		t.Errorf("an expensive arm should be penalized toward q·(1−λ)=0.5, got %v", expensive)
	}
}

// TestNormRoutingCostNoTokens: per-unit/unknown-token calls carry no token-cost
// signal, so they add no cost penalty (normCost 0) rather than a bogus one.
func TestNormRoutingCostNoTokens(t *testing.T) {
	if got := normRoutingCost(&usageRecord{Status: "success"}); got != 0 {
		t.Fatalf("normCost with no tokens = %v, want 0", got)
	}
}

// TestEmitAutoRewardDarkByDefault: with the flag unset, scoring is a no-op — it must
// not touch the ledger (safe to wire into the always-run recordUsage choke point).
func TestEmitAutoRewardDarkByDefault(t *testing.T) {
	t.Setenv("ROUTER_AUTOREWARD_ENABLED", "")
	// Must not panic and must return without attempting an attach (nil-adapter safe
	// regardless, but the gate is the contract).
	emitAutoRoutingReward(&usageRecord{Status: "success", Owner: "acme", RequestID: "req-1", CompletionTokens: 10})
}
