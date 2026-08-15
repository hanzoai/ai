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
	"sort"
	"strings"

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/decimal"
)

// openrouterFam is the OpenRouter catalog. Its address is configuration
// (OPENROUTER_URL, e.g. https://openrouter.ai/api) exactly like Zen's and Enso's, so
// an unconfigured deployment serves and lists nothing of it. The prefix is the one
// public id OpenRouter itself brands ("openrouter/auto"); every other SKU is served
// because discovery resolved it, never because its name looked a certain way.
var openrouterFam = &modelFamily{
	name:       "openrouter",
	provider:   "OpenRouter",
	prefix:     "openrouter/",
	owner:      "openrouter",
	urlKey:     "OPENROUTER_URL",
	keyKey:     "OPENROUTER_API_KEY",
	providerFn: object.OpenRouterProvider,
	decode:     openrouterCatalog,
	terms:      openrouterTerms,
	spare:      openrouterSpare,
}

// openrouterMarginDefault is the retail multiple applied to the upstream price when
// the deployment configures none: a 20% spread over what OpenRouter charges us.
const openrouterMarginDefault = "1.20"

// openrouterAuto is the id OpenRouter gives its own free router: one request to it
// is served by whichever free route the vendor has up at that moment.
//
// It takes its place in the order below by context width like any other free route,
// and deliberately gets no lift for always answering. What it answers WITH is not
// knowable in advance: measured live, one request to it was served by a coding model
// and the next by a content-safety classifier, which replied "User Safety: safe" to a
// chat prompt. A route whose output shape we cannot read is the same hazard the
// text-only rule below exists for, and the same rule decides it — a wrong answer in
// place of an honest refusal is worse than the refusal.
const openrouterAuto = "openrouter/free"

var openrouterTokensPerMillion = decimal.New(1_000_000, 0)

// openrouterMargin is the configured retail multiple (OPENROUTER_MARGIN), clamped to
// at least 1 so a mis-set or hostile value can never publish retail BELOW cost. That
// clamp is the whole invariant: resale at a loss drains the prepaid balance and takes
// the catalog down for every paying customer.
func openrouterMargin() decimal.Decimal {
	one := decimal.New(1, 0)
	raw := strings.TrimSpace(conf.GetConfigString("OPENROUTER_MARGIN"))
	if raw == "" {
		raw = openrouterMarginDefault
	}
	m, err := decimal.Parse(raw)
	if err != nil || m.Cmp(one) < 0 {
		return one
	}
	return m
}

// openrouterWireModel is the subset of OpenRouter's /v1/models item ai needs to list,
// route, and bill a SKU. Prices are JSON strings in USD per TOKEN, decoded straight
// into decimal so the conversion to $/MTok stays exact and never passes through float.
type openrouterWireModel struct {
	ID            string `json:"id"`
	ContextLength int    `json:"context_length"`
	Pricing       struct {
		Prompt     decimal.Decimal `json:"prompt"`
		Completion decimal.Decimal `json:"completion"`
	} `json:"pricing"`
	Architecture struct {
		InputModalities  []string `json:"input_modalities"`
		OutputModalities []string `json:"output_modalities"`
	} `json:"architecture"`
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
	retail := zenTier{
		MaxCtx: w.ContextLength,
		In:     costIn.Mul(margin),
		Out:    costOut.Mul(margin),
	}
	m := zenModel{
		ID:      w.ID,
		OwnedBy: openrouterOwner(w.ID),
		MaxCtx:  w.ContextLength,
		Vision:  w.vision(),
		Outputs: w.Architecture.OutputModalities,
		Base:    retail,
		Tiers:   []zenTier{retail},
		CostIn:  costIn,
		CostOut: costOut,
	}
	if !w.free() {
		m.MinTier = "paid"    // subscription floor: not reachable by free/trial
		m.Funding = "prepaid" // funding floor: real cash, so the fail-closed gate applies
	}
	return m
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

// openrouterCatalog decodes an OpenRouter /v1/models body into discovered SKUs — one
// per SKU the vendor advertises, so what we list is what they serve. A price of zero
// is published as zero and carries the floors that price implies (see model), which is
// what lets a free plan reach the routes that cost nothing.
func openrouterCatalog(body []byte) ([]zenModel, error) {
	wire, err := openrouterWire(body)
	if err != nil {
		return nil, err
	}
	margin := openrouterMargin()
	models := make([]zenModel, 0, len(wire))
	for _, w := range wire {
		models = append(models, w.model(margin))
	}
	return models, nil
}

// openrouterSpare names the routes OpenRouter still serves once its account is
// spent: the SKUs it charges nothing for AND answers in text with, drawn from the
// same listing the catalog above reads. Two readings of one body, so neither list
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
		if w.free() && w.writes() && strings.TrimSpace(w.ID) != "" {
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
