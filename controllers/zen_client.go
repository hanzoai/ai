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

// zen_client.go — ai's thin client of the Hanzo model-family services.
//
// A model family (Zen, Enso, …) is a self-contained serving layer: it owns which
// upstream serves a SKU, the identity, the reasoning-vocabulary fold, the 1M context
// ladder, vision, and the enso-ultra fan-out. ai authenticates, meters, and forwards;
// it discovers each family from GET <family>/v1/models and pipes requests verbatim.
// A family is a VALUE — a name, a base URL, a service key, an ownership prefix — so
// zen and enso are two instances of the SAME machinery (modelFamily), not two copies.
// ai holds only configuration and the public brand, never an upstream mapping. See
// hip-00NN for the catalog, pricing, and discovery contract.

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
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
	"github.com/hanzoai/decimal"
	iam "github.com/hanzoai/iam"
	"github.com/hanzoai/money"
)

// ── the families (values, not places) ────────────────────────────────────────
//
// Each family's address is configuration (a base URL + service key, set in the
// deployment), so this repository carries no routing detail and reads as open
// source. A family is added by adding an instance here — nothing else in ai knows a
// family name.

// modelFamily is one serving family as a value: its public name/brand + cold-start
// ownership prefix, the config keys that hold its address, the provider record ai
// forwards through, and a discovered-catalog snapshot refreshed from the family on a
// TTL. All of zen's original machinery is a method on this value; zen and enso are
// two instances.
type modelFamily struct {
	name       string                  // "zen" | "enso" — the Provider label + brand
	prefix     string                  // public brand prefix for cold-start ownership (before first discovery)
	owner      string                  // public /v1/models owned_by: "zenlm" (Zen LM) | "hanzo" (Hanzo)
	urlKey     string                  // config key for the base URL ("ZEN_URL" | "ENSO_URL")
	keyKey     string                  // config key for the service key ("ZEN_API_KEY" | "ENSO_API_KEY")
	providerFn func() *object.Provider // the virtual provider ai forwards through

	// discovered catalog — a read-mostly snapshot; discovery failure keeps the last
	// good snapshot so a transient blip never empties ai's model list.
	mu        sync.RWMutex
	byID      map[string]zenModel
	ids       []string
	fetchedAt time.Time
	loaded    bool
}

var (
	zenFam = &modelFamily{
		name: "zen", prefix: "zen", owner: "zenlm", urlKey: "ZEN_URL", keyKey: "ZEN_API_KEY",
		providerFn: object.ZenProvider,
	}
	ensoFam = &modelFamily{
		name: "enso", prefix: "enso", owner: "hanzo", urlKey: "ENSO_URL", keyKey: "ENSO_API_KEY",
		providerFn: object.EnsoProvider,
	}
	// modelFamilies is the discovery/route/merge iteration order — the ONE list every
	// generic family helper walks.
	modelFamilies = []*modelFamily{zenFam, ensoFam}
)

func (f *modelFamily) baseURL() string {
	return strings.TrimRight(strings.TrimSpace(conf.GetConfigString(f.urlKey)), "/")
}

func (f *modelFamily) serviceKey() string {
	return strings.TrimSpace(conf.GetConfigString(f.keyKey))
}

// enabled reports whether the family service is configured. When it is not, ai serves
// no model of that family (serves is false) and it is simply absent from /v1/models.
func (f *modelFamily) enabled() bool { return f.baseURL() != "" }

// familyForProviderType maps a virtual provider Type ("Zen" | "Enso") to its family,
// or nil for a non-family provider. The completion dispatch uses this to pipe to the
// right family with no per-family branch.
func familyForProviderType(t string) *modelFamily {
	switch t {
	case "Zen":
		return zenFam
	case "Enso":
		return ensoFam
	}
	return nil
}

// familyServing returns the family that owns a model, if any.
func familyServing(model string) (*modelFamily, bool) {
	for _, f := range modelFamilies {
		if f.serves(model) {
			return f, true
		}
	}
	return nil, false
}

// familyLookup returns the discovered model for an id/alias from whichever family
// holds it.
func familyLookup(model string) (zenModel, bool) {
	for _, f := range modelFamilies {
		if m, ok := f.lookup(model); ok {
			return m, true
		}
	}
	return zenModel{}, false
}

// familyLookupFresh is familyLookup that first refreshes each family's catalog on
// its TTL (a no-op when warm) before reading it. lookup() alone reads the cache
// with no refresh, so on a replica whose family cache is cold or stale a real SKU
// reads as unknown — which would make a gated SKU (enso) read as non-gated on a cold
// replica and skip the access gate. Access/gating decisions must see the same catalog
// the /v1/models merge does; they go through here (FamilyModelGated).
func familyLookupFresh(model string) (zenModel, bool) {
	for _, f := range modelFamilies {
		f.fresh()
		if m, ok := f.lookup(model); ok {
			return m, true
		}
	}
	return zenModel{}, false
}

// familyPassthroughRoute routes any family SKU to its family service.
func familyPassthroughRoute(model string) *modelRoute {
	for _, f := range modelFamilies {
		if r := f.passthroughRoute(model); r != nil {
			return r
		}
	}
	return nil
}

// familyModelPrice returns the discovered headline price for a family model.
func familyModelPrice(model string) (modelPrice, bool) {
	for _, f := range modelFamilies {
		if p, ok := f.modelPrice(model); ok {
			return p, true
		}
	}
	return modelPrice{}, false
}

// mergeFamilyModels overlays every configured family's discovered lineup onto the
// base /v1/models list (each SKU wins over a same-named base entry; new SKUs append).
func mergeFamilyModels(base []modelInfo) []modelInfo {
	for _, f := range modelFamilies {
		base = f.mergeModels(base)
	}
	return base
}

// ── discovered catalog (values, not places) ──────────────────────────────────

// zenTier is one context tier's retail price ($/MTok) as EXACT decimals — the same
// values the family serves, parsed losslessly (never through float).
type zenTier struct {
	MaxCtx int
	In     decimal.Decimal
	Out    decimal.Decimal
}

// zenModel is a discovered SKU: what ai needs to list, route, and bill it — the SKU's
// public contract (id, context, price ladder, vision). Not its upstream, which the
// family does not disclose (hip-00NN).
type zenModel struct {
	ID      string
	OwnedBy string
	MaxCtx  int
	Vision  bool
	Access  string    // "" = generally available; "waitlist" = access-gated (limited preview) — ai enforces the grant
	MinTier string    // "" | "free" | "trial" | "paid" — min subscription tier the family advertises for this SKU; ai enforces it (Seams A/B). "" ⇒ free (all tiers). Orthogonal to Access.
	Base    zenTier   // headline price (the in-window tier)
	Tiers   []zenTier // full ladder, ascending by MaxCtx — the billing contract
}

// gated reports whether the family SKU is access-controlled (a limited-preview SKU
// that is LISTED but callable only with a granted ModelAccess row). ai learns this
// from discovery (the family's advertised access), never a hardcode.
func (m zenModel) gated() bool { return m.Access == "waitlist" }

// minTier is the SKU's advertised minimum subscription tier on the enso ladder,
// normalized: "" ⇒ "free" (every tier may use it). One of "free" | "trial" | "paid".
// ai learns this from discovery (the family's advertised min_tier), never a hardcode;
// the tier gate (familyTierAllowed, Seams A/B) enforces it.
func (m zenModel) minTier() string {
	if t := strings.ToLower(strings.TrimSpace(m.MinTier)); t != "" {
		return t
	}
	return "free"
}

// tierFor picks the tier that serves promptTokens: the smallest whose context covers
// the prompt, else the largest. This is the family's Tier rule (hip-00NN) — cost keys
// off the SERVED tier, so a 1M-overflow bills at the pricier tier and never underbills.
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

// costCents computes exact retail cost for token counts at the served tier and returns
// it in ai's cent-granular ledger unit. The dollar value is derived the same way the
// family derives it — money/decimal, no float, no cents flooring mid-way; only the
// final debit rounds to the cent, with a one-cent floor for any non-zero usage so a
// served call is never billed zero.
func (m zenModel) costCents(promptTokens, completionTokens int) int64 {
	t := m.tierFor(promptTokens)
	in := t.In.Mul(decimal.New(int64(promptTokens), 0))
	out := t.Out.Mul(decimal.New(int64(completionTokens), 0))
	usd := in.Add(out).Quo(zenMillion, 18) // exact dollars to 18 dp — identical to the family
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

// zenWireModel is the /v1/models item shape a family serves. Prices are JSON strings
// decoded straight into decimal (decimal.UnmarshalJSON), preserving the exact value.
// There is deliberately no upstream field to read.
type zenWireModel struct {
	ID            string `json:"id"`
	OwnedBy       string `json:"owned_by"`
	Access        string `json:"access"`   // "" | "waitlist" — access gating advertised by the family
	MinTier       string `json:"min_tier"` // "" | "free" | "trial" | "paid" — min subscription tier advertised for this SKU (Seams A/B)
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
		ID: w.ID, OwnedBy: w.OwnedBy, MaxCtx: w.ContextWindow, Vision: w.Capabilities.Vision, Access: w.Access, MinTier: w.MinTier,
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

const zenCatalogTTL = 5 * time.Minute

var zenDiscoveryClient = &http.Client{Timeout: 15 * time.Second}

// refresh fetches GET <family>/v1/models and swaps in a fresh snapshot. Best-effort:
// on any error the previous snapshot stands.
func (f *modelFamily) refresh() error {
	base := f.baseURL()
	if base == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/models", nil)
	if err != nil {
		return err
	}
	if k := f.serviceKey(); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	resp, err := zenDiscoveryClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s /v1/models: status %d", f.name, resp.StatusCode)
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
	f.mu.Lock()
	f.byID = byID
	f.ids = ids
	f.fetchedAt = time.Now()
	f.loaded = true
	f.mu.Unlock()
	return nil
}

// fresh refreshes the snapshot if stale (or never loaded) and the family is
// configured. Errors are logged, not surfaced — a stale snapshot still serves.
func (f *modelFamily) fresh() {
	if !f.enabled() {
		return
	}
	f.mu.RLock()
	stale := !f.loaded || time.Since(f.fetchedAt) > zenCatalogTTL
	f.mu.RUnlock()
	if stale {
		if err := f.refresh(); err != nil {
			log.Warning("%s catalog refresh failed: %v", f.name, err)
		}
	}
}

// lookup returns the discovered model for an id/alias, if any.
func (f *modelFamily) lookup(model string) (zenModel, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	m, ok := f.byID[strings.ToLower(strings.TrimSpace(model))]
	return m, ok
}

// snapshot returns the discovered family (refreshing on a TTL).
func (f *modelFamily) snapshot() []zenModel {
	f.fresh()
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]zenModel, 0, len(f.ids))
	for _, id := range f.ids {
		if m, ok := f.byID[strings.ToLower(id)]; ok {
			out = append(out, m)
		}
	}
	return out
}

// serves reports whether the family owns this model. True for any SKU the discovered
// catalog resolves, and for the public brand prefix so a request routes correctly even
// before the first discovery completes (cold start). The prefix is the public brand,
// not a confidential mapping. False when the family is not configured.
func (f *modelFamily) serves(model string) bool {
	if !f.enabled() {
		return false
	}
	f.fresh()
	if _, ok := f.lookup(model); ok {
		return true
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), f.prefix)
}

// modelPrice returns the discovered headline price for a family model, for the
// balance-reservation estimate and the model listing. The exact debit uses
// zenModel.costCents (money/decimal), never this float projection.
func (f *modelFamily) modelPrice(model string) (modelPrice, bool) {
	m, ok := f.lookup(model)
	if !ok {
		return modelPrice{}, false
	}
	return m.price()
}

// passthroughRoute routes any family model to its family provider, forwarding the SKU
// name verbatim. ai holds no SKU→upstream mapping: the family resolves the SKU, injects
// identity, folds reasoning, and picks the upstream (hip-00NN).
func (f *modelFamily) passthroughRoute(model string) *modelRoute {
	if !f.serves(model) {
		return nil
	}
	ctx := 0
	if m, ok := f.lookup(model); ok {
		ctx = m.MaxCtx
	}
	return &modelRoute{
		providerName:  f.name,
		upstreamModel: model, // identity: the family maps the SKU to its real upstream
		premium:       true,
		ownedBy:       f.owner, // public branding: enso→"hanzo", zen→"zenlm"
		contextWindow: ctx,
	}
}

// mergeModels overlays this family's discovered lineup onto the base /v1/models list:
// a discovered SKU wins over any same-named base entry, and new SKUs are appended.
func (f *modelFamily) mergeModels(base []modelInfo) []modelInfo {
	zs := f.snapshot()
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
			owner = f.owner // public branding default: enso→"hanzo", zen→"zenlm"
		}
		info := modelInfo{
			ID: z.ID, Object: "model", Created: now, OwnedBy: owner, Premium: true,
			Pricing: pricingInfo(z.price()),
		}
		// A gated SKU is LISTED but access-controlled; advertise the default standing
		// ("waitlist"). ListModels upgrades this to the caller's real status when authed.
		if z.gated() {
			info.Access = &modelAccessInfo{State: "waitlist"}
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

// zenPipeClient forwards inference to a family. No client-level timeout: a streamed 1M
// request is long by design, and the request context (client disconnect) bounds it. A
// response-header timeout guards against a dead family without cutting live streams.
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

// pipeToFamily forwards the caller's request to the family service verbatim and relays
// the response back unchanged — the family owns identity, reasoning, upstream, and
// dialect; ai only authenticates, meters, and forwards. apiPath is "messages"
// (Anthropic) or "chat/completions" (OpenAI). Billing settles from the response usage
// at the discovered exact retail price; the settle is idempotent with any deferred
// fail-safe settle at the call site.
func (c *ApiController) pipeToFamily(fam *modelFamily, apiPath, dialect, model string, rawBody []byte, stream bool, orgId string, authUser *iam.User, isPremium bool, hold *budgetHold, start time.Time) {
	prov := fam.providerFn()
	if prov == nil {
		c.zenError(dialect, fam.name+" service is not configured", http.StatusServiceUnavailable)
		return
	}
	// Access gate (the ONE choke point): a gated SKU (enso limited preview) is
	// callable only with a granted ModelAccess row. Discovery tells us which models
	// are gated (zenModel.gated), so this is policy learned from data, not a hardcode.
	// The deferred hold.settle(0) at the call site releases the reservation on this
	// early return, so a denied request is never billed.
	if !c.familyAccessAllowed(fam, model, orgId, authUser) {
		c.zenError(dialect, gatedAccessMessage(model), http.StatusForbidden)
		return
	}
	reqID := util.GenerateUUID()
	url := prov.ProviderUrl + "/v1/" + apiPath
	req, err := http.NewRequestWithContext(c.Ctx.Request.Context(), http.MethodPost, url, bytes.NewReader(rawBody))
	if err != nil {
		c.zenError(dialect, "build "+fam.name+" request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if a := c.Ctx.Request.Header.Get("Accept"); a != "" {
		req.Header.Set("Accept", a)
	}
	if prov.ClientSecret != "" {
		req.Header.Set("Authorization", "Bearer "+prov.ClientSecret)
	}
	// Tenant attribution: the family needs a billable tenant, and ai — which settles
	// the ledger — tells the family it fronts this call so it meters without
	// double-charging.
	if orgId != "" {
		req.Header.Set("X-Org-Id", orgId)
	} else if authUser != nil {
		req.Header.Set("X-Org-Id", authUser.Owner)
	}
	req.Header.Set("X-Hanzo-Fronted-By", "ai")

	resp, err := zenPipeClient.Do(req)
	if err != nil {
		c.recordFamilyUsage(fam, model, authUser, isPremium, stream, reqID, 0, 0, start, hold, "error", err.Error())
		c.zenError(dialect, fam.name+" request failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	prompt, completion := 0, 0
	served, respID := "", ""
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
		prompt, completion, served, respID = c.relayZenStream(resp.Body)
	} else {
		b, rErr := io.ReadAll(resp.Body)
		if rErr != nil {
			c.zenError(dialect, "read "+fam.name+" response: "+rErr.Error(), http.StatusBadGateway)
			return
		}
		if resp.StatusCode != http.StatusOK {
			c.zenError(dialect, upstreamErrorMessage(b), resp.StatusCode)
			return
		}
		sniffZenUsage(b, &prompt, &completion)
		served = sniffZenModel(b)
		respID = sniffZenId(b)
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
	cents := c.recordFamilyUsage(fam, model, authUser, isPremium, stream, reqID, prompt, completion, start, hold, "success", "")
	// Learning ledger: record this served family call (source="family") and, when the
	// engine endpoint is configured, the shadow A/B pick — off the hot path, no prompt
	// text ever stored (the ledger holds none). The join key is the response id the
	// CLIENT sees (respID), which the client threads back to /v1/feedback; falls back
	// to the internal reqID when the family did not disclose one. See object.RoutingEvent.
	c.recordFamilyRouting(model, served, respID, reqID, rawBody, orgId, authUser, prompt, completion, cents, start)
	c.EnableRender = false
}

// relayZenStream copies a family's SSE response to the client verbatim and captures the
// final usage for billing. The family already emits correct dialect SSE, so ai forwards
// bytes unchanged — no translation — and only reads usage as it passes.
func (c *ApiController) relayZenStream(body io.Reader) (prompt, completion int, served, respID string) {
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
				if served == "" {
					served = sniffZenModel(payload)
				}
				if respID == "" {
					respID = sniffZenId(payload)
				}
			}
		}
	}
	return
}

// sniffZenModel reads the top-level "model" (or Anthropic's message.model) from a
// family response payload — the concrete arm/rung that served, when the family
// discloses it. "" when absent; the caller then falls back to the requested SKU.
func sniffZenModel(payload []byte) string {
	var p struct {
		Model   string `json:"model"`
		Message *struct {
			Model string `json:"model"`
		} `json:"message"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return ""
	}
	if p.Model != "" {
		return p.Model
	}
	if p.Message != nil {
		return p.Message.Model
	}
	return ""
}

// sniffZenId reads the response id the CLIENT sees — OpenAI's top-level "id"
// (chatcmpl-…) or Anthropic's message id (msg_…, at "id" for messages or nested under
// "message"). This is the exact id the client threads back to /v1/feedback, so it is
// the routing-event join key. "" when absent.
func sniffZenId(payload []byte) string {
	var p struct {
		ID      string `json:"id"`
		Message *struct {
			ID string `json:"id"`
		} `json:"message"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return ""
	}
	if p.ID != "" {
		return p.ID
	}
	if p.Message != nil {
		return p.Message.ID
	}
	return ""
}

// sniffZenUsage reads token usage from a family response — an SSE data payload or a
// full body — trying the Anthropic (input_tokens/output_tokens, possibly nested under
// message) and OpenAI (prompt_tokens/completion_tokens) shapes. Present fields update
// the running counts; absent ones leave them, so Anthropic's message_start (input) and
// message_delta (output) accumulate across events.
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

// recordFamilyUsage settles the budget hold at the exact discovered price and records
// the usage + trace. Billing lives in one place for both stream and buffered paths and
// for every family.
func (c *ApiController) recordFamilyUsage(fam *modelFamily, model string, authUser *iam.User, isPremium, stream bool, reqID string, prompt, completion int, start time.Time, hold *budgetHold, status, errMsg string) int64 {
	var cents int64
	if status == "success" {
		if zm, ok := fam.lookup(model); ok {
			cents = zm.costCents(prompt, completion)
		} else {
			cents = calculateCostCentsWithCache(model, prompt, completion, 0, 0)
		}
	}
	hold.settle(cents)
	if authUser == nil {
		return cents
	}
	rec := &usageRecord{
		Owner: authUser.Owner, User: authUser.Owner + "/" + authUser.Name, Organization: authUser.Owner,
		Model: model, Provider: fam.name,
		PromptTokens: prompt, CompletionTokens: completion, TotalTokens: prompt + completion,
		Cost: float64(cents) / 100.0, Currency: "USD",
		Premium: isPremium, Stream: stream, Status: status, ErrorMsg: errMsg,
		ClientIP: c.Ctx.Request.RemoteAddr, RequestID: reqID, Account: "hanzo",
	}
	recordUsage(rec)
	recordTrace(c.Ctx.Request.Context(), rec, start)
	return cents
}

// recordFamilyRouting writes the privacy-preserving learning ledger row for a served
// family call and, when the engine endpoint is configured, the shadow A/B pick. It is
// fire-and-forget off the request hot path (exactly like AddRoutingEvent's auto-route
// callers): a collection failure never touches the served response. It NEVER stores
// prompt text — only token/cost/latency metrics, the served arm, and (for the shadow)
// the engine's own opaque feature vector + counterfactual model.
//
// The request-derived routing inputs (last user text, media flag) are extracted
// synchronously from rawBody before the goroutine spawns, so nothing reads the beego
// context off-thread. The shadow /v1/route call is hard-capped by router.DefaultTimeout.
func (c *ApiController) recordFamilyRouting(model, served, respID, reqID string, rawBody []byte, orgId string, authUser *iam.User, prompt, completion int, cents int64, start time.Time) {
	owner := orgId
	user := ""
	if authUser != nil {
		if owner == "" {
			owner = authUser.Owner
		}
		user = authUser.Owner + "/" + authUser.Name
	}
	// The join key is the response id the client sees; fall back to the internal reqID
	// when the family disclosed none (RecordFamilyRouting normalizes it identically to
	// /v1/feedback so both sides key on the same value).
	responseId := respID
	if strings.TrimSpace(responseId) == "" {
		responseId = reqID
	}
	text, hasMedia := familyRoutingText(rawBody)
	endpoint := ""
	if cfg := GetModelConfig(); cfg != nil {
		endpoint = cfg.RouterClient(nil).Endpoint
	}
	in := object.FamilyRoutingInput{
		Owner: owner, User: user,
		RequestedModel: model, RoutedModel: served, ResponseId: responseId,
		PromptTokens: prompt, CompletionTokens: completion,
		CostCents: cents, LatencyMs: time.Since(start).Milliseconds(),
		RouterEndpoint: endpoint, ShadowText: text,
		ShadowApproxTokens: coarseTokenEstimate(rawBody), ShadowHasMedia: hasMedia,
	}
	// Fire-and-forget off the request hot path — the ONE shared writer (object) is
	// called identically by cloud's embedded-zen meter.
	go object.RecordFamilyRouting(in)
}

// familyRoutingText extracts the last user turn's text and whether the request carries
// media from a raw chat/completions or messages body — enough for the shadow router to
// classify, working for both dialects (both use messages[].role/content, content a
// string or an array of typed parts). It is a pure function of rawBody; it returns no
// prompt text to any store — only the shadow /v1/route call (which the engine already
// receives for auto routing) reads it, and only the engine's derived features persist.
func familyRoutingText(rawBody []byte) (text string, hasMedia bool) {
	var body struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(rawBody, &body) != nil {
		return "", false
	}
	for i := len(body.Messages) - 1; i >= 0; i-- {
		m := body.Messages[i]
		if m.Role != "user" {
			continue
		}
		var s string
		if json.Unmarshal(m.Content, &s) == nil {
			if s != "" {
				return s, false
			}
			continue
		}
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(m.Content, &parts) != nil {
			continue
		}
		var b strings.Builder
		for _, p := range parts {
			if strings.Contains(p.Type, "image") || strings.Contains(p.Type, "video") {
				hasMedia = true
			}
			if p.Text != "" {
				if b.Len() > 0 {
					b.WriteByte('\n')
				}
				b.WriteString(p.Text)
			}
		}
		if b.Len() > 0 || hasMedia {
			return b.String(), hasMedia
		}
	}
	return "", hasMedia
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
