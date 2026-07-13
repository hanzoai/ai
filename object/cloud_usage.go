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

// Read side of the hanzo.cloud_usage ledger.
//
// The write side (controllers/zap_native.go zapWriteUsage) appends one row per
// inference call to the datastore table hanzo.cloud_usage via the direct datastore
// client (object/datastore.go). This file is the symmetric READ side: it aggregates that
// ledger into the console2 "Overview" dashboard payload — totals + period
// deltas, an evenly-spaced time series, spend-by-model (top-N + "other"), and
// the recent-activity feed — for BOTH the tenant-scoped console surface and the
// all-orgs admin god-view. One query path, scoped by organization.
//
// Design: every shaping rule (window resolution, gap-filled series, top-N fold,
// delta math, value coercion) is a pure function. GetCloudUsageOverview is a
// thin "fetch six aggregates, then assemble" orchestration; buildCloudUsageOverview
// is the pure assembler the test drives with mock rows — no datastore needed.

package object

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// cloudUsageTableDDL is the ONE definition of the hanzo.cloud_usage schema.
// Both the write path (EnsureCloudUsageTable, called by zapWriteUsage) and the
// read path use it — there is no second copy.
const cloudUsageTableDDL = `
	CREATE TABLE IF NOT EXISTS hanzo.cloud_usage (
		id String,
		timestamp DateTime,
		owner String,
		user_id String,
		organization String,
		model String,
		provider String,
		request_id String,
		prompt_tokens UInt32,
		completion_tokens UInt32,
		total_tokens UInt32,
		cache_read_tokens UInt32,
		cache_write_tokens UInt32,
		cost_cents UInt64,
		currency String,
		status String,
		error_msg String,
		is_premium UInt8,
		is_stream UInt8,
		client_ip String,
		byo UInt8,
		fee_cents Int64,
		account String
	) ENGINE = MergeTree()
	ORDER BY (timestamp, organization, user_id)
	TTL timestamp + INTERVAL 2 YEAR`

// cloudUsageColumnMigrations bring an ALREADY-EXISTING table up to the current
// schema: CREATE TABLE IF NOT EXISTS is a no-op on a table created before these
// columns were added, so each additive column also needs an idempotent
// ADD COLUMN IF NOT EXISTS. Applied after the CREATE in EnsureCloudUsageTable so
// both a fresh and a legacy table converge on the same shape. Keep in lockstep
// with the DDL above and the zapWriteUsage INSERT.
var cloudUsageColumnMigrations = []string{
	`ALTER TABLE hanzo.cloud_usage ADD COLUMN IF NOT EXISTS byo UInt8`,
	`ALTER TABLE hanzo.cloud_usage ADD COLUMN IF NOT EXISTS fee_cents Int64`,
	`ALTER TABLE hanzo.cloud_usage ADD COLUMN IF NOT EXISTS account String`,
}

var cloudUsageTableReady atomic.Bool

// EnsureCloudUsageTable creates hanzo.cloud_usage if it does not exist, then
// applies the additive column migrations so a pre-existing table gains the
// byo/fee_cents/account columns. It is idempotent and only latches success, so a
// transient datastore outage at boot does not permanently poison later attempts.
func EnsureCloudUsageTable(ctx context.Context) error {
	if cloudUsageTableReady.Load() {
		return nil
	}
	if err := DatastoreExec(ctx, cloudUsageTableDDL); err != nil {
		return err
	}
	for _, stmt := range cloudUsageColumnMigrations {
		if err := DatastoreExec(ctx, stmt); err != nil {
			return err
		}
	}
	cloudUsageTableReady.Store(true)
	return nil
}

// ── Request ─────────────────────────────────────────────────────────────────

// CloudUsageParams is the resolved, already-authorized query for one Overview.
// The controller fills Org/AllOrgs from the session (a tenant is pinned to its
// own org; a super admin may target one org or omit for all orgs). Start/End/
// Interval come from ResolveCloudUsageWindow. The only field that reaches SQL
// un-parameterized is Interval, a closed server-chosen enum ("hour"|"day");
// Org is always passed as a bound parameter.
type CloudUsageParams struct {
	RangeLabel     string // echoed back ("24h"|"7d"|"30d"|"custom")
	Start          time.Time
	End            time.Time
	Interval       string // "hour" | "day" — time-series bucket width
	Org            string // organization slug; ignored when AllOrgs is true
	AllOrgs        bool   // super-admin all-orgs view (no organization filter)
	TopModels      int    // spend-by-model: keep top N, fold the rest into "other"
	ActivityType   string // "all" | "inference" (others → honest-empty feed)
	ActivityLimit  int
	ActivityOffset int
}

