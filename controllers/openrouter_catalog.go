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
//	the ECONOMICS — every OpenRouter SKU is resale: serving one spends real cash from
//	    a prepaid balance. The translation therefore stamps Funding "prepaid" and
//	    MinTier "paid" on EVERY model it yields, which is what makes the existing
//	    funding floor (familyFundingAllowed, fail-closed) refuse a caller we cannot
//	    confirm is paying. The floor is not re-implemented here; it is fed.
//
// Retail is the upstream price times a margin, floored at cost, and the upstream price
// travels beside it as COGS so the margin ledger books the real spread.

import (
	"encoding/json"
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
	prefix:     "openrouter/",
	owner:      "openrouter",
	urlKey:     "OPENROUTER_URL",
	keyKey:     "OPENROUTER_API_KEY",
	providerFn: object.OpenRouterProvider,
	decode:     openrouterCatalog,
}

// openrouterMarginDefault is the retail multiple applied to the upstream price when
// the deployment configures none: a 20% spread over what OpenRouter charges us.
const openrouterMarginDefault = "1.20"

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
		InputModalities []string `json:"input_modalities"`
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
// price is retained as COGS. Every SKU carries the paid floor — see the file header.
func (w openrouterWireModel) model(margin decimal.Decimal) zenModel {
	costIn := w.Pricing.Prompt.Mul(openrouterTokensPerMillion)
	costOut := w.Pricing.Completion.Mul(openrouterTokensPerMillion)
	retail := zenTier{
		MaxCtx: w.ContextLength,
		In:     costIn.Mul(margin),
		Out:    costOut.Mul(margin),
	}
	return zenModel{
		ID:      w.ID,
		OwnedBy: openrouterOwner(w.ID),
		MaxCtx:  w.ContextLength,
		Vision:  w.vision(),
		MinTier: "paid",    // subscription floor: never listed as reachable by free/trial
		Funding: "prepaid", // funding floor: real cash, so the fail-closed gate applies
		Base:    retail,
		Tiers:   []zenTier{retail},
		CostIn:  costIn,
		CostOut: costOut,
	}
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

// openrouterCatalog decodes an OpenRouter /v1/models body into discovered SKUs. A SKU
// OpenRouter prices at zero on both sides is dropped rather than published as free:
// this catalog is prepaid resale, and a free-looking price here would be a lie about
// what serving it costs.
func openrouterCatalog(body []byte) ([]zenModel, error) {
	var out struct {
		Data []openrouterWireModel `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	margin := openrouterMargin()
	models := make([]zenModel, 0, len(out.Data))
	for _, w := range out.Data {
		m := w.model(margin)
		if m.Base.In.IsZero() && m.Base.Out.IsZero() {
			continue
		}
		models = append(models, m)
	}
	return models, nil
}
