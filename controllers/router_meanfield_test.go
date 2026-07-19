// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package controllers

import (
	"math"
	"testing"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/router"
)

// TestMeanFieldCalibration is the core calibration claim: z-scoring against the FIELD
// centers the population at 0, preserves order, and gives an equally-spaced field
// equal calibrated spacing — so a model is judged by how far it stands out from its
// peers, not by its absolute score. Degenerate fields (single model, all-equal)
// calibrate to all-zero (nothing to differentiate).
func TestMeanFieldCalibration(t *testing.T) {
	cal := calibrateModelValues(map[string]float64{"a": 1, "b": 2, "c": 3})
	// Centered: the field mean maps to ~0 (b is the mean).
	if math.Abs(cal["b"]) > 1e-9 {
		t.Errorf("field mean should calibrate to 0, got b=%.4f", cal["b"])
	}
	// Order preserved and symmetric about the mean for an equally-spaced field.
	if !(cal["a"] < cal["b"] && cal["b"] < cal["c"]) {
		t.Errorf("calibration must preserve order: a=%.3f b=%.3f c=%.3f", cal["a"], cal["b"], cal["c"])
	}
	if math.Abs(cal["a"]+cal["c"]) > 1e-9 {
		t.Errorf("equally-spaced field should be symmetric: a=%.3f c=%.3f", cal["a"], cal["c"])
	}
	// Scale invariance: multiplying every raw value by a constant leaves the z-scores
	// unchanged — calibration removes the field's scale, exposing only relative standing.
	cal2 := calibrateModelValues(map[string]float64{"a": 10, "b": 20, "c": 30})
	for _, m := range []string{"a", "b", "c"} {
		if math.Abs(cal[m]-cal2[m]) > 1e-9 {
			t.Errorf("calibration not scale-invariant for %s: %.4f vs %.4f", m, cal[m], cal2[m])
		}
	}
	// Robustness on thin data: a lucky spike in a WIDE field is bounded by the field
	// std, so it cannot run away — this is why a few lucky rewards can't dominate.
	spike := calibrateModelValues(map[string]float64{"lucky": 5, "a": 1, "b": 1, "c": 1, "d": 1})
	if spike["lucky"] <= 0 {
		t.Errorf("a genuine standout should calibrate positive, got %.3f", spike["lucky"])
	}
	if spike["lucky"] > 2.5 {
		t.Errorf("a lucky spike should be bounded by the field, got z=%.3f", spike["lucky"])
	}
	// Degenerate fields differentiate nothing.
	for name, in := range map[string]map[string]float64{
		"single":   {"x": 5},
		"allEqual": {"a": 2, "b": 2, "c": 2},
		"empty":    {},
	} {
		for m, z := range calibrateModelValues(in) {
			if z != 0 {
				t.Errorf("%s field should calibrate to 0, got %s=%.3f", name, m, z)
			}
		}
	}
}

// TestMeanFieldSelectCongestion proves the equilibrium behavior: with no load β is
// irrelevant and the field-best wins (β=0 reduces to the calibrated champion); as a
// model's load share rises it is penalized until a less-loaded peer overtakes it —
// load-balancing as best response. Ties break deterministically by model id, and the
// full effective-score map is returned for every candidate.
func TestMeanFieldSelectCongestion(t *testing.T) {
	values := map[string]float64{"a": 0.9, "b": 0.6, "c": 0.3}

	// No load: the field-best (a) wins regardless of β.
	if got, eff := meanFieldSelect(values, map[string]float64{}, 0.15); got != "a" {
		t.Errorf("unloaded field should pick the best: got %q (eff=%v)", got, eff)
	}
	if got, _ := meanFieldSelect(values, map[string]float64{}, 0); got != "a" {
		t.Errorf("β=0 should reduce to the calibrated champion, got %q", got)
	}

	// Congested champion: a carries all recent load, so a strong β penalizes it and a
	// less-loaded peer wins — the traffic spreads instead of stampeding a.
	got, eff := meanFieldSelect(values, map[string]float64{"a": 1.0}, 5.0)
	if got == "a" {
		t.Errorf("a fully-loaded champion should be displaced under congestion, got %q (eff=%v)", got, eff)
	}
	if len(eff) != len(values) {
		t.Errorf("effective-score map must cover every candidate: got %d want %d", len(eff), len(values))
	}

	// Determinism: an all-equal field with no load calibrates flat, so the tie breaks
	// to the smallest model id.
	if got, _ := meanFieldSelect(map[string]float64{"z": 1, "a": 1, "m": 1}, nil, 0.2); got != "a" {
		t.Errorf("flat field should tie-break to the smallest id, got %q", got)
	}
}

