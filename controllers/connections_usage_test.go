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
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/ai/object"
)

// fakeUsageDoer routes a request to a per-path canned response so importUsage can be
// tested with no live provider account.
type fakeUsageDoer struct {
	byPath map[string]fakeResp
}

type fakeResp struct {
	status int
	body   string
}

func (f fakeUsageDoer) Do(req *http.Request) (*http.Response, error) {
	r, ok := f.byPath[req.URL.Path]
	if !ok {
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	}
	return &http.Response{StatusCode: r.status, Body: io.NopCloser(bytes.NewReader([]byte(r.body)))}, nil
}

func withFakeUsageHTTP(t *testing.T, doer usageHTTPDoer) {
	t.Helper()
	prev := providerUsageHTTP
	providerUsageHTTP = doer
	t.Cleanup(func() { providerUsageHTTP = prev })
}

func TestResolveProviderUsageWindow(t *testing.T) {
	now := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)

	// Default: last 30 days.
	from, to := resolveProviderUsageWindow("", "", now)
	if !to.Equal(now) {
		t.Fatalf("default to = %v, want now", to)
	}
	if d := to.Sub(from); d < 29*24*time.Hour || d > 31*24*time.Hour {
		t.Fatalf("default window = %v, want ~30d", d)
	}

	// Explicit YYYY-MM-DD bounds.
	from, to = resolveProviderUsageWindow("2026-01-01", "2026-01-15", now)
	if from.Format("2006-01-02") != "2026-01-01" || to.Format("2006-01-02") != "2026-01-15" {
		t.Fatalf("date bounds = %v..%v", from, to)
	}

	// Reversed bounds fall back to a sane 30-day window ending at `to`.
	from, to = resolveProviderUsageWindow("2026-03-01", "2026-01-01", now)
	if !from.Before(to) {
		t.Fatalf("reversed bounds not corrected: %v..%v", from, to)
	}

	// Over-long window is clamped to 90 days.
	from, to = resolveProviderUsageWindow("2020-01-01", "2026-01-01", now)
	if d := to.Sub(from); d > 90*24*time.Hour {
		t.Fatalf("window not clamped: %v", d)
	}
}

func TestParseUsageTimeUnixSeconds(t *testing.T) {
	def := time.Unix(0, 0).UTC()
	got := parseUsageTime("1735689600", def)
	if got.Unix() != 1735689600 {
		t.Fatalf("unix parse = %d", got.Unix())
	}
	if !parseUsageTime("nonsense", def).Equal(def) {
		t.Fatalf("bad input should fall back to default")
	}
}

func TestParseOpenAICostBuckets(t *testing.T) {
	body := `{"object":"page","data":[
	  {"start_time":1735689600,"results":[{"amount":{"value":0.42,"currency":"usd"}}]},
	  {"start_time":1735776000,"results":[{"amount":{"value":1.00,"currency":"usd"}},{"amount":{"value":0.50,"currency":"usd"}}]}
	],"has_more":false}`
	buckets := parseOpenAICostBuckets([]byte(body))
	if len(buckets) != 2 {
		t.Fatalf("buckets = %d, want 2", len(buckets))
	}
	if buckets[0].cents != 42 {
		t.Fatalf("bucket0 cents = %d, want 42", buckets[0].cents)
	}
	if buckets[1].cents != 150 { // (1.00 + 0.50) USD summed within the bucket
		t.Fatalf("bucket1 cents = %d, want 150", buckets[1].cents)
	}
	if parseOpenAICostBuckets([]byte("not json")) != nil {
		t.Fatalf("garbage should parse to nil")
	}
}

func TestParseOpenAIUsageRows(t *testing.T) {
	body := `{"data":[
	  {"start_time":1735689600,"results":[
	     {"input_tokens":1000,"output_tokens":200,"num_model_requests":3,"model":"gpt-4o"},
	     {"input_tokens":50,"output_tokens":10,"num_model_requests":1,"model":"gpt-4o-mini"}
	  ]},
	  {"start_time":1735776000,"results":[
	     {"input_tokens":500,"output_tokens":100,"num_model_requests":2,"model":"gpt-4o"}
	  ]}
	]}`
	rows := parseOpenAIUsageRows([]byte(body))
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	if rows[0].model != "gpt-4o" || rows[0].input != 1000 || rows[0].requests != 3 {
		t.Fatalf("row0 = %+v", rows[0])
	}
}

