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

// openrouter_catalog.go — the OpenRouter catalog as a discovered model family.
//
// OpenRouter is a third instance of modelFamily, not a second catalog mechanism. It
// discovers on the same TTL, lists through the same merge, routes through the same
// passthrough, and is gated by the same two floors as Zen and Enso. Only two things
// are genuinely different, and each is expressed as a value on the family rather than
// a branch in the shared machinery:
//
//	the DIALECT — OpenRouter states price per TOKEN and advertises neither a
//	    subscription tier nor a funding class, so openrouterCatalog translates its
//	    wire shape into the same discovered SKU every other family produces.
//	the ECONOMICS — the PRICE decides the floors. A SKU OpenRouter charges for is
//	    resale: serving one spends real cash, so it carries Funding "prepaid" and
//	    MinTier "paid" and the existing funding floor (familyFundingAllowed,
//	    fail-closed) refuses a caller we cannot confirm is paying. A SKU it charges
//	    nothing for spends nothing, so it carries neither floor and every tier may
//	    call it. The floors are not re-implemented here; they are fed the one fact
//	    that decides them.
//
// Retail is the upstream price times a margin, floored at cost, and the upstream price
// travels beside it as COGS so the margin ledger books the real spread.
//
// The listing is the vendor's listing: every SKU OpenRouter advertises is a SKU this
// yields. The free ones are read a SECOND time by openrouterSpare, which keeps only
// those that answer in text, so the routes a refusal falls back to are a subset of the
// free routes rather than a separate discovery.

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/decimal"
)

// openrouterFam is the OpenRouter catalog. Its address is configuration
// (OPENROUTER_URL, e.g. https://openrouter.ai/api) exactly like Zen's and Enso's, so
// an unconfigured deployment serves and lists nothing of it. The prefix is the one
// public id OpenRouter itself brands ("openrouter/auto"); every other SKU is served
// because discovery resolved it, never because its name looked a certain way.
var openrouterFam = &modelFamily{
	name:        "openrouter",
	typ:         "OpenRouter",
	prefix:      "openrouter/",
	owner:       "openrouter",
	urlKey:      "OPENROUTER_URL",
	keyKey:      "OPENROUTER_API_KEY",
	keyNames:    object.OpenRouterKeys,
	credits:     openrouterCredits,
	creditsPath: "/v1/credits",
	providerFn:  object.OpenRouterProvider,
	decode:      openrouterCatalog,
	terms:       openrouterTerms,
	spare:       openrouterSpare,
	aliases:     openrouterAliases,
}

// The catalog is read from the store the sync keeps (listing.go); assigned here
// because the sync reads the family.
func init() { openrouterFam.load = openrouterFam.listed }

// openrouterCredits reads GET /v1/credits: {"data":{"total_credits":20,
// "total_usage":20.21}}, lifetime totals whose difference is what the account has
// left. It goes below zero when usage overruns what was bought.
func openrouterCredits(b []byte) (float64, error) {
	var r struct {
		Data *struct {
			Credits float64 `json:"total_credits"`
			Usage   float64 `json:"total_usage"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return 0, err
	}
	if r.Data == nil {
		return 0, errNoCredits
	}
	return r.Data.Credits - r.Data.Usage, nil
}

var errNoCredits = errors.New("credits answer carries no data")

// openrouterAuto is the id OpenRouter gives its own free router: one request to it
// is served by whichever free route the vendor has up at that moment.
//
// It is kept OUT of the pool below, and the reason is the pool's whole purpose. The
// pool is the set of routes we can say something about; this one is a router, so what
// it answers with is not knowable in advance. Measured live: one request to it was
// served by a coding model and the next by a content-safety classifier, which replied
// "User Safety: safe" to a chat prompt. Choosing it would be delegating the choice
// the pool exists to make.
//
// It stays in the CATALOG — it is a real id a caller may name deliberately, and the
// curation narrows what we fall back to, never what we publish.
const openrouterAuto = "openrouter/free"

var openrouterTokensPerMillion = decimal.New(1_000_000, 0)

// openrouterMarkup is the multiple a model resold from OpenRouter bills over the price
// OpenRouter states: 1 + OpenRouter's fee, a percent the host's operator sets
// (object.Fee; 5.5 is what OpenRouter charges on the credit it sells). A fee that is
// not a percent of 0 or more is an error, and the catalog stays as it was: a resold
// model is never priced below what its vendor charges.
func openrouterMarkup() (decimal.Decimal, error) {
	one := decimal.New(1, 0)
	raw := strings.TrimSpace(object.Fee("openrouter"))
	if raw == "" {
		return one, nil
	}
	pct, err := decimal.Parse(raw)
	if err != nil || pct.Sign() < 0 {
		return decimal.Decimal{}, fmt.Errorf("openrouter fee %q is not a percent of 0 or more", raw)
	}
	return one.Add(pct.Mul(decimal.New(1, 2))), nil
}

// openrouterWireModel is the subset of OpenRouter's /v1/models item ai needs to list,
// route, and bill a SKU. Prices are JSON strings in USD per TOKEN, decoded straight
// into decimal so the conversion to $/MTok stays exact and never passes through float.
type openrouterWireModel struct {
	ID string `json:"id"`
	// Name is OpenRouter's display name, "<Vendor>: <model>".
	Name        string `json:"name"`
	Description string `json:"description"`
	// Created is when OpenRouter listed the model (Unix seconds): the release time the
	// catalog knows, which /v1/models reports as `created`.
	Created       int64             `json:"created"`
	ContextLength int               `json:"context_length"`
	Pricing       openrouterPricing `json:"pricing"`
	TopProvider   struct {
		// MaxCompletionTokens is the most one answer may hold; 0 when unstated.
		MaxCompletionTokens int `json:"max_completion_tokens"`
	} `json:"top_provider"`
	// Expiration is the day OpenRouter stops serving the SKU (2026-10-31); empty when
	// it states none.
	Expiration   string `json:"expiration_date"`
	Architecture struct {
		InputModalities  []string `json:"input_modalities"`
		OutputModalities []string `json:"output_modalities"`
	} `json:"architecture"`
	// SupportedParameters are the request fields the SKU honours ("tools",
	// "reasoning", …): what it can be asked to do.
	SupportedParameters []string `json:"supported_parameters"`
}

// openrouterPricing is a SKU's price list: every rate OpenRouter states, in its own
// names and units (USD per token, per request, per image, per search), and the
// conditional rates it lists under overrides. The rates billing reads by name are
// decoded exactly; the whole list is kept so every rate can be published.
type openrouterPricing struct {
	Prompt     decimal.Decimal
	Completion decimal.Decimal
	CacheRead  decimal.Decimal // input_cache_read
	// Larger are the rates past a prompt size, ascending by it: the overrides that
	// state min_prompt_tokens. An override bounded by hours of the day is a discount
	// on the base rates, so billing never reads one and never bills below the base.
	Larger []openrouterOverride
	// All is the price list as listed, each value as the vendor wrote it.
	All map[string]json.RawMessage
}

// openrouterOverride is the rates from a prompt of Min tokens on; a rate it does not
// state is the base rate.
type openrouterOverride struct {
	Min                           int
	Prompt, Completion, CacheRead decimal.Decimal
}

func (p *openrouterPricing) UnmarshalJSON(b []byte) error {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return err
	}
	rate := func(m map[string]json.RawMessage, k string, or decimal.Decimal) (decimal.Decimal, error) {
		raw, ok := m[k]
		if !ok {
			return or, nil
		}
		var d decimal.Decimal
		err := d.UnmarshalJSON(raw)
		return d, err
	}
	var err error
	*p = openrouterPricing{All: all}
	if p.Prompt, err = rate(all, "prompt", decimal.Zero()); err != nil {
		return err
	}
	if p.Completion, err = rate(all, "completion", decimal.Zero()); err != nil {
		return err
	}
	if p.CacheRead, err = rate(all, "input_cache_read", decimal.Zero()); err != nil {
		return err
	}
	var overrides []map[string]json.RawMessage
	if raw, ok := all["overrides"]; ok {
		if err := json.Unmarshal(raw, &overrides); err != nil {
			return err
		}
	}
	for _, o := range overrides {
		var min int
		if raw, ok := o["min_prompt_tokens"]; !ok || json.Unmarshal(raw, &min) != nil || min <= 0 {
			continue
		}
		if _, timed := o["utc_start"]; timed {
			continue
		}
		v := openrouterOverride{Min: min}
		if v.Prompt, err = rate(o, "prompt", p.Prompt); err != nil {
			return err
		}
		if v.Completion, err = rate(o, "completion", p.Completion); err != nil {
			return err
		}
		if v.CacheRead, err = rate(o, "input_cache_read", p.CacheRead); err != nil {
			return err
		}
		p.Larger = append(p.Larger, v)
	}
	sort.Slice(p.Larger, func(i, j int) bool { return p.Larger[i].Min < p.Larger[j].Min })
	return nil
}

// card is the price list as it bills to a caller: each rate times the markup, exact, and
// each override's rates the same with its condition kept. A value that is not a
// decimal string (a condition, a list of days) is carried as listed.
func (p openrouterPricing) card(margin decimal.Decimal) *rateCard {
	c := &rateCard{Rates: map[string]string{}}
	for k, raw := range p.All {
		if k == "overrides" {
			continue
		}
		if r, ok := retailRate(raw, margin); ok {
			c.Rates[k] = r
		}
	}
	var overrides []map[string]json.RawMessage
	if raw, ok := p.All["overrides"]; ok && json.Unmarshal(raw, &overrides) == nil {
		for _, o := range overrides {
			out := make(map[string]any, len(o))
			for k, raw := range o {
				if r, ok := retailRate(raw, margin); ok {
					out[k] = r
				} else {
					out[k] = raw
				}
			}
			c.Overrides = append(c.Overrides, out)
		}
	}
	return c
}

// retailRate is a listed rate (a decimal string) times the markup, or false for a value
// that is no rate.
func retailRate(raw json.RawMessage, margin decimal.Decimal) (string, bool) {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	d, err := decimal.Parse(s)
	if err != nil {
		return "", false
	}
	return d.Mul(margin).String(), true
}

// title is the display name without its vendor lead: "Anthropic: Claude Sonnet 4" is
// "Claude Sonnet 4", since owned_by already names the vendor. A name with no lead is
// kept whole.
func (w openrouterWireModel) title() string {
	name := strings.TrimSpace(w.Name)
	if _, rest, ok := strings.Cut(name, ": "); ok && strings.TrimSpace(rest) != "" {
		return strings.TrimSpace(rest)
	}
	return name
}

// takes reports whether the SKU honours any of the named request parameters.
func (w openrouterWireModel) takes(params ...string) bool {
	for _, p := range w.SupportedParameters {
		for _, want := range params {
			if strings.EqualFold(strings.TrimSpace(p), want) {
				return true
			}
		}
	}
	return false
}

// vision reports whether the SKU accepts image input, read from the modalities
// OpenRouter advertises — absent means not advertised, never a fabricated yes.
func (w openrouterWireModel) vision() bool {
	for _, m := range w.Architecture.InputModalities {
		if strings.EqualFold(strings.TrimSpace(m), "image") {
			return true
		}
	}
	return false
}

// model translates one OpenRouter listing into a discovered SKU at the given margin:
// per-token USD becomes per-MTok, retail becomes cost times margin, and the upstream
// price is retained as COGS.
//
// The floors follow the price, because the price is what they are about. A SKU with a
// price spends real cash per call, so it takes both — the subscription floor that
// keeps it out of a free plan's reach and the funding floor that refuses a caller we
// cannot confirm is paying. A SKU priced at zero spends nothing, so it takes neither
// and any tier may call it. Stamping the paid floors on a free SKU would refuse a
// caller over money that is never spent.
func (w openrouterWireModel) model(margin decimal.Decimal) zenModel {
	costIn := w.Pricing.Prompt.Mul(openrouterTokensPerMillion)
	costOut := w.Pricing.Completion.Mul(openrouterTokensPerMillion)
	m := zenModel{
		ID:          w.ID,
		Created:     w.Created,
		OwnedBy:     openrouterOwner(w.ID),
		Name:        w.title(),
		Description: strings.TrimSpace(w.Description),
		MaxCtx:      w.ContextLength,
		MaxOut:      w.TopProvider.MaxCompletionTokens,
		Expires:     strings.TrimSpace(w.Expiration),
		Vision:      w.vision(),
		Tools:       w.takes("tools"),
		Reasoning:   w.takes("reasoning", "include_reasoning"),
		Inputs:      w.Architecture.InputModalities,
		Outputs:     w.Architecture.OutputModalities,
		CostIn:      costIn,
		CostOut:     costOut,
		Margin:      margin,
		Card:        w.Pricing.card(margin),
	}
	m.Tiers = w.tiers(margin)
	m.Base = m.Tiers[0]
	if !w.free() {
		m.MinTier = "paid"    // subscription floor: not reachable by free/trial
		m.Funding = "prepaid" // funding floor: real cash, so the fail-closed gate applies
	}
	if w.variable() {
		// Billed per call at its stated cost; openrouterCatalog sets the ceiling. The
		// -1 is a marker, not a cost, so no COGS is derived from it.
		m.Variable, m.CostIn, m.CostOut, m.Card = true, decimal.Zero(), decimal.Zero(), nil
	}
	return m
}

// tiers is the SKU's retail ladder: the base rates up to the first override's prompt
// size, each override's from its own, the last to the context window. A tier holds
// prompts up to its MaxCtx (tierFor), so the prompt that reaches an override's size
// bills at the override, the dearer side of the line.
func (w openrouterWireModel) tiers(margin decimal.Decimal) []zenTier {
	tier := func(in, out, cache decimal.Decimal, max int) zenTier {
		return zenTier{
			MaxCtx:    max,
			In:        in.Mul(openrouterTokensPerMillion).Mul(margin),
			Out:       out.Mul(openrouterTokensPerMillion).Mul(margin),
			CacheRead: cache.Mul(openrouterTokensPerMillion).Mul(margin),
		}
	}
	p := w.Pricing
	last := tier(p.Prompt, p.Completion, p.CacheRead, w.ContextLength)
	var out []zenTier
	for _, o := range p.Larger {
		last.MaxCtx = o.Min - 1
		out = append(out, last)
		last = tier(o.Prompt, o.Completion, o.CacheRead, max(w.ContextLength, o.Min))
	}
	return append(out, last)
}

// variable reports a SKU OpenRouter prices by whatever serves each call — its routers,
// which it lists at a price of -1 because no one rate describes them.
func (w openrouterWireModel) variable() bool {
	return w.Pricing.Prompt.Sign() < 0 || w.Pricing.Completion.Sign() < 0
}

// openrouterOwner is the vendor an OpenRouter id names ("anthropic/claude-…" →
// "anthropic"), so a listing attributes each model to whoever actually made it rather
// than to the router in front of it. An id with no vendor segment falls back to the
// family's own name.
func openrouterOwner(id string) string {
	if i := strings.IndexByte(id, '/'); i > 0 {
		return strings.ToLower(id[:i])
	}
	return "openrouter"
}

// openrouterWire reads the listing. One parse, because the catalog and the spare
// routes below are two readings of the same body and a second unmarshal is a second
// place for the wire shape to drift.
func openrouterWire(body []byte) ([]openrouterWireModel, error) {
	var out struct {
		Data []openrouterWireModel `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// free reports the SKUs OpenRouter charges nothing for on either side.
func (w openrouterWireModel) free() bool {
	return w.Pricing.Prompt.IsZero() && w.Pricing.Completion.IsZero()
}

// writes reports that this SKU answers in text and in nothing else — the only
// shape a completion request can use.
//
// Free is not the same question as usable, and the live listing is what taught
// that: the two widest free routes OpenRouter advertises are `google/lyria-3-*`,
// which declare `out: ["text","audio"]` because they are MUSIC models. Ordered by
// context they sat at the top of the fallback list, so a chat request refused for
// money would have been handed to a music generator — a wrong answer in place of
// an honest error, which is worse than the outage.
//
// Undeclared modalities read as NOT usable, deliberately. This is the one place
// that decides what a customer gets when their model is unavailable, and guessing
// is not something to do there.
func (w openrouterWireModel) writes() bool {
	out := w.Architecture.OutputModalities
	return len(out) == 1 && strings.EqualFold(strings.TrimSpace(out[0]), "text")
}

// notChat names the kinds of model that answer in text without holding a
// conversation. A classifier does exactly what it was built to do and returns a
// verdict — measured live, `nemotron-3.5-content-safety:free` answered a chat turn
// with "User Safety: safe", which reaches a customer as a broken product rather
// than as the refusal it stands in for.
//
// Matched on the vendor's own naming, and kept to the shapes actually seen rather
// than every word that might one day mean this. The listing states a model's output
// KIND but not its purpose, so purpose is read from the name or not at all — and a
// name is a weaker signal than a modality, which is why this list stays short and
// only ever removes routes from a fallback that has others.
var notChat = []string{"content-safety", "guard", "moderation", "classifier"}

// converses reports that a SKU is one a chat turn can be handed to.
func (w openrouterWireModel) converses() bool {
	id := strings.ToLower(w.ID)
	for _, kind := range notChat {
		if strings.Contains(id, kind) {
			return false
		}
	}
	return true
}

// openrouterCatalog decodes an OpenRouter /v1/models body into discovered SKUs — one
// per SKU the vendor advertises, so what we list is what they serve. A price of zero
// is published as zero and carries the floors that price implies (see model), which is
// what lets a free plan reach the routes that cost nothing.
func openrouterCatalog(body []byte) ([]zenModel, error) {
	wire, err := openrouterWire(body)
	if err != nil {
		return nil, err
	}
	margin, err := openrouterMarkup()
	if err != nil {
		return nil, err
	}
	models := make([]zenModel, 0, len(wire))
	var ceiling zenTier
	for _, w := range wire {
		m := w.model(margin)
		if !m.variable() {
			for _, t := range m.Tiers {
				ceiling.In = maxDecimal(ceiling.In, t.In)
				ceiling.Out = maxDecimal(ceiling.Out, t.Out)
			}
		}
		models = append(models, m)
	}
	// A router may send a call to any SKU in the catalog, so its hold reserves the
	// dearest rate on each side and it is premium. One the catalog cannot bound —
	// nothing else in it is priced — is left out rather than listed free.
	out := models[:0]
	for _, m := range models {
		if m.variable() {
			if ceiling.In.IsZero() && ceiling.Out.IsZero() {
				continue
			}
			ceiling.MaxCtx = m.MaxCtx
			m.Base, m.Tiers = ceiling, []zenTier{ceiling}
		}
		out = append(out, m)
	}
	return out, nil
}

func maxDecimal(a, b decimal.Decimal) decimal.Decimal {
	if b.Cmp(a) > 0 {
		return b
	}
	return a
}

// openrouterSpare names the routes OpenRouter still serves once its account is
// spent: the SKUs it charges nothing for, answers in TEXT with, and can be handed a
// CONVERSATION — drawn from the same listing the catalog above reads. Two readings of one body, so neither list
// needs its own discovery and a priced route can never be handed out as a spare —
// a downgrade that bills is not a remedy.
//
// Ordered by context window, widest first, then by id — a total order over the
// listing, so which route answers is a property of what the vendor advertises and
// not of the order it happened to serialize them in. Widest first because the
// request was already sized for the SKU it asked for; a spare that cannot hold the
// prompt is not a fallback, it is a second refusal.
func openrouterSpare(body []byte) []string {
	wire, err := openrouterWire(body)
	if err != nil {
		return nil
	}
	free := make([]openrouterWireModel, 0, 8)
	for _, w := range wire {
		if w.ID == openrouterAuto {
			continue // a router, not a route — see openrouterAuto
		}
		if w.free() && w.writes() && w.converses() && strings.TrimSpace(w.ID) != "" {
			free = append(free, w)
		}
	}
	sort.Slice(free, func(i, j int) bool {
		if free[i].ContextLength != free[j].ContextLength {
			return free[i].ContextLength > free[j].ContextLength
		}
		return free[i].ID < free[j].ID
	})
	ids := make([]string, 0, len(free))
	for _, w := range free {
		ids = append(ids, w.ID)
	}
	return ids
}

// The vendor's two words for what it may keep of an exchange, and the header the
// answer carries so a client can say which applied without inferring it from a
// price list.
const (
	collectionAllow  = "allow"
	collectionDeny   = "deny"
	headerCollection = "X-Hanzo-Data-Collection"
)

// collection is the word for a route served free or for money. One reading of one
// fact, so the value sent to the vendor and the value reported to the caller can
// never be different words about the same call.
func collection(free bool) string {
	if free {
		return collectionAllow
	}
	return collectionDeny
}

// collection is what this family lets the vendor keep of an exchange on one route,
// in the same word openrouterTerms sends upstream and headerCollection reports back.
// stated is false for a family that declares no terms: it says nothing about
// collection, so there is nothing here to read and nothing to protect.
//
// ONE reading of the fact, asked by the two readers that need it — the header, which
// asks the family that ANSWERED, and the fallback, which asks the family it was
// about to move the request AWAY from. Each passes the family that owns its
// question, which is why the family is the receiver rather than a global lookup: a
// route this family cannot name reads as priced, and about a route we cannot name
// `allow` is the one guess with a customer on the other end of it.
func (f *modelFamily) collection(sku string) (word string, stated bool) {
	if f == nil || f.terms == nil {
		return "", false
	}
	return collection(f.free(sku)), true
}

// openrouterTerms states what OpenRouter may keep of this exchange, in its own
// dialect: `provider.data_collection`.
//
// A free route is free BECAUSE the vendor keeps what it carried — that is the
// trade, and asking for a free route while denying collection asks for the price
// without the terms, which the vendor answers by having no endpoint to serve it.
// A priced route pays instead and keeps nothing.
//
// Written over the caller's own `provider` object rather than replacing it, so a
// caller who states other vendor preferences keeps them.
func openrouterTerms(body []byte, free bool) []byte {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return body
	}
	prov := map[string]json.RawMessage{}
	if raw, ok := m["provider"]; ok {
		_ = json.Unmarshal(raw, &prov)
	}
	word, err := json.Marshal(collection(free))
	if err != nil {
		return body
	}
	prov["data_collection"] = word
	pb, err := json.Marshal(prov)
	if err != nil {
		return body
	}
	m["provider"] = pb
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}
