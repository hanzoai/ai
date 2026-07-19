// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package controllers

import "github.com/hanzoai/ai/object"

// Read-only view of the LIVE Mean-Field Judge Panel (router_judge_panel.go) for the
// world.hanzo.ai dashboard. Same class as /v1/router/stats?scope=platform: PUBLIC,
// platform-global, aggregates-only — model ids + scalars, no content, no PII, no org
// data — so the world widget polls it unauthenticated exactly like router-stats.

// judgeBenchmark is the STATIC, published REFERENCE benchmark from the MFJP
// ground-truth experiment (TestMFJPRecoversGroundTruth in router_judge_experiment_test.go):
// Spearman rank-correlation with KNOWN ground truth for the panel vs the baselines,
// scored on the post-warmup half at a fixed seed (reproducible). It is the reference
// proof shown BESIDE the live panel — NOT a live measurement.
type judgeBenchmark struct {
	MFJP            float64 `json:"mfjp"`
	NaiveMean       float64 `json:"naiveMean"`
	SingleNoisy     float64 `json:"singleNoisy"`
	SingleAdversary float64 `json:"singleAdversary"`
}

// publishedJudgeBenchmark are the exact numbers TestMFJPRecoversGroundTruth prints
// (fixed seed 42, n=400 post-warmup). Published as the endpoint's reference benchmark
// so the dashboard can render "panel vs baselines" without re-running the experiment.
// Update these in lockstep with the experiment if its setup ever changes.
var publishedJudgeBenchmark = judgeBenchmark{
	MFJP:            0.966, // calibrated + reputation-weighted consensus
	NaiveMean:       0.945, // unweighted, uncalibrated mean (incl. the adversary)
	SingleNoisy:     0.870, // one unreliable single judge
	SingleAdversary: 0.110, // one adversarial single judge (pure noise)
}

// judgePanelState is the /v1/router/judge-panel wire contract (world.hanzo.ai depends
// on this shape — do not deviate). `models` + `enabled` + `sampleRate` are the
// configured/dynamic posture; `judges` is the LIVE in-process calibration state (empty
// ⇒ available:false); `benchmark` is the static published proof above.
type judgePanelState struct {
	Available  bool           `json:"available"`
	Enabled    bool           `json:"enabled"`
	SampleRate float64        `json:"sampleRate"`
	Models     []string       `json:"models"`
	Judges     []PanelJudge   `json:"judges"`
	Benchmark  judgeBenchmark `json:"benchmark"`
}

// buildJudgePanelState assembles the wire contract from the resolved judge config +
// the live panel snapshot. Pure over its inputs (no web, no DB) so the exact contract
// is unit-testable: available is true iff at least one judge has scored, the panel and
// posture come from the dynamic config, and the static benchmark rides along.
func buildJudgePanelState(cfg object.JudgeConfig, judges []PanelJudge) judgePanelState {
	return judgePanelState{
		Available:  len(judges) > 0,
		Enabled:    cfg.Enabled,
		SampleRate: cfg.Sample,
		Models:     splitModels(cfg.Models),
		Judges:     judges,
		Benchmark:  publishedJudgeBenchmark,
	}
}

// GetRouterJudgePanel returns the LIVE Mean-Field Judge Panel state: the configured
// panel + dynamic judge posture (enabled/sample) resolved from the "*"
// GlobalDefaultOwner row, the live in-process per-judge calibration (weight/mean/n),
// and the static published benchmark. PUBLIC-safe and platform-global (model ids +
// scalars only), so it rides the same unauthenticated, balance-exempt class as
// /v1/router/stats?scope=platform — the world widget polls it with no auth. The judge
// state is a single in-process population (not per-org), so there is nothing to scope;
// ?scope=platform is accepted for symmetry with router-stats.
//
// @Title GetRouterJudgePanel
// @Tag Router API
// @Description live Mean-Field Judge Panel state (public, platform-global; model ids + scalars only)
// @Param scope query string false "'platform' (default) — the public panel snapshot"
// @Success 200 {object} controllers.judgePanelState The Response object
// @router /router/judge-panel [get]
func (c *ApiController) GetRouterJudgePanel() {
	c.ResponseOk(buildJudgePanelState(loadJudgeConfig(), PanelSnapshot()))
}