func TestOpenAIImporterHappyPath(t *testing.T) {
	costs := `{"data":[
	  {"start_time":1735689600,"results":[{"amount":{"value":0.42,"currency":"usd"}}]},
	  {"start_time":1735776000,"results":[{"amount":{"value":1.00,"currency":"usd"}}]}
	]}`
	usage := `{"data":[
	  {"start_time":1735689600,"results":[
	     {"input_tokens":1000,"output_tokens":200,"num_model_requests":3,"model":"gpt-4o"},
	     {"input_tokens":50,"output_tokens":10,"num_model_requests":1,"model":"gpt-4o-mini"}
	  ]},
	  {"start_time":1735776000,"results":[
	     {"input_tokens":500,"output_tokens":100,"num_model_requests":2,"model":"gpt-4o"}
	  ]}
	]}`
	withFakeUsageHTTP(t, fakeUsageDoer{byPath: map[string]fakeResp{
		"/v1/organization/costs":             {200, costs},
		"/v1/organization/usage/completions": {200, usage},
	}})

	imp := openaiUsageImporter{base: "https://api.openai.com/v1"}
	out, err := imp.importUsage(context.Background(), "sk-admin-x", time.Unix(1735689600, 0), time.Unix(1735862400, 0))
	if err != nil {
		t.Fatalf("importUsage err: %v", err)
	}
	if !out.Available {
		t.Fatalf("expected available; note=%q", out.Note)
	}
	if out.Totals.SpendCents != 142 {
		t.Fatalf("spend = %d, want 142", out.Totals.SpendCents)
	}
	if out.Totals.Tokens != 1860 || out.Totals.InputTokens != 1550 || out.Totals.OutputTokens != 310 {
		t.Fatalf("token totals = %+v", out.Totals)
	}
	if out.Totals.Requests != 6 {
		t.Fatalf("requests = %d, want 6", out.Totals.Requests)
	}
	if len(out.Series) != 2 {
		t.Fatalf("series = %d, want 2", len(out.Series))
	}
	if out.Series[0].SpendCents != 42 || out.Series[0].Tokens != 1260 || out.Series[0].Requests != 4 {
		t.Fatalf("series[0] = %+v", out.Series[0])
	}
	if len(out.ByModel) == 0 || out.ByModel[0].Model != "gpt-4o" || out.ByModel[0].Tokens != 1800 {
		t.Fatalf("byModel[0] = %+v", out.ByModel)
	}
}

func TestOpenAIImporterAdminKeyGap(t *testing.T) {
	withFakeUsageHTTP(t, fakeUsageDoer{byPath: map[string]fakeResp{
		"/v1/organization/costs":             {401, `{"error":{"message":"missing scope api.usage.read"}}`},
		"/v1/organization/usage/completions": {401, `{"error":{"message":"missing scope api.usage.read"}}`},
	}})
	imp := openaiUsageImporter{base: "https://api.openai.com/v1"}
	out, err := imp.importUsage(context.Background(), "sk-proj-normal", time.Now().AddDate(0, 0, -30), time.Now())
	if err != nil {
		t.Fatalf("auth-gap should not error: %v", err)
	}
	if out.Available {
		t.Fatalf("auth-gap should be unavailable")
	}
	if !strings.Contains(out.Note, "Admin API key") {
		t.Fatalf("note should name the admin-key requirement: %q", out.Note)
	}
}

func TestOpenAIImporterHonestEmpty(t *testing.T) {
	withFakeUsageHTTP(t, fakeUsageDoer{byPath: map[string]fakeResp{
		"/v1/organization/costs":             {200, `{"data":[]}`},
		"/v1/organization/usage/completions": {200, `{"data":[]}`},
	}})
	imp := openaiUsageImporter{base: "https://api.openai.com/v1"}
	out, _ := imp.importUsage(context.Background(), "sk-admin-x", time.Now().AddDate(0, 0, -30), time.Now())
	if out.Available {
		t.Fatalf("empty period should be unavailable")
	}
	if !strings.Contains(out.Note, "no usage") {
		t.Fatalf("note should say no usage: %q", out.Note)
	}
	if out.Totals.SpendCents != 0 || out.Totals.Tokens != 0 {
		t.Fatalf("empty should have zero totals: %+v", out.Totals)
	}
}

