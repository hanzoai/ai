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
// connections_usage.go — the IMPORT half of the AI login-manager. For a connected
// third-party account (OpenAI, Anthropic, Google, OpenRouter, DeepSeek, Groq) it UNSEALS the org's KMS-sealed
// key SERVER-SIDE (the key never touches the browser), calls THAT provider's own
// usage/cost API, and normalizes the answer into one small ProviderUsage shape
// (spend, tokens, requests, per-model, day-bucketed time series) — so the console can
// show a customer's third-party spend RIGHT NEXT TO their native Hanzo usage.
//
// Design mirrors connections_api.go / cloud_usage.go: the HTTP handler is thin
// orchestration; the per-provider fetch is behind a providerUsageImporter interface
// (OpenAI/Anthropic/OpenRouter are real importers; Google/DeepSeek/Groq are honest gaps);
// and the JSON→ProviderUsage mapping is PURE functions the tests drive with sample
// payloads — so the two hard invariants (the raw key never leaves the server; an
// empty/scope-denied provider answer degrades to an HONEST empty state, never a
// fabricated number) are provable without a live KMS or a live provider account.
//
// SEAL/UNSEAL: unsealConnectionKey resolves on a COPY of the row so the resolved key
// can never leak back into the cached provider row, and refuses to emit a bare
// "kms://…" reference upstream. The connection row is org-scoped (org+"/"+name), so a
// tenant only ever imports its OWN account's usage.

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
)

// ── the normalized shape (wire-mirrored by @hanzo/usage provider-usage.ts) ──────────

// ProviderUsage is the ONE normalized third-party-usage value. Money is USD cents
// end-to-end (matching CloudUsageOverview.spendCents), so the unified panel renders a
// connected provider with the SAME vocabulary as native Hanzo usage. `connected` is
// false when the org has no active connection; `available` is false (with a human
// `note`) when the provider API returned nothing or the key lacked the usage scope —
// the two distinct honest-empty states the UI shows instead of a fabricated zero.
type ProviderUsage struct {
	Provider  string                     `json:"provider"`
	Connected bool                       `json:"connected"`
	Available bool                       `json:"available"`
	Note      string                     `json:"note,omitempty"`
	Currency  string                     `json:"currency"`
	Start     string                     `json:"start"`
	End       string                     `json:"end"`
	Interval  string                     `json:"interval"`
	Totals    ProviderUsageTotals        `json:"totals"`
	Series    []ProviderUsageSeriesPoint `json:"series"`
	ByModel   []ProviderUsageModelSpend  `json:"byModel"`
}

// ProviderUsageTotals are the window totals — the metric cards.
type ProviderUsageTotals struct {
	SpendCents   int64 `json:"spendCents"`
	Tokens       int64 `json:"tokens"`
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
	Requests     int64 `json:"requests"`
}

// ProviderUsageSeriesPoint is one day-bucket of the ascending time series.
type ProviderUsageSeriesPoint struct {
	T          string `json:"t"` // RFC3339 bucket start (UTC)
	SpendCents int64  `json:"spendCents"`
	Tokens     int64  `json:"tokens"`
	Requests   int64  `json:"requests"`
}

// ProviderUsageModelSpend is one model's slice of the window (spend-by-model). Spend
// is 0 for providers whose per-model cost is not exposed by their usage API (honest,
// not fabricated) — tokens/requests are still the real per-model figures.
type ProviderUsageModelSpend struct {
	Model      string `json:"model"`
	SpendCents int64  `json:"spendCents"`
	Tokens     int64  `json:"tokens"`
	Requests   int64  `json:"requests"`
}

// ── the importer interface (OpenAI reference; Anthropic/Google slot in the same way) ─

// providerUsageImporter fetches + normalizes one provider's usage for [from,to] using
// the org's unsealed API key. It returns available=false with a note (NOT an error)
// when the provider API returns nothing or the key lacks the usage scope; it returns
// an error only for a genuine transport failure the handler should surface as
// "unavailable". The returned ProviderUsage carries Totals/Series/ByModel/Available/
// Note/Currency; the handler owns Provider/Connected and the window fields.
type providerUsageImporter interface {
	provider() string
	importUsage(ctx context.Context, apiKey string, from, to time.Time) (ProviderUsage, error)
}

