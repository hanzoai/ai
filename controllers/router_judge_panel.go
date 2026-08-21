// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package controllers

import (
	"math"
	"sort"
	"strings"
	"sync"
)

// Mean-Field Judge Panel (MFJP) — the diverse-judges upgrade to the single LLM judge.
//
// Instead of ONE judge (one model's bias, one point of failure, one thing to game), a
// POPULATION of diverse judges scores each response. Two mean-field mechanisms make
// the aggregate trustworthy:
//
//  1. Per-judge CALIBRATION against its own standard (the mean field). Each judge has
//     a running rating distribution (mean, std, online via Welford). A raw score is
//     z-scored against THAT judge's mean/std, then squashed — so a harsh judge's 0.6
//     and a lenient judge's 0.8 both read as "above my own bar" and count equally.
//     Standards are removed, not averaged in.
//  2. REPUTATION dynamics. Each judge carries a reliability weight updated by how well
//     it tracks the calibrated consensus (multiplicative-weights / best-response): a
//     judge that keeps agreeing keeps its weight; one that drifts is exponentially
//     down-weighted. At the mean-field fixed point no judge systematically deviates,
//     and a noisy or adversarial judge is auto-suppressed.
//
// The reward is the reliability-weighted calibrated consensus. Panel DISAGREEMENT
// (dispersion of the calibrated votes) is a free confidence signal: consensus ⇒
// trustworthy reward, spread ⇒ ambiguous case. Diversity (different model families)
// decorrelates systematic error, so you cannot game the panel the way you can game a
// single judge — a model would have to fool the whole self-calibrating population.
//
// State is small (three scalars per judge) and lives in-process; a judge that errors
// simply abstains, so the panel is resilient to any single judge being down/slow.

// judgeStat is one judge's online rating distribution + reliability weight, updated on
// every score it produces so the panel self-calibrates continuously.
type judgeStat struct {
	n      float64 // count of scores observed
	mean   float64 // running mean of RAW scores (this judge's standard)
	m2     float64 // Welford running sum of squared deltas → variance
	weight float64 // reliability in (wMin, 1]: agreement with the consensus
}

// std is the running standard deviation; a modest prior until enough data so early
// calibration neither divides by zero nor over-reacts to one sample.
func (s *judgeStat) std() float64 {
	if s.n < 2 {
		return 0.2
	}
	v := s.m2 / (s.n - 1)
	if v < 1e-6 {
		return 1e-3
	}
	return math.Sqrt(v)
}

// observe folds a raw score into the running distribution (Welford, numerically stable).
func (s *judgeStat) observe(raw float64) {
	s.n++
	d := raw - s.mean
	s.mean += d / s.n
	s.m2 += d * (raw - s.mean)
}

// judgePanel is the calibration state for the whole population, keyed by model id.
type judgePanel struct {
	mu    sync.Mutex
	stats map[string]*judgeStat
}

var panelState = &judgePanel{stats: map[string]*judgeStat{}}

// stat returns (creating if new) the running state for a judge model. Caller holds mu.
func (p *judgePanel) stat(model string) *judgeStat {
	s := p.stats[model]
	if s == nil {
		s = &judgeStat{weight: 1}
		p.stats[model] = s
	}
	return s
}

// calibrateScore maps a judge's raw score onto the common [0,1] scale by z-scoring it
// against the judge's OWN mean/std, then logistic-squashing — the mean-field
// normalization that removes each judge's standard.
func calibrateScore(raw, mean, std float64) float64 {
	if std <= 0 {
		std = 1e-3
	}
	z := (raw - mean) / std
	return 1 / (1 + math.Exp(-z))
}

// rawJudgeScore scores one model — indirected through a var so tests inject a
// deterministic per-model scorer. Production reuses judgeScore with the model swapped.
var rawJudgeScore = func(cfg *judgeConfig, model, task, prompt, response string) (float64, bool) {
	c := *cfg
	c.model = model
	return judgeScore(&c, task, prompt, response)
}

// panel-reputation tuning: lambda = how sharply a drifting judge is down-weighted;
// pullBack = gentle regression toward 1 so a judge recovers after a rough patch; wMin
// = floor so a temporarily-off judge is never fully silenced.
const (
	panelLambda   = 2.0
	panelPullBack = 0.1
	panelWMin     = 0.05
)

// panelScore runs the committee, calibrates each raw score against that judge's own
// standard, updates the online stats + reliability weights, and returns the
// reliability-weighted calibrated CONSENSUS plus a confidence in [0,1] (1 = judges
// agree). ok=false only when NO judge produced a usable score (all abstained).
// judgeConcurrency bounds, PROCESS-WIDE, how many judge scoring calls run at once.
// Each score is an outbound HTTP self-call (judgeScore → POST /v1/chat/completions at
// judgeSelfURL). Without a bound, a stall in the completion path lets these self-calls
// pile up without limit — a production crash-dump once showed ~78,000 goroutines parked
// in net/http cancel watchdogs, all judge/tier/balance self-calls to 127.0.0.1:8000,
// OOM-killing the writer. Judging is a best-effort reward signal, so an un-acquirable
// slot ABSTAINS (the panel is resilient to a missing judge) rather than queueing — the
// fan-out is capped far below the danger zone while the real reward rate (~1/min) never
// touches the bound.
const judgeConcurrency = 32

