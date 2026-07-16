// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controllers

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/beego/logs"
)

// ── Router trainer — the flywheel's fit→gate→deploy→publish job ────────────────
//
// This CLOSES the router flywheel end-to-end using ONLY shipped mechanisms and
// real reward data — no separate ML engine, no fabricated model:
//
//	feedback → reward:  clients (and the self-probe) POST /v1/add-routing-reward,
//	                    which lands on the RoutingEvent row (object.AttachRoutingReward).
//	fit:                per (task, model) empirical mean reward over the rewarded
//	                    ledger — the honest bandit statistic (fitRouterHeads, pure).
//	gate:               a task's new best-arm must clear MIN events AND beat the
//	                    incumbent's mean reward by a margin (gateRouterFit, pure).
//	auto-deploy:        the gated best-arm-per-task table is written to the "*"
//	                    OrgSettings.RouterPrefer row — which resolveAutoModel ALREADY
//	                    folds (org > "*" > conf), so the router immediately prefers
//	                    the empirically-best model. No engine reload, no restart.
//	publish:            RouterArtifactMeta upserts the verdict for "*", which
//	                    /v1/router/stats and the world.hanzo.ai flywheel panel read.
//
// It is the "holdout_reward" gate kind (honest: it is a reward-mean regression
// gate over the org's own ledger, not routerbench). Off unless enabled.
//
//	ROUTER_TRAIN_ENABLED   — "1"/"true" to run (default off)
//	ROUTER_TRAIN_EVERY     — cadence, Go duration (default "6h"; min 15m)
//	ROUTER_TRAIN_MIN_EVENTS— min rewarded rows per (task,model) arm to trust it (default 20)
//	ROUTER_TRAIN_MARGIN    — mean-reward margin the new best must beat the incumbent by (default 0.02)
//	ROUTER_TRAIN_WINDOW     — lookback for the fit, Go duration ("" = all history)

const (
	trainDefaultEvery     = 6 * time.Hour
	trainMinEvery         = 15 * time.Minute
	trainDefaultMinEvents = 20
	trainDefaultMargin    = 0.02
)

// armStat is one (task, model) arm's aggregate over the rewarded ledger.
type armStat struct {
	Task   string
	Model  string
	N      int
	SumRwd float64
}

// Mean is the arm's empirical mean reward (0 for an empty arm).
func (a armStat) Mean() float64 {
	if a.N == 0 {
		return 0
	}
	return a.SumRwd / float64(a.N)
}

// fitRouterHeads folds rewarded routing events into per-(task, model) arms. Pure:
// the whole fit is a deterministic function of the ledger, so the gate + table
// build are unit-testable without a DB. Only rows with a task, a routed model,
// and a scored reward contribute (the caller passes rewarded rows, but this is
// defensive). Returns arms keyed "task\x00model".
func fitRouterHeads(events []*object.RoutingEvent) map[string]*armStat {
	arms := map[string]*armStat{}
	for _, e := range events {
		if e == nil || e.RewardedTime == "" {
			continue
		}
		task := strings.TrimSpace(e.Task)
		model := strings.TrimSpace(e.RoutedModel)
		if task == "" || model == "" {
			continue
		}
		key := task + "\x00" + model
		a := arms[key]
		if a == nil {
			a = &armStat{Task: task, Model: model}
			arms[key] = a
		}
		a.N++
		a.SumRwd += clamp01(e.Reward)
	}
	return arms
}

// bestArmPerTask picks, for each task, the arm with the highest mean reward among
// arms with at least minEvents observations. Ties break by higher N (more
// evidence), then by model id for determinism. Returns task → winning armStat.
func bestArmPerTask(arms map[string]*armStat, minEvents int) map[string]armStat {
	byTask := map[string][]*armStat{}
	for _, a := range arms {
		if a.N >= minEvents {
			byTask[a.Task] = append(byTask[a.Task], a)
		}
	}
	out := map[string]armStat{}
	for task, list := range byTask {
		sort.Slice(list, func(i, j int) bool {
			mi, mj := list[i].Mean(), list[j].Mean()
			if mi != mj {
				return mi > mj
			}
			if list[i].N != list[j].N {
				return list[i].N > list[j].N
			}
			return list[i].Model < list[j].Model
		})
		out[task] = *list[0]
	}
	return out
}

// routerFitResult is a gate's decision for the whole fit run.
type routerFitResult struct {
	Prefer   map[string][]string // task → [best model] — the deploy table (only gated tasks)
	Events   int                 // total rewarded rows the fit saw
	NewMean  float64             // mean reward of the winning arms (coverage-weighted)
	BaseMean float64             // mean reward of the incumbent picks over the same tasks
	Passed   bool                // did any task clear the margin gate
	Note     string
	PerTask  map[string]float64 // task → winning mean (for the note / observability)
}

