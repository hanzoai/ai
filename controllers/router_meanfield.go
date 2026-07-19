// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package controllers

import (
	"math"
	"sync"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/router"
)

// Mean-Field routing LAYER — the congestion-aware equilibrium on top of the Enso
// router's per-(task,model) selection. The trainer (router_trainer.go) already fits a
// value per arm and resolveAutoModel already picks the champion; this layer composes
// with that selection WITHOUT replacing it, gated OFF by default so live routing is
// byte-identical until an admin enables it at admin.hanzo.ai.
//
// Two mean-field mechanisms, each a pure function so the whole layer is unit-testable
// and proven by a Monte-Carlo ground-truth experiment (router_meanfield_experiment_test.go):
//
//  1. Value CALIBRATION across the model population. Given the per-model estimated
//     values for a task, each model's value is z-scored against the FIELD of
//     candidates for that task (mean/std over the set). Selection then turns on how
//     far a model stands out from its peers, not its absolute number — so a model with
//     a few lucky rewards on thin data cannot dominate the way a raw argmax lets it.
//     Robust selection under noisy estimates. Pure: calibrateModelValues.
//  2. Congestion-aware routing EQUILIBRIUM (the classic mean-field game). Each request
//     is a player choosing a model; the AGGREGATE load on a model — its share of recent
//     routing decisions (the mean field) — raises its effective cost. Effective score =
//     calibrated_value − β·load_share. Best-responding to this mean field yields
//     load-balancing AND sustained exploration as an EQUILIBRIUM, not a bolted-on
//     heuristic: traffic stops stampeding onto the current-best model, so a model whose
//     realized quality degrades under load (finite capacity) is spared self-inflicted
//     overload. Pure: meanFieldSelect.
//
// The live load mean field is an in-process rolling window of the last N routing
// decisions (loadTracker; a small mutex-guarded ring). When the gate is enabled,
// resolveAutoModel best-responds to it over the task's SERVABLE candidate set and
// records the chosen model back into the window (best-response → join the field, the
// canonical mean-field dynamic). When disabled it does neither — no re-rank, no record.

// calibrateModelValues z-scores each model's estimated value against the FIELD of
// candidates for a task (mean/std over the whole set) — the mean-field normalization
// that measures a model by how far it stands out from its peers rather than by its
// absolute score. Robust to noisy per-model estimates: a lucky spike on thin data is
// only as strong as the field's spread makes it. Pure over its input.
//
// Degenerate fields carry no differentiation and calibrate to all-zero: an empty or
// single-model set (nothing to stand out from) and a set whose values are all equal
// (no spread). The returned map has the SAME keys as the input.
func calibrateModelValues(values map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(values))
	n := len(values)
	if n < 2 {
		for m := range values {
			out[m] = 0
		}
		return out
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	mean := sum / float64(n)
	var ss float64
	for _, v := range values {
		d := v - mean
		ss += d * d
	}
	std := math.Sqrt(ss / float64(n)) // population std over the candidate field
	if std < 1e-9 {
		for m := range values {
			out[m] = 0 // all values equal ⇒ the field is flat, nothing to differentiate
		}
		return out
	}
	for m, v := range values {
		out[m] = (v - mean) / std
	}
	return out
}

// meanFieldSelect is the congestion-aware best response: it calibrates the values
// against the field, subtracts β times each model's live load share (the mean field),
// and returns the argmax effective score plus the full effective-score map (for
// observability + tests). β=0 reduces to "pick the highest calibrated value" = the
// field-normalized champion; β>0 penalizes crowded models so traffic spreads across
// near-equal candidates AS AN EQUILIBRIUM. A model absent from loads has zero share.
// Ties break by model id for determinism. Pure over its inputs — no shared state.
func meanFieldSelect(values, loads map[string]float64, beta float64) (string, map[string]float64) {
	cal := calibrateModelValues(values)
	eff := make(map[string]float64, len(cal))
	for m, c := range cal {
		eff[m] = c - beta*loads[m]
	}
	best := ""
	var bestScore float64
	for m, s := range eff {
		if best == "" || s > bestScore || (s == bestScore && m < best) {
			best, bestScore = m, s
		}
	}
	return best, eff
}