// providerUsageImporters is the closed registry: adding a provider = implement the
// interface + register it here (one place). Every key MUST have a matching aiConnSpecs
// entry (asserted in the tests) so a connectable account and its importer stay in
// lockstep. Real importers hit the provider's own usage/cost API; honest-gap importers
// (google/deepseek/groq) return available=false with a precise note — never a fake zero.
var providerUsageImporters = map[string]providerUsageImporter{
	"openai":     openaiUsageImporter{base: "https://api.openai.com/v1"},
	"anthropic":  anthropicUsageImporter{base: "https://api.anthropic.com"},
	"google":     googleUsageImporter{},
	"openrouter": openrouterUsageImporter{base: "https://openrouter.ai/api/v1"},
	"deepseek":   deepseekUsageImporter{},
	"groq":       groqUsageImporter{},
}

func providerUsageImporterFor(provider string) providerUsageImporter {
	return providerUsageImporters[strings.ToLower(strings.TrimSpace(provider))]
}

// ── shared transport (a bounded, swappable HTTP doer so tests never hit the network) ─

type usageHTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// providerUsageHTTP bounds every provider usage round-trip. A package var so tests can
// substitute a fake doer (no live provider account required).
var providerUsageHTTP usageHTTPDoer = &http.Client{Timeout: 20 * time.Second}

// providerUsageGet issues an authorized JSON GET and returns the body + status. Auth
// headers are passed by the caller (Authorization: Bearer for OpenAI, x-api-key for
// Anthropic) so a key is only ever a request header, never logged or echoed.
func providerUsageGet(ctx context.Context, rawURL string, headers map[string]string) (body []byte, status int, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := providerUsageHTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<21)) // 2 MiB cap
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return b, resp.StatusCode, nil
}

func isAuthStatus(s int) bool { return s == http.StatusUnauthorized || s == http.StatusForbidden }

// ── the handler ─────────────────────────────────────────────────────────────────────

// GetAIConnectionUsage imports the caller org's usage for a connected third-party
// account. The org is resolved from the VERIFIED principal (requireConnectionOrg), so a
// tenant reads only its own connection. The key is unsealed SERVER-SIDE and never
// returned. An unconnected account, a missing importer, or a scope-denied provider all
// return a 200 ProviderUsage with connected/available flags + a human note — the UI's
// honest-empty states — never a fabricated figure.
// @Title GetAIConnectionUsage
// @Tag AI Connections API
// @Description import a connected third-party AI account's usage (spend/tokens/requests/per-model/series)
// @Param provider path string true "provider slug (see GET /v1/ai/connections)"
// @Param from query string false "window start (RFC3339, YYYY-MM-DD, or unix seconds; default 30d ago)"
// @Param to query string false "window end (RFC3339, YYYY-MM-DD, or unix seconds; default now)"
// @Success 200 {object} controllers.ProviderUsage The Response object
// @router /ai/connections/:provider/usage [get]
func (c *ApiController) GetAIConnectionUsage() {
	org, ok := c.requireConnectionOrg()
	if !ok {
		return
	}
	spec, ok := aiConnSpecFor(c.Param("provider"))
	if !ok {
		c.ResponseError(c.T("openai:provider must be one of") + ": " + aiConnProviderList())
		return
	}

	from, to := resolveProviderUsageWindow(c.Input().Get("from"), c.Input().Get("to"), time.Now())
	out := ProviderUsage{
		Provider: spec.name,
		Currency: "usd",
		Start:    from.UTC().Format(time.RFC3339),
		End:      to.UTC().Format(time.RFC3339),
		Interval: "day",
	}

	row, err := object.GetProvider(org + "/" + spec.name)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	// Not connected → honest empty (the org reverted to Hanzo-served or never linked).
	if !object.ModelProviderUsable(row) || !object.ProviderKeyPresent(row) {
		out.Note = "Connect your " + spec.label + " account to import its usage."
		c.ResponseOk(out)
		return
	}
	out.Connected = true

	key, err := unsealConnectionKey(row)
	if err != nil {
		c.ResponseErrorWithStatus(http.StatusServiceUnavailable, "could not access the connected key")
		return
	}
	if strings.TrimSpace(key) == "" {
		// The row is active but the sealed key can't be resolved on this deployment
		// (KMS unconfigured / secret tombstoned). Honest, not a crash.
		out.Available = false
		out.Note = "The connected key can't be read on this deployment yet."
		c.ResponseOk(out)
		return
	}

	imp := providerUsageImporterFor(spec.name)
	if imp == nil {
		out.Available = false
		out.Note = "Usage import for " + spec.label + " is coming soon."
		c.ResponseOk(out)
		return
	}

	ctx, cancel := context.WithTimeout(c.Context(), 25*time.Second)
	defer cancel()
	res, err := imp.importUsage(ctx, key, from, to)
	if err != nil {
		// A genuine upstream failure → honest "unavailable" (typed error envelope), so the
		// UI renders a retry state rather than fabricated zeros.
		c.ResponseError(err.Error())
		return
	}
	out.Available = res.Available
	out.Note = res.Note
	if res.Currency != "" {
		out.Currency = res.Currency
	}
	out.Totals = res.Totals
	out.Series = res.Series
	out.ByModel = res.ByModel
	c.ResponseOk(out)
}

