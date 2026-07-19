// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package controllers

import (
	"math"
	"math/rand"
	"sort"
	"testing"
)

// Scientific test for the Mean-Field Judge Panel (MFJP): a Monte-Carlo experiment
// with KNOWN ground truth, driving the SHIPPED panelScore, that quantifies WHEN the
// diverse self-calibrating panel beats the two obvious baselines:
//
//   - single judge  — one model's raw score (what we do today).
//   - naive mean     — the unweighted, uncalibrated average of all judges (the obvious
//                      "just use more judges" that a lot of people ship).
//   - MFJP           — calibrated (per-judge z-score) + reputation-weighted consensus.
//
// Ground-truth setup: M items each have a true quality q ∈ [0,1]. Each judge reports
// q PLUS its own systematic bias and Gaussian noise (harsh/lenient/noisy), and ONE
// judge is adversarial (emits pure noise — no signal). The fair, scale/bias-invariant
// score for "did the estimate preserve the true quality ranking" is Spearman rank
// correlation with q. We assert MFJP wins and the adversary is suppressed, and log the
// full numbers so the result is reproducible and inspectable (`go test -run MFJP -v`).

func expClamp(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

// expRank returns the fractional ranks of xs (average ranks for ties).
func expRank(xs []float64) []float64 {
	type iv struct {
		v   float64
		idx int
	}
	arr := make([]iv, len(xs))
	for i, v := range xs {
		arr[i] = iv{v, i}
	}
	sort.Slice(arr, func(a, b int) bool { return arr[a].v < arr[b].v })
	ranks := make([]float64, len(xs))
	for i := 0; i < len(arr); {
		j := i
		for j < len(arr) && arr[j].v == arr[i].v {
			j++
		}
		avg := float64(i+j-1) / 2.0
		for k := i; k < j; k++ {
			ranks[arr[k].idx] = avg
		}
		i = j
	}
	return ranks
}

func expPearson(a, b []float64) float64 {
	n := float64(len(a))
	var sa, sb, saa, sbb, sab float64
	for i := range a {
		sa += a[i]
		sb += b[i]
		saa += a[i] * a[i]
		sbb += b[i] * b[i]
		sab += a[i] * b[i]
	}
	num := n*sab - sa*sb
	den := math.Sqrt((n*saa - sa*sa) * (n*sbb - sb*sb))
	if den == 0 {
		return 0
	}
	return num / den
}

// expSpearman = Pearson on ranks — scale- and bias-invariant, so it fairly compares raw
// judge scores against the calibrated consensus.
func expSpearman(a, b []float64) float64 { return expPearson(expRank(a), expRank(b)) }

// TestMFJPRecoversGroundTruth proves the panel recovers the true quality ranking
// better than a naive mean and far better than an unreliable single judge, and that
// the adversarial judge is down-weighted out of influence.
func TestMFJPRecoversGroundTruth(t *testing.T) {
	rng := rand.New(rand.NewSource(42)) // fixed seed → reproducible
	const M = 800

	type J struct {
		id        string
		bias      float64
		noise     float64
		adversary bool
	}
	judges := []J{
		{"claude-mini", 0.00, 0.08, false}, // near-unbiased, low noise
		{"gpt-mini", 0.18, 0.10, false},    // lenient
		{"zen-mini", -0.22, 0.11, false},   // harsh
		{"qwen-mini", 0.05, 0.18, false},   // noisy
		{"rogue", 0.00, 0.00, true},        // adversary: pure noise, no signal
	}
	models := make([]string, len(judges))
	for i, j := range judges {
		models[i] = j.id
	}

	truth := make([]float64, M)
	for i := range truth {
		truth[i] = rng.Float64()
	}
	raw := map[string][]float64{}
	for _, j := range judges {
		xs := make([]float64, M)
		for i := 0; i < M; i++ {
			if j.adversary {
				xs[i] = rng.Float64() // no signal at all
			} else {
				xs[i] = expClamp(truth[i] + j.bias + rng.NormFloat64()*j.noise)
			}
		}
		raw[j.id] = xs
	}

	prev := rawJudgeScore
	defer func() { rawJudgeScore = prev }()
	var cur int
	rawJudgeScore = func(_ *judgeConfig, model, _, _, _ string) (float64, bool) {
		return raw[model][cur], true
	}
	resetPanel()

	mfjp := make([]float64, M)
	naive := make([]float64, M)
	for i := 0; i < M; i++ {
		cur = i
		r, _, ok := panelScore(&judgeConfig{}, models, "task", "prompt", "response")
		if !ok {
			t.Fatal("panel abstained unexpectedly")
		}
		mfjp[i] = r
		var s float64
		for _, j := range judges {
			s += raw[j.id][i]
		}
		naive[i] = s / float64(len(judges)) // includes the adversary
	}

	// Score on the post-warmup half (calibration + reputation have converged).
	h := M / 2
	spMFJP := expSpearman(mfjp[h:], truth[h:])
	spNaive := expSpearman(naive[h:], truth[h:])
	spSingleGood := expSpearman(raw["claude-mini"][h:], truth[h:])
	spSingleRogue := expSpearman(raw["rogue"][h:], truth[h:])
	spSingleNoisy := expSpearman(raw["qwen-mini"][h:], truth[h:])
	wRogue := panelState.stat("rogue").weight
	wGood := panelState.stat("claude-mini").weight

	t.Logf("── MFJP vs baselines: Spearman rank-correlation with ground truth (n=%d) ──", M-h)
	t.Logf("  MFJP (calibrated + reputation-weighted) : %.3f", spMFJP)
	t.Logf("  naive mean (all judges, incl. adversary) : %.3f", spNaive)
	t.Logf("  single judge — good (claude-mini)        : %.3f", spSingleGood)
	t.Logf("  single judge — noisy (qwen-mini)         : %.3f", spSingleNoisy)
	t.Logf("  single judge — adversary (rogue)         : %.3f", spSingleRogue)
	t.Logf("  adversary final weight %.3f vs good judge %.3f (suppressed)", wRogue, wGood)

	// 1) The whole point: MFJP beats the naive mean — calibration + reputation add
	//    value beyond "just average more judges" (the adversary + scale gaps sink the
	//    naive mean; MFJP normalizes scales and suppresses the adversary).
	if spMFJP <= spNaive {
		t.Errorf("MFJP (%.3f) should beat the naive mean (%.3f)", spMFJP, spNaive)
	}
	// 2) "Works better in some cases": when your single judge is unreliable (noisy or
	//    adversarial), the panel is dramatically better — this is the robustness win.
	if spMFJP <= spSingleNoisy {
		t.Errorf("MFJP (%.3f) should beat a noisy single judge (%.3f)", spMFJP, spSingleNoisy)
	}
	if spMFJP-spSingleRogue < 0.5 {
		t.Errorf("MFJP (%.3f) should massively beat an adversarial single judge (%.3f)", spMFJP, spSingleRogue)
	}
	// 3) The mean-field reputation dynamic actually suppressed the adversary.
	if wRogue >= wGood {
		t.Errorf("adversary not down-weighted: rogue=%.3f vs good=%.3f", wRogue, wGood)
	}
	// 4) And MFJP is at least as good as the BEST single judge (noise averaging over
	//    the reliable ones), so you never lose by using the panel.
	if spMFJP < spSingleGood-0.02 {
		t.Errorf("MFJP (%.3f) fell below even the best single judge (%.3f)", spMFJP, spSingleGood)
	}
}
