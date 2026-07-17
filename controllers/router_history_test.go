// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package controllers

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/ai/object"
)

// evOn builds a routing event stamped on a specific UTC calendar day (noon), so the
// daily bucketing is deterministic. reward != nil marks the event scored.
func evOn(day, task, model, source string, reward *float64) *object.RoutingEvent {
	e := &object.RoutingEvent{
		CreatedTime: day + "T12:00:00Z",
		Task:        task,
		RoutedModel: model,
		Source:      source,
	}
	if reward != nil {
		e.Reward = *reward
		e.RewardedTime = day + "T13:00:00Z"
	}
	return e
}

var histNow = time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC) // window [07-10 .. 07-16]

// TestComputeRouterHistory covers the daily improvement series: per-day events,
// reward rate, cost-saved (routed vs the whole-window opus baseline) + its cumulative,
// task mix, honest zero rows for empty days, and the retrain timeline with the
// accuracy-only holdout rule.
func TestComputeRouterHistory(t *testing.T) {
	events := []*object.RoutingEvent{
		// 07-14: two cheap "mini" routes (baseline opus=30), one scored.
		evOn("2026-07-14", "code", "mini", "engine", f(0.8)),
		evOn("2026-07-14", "code", "mini", "engine", nil),
		// 07-15: three "mid" routes, two scored, mixed tasks.
		evOn("2026-07-15", "code", "mid", "engine", f(0.6)),
		evOn("2026-07-15", "reasoning", "mid", "engine", f(1.0)),
		evOn("2026-07-15", "chat", "mid", "heuristic", nil),
		// 07-16: one route to the baseline itself → zero saving.
		evOn("2026-07-16", "vision", "opus", "engine", nil),
	}
	retrains := []*object.RouterTrainingLog{
		{Owner: "*", Version: "router-2026-07-14", TrainedTime: "2026-07-14T04:20:00Z", Events: 100, GatePassed: true, Published: true, GateMetric: "holdout_accuracy", GateValue: 0.71, GateBase: 0.68},
		{Owner: "*", Version: "router-2026-07-15", TrainedTime: "2026-07-15T04:20:00Z", Events: 200, GatePassed: true, Published: false, GateMetric: "holdout_reward", GateValue: 0.33, GateBase: 0.30},
	}

	h := computeRouterHistory(events, retrains, fixedPrices, histNow, 7, scopePlatform)

	if h.Scope != scopePlatform || h.Window.Days != 7 {
		t.Fatalf("scope/window wrong: %+v", h.Window)
	}
	if len(h.Daily) != 7 {
		t.Fatalf("daily rows = %d, want 7 (one per window day)", len(h.Daily))
	}
	// Oldest-first, dates dense and correct.
	if h.Daily[0].Date != "2026-07-10" || h.Daily[6].Date != "2026-07-16" {
		t.Fatalf("day bounds wrong: %s .. %s", h.Daily[0].Date, h.Daily[6].Date)
	}
	// Empty leading days are honest zero rows.
	for i := 0; i < 4; i++ {
		if h.Daily[i].Events != 0 || h.Daily[i].CumulativeCostSaved != 0 {
			t.Errorf("day %s should be a zero row: %+v", h.Daily[i].Date, h.Daily[i])
		}
	}
	// 07-14: 2 events, saved = 2*(30-2)=56, reward 0.8, task code:2.
	d14 := h.Daily[4]
	if d14.Events != 2 || d14.CostSavedIndex != 56 || d14.CumulativeCostSaved != 56 || d14.RewardRate != 0.8 || d14.ByTask["code"] != 2 {
		t.Errorf("07-14 = %+v", d14)
	}
	// 07-15: 3 events, saved = 3*(30-10)=60, cumulative 116, reward (0.6+1)/2=0.8.
	d15 := h.Daily[5]
	if d15.Events != 3 || d15.CostSavedIndex != 60 || d15.CumulativeCostSaved != 116 || d15.RewardRate != 0.8 {
		t.Errorf("07-15 = %+v", d15)
	}
	// 07-16: routed to the baseline → zero saved; cumulative holds at 116.
	d16 := h.Daily[6]
	if d16.Events != 1 || d16.CostSavedIndex != 0 || d16.CumulativeCostSaved != 116 {
		t.Errorf("07-16 = %+v", d16)
	}
	// Totals.
	if h.Totals.Events != 6 || h.Totals.CumulativeCostSaved != 116 || h.Totals.RewardRate != 0.8 || h.Totals.DaysActive != 3 {
		t.Errorf("totals = %+v", h.Totals)
	}
	// Retrain timeline: accuracy gate exposes holdout_accuracy; a reward gate does NOT.
	if len(h.Retrains) != 2 {
		t.Fatalf("retrains = %d, want 2", len(h.Retrains))
	}
	if h.Retrains[0].HoldoutAccuracy == nil || *h.Retrains[0].HoldoutAccuracy != 0.71 || h.Retrains[0].Date != "2026-07-14" {
		t.Errorf("retrain[0] accuracy wrong: %+v", h.Retrains[0])
	}
	if h.Retrains[1].HoldoutAccuracy != nil {
		t.Errorf("reward gate must NOT be relabeled as accuracy: %+v", h.Retrains[1])
	}
	if h.Retrains[1].GateValue != 0.33 || h.Retrains[1].GateMetric != "holdout_reward" {
		t.Errorf("retrain[1] gate honesty wrong: %+v", h.Retrains[1])
	}

	// PRIVACY: the platform surface must carry tasks but NO model ids.
	b, _ := json.Marshal(h)
	for _, model := range []string{"opus", "mini", "mid"} {
		if strings.Contains(string(b), `"`+model+`"`) {
			t.Fatalf("platform history leaked model id %q: %s", model, b)
		}
	}
	if !strings.Contains(string(b), "code") { // tasks are safe + present
		t.Errorf("platform history should expose task mix")
	}
}

// TestComputeRouterHistory_EmptyHonest: with no data the series is all zeros — never
// fabricated — so the flywheel charts start flat and grow with real events.
func TestComputeRouterHistory_EmptyHonest(t *testing.T) {
	h := computeRouterHistory(nil, nil, fixedPrices, histNow, 14, scopePlatform)
	if len(h.Daily) != 14 {
		t.Fatalf("daily = %d, want 14 zero rows", len(h.Daily))
	}
	for _, d := range h.Daily {
		if d.Events != 0 || d.RewardRate != 0 || d.CostSavedIndex != 0 || d.CumulativeCostSaved != 0 {
			t.Errorf("non-zero row in empty history: %+v", d)
		}
	}
	if h.Totals.Events != 0 || h.Totals.CumulativeCostSaved != 0 || h.Totals.DaysActive != 0 || len(h.Retrains) != 0 {
		t.Errorf("empty totals/retrains not zero: %+v retrains=%d", h.Totals, len(h.Retrains))
	}
}

// TestComputeRouterHistory_DaysClamp bounds the window.
func TestComputeRouterHistory_DaysClamp(t *testing.T) {
	if h := computeRouterHistory(nil, nil, fixedPrices, histNow, 9999, scopePlatform); h.Window.Days != routerHistoryMaxDays || len(h.Daily) != routerHistoryMaxDays {
		t.Errorf("days not capped: %d / %d rows", h.Window.Days, len(h.Daily))
	}
	if h := computeRouterHistory(nil, nil, fixedPrices, histNow, 0, scopePlatform); h.Window.Days != routerHistoryDefaultDays {
		t.Errorf("zero days not defaulted: %d", h.Window.Days)
	}
}
