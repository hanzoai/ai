// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controllers

import (
	"strings"
	"time"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

// Router IMPROVEMENT surface: the flywheel getting smarter OVER TIME, for the
// investor/customer world.hanzo.ai view. Like /v1/router/stats it emits AGGREGATES
// ONLY over the RoutingEvent ledger + the append-only retrain log — never raw events,
// prompt-derived data, or per-request rows. The public platform scope additionally
// emits NO model ids (only the task mix, which is safe): tasks are what customers do,
// model ids are the private roster. Everything is honest-empty: the ledger is nearly
// empty today, so the daily series starts flat/zero and CLIMBS with real data — days
// with no events emit an explicit zero row (honest gaps), never an interpolation.

const (
	routerHistoryDefaultDays = 30
	routerHistoryMaxDays     = 90
	// routerHistoryMaxRetrains bounds the retrain markers returned (chronological).
	routerHistoryMaxRetrains = 400
)

// historyWindow is the resolved calendar-day window.
type historyWindow struct {
	Since string `json:"since"`
	Until string `json:"until"`
	Days  int    `json:"days"`
}

// historyDay is one UTC calendar day's aggregate. cost_saved_index is that day's
// proportional saved index (baseline blended $/MTok minus routed, summed over priced
// events — the same honest proxy as /v1/router/stats, never a billed dollar figure);
// cumulative_cost_saved is the running total through this day (the investor curve).
// by_task is the day's task histogram (safe on every scope).
type historyDay struct {
	Date                string         `json:"date"` // YYYY-MM-DD (UTC)
	Events              int            `json:"events"`
	RewardRate          float64        `json:"reward_rate"`
	RewardedEvents      int            `json:"rewarded_events"`
	CostSavedIndex      float64        `json:"cost_saved_index"`
	CumulativeCostSaved float64        `json:"cumulative_cost_saved"`
	LearnedShare        float64        `json:"learned_share"`
	ByTask              map[string]int `json:"by_task"`
}

// historyRetrain is one retrain marker on the timeline. holdout_accuracy is present
// ONLY when the gate metric is a holdout accuracy — we never relabel a reward/AIQ gate
// as an "accuracy". gate_value/gate_metric are always exposed honestly.
type historyRetrain struct {
	Date            string   `json:"date"`
	Version         string   `json:"version"`
	TrainedTime     string   `json:"trained_time"`
	HoldoutAccuracy *float64 `json:"holdout_accuracy,omitempty"`
	GateMetric      string   `json:"gate_metric"`
	GateValue       float64  `json:"gate_value"`
	GateBase        float64  `json:"gate_base"`
	GatePass        bool     `json:"gate_pass"`
	Published       bool     `json:"published"`
	Events          int      `json:"events"`
}

// historyTotals is the headline roll-up: total events (adoption), cumulative saved
// index (the investor number), overall reward rate, and how many days saw traffic.
type historyTotals struct {
	Events              int     `json:"events"`
	CumulativeCostSaved float64 `json:"cumulative_cost_saved"`
	RewardRate          float64 `json:"reward_rate"`
	DaysActive          int     `json:"days_active"`
}

// routerHistory is the whole time-series payload.
type routerHistory struct {
	Scope    string           `json:"scope"`
	Window   historyWindow    `json:"window"`
	Daily    []historyDay     `json:"daily"`
	Retrains []historyRetrain `json:"retrains"`
	Totals   historyTotals    `json:"totals"`
}

// historyWindowStart is the UTC midnight of the oldest day in a `days`-day window
// ending today. The handler (DB query) and computeRouterHistory (bucketing) both use
// it, so the fetched events and the plotted buckets always agree.
func historyWindowStart(now time.Time, days int) time.Time {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return today.AddDate(0, 0, -(days - 1))
}

// isHoldoutAccuracy reports whether a gate metric names a holdout ACCURACY — the only
// case where gate_value is honestly a model-accuracy we can plot as such.
func isHoldoutAccuracy(metric string) bool {
	return strings.Contains(strings.ToLower(metric), "accuracy")
}

// dayKey formats an RFC3339 timestamp as its UTC calendar day (YYYY-MM-DD); "" on a
// parse failure.
func dayKey(rfc3339 string) string {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return ""
	}
	return t.UTC().Format("2006-01-02")
}