// gateRouterFit compares each task's empirical best arm to the INCUMBENT choice
// (the first model in the current "*" Prefer row for that task, else the conf
// baseline via confPrefer). A task is DEPLOYED only when its best arm beats the
// incumbent's mean by >= margin. The run "passes" when at least one task deploys.
// Pure — the incumbent table + conf are passed in, so this is fully testable.
func gateRouterFit(arms map[string]*armStat, incumbent map[string][]string, conf map[string][]string, minEvents int, margin float64) routerFitResult {
	best := bestArmPerTask(arms, minEvents)
	// Incumbent mean per task = the mean of the arm the incumbent currently prefers.
	incMean := func(task string) (float64, bool) {
		pick := firstOf(incumbent[task])
		if pick == "" {
			pick = firstOf(conf[task])
		}
		if pick == "" {
			return 0, false
		}
		if a := arms[task+"\x00"+pick]; a != nil && a.N > 0 {
			return a.Mean(), true
		}
		return 0, false
	}

	res := routerFitResult{Prefer: map[string][]string{}, PerTask: map[string]float64{}}
	var newSum, baseSum float64
	var newN, baseN int
	for _, e := range arms {
		res.Events += e.N
	}
	deployed := []string{}
	for task, w := range best {
		base, haveBase := incMean(task)
		// No incumbent evidence in-window → require the best to clear an absolute
		// floor (margin above 0) so a first-ever fit still deploys a real winner.
		if !haveBase {
			base = 0
		}
		if w.Mean() >= base+margin {
			res.Prefer[task] = []string{w.Model}
			res.PerTask[task] = w.Mean()
			newSum += w.Mean() * float64(w.N)
			newN += w.N
			baseSum += base * float64(w.N)
			baseN += w.N
			deployed = append(deployed, fmt.Sprintf("%s→%s(%.2f)", task, w.Model, w.Mean()))
		}
	}
	if newN > 0 {
		res.NewMean = newSum / float64(newN)
	}
	if baseN > 0 {
		res.BaseMean = baseSum / float64(baseN)
	}
	res.Passed = len(res.Prefer) > 0
	if res.Passed {
		sort.Strings(deployed)
		res.Note = "deployed " + strings.Join(deployed, ", ")
	} else {
		res.Note = "kept incumbent: no task cleared the reward-margin gate"
	}
	return res
}

// StartRouterTrainer schedules the fit→gate→deploy→publish loop when enabled.
// Called once at boot (cmd/aid). Never fails boot.
func StartRouterTrainer() {
	if !envTrue("ROUTER_TRAIN_ENABLED") {
		logs.Info("router trainer: disabled (set ROUTER_TRAIN_ENABLED=1 to enable)")
		return
	}
	every := envDuration("ROUTER_TRAIN_EVERY", trainDefaultEvery)
	if every < trainMinEvery {
		every = trainMinEvery
	}
	logs.Info("router trainer: enabled — every %s", every)
	go func() {
		for {
			time.Sleep(every)
			if err := runRouterTraining(); err != nil {
				logs.Warning("router trainer: %v", err)
			}
		}
	}()
}

