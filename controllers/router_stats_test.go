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
	"encoding/json"
	"testing"
	"time"

	"github.com/hanzoai/ai/object"
)

// fixedPrices is a deterministic price index for tests: opus is the premium
// (baseline) arm, mini is cheap, local is free/unknown (excluded from cost).
func fixedPrices(model string) float64 {
	switch model {
	case "opus":
		return 30
	case "mid":
		return 10
	case "mini":
		return 2
	default:
		return 0 // local / unknown → not priced
	}
}

func ev(minsAgo int, task, model, source string, conf float64, reward *float64) *object.RoutingEvent {
	e := &object.RoutingEvent{
		CreatedTime: time.Now().UTC().Add(-time.Duration(minsAgo) * time.Minute).Format(time.RFC3339),
		Task:        task,
		RoutedModel: model,
		Source:      source,
		Confidence:  conf,
	}
	if reward != nil {
		e.Reward = *reward
		e.RewardedTime = time.Now().UTC().Format(time.RFC3339)
	}
	return e
}

func f(v float64) *float64 { return &v }

func TestComputeRouterStats_Distributions(t *testing.T) {
	now := time.Now().UTC()
	start := now.Add(-24 * time.Hour)
	events := []*object.RoutingEvent{
		ev(10, "code", "mini", "engine", 0.9, f(1)),
		ev(20, "code", "opus", "heuristic", 0, nil),
		ev(30, "chat", "mini", "engine", 0.8, f(0)),
		ev(40, "chat", "mid", "engine", 0.7, nil),
	}
	s := computeRouterStats(events, fixedPrices, start, now, scopeOrg, "acme", true)

	if s.Window.Events != 4 {
		t.Fatalf("events = %d, want 4", s.Window.Events)
	}
	if s.ByModel["mini"] != 2 || s.ByModel["opus"] != 1 || s.ByModel["mid"] != 1 {
		t.Fatalf("by_model wrong: %+v", s.ByModel)
	}
	if s.ByTask["code"].Events != 2 || s.ByTask["code"].Models["mini"] != 1 || s.ByTask["code"].Models["opus"] != 1 {
		t.Fatalf("by_task code wrong: %+v", s.ByTask["code"])
	}
	// engine share = 3/4
	if s.Quality.EngineShare != 0.75 {
		t.Fatalf("engine_share = %v, want 0.75", s.Quality.EngineShare)
	}
	// reward rate = mean(1, 0) over the two rewarded events = 0.5
	if s.Quality.RewardedEvents != 2 || s.Quality.RewardRate != 0.5 {
		t.Fatalf("reward: rate=%v n=%d, want 0.5/2", s.Quality.RewardRate, s.Quality.RewardedEvents)
	}
	if s.Quality.ShadowAgreement != nil {
		t.Fatalf("shadow_agreement should be nil until the ledger carries a shadow pick")
	}
}

func TestComputeRouterStats_CostSaved(t *testing.T) {
	now := time.Now().UTC()
	start := now.Add(-24 * time.Hour)
	// 3 priced events routed to mini(2), mid(10), opus(30); baseline=opus(30).
	// counterfactual = 30 each; routed sum = 42; saved = 90-42 = 48.
	events := []*object.RoutingEvent{
		ev(1, "chat", "mini", "engine", 0.9, nil),
		ev(2, "reasoning", "mid", "engine", 0.9, nil),
		ev(3, "reasoning", "opus", "heuristic", 0, nil),
		ev(4, "chat", "local", "engine", 0.9, nil), // unpriced → excluded
	}
	s := computeRouterStats(events, fixedPrices, start, now, scopeOrg, "acme", true)
	if s.Cost == nil {
		t.Fatal("cost should be present")
	}
	if s.Cost.BaselineModel != "opus" {
		t.Fatalf("baseline = %q, want opus", s.Cost.BaselineModel)
	}
	if s.Cost.PricedEvents != 3 {
		t.Fatalf("priced_events = %d, want 3 (local excluded)", s.Cost.PricedEvents)
	}
	// saved_pct = 48/90*100 = 53.3333
	if s.Cost.SavedPct < 53.3 || s.Cost.SavedPct > 53.34 {
		t.Fatalf("saved_pct = %v, want ~53.33", s.Cost.SavedPct)
	}
	if s.Cost.CumulativeSavedIndex != 48 {
		t.Fatalf("cumulative_saved = %v, want 48", s.Cost.CumulativeSavedIndex)
	}
	if s.Cost.RoutedIndex == nil || *s.Cost.RoutedIndex != 14 { // (2+10+30)/3
		t.Fatalf("routed_index = %v, want 14", s.Cost.RoutedIndex)
	}
	if s.Cost.CounterfactualIndex == nil || *s.Cost.CounterfactualIndex != 30 {
		t.Fatalf("counterfactual_index = %v, want 30", s.Cost.CounterfactualIndex)
	}
}