// ── Response ────────────────────────────────────────────────────────────────

type CloudUsageTotals struct {
	Tokens           int64 `json:"tokens"`
	PromptTokens     int64 `json:"promptTokens"`
	CompletionTokens int64 `json:"completionTokens"`
	Requests         int64 `json:"requests"`
	SpendCents       int64 `json:"spendCents"`
	Models           int64 `json:"models"`
	Providers        int64 `json:"providers"`
}

// CloudUsageDelta is one card's "vs prior period" comparison. Pct is nil when
// the prior period had no basis (prior == 0), so the client shows "—"/"new"
// instead of a fabricated ratio.
type CloudUsageDelta struct {
	Current int64    `json:"current"`
	Prior   int64    `json:"prior"`
	Pct     *float64 `json:"pct"`
}

type CloudUsageSeriesPoint struct {
	T          string `json:"t"` // RFC3339 bucket start (UTC)
	Tokens     int64  `json:"tokens"`
	SpendCents int64  `json:"spendCents"`
	Requests   int64  `json:"requests"`
	Models     int64  `json:"models"`
}

type CloudUsageModelSpend struct {
	Model      string  `json:"model"`
	Provider   string  `json:"provider"`
	SpendCents int64   `json:"spendCents"`
	Tokens     int64   `json:"tokens"`
	Requests   int64   `json:"requests"`
	Pct        float64 `json:"pct"` // share of total spend, 0..100
}

type CloudUsageModelOther struct {
	SpendCents int64   `json:"spendCents"`
	Tokens     int64   `json:"tokens"`
	Requests   int64   `json:"requests"`
	Pct        float64 `json:"pct"`
	ModelCount int     `json:"modelCount"`
}

type CloudUsageByModel struct {
	Items      []CloudUsageModelSpend `json:"items"`
	Other      *CloudUsageModelOther  `json:"other"`
	TotalCents int64                  `json:"totalCents"`
}

type CloudUsageActivityRow struct {
	Time             string `json:"time"` // RFC3339 (UTC)
	Model            string `json:"model"`
	Provider         string `json:"provider"`
	Type             string `json:"type"` // "inference" — the only event class in this ledger
	Status           string `json:"status"`
	Tokens           int64  `json:"tokens"`
	PromptTokens     int64  `json:"promptTokens"`
	CompletionTokens int64  `json:"completionTokens"`
	CostCents        int64  `json:"costCents"`
	Stream           bool   `json:"stream"`
	Premium          bool   `json:"premium"`
	RequestID        string `json:"requestId"`
	Org              string `json:"org"`
	User             string `json:"user"`
}

type CloudUsageActivity struct {
	Items  []CloudUsageActivityRow `json:"items"`
	Limit  int                     `json:"limit"`
	Offset int                     `json:"offset"`
	Total  int64                   `json:"total"`
	Type   string                  `json:"type"`
}

type CloudUsageScope struct {
	Org     string `json:"org"` // "" when allOrgs
	AllOrgs bool   `json:"allOrgs"`
}

type CloudUsageOverview struct {
	Range    string                     `json:"range"`
	Start    string                     `json:"start"` // RFC3339 (UTC)
	End      string                     `json:"end"`   // RFC3339 (UTC)
	Interval string                     `json:"interval"`
	Scope    CloudUsageScope            `json:"scope"`
	Totals   CloudUsageTotals           `json:"totals"`
	Deltas   map[string]CloudUsageDelta `json:"deltas"` // keys: tokens, spendCents, requests, models
	Series   []CloudUsageSeriesPoint    `json:"series"`
	ByModel  CloudUsageByModel          `json:"byModel"`
	Activity CloudUsageActivity         `json:"activity"`
}

// ── Window resolution (pure) ──────────────────────────────────────────────────