func TestParseAnthropicUsageRows(t *testing.T) {
	body := `{"data":[
	  {"starting_at":"2026-01-01T00:00:00Z","results":[
	     {"uncached_input_tokens":100,"cache_read_input_tokens":20,"output_tokens":40,"model":"claude-opus-4-8"}
	  ]}
	]}`
	rows := parseAnthropicUsageRows([]byte(body))
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].input != 120 || rows[0].output != 40 || rows[0].model != "claude-opus-4-8" {
		t.Fatalf("row = %+v", rows[0])
	}
	if rows[0].start != parseRFC3339Unix("2026-01-01T00:00:00Z") {
		t.Fatalf("start not parsed")
	}
}

func TestAnthropicImporterTokensImportedSpendGap(t *testing.T) {
	body := `{"data":[{"starting_at":"2026-01-01T00:00:00Z","results":[
	  {"uncached_input_tokens":100,"output_tokens":40,"model":"claude-opus-4-8"}
	]}]}`
	withFakeUsageHTTP(t, fakeUsageDoer{byPath: map[string]fakeResp{
		"/v1/organizations/usage_report/messages": {200, body},
	}})
	imp := anthropicUsageImporter{base: "https://api.anthropic.com"}
	out, err := imp.importUsage(context.Background(), "sk-ant-admin-x", time.Now().AddDate(0, 0, -7), time.Now())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !out.Available || out.Totals.Tokens != 140 {
		t.Fatalf("expected 140 tokens available: %+v", out)
	}
	if out.Totals.SpendCents != 0 || !strings.Contains(out.Note, "cost report") {
		t.Fatalf("spend gap should be honest: %+v", out)
	}
}

func TestGoogleImporterHonestGap(t *testing.T) {
	out, err := googleUsageImporter{}.importUsage(context.Background(), "AIza-x", time.Now().AddDate(0, 0, -7), time.Now())
	if err != nil || out.Available {
		t.Fatalf("google should be honest-unavailable: %+v err=%v", out, err)
	}
	if !strings.Contains(out.Note, "Google Cloud Billing") {
		t.Fatalf("note should explain the gap: %q", out.Note)
	}
}

func TestProviderUsageImporterRegistry(t *testing.T) {
	if providerUsageImporterFor("OpenAI") == nil || providerUsageImporterFor("openai").provider() != "openai" {
		t.Fatalf("openai importer not resolved")
	}
	if providerUsageImporterFor("anthropic") == nil || providerUsageImporterFor("google") == nil {
		t.Fatalf("anthropic/google importers not registered")
	}
	if providerUsageImporterFor("mystery") != nil {
		t.Fatalf("unknown provider should resolve nil")
	}
}

func TestUnsealConnectionKeyCopiesAndRefusesRef(t *testing.T) {
	// A plaintext key (dev / no KMS) is returned verbatim; the source row is not mutated.
	row := &object.Provider{Name: "openai", ClientSecret: "sk-plain-123"}
	got, err := unsealConnectionKey(row)
	if err != nil || got != "sk-plain-123" {
		t.Fatalf("plaintext key = %q err=%v", got, err)
	}
	if row.ClientSecret != "sk-plain-123" {
		t.Fatalf("source row mutated: %q", row.ClientSecret)
	}

	// An unresolved kms:// reference (KMS disabled) is NEVER emitted upstream.
	ref := &object.Provider{Name: "openai", ClientSecret: "kms://byok_org_openai"}
	got, err = unsealConnectionKey(ref)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "" {
		t.Fatalf("kms ref should resolve to empty, got %q", got)
	}
	if ref.ClientSecret != "kms://byok_org_openai" {
		t.Fatalf("source ref mutated: %q", ref.ClientSecret)
	}

	if _, err := unsealConnectionKey(nil); err == nil {
		t.Fatalf("nil row should error")
	}
}
