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

package object

import (
	"strings"
	"testing"
	"time"
)

// The dedup aliases deliberately reuse their column names (any(timestamp) AS
// timestamp), and ClickHouse resolves a WHERE identifier in the same SELECT
// against those aliases — reading the AGGREGATE and refusing with
// ILLEGAL_AGGREGATION (code 184). The predicate must therefore live one level
// below, on a plain scan of the base table.
func TestCloudUsageDedupedSourceFiltersBelowTheAliases(t *testing.T) {
	src := cloudUsageDedupedSource("timestamp >= ? AND timestamp < ? AND organization = ?")
	want := "FROM (SELECT * FROM hanzo.cloud_usage WHERE timestamp >= ? AND timestamp < ? AND organization = ?) GROUP BY id)"
	if !strings.HasSuffix(src, want) {
		t.Fatalf("dedup predicate must be an inner plain scan, got: %s", src)
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("bad time %q: %v", s, err)
	}
	return tm.UTC()
}

func TestCloudUsageDelta(t *testing.T) {
	tests := []struct {
		name           string
		current, prior int64
		wantPctNil     bool
		wantPct        float64
	}{
		{name: "no prior basis -> nil pct", current: 100, prior: 0, wantPctNil: true},
		{name: "growth", current: 1000, prior: 800, wantPct: 25},
		{name: "decline", current: 75, prior: 100, wantPct: -25},
		{name: "flat", current: 50, prior: 50, wantPct: 0},
		{name: "rounds to one decimal", current: 1001, prior: 3000, wantPct: -66.6},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := cloudUsageDelta(tc.current, tc.prior)
			if d.Current != tc.current || d.Prior != tc.prior {
				t.Fatalf("delta carried wrong base: %+v", d)
			}
			if tc.wantPctNil {
				if d.Pct != nil {
					t.Fatalf("expected nil pct, got %v", *d.Pct)
				}
				return
			}
			if d.Pct == nil {
				t.Fatalf("expected pct %v, got nil", tc.wantPct)
			}
			if *d.Pct != tc.wantPct {
				t.Errorf("pct = %v, want %v", *d.Pct, tc.wantPct)
			}
		})
	}
}