// unsealConnectionKey resolves the org's sealed provider key to its raw value on a COPY
// of the row — so the resolved secret can NEVER leak into the cached provider row that
// GetProvider may return. It refuses to emit a bare "kms://…" reference (an unresolved
// key on a KMS-less deployment) upstream, returning "" so the caller renders an honest
// empty state instead of sending a reference to a provider API.
func unsealConnectionKey(row *object.Provider) (string, error) {
	if row == nil {
		return "", fmt.Errorf("no connection")
	}
	cp := *row
	if err := object.ResolveProviderSecret(&cp); err != nil {
		// A reference we cannot unseal is, to this caller, the same fact as no
		// connection: there is no key to import usage with. It renders the honest
		// empty state ("connected, unavailable") rather than a 500 or — far worse
		// — a bare reference sent to a provider API. Logged because a store that
		// cannot resolve a sealed key is an operator problem even when the UI
		// degrades gracefully.
		log.Warn("connections: could not unseal provider key", "provider", row.Name, "error", err)
		return "", nil
	}
	key := strings.TrimSpace(cp.ClientSecret)
	if key == "" || strings.HasPrefix(key, "kms://") {
		return "", nil
	}
	return key, nil
}

// resolveProviderUsageWindow parses the ?from/?to params (RFC3339, YYYY-MM-DD, or unix
// seconds) into a sane window: default last 30 days, clamped to at most 90 days, from
// strictly before to.
func resolveProviderUsageWindow(fromRaw, toRaw string, now time.Time) (from, to time.Time) {
	to = parseUsageTime(toRaw, now)
	from = parseUsageTime(fromRaw, to.AddDate(0, 0, -30))
	if !from.Before(to) {
		from = to.AddDate(0, 0, -30)
	}
	if to.Sub(from) > 90*24*time.Hour {
		from = to.AddDate(0, 0, -90)
	}
	return from, to
}

func parseUsageTime(raw string, def time.Time) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return t.UTC()
	}
	if sec, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return time.Unix(sec, 0).UTC()
	}
	return def
}

// ── shared normalization helpers (pure) ─────────────────────────────────────────────

func seriesPointAt(m map[int64]*ProviderUsageSeriesPoint, start int64) *ProviderUsageSeriesPoint {
	p := m[start]
	if p == nil {
		p = &ProviderUsageSeriesPoint{T: time.Unix(start, 0).UTC().Format(time.RFC3339)}
		m[start] = p
	}
	return p
}