// ResolveCloudUsageWindow maps a range label (plus optional custom start/end)
// to an absolute [start, end) window and a bucket interval. Pure: `now` is
// injected so the test is deterministic.
func ResolveCloudUsageWindow(rangeLabel, startStr, endStr string, now time.Time) (start, end time.Time, interval string, err error) {
	now = now.UTC()
	switch strings.ToLower(strings.TrimSpace(rangeLabel)) {
	case "", "24h", "1d", "day", "today":
		return now.Add(-24 * time.Hour), now, "hour", nil
	case "7d", "week":
		return now.Add(-7 * 24 * time.Hour), now, "day", nil
	case "30d", "month":
		return now.Add(-30 * 24 * time.Hour), now, "day", nil
	case "custom":
		start, err = parseCloudUsageTimeParam(startStr)
		if err != nil {
			return time.Time{}, time.Time{}, "", fmt.Errorf("custom range requires a valid start: %w", err)
		}
		if strings.TrimSpace(endStr) == "" {
			end = now
		} else if end, err = parseCloudUsageTimeParam(endStr); err != nil {
			return time.Time{}, time.Time{}, "", fmt.Errorf("custom range has an invalid end: %w", err)
		}
		if !end.After(start) {
			return time.Time{}, time.Time{}, "", fmt.Errorf("custom range end must be after start")
		}
		interval = "hour"
		if end.Sub(start) > 48*time.Hour {
			interval = "day"
		}
		return start.UTC(), end.UTC(), interval, nil
	default:
		return time.Time{}, time.Time{}, "", fmt.Errorf("unknown range %q", rangeLabel)
	}
}

