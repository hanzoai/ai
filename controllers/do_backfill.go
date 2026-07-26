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
//
// do_backfill.go — the DigitalOcean usage BACKFILL importer. DO is our primary AI
// venue (the do-ai GenAI serverless-inference gateway), and for windows where our
// OWN metering (controllers/zap_native.go zapWriteUsage) never wrote a row, the
// hanzo.cloud_usage ledger has a hole: world.hanzo.ai / admin.hanzo.ai / console
// then render an incomplete history. This importer fills those holes from DO's
// authoritative billing API and writes the missing rows into the SAME ledger the
// native writer owns — so there is still ONE usage table, one write module (ai).
//
// GAP-FILL SEMANTICS (the four hard invariants, all provable without a live DO
// account or datastore because the mapping + planning are pure functions):
//
//  1. GAPS ONLY, NEVER DOUBLE-COUNT. A candidate day is imported only when native
//     metering wrote NO do-ai row for that day (planDOBackfill skips days present in
//     nativeCoveredDays). Coverage is judged per (provider, DAY) — not per
//     (day, model) — on purpose: DO reports spend-only line items whose product
//     labels never align with our per-request model names, so a (day, model) test
//     would treat every DO row as an uncovered gap and re-add spend we already
//     metered. The primary constraint is "never double-count", so a day we metered
//     natively is considered whole. `force` overrides this for a deliberate re-import.
//
//  2. PROVENANCE. Every imported row carries source="do-backfill" (a dedicated,
//     additive cloud_usage column ensured by ensureBackfillSchema). Native rows
//     default source='' — so finance/readers can always separate backfilled history
//     from natively-metered history. The row's model is DO's OWN label for the
//     charge (never a fabricated model name), tokens are 0 (DO exposes dollars only,
//     no token counts — we never invent them), and cost_cents is the real DO spend.
//
//  3. IDEMPOTENT. Each row's key is (day, model, source); planDOBackfill skips any
//     (day, model) already present in existingBackfillKeys. Re-running imports only
//     what is genuinely new — running twice leaves the ledger in the same state.
//
//  4. DRY-RUN FIRST-CLASS. RunDOBackfill defaults to DryRun; it returns the exact
//     rows/days/totals it WOULD write and touches nothing. Persisting requires an
//     explicit dryRun=false. The super-admin endpoint is PostBackfillDOUsage.
//
// DO API + auth reuse the finance client convention (cloud/clients/admin/digitalocean):
// a single personal-access token DO_API_TOKEN (KMSSecret-injected, never hard-coded),
// Bearer-authenticated raw REST against api.digitalocean.com. GenAI inference shows up
// as invoice LINE ITEMS (GET /v2/customers/my/invoices → per-invoice
// GET /v2/customers/my/invoices/{uuid} → invoice_items[]), each an amount + start_time
// + product/description — dollars, not tokens.

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/ai/log"

	"github.com/hanzoai/ai/object"
)

const (
	// doBackfillTokenEnv is the SAME single DO personal-access token the cloud
	// finance + do subsystems read (sourced from a KMSSecret on the env, never
	// hard-coded). Absent it the importer reports an honest not-configured error.
	doBackfillTokenEnv = "DO_API_TOKEN"

	// doBackfillSource is the provenance tag stamped on cloud_usage.source for every
	// imported row. Native metering leaves source='' (the column default), so this is
	// the ONE discriminator readers/finance use to tell backfilled from metered.
	doBackfillSource = "do-backfill"

	// doBackfillProvider matches the finance providerGrants key ("do-ai") and the
	// mission's "do-ai gateway" — so backfilled rows roll up under the same provider
	// as native DO usage in every by-provider view.
	doBackfillProvider = "do-ai"

	// doBackfillOwner/Org is the platform identity for upstream DO spend that predates
	// (or was missed by) per-customer metering: it is genuinely un-attributable to a
	// customer, so we honestly stamp it to the operator's own org rather than fabricate
	// a customer. The all-orgs god-view (world/admin) includes it regardless; a tenant
	// view never sees another customer's data because of it.
	doBackfillOwner = "hanzo"

	// doBackfillStatus marks these as reconstructed rows in the activity feed.
	doBackfillStatus = "backfilled"

	// DO billing API paths (reused convention; see file header).
	doInvoicesPath = "/v2/customers/my/invoices"

	// doMaxInvoices bounds how many invoices we page (one 200-wide page = 200 months
	// ≈ 16 years; far beyond any real account) so a pathological account can neither
	// exhaust memory nor spin.
	doMaxInvoices = 200
)