var judgeSem = make(chan struct{}, judgeConcurrency)

// scoreJudge runs one judge's HTTP scoring OUTSIDE any panel lock, under the global
// judge-concurrency bound. A saturated bound (or an erroring judge) abstains — the
// panel stays resilient either way.
func scoreJudge(cfg *judgeConfig, model, task, prompt, response string) (float64, bool) {
	select {
	case judgeSem <- struct{}{}:
		defer func() { <-judgeSem }()
	default:
		return 0, false // saturated → abstain rather than pile up another self-call
	}
	return rawJudgeScore(cfg, model, task, prompt, response)
}

func panelScore(cfg *judgeConfig, models []string, task, prompt, response string) (reward, confidence float64, ok bool) {
	type vote struct {
		model string
		cal   float64
		w     float64
	}

	// Phase 1 — gather raw judge scores over HTTP with NO panel lock held, under the
	// process-wide concurrency bound. Holding panelState.mu across these (up to 30s
	// each, N per request) serialized ALL judged traffic on one global mutex AND blocked
	// the /v1/ai/router/judge-panel read path (PanelSnapshot); combined with the unbounded
	// self-call fan-out that wedged the writer under load. The calibration/stat updates
	// that DO need the lock are pure in-memory and run in Phase 2 below.
	type rawScore struct {
		model string
		score float64
	}
	raws := make([]rawScore, 0, len(models))
	for _, m := range models {
		s, good := scoreJudge(cfg, m, task, prompt, response)
		if !good {
			continue // an erroring/abstaining judge drops out; the panel stays resilient
		}
		raws = append(raws, rawScore{model: m, score: s})
	}
	if len(raws) == 0 {
		return 0, 0, false
	}

	// Phase 2 — calibrate each raw against that judge's own standard, fold it into the
	// online stats, and update reliability weights. Pure in-memory; the lock is held
	// only here (microseconds), never across an HTTP call.
	votes := make([]vote, 0, len(raws))
	panelState.mu.Lock()
	defer panelState.mu.Unlock()
	for _, r := range raws {
		st := panelState.stat(r.model)
		cal := calibrateScore(r.score, st.mean, st.std())
		st.observe(r.score)
		votes = append(votes, vote{model: r.model, cal: cal, w: st.weight})
	}

	// Reliability-weighted calibrated consensus.
	var sumW, sumWC float64
	for _, v := range votes {
		sumW += v.w
		sumWC += v.w * v.cal
	}
	reward = sumWC / sumW

	// Reputation update (mean-field best-response): agree → keep weight, drift →
	// exponential decay, then a gentle pull back toward 1, clamped to (wMin, 1].
	for _, v := range votes {
		st := panelState.stat(v.model)
		w := st.weight * math.Exp(-panelLambda*math.Abs(v.cal-reward))
		w = (1-panelPullBack)*w + panelPullBack*1.0
		if w < panelWMin {
			w = panelWMin
		} else if w > 1 {
			w = 1
		}
		st.weight = w
	}

	// Confidence = 1 − weighted spread of the calibrated votes around the consensus.
	// Unanimous panel ⇒ 1 (trustworthy reward); wide disagreement ⇒ low (ambiguous
	// case). The calibrated votes are already in [0,1], so their std is in [0,0.5];
	// scale by 2 to fill [0,1] and clamp.
	var wVar float64
	for _, v := range votes {
		d := v.cal - reward
		wVar += v.w * d * d
	}
	spread := math.Sqrt(wVar/sumW) * 2
	if spread > 1 {
		spread = 1
	}
	confidence = 1 - spread
	return reward, confidence, true
}

// PanelJudge is one judge's public, in-process snapshot: its model id, current
// reliability weight, running mean of RAW scores (the judge's own calibration
// baseline), and how many scores it has observed. Scalars + a model id only — no
// content, no PII — so it rides the public /v1/ai/router/judge-panel surface the
// world.hanzo.ai dashboard polls.
type PanelJudge struct {
	Model  string  `json:"model"`
	Weight float64 `json:"weight"`
	Mean   float64 `json:"mean"`
	N      int     `json:"n"`
}

// PanelSnapshot copies the LIVE MFJP calibration state under the lock into a stable,
// deterministically-ordered (by model id) slice of per-judge scalars for the read
// endpoint. Empty when no judge has scored yet ⇒ the endpoint reports available:false.
func PanelSnapshot() []PanelJudge {
	panelState.mu.Lock()
	defer panelState.mu.Unlock()
	out := make([]PanelJudge, 0, len(panelState.stats))
	for model, s := range panelState.stats {
		out = append(out, PanelJudge{Model: model, Weight: s.weight, Mean: s.mean, N: int(s.n)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}

// splitModels parses a comma-separated ROUTER_JUDGE_MODEL into a clean model list
// (trimmed, empties dropped). Always returns at least one element when the input is
// non-empty; an all-empty input yields a single empty string (caller already gated on
// model != "").
func splitModels(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{strings.TrimSpace(s)}
	}
	return out
}
