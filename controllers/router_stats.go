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
	"encoding/json"
	"sort"
	"strconv"
	"time"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

// Router observability surface: aggregates-only over the RoutingEvent ledger, for
// the admin savings-vs-perf panel and the world.hanzo.ai live-training widget. It
// NEVER emits raw events, feature vectors, prompt-derived data, or per-request
// rows — only counts, rates, and blended price indices. See
// universe/docs/architecture/personal-router-training.md.

const (
	// routerStatsDefaultHours bounds the default window to a rolling day, matching
	// the nightly retrain cadence and the "last 24h" the world widget shows.
	routerStatsDefaultHours = 24
	// routerStatsMaxHours caps the window so a scan of the ledger stays bounded.
	routerStatsMaxHours = 24 * 90
	// routerStatsBuckets is the per-hour throughput resolution for the sparkline.
	routerStatsBuckets = 24
	// scopePlatform is the public-safe aggregate (no auth, no org, no absolute $).
	scopePlatform = "platform"
	// scopeOrg is the authenticated, org-scoped aggregate (carries $ indices).
	scopeOrg = "org"
)

// statsWindow is the resolved time window + the total events scanned in it.
type statsWindow struct {
	Since  string `json:"since"`
	Until  string `json:"until"`
	Events int    `json:"events"`
}

// costStats is the cost-saved surface. All indices are BLENDED $/MTok proxies:
// the RoutingEvent ledger carries no token counts (privacy — only the served model
// id), so cost is per-request unit-priced by the served model's blended
// (input+output)/2 $/MTok. Savings are therefore a proportional proxy, not a
// billed dollar figure — honest and comparable, never presented as actual spend.
// RoutedIndex/CounterfactualIndex are absolute price levels and are OMITTED in the
// public platform scope (pointers → nil); the ratios and the counterfactual model
// id are safe to expose publicly.
type costStats struct {
	RoutedIndex          *float64 `json:"routed_index,omitempty"`         // avg served-model blended $/MTok
	CounterfactualIndex  *float64 `json:"counterfactual_index,omitempty"` // best-single-model blended $/MTok
	SavedPct             float64  `json:"saved_pct"`                      // (cf-routed)/cf * 100
	CumulativeSavedIndex float64  `json:"cumulative_saved_index"`         // sum of per-request (cf-routed)
	BaselineModel        string   `json:"baseline_model"`                 // the counterfactual single model
	PricedEvents         int      `json:"priced_events"`                  // events with a known price (coverage)
}

// qualityStats is the quality-proxy surface: outcome reward rate (where scored),
// the learned-engine share vs the heuristic fallback, mean engine confidence, and
// shadow-vs-served agreement when the ledger carries a shadow pick (nil until that
// field lands — the reader stays tolerant of both old and new rows).
type qualityStats struct {
	RewardRate      float64  `json:"reward_rate"`
	RewardedEvents  int      `json:"rewarded_events"`
	EngineShare     float64  `json:"engine_share"`
	AvgConfidence   float64  `json:"avg_confidence"`
	ShadowAgreement *float64 `json:"shadow_agreement"`
}

// taskStats is one task's routed-model distribution.
type taskStats struct {
	Events int            `json:"events"`
	Models map[string]int `json:"models"`
}

// throughputStats is the per-hour event count over the window, oldest bucket first
// (index len-1 is the current hour) — the world widget's live sparkline.
type throughputStats struct {
	PerHour     []int `json:"per_hour"`
	TotalWindow int   `json:"total_window"`
}

// retrainMeta is the public-safe published-state of the scope's heads artifact:
// what the nightly retrain last produced and whether the gate cleared it. Carries
// no weights and no event content, so it rides the public platform surface.
type retrainMeta struct {
	Version     string  `json:"version"`
	TrainedTime string  `json:"trained_time"`
	Events      int     `json:"events"`
	GatePassed  bool    `json:"gate_passed"`
	Published   bool    `json:"published"`
	GateKind    string  `json:"gate_kind"`
	GateMetric  string  `json:"gate_metric"`
	GateValue   float64 `json:"gate_value"`
	GateBase    float64 `json:"gate_base"`
	Note        string  `json:"note"`
}

// routerStats is the whole aggregate. Cost is nil-able so the public platform
// scope can drop it entirely if no event was priceable; Org is omitted publicly.
// Retrain is nil until the nightly job has published a status for the scope.
type routerStats struct {
	Scope      string               `json:"scope"`
	Org        string               `json:"org,omitempty"`
	Window     statsWindow          `json:"window"`
	Cost       *costStats           `json:"cost,omitempty"`
	Quality    qualityStats         `json:"quality"`
	ByTask     map[string]taskStats `json:"by_task"`
	ByModel    map[string]int       `json:"by_model"`
	Throughput throughputStats      `json:"throughput"`
	Retrain    *retrainMeta         `json:"retrain,omitempty"`
}

