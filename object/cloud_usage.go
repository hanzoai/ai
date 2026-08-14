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

	"github.com/hanzoai/types"
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
		project String,
		model String,
		provider String,
		origin String,
		agent String,
		api_key_hash String,
		session_id String,
		trace_id String,
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
		account String,
		cost_nano Int64,
		billed_nano Int64,
		margin_nano Int64,
		unpriced UInt8
	) ENGINE = ReplacingMergeTree()
	ORDER BY (timestamp, organization, user_id, id)
	TTL timestamp + INTERVAL 2 YEAR`

// cloudUsageReplacingMergeTreeMigration documents the ONE-TIME, operator-run
// migration that converts a pre-existing hanzo.cloud_usage (created as plain
// MergeTree, ORDER BY (timestamp, organization, user_id)) to the id-deduplicating
// ReplacingMergeTree above (same key + id appended). CREATE TABLE IF NOT EXISTS
// does NOT alter an existing table's engine or sort key, and ClickHouse cannot
// ALTER either in place, so this is applied by hand (devnet first) and NEVER
// auto-run on boot (the backfill is heavy and would race across pods). Write-safe
// sequence (no lost writes):
//
//	CREATE TABLE hanzo.cloud_usage_ridedup (<identical columns>)
//	  ENGINE = ReplacingMergeTree()
//	  ORDER BY (timestamp, organization, user_id, id)
//	  TTL timestamp + INTERVAL 2 YEAR;
//	-- swap the empty ReplacingMergeTree in FIRST so live writes land in it:
//	EXCHANGE TABLES hanzo.cloud_usage AND hanzo.cloud_usage_ridedup;
//	-- backfill old rows; existing ids are unique so nothing real collapses, and
//	-- any id already re-written post-EXCHANGE dedups harmlessly:
//	INSERT INTO hanzo.cloud_usage SELECT * FROM hanzo.cloud_usage_ridedup;
//	DROP TABLE hanzo.cloud_usage_ridedup;
//
// GetCloudUsageOverview reads dedup by id at query time (GROUP BY id), so a
// duplicate is never double-counted in the window between EXCHANGE and the first
// background merge — independently of this engine change.

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
	// Nano-USD margin ledger (money-of-record): cost_nano = provider COGS,
	// billed_nano = org debit, margin_nano = billed_nano − cost_nano. cost_cents
	// stays the derived spend column the console reads.
	`ALTER TABLE hanzo.cloud_usage ADD COLUMN IF NOT EXISTS cost_nano Int64`,
	`ALTER TABLE hanzo.cloud_usage ADD COLUMN IF NOT EXISTS billed_nano Int64`,
	`ALTER TABLE hanzo.cloud_usage ADD COLUMN IF NOT EXISTS margin_nano Int64`,
	// unpriced = 1 when the model had no configured price and billed at the default,
	// so the honest "priced?" flag is queryable in the warehouse, not just the span.
	`ALTER TABLE hanzo.cloud_usage ADD COLUMN IF NOT EXISTS unpriced UInt8`,
	// project is the caller's org SUB-SCOPE (X-Project-Id); it lets the per-org
	// metrics board narrow WITHIN an org by project. Additive: a pre-existing row
	// carries '' (the org's default project == whole-org view).
	`ALTER TABLE hanzo.cloud_usage ADD COLUMN IF NOT EXISTS project String`,
	// origin is the HOST that answered (object.Provider.Origin), beside provider,
	// which is our route label for the same call. A label cannot disagree with
	// itself however far the thing it names drifts; two columns can, and that
	// disagreement is the measurement.
	`ALTER TABLE hanzo.cloud_usage ADD COLUMN IF NOT EXISTS origin String`,
	// agent is the machine credential that placed the call when no person did.
	// Rows with an agent and an empty user_id are spend nobody owns — the query
	// that finds them is the point of the column.
	`ALTER TABLE hanzo.cloud_usage ADD COLUMN IF NOT EXISTS agent String`,
	// api_key_hash is the SHA-256 ref of the caller credential the gen_ai span
	// already carries — never the key. On the ledger it answers "what did this
	// key spend", which is what a revocation decision needs and could not ask.
	`ALTER TABLE hanzo.cloud_usage ADD COLUMN IF NOT EXISTS api_key_hash String`,
	// session_id is the conversation the call belongs to. The gen_ai span has
	// carried it all along, but a span plane and a spend ledger answer different
	// questions: "what happened" versus "what did it cost". Asking which
	// conversation spent the money meant joining two stores on a column only one
	// of them had. Here it is a GROUP BY.
	`ALTER TABLE hanzo.cloud_usage ADD COLUMN IF NOT EXISTS session_id String`,
	// trace_id is the gen_ai span's own id, so a row and the span describing the
	// same call join on an id both observed rather than on two ids that merely
	// describe the same request.
	`ALTER TABLE hanzo.cloud_usage ADD COLUMN IF NOT EXISTS trace_id String`,
}

// CloudUsageColumns is the write order for a usage row, and the ONLY place it is
// written down. CloudUsageInsert is derived from it, so the column list and the
// placeholder count cannot disagree — the shift-by-one that silently lands every
// value after a newly inserted column in its neighbour's field is not expressible.
//
// It lives beside the DDL because a schema and the statement that fills it are one
// fact. Split across two packages, they drifted: four columns were declared here,
// documented here, populated on the record, and written by nothing.
var CloudUsageColumns = []string{
	"id", "timestamp", "owner", "user_id", "organization", "project",
	"model", "provider", "origin", "agent", "api_key_hash", "session_id", "trace_id",
	"request_id",
	"prompt_tokens", "completion_tokens", "total_tokens",
	"cache_read_tokens", "cache_write_tokens",
	"cost_cents", "currency", "status", "error_msg",
	"is_premium", "is_stream", "client_ip",
	"byo", "fee_cents", "account",
	"cost_nano", "billed_nano", "margin_nano", "unpriced",
}

// CloudUsageInsert is the usage-row INSERT, derived from CloudUsageColumns.
var CloudUsageInsert = "INSERT INTO hanzo.cloud_usage (" +
	strings.Join(CloudUsageColumns, ", ") + ") VALUES (" +
	strings.TrimSuffix(strings.Repeat("?, ", len(CloudUsageColumns)), ", ") + ")"

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
// Interval come from types.ParseWindow. The only field that reaches SQL
// un-parameterized is Interval, whose type admits only "hour" or "day"; Org is
// always passed as a bound parameter.
type CloudUsageParams struct {
	RangeLabel string // echoed back ("24h"|"7d"|"30d"|"custom")
	Start      time.Time
	End        time.Time
	Interval   types.Interval // time-series bucket width
	Org        string         // organization slug; ignored when AllOrgs is true
	AllOrgs    bool           // super-admin all-orgs view (no organization filter)
	// Admin selects which of the two lenses the ONE ledger row is read through.
	// It is not a second permission check: the controller sets it from the SAME
	// evaluation of the SAME predicate (util.IsSuperAdmin + own brand) that already
	// decided Org/AllOrgs, so a reader's scope and a reader's columns can never
	// disagree about who is asking.
	//
	// false — the CUSTOMER lens, and the zero value on purpose. `provider` is the
	// SKU it has always been ("hanzo", "enso"); `origin` is neither selected nor
	// assigned. A caller that asks for nothing gets the customer shape, so the
	// upstream cannot be disclosed by forgetting to hide it — only by proving the
	// predicate.
	//
	// true — the ADMIN lens: the same row additionally carries the host that
	// actually answered.
	Admin          bool
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

	// Upstream is the host that ANSWERED this call — the hostname of the serving
	// provider's URL, observed at the moment of the call rather than copied from a
	// route's configured name. Present only under the admin lens; omitempty, so a
	// customer read does not carry the key at all.
	//
	// Provider and Upstream are two different facts about one call: what we sold
	// and where it was served. Keeping them in one row is what lets them be asked
	// whether they still agree — a reroute or a fallback moves the second while the
	// first keeps reading correct. Two stores would answer that question twice.
	Upstream string `json:"upstream,omitempty"`
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

// ── Live orchestration ────────────────────────────────────────────────────────

// cloudUsageDedupedSource wraps the ledger in an id-deduplication subquery so a
// duplicate row (a ZAP frame replay or an app retry) is counted exactly once at
// query time, without waiting for the ReplacingMergeTree background merge. any()
// is exact because rows sharing an id are byte-identical (id = the per-completion
// request UUID).
//
// The predicate lives in its OWN inner scan, one level below the aggregation.
// It cannot share a SELECT with the any() aliases: each alias deliberately
// reuses its column's name, and ClickHouse resolves a WHERE identifier against
// the same query's SELECT aliases — so `WHERE timestamp >= ?` beside
// `any(timestamp) AS timestamp` reads the AGGREGATE and refuses with
// ILLEGAL_AGGREGATION (code 184). The scan is still a plain filter over the
// base table, so the (timestamp, organization) primary index prunes it as
// before.
func cloudUsageDedupedSource(where string) string {
	return "(SELECT id, any(timestamp) AS timestamp, any(owner) AS owner, " +
		"any(user_id) AS user_id, any(organization) AS organization, any(model) AS model, " +
		"any(provider) AS provider, any(request_id) AS request_id, " +
		"any(prompt_tokens) AS prompt_tokens, any(completion_tokens) AS completion_tokens, " +
		"any(total_tokens) AS total_tokens, any(cost_cents) AS cost_cents, " +
		"any(status) AS status, any(is_stream) AS is_stream, any(is_premium) AS is_premium " +
		"FROM (SELECT * FROM hanzo.cloud_usage WHERE " + where + ") GROUP BY id)"
}

// cloudUsageTotalsSQL is the id-deduplicated totals aggregation over one window.
func cloudUsageTotalsSQL(where string) string {
	return "SELECT count() AS requests, sum(total_tokens) AS tokens, " +
		"sum(prompt_tokens) AS prompt_tokens, sum(completion_tokens) AS completion_tokens, " +
		"sum(cost_cents) AS cost_cents, uniqExact(model) AS models, uniqExact(provider) AS providers " +
		"FROM " + cloudUsageDedupedSource(where)
}

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

	totalsRow, err := cloudUsageQueryOne(ctx, cloudUsageTotalsSQL(where), args)
	if err != nil {
		return nil, fmt.Errorf("usage totals: %w", err)
	}
	priorRow, err := cloudUsageQueryOne(ctx, cloudUsageTotalsSQL(priorWhere), priorArgs)
	if err != nil {
		return nil, fmt.Errorf("usage prior totals: %w", err)
	}

	// Interval admits only Hour or Day, so the bucket function this interpolates
	// cannot be widened by a request.
	bucketFn := "Hour"
	if p.Interval == types.Day {
		bucketFn = "Day"
	}
	seriesSQL := fmt.Sprintf("SELECT toStartOf%s(timestamp, 'UTC') AS bucket, sum(total_tokens) AS tokens, "+
		"sum(cost_cents) AS cost_cents, count() AS requests, uniqExact(model) AS models "+
		"FROM %s GROUP BY bucket ORDER BY bucket", bucketFn, cloudUsageDedupedSource(where))
	seriesRows, err := DatastoreQuery(ctx, seriesSQL, args...)
	if err != nil {
		return nil, fmt.Errorf("usage series: %w", err)
	}

	modelSQL := "SELECT model, any(provider) AS provider, sum(cost_cents) AS cost_cents, " +
		"sum(total_tokens) AS tokens, count() AS requests FROM " + cloudUsageDedupedSource(where) +
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
			"completion_tokens, cost_cents, is_stream, is_premium, request_id, user_id, organization%s "+
			"FROM %s ORDER BY timestamp DESC LIMIT %d OFFSET %d",
			cloudUsageUpstreamColumn(p), cloudUsageDedupedSource(where), limit, offset)
		if activityRows, err = DatastoreQuery(ctx, activitySQL, args...); err != nil {
			return nil, fmt.Errorf("usage activity: %w", err)
		}
		countRow, err := cloudUsageQueryOne(ctx, "SELECT count(DISTINCT id) AS requests FROM hanzo.cloud_usage WHERE "+where, args)
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
		Interval: string(p.Interval),
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
func buildCloudUsageSeries(start, end time.Time, interval types.Interval, rows []map[string]interface{}) []CloudUsageSeriesPoint {
	step := interval.Step()

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

// cloudUsageUpstreamColumn is the FIRST of the two places the lens applies: under
// the customer lens `origin` is never selected, so the host is not in the process
// that serves the response and cannot be disclosed by any later mistake in
// shaping it. Returns a leading ", origin" for the admin lens, "" otherwise.
//
// It is a constant fragment chosen by a bool — no caller input reaches the SQL.
func cloudUsageUpstreamColumn(p CloudUsageParams) string {
	if !p.Admin {
		return ""
	}
	return ", origin"
}

// cloudUsageUpstream is the SECOND place the lens applies, and the ONLY writer of
// CloudUsageActivityRow.Upstream. Assignment goes through this function so the
// question "may this reader see the host?" has exactly one answer in exactly one
// place, and the customer lens returns "" even if the column were somehow present.
//
// Two layers rather than one because they fail differently: the first keeps the
// value out of memory, the second keeps it out of the payload. Either alone is
// correct; together, being wrong takes two mistakes instead of one.
func cloudUsageUpstream(p CloudUsageParams, r map[string]interface{}) string {
	if !p.Admin {
		return ""
	}
	return cuString(r["origin"])
}

func buildCloudUsageActivity(p CloudUsageParams, rows []map[string]interface{}, total int64) CloudUsageActivity {
	items := make([]CloudUsageActivityRow, 0, len(rows))
	for _, r := range rows {
		items = append(items, CloudUsageActivityRow{
			Upstream:         cloudUsageUpstream(p, r),
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