// computeRouterHistory folds a window of routing events + retrain-log rows into the
// daily improvement series. Pure over its inputs (no DB, no router) so the whole
// contract is unit-testable. `now` bounds the window; events are assumed already
// filtered to created_time >= windowStart; retrains are oldest-first. The baseline for
// cost-saved is the WHOLE-WINDOW priciest served model (stable, so the cumulative
// curve doesn't jump when a day's mix shifts) — same proxy as computeRouterStats.
func computeRouterHistory(events []*object.RoutingEvent, retrains []*object.RouterTrainingLog, price priceIndexFn, now time.Time, days int, scope string, tuned map[string][]string) routerHistory {
	if days <= 0 {
		days = routerHistoryDefaultDays
	}
	if days > routerHistoryMaxDays {
		days = routerHistoryMaxDays
	}
	startDay := historyWindowStart(now, days)

	// Whole-window price set → baseline (the premium single model an org would default
	// to WITHOUT routing).
	prices := map[string]float64{}
	for _, e := range events {
		if e.RoutedModel == "" {
			continue
		}
		if _, seen := prices[e.RoutedModel]; !seen {
			prices[e.RoutedModel] = price(e.RoutedModel)
		}
	}
	_, baselinePrice := maxPriced(prices)

	type dayAgg struct {
		events    int
		rewardSum float64
		rewardedN int
		learnedN  int
		byTask    map[string]int
		routedSum float64
		cfSum     float64
	}
	byDay := make(map[string]*dayAgg, days)
	dayKeys := make([]string, 0, days)
	for d := 0; d < days; d++ {
		key := startDay.AddDate(0, 0, d).Format("2006-01-02")
		byDay[key] = &dayAgg{byTask: map[string]int{}}
		dayKeys = append(dayKeys, key)
	}

	for _, e := range events {
		da := byDay[dayKey(e.CreatedTime)]
		if da == nil {
			continue // outside the window (or unparseable)
		}
		da.events++
		if e.RoutedModel != "" && firstOf(tuned[e.Task]) == e.RoutedModel {
			da.learnedN++
		}
		if e.RewardedTime != "" {
			da.rewardSum += e.Reward
			da.rewardedN++
		}
		if e.Task != "" {
			da.byTask[e.Task]++
		}
		if baselinePrice > 0 {
			if p, ok := prices[e.RoutedModel]; ok && p > 0 {
				da.routedSum += p
				da.cfSum += baselinePrice
			}
		}
	}

	var cumulative float64
	var totalEvents, totalRewarded int
	var totalRewardSum float64
	daysActive := 0
	daily := make([]historyDay, 0, days)
	for _, key := range dayKeys {
		da := byDay[key]
		daySaved := round4(da.cfSum - da.routedSum)
		cumulative = round4(cumulative + (da.cfSum - da.routedSum))
		rr := 0.0
		if da.rewardedN > 0 {
			rr = round4(da.rewardSum / float64(da.rewardedN))
		}
		es := 0.0
		if da.events > 0 {
			es = round4(float64(da.learnedN) / float64(da.events))
			daysActive++
		}
		totalEvents += da.events
		totalRewarded += da.rewardedN
		totalRewardSum += da.rewardSum
		daily = append(daily, historyDay{
			Date:                key,
			Events:              da.events,
			RewardRate:          rr,
			RewardedEvents:      da.rewardedN,
			CostSavedIndex:      daySaved,
			CumulativeCostSaved: cumulative,
			LearnedShare:        es,
			ByTask:              da.byTask,
		})
	}

	rows := make([]historyRetrain, 0, len(retrains))
	for _, r := range retrains {
		hr := historyRetrain{
			Date:        dayKey(r.TrainedTime),
			Version:     r.Version,
			TrainedTime: r.TrainedTime,
			GateMetric:  r.GateMetric,
			GateValue:   round4(r.GateValue),
			GateBase:    round4(r.GateBase),
			GatePass:    r.GatePassed,
			Published:   r.Published,
			Events:      r.Events,
		}
		if isHoldoutAccuracy(r.GateMetric) {
			acc := round4(r.GateValue)
			hr.HoldoutAccuracy = &acc
		}
		rows = append(rows, hr)
	}

	totalRR := 0.0
	if totalRewarded > 0 {
		totalRR = round4(totalRewardSum / float64(totalRewarded))
	}

	until := startDay.AddDate(0, 0, days) // exclusive end = midnight after the last day
	return routerHistory{
		Scope: scope,
		Window: historyWindow{
			Since: startDay.Format(time.RFC3339),
			Until: until.Format(time.RFC3339),
			Days:  days,
		},
		Daily:    daily,
		Retrains: rows,
		Totals: historyTotals{
			Events:              totalEvents,
			CumulativeCostSaved: cumulative,
			RewardRate:          totalRR,
			DaysActive:          daysActive,
		},
	}
}