func sortedSeries(m map[int64]*ProviderUsageSeriesPoint) []ProviderUsageSeriesPoint {
	keys := make([]int64, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	out := make([]ProviderUsageSeriesPoint, 0, len(keys))
	for _, k := range keys {
		out = append(out, *m[k])
	}
	return out
}

func modelSpendAt(m map[string]*ProviderUsageModelSpend, model string) *ProviderUsageModelSpend {
	v := m[model]
	if v == nil {
		v = &ProviderUsageModelSpend{Model: model}
		m[model] = v
	}
	return v
}

func sortedModelSpend(m map[string]*ProviderUsageModelSpend) []ProviderUsageModelSpend {
	out := make([]ProviderUsageModelSpend, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SpendCents != out[j].SpendCents {
			return out[i].SpendCents > out[j].SpendCents
		}
		if out[i].Tokens != out[j].Tokens {
			return out[i].Tokens > out[j].Tokens
		}
		return out[i].Model < out[j].Model
	})
	return out
}

func dollarsToCents(v float64) int64 { return int64(math.Round(v * 100)) }

func parseRFC3339Unix(s string) int64 {
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(s)); err == nil {
		return t.Unix()
	}
	return 0
}

// ── OpenAI importer (the reference) ─────────────────────────────────────────────────
//
// OpenAI's org-level Usage + Costs APIs are the reference implementation:
//   - GET {base}/organization/costs?start_time&end_time&bucket_width=1d           → USD spend per day-bucket
//   - GET {base}/organization/usage/completions?…&group_by=model                  → tokens + requests per day-bucket per model
//
// IMPORTANT (honest gap, surfaced in the note): these org endpoints require an ADMIN
// API key (sk-admin-…) carrying the `api.usage.read` scope — a normal project key
// (sk-…/sk-proj-…) is 401'd. When both calls are auth-blocked the importer returns
// available=false with a note telling the customer to reconnect with an admin key,
// rather than a fabricated zero. Per-model SPEND is not exposed by the costs API, so
// byModel carries real tokens/requests with spend 0 (honest); window spend comes from
// the costs API.

type openaiUsageImporter struct{ base string }

func (openaiUsageImporter) provider() string { return "openai" }

func (o openaiUsageImporter) importUsage(ctx context.Context, apiKey string, from, to time.Time) (ProviderUsage, error) {
	limit := windowDayLimit(from, to, 180)
	hdr := map[string]string{"Authorization": "Bearer " + apiKey}
	build := func(path string, extra url.Values) string {
		v := url.Values{}
		v.Set("start_time", strconv.FormatInt(from.Unix(), 10))
		v.Set("end_time", strconv.FormatInt(to.Unix(), 10))
		v.Set("bucket_width", "1d")
		v.Set("limit", strconv.Itoa(limit))
		for k, vals := range extra {
			for _, vv := range vals {
				v.Add(k, vv)
			}
		}
		return strings.TrimRight(o.base, "/") + path + "?" + v.Encode()
	}

	costsBody, costsStatus, costsErr := providerUsageGet(ctx, build("/organization/costs", nil), hdr)
	usageExtra := url.Values{}
	usageExtra.Add("group_by", "model")
	usageBody, usageStatus, usageErr := providerUsageGet(ctx, build("/organization/usage/completions", usageExtra), hdr)

	// Both round-trips failed at the transport level → surface as an error (retry state).
	if costsErr != nil && usageErr != nil {
		return ProviderUsage{}, fmt.Errorf("openai usage request failed: %v", usageErr)
	}

	out := ProviderUsage{Currency: "usd"}
	// Both endpoints are scope-gated by the same admin scope, so both auth-blocking means
	// the key simply lacks it — an honest, actionable empty state, not an error.
	if isAuthStatus(costsStatus) && isAuthStatus(usageStatus) {
		out.Note = "OpenAI usage import needs an Admin API key (sk-admin-…) with the api.usage.read scope. Reconnect OpenAI with an admin key to import spend."
		return out, nil
	}

	series := map[int64]*ProviderUsageSeriesPoint{}
	if costsErr == nil && costsStatus/100 == 2 {
		for _, b := range parseOpenAICostBuckets(costsBody) {
			p := seriesPointAt(series, b.start)
			p.SpendCents += b.cents
			out.Totals.SpendCents += b.cents
		}
	}
	models := map[string]*ProviderUsageModelSpend{}
	if usageErr == nil && usageStatus/100 == 2 {
		for _, r := range parseOpenAIUsageRows(usageBody) {
			tok := r.input + r.output
			p := seriesPointAt(series, r.start)
			p.Tokens += tok
			p.Requests += r.requests
			out.Totals.Tokens += tok
			out.Totals.InputTokens += r.input
			out.Totals.OutputTokens += r.output
			out.Totals.Requests += r.requests
			if r.model != "" {
				m := modelSpendAt(models, r.model)
				m.Tokens += tok
				m.Requests += r.requests
			}
		}
	}
	out.Series = sortedSeries(series)
	out.ByModel = sortedModelSpend(models)
	out.Available = len(out.Series) > 0 || out.Totals.Tokens > 0 || out.Totals.SpendCents > 0
	if !out.Available && out.Note == "" {
		out.Note = "OpenAI reported no usage for this period."
	}
	return out, nil
}