// parseCloudUsageTimeParam accepts RFC3339 or a unix-seconds string.
func parseCloudUsageTimeParam(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty time")
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	if secs, err := strconv.ParseInt(s, 10, 64); err == nil && secs > 0 {
		return time.Unix(secs, 0).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("not RFC3339 or unix seconds: %q", s)
}

// ── Live orchestration ────────────────────────────────────────────────────────

const cloudUsageTotalsSelect = "SELECT count() AS requests, sum(total_tokens) AS tokens, " +
	"sum(prompt_tokens) AS prompt_tokens, sum(completion_tokens) AS completion_tokens, " +
	"sum(cost_cents) AS cost_cents, uniqExact(model) AS models, uniqExact(provider) AS providers " +
	"FROM hanzo.cloud_usage WHERE "

// GetCloudUsageOverview runs the aggregate queries against the datastore ledger
// and assembles the Overview. Errors are surfaced (not swallowed) so the client
// can show an honest "unavailable" state rather than fabricated zeros.
func GetCloudUsageOverview(ctx context.Context, p CloudUsageParams) (*CloudUsageOverview, error) {
	if !DatastoreEnabled() {
		return nil, fmt.Errorf("usage ledger unavailable: datastore peer not connected")
	}
	if err := EnsureCloudUsageTable(ctx); err != nil {
		return nil, fmt.Errorf("usage ledger: %w", err)
	}

	where, args := p.whereClause(p.Start, p.End)
	span := p.End.Sub(p.Start)
	priorWhere, priorArgs := p.whereClause(p.Start.Add(-span), p.Start)

	totalsRow, err := cloudUsageQueryOne(ctx, cloudUsageTotalsSelect+where, args)
	if err != nil {
		return nil, fmt.Errorf("usage totals: %w", err)
	}
	priorRow, err := cloudUsageQueryOne(ctx, cloudUsageTotalsSelect+priorWhere, priorArgs)
	if err != nil {
		return nil, fmt.Errorf("usage prior totals: %w", err)
	}

	bucketFn := "Hour"
	if p.Interval == "day" {
		bucketFn = "Day"
	}
	seriesSQL := fmt.Sprintf("SELECT toStartOf%s(timestamp, 'UTC') AS bucket, sum(total_tokens) AS tokens, "+
		"sum(cost_cents) AS cost_cents, count() AS requests, uniqExact(model) AS models "+
		"FROM hanzo.cloud_usage WHERE %s GROUP BY bucket ORDER BY bucket", bucketFn, where)
	seriesRows, err := DatastoreQuery(ctx, seriesSQL, args...)
	if err != nil {
		return nil, fmt.Errorf("usage series: %w", err)
	}

	modelSQL := "SELECT model, any(provider) AS provider, sum(cost_cents) AS cost_cents, " +
		"sum(total_tokens) AS tokens, count() AS requests FROM hanzo.cloud_usage WHERE " + where +
		" GROUP BY model ORDER BY cost_cents DESC LIMIT 100"
	modelRows, err := DatastoreQuery(ctx, modelSQL, args...)
	if err != nil {
		return nil, fmt.Errorf("usage by-model: %w", err)
	}

	var activityRows []map[string]interface{}
	var activityTotal int64
	if cloudUsageActivityQueryable(p.ActivityType) {
		limit := p.ActivityLimit
		if limit <= 0 {
			limit = 20
		}
		if limit > 200 {
			limit = 200
		}
		offset := p.ActivityOffset
		if offset < 0 {
			offset = 0
		}
		activitySQL := fmt.Sprintf("SELECT timestamp, model, provider, status, total_tokens, prompt_tokens, "+
			"completion_tokens, cost_cents, is_stream, is_premium, request_id, user_id, organization "+
			"FROM hanzo.cloud_usage WHERE %s ORDER BY timestamp DESC LIMIT %d OFFSET %d", where, limit, offset)
		if activityRows, err = DatastoreQuery(ctx, activitySQL, args...); err != nil {
			return nil, fmt.Errorf("usage activity: %w", err)
		}
		countRow, err := cloudUsageQueryOne(ctx, "SELECT count() AS requests FROM hanzo.cloud_usage WHERE "+where, args)
		if err != nil {
			return nil, fmt.Errorf("usage activity count: %w", err)
		}
		activityTotal = cuInt64(countRow["requests"])
	}

	return buildCloudUsageOverview(p, totalsRow, priorRow, seriesRows, modelRows, activityRows, activityTotal), nil
}

// whereClause builds the time + organization predicate. Times are formatted as
// datastore DateTime literals (UTC); the org slug is always a bound parameter
// (never interpolated) so a super admin's ?org= can't inject SQL.
func (p CloudUsageParams) whereClause(start, end time.Time) (string, []interface{}) {
	clause := "timestamp >= ? AND timestamp < ?"
	args := []interface{}{cloudUsageTS(start), cloudUsageTS(end)}
	if !p.AllOrgs {
		clause += " AND organization = ?"
		args = append(args, p.Org)
	}
	return clause, args
}

func cloudUsageTS(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05") }

func cloudUsageQueryOne(ctx context.Context, sql string, args []interface{}) (map[string]interface{}, error) {
	rows, err := DatastoreQuery(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return map[string]interface{}{}, nil
	}
	return rows[0], nil
}

// cloudUsageActivityQueryable reports whether an activity-tab filter maps to rows
// in THIS ledger. The cloud_usage table holds only inference events, so the
// deployments/jobs/payments tabs are honestly empty here (they are sourced from
// other surfaces) rather than faked.
func cloudUsageActivityQueryable(t string) bool {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "", "all", "inference":
		return true
	default:
		return false
	}
}

// ── Pure assembler (unit-tested) ──────────────────────────────────────────────

// buildCloudUsageOverview assembles the Overview from already-fetched raw rows.
// It is pure (no I/O), so the test drives it with mock datastore rows.
func buildCloudUsageOverview(
	p CloudUsageParams,
	totalsRow, priorRow map[string]interface{},
	seriesRows, modelRows, activityRows []map[string]interface{},
	activityTotal int64,
) *CloudUsageOverview {
	totals := cloudUsageTotalsFromRow(totalsRow)
	prior := cloudUsageTotalsFromRow(priorRow)

	scope := CloudUsageScope{Org: p.Org, AllOrgs: p.AllOrgs}
	if p.AllOrgs {
		scope.Org = ""
	}

	items, other := foldCloudUsageModels(modelRows, p.TopModels, totals.SpendCents)

	return &CloudUsageOverview{
		Range:    p.RangeLabel,
		Start:    p.Start.UTC().Format(time.RFC3339),
		End:      p.End.UTC().Format(time.RFC3339),
		Interval: p.Interval,
		Scope:    scope,
		Totals:   totals,
		Deltas: map[string]CloudUsageDelta{
			"tokens":     cloudUsageDelta(totals.Tokens, prior.Tokens),
			"spendCents": cloudUsageDelta(totals.SpendCents, prior.SpendCents),
			"requests":   cloudUsageDelta(totals.Requests, prior.Requests),
			"models":     cloudUsageDelta(totals.Models, prior.Models),
		},
		Series:   buildCloudUsageSeries(p.Start, p.End, p.Interval, seriesRows),
		ByModel:  CloudUsageByModel{Items: items, Other: other, TotalCents: totals.SpendCents},
		Activity: buildCloudUsageActivity(p, activityRows, activityTotal),
	}
}

func cloudUsageTotalsFromRow(r map[string]interface{}) CloudUsageTotals {
	return CloudUsageTotals{
		Tokens:           cuInt64(r["tokens"]),
		PromptTokens:     cuInt64(r["prompt_tokens"]),
		CompletionTokens: cuInt64(r["completion_tokens"]),
		Requests:         cuInt64(r["requests"]),
		SpendCents:       cuInt64(r["cost_cents"]),
		Models:           cuInt64(r["models"]),
		Providers:        cuInt64(r["providers"]),
	}
}

// cloudUsageDelta computes a card's prior-period comparison. Pct is left nil when
// prior == 0 (no basis), so the client never renders a fake "+∞%".
func cloudUsageDelta(current, prior int64) CloudUsageDelta {
	d := CloudUsageDelta{Current: current, Prior: prior}
	if prior > 0 {
		p := round1((float64(current) - float64(prior)) / float64(prior) * 100)
		d.Pct = &p
	}
	return d
}

// buildCloudUsageSeries turns sparse datastore buckets into an evenly-spaced,
// gap-filled series so the client charts a continuous line. Bucket alignment
// matches toStartOf{Hour,Day}(…, 'UTC'): Go's Truncate over the chosen step
// lands on the same UTC boundaries.
func buildCloudUsageSeries(start, end time.Time, interval string, rows []map[string]interface{}) []CloudUsageSeriesPoint {
	step := cloudUsageStep(interval)

	type agg struct{ tokens, spend, requests, models int64 }
	idx := make(map[int64]agg, len(rows))
	for _, r := range rows {
		bt := cuTime(r["bucket"]).Truncate(step)
		idx[bt.Unix()] = agg{
			tokens:   cuInt64(r["tokens"]),
			spend:    cuInt64(r["cost_cents"]),
			requests: cuInt64(r["requests"]),
			models:   cuInt64(r["models"]),
		}
	}

	out := make([]CloudUsageSeriesPoint, 0, 64)
	for t := start.UTC().Truncate(step); t.Before(end); t = t.Add(step) {
		a := idx[t.Unix()]
		out = append(out, CloudUsageSeriesPoint{
			T:          t.UTC().Format(time.RFC3339),
			Tokens:     a.tokens,
			SpendCents: a.spend,
			Requests:   a.requests,
			Models:     a.models,
		})
	}
	return out
}

func cloudUsageStep(interval string) time.Duration {
	if strings.EqualFold(interval, "day") {
		return 24 * time.Hour
	}
	return time.Hour
}

// foldCloudUsageModels keeps the top-N models by spend and folds the rest into a
// single "other" bucket, computing each share of total spend. Pure.
func foldCloudUsageModels(rows []map[string]interface{}, topN int, totalCents int64) ([]CloudUsageModelSpend, *CloudUsageModelOther) {
	if topN <= 0 {
		topN = 6
	}

	type m struct {
		model, provider         string
		spend, tokens, requests int64
	}
	all := make([]m, 0, len(rows))
	for _, r := range rows {
		all = append(all, m{
			model:    cuString(r["model"]),
			provider: cuString(r["provider"]),
			spend:    cuInt64(r["cost_cents"]),
			tokens:   cuInt64(r["tokens"]),
			requests: cuInt64(r["requests"]),
		})
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].spend > all[j].spend })

	items := make([]CloudUsageModelSpend, 0, topN)
	var other CloudUsageModelOther
	for i, x := range all {
		if i < topN {
			items = append(items, CloudUsageModelSpend{
				Model:      x.model,
				Provider:   x.provider,
				SpendCents: x.spend,
				Tokens:     x.tokens,
				Requests:   x.requests,
				Pct:        pctOf(x.spend, totalCents),
			})
		} else {
			other.SpendCents += x.spend
			other.Tokens += x.tokens
			other.Requests += x.requests
			other.ModelCount++
		}
	}
	if other.ModelCount > 0 {
		other.Pct = pctOf(other.SpendCents, totalCents)
		return items, &other
	}
	return items, nil
}