// getRouterArtifactMeta is indirected through a var so the stats contract is
// testable without a live DB.
var getRouterArtifactMeta = object.GetRouterArtifactMeta

// attachRetrainMeta folds the scope's published retrain status into the aggregate
// (nil-safe: no row → no `retrain` block). owner "*" is the shared base heads.
func attachRetrainMeta(stats *routerStats, owner string) {
	m, err := getRouterArtifactMeta(owner)
	if err != nil || m == nil {
		return
	}
	stats.Retrain = &retrainMeta{
		Version:     m.Version,
		TrainedTime: m.TrainedTime,
		Events:      m.Events,
		GatePassed:  m.GatePassed,
		Published:   m.Published,
		GateKind:    m.GateKind,
		GateMetric:  m.GateMetric,
		GateValue:   m.GateValue,
		GateBase:    m.GateBase,
		Note:        m.Note,
	}
}

// priceIndexFn returns a model's blended $/MTok price index (0 = unknown/free).
type priceIndexFn func(model string) float64

// computeRouterStats folds a window of routing events into the aggregate. Pure over
// its inputs (no DB, no beego) so the whole contract is unit-testable. `now` bounds
// the throughput buckets; `windowStart` is the oldest included instant;
// `includeAbsoluteCost` keeps the absolute $/MTok levels (org scope) vs dropping
// them (public platform scope). Events are assumed already filtered to the window
// by the caller (the DB query does `created_time >= since`).
func computeRouterStats(events []*object.RoutingEvent, price priceIndexFn, windowStart, now time.Time, scope string, org string, includeAbsoluteCost bool) routerStats {
	byModel := map[string]int{}
	byTask := map[string]taskStats{}
	perHour := make([]int, routerStatsBuckets)

	var rewardSum float64
	var rewardedN int
	var engineN int
	var confSum float64
	var confN int
	// Shadow-vs-served agreement: over family rows carrying a shadow pick, the share
	// where the learned engine WOULD have chosen what actually served — the production
	// A/B agreement rate (now that RoutingEvent.ShadowModel lands it).
	var shadowN, shadowAgreeN int

	// First pass: distributions, quality, throughput, and the model price set.
	prices := map[string]float64{}
	windowSecs := now.Sub(windowStart).Seconds()
	for _, e := range events {
		if e.ShadowModel != "" {
			shadowN++
			if e.ShadowModel == e.RoutedModel {
				shadowAgreeN++
			}
		}
		if e.RoutedModel != "" {
			byModel[e.RoutedModel]++
			if _, seen := prices[e.RoutedModel]; !seen {
				prices[e.RoutedModel] = price(e.RoutedModel)
			}
			ts := byTask[e.Task]
			if ts.Models == nil {
				ts.Models = map[string]int{}
			}
			ts.Events++
			ts.Models[e.RoutedModel]++
			byTask[e.Task] = ts
		}
		if e.Source == "engine" {
			engineN++
		}
		if e.Confidence > 0 {
			confSum += e.Confidence
			confN++
		}
		if e.RewardedTime != "" {
			rewardSum += e.Reward
			rewardedN++
		}
		if b := hourBucket(e.CreatedTime, windowStart, windowSecs); b >= 0 {
			perHour[b]++
		}
	}

	// Baseline = the priced served model with the highest blended index: the
	// premium single model an org would default to WITHOUT routing. Cost saved is
	// what routing to cheaper-but-capable models buys vs that baseline.
	baselineModel, baselinePrice := maxPriced(prices)
	var routedSum, cfSum, cumSaved float64
	priced := 0
	if baselinePrice > 0 {
		for _, e := range events {
			p, ok := prices[e.RoutedModel]
			if !ok || p <= 0 {
				continue
			}
			routedSum += p
			cfSum += baselinePrice
			cumSaved += baselinePrice - p
			priced++
		}
	}

	total := len(events)
	q := qualityStats{ShadowAgreement: nil}
	if shadowN > 0 {
		agree := float64(shadowAgreeN) / float64(shadowN)
		q.ShadowAgreement = &agree
	}
	if rewardedN > 0 {
		q.RewardRate = rewardSum / float64(rewardedN)
	}
	q.RewardedEvents = rewardedN
	if total > 0 {
		q.EngineShare = float64(engineN) / float64(total)
	}
	if confN > 0 {
		q.AvgConfidence = confSum / float64(confN)
	}

	out := routerStats{
		Scope:   scope,
		Window:  statsWindow{Since: windowStart.UTC().Format(time.RFC3339), Until: now.UTC().Format(time.RFC3339), Events: total},
		Quality: q,
		ByTask:  byTask,
		ByModel: byModel,
		Throughput: throughputStats{
			PerHour:     perHour,
			TotalWindow: total,
		},
	}
	if scope == scopeOrg {
		out.Org = org
	}

	if priced > 0 {
		cost := &costStats{
			SavedPct:             pct(cfSum-routedSum, cfSum),
			CumulativeSavedIndex: round4(cumSaved),
			BaselineModel:        baselineModel,
			PricedEvents:         priced,
		}
		if includeAbsoluteCost {
			ri := round4(routedSum / float64(priced))
			ci := round4(cfSum / float64(priced))
			cost.RoutedIndex = &ri
			cost.CounterfactualIndex = &ci
		}
		out.Cost = cost
	}

	// Enso is a PRIVATE, closed family: the public platform surface must never
	// name an upstream arm (claude/gpt/deepseek/...). Relabel every served model id
	// to an opaque, stable Enso arm label (arm-1 = the premium/priciest arm), so the
	// world.hanzo.ai widget shows real routing SHARE without leaking the roster.
	if scope == scopePlatform {
		labels := armLabelsFor(prices)
		out.ByModel = relabelCounts(out.ByModel, labels)
		for task, ts := range out.ByTask {
			ts.Models = relabelCounts(ts.Models, labels)
			out.ByTask[task] = ts
		}
		if out.Cost != nil {
			out.Cost.BaselineModel = labels[out.Cost.BaselineModel]
		}
	}
	return out
}

