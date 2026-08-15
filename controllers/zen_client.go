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

	"github.com/google/uuid"
	"github.com/hanzoai/ai/conf"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/decimal"
	"github.com/hanzoai/money"
)

// ── the families (values, not places) ────────────────────────────────────────
//
// Each family's address is configuration (a base URL + service key, set in the
// deployment), so this repository carries no routing detail and reads as open
// source. A family is added by adding an instance here — nothing else in ai knows a
// family name.

// flagshipWindow is the served context window (1M tokens) ai guarantees for the
// flagship Zen/Enso SKUs, independent of what a given discovery snapshot advertises.
const flagshipWindow = 1_000_000

// modelFamily is one serving family as a value: its public name/brand + cold-start
// ownership prefix, the config keys that hold its address, the provider record ai
// forwards through, and a discovered-catalog snapshot refreshed from the family on a
// TTL. All of zen's original machinery is a method on this value; zen and enso are
// two instances.
type modelFamily struct {
	name       string                  // "zen" | "enso" | "openrouter" — the brand label
	provider   string                  // the object.Provider Type this family serves ("Zen" | "Enso" | "OpenRouter")
	prefix     string                  // public brand prefix for cold-start ownership (before first discovery)
	owner      string                  // public /v1/models owned_by: "zenlm" (Zen LM) | "hanzo" (Hanzo)
	urlKey     string                  // config key for the base URL ("ZEN_URL" | "ENSO_URL")
	keyKey     string                  // config key for the service key ("ZEN_API_KEY" | "ENSO_API_KEY")
	providerFn func() *object.Provider // the virtual provider ai forwards through
	// windows is the served context window ai guarantees for a DISCOVERED SKU,
	// keyed by SKU id — a flagship reports its real 1M window even when a given
	// discovery snapshot advertises less. It cannot add a SKU: a model appears in
	// /v1/models iff the family serves it. A map (not a list ai could iterate into
	// the listing) is what makes an unservable entry unrepresentable — the previous
	// list synthesized a listing for any SKU the family "would" serve, which is how
	// enso-pro was advertised, unpriced, for a family that answers 404 for it.
	windows map[string]int

	// decode turns a family's raw GET /v1/models body into discovered SKUs. nil means
	// the Hanzo family wire shape (zenWireModel), which already carries every field ai
	// needs. A family that publishes a DIFFERENT catalog dialect — OpenRouter states
	// price per TOKEN and never states a tier or a funding class — supplies its own
	// translation here, so the dialect is the ONLY thing that varies and discovery,
	// TTL, listing, routing, and gating stay one implementation.
	decode func([]byte) ([]zenModel, error)

	// spare names the routes this family still serves once its account with the
	// vendor is spent — the SKUs the vendor charges nothing for. It reads the same
	// /v1/models body decode reads, in the family's own dialect, because what "free"
	// looks like on the wire is exactly as dialect-specific as what a price is.
	//
	// It is a SUBSET of the catalog, not a second one: the routes the vendor charges
	// nothing for AND answers in text with. Price is what qualifies a route as a
	// spare, so a spare bills nothing whether a caller named it or a refusal for
	// money landed on it — a downgrade can never quietly become something we charge
	// for, and a route we charge for can never be handed out as a remedy.
	//
	// nil means the family has none, and a refusal for money from it is final.
	spare func([]byte) []string

	// freeName is the one id this family publishes for the free pool: a route a
	// caller can name, and the route a refusal from this family falls to. It is
	// branded per family because that is where a caller looks for it, and it is
	// served from the SAME pool for all of them, because a route that costs nothing
	// is not made different by whose name is on the door.
	freeName string

	// terms states, in the family's own dialect, what the vendor may keep of an
	// exchange. It is called with the outgoing body and whether the route being
	// asked for is one the vendor charges nothing for, because that is the trade a
	// free route IS: the vendor serves it for nothing and keeps what it carried.
	// A priced route pays instead, and keeps nothing.
	//
	// nil for a family whose dialect states no such thing, and the body then goes
	// out untouched — vendor-specific fields are never sent to a vendor that did
	// not define them.
	terms func(body []byte, free bool) []byte

	// discovered catalog — a read-mostly snapshot; discovery failure keeps the last
	// good snapshot so a transient blip never empties ai's model list.
	mu        sync.RWMutex
	byID      map[string]zenModel
	ids       []string
	spares    []string
	fetchedAt time.Time
	loaded    bool
}

var (
	zenFam = &modelFamily{
		name: "zen", provider: "Zen", prefix: "zen", owner: "zenlm", urlKey: "ZEN_URL", keyKey: "ZEN_API_KEY",
		freeName:   "zen-free",
		providerFn: object.ZenProvider,
		windows: map[string]int{
			"zen5":       flagshipWindow,
			"zen5-coder": flagshipWindow,
			"zen5-pro":   flagshipWindow,
		},
	}
	ensoFam = &modelFamily{
		name: "enso", provider: "Enso", prefix: "enso", owner: "hanzo", urlKey: "ENSO_URL", keyKey: "ENSO_API_KEY",
		freeName:   "enso-free",
		providerFn: object.EnsoProvider,
		// enso-pro is deliberately absent: the enso service serves enso, enso-flash
		// and enso-ultra and answers 404 for enso-pro (probed directly — the balance
		// gate returns 402 before model resolution, so only a direct probe can tell).
		windows: map[string]int{
			"enso":       flagshipWindow,
			"enso-ultra": flagshipWindow,
		},
	}
	// engineFam is OUR OWN compute: the models this deployment serves itself, at no
	// marginal cost to anybody's account. It publishes no catalog of its own here —
	// it exists so the free pool has a route that cannot be taken away by a vendor's
	// balance — so it is deliberately absent from modelFamilies below and is reached
	// only through freeRoutes.
	engineFam = &modelFamily{
		name: "engine", provider: "Engine", prefix: "engine/", owner: "hanzo",
		urlKey: "ENGINE_URL", keyKey: "ENGINE_API_KEY",
	}

	// modelFamilies is the discovery/route/merge iteration order — the ONE list every
	// generic family helper walks. openrouter is last: the first-party families claim
	// their own SKUs first, and the resale catalog answers for what is left.
	modelFamilies = []*modelFamily{zenFam, ensoFam, openrouterFam}
)

// window returns the context window ai guarantees when listing and routing a SKU,
// or 0 when the family guarantees none. It applies only to a configured family, so
// a bare or unconfigured family reports no guaranteed window.
func (f *modelFamily) window(model string) int {
	if !f.enabled() {
		return 0
	}
	return f.windows[strings.ToLower(strings.TrimSpace(model))]
}

// baseURL and serviceKey resolve through the family's OWN provider constructor —
// the same one pipeToFamily forwards through — so the address that gates listing
// is the address that serves. That constructor reads the admin row first and
// falls back to deployment config, which is what makes admin.hanzo.ai the one
// control: disabling a family there makes baseURL empty, so enabled() goes false
// and its models leave /v1/models on the next resolution.
//
// Reading conf directly here (which is what these did) meant the console toggle
// moved a row that listing never consulted: a family could be disabled in the
// admin UI and still be listed and served.
func (f *modelFamily) baseURL() string {
	if f.providerFn != nil {
		if p := f.providerFn(); p != nil {
			return strings.TrimRight(strings.TrimSpace(p.ProviderUrl), "/")
		}
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(conf.GetConfigString(f.urlKey)), "/")
}

func (f *modelFamily) serviceKey() string {
	if f.providerFn != nil {
		if p := f.providerFn(); p != nil {
			return strings.TrimSpace(p.ClientSecret)
		}
		return ""
	}
	return strings.TrimSpace(conf.GetConfigString(f.keyKey))
}

// enabled reports whether the family service is configured AND admin-enabled. When
// it is not, ai serves no model of that family (serves is false) and it is simply
// absent from /v1/models.
func (f *modelFamily) enabled() bool { return f.baseURL() != "" }

// familyForProviderType maps a virtual provider Type onto its family, resolved from the
// SAME modelFamilies list that discovery, listing and routing all read.
//
// It used to be a hand-written switch, which is a second list that must be kept in step
// with the first — and it was not. OpenRouter was added to modelFamilies, so its 338
// prepaid models were discovered, listed and routable, but the switch never named it. Every
// dispatch site guards on `if fam := familyForProviderType(...); fam != nil`, so a nil sent
// those requests down the generic relay — around pipeToFamily, which is where the funding
// gate lives. Listed, served, and ungated, on real cash.
//
// Deriving the lookup from the one list makes that shape unrepresentable: a family that
// exists is a family this resolves, and adding one cannot silently skip the gate.
func familyForProviderType(t string) *modelFamily {
	for _, f := range modelFamilies {
		if f.provider == t {
			return f
		}
	}
	return nil
}

// freeID is the platform's OWN name for the pool: one id every surface can send and
// always get a free answer from, wearing no brand at all. enso-free and zen-free are
// the same pool behind a brand; this is the same pool in front of all of them, so a
// client that does not care which family answered has one id to remember.
const freeID = "free"

// frontDoor reports that this id is a name this family publishes for the pool — its
// own branded one, and, for the family that HOLDS the pool, the platform's unbranded
// one. A front door is not a model any vendor has: it is a name for a choice.
func (f *modelFamily) frontDoor(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if f.freeName != "" && m == strings.ToLower(f.freeName) {
		return true
	}
	return f == freeFamily() && m == freeID
}

// freeNames are every id this family publishes for the pool, with the owner each is
// branded to. The platform's own id is owned by us because it is our product, not the
// vendor's, whichever vendor happens to answer it.
func (f *modelFamily) freeNames() []struct{ id, owner string } {
	var out []struct{ id, owner string }
	if f.freeName != "" {
		out = append(out, struct{ id, owner string }{f.freeName, f.owner})
	}
	if f == freeFamily() {
		out = append(out, struct{ id, owner string }{freeID, "hanzo"})
	}
	return out
}

// freeFamily holds the platform's free routes: the family whose vendor publishes
// routes it charges nothing for. Every family falls back to it, itself included,
// because a route that costs nothing is not made different by whose refusal sent a
// request to it.
func freeFamily() *modelFamily { return openrouterFam }

// spare is a route that can answer when the one asked for cannot: the family that
// carries it and the id it is served under. A pair, because the vendor that ran out
// of money is rarely the one still holding a free route.
type spare struct {
	fam *modelFamily
	id  string
}

// freeRoutes are the routes that cost nothing and answer a chat turn, best first.
// ONE pool for the whole platform: one list to curate, and every family falls back
// to the same places.
//
// OUR OWN COMPUTE IS LAST, and last is the point of it. A route we serve ourselves
// cannot run out of a vendor's money and cannot be rate limited by one, so it is the
// answer that remains when every borrowed one is gone — which is exactly a last
// resort and not a first choice. Measured: the engine serves one small model, and
// asked to reply with a single word it answers with several sentences about editing
// a sentence. Putting it first would spend that on every free answer to insure
// against an outage that has not happened; putting it last insures against it for
// nothing. Its place moves the day it serves a model that answers as well as the
// borrowed ones, and only the order below changes.
func freeRoutes() []spare {
	var out []spare
	for _, id := range freeFamily().spareRoutes() {
		out = append(out, spare{freeFamily(), id})
	}
	if engineFam.enabled() {
		out = append(out, spare{engineFam, engineModel})
	}
	return out
}

// servingFamily returns the family that will actually carry this sku — the free
// family for a route out of the pool, whichever family claims it otherwise.
func servingFamily(sku string) *modelFamily {
	if freeFamily().isSpare(sku) {
		return freeFamily()
	}
	f, _ := familyServing(sku)
	return f
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
	// Outputs are the kinds of answer this SKU produces, as the family advertises
	// them ("text", "audio", "image"). Empty when the family advertises none, which
	// reads as unknown and never as text.
	Outputs []string
	Access  string // "" = generally available; "waitlist" = access-gated (limited preview) — ai enforces the grant
	MinTier string // "" | "free" | "trial" | "paid" — min subscription tier the family advertises for this SKU; ai enforces it (Seams A/B). "" ⇒ free (all tiers). Orthogonal to Access.
	// Funding is how the SKU's usage is PAID FOR upstream: "prepaid" means every path it
	// can take spends a real-cash balance; "" means credits. It is a different KIND of
	// floor from MinTier and is enforced differently — see familyFundingAllowed.
	Funding string
	Base    zenTier   // headline RETAIL price (the in-window tier)
	Tiers   []zenTier // full ladder, ascending by MaxCtx — the billing contract

	// CostIn/CostOut are the upstream COGS ($/MTok) behind Base, when the family
	// discloses what the SKU costs us. Zero means undisclosed, which modelPrice
	// already reads as cost == price (zero margin) — so a family that states only a
	// price behaves exactly as before. A resale family (OpenRouter) states both, and
	// the retail it publishes is this cost times a margin, never below it.
	CostIn  decimal.Decimal
	CostOut decimal.Decimal
}

// gated reports whether the family SKU is access-controlled (a limited-preview SKU
// that is LISTED but callable only with a granted ModelAccess row). ai learns this
// from discovery (the family's advertised access), never a hardcode.
func (m zenModel) gated() bool { return m.Access == "waitlist" }

// priced reports whether the family charges for this SKU on either side. It is the
// one reading of "costs money" the listing takes, so premium and free are answers to
// the same question rather than two opinions of it.
func (m zenModel) priced() bool { return !m.Base.In.IsZero() || !m.Base.Out.IsZero() }

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
//
// A discovered SKU always HAS a price, and zero is one of them. "No price" is a
// different fact — nobody stated one — and only a model discovery never saw can be
// in that state. Reading zero as absent is what let a route the vendor charges
// nothing for be listed without a price and gated as though it cost money.
func (m zenModel) price() (modelPrice, bool) {
	in, _ := strconv.ParseFloat(m.Base.In.String(), 64)
	out, _ := strconv.ParseFloat(m.Base.Out.String(), 64)
	costIn, _ := strconv.ParseFloat(m.CostIn.String(), 64)
	costOut, _ := strconv.ParseFloat(m.CostOut.String(), 64)
	return modelPrice{
		InputPerMillion: in, OutputPerMillion: out,
		CostInPerMillion: costIn, CostOutPerMillion: costOut,
	}, true
}

// zenWireModel is the /v1/models item shape a family serves. Prices are JSON strings
// decoded straight into decimal (decimal.UnmarshalJSON), preserving the exact value.
// There is deliberately no upstream field to read.
type zenWireModel struct {
	ID            string `json:"id"`
	OwnedBy       string `json:"owned_by"`
	Access        string `json:"access"`   // "" | "waitlist" — access gating advertised by the family
	MinTier       string `json:"min_tier"` // "" | "free" | "trial" | "paid" — min subscription tier advertised for this SKU (Seams A/B)
	Funding       string `json:"funding"`  // "prepaid" = every path this SKU can take spends a real-cash balance
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
		ID: w.ID, OwnedBy: w.OwnedBy, MaxCtx: w.ContextWindow, Vision: w.Capabilities.Vision, Access: w.Access, MinTier: w.MinTier, Funding: w.Funding,
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

// decodeCatalog translates a family's raw /v1/models body into discovered SKUs using
// the family's own dialect, defaulting to the Hanzo family wire shape.
func (f *modelFamily) decodeCatalog(body []byte) ([]zenModel, error) {
	if f.decode != nil {
		return f.decode(body)
	}
	var out struct {
		Data []zenWireModel `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	models := make([]zenModel, 0, len(out.Data))
	for _, w := range out.Data {
		models = append(models, w.model())
	}
	return models, nil
}

const zenCatalogTTL = 5 * time.Minute

// engineModel is the id the engine answers to for whatever it currently has loaded.
// It is an alias on the engine's side, so the pool names the ROLE rather than a
// checkpoint that changes when the deployment loads a different one.
const engineModel = "default"

// spareTries bounds how many of a vendor's free routes one refused request may be
// offered to. Three, the same number of attempts the failover loop allows across
// vendors (maxProviders) and for the same reason: worst-case latency has to be a
// property of a constant here rather than of a catalogue an operator does not own.
const spareTries = 3

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
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	models, err := f.decodeCatalog(body)
	if err != nil {
		return err
	}
	byID := make(map[string]zenModel, len(models))
	ids := make([]string, 0, len(models))
	for _, m := range models {
		if strings.TrimSpace(m.ID) == "" {
			continue
		}
		byID[strings.ToLower(m.ID)] = m
		ids = append(ids, m.ID)
	}
	var spares []string
	if f.spare != nil {
		spares = f.spare(body)
	}
	f.mu.Lock()
	f.byID = byID
	f.ids = ids
	f.spares = spares
	f.fetchedAt = time.Now()
	f.loaded = true
	f.mu.Unlock()
	return nil
}

// spareRoutes are the routes this family serves once its account is spent, best
// first. Empty for a family that declared none, and for one whose discovery has
// never succeeded — an unknown vendor gets no fallback invented for it.
func (f *modelFamily) spareRoutes() []string {
	f.fresh()
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.spares
}

// free reports that this family serves the named route for nothing. Read from the
// discovered price, which is the same number the ledger bills, so what a call costs
// and the terms it is served under are one fact.
func (f *modelFamily) free(id string) bool {
	m, ok := f.lookup(id)
	return ok && !m.priced()
}

// isSpare reports whether a SKU is one of the free routes rather than a catalog
// entry. It is what prices a spare at nothing: the vendor charges us nothing for
// it — that is the whole reason it can answer at all when the account is empty —
// so billing a customer retail for one would be charging for a downgrade.
func (f *modelFamily) isSpare(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, s := range f.spares {
		if strings.ToLower(s) == model {
			return true
		}
	}
	return false
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
	// The family's own free id is served by choosing from the platform pool, so it
	// belongs to this family whether or not its vendor has ever heard of it — but
	// only while the pool actually holds something to choose.
	if f.frontDoor(model) {
		return len(freeRoutes()) > 0
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
	// The family's free id is served from the pool, and the pool is the routes that
	// cost nothing. It is a stated price of zero, not an absent one — which is what
	// keeps the balance gate from asking a caller to fund a call that bills nothing.
	if f.frontDoor(model) {
		return modelPrice{}, true
	}
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
	if w := f.window(model); w > 0 {
		ctx = w // guarantee the flagship served window
	}
	p, priced := f.modelPrice(model)
	return &modelRoute{
		providerName:  f.name,
		upstreamModel: model, // identity: the family maps the SKU to its real upstream
		// Premium is a claim about money, so the price makes it — the free id and the
		// free routes are not premium, and saying they were would read to a client as
		// a model their plan cannot afford.
		premium:       !priced || p.InputPerMillion > 0 || p.OutputPerMillion > 0,
		ownedBy:       f.owner, // public branding: enso→"hanzo", zen→"zenlm"
		contextWindow: ctx,
	}
}

// mergeModels overlays this family's DISCOVERED lineup onto the base /v1/models list:
// a discovered SKU wins over any same-named base entry, new SKUs are appended, and
// each carries its served context window (from discovery, raised to the guaranteed
// window for the flagship SKUs).
//
// Discovery is the only source of a listing. ai never adds a SKU the family does not
// serve: a listed model that 404s upstream is worse than an absent one, and it is how
// enso-pro reached /v1/models with no price at all.
func (f *modelFamily) mergeModels(base []modelInfo) []modelInfo {
	zs := f.snapshot()
	now := time.Now().Unix()
	if len(zs) == 0 {
		// A family whose own lineup is empty still publishes its free id: that id is
		// served from the platform pool, not from anything this family discovered.
		if infos := f.freeInfo(now); len(infos) > 0 {
			base = append(base, infos...)
			sort.Slice(base, func(i, j int) bool { return base[i].ID < base[j].ID })
		}
		return base
	}
	idx := make(map[string]int, len(base))
	for i, m := range base {
		idx[strings.ToLower(m.ID)] = i
	}
	upsert := func(info modelInfo) {
		k := strings.ToLower(info.ID)
		if i, ok := idx[k]; ok {
			base[i] = info
		} else {
			idx[k] = len(base)
			base = append(base, info)
		}
	}
	for _, z := range zs {
		owner := z.OwnedBy
		if owner == "" {
			owner = f.owner // public branding default: enso→"hanzo", zen→"zenlm"
		}
		window := z.MaxCtx
		if w := f.window(z.ID); w > 0 {
			window = w // flagship SKUs report their guaranteed served window
		}
		info := modelInfo{
			// Premium is a statement about money, so the price is what makes it:
			// a SKU the family charges for is premium, and one it charges nothing
			// for is not. A free route reported as premium reads to a client as a
			// SKU their plan cannot afford, which is the opposite of true.
			ID: z.ID, Object: "model", Created: now, OwnedBy: owner, Premium: z.priced(),
			Pricing: pricingInfo(z.price()), ContextWindow: window, Outputs: z.Outputs,
		}
		// A gated SKU is LISTED but access-controlled; advertise the default standing
		// ("waitlist"). ListModels upgrades this to the caller's real status when authed.
		if z.gated() {
			info.Access = &modelAccessInfo{State: "waitlist"}
		}
		upsert(info)
	}
	for _, info := range f.freeInfo(now) {
		upsert(info)
	}
	sort.Slice(base, func(i, j int) bool { return base[i].ID < base[j].ID })
	return base
}

// freeInfo is the listing entry for this family's free router, present only while
// the pool holds a route to serve it with — a listed model that answers nothing is
// worse than an absent one. Its context is the narrowest in the pool, because that
// is the prompt every route in the pool can hold, and the widest would promise a
// window the answer might not have.
func (f *modelFamily) freeInfo(now int64) []modelInfo {
	names := f.freeNames()
	routes := freeRoutes()
	if len(names) == 0 || len(routes) == 0 {
		return nil
	}
	window := 0
	for _, r := range routes {
		m, ok := r.fam.lookup(r.id)
		if !ok {
			continue
		}
		if window == 0 || (m.MaxCtx > 0 && m.MaxCtx < window) {
			window = m.MaxCtx
		}
	}
	out := make([]modelInfo, 0, len(names))
	for _, n := range names {
		out = append(out, modelInfo{
			ID: n.id, Object: "model", Created: now, OwnedBy: n.owner,
			Premium: false, ContextWindow: window, Outputs: []string{"text"},
			Pricing: &modelPricingInfo{Input: 0, Output: 0},
		})
	}
	return out
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

// withModel returns body with its top-level "model" field set to model, preserving
// every other field. It reconciles the forwarded body with the model ai resolved: an
// `auto`/`zen-router` request rewrites request.Model to a concrete SKU while the raw
// body still says "auto", and the family serves by the body's model field. RawMessage
// preserves all sibling fields losslessly; on any parse/marshal error the body is
// returned unchanged so the rewrite never drops a request (the family then surfaces a
// precise error on the original body).
func withModel(body []byte, model string) []byte {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return body
	}
	mb, err := json.Marshal(model)
	if err != nil {
		return body
	}
	m["model"] = mb
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// pipeToFamily forwards the caller's request to the family service verbatim and relays
// the response back unchanged — the family owns identity, reasoning, upstream, and
// dialect; ai only authenticates, meters, and forwards. apiPath is "messages"
// (Anthropic) or "chat/completions" (OpenAI). Billing settles from the response usage
// at the discovered exact retail price; the settle is idempotent with any deferred
// fail-safe settle at the call site.
// It returns nil when the request is finished with — served, or refused for a
// reason no other vendor would fix, in which case the client already has the
// answer. It returns the refusal when the FAMILY could not serve it and NOTHING
// has been written to the client, so the caller is free to offer the same
// request to the route's declared alternates. That distinction is the whole
// reason a vendor running out of money stopped being an outage: the family is
// one provider among several, and a 402 from it is a fact about that vendor's
// balance, not about the request.
//
// The two answers differ in what they owe the reservation, and it follows from
// what they mean:
//
//	nil       — the request is over, so the hold is RELEASED. Nothing downstream
//	            is going to run and settle it.
//	a refusal — the request is still moving, so the hold is UNTOUCHED. settle is
//	            one-shot, and releasing it here would leave whatever provider
//	            eventually serves this request unbilled.
//
// So: returning nil settles, always, via done. The success path has already
// settled the real cost by then and settle is idempotent, which is what lets one
// rule cover every ending rather than each ending remembering for itself — which
// is how a 400 from an embeddings family came to hold a customer's cents with
// nothing left running that would ever give them back.
func (c *ApiController) pipeToFamily(fam *modelFamily, apiPath, dialect, model string, rawBody []byte, stream bool, orgId string, authUser *iam.User, isPremium bool, hold *budgetHold, start time.Time) []attempt {
	// done ends the request here: the client has its answer, or has gone.
	done := func() []attempt {
		hold.settle(0)
		return nil
	}

	// sku is the route being asked for. requested is what the CALLER named, and is
	// set only when a different route ends up answering — so it is both the tag on
	// the served generation and the test for whether this was a fallback at all.
	sku, requested := model, ""

	// note records what one refusal says about the account that answered it: the
	// account rests, and somebody is told. Separate from refused because a refusal
	// the request RECOVERS from is still a refusal — an empty account nobody logs
	// is an empty account nobody tops up.
	note := func(err error) attempt {
		cooled.rest(orgId, fam.name, err)
		at := attempt{
			provider: fam.name,
			upstream: sku,
			origin:   fam.name,
			status:   upstreamHTTPStatus(err),
			fault:    faultProvider,
			err:      err,
		}
		announce(model, at)
		return at
	}

	// refused reports a provider-side failure with nothing written: the request
	// is still movable, so hand the reason back rather than spending it on an
	// error page.
	refused := func(err error) []attempt {
		// The caller hung up. There is nobody left to serve, so offering the
		// request to a second vendor would spend money answering an empty room —
		// and would blame a healthy vendor for the client's disconnect.
		if c.Ctx.Request.Context().Err() != nil {
			c.EnableRender = false
			return done()
		}
		return []attempt{note(err)}
	}

	prov := fam.providerFn()
	if prov == nil {
		// Unconfigured or switched off. From the request's point of view that is
		// a provider that cannot serve, so try the alternates before saying no.
		return refused(&apiError{status: http.StatusServiceUnavailable, msg: fam.name + " service is not configured"})
	}
	// Access gate (the ONE choke point): a gated SKU (enso limited preview) is
	// callable only with a granted ModelAccess row. Discovery tells us which models
	// are gated (zenModel.gated), so this is policy learned from data, not a hardcode.
	// The deferred hold.settle(0) at the call site releases the reservation on this
	// early return, so a denied request is never billed.
	if msg := c.familyRefusal(fam, model, orgId, authUser); msg != "" {
		// OUR gate, not the vendor's: this caller may not have this SKU. No other
		// provider changes that, so answer it here.
		c.zenError(dialect, msg, http.StatusForbidden)
		return done()
	}
	// Forward the model ai RESOLVED, not the caller's raw model id. An
	// `auto`/`zen-router` request rewrote request.Model to a concrete family SKU, but
	// the raw body still carries "model":"auto" — and the family serves by the body's
	// model field, so it 404s on the unknown id "auto" while a direct request for the
	// same SKU (body model == SKU) serves. Reconcile the forwarded body's model with
	// the resolved SKU; byte-identical for a direct request (model already matches).
	rawBody = withModel(rawBody, model)
	reqID := uuid.NewString()
	// The family relays whoever it bought the inference from, so the answer leaves
	// through our door wearing our id, the SKU asked for, and the seller (see
	// envelope.go). The id is the one this call is already metered under, which is
	// also what the reward join keys on, so the customer, the ledger and the routing
	// event all name this completion the same way.
	//
	// EVERY relayed dialect gets a mark. Only chat had one, so the Anthropic
	// dialect — which is what Claude Code and every agent on that SDK speaks — and
	// the embeddings shape went on publishing the sub-provider's name, `gen-` ids
	// and a finish reason we do not define, long after the chat path stopped. The
	// id shape differs because the dialects do: a message is msg_, a completion is
	// chatcmpl-, and an embeddings list has no id at all.
	mk := &mark{model: model, seller: seller(prov, authUser)}
	switch {
	case dialect == "anthropic":
		mk.speaks, mk.id = messageShape, "msg_"+reqID
	case apiPath == "embeddings":
		mk.speaks = listShape
	default:
		mk.speaks, mk.id = chatShape, "chatcmpl-"+reqID
	}
	url := prov.ProviderUrl + "/v1/" + apiPath
	// One build, one place to fail: the method, the URL and the headers do not vary
	// by SKU, so a request that cannot be built cannot be built for any of them.
	req, err := http.NewRequestWithContext(c.Ctx.Request.Context(), http.MethodPost, url, nil)
	if err != nil {
		// Our own request is malformed. Another vendor would not fix that.
		c.zenError(dialect, "build "+fam.name+" request: "+err.Error(), http.StatusInternalServerError)
		return done()
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

	// dispatch offers this request to ONE route of ONE family: the family decides the
	// address and the credential, the sku decides the model, and every other part of
	// the request is the caller's own. That is what lets a route on a DIFFERENT
	// vendor answer a request this one cannot — the vendor that ran out of money is
	// rarely the one still holding a free route.
	dispatch := func(f *modelFamily, s string) (*http.Response, error) {
		p := prov
		if f != fam {
			p = f.providerFn()
			if p == nil {
				return nil, &apiError{status: http.StatusServiceUnavailable, msg: f.name + " service is not configured"}
			}
		}
		r, rErr := http.NewRequestWithContext(c.Ctx.Request.Context(), http.MethodPost, p.ProviderUrl+"/v1/"+apiPath, nil)
		if rErr != nil {
			return nil, rErr
		}
		r.Header = req.Header.Clone()
		if p.ClientSecret != "" {
			r.Header.Set("Authorization", "Bearer "+p.ClientSecret)
		}
		b := withModel(rawBody, s)
		if f.terms != nil {
			b = f.terms(b, f.free(s))
		}
		r.Body = io.NopCloser(bytes.NewReader(b))
		r.ContentLength = int64(len(b))
		// GetBody is what http.NewRequest sets for a bytes.Reader and what the
		// transport needs to replay a body across a redirect or an HTTP/2 refusal.
		// Setting the body by hand without it silently drops that.
		r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }
		return zenPipeClient.Do(r)
	}

	send := func(s string) (*http.Response, error) { return dispatch(fam, s) }

	// pool walks the platform's free routes in order and reports the one that
	// answered. It is the whole of what a free router does, and the whole of what a
	// refusal falls back to, so both are one walk rather than two that can drift.
	//
	// A SHORT walk, not the whole list. More than one try because a free tier is rate
	// limited by design — the first route is about as likely to answer 429 as to
	// answer — and bounded because every try is a round trip the caller waits
	// through, so worst-case latency is a property of spareTries rather than of how
	// many free routes a vendor happens to advertise.
	pool := func(skip string) (*http.Response, string) {
		// The pool holds routes that hold a CONVERSATION. An embeddings request has
		// no free chat route to fall to, and offering it three anyway spends three
		// round trips of the caller's time to be told three times that a chat model
		// does not embed.
		if apiPath == "embeddings" {
			return nil, ""
		}
		tried := 0
		for _, alt := range freeRoutes() {
			if strings.EqualFold(alt.id, skip) {
				continue
			}
			if tried == spareTries || c.Ctx.Request.Context().Err() != nil {
				return nil, ""
			}
			tried++
			r, e := dispatch(alt.fam, alt.id)
			if e != nil {
				continue
			}
			if r.StatusCode == http.StatusOK {
				return r, alt.id
			}
			r.Body.Close()
		}
		return nil, ""
	}

	// spared offers the SAME request to a free route once this vendor cannot serve
	// it, and reports the route that answered.
	//
	// The route need not be this vendor's. It used to have to be, which is why the
	// two families whose vendor is reached through a service of their own — enso and
	// zen — had no fallback at all: they declare no free route, so a refusal from
	// either ended the request even while free routes elsewhere were answering.
	//
	// It fires on the vendor being unable to serve at all — its account with us spent,
	// or its own failure — and on nothing else. `down` reads the STATUS first, because
	// a 402 and a 5xx are facts, and consults text only where there is no status to
	// read, and then only a narrow list of phrases measured from real upstreams. A
	// malformed body (400), a model this vendor has not got (404), refused content
	// (422), a rate limit (429) and a timeout all miss it, so none of them can quietly
	// hand the caller a smaller model in place of an error. Our OWN balance gate
	// cannot reach here either — it refuses before a vendor is dialled — so a 402 at
	// this point is a statement about our account with the vendor, never about the
	// customer's.
	//
	// A SHORT walk, not the whole list. More than one try because a free tier is
	// rate limited by design — the first route is about as likely to answer 429 as
	// to answer — and bounded because every try is a round trip the caller waits
	// through, so worst-case latency stays a property of spareTries rather than of
	// how many free routes a vendor happens to advertise.
	spared := func(err error, body []byte) (*http.Response, string) {
		// Our own spend gate, relayed by a service that fronts for us, wears the
		// same 402 and means the opposite thing: the CUSTOMER owes money. That has
		// to reach them. Answering it with a free model would tell somebody their
		// payment problem had fixed itself.
		if billingNotice(body) {
			return nil, ""
		}
		if !down(err, strings.ToLower(err.Error())) || c.Ctx.Request.Context().Err() != nil {
			return nil, ""
		}
		return pool(sku)
	}

	// A free router is served by CHOOSING. The caller named the family's free id,
	// which is not a model any vendor holds — it is this family's name for
	// "whichever free route answers" — so the pool decides, and the answer leaves
	// wearing the route that actually wrote it.
	var resp *http.Response
	if fam.frontDoor(sku) {
		r, alt := pool("")
		if r == nil {
			return refused(&apiError{status: http.StatusServiceUnavailable,
				msg: fmt.Sprintf("model %q: no free route answered", model)})
		}
		mk.model, sku, resp = alt, alt, r
	} else if resp, err = send(sku); err != nil {
		// Never reached the family at all. Nothing is written and nothing is
		// billed — deliberately no recordFamilyUsage here, because its
		// hold.settle would release the reservation one-shot and leave the cost
		// of whichever provider ends up serving this request uncharged.
		return refused(err)
	}
	defer func() { resp.Body.Close() }()

	// ONE status decision, ahead of the split, because the answer to "can this
	// request still be moved" is the same for both shapes. The status arrives
	// BEFORE any byte of a stream is written — the WriteHeader below is still
	// ahead of us — so a streaming request is exactly as movable here as a
	// buffered one. This is the only window in which that is true, which is why
	// the decision lives at the status and not somewhere down in the relay.
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		err := &apiError{status: resp.StatusCode, msg: upstreamErrorMessage(b)}
		r, alt := spared(err, b)
		if r == nil {
			if faultOf(err) == faultProvider {
				return refused(err)
			}
			c.zenError(dialect, upstreamErrorMessage(b), resp.StatusCode)
			return done()
		}
		// The account is empty and this vendor still serves that route for
		// nothing. The refusal is recorded anyway — it is why the caller is about
		// to get a different model, and the account it names does not top itself
		// up — and the answer leaves wearing the SKU that actually made it.
		note(err)
		resp.Body.Close()
		resp, requested, sku = r, model, alt
		mk.model = alt
		log.Warn("family=%s is out of money (402) — %s served by the free route %s; the client is told which",
			fam.name, model, alt)
	}

	// The client is told the terms its answer was served under, because a free route
	// is free in exchange for what the vendor keeps and somebody has to be able to
	// say so — in a consent notice, in a log, in a policy page. Set before any byte
	// of a stream goes out, which is the last moment a header can be set at all.
	if served := servingFamily(sku); served != nil && served.terms != nil {
		c.Ctx.Output.Header(headerCollection, collection(served.free(sku)))
	}

	prompt, completion := 0, 0
	served, respID := "", ""
	if stream {
		c.Ctx.ResponseWriter.Header().Set("Content-Type", "text/event-stream")
		c.Ctx.ResponseWriter.Header().Set("Cache-Control", "no-cache")
		c.Ctx.ResponseWriter.Header().Set("Connection", "keep-alive")
		c.Ctx.ResponseWriter.WriteHeader(http.StatusOK)
		prompt, completion, served, respID = c.relayZenStream(resp.Body, mk)
	} else {
		b, rErr := io.ReadAll(resp.Body)
		if rErr != nil {
			// The family's answer was truncated on the way to us. Nothing has
			// reached the client, so this is still movable.
			return refused(rErr)
		}
		sniffZenUsage(b, &prompt, &completion)
		served = sniffZenModel(b)
		respID = sniffZenId(b)
		ct := resp.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/json"
		}
		c.Ctx.Output.Header("Content-Type", ct)
		c.Ctx.Output.Body(mk.stamp(b))
	}

	// A stamped answer carries OUR id, so that is the id the client will thread back
	// to /v1/feedback and the id the reward has to join on.
	// An embeddings list has no id of its own, so nothing is stamped and the join
	// falls back to the internal reqID — which is honest: the client was handed no
	// id to correlate on.
	if mk.id != "" {
		respID = mk.id
	}
	if prompt == 0 {
		prompt = coarseTokenEstimate(rawBody)
	}
	cents := c.recordFamilyUsage(fam, sku, requested, prov, mk, authUser, isPremium, stream, reqID, prompt, completion, start, hold, "success", "")
	// Learning ledger: record this served family call (source="family") and, when the
	// engine endpoint is configured, the shadow A/B pick — off the hot path, no prompt
	// text ever stored (the ledger holds none). The join key is the response id the
	// CLIENT sees (respID), which the client threads back to /v1/feedback; falls back
	// to the internal reqID when the family did not disclose one. See object.RoutingEvent.
	c.recordFamilyRouting(model, served, respID, reqID, rawBody, orgId, authUser, prompt, completion, cents, start)
	c.EnableRender = false
	return done()
}

// relayZenStream copies a family's SSE response to the client and captures the final
// usage for billing. The family already emits correct dialect SSE, so ai does not
// translate — it reads usage as it passes and, for the one dialect whose envelope is
// ours (a non-nil mark), stamps each event before it goes out. Every chunk of one
// completion is stamped from the same mark, so the id a client correlates on holds
// for the whole stream.
func (c *ApiController) relayZenStream(body io.Reader, mk *mark) (prompt, completion int, served, respID string) {
	w := c.Ctx.ResponseWriter
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
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
				if mk != nil {
					line = append([]byte("data: "), mk.stamp(payload)...)
				}
			}
		}
		_, _ = w.Write(line)
		_, _ = w.Write(zenNewline)
		w.Flush()
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
// for every family. origin is who actually served the call (originOf) — the row's
// answer to a question the response no longer answers.
// model is the SKU that ANSWERED and requested the one the caller named, set only
// when a spare route answered in its place. Both travel because the ledger is
// answering two questions — what did we serve, and was it what was asked for — and
// a row that can only answer the first cannot be read as a downgrade at all.
func (c *ApiController) recordFamilyUsage(fam *modelFamily, model, requested string, prov *object.Provider, mk *mark, authUser *iam.User, isPremium, stream bool, reqID string, prompt, completion int, start time.Time, hold *budgetHold, status, errMsg string) int64 {
	// The vendor charges nothing for a spare route — that is the whole reason it can
	// answer while the account is empty — so it is billed at nothing. Charging
	// retail for a downgrade the customer did not choose would be taking money for
	// the outage. Stated once, on the record, so the hold, the debit, the warehouse
	// row and the span all read the same zero (see usageCostNano).
	free := fam.isSpare(model) || freeFamily().isSpare(model)
	var cents int64
	if status == "success" && !free {
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
		Owner: c.billingOrg(authUser), Organization: authUser.Owner,
		Model: model, Requested: requested, Free: free, Provider: fam.name, Origin: originOf(prov, mk),
		PromptTokens: prompt, CompletionTokens: completion, TotalTokens: prompt + completion,
		Cost: float64(cents) / 100.0, Currency: "USD",
		Premium: isPremium, Stream: stream, Status: status, ErrorMsg: errMsg,
		ClientIP: c.Ctx.Request.RemoteAddr, RequestID: reqID, Account: "hanzo",
		// What the call cost us to buy, when the answer stated it, beside what we
		// charged for it. usageMargin reads this as the COGS, so the margin on a
		// relayed call stops being a guess.
		CostNanoExact: mk.cogs(),
	}
	rec.bind(c.Ctx.Request.Context(), authUser)
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
// synchronously from rawBody before the goroutine spawns, so nothing reads the router
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
