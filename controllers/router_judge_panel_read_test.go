// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package controllers

import (
	"encoding/json"
	"testing"

	"github.com/hanzoai/ai/object"
)

// TestPanelSnapshotCopiesState: the snapshot reflects the live calibration state,
// deterministically ordered by model id, with the raw-score mean, reliability weight,
// and observation count per judge — and is empty before any judge scores.
func TestPanelSnapshotCopiesState(t *testing.T) {
	resetPanel()
	if got := PanelSnapshot(); len(got) != 0 {
		t.Fatalf("fresh panel must snapshot empty, got %d judges", len(got))
	}
	// Drive real state through the shipped observe/weight path (not hand-set fields).
	panelState.stat("gpt-4o-mini").observe(0.7)
	panelState.stat("claude-3-5-haiku").observe(0.6)
	panelState.stat("claude-3-5-haiku").observe(0.8)
	panelState.stat("llama-3.1-8b").weight = 0.42

	got := PanelSnapshot()
	if len(got) != 3 {
		t.Fatalf("snapshot should hold 3 judges, got %d", len(got))
	}
	// Deterministic ascending model-id order (world renders a stable list).
	if got[0].Model != "claude-3-5-haiku" || got[1].Model != "gpt-4o-mini" || got[2].Model != "llama-3.1-8b" {
		t.Errorf("snapshot must be sorted by model id, got %q,%q,%q", got[0].Model, got[1].Model, got[2].Model)
	}
	// claude judge saw two scores; mean is their raw average (0.7), n=2.
	if got[0].N != 2 || got[0].Mean < 0.69 || got[0].Mean > 0.71 {
		t.Errorf("claude judge snapshot wrong: n=%d mean=%.3f (want n=2 mean≈0.70)", got[0].N, got[0].Mean)
	}
	if got[2].Weight != 0.42 {
		t.Errorf("weight must be copied through: got %.3f want 0.42", got[2].Weight)
	}
}

// TestJudgePanelContract pins the EXACT wire shape world.hanzo.ai depends on: the keys,
// the available flag flipping on judge presence, the configured panel + posture, and
// the static published benchmark carried verbatim.
func TestJudgePanelContract(t *testing.T) {
	resetPanel()
	cfg := object.JudgeConfig{Enabled: true, Models: "claude-3-5-haiku,gpt-4o-mini,llama-3.1-8b", Sample: 0.1}

	// Empty panel ⇒ available:false, but posture + panel + benchmark still present.
	empty := buildJudgePanelState(cfg, PanelSnapshot())
	if empty.Available {
		t.Error("no judges scored yet ⇒ available must be false")
	}
	if len(empty.Models) != 3 || !empty.Enabled || empty.SampleRate != 0.1 {
		t.Errorf("posture not surfaced: %+v", empty)
	}
	if empty.Benchmark != publishedJudgeBenchmark {
		t.Errorf("static benchmark must ride along: got %+v", empty.Benchmark)
	}

	// Once a judge scores, available flips true and the live judge is included.
	panelState.stat("gpt-4o-mini").observe(0.62)
	state := buildJudgePanelState(cfg, PanelSnapshot())
	if !state.Available || len(state.Judges) != 1 {
		t.Fatalf("scored panel should be available with 1 judge, got available=%v judges=%d", state.Available, len(state.Judges))
	}

	// Exact JSON keys + nested shapes (the contract world consumes under `.data`).
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"available", "enabled", "sampleRate", "models", "judges", "benchmark"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("missing top-level key %q in %s", k, b)
		}
	}
	var bench map[string]float64
	if err := json.Unmarshal(raw["benchmark"], &bench); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"mfjp", "naiveMean", "singleNoisy", "singleAdversary"} {
		if _, ok := bench[k]; !ok {
			t.Errorf("missing benchmark key %q", k)
		}
	}
	if bench["mfjp"] != 0.966 {
		t.Errorf("published mfjp benchmark must be the experiment's 0.966, got %.3f", bench["mfjp"])
	}
	var judges []map[string]json.RawMessage
	if err := json.Unmarshal(raw["judges"], &judges); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"model", "weight", "mean", "n"} {
		if _, ok := judges[0][k]; !ok {
			t.Errorf("missing judge key %q", k)
		}
	}
}
