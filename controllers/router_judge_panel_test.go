// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package controllers

import (
	"math"
	"testing"
)

// resetPanel clears the in-process calibration state between tests.
func resetPanel() { panelState = &judgePanel{stats: map[string]*judgeStat{}} }

// TestSplitModels: comma list → clean panel; single → one judge.
func TestSplitModels(t *testing.T) {
	cases := map[string]int{
		"a": 1, "a,b,c": 3, " a , b ,, c ": 3, "solo": 1,
	}
	for in, want := range cases {
		if got := len(splitModels(in)); got != want {
			t.Errorf("splitModels(%q) = %d models, want %d", in, got, want)
		}
	}
}

// TestCalibrationRemovesJudgeBias is the core mean-field claim: a systematically HARSH
// judge and a systematically LENIENT judge, rating the SAME quality of response, should
// converge to a SIMILAR calibrated verdict — because each is z-scored against its own
// standard. So a genuinely good response scores high on the panel regardless of the
// judges' raw scales.
func TestCalibrationRemovesJudgeBias(t *testing.T) {
	resetPanel()
	// Warm each judge with its own baseline distribution over many mediocre responses:
	// harsh averages ~0.3, lenient ~0.8, each with spread.
	for i := 0; i < 200; i++ {
		x := float64(i%5) * 0.05 // 0..0.2 jitter
		panelState.stat("harsh").observe(0.30 + x)
		panelState.stat("lenient").observe(0.80 + x)
	}
	// A GOOD response sits at the SAME relative position for both judges — 1.2 std
	// above each judge's own mean. Calibration must map both to (nearly) the SAME
	// verdict despite harsh's raw ~0.4 and lenient's raw ~1.0 — that's the mean-field
	// property: the judge's absolute scale is removed, only its relative rating counts.
	hs, ls := panelState.stat("harsh"), panelState.stat("lenient")
	cH := calibrateScore(hs.mean+1.2*hs.std(), hs.mean, hs.std())
	cL := calibrateScore(ls.mean+1.2*ls.std(), ls.mean, ls.std())
	if math.Abs(cH-cL) > 0.05 {
		t.Errorf("calibration failed to remove bias: harsh→%.3f, lenient→%.3f (same z should match)", cH, cL)
	}
	if cH < 0.6 || cL < 0.6 {
		t.Errorf("a response above the judge's mean should calibrate high: harsh=%.2f lenient=%.2f", cH, cL)
	}
}

// TestPanelConsensusAndReputation: with a mocked committee where two judges agree and
// one is a rogue outlier, the consensus tracks the majority and the rogue is
// down-weighted after repeated disagreement.
func TestPanelConsensusAndReputation(t *testing.T) {
	resetPanel()
	prev := rawJudgeScore
	t.Cleanup(func() { rawJudgeScore = prev })
	// good, good, rogue-low for the same (good) response, every call.
	raw := map[string]float64{"a": 0.9, "b": 0.85, "rogue": 0.1}
	rawJudgeScore = func(_ *judgeConfig, model, _, _, _ string) (float64, bool) {
		return raw[model], true
	}
	models := []string{"a", "b", "rogue"}
	var reward, conf float64
	var ok bool
	for i := 0; i < 40; i++ {
		reward, conf, ok = panelScore(&judgeConfig{}, models, "code", "p", "r")
		if !ok {
			t.Fatal("panelScore returned ok=false")
		}
	}
	// The rogue must have lost weight relative to the agreeing majority.
	wRogue := panelState.stat("rogue").weight
	wA := panelState.stat("a").weight
	if wRogue >= wA {
		t.Errorf("rogue judge not down-weighted: rogue=%.3f vs a=%.3f", wRogue, wA)
	}
	// Reward should sit with the (calibrated) majority, not be dragged to the rogue.
	if reward < 0.5 {
		t.Errorf("consensus dragged down by rogue: reward=%.2f", reward)
	}
	// Confidence in [0,1].
	if conf < 0 || conf > 1 {
		t.Errorf("confidence out of range: %.2f", conf)
	}
}

// TestPanelResilientToAbstain: a judge that errors abstains; the panel still scores
// from the rest, and returns ok=false only when ALL abstain.
func TestPanelResilientToAbstain(t *testing.T) {
	resetPanel()
	prev := rawJudgeScore
	t.Cleanup(func() { rawJudgeScore = prev })
	rawJudgeScore = func(_ *judgeConfig, model, _, _, _ string) (float64, bool) {
		if model == "down" {
			return 0, false // this judge is unreachable
		}
		return 0.8, true
	}
	if _, _, ok := panelScore(&judgeConfig{}, []string{"up", "down"}, "t", "p", "r"); !ok {
		t.Error("panel should score from the reachable judge when one abstains")
	}
	rawJudgeScore = func(_ *judgeConfig, _, _, _, _ string) (float64, bool) { return 0, false }
	if _, _, ok := panelScore(&judgeConfig{}, []string{"x", "y"}, "t", "p", "r"); ok {
		t.Error("panel must return ok=false when ALL judges abstain")
	}
}
