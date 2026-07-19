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

	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
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
	// trainBootDelay is how long after boot the FIRST fit runs — short enough that
	// frequent redeploys can't perpetually reset a long interval to zero completed
	// cycles (churn-resilience), long enough to let the pod finish serving-init
	// first. After this the trainer settles into the steady ROUTER_TRAIN_EVERY
	// cadence. Overridable via ROUTER_TRAIN_BOOT_DELAY (Go duration).
	trainBootDelay = 90 * time.Second
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
		log.Info("router trainer: disabled (set ROUTER_TRAIN_ENABLED=1 to enable)")
		return
	}
	every := envDuration("ROUTER_TRAIN_EVERY", trainDefaultEvery)
	if every < trainMinEvery {
		every = trainMinEvery
	}
	bootDelay := envDuration("ROUTER_TRAIN_BOOT_DELAY", trainBootDelay)
	log.Info("router trainer: enabled — first fit in %s, then every %s", bootDelay, every)
	go func() {
		// Fit-early, THEN cadence. Running the first cycle shortly after boot —
		// rather than after a full interval — means frequent redeploys can't
		// perpetually reset a long sleep to zero completed cycles: every pod that
		// lives past the short boot delay produces at least one verdict. The
		// promote-or-keep gate is untouched, so a boot fit with thin rewards just
		// logs keep-incumbent (a completed, logged cycle), never a bad promotion.
		fit := func() {
			if err := runRouterTraining(); err != nil {
				log.Warning("router trainer: %v", err)
			}
		}
		time.Sleep(bootDelay)
		fit()
		t := time.NewTicker(every)
		defer t.Stop()
		for range t.C {
			fit()
		}
	}()
}

// runRouterTraining executes ONE training cycle that trains BOTH the shared "*"
// base AND every org's own policy — one ledger read, many fits:
//
//  1. GLOBAL "*" base: fit from the CONSENTING + internal orgs' rewards
//     (TrainingContribution == enabled, plus our own), gate against the live "*"
//     row + conf, deploy to "*". Consent-gated — a customer shares with the base
//     only by choice.
//  2. PER-ORG: for EVERY org with enough of its OWN rewarded rows, fit from that
//     org's OWN data, gate against the org's OWN incumbent row + conf, deploy to
//     the org's OWN row. An org always trains on its own data (its data, its
//     router — no consent needed); resolveAutoModel folds org > "*" > conf, so an
//     org's learned policy wins for that org while the base serves everyone else.
func runRouterTraining() error {
	minEvents := envInt("ROUTER_TRAIN_MIN_EVENTS", trainDefaultMinEvents)
	margin := envFloat("ROUTER_TRAIN_MARGIN", trainDefaultMargin)
	since := ""
	if w := envDuration("ROUTER_TRAIN_WINDOW", 0); w > 0 {
		since = time.Now().UTC().Add(-w).Format(time.RFC3339)
	}

	// One read of the whole rewarded ledger, partitioned by owner.
	all, err := object.GetRewardedRoutingEvents("", since)
	if err != nil {
		return fmt.Errorf("read rewarded ledger: %w", err)
	}
	byOwner := map[string][]*object.RoutingEvent{}
	for _, e := range all {
		if e != nil {
			byOwner[e.Owner] = append(byOwner[e.Owner], e)
		}
	}

	// 1) Global "*" base — consent-gated union of the contributing owners' data.
	consent := map[string]bool{}
	for _, o := range trainingOwnerSet() {
		consent[o] = true
	}
	var globalEvents []*object.RoutingEvent
	for owner, evs := range byOwner {
		if consent[owner] {
			globalEvents = append(globalEvents, evs...)
		}
	}
	if err := trainScope(object.GlobalDefaultOwner, globalEvents, minEvents, margin); err != nil {
		log.Warning("router trainer: global base: %v", err)
	}

	// 2) Per-org — every org trains its OWN policy from its OWN rewards.
	for owner, evs := range byOwner {
		if owner == "" || owner == object.GlobalDefaultOwner || len(evs) < minEvents {
			continue
		}
		if err := trainScope(owner, evs, minEvents, margin); err != nil {
			log.Warning("router trainer: org %s: %v", owner, err)
		}
	}
	return nil
}

// trainScope runs ONE fit→gate→deploy→publish cycle for a scope (the shared "*"
// pseudo-owner or a concrete org) from the given rewarded events: fit per-(task,
// model), gate against THIS scope's incumbent Prefer row + conf, and on a pass
// write THIS scope's Prefer row + publish its artifact meta; on a miss keep the
// incumbent and record the honest verdict. Reused by the base and every org.
func trainScope(scopeOwner string, events []*object.RoutingEvent, minEvents int, margin float64) error {
	arms := fitRouterHeads(events)
	incumbent := orgRouterPreferLookup(scopeOwner)
	confPrefer, _ := confRouterPolicy()
	res := gateRouterFit(arms, incumbent, confPrefer, minEvents, margin)

	version := time.Now().UTC().Format("2006-01-02T15:04Z")
	meta := &object.RouterArtifactMeta{
		Owner:       scopeOwner,
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
		if err := deployRouterPreferTo(scopeOwner, res.Prefer); err != nil {
			meta.Published = false
			meta.Note = "gate passed but deploy failed: " + err.Error()
			_ = object.UpsertRouterArtifactMeta(meta)
			_ = object.AppendRouterTrainingLog(object.NewRouterTrainingLog(meta))
			return fmt.Errorf("deploy prefer: %w", err)
		}
		meta.Published = true
		log.Info("router trainer[%s]: published v%s — %s (events=%d, reward %.3f vs %.3f)",
			scopeOwner, version, res.Note, res.Events, res.NewMean, res.BaseMean)
	} else {
		meta.Published = false
		log.Info("router trainer[%s]: %s (events=%d)", scopeOwner, res.Note, res.Events)
	}
	if err := object.UpsertRouterArtifactMeta(meta); err != nil {
		return err
	}
	// Also append to the IMMUTABLE retrain timeline (best-effort) — this is the row the
	// world.hanzo.ai "Model Improvement" panel counts as a retrain. Every fit logs one,
	// pass OR miss, so the timeline is exactly what happened. Without this the in-process
	// trainer only ever upserts "latest" and the panel shows 0 retrains forever.
	_ = object.AppendRouterTrainingLog(object.NewRouterTrainingLog(meta))
	return nil
}