type openaiCostBucket struct {
	start int64
	cents int64
}

// parseOpenAICostBuckets folds the OpenAI costs page into per-day-bucket USD cents.
func parseOpenAICostBuckets(body []byte) []openaiCostBucket {
	var page struct {
		Data []struct {
			StartTime int64 `json:"start_time"`
			Results   []struct {
				Amount struct {
					Value    float64 `json:"value"`
					Currency string  `json:"currency"`
				} `json:"amount"`
			} `json:"results"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &page) != nil {
		return nil
	}
	out := make([]openaiCostBucket, 0, len(page.Data))
	for _, b := range page.Data {
		var cents int64
		for _, r := range b.Results {
			cents += dollarsToCents(r.Amount.Value)
		}
		out = append(out, openaiCostBucket{start: b.StartTime, cents: cents})
	}
	return out
}

type openaiUsageRow struct {
	start    int64
	input    int64
	output   int64
	requests int64
	model    string
}

// parseOpenAIUsageRows flattens the OpenAI usage/completions page into per-bucket,
// per-model rows.
func parseOpenAIUsageRows(body []byte) []openaiUsageRow {
	var page struct {
		Data []struct {
			StartTime int64 `json:"start_time"`
			Results   []struct {
				InputTokens      int64  `json:"input_tokens"`
				OutputTokens     int64  `json:"output_tokens"`
				NumModelRequests int64  `json:"num_model_requests"`
				Model            string `json:"model"`
			} `json:"results"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &page) != nil {
		return nil
	}
	var out []openaiUsageRow
	for _, b := range page.Data {
		for _, r := range b.Results {
			out = append(out, openaiUsageRow{
				start:    b.StartTime,
				input:    r.InputTokens,
				output:   r.OutputTokens,
				requests: r.NumModelRequests,
				model:    r.Model,
			})
		}
	}
	return out
}

// ── Anthropic importer (slots into the same interface) ──────────────────────────────
//
// Anthropic's Admin Usage & Cost API mirrors OpenAI's shape:
//   - GET {base}/v1/organizations/usage_report/messages?starting_at&ending_at&bucket_width=1d&group_by[]=model
// It also requires an ADMIN key (sk-ant-admin-…). Token usage is imported; per-request
// counts and per-token spend are NOT in the messages usage report (the cost report is a
// separate admin call), so requests/spend are left 0 with an honest note — never
// fabricated. The parse is defensive: an unexpected shape degrades to an honest empty
// state rather than a crash.

type anthropicUsageImporter struct{ base string }

func (anthropicUsageImporter) provider() string { return "anthropic" }

func (a anthropicUsageImporter) importUsage(ctx context.Context, apiKey string, from, to time.Time) (ProviderUsage, error) {
	v := url.Values{}
	v.Set("starting_at", from.UTC().Format(time.RFC3339))
	v.Set("ending_at", to.UTC().Format(time.RFC3339))
	v.Set("bucket_width", "1d")
	v.Add("group_by[]", "model")
	rawURL := strings.TrimRight(a.base, "/") + "/v1/organizations/usage_report/messages?" + v.Encode()
	hdr := map[string]string{"x-api-key": apiKey, "anthropic-version": "2023-06-01"}

	body, status, err := providerUsageGet(ctx, rawURL, hdr)
	if err != nil {
		return ProviderUsage{}, fmt.Errorf("anthropic usage request failed: %w", err)
	}
	out := ProviderUsage{Currency: "usd"}
	if isAuthStatus(status) {
		out.Note = "Anthropic usage import needs an Admin API key (sk-ant-admin-…). Reconnect Anthropic with an admin key to import usage."
		return out, nil
	}
	if status/100 != 2 {
		out.Note = "Anthropic usage is unavailable right now."
		return out, nil
	}

	series := map[int64]*ProviderUsageSeriesPoint{}
	models := map[string]*ProviderUsageModelSpend{}
	for _, r := range parseAnthropicUsageRows(body) {
		tok := r.input + r.output
		p := seriesPointAt(series, r.start)
		p.Tokens += tok
		out.Totals.Tokens += tok
		out.Totals.InputTokens += r.input
		out.Totals.OutputTokens += r.output
		if r.model != "" {
			modelSpendAt(models, r.model).Tokens += tok
		}
	}
	out.Series = sortedSeries(series)
	out.ByModel = sortedModelSpend(models)
	out.Available = len(out.Series) > 0 || out.Totals.Tokens > 0
	if !out.Available {
		out.Note = "Anthropic reported no usage for this period."
	} else {
		out.Note = "Token usage imported. Spend needs the Anthropic cost report (coming soon)."
	}
	return out, nil
}

type anthropicUsageRow struct {
	start  int64
	input  int64
	output int64
	model  string
}

// parseAnthropicUsageRows flattens the Anthropic messages usage report into per-bucket,
// per-model token rows, tolerating the several input-token fields the report splits
// across (uncached + cache-creation + cache-read, or a flat input_tokens).
func parseAnthropicUsageRows(body []byte) []anthropicUsageRow {
	var page struct {
		Data []struct {
			StartingAt string `json:"starting_at"`
			Results    []struct {
				UncachedInputTokens      int64  `json:"uncached_input_tokens"`
				CacheCreationInputTokens int64  `json:"cache_creation_input_tokens"`
				CacheReadInputTokens     int64  `json:"cache_read_input_tokens"`
				InputTokens              int64  `json:"input_tokens"`
				OutputTokens             int64  `json:"output_tokens"`
				Model                    string `json:"model"`
			} `json:"results"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &page) != nil {
		return nil
	}
	var out []anthropicUsageRow
	for _, b := range page.Data {
		start := parseRFC3339Unix(b.StartingAt)
		for _, r := range b.Results {
			input := r.UncachedInputTokens + r.CacheCreationInputTokens + r.CacheReadInputTokens + r.InputTokens
			out = append(out, anthropicUsageRow{start: start, input: input, output: r.OutputTokens, model: r.Model})
		}
	}
	return out
}

// ── Google importer (honest gap) ────────────────────────────────────────────────────
//
// Google Gemini exposes no per-API-key usage or cost endpoint — spend is reported only
// in Google Cloud Billing (out of band). So the Google importer is honest: connected,
// but usage import is not available. Registered so the interface is uniform and the gap
// is explicit rather than a missing case.

type googleUsageImporter struct{}

func (googleUsageImporter) provider() string { return "google" }

func (googleUsageImporter) importUsage(_ context.Context, _ string, _, _ time.Time) (ProviderUsage, error) {
	return ProviderUsage{
		Currency:  "usd",
		Available: false,
		Note:      "Google Gemini has no per-API-key usage or cost API — spend is reported in Google Cloud Billing. Usage import isn't available for Google yet.",
	}, nil
}

// ── OpenRouter importer (real; credits + activity) ──────────────────────────────────
//
// OpenRouter meters in USD credits (1 credit = 1 USD). Two endpoints, both authorized
// by a normal Bearer key:
//   - GET {base}/activity → per-DAY, per-MODEL rows {date, model, usage(USD), requests,
//     prompt_tokens, completion_tokens}. Best fidelity; filtered to [from,to] and
//     authoritative when readable (an empty window is a real "no usage", not a gap).
//   - GET {base}/credits  → {data:{total_credits, total_usage}} — LIFETIME USD totals.
//
// activity is analytics-scoped, so a key without that scope degrades to the credits
// LIFETIME total as ONE aggregate row WITH an explicit all-time note (never a fabricated
// per-day split). An idle account is an honest empty; both endpoints failing is an error.

type openrouterUsageImporter struct{ base string }

func (openrouterUsageImporter) provider() string { return "openrouter" }

func (o openrouterUsageImporter) importUsage(ctx context.Context, apiKey string, from, to time.Time) (ProviderUsage, error) {
	hdr := map[string]string{"Authorization": "Bearer " + apiKey}
	root := strings.TrimRight(o.base, "/")
	out := ProviderUsage{Currency: "usd"}

	// Preferred: per-day, per-model activity, filtered to the window. When this read
	// succeeds it is authoritative — an empty window means no usage, not "try credits".
	actBody, actStatus, actErr := providerUsageGet(ctx, root+"/activity", hdr)
	if actErr == nil && actStatus/100 == 2 {
		fromDay, toUnix := dayStartUnix(from), to.Unix()
		series := map[int64]*ProviderUsageSeriesPoint{}
		models := map[string]*ProviderUsageModelSpend{}
		for _, r := range parseOpenRouterActivityRows(actBody) {
			if r.start < fromDay || r.start > toUnix {
				continue
			}
			tok := r.input + r.output
			p := seriesPointAt(series, r.start)
			p.SpendCents += r.cents
			p.Tokens += tok
			p.Requests += r.requests
			out.Totals.SpendCents += r.cents
			out.Totals.Tokens += tok
			out.Totals.InputTokens += r.input
			out.Totals.OutputTokens += r.output
			out.Totals.Requests += r.requests
			if r.model != "" {
				m := modelSpendAt(models, r.model)
				m.SpendCents += r.cents
				m.Tokens += tok
				m.Requests += r.requests
			}
		}
		out.Series = sortedSeries(series)
		out.ByModel = sortedModelSpend(models)
		out.Available = len(out.Series) > 0 || out.Totals.SpendCents > 0 || out.Totals.Tokens > 0
		if !out.Available {
			out.Note = "OpenRouter reported no usage for this period."
		}
		return out, nil
	}

	// Fallback: activity is unreadable (no analytics scope) → lifetime credits aggregate.
	crBody, crStatus, crErr := providerUsageGet(ctx, root+"/credits", hdr)
	if actErr != nil && crErr != nil {
		return ProviderUsage{}, fmt.Errorf("openrouter usage request failed: %v", actErr)
	}
	if crErr == nil && crStatus/100 == 2 {
		if total, ok := parseOpenRouterCreditsUsageCents(crBody); ok && total > 0 {
			out.Totals.SpendCents = total
			out.Available = true
			out.Note = "Showing your all-time OpenRouter spend — OpenRouter's per-key credits endpoint has no date-window breakdown. Connect a key with analytics access to import daily, per-model usage."
			return out, nil
		}
		out.Note = "OpenRouter reported no usage yet."
		return out, nil
	}

	// Neither endpoint was usable — distinguish a scope/auth block from a transient one.
	if isAuthStatus(actStatus) || isAuthStatus(crStatus) {
		out.Note = "OpenRouter didn't authorize usage import for this key."
	} else {
		out.Note = "OpenRouter usage is unavailable right now."
	}
	return out, nil
}

type openrouterActivityRow struct {
	start    int64 // day bucket (UTC midnight unix)
	cents    int64 // USD spend → cents
	input    int64 // prompt tokens
	output   int64 // completion tokens
	requests int64
	model    string
}

// parseOpenRouterActivityRows flattens the OpenRouter activity page into per-day,
// per-model rows. `usage` is USD; counts are decoded as float64 then cast so an
// int-or-float encoding of tokens/requests can't drop the whole page.
func parseOpenRouterActivityRows(body []byte) []openrouterActivityRow {
	var page struct {
		Data []struct {
			Date             string  `json:"date"`
			Model            string  `json:"model"`
			Usage            float64 `json:"usage"`
			Requests         float64 `json:"requests"`
			PromptTokens     float64 `json:"prompt_tokens"`
			CompletionTokens float64 `json:"completion_tokens"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &page) != nil {
		return nil
	}
	out := make([]openrouterActivityRow, 0, len(page.Data))
	for _, r := range page.Data {
		out = append(out, openrouterActivityRow{
			start:    dayUnix(r.Date),
			cents:    dollarsToCents(r.Usage),
			input:    int64(r.PromptTokens),
			output:   int64(r.CompletionTokens),
			requests: int64(r.Requests),
			model:    r.Model,
		})
	}
	return out
}

// parseOpenRouterCreditsUsageCents extracts LIFETIME USD usage (→ cents) from the
// credits payload {data:{total_credits, total_usage}}. ok is false only on a bad body.
func parseOpenRouterCreditsUsageCents(body []byte) (int64, bool) {
	var page struct {
		Data struct {
			TotalCredits float64 `json:"total_credits"`
			TotalUsage   float64 `json:"total_usage"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &page) != nil {
		return 0, false
	}
	return dollarsToCents(page.Data.TotalUsage), true
}

// dayStartUnix truncates t to UTC midnight — the window's lower bound so the from-day's
// (midnight-dated) activity row is included.
func dayStartUnix(t time.Time) int64 {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC).Unix()
}

// dayUnix parses a YYYY-MM-DD activity date to its UTC midnight unix bucket.
func dayUnix(date string) int64 {
	if t, err := time.Parse("2006-01-02", strings.TrimSpace(date)); err == nil {
		return t.UTC().Unix()
	}
	return 0
}

// ── DeepSeek importer (honest gap) ──────────────────────────────────────────────────
//
// DeepSeek's only account endpoint is GET /user/balance (remaining balance — granted +
// topped-up), NOT spend, and there is no per-key usage or cost API. So the importer is
// honest: connected, but usage import is unavailable (balance is a different metric).

type deepseekUsageImporter struct{}

func (deepseekUsageImporter) provider() string { return "deepseek" }

func (deepseekUsageImporter) importUsage(_ context.Context, _ string, _, _ time.Time) (ProviderUsage, error) {
	return ProviderUsage{
		Currency:  "usd",
		Available: false,
		Note:      "DeepSeek exposes account balance (GET /user/balance), not per-key usage — there is no usage or cost API to import. Track DeepSeek spend in the DeepSeek platform.",
	}, nil
}

// ── Groq importer (honest gap) ──────────────────────────────────────────────────────
//
// Groq bills per token but exposes no per-key usage or cost API — spend limits are
// org-wide and usage is visible only in the Groq console. So the importer is honest:
// connected, but usage import is unavailable.

type groqUsageImporter struct{}

func (groqUsageImporter) provider() string { return "groq" }

func (groqUsageImporter) importUsage(_ context.Context, _ string, _, _ time.Time) (ProviderUsage, error) {
	return ProviderUsage{
		Currency:  "usd",
		Available: false,
		Note:      "Groq has no per-API-key usage or cost API — usage is reported only in the Groq console. Usage import isn't available for Groq yet.",
	}, nil
}

// windowDayLimit is the number of 1-day buckets a [from,to] window spans, +1 for the
// partial edge, capped. Used as the provider `limit` so a single page covers the window.
func windowDayLimit(from, to time.Time, cap int) int {
	days := int(to.Sub(from).Hours()/24) + 2
	if days < 1 {
		days = 1
	}
	if days > cap {
		days = cap
	}
	return days
}