// rankValues turns the router's ORDERED candidate list for a task (rank 0 = the
// router's best, as curated by conf + written by the trainer) into cardinal values by
// a linear descending map: rank i → (N−i). This is the assumption-minimal
// cardinalization of an ordinal preference — the router's own ordering IS the value
// signal, and calibrateModelValues then normalizes the scale. So the mean-field layer
// spreads load across near-equal-preference candidates rather than inventing quality
// numbers the router never produced.
func rankValues(candidates []string) map[string]float64 {
	out := make(map[string]float64, len(candidates))
	n := len(candidates)
	for i, m := range candidates {
		out[m] = float64(n - i)
	}
	return out
}

// loadTracker is the in-process rolling load mean field: the last `size` routing
// decisions in a ring, with an incrementally-maintained per-model count so shares are
// O(models) to read. Small and mutex-guarded — it sits on the `auto` hot path only
// when the gate is enabled.
type loadTracker struct {
	mu     sync.Mutex
	size   int
	ring   []string       // circular buffer of recent decisions ("" = empty slot)
	pos    int            // next write index
	filled int            // non-empty slots (grows to size, then constant)
	counts map[string]int // model → occurrences currently in the ring
}

// newLoadTracker returns a rolling load window of the given capacity.
func newLoadTracker(size int) *loadTracker {
	if size < 1 {
		size = 1
	}
	return &loadTracker{size: size, ring: make([]string, size), counts: map[string]int{}}
}

// record folds one routing decision into the window, evicting the oldest when full.
func (t *loadTracker) record(model string) {
	if model == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if old := t.ring[t.pos]; old != "" {
		if t.counts[old]--; t.counts[old] <= 0 {
			delete(t.counts, old)
		}
	} else {
		t.filled++
	}
	t.ring[t.pos] = model
	t.counts[model]++
	t.pos = (t.pos + 1) % t.size
}

// shares returns each model's fraction of the current window (empty when no decision
// has been recorded yet). The keys are exactly the models present in the window.
func (t *loadTracker) shares() map[string]float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]float64, len(t.counts))
	if t.filled == 0 {
		return out
	}
	for m, c := range t.counts {
		out[m] = float64(c) / float64(t.filled)
	}
	return out
}

// share returns one model's fraction of the current window (0 when empty/absent).
func (t *loadTracker) share(model string) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.filled == 0 {
		return 0
	}
	return float64(t.counts[model]) / float64(t.filled)
}

// routerLoadWindow is how many recent `auto` decisions define the live load mean
// field. Large enough to be a stable share signal, small enough to track drift within
// a burst and to keep the ring tiny.
const routerLoadWindow = 512

// routerLoads is the process-wide live load mean field, fed only when the congestion
// layer is enabled (a disabled gate records nothing, so the window is inert).
var routerLoads = newLoadTracker(routerLoadWindow)

// SourceMeanField labels a RoutingEvent whose model the congestion layer chose OFF the
// base champion — an honest origin tag in the ledger, distinct from engine/heuristic/
// explore, set ONLY when the mean-field re-rank actually changed the pick.
const SourceMeanField = "meanfield"

// meanFieldCfg is the resolved gate config source — indirected through a var so tests
// drive the layer without a live settings row. Production reads the 60s-cached "*"
// GlobalDefaultOwner row (default: DISABLED).
var meanFieldCfg = object.GetCachedMeanFieldConfig

// meanFieldRoute is the GATED live adapter that composes the congestion layer with the
// base decision. It is the ONE place the gate is read:
//
//   - DISABLED (default): returns (champion, false) immediately — no candidate scan,
//     no load record, no re-rank. Live routing is byte-identical to pre-layer behavior.
//   - ENABLED: best-responds to the live load mean field over the task's SERVABLE
//     candidates (client.Known already filters to models the caller can actually
//     serve, so the layer never routes to a model that would 403/404), records the
//     chosen model into the window, and returns (model, model != champion).
//
// With a single servable candidate there is nothing to spread over, so it records the
// champion (keeping the load field warm) and leaves the pick unchanged.
func meanFieldRoute(client router.Client, task router.Task, champion string) (string, bool) {
	cfg := meanFieldCfg()
	if !cfg.Enabled {
		return champion, false
	}
	cands := client.Policy.Candidates(task, client.Known)
	if len(cands) < 2 {
		routerLoads.record(champion)
		return champion, false
	}
	best, _ := meanFieldSelect(rankValues(cands), routerLoads.shares(), cfg.Beta)
	if best == "" {
		best = champion
	}
	routerLoads.record(best)
	return best, best != champion
}
