// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package controllers

import (
	"math/rand"
	"testing"
)

// Scientific test for the Mean-Field routing LAYER: a Monte-Carlo experiment with
// KNOWN per-model true quality and a CONGESTION penalty — a model's realized quality
// DROPS as its load rises (finite capacity) — driving the SHIPPED meanFieldSelect +
// loadTracker, that quantifies WHEN the congestion-aware equilibrium beats the obvious
// baseline:
//
//   - greedy  — "always route to the argmax value" (what a pure exploit bandit does).
//   - meanfield — best-respond to the live load mean field: calibrated value − β·load_share.
//
// Ground-truth setup: K models each have a fixed true quality q ∈ [0,1]. When a model
// currently carries load share L, the reward a request draws from it is
// clamp(q − κ·L + noise): the top model, hammered by a greedy router until it carries
// ALL the traffic, degrades below idle alternatives — self-inflicted overload. Both
// policies see the SAME estimated values (the known q), so the ONLY difference is
// congestion-awareness. We assert the mean-field layer wins on BOTH axes the design
// claims — LOWER total regret AND lower peak load (less overload) — and log the full
// numbers at a fixed seed so the result is reproducible (`go test -run MeanField -v`).

// TestMeanFieldBeatsGreedyUnderCongestion is the "prove it works better" experiment.
func TestMeanFieldBeatsGreedyUnderCongestion(t *testing.T) {
	rng := rand.New(rand.NewSource(7)) // fixed seed → reproducible
	const (
		rounds = 5000
		kappa  = 0.6  // congestion strength: full load costs 0.6 of realized quality
		beta   = 1.0  // mean-field congestion coefficient
		sigma  = 0.05 // reward observation noise
		window = 256  // rolling load window for both policies
	)
	// Known true qualities, best-first. m0 is the greedy magnet.
	models := []string{"m0", "m1", "m2", "m3", "m4"}
	quality := map[string]float64{"m0": 0.90, "m1": 0.84, "m2": 0.78, "m3": 0.70, "m4": 0.60}
	values := map[string]float64{}
	for m, q := range quality {
		values[m] = q // both policies route on the KNOWN value — congestion-awareness is the only difference
	}
	var qStar float64 // uncongested ideal = the reference for regret
	for _, q := range quality {
		if q > qStar {
			qStar = q
		}
	}

	// realized draws the congested, noisy reward for choosing model m at load share L.
	realized := func(m string, share float64) float64 {
		return expClamp(quality[m] - kappa*share + rng.NormFloat64()*sigma)
	}

	// greedyPick is the deterministic argmax of the known value (always m0 here).
	greedyPick := func() string {
		best, bestV := "", 0.0
		for _, m := range models {
			if best == "" || values[m] > bestV {
				best, bestV = m, values[m]
			}
		}
		return best
	}

	// maxShare is the most-loaded model's fraction of a full window — the peak load,
	// i.e. the overload metric.
	maxShare := func(tr *loadTracker) float64 {
		peak := 0.0
		for _, s := range tr.shares() {
			if s > peak {
				peak = s
			}
		}
		return peak
	}

	greedyLoads, mfLoads := newLoadTracker(window), newLoadTracker(window)
	var greedyRegret, mfRegret float64
	var greedyMaxLoad, mfMaxLoad float64
	mfPicks := map[string]float64{}

	for i := 0; i < rounds; i++ {
		// Greedy: always the argmax; its share climbs toward 1.0 → self-congestion.
		g := greedyPick()
		greedyRegret += qStar - realized(g, greedyLoads.share(g))
		greedyLoads.record(g)

		// Mean-field: best-respond to the current load field, then join it.
		m, _ := meanFieldSelect(values, mfLoads.shares(), beta)
		mfRegret += qStar - realized(m, mfLoads.share(m))
		mfLoads.record(m)
		mfPicks[m]++

		// Peak load is meaningful only once the window is full — a cold window where a
		// single pick is 100% of one sample is not "overload". Track the steady-state peak.
		if i >= window {
			if s := maxShare(greedyLoads); s > greedyMaxLoad {
				greedyMaxLoad = s
			}
			if s := maxShare(mfLoads); s > mfMaxLoad {
				mfMaxLoad = s
			}
		}
	}

	// Spearman between mean-field's per-model pick share and true quality: the layer
	// still sends MORE traffic to better models (positive correlation) while capping
	// them — quality-respecting load-balancing, not blind round-robin.
	alloc := make([]float64, len(models))
	truth := make([]float64, len(models))
	for i, m := range models {
		alloc[i] = mfPicks[m] / float64(rounds)
		truth[i] = quality[m]
	}
	spAlloc := expSpearman(alloc, truth)

	greedyAvgReward := qStar - greedyRegret/float64(rounds)
	mfAvgReward := qStar - mfRegret/float64(rounds)

	t.Logf("── Mean-field routing vs greedy argmax under congestion (rounds=%d, κ=%.2f, β=%.2f) ──", rounds, kappa, beta)
	t.Logf("  total regret     : greedy %.1f   meanfield %.1f   (lower is better)", greedyRegret, mfRegret)
	t.Logf("  avg realized rwd : greedy %.3f  meanfield %.3f", greedyAvgReward, mfAvgReward)
	t.Logf("  peak load share  : greedy %.3f  meanfield %.3f   (lower = less overload)", greedyMaxLoad, mfMaxLoad)
	t.Logf("  meanfield pick-share↔quality Spearman: %.3f (still prefers better models)", spAlloc)
	t.Logf("  greedy allocation is degenerate: %.0f%% on %s", greedyMaxLoad*100, greedyPick())

	// 1) The core claim: congestion-awareness yields LOWER total regret — the greedy
	//    router overloads its champion into a worse realized quality than a spread does.
	if mfRegret >= greedyRegret {
		t.Errorf("mean-field should have LOWER total regret: meanfield=%.1f greedy=%.1f", mfRegret, greedyRegret)
	}
	// 2) And lower peak load — it does not stampede a single model into overload.
	if mfMaxLoad >= greedyMaxLoad {
		t.Errorf("mean-field should have LOWER peak load: meanfield=%.3f greedy=%.3f", mfMaxLoad, greedyMaxLoad)
	}
	// 3) Sanity: greedy really does collapse onto one model (near-total peak load).
	if greedyMaxLoad < 0.95 {
		t.Errorf("greedy should overload its champion (peak≈1.0), got %.3f", greedyMaxLoad)
	}
	// 4) The spread is quality-respecting, not blind: better models still get more traffic.
	if spAlloc <= 0 {
		t.Errorf("mean-field allocation should correlate positively with quality, got %.3f", spAlloc)
	}
}