// TestMeanFieldRankValues: the router's ordered candidate list becomes linearly
// descending cardinal values (rank 0 highest), so the field-best equals the router's
// own champion before any congestion penalty applies.
func TestMeanFieldRankValues(t *testing.T) {
	v := rankValues([]string{"top", "mid", "low"})
	if !(v["top"] > v["mid"] && v["mid"] > v["low"]) {
		t.Errorf("rank values must descend with preference order: %v", v)
	}
	if got, _ := meanFieldSelect(v, nil, 0.15); got != "top" {
		t.Errorf("unloaded rank field should pick the router's rank-0 champion, got %q", got)
	}
}

// TestMeanFieldLoadTracker exercises the rolling window: shares reflect occupancy, the
// ring evicts oldest-first when full, and an empty window reports zero.
func TestMeanFieldLoadTracker(t *testing.T) {
	tr := newLoadTracker(4)
	if s := tr.shares(); len(s) != 0 {
		t.Errorf("empty tracker should report no shares, got %v", s)
	}
	if tr.share("a") != 0 {
		t.Error("empty tracker share should be 0")
	}
	for _, m := range []string{"a", "a", "b", "c"} {
		tr.record(m)
	}
	sh := tr.shares()
	if sh["a"] != 0.5 || sh["b"] != 0.25 || sh["c"] != 0.25 {
		t.Errorf("shares over a full window wrong: %v", sh)
	}
	// One more decision evicts the oldest 'a'; the window is now a,b,c,d at 0.25 each.
	tr.record("d")
	sh = tr.shares()
	if sh["a"] != 0.25 || sh["d"] != 0.25 {
		t.Errorf("ring eviction wrong: %v", sh)
	}
	if math.Abs(sumShares(sh)-1.0) > 1e-9 {
		t.Errorf("shares must sum to 1 over a full window, got %.4f", sumShares(sh))
	}
}

func sumShares(m map[string]float64) float64 {
	var s float64
	for _, v := range m {
		s += v
	}
	return s
}

// TestMeanFieldGateOff is the safety guarantee: with the gate DISABLED (the default),
// meanFieldRoute returns the champion untouched and records NOTHING — live routing is
// byte-identical. Flipping the gate on lets a congested champion be displaced.
func TestMeanFieldGateOff(t *testing.T) {
	prevCfg := meanFieldCfg
	prevLoads := routerLoads
	defer func() { meanFieldCfg, routerLoads = prevCfg, prevLoads }()
	routerLoads = newLoadTracker(100)

	client := router.Client{Policy: router.Policy{Prefer: map[string][]string{"code": {"a", "b", "c"}}}}

	// Gate OFF (default): champion unchanged, and the load window stays inert.
	meanFieldCfg = func() object.MeanFieldConfig { return object.MeanFieldConfig{Enabled: false, Beta: 0.15} }
	if m, changed := meanFieldRoute(client, "code", "a"); m != "a" || changed {
		t.Errorf("disabled gate must leave routing unchanged: got (%q, %v)", m, changed)
	}
	if len(routerLoads.shares()) != 0 {
		t.Error("disabled gate must record nothing into the load window")
	}

	// Gate ON with a heavily-loaded champion + strong β: the pick is displaced.
	meanFieldCfg = func() object.MeanFieldConfig { return object.MeanFieldConfig{Enabled: true, Beta: 5.0} }
	for i := 0; i < 90; i++ {
		routerLoads.record("a")
	}
	m, changed := meanFieldRoute(client, "code", "a")
	if !changed || m == "a" {
		t.Errorf("enabled gate should displace a congested champion, got (%q, %v)", m, changed)
	}

	// A single servable candidate has nothing to spread over: unchanged even when on.
	solo := router.Client{Policy: router.Policy{Prefer: map[string][]string{"code": {"only"}}}}
	if m, changed := meanFieldRoute(solo, "code", "only"); m != "only" || changed {
		t.Errorf("single-candidate task must stay on its only model: got (%q, %v)", m, changed)
	}
}