// armLabelsFor maps each concrete model id to a stable opaque Enso arm label,
// ordered by price DESC (arm-1 = the premium arm) with ties broken by id — price
// rank is far more stable than volume, so an arm keeps its identity across polls.
// Unpriced/free arms are numbered after the priced ones (still opaque). This is the
// ONE place upstream ids are turned into public arm labels.
func armLabelsFor(prices map[string]float64) map[string]string {
	ids := make([]string, 0, len(prices))
	for id := range prices {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		pi, pj := prices[ids[i]], prices[ids[j]]
		if pi != pj {
			return pi > pj
		}
		return ids[i] < ids[j]
	})
	labels := make(map[string]string, len(ids))
	for i, id := range ids {
		labels[id] = "arm-" + strconv.Itoa(i+1)
	}
	return labels
}

// relabelCounts folds a model-keyed count map through the arm labels; an id with no
// label (never priced, so absent from the labeler) is bucketed as "arm-other" so no
// concrete id can ever escape onto the public surface.
func relabelCounts(byId map[string]int, labels map[string]string) map[string]int {
	out := make(map[string]int, len(byId))
	for id, n := range byId {
		label := labels[id]
		if label == "" {
			label = "arm-other"
		}
		out[label] += n
	}
	return out
}

// maxPriced returns the model with the highest positive price index (ties broken
// by model id for determinism), or ("",0) when none is priced.
func maxPriced(prices map[string]float64) (string, float64) {
	best := ""
	var bestP float64
	for m, p := range prices {
		if p <= 0 {
			continue
		}
		if p > bestP || (p == bestP && m < best) {
			best, bestP = m, p
		}
	}
	return best, bestP
}

// hourBucket maps an RFC3339 timestamp to a [0, routerStatsBuckets) index within
// [windowStart, now], oldest first; -1 when out of range or unparseable.
func hourBucket(createdTime string, windowStart time.Time, windowSecs float64) int {
	if windowSecs <= 0 {
		return -1
	}
	t, err := time.Parse(time.RFC3339, createdTime)
	if err != nil {
		return -1
	}
	off := t.Sub(windowStart).Seconds()
	if off < 0 || off > windowSecs {
		return -1
	}
	b := int(off / windowSecs * float64(routerStatsBuckets))
	if b >= routerStatsBuckets {
		b = routerStatsBuckets - 1
	}
	if b < 0 {
		b = 0
	}
	return b
}

func pct(num, den float64) float64 {
	if den <= 0 {
		return 0
	}
	return round4(num / den * 100)
}

func round4(f float64) float64 {
	return float64(int64(f*10000+0.5*sign(f))) / 10000
}

func sign(f float64) float64 {
	if f < 0 {
		return -1
	}
	return 1
}