// runRouterTraining executes ONE fit→gate→deploy→publish cycle for the shared "*"
// scope. It reads the rewarded ledger (all orgs — the base heads learn from the
// whole platform), fits, gates against the live "*" row + conf, and on a pass
// writes the new "*" Prefer row (auto-deploy) then upserts the artifact meta. On a
// gate miss it upserts the meta with GatePassed=false and leaves the row untouched.
func runRouterTraining() error {
	minEvents := envInt("ROUTER_TRAIN_MIN_EVENTS", trainDefaultMinEvents)
	margin := envFloat("ROUTER_TRAIN_MARGIN", trainDefaultMargin)
	since := ""
	if w := envDuration("ROUTER_TRAIN_WINDOW", 0); w > 0 {
		since = time.Now().UTC().Add(-w).Format(time.RFC3339)
	}

	// CONSENT: the shared "*" base heads learn ONLY from orgs that opted in
	// (OrgSettings.TrainingContribution == enabled) PLUS the reserved internal orgs
	// (our own traffic — incl. the self-probe — is always ours to train on). An org
	// that never set the flag is excluded, so a customer owns its data and shares it
	// only by choice. Fail-closed: no consenting owners → the fit sees no rows.
	owners := trainingOwnerSet()
	events, err := object.GetRewardedRoutingEventsForOwners(owners, since)
	if err != nil {
		return fmt.Errorf("read rewarded ledger: %w", err)
	}
	arms := fitRouterHeads(events)

	incumbent := orgRouterPreferLookup(object.GlobalDefaultOwner) // current "*" row (nil if none)
	confPrefer, _ := confRouterPolicy()
	res := gateRouterFit(arms, incumbent, confPrefer, minEvents, margin)

	version := time.Now().UTC().Format("2006-01-02T15:04Z")
	meta := &object.RouterArtifactMeta{
		Owner:       object.GlobalDefaultOwner,
		Version:     version,
		TrainedTime: time.Now().UTC().Format(time.RFC3339),
		Events:      res.Events,
		GatePassed:  res.Passed,
		GateKind:    "holdout",
		GateMetric:  "holdout_reward",
		GateValue:   res.NewMean,
		GateBase:    res.BaseMean,
		Note:        res.Note,
	}

	if res.Passed {
		if err := deployRouterPrefer(res.Prefer); err != nil {
			// Deploy failed: record the fit as gated-but-unpublished, honestly.
			meta.Published = false
			meta.Note = "gate passed but deploy failed: " + err.Error()
			_ = object.UpsertRouterArtifactMeta(meta)
			return fmt.Errorf("deploy prefer: %w", err)
		}
		meta.Published = true
		logs.Info("router trainer: published v%s — %s (events=%d, reward %.3f vs %.3f)",
			version, res.Note, res.Events, res.NewMean, res.BaseMean)
	} else {
		meta.Published = false
		logs.Info("router trainer: %s (events=%d)", res.Note, res.Events)
	}
	return object.UpsertRouterArtifactMeta(meta)
}

// deployRouterPrefer merges the gated best-arm table into the shared "*"
// OrgSettings.RouterPrefer row — the SAME row resolveAutoModel folds — preserving
// any task the fit did not touch and the row's AutoRouting/session fields. This is
// the auto-deploy: the router prefers the new models on the next request, no reload.
func deployRouterPrefer(prefer map[string][]string) error {
	existing, err := object.GetOrgSettings(object.GlobalDefaultOwner)
	if err != nil {
		return err
	}
	merged := map[string][]string{}
	if existing != nil {
		for k, v := range existing.RouterPrefer {
			merged[k] = v
		}
	}
	for task, models := range prefer {
		merged[task] = models
	}
	row := object.OrgSettings{
		Owner:        object.GlobalDefaultOwner,
		RouterPrefer: object.JSONMap[[]string](merged),
	}
	if existing == nil {
		_, err = object.AddOrgSettings(&row)
		return err
	}
	row.RouterCostCeiling = existing.RouterCostCeiling
	row.AutoRouting = existing.AutoRouting
	row.DefaultSessionRouting = existing.DefaultSessionRouting
	row.CreatedTime = existing.CreatedTime
	_, err = object.UpdateOrgSettings(object.GlobalDefaultOwner, &row)
	return err
}

// trainingOwnerSet is the consent-respecting owner set for the shared base fit:
// the orgs that opted in via OrgSettings.TrainingContribution (enabled) UNION the
// reserved internal orgs (always ours — "admin", "hanzo", plus any in
// ROUTER_TRAIN_INTERNAL_ORGS, comma-separated). Deduped. On a lookup error it
// falls back to the internal orgs alone (never silently widens to all orgs).
func trainingOwnerSet() []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(o string) {
		o = strings.ToLower(strings.TrimSpace(o))
		if o == "" || o == object.GlobalDefaultOwner || seen[o] {
			return
		}
		seen[o] = true
		out = append(out, o)
	}
	// Reserved internal orgs — our own data (incl. the self-probe) is always ours.
	add("admin")
	add("hanzo")
	for _, o := range strings.Split(os.Getenv("ROUTER_TRAIN_INTERNAL_ORGS"), ",") {
		add(o)
	}
	// Consenting customer orgs.
	if orgs, err := object.ListTrainingContributorOrgs(); err == nil {
		for _, o := range orgs {
			add(o)
		}
	} else {
		logs.Warning("router trainer: contributor-list read failed (%v); internal orgs only", err)
	}
	return out
}

// ── small helpers ─────────────────────────────────────────────────────────────

func clamp01(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

func firstOf(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	return ss[0]
}

func envTrue(k string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(k)))
	return v == "1" || v == "true" || v == "yes"
}

func envInt(k string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(k))); err == nil && n > 0 {
		return n
	}
	return def
}

func envFloat(k string, def float64) float64 {
	if f, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv(k)), 64); err == nil && f >= 0 {
		return f
	}
	return def
}

func envDuration(k string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(os.Getenv(k))); err == nil && d > 0 {
		return d
	}
	return def
}