func TestBuildCloudUsageSeries_GapFill(t *testing.T) {
	start := mustTime(t, "2026-06-29T00:00:00Z")
	end := mustTime(t, "2026-06-29T03:00:00Z")
	rows := []map[string]interface{}{
		{"bucket": "2026-06-29 00:00:00", "tokens": float64(100), "cost_cents": float64(10), "requests": float64(2), "models": float64(1)},
		{"bucket": "2026-06-29 02:00:00", "tokens": "300", "cost_cents": float64(30), "requests": float64(4), "models": float64(2)},
	}
	got := buildCloudUsageSeries(start, end, "hour", rows)

	if len(got) != 3 {
		t.Fatalf("series length = %d, want 3 (gap-filled)", len(got))
	}
	want := []CloudUsageSeriesPoint{
		{T: "2026-06-29T00:00:00Z", Tokens: 100, SpendCents: 10, Requests: 2, Models: 1},
		{T: "2026-06-29T01:00:00Z", Tokens: 0, SpendCents: 0, Requests: 0, Models: 0},
		{T: "2026-06-29T02:00:00Z", Tokens: 300, SpendCents: 30, Requests: 4, Models: 2},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("point[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestBuildCloudUsageSeries_Daily(t *testing.T) {
	start := mustTime(t, "2026-06-27T00:00:00Z")
	end := mustTime(t, "2026-06-30T00:00:00Z")
	rows := []map[string]interface{}{
		{"bucket": "2026-06-28 00:00:00", "tokens": float64(500), "cost_cents": float64(50), "requests": float64(9), "models": float64(3)},
	}
	got := buildCloudUsageSeries(start, end, "day", rows)
	if len(got) != 3 {
		t.Fatalf("daily series length = %d, want 3", len(got))
	}
	if got[1].T != "2026-06-28T00:00:00Z" || got[1].Tokens != 500 || got[1].Requests != 9 {
		t.Errorf("mid bucket = %+v, want 2026-06-28 tokens=500 requests=9", got[1])
	}
	if got[0].Tokens != 0 || got[2].Tokens != 0 {
		t.Errorf("expected empty edge buckets, got %+v / %+v", got[0], got[2])
	}
}

func TestFoldCloudUsageModels_TopNAndOther(t *testing.T) {
	// Eight models, spend descending after sort. total = 1000 cents.
	rows := []map[string]interface{}{
		{"model": "m3", "provider": "do-ai", "cost_cents": float64(120), "tokens": float64(1200), "requests": float64(12)},
		{"model": "m1", "provider": "do-ai", "cost_cents": float64(500), "tokens": float64(5000), "requests": float64(50)},
		{"model": "m2", "provider": "fireworks", "cost_cents": float64(200), "tokens": float64(2000), "requests": float64(20)},
		{"model": "m4", "provider": "zen", "cost_cents": float64(80), "tokens": float64(800), "requests": float64(8)},
		{"model": "m5", "provider": "do-ai", "cost_cents": float64(50), "tokens": float64(500), "requests": float64(5)},
		{"model": "m6", "provider": "do-ai", "cost_cents": float64(30), "tokens": float64(300), "requests": float64(3)},
		{"model": "m7", "provider": "do-ai", "cost_cents": float64(15), "tokens": float64(150), "requests": float64(2)},
		{"model": "m8", "provider": "do-ai", "cost_cents": float64(5), "tokens": float64(50), "requests": float64(1)},
	}
	items, other := foldCloudUsageModels(rows, 6, 1000)

	if len(items) != 6 {
		t.Fatalf("items = %d, want 6", len(items))
	}
	if items[0].Model != "m1" || items[0].SpendCents != 500 {
		t.Errorf("top item = %+v, want m1/500", items[0])
	}
	if items[0].Pct != 50 {
		t.Errorf("top pct = %v, want 50", items[0].Pct)
	}
	// m1..m6 are top-6 by spend; m7(15)+m8(5)=20 fold into other.
	if other == nil {
		t.Fatalf("expected an 'other' bucket")
	}
	if other.ModelCount != 2 || other.SpendCents != 20 {
		t.Errorf("other = %+v, want modelCount=2 spend=20", other)
	}
	if other.Pct != 2 {
		t.Errorf("other pct = %v, want 2", other.Pct)
	}
}

func TestFoldCloudUsageModels_NoOtherWhenFew(t *testing.T) {
	rows := []map[string]interface{}{
		{"model": "a", "provider": "do-ai", "cost_cents": float64(60), "tokens": float64(1), "requests": float64(1)},
		{"model": "b", "provider": "do-ai", "cost_cents": float64(40), "tokens": float64(1), "requests": float64(1)},
	}
	items, other := foldCloudUsageModels(rows, 6, 100)
	if len(items) != 2 || other != nil {
		t.Fatalf("items=%d other=%v, want 2 items and no other", len(items), other)
	}
	if items[0].Model != "a" || items[0].Pct != 60 || items[1].Pct != 40 {
		t.Errorf("unexpected fold: %+v", items)
	}
}

func TestBuildCloudUsageOverview_EndToEnd(t *testing.T) {
	p := CloudUsageParams{
		RangeLabel:    "24h",
		Start:         mustTime(t, "2026-06-29T00:00:00Z"),
		End:           mustTime(t, "2026-06-29T03:00:00Z"),
		Interval:      "hour",
		Org:           "acme",
		AllOrgs:       false,
		TopModels:     6,
		ActivityType:  "all",
		ActivityLimit: 20,
	}
	totalsRow := map[string]interface{}{
		"requests": float64(10), "tokens": float64(1000), "prompt_tokens": float64(600),
		"completion_tokens": float64(400), "cost_cents": float64(250), "models": float64(3), "providers": float64(2),
	}
	priorRow := map[string]interface{}{
		"requests": float64(5), "tokens": float64(800), "cost_cents": float64(200), "models": float64(2),
	}
	seriesRows := []map[string]interface{}{
		{"bucket": "2026-06-29 00:00:00", "tokens": float64(400), "cost_cents": float64(100), "requests": float64(4), "models": float64(2)},
		{"bucket": "2026-06-29 02:00:00", "tokens": float64(600), "cost_cents": float64(150), "requests": float64(6), "models": float64(3)},
	}
	modelRows := []map[string]interface{}{
		{"model": "zen-omni", "provider": "do-ai", "cost_cents": float64(150), "tokens": float64(600), "requests": float64(6)},
		{"model": "gpt-x", "provider": "openai-direct", "cost_cents": float64(100), "tokens": float64(400), "requests": float64(4)},
	}
	activityRows := []map[string]interface{}{
		{
			"timestamp": "2026-06-29 02:30:00", "model": "zen-omni", "provider": "do-ai", "status": "success",
			"total_tokens": float64(120), "prompt_tokens": float64(80), "completion_tokens": float64(40),
			"cost_cents": float64(3), "is_stream": float64(1), "is_premium": float64(0),
			"request_id": "req-1", "user_id": "acme/alice", "organization": "acme",
		},
		{
			"timestamp": "2026-06-29 01:10:00", "model": "gpt-x", "provider": "openai-direct", "status": "success",
			"total_tokens": float64(90), "prompt_tokens": float64(50), "completion_tokens": float64(40),
			"cost_cents": float64(2), "is_stream": float64(0), "is_premium": float64(1),
			"request_id": "req-2", "user_id": "acme/bob", "organization": "acme",
		},
	}

	ov := buildCloudUsageOverview(p, totalsRow, priorRow, seriesRows, modelRows, activityRows, 42)

	// Echo + scope
	if ov.Range != "24h" || ov.Interval != "hour" {
		t.Errorf("range/interval = %q/%q", ov.Range, ov.Interval)
	}
	if ov.Start != "2026-06-29T00:00:00Z" || ov.End != "2026-06-29T03:00:00Z" {
		t.Errorf("window echo = %q..%q", ov.Start, ov.End)
	}
	if ov.Scope.Org != "acme" || ov.Scope.AllOrgs {
		t.Errorf("scope = %+v, want org=acme allOrgs=false", ov.Scope)
	}

	// Totals
	wantTotals := CloudUsageTotals{Tokens: 1000, PromptTokens: 600, CompletionTokens: 400, Requests: 10, SpendCents: 250, Models: 3, Providers: 2}
	if ov.Totals != wantTotals {
		t.Errorf("totals = %+v, want %+v", ov.Totals, wantTotals)
	}

	// Deltas
	assertPct := func(key string, want float64) {
		d, ok := ov.Deltas[key]
		if !ok || d.Pct == nil {
			t.Fatalf("delta %q missing or nil pct: %+v", key, d)
		}
		if *d.Pct != want {
			t.Errorf("delta %q pct = %v, want %v", key, *d.Pct, want)
		}
	}
	assertPct("tokens", 25)     // 1000 vs 800
	assertPct("spendCents", 25) // 250 vs 200
	assertPct("requests", 100)  // 10 vs 5
	assertPct("models", 50)     // 3 vs 2

	// Series gap-filled to 3 hourly points
	if len(ov.Series) != 3 {
		t.Fatalf("series len = %d, want 3", len(ov.Series))
	}
	if ov.Series[1].Tokens != 0 || ov.Series[0].Tokens != 400 || ov.Series[2].Tokens != 600 {
		t.Errorf("series tokens = [%d,%d,%d], want [400,0,600]", ov.Series[0].Tokens, ov.Series[1].Tokens, ov.Series[2].Tokens)
	}

	// By-model: 2 models, no other, pct against 250 total
	if ov.ByModel.TotalCents != 250 || len(ov.ByModel.Items) != 2 || ov.ByModel.Other != nil {
		t.Errorf("byModel = %+v", ov.ByModel)
	}
	if ov.ByModel.Items[0].Model != "zen-omni" || ov.ByModel.Items[0].Pct != 60 {
		t.Errorf("top model = %+v, want zen-omni 60%%", ov.ByModel.Items[0])
	}

	// Activity feed mapped (incl. stream/premium bools and types)
	if len(ov.Activity.Items) != 2 || ov.Activity.Total != 42 || ov.Activity.Type != "all" {
		t.Fatalf("activity = %+v", ov.Activity)
	}
	a0 := ov.Activity.Items[0]
	if a0.Model != "zen-omni" || a0.Type != "inference" || !a0.Stream || a0.Premium || a0.CostCents != 3 || a0.Time != "2026-06-29T02:30:00Z" {
		t.Errorf("activity[0] = %+v", a0)
	}
	if ov.Activity.Items[1].Premium != true || ov.Activity.Items[1].Stream != false {
		t.Errorf("activity[1] stream/premium = %v/%v, want false/true", ov.Activity.Items[1].Stream, ov.Activity.Items[1].Premium)
	}
}

func TestBuildCloudUsageOverview_AllOrgsScopeBlanksOrg(t *testing.T) {
	p := CloudUsageParams{
		RangeLabel: "7d", Start: mustTime(t, "2026-06-22T00:00:00Z"), End: mustTime(t, "2026-06-29T00:00:00Z"),
		Interval: "day", Org: "ignored-when-all", AllOrgs: true, TopModels: 6, ActivityType: "",
	}
	ov := buildCloudUsageOverview(p, map[string]interface{}{}, map[string]interface{}{}, nil, nil, nil, 0)
	if !ov.Scope.AllOrgs || ov.Scope.Org != "" {
		t.Errorf("all-orgs scope = %+v, want allOrgs=true org=''", ov.Scope)
	}
	if ov.Activity.Type != "all" {
		t.Errorf("empty activityType should normalize to 'all', got %q", ov.Activity.Type)
	}
	// Empty totals must be honest zeros, deltas present with nil pct (no prior basis).
	if ov.Totals.Tokens != 0 || ov.Deltas["tokens"].Pct != nil {
		t.Errorf("empty-store overview not honest: totals=%+v delta=%+v", ov.Totals, ov.Deltas["tokens"])
	}
}

func TestCloudUsageCoercion(t *testing.T) {
	if cuInt64(float64(42)) != 42 || cuInt64("42") != 42 || cuInt64(int64(42)) != 42 || cuInt64(nil) != 0 || cuInt64(uint64(7)) != 7 {
		t.Error("cuInt64 coercion wrong")
	}
	if cuInt64("1.9") != 1 { // float-ish string truncates
		t.Errorf("cuInt64 float string = %d, want 1", cuInt64("1.9"))
	}
	if !cuBool(float64(1)) || !cuBool("1") || !cuBool(true) || cuBool(float64(0)) || cuBool("0") || cuBool(nil) {
		t.Error("cuBool coercion wrong")
	}
	if cuString(nil) != "" || cuString("x") != "x" {
		t.Error("cuString coercion wrong")
	}
	if got := cuTime("2026-06-29 02:30:00"); got.Format(time.RFC3339) != "2026-06-29T02:30:00Z" {
		t.Errorf("cuTime datastore layout = %v", got)
	}
	if got := cuTime("2026-06-29T02:30:00Z"); got.Format(time.RFC3339) != "2026-06-29T02:30:00Z" {
		t.Errorf("cuTime rfc3339 = %v", got)
	}
}