// blendedPriceForOrg is the production price index: the served model's
// (input+output)/2 $/MTok resolved through the org-aware pricing table. Free/local
// or unknown models return 0 and are excluded from the cost math (honest coverage).
func blendedPriceForOrg(org string) priceIndexFn {
	return func(model string) float64 {
		p := getModelPriceForOrg(model, org)
		return (p.InputPerMillion + p.OutputPerMillion) / 2
	}
}

// resolveStatsWindow parses ?since= (RFC3339) or ?hours= (default 24, capped) into
// a [windowStart, now]. ?since wins when both are present.
func (c *ApiController) resolveStatsWindow() (windowStart, now time.Time, sinceStr string) {
	now = time.Now().UTC()
	if s := c.Input().Get("since"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t.UTC(), now, t.UTC().Format(time.RFC3339)
		}
	}
	hours := routerStatsDefaultHours
	if h := c.Input().Get("hours"); h != "" {
		if n := util.ParseInt(h); n > 0 {
			hours = n
		}
	}
	if hours > routerStatsMaxHours {
		hours = routerStatsMaxHours
	}
	windowStart = now.Add(-time.Duration(hours) * time.Hour)
	return windowStart, now, windowStart.Format(time.RFC3339)
}

// GetRouterStats returns the router observability aggregate. Two scopes, one route:
//
//   - ?scope=platform — PUBLIC-safe aggregate over ALL orgs, no authentication.
//     Emits rates, shares, per-task/per-model counts, throughput, and the cost
//     RATIO (saved_pct) + counterfactual model id, but NEVER absolute $ levels,
//     org identity, raw events, or feature vectors. This is what world.hanzo.ai
//     polls.
//   - default (org scope) — requires a signed-in principal; scoped to the caller's
//     OWN org (a super admin may pass ?org= to target another org or "" for all).
//     Carries the absolute $/MTok indices for the admin savings panel.
//
// Window: ?since= (RFC3339) or ?hours= (default 24, capped). Aggregates only.
//
// @Title GetRouterStats
// @Tag Router API
// @Description router savings-vs-performance aggregate (scope=platform is public-safe; default is org-scoped)
// @Param scope query string false "'platform' for the public aggregate; omit for the caller's org"
// @Param org query string false "super-admin only: target org (” = all orgs)"
// @Param hours query int false "window size in hours (default 24)"
// @Param since query string false "window start (RFC3339); overrides hours"
// @Success 200 {object} controllers.routerStats The Response object
// @router /router/stats [get]
func (c *ApiController) GetRouterStats() {
	windowStart, now, sinceStr := c.resolveStatsWindow()

	if c.Input().Get("scope") == scopePlatform {
		events, err := getRoutingEvents("", sinceStr)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}
		stats := computeRouterStats(events, blendedPriceForOrg(""), windowStart, now, scopePlatform, "", false)
		// The shared base heads are scope "*"; the world widget shows the base's
		// last retrain + gate verdict (version/time/metric only — never weights).
		attachRetrainMeta(&stats, object.GlobalDefaultOwner)
		c.ResponseOk(stats)
		return
	}

	user, ok := c.RequirePrincipal()
	if !ok {
		return
	}

	// Scope: a super admin may target any org (or all with ?org=""); everyone else
	// is pinned to their OWN org — the ?org= param is ignored for a non-super-admin,
	// so a tenant can never read another org's stats.
	org := user.Owner
	if util.IsSuperAdmin(user) {
		if v := c.Input().Get("org"); v != "" || c.Input().Get("org_all") == "1" {
			org = v
		}
	}

	events, err := getRoutingEvents(org, sinceStr)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	stats := computeRouterStats(events, blendedPriceForOrg(org), windowStart, now, scopeOrg, org, true)
	// Show the org's own heads retrain status, falling back to the shared base
	// ("*") when the org has no personal-heads job (the common case today).
	metaOwner := org
	if org == "" {
		metaOwner = object.GlobalDefaultOwner
	}
	attachRetrainMeta(&stats, metaOwner)
	if stats.Retrain == nil && metaOwner != object.GlobalDefaultOwner {
		attachRetrainMeta(&stats, object.GlobalDefaultOwner)
	}
	c.ResponseOk(stats)
}

