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

import (
	"testing"

	"github.com/hanzoai/ai/object"
)

// rewarded builds a batch of n rewarded routing events for one (task, model) arm
// at a fixed reward — the pure fit's only inputs.
func rewarded(task, model string, n int, reward float64) []*object.RoutingEvent {
	out := make([]*object.RoutingEvent, n)
	for i := range out {
		out[i] = &object.RoutingEvent{
			Task:         task,
			RoutedModel:  model,
			Reward:       reward,
			RewardedTime: "2026-07-16T00:00:00Z",
		}
	}
	return out
}

// TestFitRouterHeads proves the fold: per-(task,model) mean over rewarded rows,
// unscored/blank rows ignored, reward clamped to [0,1].
func TestFitRouterHeads(t *testing.T) {
	var ev []*object.RoutingEvent
	ev = append(ev, rewarded("code", "zen5-coder", 3, 0.9)...)
	ev = append(ev, rewarded("code", "gpt-4o", 2, 0.4)...)
	// An unscored row (no RewardedTime) must not contribute.
	ev = append(ev, &object.RoutingEvent{Task: "code", RoutedModel: "zen5-coder", Reward: 0.0})
	// An over-range reward clamps to 1.
	ev = append(ev, &object.RoutingEvent{Task: "math", RoutedModel: "zen5", Reward: 5, RewardedTime: "t"})

	arms := fitRouterHeads(ev)
	if a := arms["code\x00zen5-coder"]; a == nil || a.N != 3 || a.Mean() != 0.9 {
		t.Errorf("code/zen5-coder: got %+v, want N=3 mean=0.9", a)
	}
	if a := arms["code\x00gpt-4o"]; a == nil || a.N != 2 || a.Mean() != 0.4 {
		t.Errorf("code/gpt-4o: got %+v, want N=2 mean=0.4", a)
	}
	if a := arms["math\x00zen5"]; a == nil || a.Mean() != 1.0 {
		t.Errorf("math/zen5: reward must clamp to 1.0, got %+v", a)
	}
}

// TestBestArmPerTaskHonorsMinEvents proves a low-evidence arm can't win.
func TestBestArmPerTaskHonorsMinEvents(t *testing.T) {
	arms := fitRouterHeads(append(
		rewarded("code", "hot-new", 3, 1.0),    // great but only 3 samples
		rewarded("code", "steady", 50, 0.7)..., // solid evidence
	))
	best := bestArmPerTask(arms, 20)
	if best["code"].Model != "steady" {
		t.Errorf("min-events must exclude the 3-sample arm; got %q", best["code"].Model)
	}
	// Lower the bar and the high-mean arm wins.
	if bestArmPerTask(arms, 2)["code"].Model != "hot-new" {
		t.Error("with minEvents=2 the higher-mean arm should win")
	}
}

// TestGateDeploysOnlyOnMarginBeat proves a task deploys only when its best arm
// beats the incumbent's mean by the margin, and the "*"/conf incumbent is read.
func TestGateDeploysOnlyOnMarginBeat(t *testing.T) {
	var ev []*object.RoutingEvent
	ev = append(ev, rewarded("code", "challenger", 40, 0.90)...)
	ev = append(ev, rewarded("code", "incumbent", 40, 0.80)...)   // beat by 0.10
	ev = append(ev, rewarded("math", "challenger2", 40, 0.61)...) // barely
	ev = append(ev, rewarded("math", "incumbent2", 40, 0.60)...)  // beat by only 0.01
	arms := fitRouterHeads(ev)

	incumbent := map[string][]string{"code": {"incumbent"}, "math": {"incumbent2"}}
	res := gateRouterFit(arms, incumbent, nil, 20, 0.02)

	if got := res.Prefer["code"]; len(got) != 1 || got[0] != "challenger" {
		t.Errorf("code: 0.10 > margin → deploy challenger, got %v", got)
	}
	if _, ok := res.Prefer["math"]; ok {
		t.Error("math: 0.01 < 0.02 margin → must NOT deploy (keep incumbent)")
	}
	if !res.Passed {
		t.Error("run passes when ≥1 task deploys")
	}
	if res.NewMean <= res.BaseMean {
		t.Errorf("deployed new mean %.3f must exceed base %.3f", res.NewMean, res.BaseMean)
	}
}

// TestGateFirstFitNoIncumbent proves that with no incumbent evidence a real
// winner still deploys (beats the absolute floor of margin above 0).
func TestGateFirstFitNoIncumbent(t *testing.T) {
	arms := fitRouterHeads(rewarded("creative", "zen5", 30, 0.75))
	res := gateRouterFit(arms, nil, nil, 20, 0.02)
	if got := res.Prefer["creative"]; len(got) != 1 || got[0] != "zen5" {
		t.Errorf("first fit with a clear winner must deploy, got %v", got)
	}
}

// TestGateHoldsWhenNothingClears proves an all-weak fit keeps the incumbent and
// records a non-published verdict (the honest "kept incumbent" path).
func TestGateHoldsWhenNothingClears(t *testing.T) {
	arms := fitRouterHeads(rewarded("code", "meh", 5, 0.5)) // below minEvents
	res := gateRouterFit(arms, map[string][]string{"code": {"incumbent"}}, nil, 20, 0.02)
	if res.Passed || len(res.Prefer) != 0 {
		t.Errorf("nothing clears min-events → no deploy, got passed=%v prefer=%v", res.Passed, res.Prefer)
	}
	if res.Note == "" {
		t.Error("a held run must carry an honest note")
	}
}
