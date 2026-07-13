// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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

// zen_client.go — ai's thin client of the Zen model family service.
//
// ai authenticates, meters, and forwards; zen owns the family: which upstream
// serves a SKU, the identity, the reasoning-vocabulary fold, the 1M context
// ladder, and vision routing. ai discovers the family from GET <zen>/v1/models
// and pipes zen requests to zen verbatim. It holds only a base URL (configuration)
// and the public "zen" brand — no upstream mapping. See hip-00NN for the catalog,
// pricing, and discovery contract.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
	beegoLogs "github.com/hanzoai/beego/logs"
	"github.com/hanzoai/decimal"
	iam "github.com/hanzoai/iam"
	"github.com/hanzoai/money"
)

// ── configuration ───────────────────────────────────────────────────────────
// The zen base URL and service key are configuration, never source: values set in
// the deployment, so this repository carries no routing detail and reads as open
// source.

func zenBaseURL() string {
	return strings.TrimRight(strings.TrimSpace(conf.GetConfigString("ZEN_URL")), "/")
}

func zenServiceKey() string {
	return strings.TrimSpace(conf.GetConfigString("ZEN_API_KEY"))
}

// zenEnabled reports whether a zen service is configured. When it is not, ai serves
// no zen model (zenServes is false) and the family is simply absent from /v1/models.
// The provider record ai forwards zen calls through is object.ZenProvider() — the
// one place zen's address is synthesized from configuration.
func zenEnabled() bool { return zenBaseURL() != "" }

// ── discovered catalog (values, not places) ──────────────────────────────────

// zenTier is one context tier's retail price ($/MTok) as EXACT decimals — the same
// values zen serves, parsed losslessly (never through float).
type zenTier struct {
	MaxCtx int
	In     decimal.Decimal
	Out    decimal.Decimal
}

// zenModel is a discovered SKU: what ai needs to list, route, and bill it — the
// SKU's public contract (id, context, price ladder, vision). Not its upstream,
// which zen does not disclose (hip-00NN).
type zenModel struct {
	ID      string
	OwnedBy string
	MaxCtx  int
	Vision  bool
	Base    zenTier   // headline price (the in-window tier)
	Tiers   []zenTier // full ladder, ascending by MaxCtx — the billing contract
}

// tierFor picks the tier that serves promptTokens: the smallest whose context
// covers the prompt, else the largest. This is zen's Tier rule (hip-00NN) — cost
// keys off the SERVED tier, so a 1M-overflow bills at the pricier tier and can
// never underbill.
func (m zenModel) tierFor(promptTokens int) zenTier {
	for _, t := range m.Tiers {
		if promptTokens <= t.MaxCtx {
			return t
		}
	}
	if len(m.Tiers) > 0 {
		return m.Tiers[len(m.Tiers)-1]
	}
	return m.Base
}

var zenMillion = decimal.New(1_000_000, 0)

// costCents computes exact retail cost for token counts at the served tier and
// returns it in ai's cent-granular ledger unit. The dollar value is derived the
// same way zen derives it — money/decimal, no float, no cents flooring mid-way;
// only the final debit rounds to the cent, with a one-cent floor for any non-zero
// usage so a served call is never billed zero.
func (m zenModel) costCents(promptTokens, completionTokens int) int64 {
	t := m.tierFor(promptTokens)
	in := t.In.Mul(decimal.New(int64(promptTokens), 0))
	out := t.Out.Mul(decimal.New(int64(completionTokens), 0))
	usd := in.Add(out).Quo(zenMillion, 18) // exact dollars to 18 dp — identical to zen
	cents := money.New(usd, money.USD).Minor().Int64()
	if cents <= 0 && (promptTokens > 0 || completionTokens > 0) {
		cents = 1
	}
	return cents
}

// price projects the headline tier into ai's legacy per-model price struct (float,
// used only for the balance-reservation estimate and the /v1/models display). The
// exact debit path uses costCents, never this projection.
func (m zenModel) price() (modelPrice, bool) {
	in, _ := strconv.ParseFloat(m.Base.In.String(), 64)
	out, _ := strconv.ParseFloat(m.Base.Out.String(), 64)
	if in <= 0 && out <= 0 {
		return modelPrice{}, false
	}
	return modelPrice{InputPerMillion: in, OutputPerMillion: out}, true
}

// zenWireModel is the /v1/models item shape zen serves. Prices are JSON strings
// decoded straight into decimal (decimal.UnmarshalJSON), preserving the exact
// value. There is deliberately no upstream field to read.
type zenWireModel struct {
	ID            string `json:"id"`
	OwnedBy       string `json:"owned_by"`
	ContextWindow int    `json:"context_window"`
	Pricing       struct {
		Input  decimal.Decimal `json:"input"`
		Output decimal.Decimal `json:"output"`
	} `json:"pricing"`
	PricingTiers []struct {
		MaxContext int             `json:"max_context"`
		Input      decimal.Decimal `json:"input"`
		Output     decimal.Decimal `json:"output"`
	} `json:"pricing_tiers"`
	Capabilities struct {
		Vision bool `json:"vision"`
	} `json:"capabilities"`
}

func (w zenWireModel) model() zenModel {
	zm := zenModel{
		ID: w.ID, OwnedBy: w.OwnedBy, MaxCtx: w.ContextWindow, Vision: w.Capabilities.Vision,
		Base: zenTier{MaxCtx: w.ContextWindow, In: w.Pricing.Input, Out: w.Pricing.Output},
	}
	for _, t := range w.PricingTiers {
		zm.Tiers = append(zm.Tiers, zenTier{MaxCtx: t.MaxContext, In: t.Input, Out: t.Output})
	}
	if len(zm.Tiers) == 0 {
		zm.Tiers = []zenTier{zm.Base}
	}
	return zm
}

// zenCatalog is the discovered family: a read-mostly snapshot refreshed from zen on
// a TTL. Discovery failure keeps the last good snapshot — a transient zen blip never
// empties ai's model list.
var zenCatalog struct {
	mu        sync.RWMutex
	byID      map[string]zenModel
	ids       []string
	fetchedAt time.Time
	loaded    bool
}

const zenCatalogTTL = 5 * time.Minute

var zenDiscoveryClient = &http.Client{Timeout: 15 * time.Second}