// PublishRouterArtifactMeta records the outcome of a retrain run for a scope — the
// nightly job POSTs this after fitting + gating (whether it published the new
// artifact or kept the incumbent). Super-admin only: it is a platform-control
// write, exactly like the ledger export. Owner defaults to "*" (the shared base
// heads) when the body omits it.
//
// @Title PublishRouterArtifactMeta
// @Tag Router API
// @Description record a retrain run's published-state + gate verdict for a scope (super admin only)
// @Param body body object.RouterArtifactMeta true "the retrain outcome"
// @Success 200 {object} object.RouterArtifactMeta The stored row
// @router /router/publish-artifact-meta [post]
func (c *ApiController) PublishRouterArtifactMeta() {
	if !c.RequireSuperAdmin() {
		return
	}
	var meta object.RouterArtifactMeta
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, &meta); err != nil {
		c.ResponseError(err.Error())
		return
	}
	if meta.Owner == "" {
		meta.Owner = object.GlobalDefaultOwner
	}
	if err := object.UpsertRouterArtifactMeta(&meta); err != nil {
		c.ResponseError(err.Error())
		return
	}
	// Also append to the IMMUTABLE retrain timeline (best-effort). The upsert above is
	// the source of truth for "latest per scope"; this append is the history the
	// world.hanzo.ai Model-Improvement panel plots. Same shared projection the in-process
	// trainer uses, so the two paths can't drift. A log failure must never fail the publish.
	_ = object.AppendRouterTrainingLog(object.NewRouterTrainingLog(&meta))
	c.ResponseOk(meta)
}

// trainingContributionBody is the wire shape for the opt-in read + write.
type trainingContributionBody struct {
	// Enabled mirrors the checkbox: true = opt IN to the cross-org base refresh,
	// false = opt OUT (the privacy-safe default). The stored three-state
	// ("enabled"/"disabled"/unset) collapses to this boolean for the UI (unset
	// reads as false).
	Enabled bool `json:"enabled"`
}

// GetTrainingContribution returns the caller's OWN org training-contribution opt-in
// (org > unset). Org admin-gated, self-scoped via GetOrg — a customer reads only
// their own org's setting.
//
// @Title GetTrainingContribution
// @Tag Router API
// @Description get the caller's org training-data contribution opt-in
// @Success 200 {object} controllers.trainingContributionBody The Response object
// @router /get-training-contribution [get]
func (c *ApiController) GetTrainingContribution() {
	if !c.RequireAdmin() {
		return
	}
	org := c.GetOrg()
	c.ResponseOk(trainingContributionBody{
		Enabled: object.GetCachedOrgTrainingContribution(org) == object.TrainingContributionEnabled,
	})
}

// UpdateTrainingContribution upserts the caller's OWN org training-contribution
// opt-in. The owner is forced to GetOrg() (any owner in the body is ignored), and
// the other OrgSettings fields (AutoRouting / DefaultSessionRouting / RouterPrefer)
// are preserved — the opt-in is an orthogonal concern. Org admin-gated.
//
// @Title UpdateTrainingContribution
// @Tag Router API
// @Description opt the caller's org in/out of contributing routing events to the shared retrain
// @Param body body controllers.trainingContributionBody true "the opt-in state"
// @Success 200 {object} controllers.trainingContributionBody The resolved state
// @router /update-training-contribution [post]
func (c *ApiController) UpdateTrainingContribution() {
	if !c.RequireAdmin() {
		return
	}
	org := c.GetOrg()
	if org == "" {
		c.ResponseError(c.T("auth:Please sign in first"))
		return
	}

	var body trainingContributionBody
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, &body); err != nil {
		c.ResponseError(err.Error())
		return
	}
	value := object.TrainingContributionDisabled
	if body.Enabled {
		value = object.TrainingContributionEnabled
	}

	existing, err := object.GetOrgSettings(org)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	if existing == nil {
		row := object.OrgSettings{Owner: org, TrainingContribution: value}
		if _, err := object.AddOrgSettings(&row); err != nil {
			c.ResponseError(err.Error())
			return
		}
	} else {
		existing.TrainingContribution = value
		if _, err := object.UpdateOrgSettings(org, existing); err != nil {
			c.ResponseError(err.Error())
			return
		}
	}
	c.ResponseOk(trainingContributionBody{Enabled: body.Enabled})
}

// sortedModelShare is a small helper for tests/consumers that need a deterministic
// ordering of a by-model map (descending count, ties by id). Exposed here so the
// aggregate's ordering contract lives with the aggregate.
func sortedModelShare(byModel map[string]int) []string {
	ids := make([]string, 0, len(byModel))
	for id := range byModel {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if byModel[ids[i]] != byModel[ids[j]] {
			return byModel[ids[i]] > byModel[ids[j]]
		}
		return ids[i] < ids[j]
	})
	return ids
}
