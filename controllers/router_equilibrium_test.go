// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//      http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controllers

import (
	"reflect"
	"testing"
)

func arm(task, model string, n int, meanRwd float64) *armStat {
	return &armStat{Task: task, Model: model, N: n, SumRwd: meanRwd * float64(n)}
}

func armsOf(list ...*armStat) map[string]*armStat {
	m := map[string]*armStat{}
	for _, a := range list {
		m[a.Task+"|"+a.Model] = a
	}
	return m
}

// The load-bearing guarantee: with no capacity limits and no cost weight, the
// equilibrium allocation must put ALL traffic on the argmax arm — i.e. it reproduces
// bestArmPerTask exactly. Without this, the change is a behaviour change rather than a
// generalization, and could silently alter production routing.
func TestEquilibriumReducesToArgmax(t *testing.T) {
	arms := armsOf(
		arm("code", "alpha", 100, 0.90),
		arm("code", "beta", 100, 0.70),
		arm("chat", "gamma", 50, 0.60),
		arm("chat", "delta", 50, 0.80),
		arm("chat", "epsilon", 3, 0.99), // below minEvents: ineligible in BOTH paths
	)
	const minEvents = 10

	best := bestArmPerTask(arms, minEvents)
	got := equilibriumAllocation(arms, map[string]armCapacity{}, 0, minEvents)

	if len(got) != len(best) {
		t.Fatalf("task count differs: equilibrium=%d argmax=%d", len(got), len(best))
	}
	for task, b := range best {
		list, ok := got[task]
		if !ok {
			t.Fatalf("task %q missing from equilibrium", task)
		}
		if len(list) != 1 {
			t.Errorf("task %q: unconstrained equilibrium must concentrate on one arm, got %v", task, list)
			continue
		}
		if list[0] != b.Model {
			t.Errorf("task %q: equilibrium picked %q, argmax picked %q", task, list[0], b.Model)
		}
	}
	if _, ok := got["chat"]; ok && len(got["chat"]) > 0 && got["chat"][0] == "epsilon" {
		t.Error("below-minEvents arm leaked into the allocation")
	}
}

// Under a hard capacity the equilibrium must SPILL to the next-best arm rather than
// overload the winner — the entire point of the mean-field view.
func TestEquilibriumSpillsUnderCapacity(t *testing.T) {
	arms := armsOf(
		arm("code", "alpha", 100, 0.90), // best, but can only absorb 30% of traffic
		arm("code", "beta", 100, 0.70),  // next best, uncapped
		arm("code", "gamma", 100, 0.50),
	)
	caps := map[string]armCapacity{"alpha": {Model: "alpha", Capacity: 0.30}}

	got := equilibriumAllocation(arms, caps, 0, 10)["code"]
	want := []string{"alpha", "beta"} // alpha fills 30%, beta absorbs the remaining 70%
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("spill order = %v, want %v", got, want)
	}
	// gamma must not appear: beta had the capacity to absorb everything alpha could not.
	for _, m := range got {
		if m == "gamma" {
			t.Error("gamma allocated despite beta having spare capacity")
		}
	}
}

// A zero-capacity arm cannot serve and must be skipped entirely, even if it has the
// best mean reward — otherwise a drained provider keeps winning the table.
func TestEquilibriumSkipsZeroCapacity(t *testing.T) {
	arms := armsOf(
		arm("code", "alpha", 100, 0.95),
		arm("code", "beta", 100, 0.60),
	)
	caps := map[string]armCapacity{"alpha": {Model: "alpha", Capacity: 1e-12}}

	got := equilibriumAllocation(arms, caps, 0, 10)["code"]
	if len(got) != 1 || got[0] != "beta" {
		t.Fatalf("zero-capacity arm not skipped: got %v, want [beta]", got)
	}
}

// Cost must be able to flip the order: a slightly worse but much cheaper arm wins once
// the tenant weights cost. This is what makes "best model under MY budget" expressible.
func TestEquilibriumCostFlipsOrder(t *testing.T) {
	arms := armsOf(
		arm("code", "pricey", 100, 0.90),
		arm("code", "cheap", 100, 0.85),
	)
	caps := map[string]armCapacity{
		"pricey": {Model: "pricey", Cost: 1.00},
		"cheap":  {Model: "cheap", Cost: 0.10},
	}

	// costWeight 0: reward alone decides -> pricey.
	if got := equilibriumAllocation(arms, caps, 0, 10)["code"]; got[0] != "pricey" {
		t.Fatalf("costWeight=0 should prefer the higher-reward arm, got %v", got)
	}
	// costWeight 0.2: 0.90-0.20 = 0.70 vs 0.85-0.02 = 0.83 -> cheap wins.
	if got := equilibriumAllocation(arms, caps, 0.2, 10)["code"]; got[0] != "cheap" {
		t.Fatalf("cost weighting failed to flip the order, got %v", got)
	}
}