// refreshZenCatalog fetches GET <zen>/v1/models and swaps in a fresh snapshot.
// Best-effort: on any error the previous snapshot stands.
func refreshZenCatalog() error {
	base := zenBaseURL()
	if base == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/models", nil)
	if err != nil {
		return err
	}
	if k := zenServiceKey(); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	resp, err := zenDiscoveryClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("zen /v1/models: status %d", resp.StatusCode)
	}
	var out struct {
		Data []zenWireModel `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	byID := make(map[string]zenModel, len(out.Data))
	ids := make([]string, 0, len(out.Data))
	for _, w := range out.Data {
		if strings.TrimSpace(w.ID) == "" {
			continue
		}
		byID[strings.ToLower(w.ID)] = w.model()
		ids = append(ids, w.ID)
	}
	zenCatalog.mu.Lock()
	zenCatalog.byID = byID
	zenCatalog.ids = ids
	zenCatalog.fetchedAt = time.Now()
	zenCatalog.loaded = true
	zenCatalog.mu.Unlock()
	return nil
}

// zenCatalogFresh refreshes the snapshot if stale (or never loaded) and zen is
// configured. Errors are logged, not surfaced — a stale snapshot still serves.
func zenCatalogFresh() {
	if !zenEnabled() {
		return
	}
	zenCatalog.mu.RLock()
	stale := !zenCatalog.loaded || time.Since(zenCatalog.fetchedAt) > zenCatalogTTL
	zenCatalog.mu.RUnlock()
	if stale {
		if err := refreshZenCatalog(); err != nil {
			beegoLogs.Warning("zen catalog refresh failed: %v", err)
		}
	}
}

// zenLookup returns the discovered model for an id/alias, if any.
func zenLookup(model string) (zenModel, bool) {
	zenCatalog.mu.RLock()
	defer zenCatalog.mu.RUnlock()
	m, ok := zenCatalog.byID[strings.ToLower(strings.TrimSpace(model))]
	return m, ok
}

// zenModelsSnapshot returns the discovered family (refreshing on a TTL).
func zenModelsSnapshot() []zenModel {
	zenCatalogFresh()
	zenCatalog.mu.RLock()
	defer zenCatalog.mu.RUnlock()
	out := make([]zenModel, 0, len(zenCatalog.ids))
	for _, id := range zenCatalog.ids {
		if m, ok := zenCatalog.byID[strings.ToLower(id)]; ok {
			out = append(out, m)
		}
	}
	return out
}

// zenServes reports whether the zen service owns this model. True for any SKU the
// discovered catalog resolves, and for the public "zen" brand prefix so a request
// routes correctly even before the first discovery completes (cold start). The
// prefix is the public brand, not a confidential mapping. False when zen is not
// configured — ai then serves no zen model.
func zenServes(model string) bool {
	if !zenEnabled() {
		return false
	}
	zenCatalogFresh()
	if _, ok := zenLookup(model); ok {
		return true
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "zen")
}

// zenModelPrice returns the discovered headline price for a zen model, for the
// balance-reservation estimate and the model listing. The exact debit uses
// zenModel.costCents (money/decimal), never this float projection.
func zenModelPrice(model string) (modelPrice, bool) {
	m, ok := zenLookup(model)
	if !ok {
		return modelPrice{}, false
	}
	return m.price()
}

// zenPassthroughRoute routes any zen model to the zen provider, forwarding the SKU
// name verbatim. ai holds no zen→upstream mapping: zen resolves the SKU, injects
// identity, folds reasoning, and picks the upstream (hip-00NN).
func zenPassthroughRoute(model string) *modelRoute {
	if !zenServes(model) {
		return nil
	}
	ctx := 0
	if m, ok := zenLookup(model); ok {
		ctx = m.MaxCtx
	}
	return &modelRoute{
		providerName:  "zen",
		upstreamModel: model, // identity: zen maps the SKU to its real upstream
		premium:       true,
		ownedBy:       "hanzo",
		contextWindow: ctx,
	}
}

// mergeZenModels overlays the discovered zen family onto the base /v1/models list:
// a discovered SKU wins over any same-named base entry, and new SKUs are appended.
func mergeZenModels(base []modelInfo) []modelInfo {
	zs := zenModelsSnapshot()
	if len(zs) == 0 {
		return base
	}
	now := time.Now().Unix()
	idx := make(map[string]int, len(base))
	for i, m := range base {
		idx[strings.ToLower(m.ID)] = i
	}
	for _, z := range zs {
		owner := z.OwnedBy
		if owner == "" {
			owner = "hanzo"
		}
		info := modelInfo{
			ID: z.ID, Object: "model", Created: now, OwnedBy: owner, Premium: true,
			Pricing: pricingInfo(z.price()),
		}
		if i, ok := idx[strings.ToLower(z.ID)]; ok {
			base[i] = info
		} else {
			idx[strings.ToLower(z.ID)] = len(base)
			base = append(base, info)
		}
	}
	sort.Slice(base, func(i, j int) bool { return base[i].ID < base[j].ID })
	return base
}

// ── the pipe (IO edge) ────────────────────────────────────────────────────────

// zenPipeClient forwards inference to zen. No client-level timeout: a streamed 1M
// request is long by design, and the request context (client disconnect) bounds it.
// A response-header timeout guards against a dead zen without cutting live streams.
var zenPipeClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ResponseHeaderTimeout: 120 * time.Second,
	},
}

var (
	zenNewline    = []byte("\n")
	zenDataPrefix = []byte("data:")
)

// pipeToZen forwards the caller's request to zen verbatim and relays zen's response
// back unchanged — zen owns identity, reasoning, upstream, and dialect; ai only
// authenticates, meters, and forwards. apiPath is "messages" (Anthropic) or
// "chat/completions" (OpenAI). Billing settles from the response usage at the
// discovered exact retail price; the settle is idempotent with any deferred
// fail-safe settle at the call site.
func (c *ApiController) pipeToZen(apiPath, dialect, model string, rawBody []byte, stream bool, orgId string, authUser *iam.User, isPremium bool, hold *budgetHold, start time.Time) {
	prov := object.ZenProvider()
	if prov == nil {
		c.zenError(dialect, "zen service is not configured", http.StatusServiceUnavailable)
		return
	}
	reqID := util.GenerateUUID()
	url := prov.ProviderUrl + "/v1/" + apiPath
	req, err := http.NewRequestWithContext(c.Ctx.Request.Context(), http.MethodPost, url, bytes.NewReader(rawBody))
	if err != nil {
		c.zenError(dialect, "build zen request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if a := c.Ctx.Request.Header.Get("Accept"); a != "" {
		req.Header.Set("Accept", a)
	}
	if prov.ClientSecret != "" {
		req.Header.Set("Authorization", "Bearer "+prov.ClientSecret)
	}
	// Tenant attribution: zen needs a billable tenant, and ai — which settles the
	// ledger — tells zen it fronts this call so zen meters without double-charging.
	if orgId != "" {
		req.Header.Set("X-Org-Id", orgId)
	} else if authUser != nil {
		req.Header.Set("X-Org-Id", authUser.Owner)
	}
	req.Header.Set("X-Hanzo-Fronted-By", "ai")

	resp, err := zenPipeClient.Do(req)
	if err != nil {
		c.recordZenUsage(model, authUser, isPremium, stream, reqID, 0, 0, start, hold, "error", err.Error())
		c.zenError(dialect, "zen request failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	prompt, completion := 0, 0
	if stream {
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			c.zenError(dialect, upstreamErrorMessage(b), resp.StatusCode)
			return
		}
		c.Ctx.ResponseWriter.Header().Set("Content-Type", "text/event-stream")
		c.Ctx.ResponseWriter.Header().Set("Cache-Control", "no-cache")
		c.Ctx.ResponseWriter.Header().Set("Connection", "keep-alive")
		c.Ctx.ResponseWriter.WriteHeader(http.StatusOK)
		prompt, completion = c.relayZenStream(resp.Body)
	} else {
		b, rErr := io.ReadAll(resp.Body)
		if rErr != nil {
			c.zenError(dialect, "read zen response: "+rErr.Error(), http.StatusBadGateway)
			return
		}
		if resp.StatusCode != http.StatusOK {
			c.zenError(dialect, upstreamErrorMessage(b), resp.StatusCode)
			return
		}
		sniffZenUsage(b, &prompt, &completion)
		ct := resp.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/json"
		}
		c.Ctx.Output.Header("Content-Type", ct)
		c.Ctx.Output.Body(b)
	}

	if prompt == 0 {
		prompt = coarseTokenEstimate(rawBody)
	}
	c.recordZenUsage(model, authUser, isPremium, stream, reqID, prompt, completion, start, hold, "success", "")
	c.EnableRender = false
}

// relayZenStream copies zen's SSE response to the client verbatim and captures the
// final usage for billing. zen already emits correct dialect SSE, so ai forwards
// bytes unchanged — no translation — and only reads usage as it passes.
func (c *ApiController) relayZenStream(body io.Reader) (prompt, completion int) {
	w := c.Ctx.ResponseWriter
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		_, _ = w.Write(line)
		_, _ = w.Write(zenNewline)
		w.Flush()
		if bytes.HasPrefix(line, zenDataPrefix) {
			payload := bytes.TrimSpace(line[len(zenDataPrefix):])
			if len(payload) > 0 && payload[0] == '{' {
				sniffZenUsage(payload, &prompt, &completion)
			}
		}
	}
	return
}

// sniffZenUsage reads token usage from a zen response — an SSE data payload or a
// full body — trying the Anthropic (input_tokens/output_tokens, possibly nested
// under message) and OpenAI (prompt_tokens/completion_tokens) shapes. Present
// fields update the running counts; absent ones leave them, so Anthropic's
// message_start (input) and message_delta (output) accumulate across events.
func sniffZenUsage(payload []byte, prompt, completion *int) {
	var p struct {
		Usage *struct {
			InputTokens      int `json:"input_tokens"`
			OutputTokens     int `json:"output_tokens"`
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
		Message *struct {
			Usage *struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return
	}
	if u := p.Usage; u != nil {
		if u.InputTokens > 0 {
			*prompt = u.InputTokens
		}
		if u.PromptTokens > 0 {
			*prompt = u.PromptTokens
		}
		if u.OutputTokens > 0 {
			*completion = u.OutputTokens
		}
		if u.CompletionTokens > 0 {
			*completion = u.CompletionTokens
		}
	}
	if p.Message != nil && p.Message.Usage != nil {
		if p.Message.Usage.InputTokens > 0 {
			*prompt = p.Message.Usage.InputTokens
		}
		if p.Message.Usage.OutputTokens > 0 {
			*completion = p.Message.Usage.OutputTokens
		}
	}
}

// coarseTokenEstimate is a last-resort prompt-token estimate (~4 chars/token) so a
// successful call whose usage ai could not read is still billed, never zero.
func coarseTokenEstimate(body []byte) int {
	return len(body)/4 + 1
}

// recordZenUsage settles the budget hold at the exact discovered price and records
// the usage + trace. Billing lives in one place for both stream and buffered paths.
func (c *ApiController) recordZenUsage(model string, authUser *iam.User, isPremium, stream bool, reqID string, prompt, completion int, start time.Time, hold *budgetHold, status, errMsg string) {
	var cents int64
	if status == "success" {
		if zm, ok := zenLookup(model); ok {
			cents = zm.costCents(prompt, completion)
		} else {
			cents = calculateCostCentsWithCache(model, prompt, completion, 0, 0)
		}
	}
	hold.settle(cents)
	if authUser == nil {
		return
	}
	rec := &usageRecord{
		Owner: authUser.Owner, User: authUser.Owner + "/" + authUser.Name, Organization: authUser.Owner,
		Model: model, Provider: "zen",
		PromptTokens: prompt, CompletionTokens: completion, TotalTokens: prompt + completion,
		Cost: float64(cents) / 100.0, Currency: "USD",
		Premium: isPremium, Stream: stream, Status: status, ErrorMsg: errMsg,
		ClientIP: c.Ctx.Request.RemoteAddr, RequestID: reqID, Account: "hanzo",
	}
	recordUsage(rec)
	recordTrace(c.Ctx.Request.Context(), rec, start)
}

// zenError writes a dialect-correct error. Anthropic callers (Claude Code) get the
// Anthropic error envelope; OpenAI callers get the OpenAI one.
func (c *ApiController) zenError(dialect, msg string, status int) {
	if dialect == "anthropic" {
		c.respondAnthropicError(anthropicErrorTypeForStatus(status), msg, status)
		return
	}
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{"message": msg, "type": "api_error", "code": status},
	})
	c.Ctx.ResponseWriter.Header().Set("Content-Type", "application/json")
	c.Ctx.ResponseWriter.WriteHeader(status)
	_, _ = c.Ctx.ResponseWriter.Write(body)
	c.EnableRender = false
}