func buildCloudUsageActivity(p CloudUsageParams, rows []map[string]interface{}, total int64) CloudUsageActivity {
	items := make([]CloudUsageActivityRow, 0, len(rows))
	for _, r := range rows {
		items = append(items, CloudUsageActivityRow{
			Time:             cuTime(r["timestamp"]).Format(time.RFC3339),
			Model:            cuString(r["model"]),
			Provider:         cuString(r["provider"]),
			Type:             "inference",
			Status:           cuString(r["status"]),
			Tokens:           cuInt64(r["total_tokens"]),
			PromptTokens:     cuInt64(r["prompt_tokens"]),
			CompletionTokens: cuInt64(r["completion_tokens"]),
			CostCents:        cuInt64(r["cost_cents"]),
			Stream:           cuBool(r["is_stream"]),
			Premium:          cuBool(r["is_premium"]),
			RequestID:        cuString(r["request_id"]),
			Org:              cuString(r["organization"]),
			User:             cuString(r["user_id"]),
		})
	}
	at := strings.ToLower(strings.TrimSpace(p.ActivityType))
	if at == "" {
		at = "all"
	}
	return CloudUsageActivity{Items: items, Limit: p.ActivityLimit, Offset: p.ActivityOffset, Total: total, Type: at}
}

// ── Value coercion ────────────────────────────────────────────────────────────
//
// datastore rows arrive from the direct datastore-go client as native-typed maps
// (uint64/string/time.Time/float64), but coercion stays defensive: a value may
// still surface as float64, json.Number, or (for big UInt64) a string, and DateTime
// as "2006-01-02 15:04:05". These helpers coerce robustly so a driver encoding
// change can't crash a read.