// doAPIBase is DigitalOcean's public API host, overridable in tests. The fake HTTP
// doer in the test matches on request PATH, so tests leave this at the real host.
var doAPIBase = "https://api.digitalocean.com"

// doAILineItemKeywords is the ONE place that defines what counts as DO AI spend: an
// invoice line item is imported only when its product/description/category/group
// contains one of these (case-insensitive). DO's GenAI serverless-inference product
// has been branded both "GenAI" and "Gradient"/"GradientAI"; "inference" catches the
// serverless-inference line regardless. Non-AI DO charges (droplets, spaces,
// bandwidth, load balancers) are excluded so we never import infra spend as usage.
var doAILineItemKeywords = []string{"genai", "gradient", "inference"}

// ── request / response shapes ─────────────────────────────────────────────────────

// DOBackfillOptions is the resolved, already-authorized backfill request.
type DOBackfillOptions struct {
	From   time.Time // window start (inclusive day)
	To     time.Time // window end (inclusive day)
	DryRun bool      // when true, plan only — write nothing (the default)
	Force  bool      // when true, import even days native metering already covers
}

// DOBackfillRow is one (day, model) row the importer would write (dry-run) or wrote.
type DOBackfillRow struct {
	Day       string `json:"day"` // YYYY-MM-DD (UTC)
	Model     string `json:"model"`
	Provider  string `json:"provider"`
	CostCents int64  `json:"costCents"`
	Source    string `json:"source"`
	ID        string `json:"id"` // deterministic — re-deriving it for the same (day,model) is stable
}

// DOBackfillPlan is the importer's full answer: what it would write / wrote, plus the
// honest accounting of everything it skipped and why. Returned by both the dry-run and
// the write path so the console shows an identical shape either way.
type DOBackfillPlan struct {
	DryRun                   bool            `json:"dryRun"`
	Force                    bool            `json:"force"`
	Provider                 string          `json:"provider"`
	Source                   string          `json:"source"`
	From                     string          `json:"from"` // RFC3339 (UTC)
	To                       string          `json:"to"`   // RFC3339 (UTC)
	Rows                     []DOBackfillRow `json:"rows"`
	Days                     int             `json:"days"`
	Models                   int             `json:"models"`
	TotalCents               int64           `json:"totalCents"`
	Written                  int             `json:"written"`
	SkippedNativelyCovered   int             `json:"skippedNativelyCovered"`
	SkippedAlreadyBackfilled int             `json:"skippedAlreadyBackfilled"`
	InvoicesScanned          int             `json:"invoicesScanned"`
	InvoiceFetchErrors       int             `json:"invoiceFetchErrors"`
	Note                     string          `json:"note"`
}

// ── DO billing wire shapes (field names confirmed against DO's public API) ──────────

type doInvoiceRef struct {
	UUID   string
	Period string // "YYYY-MM"
}

type doInvoiceItem struct {
	Product          string
	GroupDescription string
	Description      string
	Amount           string // decimal-dollar string, e.g. "12.34"
	Category         string
	StartTime        time.Time
	EndTime          time.Time
}

// parseDOInvoiceRefs folds GET /v2/customers/my/invoices into its invoice references.
// A malformed body degrades to an empty slice (honest empty), never a crash.
func parseDOInvoiceRefs(body []byte) []doInvoiceRef {
	var page struct {
		Invoices []struct {
			InvoiceUUID   string `json:"invoice_uuid"`
			InvoicePeriod string `json:"invoice_period"`
		} `json:"invoices"`
	}
	if json.Unmarshal(body, &page) != nil {
		return nil
	}
	out := make([]doInvoiceRef, 0, len(page.Invoices))
	for _, iv := range page.Invoices {
		if uuid := strings.TrimSpace(iv.InvoiceUUID); uuid != "" {
			out = append(out, doInvoiceRef{UUID: uuid, Period: strings.TrimSpace(iv.InvoicePeriod)})
		}
	}
	return out
}

// parseDOInvoiceItems folds GET /v2/customers/my/invoices/{uuid} into its line items.
func parseDOInvoiceItems(body []byte) []doInvoiceItem {
	var page struct {
		InvoiceItems []struct {
			Product          string    `json:"product"`
			GroupDescription string    `json:"group_description"`
			Description      string    `json:"description"`
			Amount           string    `json:"amount"`
			Category         string    `json:"category"`
			StartTime        time.Time `json:"start_time"`
			EndTime          time.Time `json:"end_time"`
		} `json:"invoice_items"`
	}
	if json.Unmarshal(body, &page) != nil {
		return nil
	}
	out := make([]doInvoiceItem, 0, len(page.InvoiceItems))
	for _, it := range page.InvoiceItems {
		out = append(out, doInvoiceItem{
			Product:          it.Product,
			GroupDescription: it.GroupDescription,
			Description:      it.Description,
			Amount:           it.Amount,
			Category:         it.Category,
			StartTime:        it.StartTime,
			EndTime:          it.EndTime,
		})
	}
	return out
}

// ── pure mapping (DO line items → candidate rows) ───────────────────────────────────

// doBackfillCandidate is one un-reconciled (day, model, cents) triple from DO before
// gap/idempotency planning.
type doBackfillCandidate struct {
	day   string // YYYY-MM-DD (UTC)
	model string
	cents int64
}

// isDOAILineItem reports whether an invoice line item is DO GenAI / inference spend —
// the only lines this importer maps. Matching is a case-insensitive substring test over
// the item's product, description, group description, and category against
// doAILineItemKeywords, so a non-AI DO charge is never imported as AI usage.
func isDOAILineItem(it doInvoiceItem) bool {
	hay := strings.ToLower(it.Product + "\x00" + it.Description + "\x00" + it.GroupDescription + "\x00" + it.Category)
	for _, kw := range doAILineItemKeywords {
		if strings.Contains(hay, kw) {
			return true
		}
	}
	return false
}

// deriveDOModel is DO's OWN label for the charge — the honest per-model granularity DO
// gives us (never a fabricated model name). Prefer the specific description, then the
// group description, then the product; fall back to a single explicit label when DO
// names nothing, so spend still buckets rather than vanishing.
func deriveDOModel(it doInvoiceItem) string {
	for _, s := range []string{it.Description, it.GroupDescription, it.Product} {
		if t := strings.TrimSpace(s); t != "" {
			return t
		}
	}
	return "do-genai"
}

// doItemDay buckets a line item to a UTC calendar day: its start_time, else its
// end_time, else the invoice period's first day. Returns "" when none is usable (the
// caller then drops the line rather than guess a date).
func doItemDay(it doInvoiceItem, period string) string {
	if !it.StartTime.IsZero() {
		return it.StartTime.UTC().Format("2006-01-02")
	}
	if !it.EndTime.IsZero() {
		return it.EndTime.UTC().Format("2006-01-02")
	}
	if t, err := time.Parse("2006-01", strings.TrimSpace(period)); err == nil {
		return t.UTC().Format("2006-01-02")
	}
	return ""
}

// doInvoiceItemToCandidate maps ONE line item to a candidate, or ok=false when it is not
// AI spend, carries no positive dollar amount (credits/adjustments/refunds are not
// usage), or cannot be dated. Pure.
func doInvoiceItemToCandidate(it doInvoiceItem, period string) (doBackfillCandidate, bool) {
	if !isDOAILineItem(it) {
		return doBackfillCandidate{}, false
	}
	cents := doDollarsToCents(it.Amount)
	if cents <= 0 {
		return doBackfillCandidate{}, false
	}
	day := doItemDay(it, period)
	if day == "" {
		return doBackfillCandidate{}, false
	}
	return doBackfillCandidate{day: day, model: deriveDOModel(it), cents: cents}, true
}

// foldDOCandidates aggregates candidates by (day, model), summing cents — so several
// line items on the same day for the same DO product become ONE row. Output is sorted
// by (day, model) for a deterministic plan. Pure.
func foldDOCandidates(in []doBackfillCandidate) []doBackfillCandidate {
	sum := map[string]int64{}
	meta := map[string]doBackfillCandidate{}
	for _, c := range in {
		k := c.day + "\x00" + c.model
		sum[k] += c.cents
		meta[k] = doBackfillCandidate{day: c.day, model: c.model}
	}
	out := make([]doBackfillCandidate, 0, len(sum))
	for k, cents := range sum {
		m := meta[k]
		out = append(out, doBackfillCandidate{day: m.day, model: m.model, cents: cents})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].day != out[j].day {
			return out[i].day < out[j].day
		}
		return out[i].model < out[j].model
	})
	return out
}

// backfillKey is the (day, model) identity used for idempotency lookups — the SAME key
// existingBackfillKeys builds from the ledger.
func backfillKey(day, model string) string { return day + "\x00" + model }

// backfillID is the deterministic cloud_usage row id for a (day, model): re-deriving it
// for the same pair is stable, and the fnv suffix keeps ids distinct for two DO models
// whose slugs would otherwise collide within a day.
func backfillID(day, model string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(model))
	return fmt.Sprintf("%s:%s:%08x", doBackfillSource, day, h.Sum32())
}

// planDOBackfill is the pure heart of the importer: given DO candidates plus the two
// coverage sets read from the ledger, it decides exactly which rows to write and tallies
// every skip. It is total and side-effect-free, so the test drives every gap /
// idempotency / force path with plain maps — no datastore, no DO account, no clock.
//
//   - nativeDays: days (YYYY-MM-DD) that already carry a NATIVE do-ai row. A candidate on
//     such a day is skipped (never double-counted) unless force is set.
//   - existingKeys: (day, model) pairs already backfilled. A candidate matching one is
//     skipped so a re-run is a no-op — the idempotency guarantee.
func planDOBackfill(folded []doBackfillCandidate, nativeDays, existingKeys map[string]bool, force bool) DOBackfillPlan {
	plan := DOBackfillPlan{Force: force, Provider: doBackfillProvider, Source: doBackfillSource}
	days := map[string]bool{}
	models := map[string]bool{}
	for _, c := range folded {
		if !force && nativeDays[c.day] {
			plan.SkippedNativelyCovered++
			continue
		}
		if existingKeys[backfillKey(c.day, c.model)] {
			plan.SkippedAlreadyBackfilled++
			continue
		}
		plan.Rows = append(plan.Rows, DOBackfillRow{
			Day:       c.day,
			Model:     c.model,
			Provider:  doBackfillProvider,
			CostCents: c.cents,
			Source:    doBackfillSource,
			ID:        backfillID(c.day, c.model),
		})
		plan.TotalCents += c.cents
		days[c.day] = true
		models[c.model] = true
	}
	plan.Days = len(days)
	plan.Models = len(models)
	return plan
}

// doDollarsToCents parses a DO decimal-dollar string ("12.34", "-5.00") to integer
// cents, rounding to the nearest cent. Blank/invalid → 0 (the honest fallback — never a
// fabricated amount). Mirrors the cloud finance client's edge conversion.
func doDollarsToCents(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int64(math.Round(f * 100))
}

// ── DO fetch + candidate collection (HTTP only — unit-testable via the fake doer) ────

// doFetchStats accounts for what the DO fetch traversed, surfaced in the plan.
type doFetchStats struct {
	InvoicesScanned    int
	InvoiceFetchErrors int
}

// doAuthHeader is the single Bearer header every DO read carries (token never logged).
func doAuthHeader(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

// fetchDOInvoiceRefs lists the account's invoices. Auth failure is surfaced as a precise
// error (token/scope), so the endpoint renders an actionable message rather than a
// phantom empty backfill.
func fetchDOInvoiceRefs(ctx context.Context, token string) ([]doInvoiceRef, error) {
	v := url.Values{}
	v.Set("per_page", strconv.Itoa(doMaxInvoices))
	body, status, err := providerUsageGet(ctx, doAPIBase+doInvoicesPath+"?"+v.Encode(), doAuthHeader(token))
	if err != nil {
		return nil, fmt.Errorf("digitalocean invoices request failed: %w", err)
	}
	if isAuthStatus(status) {
		return nil, fmt.Errorf("digitalocean did not authorize billing access (status %d) — DO_API_TOKEN needs read access to billing", status)
	}
	if status/100 != 2 {
		return nil, fmt.Errorf("digitalocean invoices status %d", status)
	}
	return parseDOInvoiceRefs(body), nil
}

// fetchDOInvoiceItems reads one invoice's line items.
func fetchDOInvoiceItems(ctx context.Context, token, uuid string) ([]doInvoiceItem, error) {
	body, status, err := providerUsageGet(ctx, doAPIBase+doInvoicesPath+"/"+url.PathEscape(uuid), doAuthHeader(token))
	if err != nil {
		return nil, fmt.Errorf("digitalocean invoice %s request failed: %w", uuid, err)
	}
	if status/100 != 2 {
		return nil, fmt.Errorf("digitalocean invoice %s status %d", uuid, status)
	}
	return parseDOInvoiceItems(body), nil
}

// collectDOCandidates fetches invoices in [from,to], pulls each in-window invoice's line
// items, maps the AI ones to candidates, and folds them by (day, model). It does HTTP
// only (no datastore), so the test drives the whole DO pipeline with a fake doer. A
// single invoice fetch failing is counted (InvoiceFetchErrors) and skipped rather than
// aborting the whole backfill — a partial import is still honest and additive.
func collectDOCandidates(ctx context.Context, token string, from, to time.Time) ([]doBackfillCandidate, doFetchStats, error) {
	refs, err := fetchDOInvoiceRefs(ctx, token)
	if err != nil {
		return nil, doFetchStats{}, err
	}
	var (
		cands []doBackfillCandidate
		stats doFetchStats
	)
	for _, ref := range refs {
		if !doInvoicePeriodOverlaps(ref.Period, from, to) {
			continue
		}
		stats.InvoicesScanned++
		items, ierr := fetchDOInvoiceItems(ctx, token, ref.UUID)
		if ierr != nil {
			stats.InvoiceFetchErrors++
			log.Warn("DO backfill: invoice %s: %v", ref.UUID, ierr)
			continue
		}
		for _, it := range items {
			c, ok := doInvoiceItemToCandidate(it, ref.Period)
			if !ok || !doDayInWindow(c.day, from, to) {
				continue
			}
			cands = append(cands, c)
		}
	}
	return foldDOCandidates(cands), stats, nil
}

// doInvoicePeriodOverlaps reports whether a "YYYY-MM" invoice period overlaps [from,to],
// so we only fetch line items for invoices that can contribute. An unparseable period is
// treated as overlapping (fetched) — per-item day filtering then decides honestly.
func doInvoicePeriodOverlaps(period string, from, to time.Time) bool {
	monthStart, err := time.Parse("2006-01", strings.TrimSpace(period))
	if err != nil {
		return true
	}
	monthEnd := monthStart.AddDate(0, 1, 0)
	return monthStart.Before(to) && monthEnd.After(from)
}

// doDayInWindow reports whether a YYYY-MM-DD day falls within [from,to] at day
// granularity (both ends inclusive).
func doDayInWindow(day string, from, to time.Time) bool {
	d, err := time.Parse("2006-01-02", day)
	if err != nil {
		return false
	}
	lo := from.UTC().Truncate(24 * time.Hour)
	hi := to.UTC().Truncate(24 * time.Hour)
	return !d.Before(lo) && !d.After(hi)
}

// ── datastore coverage + write (the thin I/O layer) ─────────────────────────────────

// ensureBackfillSchema adds the additive cloud_usage.source provenance column if it is
// missing. The backfill feature is the sole reader/writer of this column, so it owns the
// column's creation here (idempotent ADD COLUMN IF NOT EXISTS) — the native write path
// is untouched and its rows keep the ” default that marks them as metered.
func ensureBackfillSchema(ctx context.Context) error {
	return object.DatastoreExec(ctx, "ALTER TABLE hanzo.cloud_usage ADD COLUMN IF NOT EXISTS source String")
}

// doDayWindowLits returns the datastore DateTime bounds [lo, hi) that span exactly the
// full calendar days from's-day .. to's-day inclusive — the SAME day range doDayInWindow
// accepts. Aligning coverage to whole days (not the raw clock bounds) is what makes the
// gap check sound: a native row anywhere on a day marks the WHOLE day covered, so a
// backfill row (written at that day's noon) can never be double-counted against it.
func doDayWindowLits(from, to time.Time) (lo, hi string) {
	loT := from.UTC().Truncate(24 * time.Hour)
	hiT := to.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)
	return doTSLit(loT), doTSLit(hiT)
}

// nativeCoveredDays returns the set of UTC days (YYYY-MM-DD) that already carry a NATIVE
// (source=”) do-ai row in [from,to] — the days a backfill must NOT touch.
func nativeCoveredDays(ctx context.Context, from, to time.Time) (map[string]bool, error) {
	lo, hi := doDayWindowLits(from, to)
	rows, err := object.DatastoreQuery(ctx,
		"SELECT DISTINCT toString(toDate(timestamp)) AS d FROM hanzo.cloud_usage "+
			"WHERE provider = ? AND source = '' AND timestamp >= ? AND timestamp < ?",
		doBackfillProvider, lo, hi)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(rows))
	for _, r := range rows {
		if d := doDayString(r["d"]); d != "" {
			out[d] = true
		}
	}
	return out, nil
}

// existingBackfillKeys returns the set of (day, model) already backfilled (source=
// 'do-backfill') in [from,to] — so a re-run skips them and stays idempotent.
func existingBackfillKeys(ctx context.Context, from, to time.Time) (map[string]bool, error) {
	lo, hi := doDayWindowLits(from, to)
	rows, err := object.DatastoreQuery(ctx,
		"SELECT DISTINCT toString(toDate(timestamp)) AS d, model FROM hanzo.cloud_usage "+
			"WHERE provider = ? AND source = ? AND timestamp >= ? AND timestamp < ?",
		doBackfillProvider, doBackfillSource, lo, hi)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(rows))
	for _, r := range rows {
		d := doDayString(r["d"])
		m, _ := r["model"].(string)
		if d != "" {
			out[backfillKey(d, m)] = true
		}
	}
	return out, nil
}

// writeDOBackfillRows appends the planned rows to hanzo.cloud_usage. It writes ONLY the
// columns it owns (source='do-backfill', tokens 0, the DO cost, provenance identity) and
// omits the rest — Datastore fills them with their column defaults, so this writer is
// independent of the native writer's margin/fee columns. Returns the number written.
func writeDOBackfillRows(ctx context.Context, rows []DOBackfillRow) (int, error) {
	written := 0
	for _, row := range rows {
		day, err := time.Parse("2006-01-02", row.Day)
		if err != nil {
			continue
		}
		ts := day.Add(12 * time.Hour) // noon UTC — unambiguously inside the day for toDate()
		if err := object.DatastoreExec(
			ctx,
			"INSERT INTO hanzo.cloud_usage "+
				"(id, timestamp, owner, user_id, organization, model, provider, request_id, "+
				"prompt_tokens, completion_tokens, total_tokens, cost_cents, currency, status, source) "+
				"VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, 0, 0, ?, 'usd', ?, ?)",
			row.ID, ts, doBackfillOwner, "", doBackfillOwner, row.Model, doBackfillProvider, row.ID,
			uint64(row.CostCents), doBackfillStatus, doBackfillSource,
		); err != nil {
			return written, fmt.Errorf("write backfill row %s: %w", row.ID, err)
		}
		written++
	}
	return written, nil
}

// RunDOBackfill is the orchestration: fetch DO spend, read the ledger's native +
// already-backfilled coverage, plan the gap-fill (pure), and — only when dryRun is
// false — persist. It is a thin seam over the pure/testable pieces; the DO fetch and the
// planning are exercised directly by the tests, this glue is not.
func RunDOBackfill(ctx context.Context, opts DOBackfillOptions) (DOBackfillPlan, error) {
	token := strings.TrimSpace(os.Getenv(doBackfillTokenEnv))
	if token == "" {
		return DOBackfillPlan{}, fmt.Errorf("%s not configured — cannot read DigitalOcean billing", doBackfillTokenEnv)
	}
	if !object.DatastoreEnabled() {
		return DOBackfillPlan{}, fmt.Errorf("usage ledger unavailable: datastore peer not connected")
	}
	if err := object.EnsureCloudUsageTable(ctx); err != nil {
		return DOBackfillPlan{}, fmt.Errorf("usage ledger: %w", err)
	}
	if err := ensureBackfillSchema(ctx); err != nil {
		return DOBackfillPlan{}, fmt.Errorf("usage ledger: ensure source column: %w", err)
	}

	folded, stats, err := collectDOCandidates(ctx, token, opts.From, opts.To)
	if err != nil {
		return DOBackfillPlan{}, err
	}

	nativeDays, err := nativeCoveredDays(ctx, opts.From, opts.To)
	if err != nil {
		return DOBackfillPlan{}, fmt.Errorf("native coverage: %w", err)
	}
	existing, err := existingBackfillKeys(ctx, opts.From, opts.To)
	if err != nil {
		return DOBackfillPlan{}, fmt.Errorf("existing backfill: %w", err)
	}

	plan := planDOBackfill(folded, nativeDays, existing, opts.Force)
	plan.DryRun = opts.DryRun
	plan.From = opts.From.UTC().Format(time.RFC3339)
	plan.To = opts.To.UTC().Format(time.RFC3339)
	plan.InvoicesScanned = stats.InvoicesScanned
	plan.InvoiceFetchErrors = stats.InvoiceFetchErrors

	if opts.DryRun {
		plan.Note = fmt.Sprintf("Dry run — %d row(s) across %d day(s) would be written to hanzo.cloud_usage. Re-POST with dryRun=false to persist.", len(plan.Rows), plan.Days)
		return plan, nil
	}

	n, werr := writeDOBackfillRows(ctx, plan.Rows)
	plan.Written = n
	if werr != nil {
		plan.Note = fmt.Sprintf("Wrote %d of %d row(s) before an error: %v", n, len(plan.Rows), werr)
		return plan, werr
	}
	plan.Note = fmt.Sprintf("Wrote %d backfilled row(s) across %d day(s) to hanzo.cloud_usage.", n, plan.Days)
	return plan, nil
}

// doTSLit formats a datastore DateTime literal (UTC) — identical to cloud_usage's own.
func doTSLit(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05") }

// doDayString coerces a datastore Date/DateTime/string cell to a YYYY-MM-DD string.
func doDayString(v interface{}) string {
	switch d := v.(type) {
	case string:
		return strings.TrimSpace(d)
	case time.Time:
		return d.UTC().Format("2006-01-02")
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", d))
	}
}

// ── super-admin endpoint ────────────────────────────────────────────────────────────

// resolveDOBackfillWindow parses ?from/?to (RFC3339, YYYY-MM-DD, or unix seconds) into a
// backfill window: default the last 365 days (DO invoices are monthly, so a wide default
// catches missed history), clamped to at most 2 years, from strictly before to.
func resolveDOBackfillWindow(fromRaw, toRaw string, now time.Time) (from, to time.Time) {
	to = parseUsageTime(toRaw, now)
	from = parseUsageTime(fromRaw, to.AddDate(-1, 0, 0))
	if !from.Before(to) {
		from = to.AddDate(-1, 0, 0)
	}
	if to.Sub(from) > 730*24*time.Hour {
		from = to.AddDate(0, 0, -730)
	}
	return from, to
}

// PostBackfillDOUsage
// @Title PostBackfillDOUsage
// @Tag Usage API
// @Description Super-admin: backfill the hanzo.cloud_usage ledger from DigitalOcean's
// billing API for (day) windows our native metering missed — GAPS ONLY (never
// double-counts a natively-metered day), PROVENANCE-tagged (source="do-backfill"),
// and IDEMPOTENT (re-running is a no-op). DRY-RUN by default: returns the rows/days/
// totals it WOULD write and persists nothing unless dryRun=false is passed explicitly.
// @Param from query string false "window start (RFC3339, YYYY-MM-DD, or unix seconds; default 365d ago)"
// @Param to query string false "window end (RFC3339, YYYY-MM-DD, or unix seconds; default now)"
// @Param dryRun query string false "false to persist; anything else (default) plans only"
// @Param force query string false "true to import even days native metering already covers"
// @Success 200 {object} controllers.DOBackfillPlan The Response object
// @router /admin/usage/backfill-do [post]
func (c *ApiController) PostBackfillDOUsage() {
	// Controller self-guard (defense in depth): this writes the platform-wide financial
	// ledger, so re-check super admin even though the authz filter gates the route.
	// Fail-closed 401/403.
	if !c.RequireSuperAdmin() {
		return
	}

	from, to := resolveDOBackfillWindow(c.Input().Get("from"), c.Input().Get("to"), time.Now())
	opts := DOBackfillOptions{
		From:   from,
		To:     to,
		DryRun: !doFlagFalse(c.Input().Get("dryRun")), // WRITE requires an explicit dryRun=false
		Force:  doFlagTrue(c.Input().Get("force")),
	}

	ctx, cancel := context.WithTimeout(c.Ctx.Request.Context(), 60*time.Second)
	defer cancel()

	plan, err := RunDOBackfill(ctx, opts)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(plan)
}

// doFlagFalse reports whether a query flag explicitly says false ("false"/"0"/"no"/"off").
// Used so dryRun stays ON unless the caller deliberately turns it off.
func doFlagFalse(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "false", "0", "no", "off":
		return true
	default:
		return false
	}
}

// doFlagTrue reports whether a query flag explicitly says true.
func doFlagTrue(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "1", "yes", "on":
		return true
	default:
		return false
	}
}