// GetRouterHistory returns the router-improvement time-series. Two scopes, one route,
// mirroring /v1/router/stats:
//
//   - ?scope=platform — PUBLIC-safe aggregate over ALL orgs, no authentication. Emits
//     the daily reward/cost-saved/adoption series (task mix included, model ids NOT)
//     and the retrain timeline. This is what world.hanzo.ai polls.
//   - default (org scope) — requires a signed-in principal, scoped to the caller's OWN
//     org (a super admin may pass ?org= to target another or "" for all).
//
// Window: ?days=N (default 30, capped at 90). Aggregates only.
//
// @Title GetRouterHistory
// @Tag Router API
// @Description router improvement time-series (reward + cost-saved + adoption over time, retrain markers); scope=platform is public-safe
// @Param scope query string false "'platform' for the public aggregate; omit for the caller's org"
// @Param days query int false "window size in days (default 30, max 90)"
// @Param org query string false "super-admin only: target org (” = all orgs)"
// @Success 200 {object} controllers.routerHistory The Response object
// @router /router/history [get]
func (c *ApiController) GetRouterHistory() {
	days := routerHistoryDefaultDays
	if v := c.Input().Get("days"); v != "" {
		if n := util.ParseInt(v); n > 0 {
			days = n
		}
	}
	if days > routerHistoryMaxDays {
		days = routerHistoryMaxDays
	}
	now := time.Now().UTC()
	since := historyWindowStart(now, days).Format(time.RFC3339)

	if c.Input().Get("scope") == scopePlatform {
		events, err := getRoutingEvents("", since)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}
		retrains, _ := object.ListRouterTrainingLog(object.GlobalDefaultOwner, since, routerHistoryMaxRetrains)
		c.ResponseOk(computeRouterHistory(events, retrains, blendedPriceForOrg(""), now, days, scopePlatform, tunedRouterPrefer("")))
		return
	}

	user, ok := c.RequirePrincipal()
	if !ok {
		return
	}
	org := user.Owner
	if util.IsSuperAdmin(user) {
		if v := c.Input().Get("org"); v != "" || c.Input().Get("org_all") == "1" {
			org = v
		}
	}
	events, err := getRoutingEvents(org, since)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	metaOwner := org
	if org == "" {
		metaOwner = object.GlobalDefaultOwner
	}
	retrains, _ := object.ListRouterTrainingLog(metaOwner, since, routerHistoryMaxRetrains)
	c.ResponseOk(computeRouterHistory(events, retrains, blendedPriceForOrg(org), now, days, scopeOrg, tunedRouterPrefer(org)))
}