// The public platform scope must NEVER carry absolute $ levels or org identity,
// but SHOULD carry the safe ratio + counterfactual model id.
func TestComputeRouterStats_PublicScopeRedaction(t *testing.T) {
	now := time.Now().UTC()
	start := now.Add(-24 * time.Hour)
	events := []*object.RoutingEvent{
		ev(1, "chat", "mini", "engine", 0.9, nil),
		ev(2, "reasoning", "opus", "heuristic", 0, nil),
	}
	s := computeRouterStats(events, fixedPrices, start, now, scopePlatform, "acme", false)

	if s.Org != "" {
		t.Fatalf("platform scope must not leak org, got %q", s.Org)
	}
	if s.Cost == nil {
		t.Fatal("platform scope still exposes the safe cost ratio")
	}
	if s.Cost.RoutedIndex != nil || s.Cost.CounterfactualIndex != nil {
		t.Fatalf("platform scope must drop absolute $ levels, got routed=%v cf=%v", s.Cost.RoutedIndex, s.Cost.CounterfactualIndex)
	}
	if s.Cost.SavedPct <= 0 || s.Cost.BaselineModel != "opus" {
		t.Fatalf("platform scope keeps saved_pct + baseline id, got %+v", s.Cost)
	}

	// Serialized, the redacted level fields must be ABSENT (omitempty on nil ptr).
	b, _ := json.Marshal(s)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	cost := m["cost"].(map[string]any)
	if _, ok := cost["routed_index"]; ok {
		t.Fatalf("routed_index must not serialize in platform scope: %s", b)
	}
	if _, ok := cost["counterfactual_index"]; ok {
		t.Fatalf("counterfactual_index must not serialize in platform scope: %s", b)
	}
	if _, ok := m["org"]; ok {
		t.Fatalf("org must not serialize in platform scope: %s", b)
	}
}

func TestComputeRouterStats_Throughput(t *testing.T) {
	now := time.Now().UTC()
	start := now.Add(-24 * time.Hour)
	// two events in the most recent hour, one ~12h ago.
	events := []*object.RoutingEvent{
		ev(5, "chat", "mini", "engine", 0.9, nil),
		ev(20, "chat", "mini", "engine", 0.9, nil),
		ev(12*60, "chat", "mid", "engine", 0.9, nil),
	}
	s := computeRouterStats(events, fixedPrices, start, now, scopeOrg, "acme", true)
	if len(s.Throughput.PerHour) != routerStatsBuckets {
		t.Fatalf("per_hour len = %d, want %d", len(s.Throughput.PerHour), routerStatsBuckets)
	}
	if s.Throughput.TotalWindow != 3 {
		t.Fatalf("total_window = %d, want 3", s.Throughput.TotalWindow)
	}
	// most recent bucket holds the two fresh events.
	if got := s.Throughput.PerHour[routerStatsBuckets-1]; got != 2 {
		t.Fatalf("latest bucket = %d, want 2", got)
	}
	var sum int
	for _, v := range s.Throughput.PerHour {
		sum += v
	}
	if sum != 3 {
		t.Fatalf("throughput buckets sum = %d, want 3", sum)
	}
}

func TestComputeRouterStats_Empty(t *testing.T) {
	now := time.Now().UTC()
	start := now.Add(-24 * time.Hour)
	s := computeRouterStats(nil, fixedPrices, start, now, scopeOrg, "acme", true)
	if s.Cost != nil {
		t.Fatalf("no priced events → cost nil, got %+v", s.Cost)
	}
	if s.Window.Events != 0 || s.Quality.RewardRate != 0 || s.Quality.EngineShare != 0 {
		t.Fatalf("empty aggregate should be all-zero, got %+v", s)
	}
}

func TestSortedModelShare(t *testing.T) {
	got := sortedModelShare(map[string]int{"a": 1, "b": 3, "c": 3})
	// desc by count, ties by id → b,c (both 3, b<c), then a
	if len(got) != 3 || got[0] != "b" || got[1] != "c" || got[2] != "a" {
		t.Fatalf("sorted share = %v, want [b c a]", got)
	}
}