func cuInt64(v interface{}) int64 {
	switch n := v.(type) {
	case nil:
		return 0
	case int:
		return int64(n)
	case int64:
		return n
	case int32:
		return int64(n)
	case int16:
		return int64(n)
	case int8:
		return int64(n)
	case uint:
		return int64(n)
	case uint64:
		return int64(n)
	case uint32:
		return int64(n)
	case uint16:
		return int64(n)
	case uint8:
		return int64(n)
	case float64:
		return int64(n)
	case float32:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	case string:
		if i, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64); err == nil {
			return i
		}
		if f, err := strconv.ParseFloat(strings.TrimSpace(n), 64); err == nil {
			return int64(f)
		}
		return 0
	default:
		return 0
	}
}

func cuString(v interface{}) string {
	switch s := v.(type) {
	case nil:
		return ""
	case string:
		return s
	case fmt.Stringer:
		return s.String()
	default:
		return fmt.Sprintf("%v", s)
	}
}

func cuBool(v interface{}) bool {
	switch b := v.(type) {
	case nil:
		return false
	case bool:
		return b
	case string:
		return b == "1" || strings.EqualFold(b, "true")
	default:
		return cuInt64(v) != 0
	}
}

func cuTime(v interface{}) time.Time {
	// The direct datastore driver decodes DateTime columns to time.Time; take it
	// as-is before falling back to the string/unix layouts (a JSON transport path).
	if t, ok := v.(time.Time); ok {
		return t.UTC()
	}
	s := strings.TrimSpace(cuString(v))
	if s != "" {
		for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339, "2006-01-02"} {
			if t, err := time.Parse(layout, s); err == nil {
				return t.UTC()
			}
		}
	}
	if n := cuInt64(v); n > 0 {
		return time.Unix(n, 0).UTC()
	}
	return time.Time{}
}

func pctOf(part, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return round1(float64(part) / float64(total) * 100)
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }
