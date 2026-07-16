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
	"context"
	"testing"
	"time"
)

// ── DO wire parsing ─────────────────────────────────────────────────────────────────

func TestParseDOInvoiceRefs(t *testing.T) {
	body := `{"invoices":[
	  {"invoice_uuid":"aa-1","amount":"120.00","invoice_period":"2026-01"},
	  {"invoice_uuid":"bb-2","amount":"340.00","invoice_period":"2026-02"},
	  {"invoice_uuid":"","amount":"0.00","invoice_period":"2026-03"}
	],"meta":{"total":3}}`
	refs := parseDOInvoiceRefs([]byte(body))
	if len(refs) != 2 {
		t.Fatalf("refs = %d, want 2 (blank uuid dropped)", len(refs))
	}
	if refs[0].UUID != "aa-1" || refs[0].Period != "2026-01" {
		t.Fatalf("ref0 = %+v", refs[0])
	}
	if parseDOInvoiceRefs([]byte("not json")) != nil {
		t.Fatalf("garbage should parse to nil")
	}
}

func TestParseDOInvoiceItems(t *testing.T) {
	// Real-shaped DO invoice payload: a GenAI serverless-inference line plus a droplet.
	body := `{"invoice_items":[
	  {"product":"GenAI Platform","group_description":"Serverless Inference",
	   "description":"GenAI Serverless Inference - llama3-8b-instruct","amount":"42.50",
	   "duration":"720","duration_unit":"Hours","start_time":"2026-01-05T00:00:00Z",
	   "end_time":"2026-02-01T00:00:00Z","project_name":"hanzo","category":"paas"},
	  {"product":"Droplets","description":"s-2vcpu-4gb","amount":"24.00",
	   "start_time":"2026-01-05T00:00:00Z","end_time":"2026-02-01T00:00:00Z","category":"iaas"}
	],"meta":{"total":2}}`
	items := parseDOInvoiceItems([]byte(body))
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	if items[0].Product != "GenAI Platform" || items[0].Amount != "42.50" {
		t.Fatalf("item0 = %+v", items[0])
	}
	if items[0].StartTime.UTC().Format("2006-01-02") != "2026-01-05" {
		t.Fatalf("item0 start = %v", items[0].StartTime)
	}
	if parseDOInvoiceItems([]byte("{")) != nil {
		t.Fatalf("garbage should parse to nil")
	}
}

// ── AI line-item classification ─────────────────────────────────────────────────────

func TestIsDOAILineItem(t *testing.T) {
	cases := []struct {
		name string
		it   doInvoiceItem
		want bool
	}{
		{"genai product", doInvoiceItem{Product: "GenAI Platform"}, true},
		{"gradient brand", doInvoiceItem{Product: "GradientAI", Description: "agent"}, true},
		{"inference in description", doInvoiceItem{Product: "Serverless", Description: "Model Inference"}, true},
		{"inference in category", doInvoiceItem{Category: "ai-inference"}, true},
		{"droplet not ai", doInvoiceItem{Product: "Droplets", Description: "s-2vcpu-4gb"}, false},
		{"spaces not ai", doInvoiceItem{Product: "Spaces", Category: "storage"}, false},
		{"bandwidth not ai", doInvoiceItem{Product: "Bandwidth"}, false},
		{"empty not ai", doInvoiceItem{}, false},
	}
	for _, tc := range cases {
		if got := isDOAILineItem(tc.it); got != tc.want {
			t.Errorf("%s: isDOAILineItem = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestDeriveDOModel(t *testing.T) {
	cases := []struct {
		it   doInvoiceItem
		want string
	}{
		{doInvoiceItem{Description: "GenAI Serverless Inference - llama3-8b"}, "GenAI Serverless Inference - llama3-8b"},
		{doInvoiceItem{GroupDescription: "Serverless Inference", Product: "GenAI"}, "Serverless Inference"},
		{doInvoiceItem{Product: "GenAI Platform"}, "GenAI Platform"},
		{doInvoiceItem{}, "do-genai"}, // honest fallback, never fabricated
	}
	for _, tc := range cases {
		if got := deriveDOModel(tc.it); got != tc.want {
			t.Errorf("deriveDOModel(%+v) = %q, want %q", tc.it, got, tc.want)
		}
	}
}

// ── dollar → cents ──────────────────────────────────────────────────────────────────

func TestDoDollarsToCents(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"12.34", 1234},
		{"0.005", 1}, // rounds to the nearest cent
		{"-5.00", -500},
		{"100", 10000},
		{"", 0},
		{"bogus", 0},
	}
	for _, tc := range cases {
		if got := doDollarsToCents(tc.in); got != tc.want {
			t.Errorf("doDollarsToCents(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// ── candidate mapping ───────────────────────────────────────────────────────────────

func TestDoInvoiceItemToCandidate(t *testing.T) {
	ai := doInvoiceItem{
		Product: "GenAI Platform", Description: "GenAI Serverless Inference",
		Amount: "42.50", StartTime: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC),
	}
	c, ok := doInvoiceItemToCandidate(ai, "2026-01")
	if !ok {
		t.Fatalf("ai line item should map")
	}
	if c.day != "2026-01-05" || c.cents != 4250 || c.model != "GenAI Serverless Inference" {
		t.Fatalf("candidate = %+v", c)
	}

	// Non-AI → dropped.
	if _, ok := doInvoiceItemToCandidate(doInvoiceItem{Product: "Droplets", Amount: "9.00", StartTime: ai.StartTime}, "2026-01"); ok {
		t.Fatalf("droplet must not map")
	}

	// Zero / credit amount → dropped (not usage).
	if _, ok := doInvoiceItemToCandidate(doInvoiceItem{Product: "GenAI", Amount: "0.00", StartTime: ai.StartTime}, "2026-01"); ok {
		t.Fatalf("zero-amount AI line must not map")
	}
	if _, ok := doInvoiceItemToCandidate(doInvoiceItem{Product: "GenAI", Amount: "-3.00", StartTime: ai.StartTime}, "2026-01"); ok {
		t.Fatalf("credit AI line must not map")
	}

	// No start_time → fall back to the invoice period's first day.
	c2, ok := doInvoiceItemToCandidate(doInvoiceItem{Product: "GenAI", Amount: "1.00"}, "2026-03")
	if !ok || c2.day != "2026-03-01" {
		t.Fatalf("period fallback = %+v ok=%v", c2, ok)
	}
}

func TestFoldDOCandidates(t *testing.T) {
	in := []doBackfillCandidate{
		{day: "2026-01-05", model: "GenAI Inference", cents: 100},
		{day: "2026-01-05", model: "GenAI Inference", cents: 250}, // same (day,model) → summed
		{day: "2026-01-05", model: "GenAI Agent", cents: 50},
		{day: "2026-01-04", model: "GenAI Inference", cents: 999},
	}
	out := foldDOCandidates(in)
	if len(out) != 3 {
		t.Fatalf("folded = %d, want 3", len(out))
	}
	// Sorted by (day, model): 01-04 first.
	if out[0].day != "2026-01-04" || out[0].cents != 999 {
		t.Fatalf("out0 = %+v", out[0])
	}
	// The summed (2026-01-05, GenAI Inference) row.
	var found bool
	for _, c := range out {
		if c.day == "2026-01-05" && c.model == "GenAI Inference" {
			found = true
			if c.cents != 350 {
				t.Fatalf("summed cents = %d, want 350", c.cents)
			}
		}
	}
	if !found {
		t.Fatalf("summed row missing: %+v", out)
	}
}

// ── the pure planning core: gaps-only, provenance, idempotency, force ────────────────

func TestPlanDOBackfill_GapsOnly(t *testing.T) {
	folded := []doBackfillCandidate{
		{day: "2026-01-04", model: "GenAI Inference", cents: 999}, // gap → written
		{day: "2026-01-05", model: "GenAI Inference", cents: 350}, // natively covered → skipped
		{day: "2026-01-06", model: "GenAI Inference", cents: 100}, // already backfilled → skipped
	}
	nativeDays := map[string]bool{"2026-01-05": true}
	existing := map[string]bool{backfillKey("2026-01-06", "GenAI Inference"): true}

	plan := planDOBackfill(folded, nativeDays, existing, false)

	if len(plan.Rows) != 1 {
		t.Fatalf("rows = %d, want 1 (only the gap day)", len(plan.Rows))
	}
	r := plan.Rows[0]
	if r.Day != "2026-01-04" || r.CostCents != 999 {
		t.Fatalf("written row = %+v", r)
	}
	if r.Source != doBackfillSource || r.Provider != doBackfillProvider {
		t.Fatalf("provenance not stamped: %+v", r)
	}
	if r.ID != backfillID("2026-01-04", "GenAI Inference") {
		t.Fatalf("id = %q, not deterministic", r.ID)
	}
	if plan.SkippedNativelyCovered != 1 {
		t.Fatalf("SkippedNativelyCovered = %d, want 1", plan.SkippedNativelyCovered)
	}
	if plan.SkippedAlreadyBackfilled != 1 {
		t.Fatalf("SkippedAlreadyBackfilled = %d, want 1", plan.SkippedAlreadyBackfilled)
	}
	if plan.TotalCents != 999 || plan.Days != 1 || plan.Models != 1 {
		t.Fatalf("totals = cents %d days %d models %d", plan.TotalCents, plan.Days, plan.Models)
	}
}

func TestPlanDOBackfill_ForceOverridesNativeButNotIdempotency(t *testing.T) {
	folded := []doBackfillCandidate{
		{day: "2026-01-05", model: "GenAI Inference", cents: 350}, // natively covered
		{day: "2026-01-06", model: "GenAI Inference", cents: 100}, // already backfilled
	}
	nativeDays := map[string]bool{"2026-01-05": true}
	existing := map[string]bool{backfillKey("2026-01-06", "GenAI Inference"): true}

	plan := planDOBackfill(folded, nativeDays, existing, true) // force

	// force re-imports the natively-covered day, but STILL skips the already-backfilled
	// (day,model) so a re-run can never duplicate.
	if len(plan.Rows) != 1 || plan.Rows[0].Day != "2026-01-05" {
		t.Fatalf("force rows = %+v", plan.Rows)
	}
	if plan.SkippedNativelyCovered != 0 {
		t.Fatalf("force should not skip natively-covered, got %d", plan.SkippedNativelyCovered)
	}
	if plan.SkippedAlreadyBackfilled != 1 {
		t.Fatalf("force must still honor idempotency, got %d", plan.SkippedAlreadyBackfilled)
	}
}

// TestPlanDOBackfill_Idempotent proves running twice = same state: feeding the first
// run's written keys back as existingKeys yields an empty second plan.
func TestPlanDOBackfill_Idempotent(t *testing.T) {
	folded := []doBackfillCandidate{
		{day: "2026-01-04", model: "GenAI Inference", cents: 999},
		{day: "2026-01-07", model: "GenAI Agent", cents: 500},
	}
	first := planDOBackfill(folded, nil, nil, false)
	if len(first.Rows) != 2 {
		t.Fatalf("first run rows = %d, want 2", len(first.Rows))
	}

	existing := map[string]bool{}
	for _, r := range first.Rows {
		existing[backfillKey(r.Day, r.Model)] = true
	}
	second := planDOBackfill(folded, nil, existing, false)
	if len(second.Rows) != 0 {
		t.Fatalf("second run rows = %d, want 0 (idempotent)", len(second.Rows))
	}
	if second.SkippedAlreadyBackfilled != 2 {
		t.Fatalf("second run should skip both, got %d", second.SkippedAlreadyBackfilled)
	}
}

// ── window helpers ──────────────────────────────────────────────────────────────────

func TestDoInvoicePeriodOverlaps(t *testing.T) {
	from := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		period string
		want   bool
	}{
		{"2026-01", true},  // overlaps at the tail
		{"2026-02", true},  // fully inside
		{"2026-03", true},  // overlaps at the head
		{"2025-12", false}, // before
		{"2026-04", false}, // after
		{"garbage", true},  // unparseable → fetched, per-item filter decides
	}
	for _, tc := range cases {
		if got := doInvoicePeriodOverlaps(tc.period, from, to); got != tc.want {
			t.Errorf("period %q overlaps = %v, want %v", tc.period, got, tc.want)
		}
	}
}

func TestDoDayInWindow(t *testing.T) {
	from := time.Date(2026, 1, 15, 8, 0, 0, 0, time.UTC) // intra-day from
	to := time.Date(2026, 1, 20, 8, 0, 0, 0, time.UTC)
	if !doDayInWindow("2026-01-15", from, to) {
		t.Errorf("from-day should be in window")
	}
	if !doDayInWindow("2026-01-20", from, to) {
		t.Errorf("to-day should be in window")
	}
	if doDayInWindow("2026-01-14", from, to) {
		t.Errorf("day before window should be out")
	}
	if doDayInWindow("2026-01-21", from, to) {
		t.Errorf("day after window should be out")
	}
	if doDayInWindow("bogus", from, to) {
		t.Errorf("unparseable day should be out")
	}
}

// TestDoDayWindowLits proves the coverage query spans WHOLE calendar days: an intra-day
// `from` widens down to that day's 00:00:00, and `to` widens up to the end of its day —
// so a native row anywhere on an edge day marks the whole day covered (no double-count).
func TestDoDayWindowLits(t *testing.T) {
	from := time.Date(2026, 1, 15, 8, 30, 0, 0, time.UTC)
	to := time.Date(2026, 1, 20, 17, 45, 0, 0, time.UTC)
	lo, hi := doDayWindowLits(from, to)
	if lo != "2026-01-15 00:00:00" {
		t.Fatalf("lo = %q, want 2026-01-15 00:00:00 (day-truncated)", lo)
	}
	if hi != "2026-01-21 00:00:00" {
		t.Fatalf("hi = %q, want 2026-01-21 00:00:00 (to-day + 1)", hi)
	}
}

func TestResolveDOBackfillWindow(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)

	// Default: last 365 days ending now.
	from, to := resolveDOBackfillWindow("", "", now)
	if !to.Equal(now) {
		t.Fatalf("default to = %v, want now", to)
	}
	if d := to.Sub(from); d < 364*24*time.Hour || d > 366*24*time.Hour {
		t.Fatalf("default window = %v, want ~365d", d)
	}

	// Explicit YYYY-MM-DD bounds.
	from, to = resolveDOBackfillWindow("2026-01-01", "2026-03-01", now)
	if from.Format("2006-01-02") != "2026-01-01" || to.Format("2006-01-02") != "2026-03-01" {
		t.Fatalf("bounds = %v..%v", from, to)
	}

	// Over-long window clamps to <= 2 years.
	from, to = resolveDOBackfillWindow("2019-01-01", "2026-01-01", now)
	if d := to.Sub(from); d > 730*24*time.Hour {
		t.Fatalf("window not clamped: %v", d)
	}
}

func TestDoFlagParsing(t *testing.T) {
	for _, s := range []string{"false", "0", "no", "off", "FALSE", " No "} {
		if !doFlagFalse(s) {
			t.Errorf("doFlagFalse(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "true", "1", "yes", "anything"} {
		if doFlagFalse(s) {
			t.Errorf("doFlagFalse(%q) = true, want false", s)
		}
	}
	for _, s := range []string{"true", "1", "yes", "on", "TRUE"} {
		if !doFlagTrue(s) {
			t.Errorf("doFlagTrue(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "false", "0", "maybe"} {
		if doFlagTrue(s) {
			t.Errorf("doFlagTrue(%q) = true, want false", s)
		}
	}
}

func TestBackfillIDDeterministicAndDistinct(t *testing.T) {
	a := backfillID("2026-01-04", "GenAI Inference")
	if a != backfillID("2026-01-04", "GenAI Inference") {
		t.Fatalf("id not deterministic")
	}
	// Distinct model → distinct id within the same day.
	if a == backfillID("2026-01-04", "GenAI Agent") {
		t.Fatalf("distinct models must yield distinct ids")
	}
	// Distinct day → distinct id.
	if a == backfillID("2026-01-05", "GenAI Inference") {
		t.Fatalf("distinct days must yield distinct ids")
	}
}

// ── the full DO fetch pipeline (HTTP only) via the shared fake doer ─────────────────

func TestCollectDOCandidates(t *testing.T) {
	invoices := `{"invoices":[
	  {"invoice_uuid":"jan-2026","amount":"66.50","invoice_period":"2026-01"},
	  {"invoice_uuid":"old-2020","amount":"5.00","invoice_period":"2020-01"}
	]}`
	janItems := `{"invoice_items":[
	  {"product":"GenAI Platform","description":"GenAI Serverless Inference",
	   "amount":"42.50","start_time":"2026-01-05T00:00:00Z","category":"paas"},
	  {"product":"GenAI Platform","description":"GenAI Serverless Inference",
	   "amount":"7.50","start_time":"2026-01-05T00:00:00Z","category":"paas"},
	  {"product":"GenAI Platform","description":"GenAI Agent",
	   "amount":"10.00","start_time":"2026-01-06T00:00:00Z","category":"paas"},
	  {"product":"Droplets","description":"s-2vcpu-4gb","amount":"24.00",
	   "start_time":"2026-01-05T00:00:00Z","category":"iaas"}
	]}`
	withFakeUsageHTTP(t, fakeUsageDoer{byPath: map[string]fakeResp{
		"/v2/customers/my/invoices":          {200, invoices},
		"/v2/customers/my/invoices/jan-2026": {200, janItems},
		// old-2020 is out of window; its items are never fetched, so no stub needed.
	}})

	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	folded, stats, err := collectDOCandidates(context.Background(), "do-token", from, to)
	if err != nil {
		t.Fatalf("collectDOCandidates err: %v", err)
	}
	if stats.InvoicesScanned != 1 { // only the in-window invoice was pulled
		t.Fatalf("InvoicesScanned = %d, want 1", stats.InvoicesScanned)
	}
	if stats.InvoiceFetchErrors != 0 {
		t.Fatalf("InvoiceFetchErrors = %d, want 0", stats.InvoiceFetchErrors)
	}
	// Droplet excluded; the two 2026-01-05 inference lines summed; agent kept separate.
	if len(folded) != 2 {
		t.Fatalf("folded = %d, want 2 (%+v)", len(folded), folded)
	}
	byKey := map[string]int64{}
	for _, c := range folded {
		byKey[c.day+"|"+c.model] = c.cents
	}
	if byKey["2026-01-05|GenAI Serverless Inference"] != 5000 { // 42.50 + 7.50
		t.Fatalf("inference cents = %d, want 5000", byKey["2026-01-05|GenAI Serverless Inference"])
	}
	if byKey["2026-01-06|GenAI Agent"] != 1000 {
		t.Fatalf("agent cents = %d, want 1000", byKey["2026-01-06|GenAI Agent"])
	}
}

func TestCollectDOCandidates_AuthError(t *testing.T) {
	withFakeUsageHTTP(t, fakeUsageDoer{byPath: map[string]fakeResp{
		"/v2/customers/my/invoices": {403, `{"id":"forbidden","message":"forbidden"}`},
	}})
	_, _, err := collectDOCandidates(context.Background(), "bad-token", time.Now().AddDate(0, -1, 0), time.Now())
	if err == nil {
		t.Fatalf("expected an auth error, got nil")
	}
}

// TestCollectDOCandidates_PartialInvoiceFailure proves one bad invoice fetch is counted
// and skipped, not fatal — the rest of the backfill still lands.
func TestCollectDOCandidates_PartialInvoiceFailure(t *testing.T) {
	invoices := `{"invoices":[
	  {"invoice_uuid":"good","amount":"10.00","invoice_period":"2026-01"},
	  {"invoice_uuid":"broken","amount":"10.00","invoice_period":"2026-01"}
	]}`
	goodItems := `{"invoice_items":[
	  {"product":"GenAI","description":"Inference","amount":"10.00","start_time":"2026-01-10T00:00:00Z"}
	]}`
	withFakeUsageHTTP(t, fakeUsageDoer{byPath: map[string]fakeResp{
		"/v2/customers/my/invoices":        {200, invoices},
		"/v2/customers/my/invoices/good":   {200, goodItems},
		"/v2/customers/my/invoices/broken": {500, `{"message":"boom"}`},
	}})

	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	folded, stats, err := collectDOCandidates(context.Background(), "tok", from, to)
	if err != nil {
		t.Fatalf("partial failure should not be fatal: %v", err)
	}
	if stats.InvoicesScanned != 2 || stats.InvoiceFetchErrors != 1 {
		t.Fatalf("stats = %+v, want scanned 2 errors 1", stats)
	}
	if len(folded) != 1 || folded[0].cents != 1000 {
		t.Fatalf("folded = %+v, want the one good row", folded)
	}
}