// deployRouterPreferTo merges the gated best-arm table into a scope's
// OrgSettings.RouterPrefer row (the SAME rows resolveAutoModel folds org > "*" >
// conf) — preserving any task the fit did not touch and the row's
// AutoRouting/session/ceiling fields. The auto-deploy: the router prefers the new
// models on the next request, no reload. scopeOwner is "*" for the shared base or
// a concrete org for a per-org policy.
func deployRouterPreferTo(scopeOwner string, prefer map[string][]string) error {
	existing, err := object.GetOrgSettings(scopeOwner)
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
		Owner:        scopeOwner,
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
	_, err = object.UpdateOrgSettings(scopeOwner, &row)
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
		log.Warning("router trainer: contributor-list read failed (%v); internal orgs only", err)
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

// ── mean-field allocation ─────────────────────────────────────────────────────
//
// bestArmPerTask picks the single highest-mean arm per task. That is correct only
// when following the advice does not change the advice. It does: if every request
// for a task goes to one model, that model saturates — provider rate limits bite,
// the latency knee is crossed, and a cost ceiling is blown — at which point it is no
// longer the best arm, though the table still says it is.
//
// The mean-field (Wardrop) view fixes this. Each request best-responds to the
// AGGREGATE load rather than to a static table, so an arm's effective value falls as
// its allocated share approaches capacity. The equilibrium is the allocation under
// which no request would rather switch. Under hard capacities that equilibrium is
// exactly water-filling: take value in descending order, fill each arm to capacity,
// spill the remainder to the next best.
//
// This is a strict GENERALIZATION, not a replacement: with unlimited capacity and no
// cost weight it allocates everything to the argmax arm and reproduces
// bestArmPerTask exactly (asserted in TestEquilibriumReducesToArgmax).
//
// Note deliberately NOT applied here: mean-field consensus among JUDGES herds — each
// judge revising toward the panel aggregate destroys the independence that made the
// aggregate informative, and measured below plain majority. Mean-field belongs in the
// ALLOCATION (a congestion game), not in the reward's own formation.

// armCapacity describes what an arm costs and how much traffic it can absorb before
// congesting. Capacity <= 0 means unconstrained.
type armCapacity struct {
	Model    string
	Cost     float64 // per-request cost, in whatever unit the caller keeps (compared only)
	Capacity float64 // share of this task's traffic the arm can absorb, 0..1; <=0 = unlimited
}

// equilibriumAllocation returns, per task, the models ordered by their equilibrium
// share (largest first) — the shape deployRouterPreferTo already accepts.
//
// costWeight trades reward against cost: value(a) = mean(a) - costWeight*cost(a).
// Arms below minEvents are not eligible (same rule as bestArmPerTask).
func equilibriumAllocation(
	arms map[string]*armStat,
	caps map[string]armCapacity,
	costWeight float64,
	minEvents int,
) map[string][]string {
	byTask := map[string][]*armStat{}
	for _, a := range arms {
		if a.N >= minEvents {
			byTask[a.Task] = append(byTask[a.Task], a)
		}
	}

	out := map[string][]string{}
	for task, list := range byTask {
		// Cost-adjusted value; ties broken exactly as bestArmPerTask (N desc, then model
		// name) so the degenerate case is bit-identical to the incumbent behaviour.
		value := func(a *armStat) float64 {
			return a.Mean() - costWeight*caps[a.Model].Cost
		}
		sort.Slice(list, func(i, j int) bool {
			vi, vj := value(list[i]), value(list[j])
			if vi != vj {
				return vi > vj
			}
			if list[i].N != list[j].N {
				return list[i].N > list[j].N
			}
			return list[i].Model < list[j].Model
		})

		// Water-fill one unit of demand down the value order.
		remaining := 1.0
		var ordered []string
		for _, a := range list {
			if remaining <= 1e-9 {
				break
			}
			cap := caps[a.Model].Capacity
			take := remaining
			if cap > 0 && cap < take {
				take = cap
			}
			if take <= 1e-9 {
				continue // arm is at zero capacity: it cannot serve, skip it
			}
			ordered = append(ordered, a.Model)
			remaining -= take
		}
		if len(ordered) > 0 {
			out[task] = ordered
		}
	}
	return out
}
