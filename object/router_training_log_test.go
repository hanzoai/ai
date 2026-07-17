// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package object

import "testing"

// TestRouterTrainingLog exercises the append-only retrain timeline: immutable rows
// (distinct ids + logged times), chronological listing by trained_time, the since
// filter, the most-recent limit, and per-scope isolation.
func TestRouterTrainingLog(t *testing.T) {
	restore, err := UseMemoryDB("file:router_training_log_test?mode=memory&cache=shared", &RouterTrainingLog{})
	if err != nil {
		t.Fatalf("UseMemoryDB: %v", err)
	}
	defer restore()

	rows := []*RouterTrainingLog{
		{Owner: "*", Version: "v1", TrainedTime: "2026-07-14T04:20:00Z", Events: 100, GatePassed: true, Published: true, GateMetric: "holdout_accuracy", GateValue: 0.71},
		{Owner: "*", Version: "v2", TrainedTime: "2026-07-15T04:20:00Z", Events: 220, GatePassed: true, Published: true, GateMetric: "holdout_accuracy", GateValue: 0.73},
		{Owner: "acme", Version: "o1", TrainedTime: "2026-07-15T04:20:00Z", Events: 40, GatePassed: false, Published: false, GateMetric: "holdout_reward", GateValue: 0.31},
	}
	for _, r := range rows {
		if err := AppendRouterTrainingLog(r); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	// Shared-base scope: two rows, oldest first, with unique append ids + logged times.
	base, err := ListRouterTrainingLog("*", "", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(base) != 2 || base[0].Version != "v1" || base[1].Version != "v2" {
		t.Fatalf("shared-base log wrong (want v1,v2 chronological): %+v", base)
	}
	if base[0].Id == "" || base[0].Id == base[1].Id {
		t.Error("append-only rows must have distinct non-empty ids")
	}
	if base[0].LoggedTime == "" {
		t.Error("append must stamp LoggedTime")
	}

	// since filter (trained_time >= since).
	recent, _ := ListRouterTrainingLog("*", "2026-07-15T00:00:00Z", 0)
	if len(recent) != 1 || recent[0].Version != "v2" {
		t.Errorf("since filter wrong: %+v", recent)
	}

	// limit keeps the MOST RECENT, still chronological.
	capped, _ := ListRouterTrainingLog("*", "", 1)
	if len(capped) != 1 || capped[0].Version != "v2" {
		t.Errorf("limit should keep the most recent: %+v", capped)
	}

	// Per-scope isolation: an org sees only its own rows.
	org, _ := ListRouterTrainingLog("acme", "", 0)
	if len(org) != 1 || org[0].Version != "o1" {
		t.Errorf("org scope not isolated: %+v", org)
	}
}
