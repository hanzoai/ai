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

// kinds.go — which of an Enso tier's rungs a request may be served on, in what order,
// and what the rung that answered bills.
//
// Every Enso tier holds three kinds of rung (universe files/enso/catalog.yaml):
//
//	ours     our own credits (NVIDIA): billed as the grant says, the plan's usage
//	         at list price, or the wallet with no plan
//	premium  a prepaid key: billed at list from the payer's prepaid cash, on every
//	         plan, Unlimited included
//	free     the no-cost pool: billed nothing
//
// The balance gate decides the order from who pays and the engine walks it
// (X-Hanzo-Classes): a paying caller ours, premium, free, premium only while its
// wallet holds cash, since nothing else pays for it; a caller nothing pays for free,
// ours, at no charge. FAST MODE puts premium first and bills its multiple of the
// price (ensoFast) from prepaid cash. A request the gate did not mark is served free,
// ours: no unmarked request reaches a prepaid rung.

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hanzoai/ai/object"
	"github.com/zap-proto/zip"
)

const (
	// classesHeader tells the engine the kinds a request may be served on, in order.
	classesHeader = "X-Hanzo-Classes"
	// classHeader is the kind of the rung that answered, as the engine says it.
	classHeader = "X-Hanzo-Class"
	// fastHeader asks the engine for fast mode, and on its answer says the rung that
	// answered was asked fast.
	fastHeader = "X-Hanzo-Fast"
)

// The orders the gate gives a request.
const (
	// KindsPaying: a payer whose plan or prepaid cash pays, its wallet holding cash.
	KindsPaying = "ours,premium,free"
	// KindsPlan: a paid plan with no cash beside it: nothing pays a premium rung.
	KindsPlan = "ours,free"
	// KindsFree: a payer nothing pays for.
	KindsFree = "free,ours"
	// KindsFast: fast mode, which prepaid cash pays for.
	KindsFast = "premium,ours,free"
)

// Kind words as the engine says them.
const (
	kindOurs    = "ours"
	kindPremium = "premium"
	kindFree    = "free"
)

// kinds is what the gate decided for a request: the order of kinds, whether it is
// fast, whether nothing pays for it, and the model the caller named (a router id or
// a tier), which prices fast mode.
type kinds struct {
	order string
	fast  bool
	free  bool
	named string
}

type kindsKey struct{}

// Kinds marks the request's order of kinds, whether it is fast, and the model the
// caller named. fast holds only with premium in the order.
func Kinds(c *zip.Ctx, order string, fast bool, named string) {
	setKinds(c, kinds{order: order, fast: fast && strings.Contains(order, kindPremium), named: named})
}

// Free marks a request nothing pays for: served free, ours, billed nothing. A request
// no grant covers is covered by a free grant, so no wallet is asked for it.
func Free(c *zip.Ctx, named string) {
	setKinds(c, kinds{order: KindsFree, free: true, named: named})
	if grantOf(c) == nil {
		Cover(c, &object.LimitGrant{Pays: object.PaysFree, Class: object.ClassFree, Settle: func(int64) {}})
	}
}

// Order is what the gate marked on ctx: the order of kinds, whether it is fast, and
// whether nothing pays for it.
func Order(ctx context.Context) (order string, fast, free bool) {
	k := kindsOn(ctx)
	return k.order, k.fast, k.free
}

func setKinds(c *zip.Ctx, l kinds) {
	if c == nil {
		return
	}
	c.Locals(kindsKey{}, l)
	c.SetContext(context.WithValue(c.Context(), kindsKey{}, l))
}

// kindsOn is what the gate marked on ctx; unmarked, free, ours.
func kindsOn(ctx context.Context) kinds {
	if ctx != nil {
		if l, ok := ctx.Value(kindsKey{}).(kinds); ok && l.order != "" {
			return l
		}
	}
	return kinds{order: KindsFree}
}

// WantsFast reports whether a request asked for fast mode: X-Fast, or `fast` in its
// JSON body (wantsFast's two spellings).
func WantsFast(c *zip.Ctx) bool {
	if v := c.Header("X-Fast"); v != "" {
		return v == "1" || strings.EqualFold(v, "true")
	}
	var f fastFlag
	_ = json.Unmarshal(c.Body(), &f)
	return f.Fast
}

// cashPays reports whether the answer bills the payer's prepaid cash whatever its
// grant: a premium rung's, or a fast one's.
func (sv serving) cashPays() bool { return sv.class == kindPremium || sv.fast }

// Unsold reports whether model names no-cost routing rather than a model a caller may
// ask for: the pool's own id, or a model the pool's vendor charges nothing for. They
// answer only as fallback rungs inside a tier, named for the tier.
func Unsold(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return false
	}
	return m == freeID || strings.HasSuffix(m, ":free") || freeFamily().free(m)
}

// IsEnso reports whether model is an Enso tier the family sells: a SKU it serves at a
// price, which the gate pays for by plan or cash or serves free (kinds).
func IsEnso(model string) bool {
	if FamilyOf(model) != ensoFam.name {
		return false
	}
	m, ok := familyLookupFresh(model)
	return ok && m.priced() && !m.gated()
}

// LaneOf is the kind of rung model's route serves on (object.LimitAsk.Lane): "" for a
// Hanzo family's SKU, whose rungs of every kind the answer names; free for the pool
// and its no-cost models; ours for our own compute and the decision service; premium
// for every other route, which a prepaid upstream account serves.
func LaneOf(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case m == "":
		return ""
	case Unsold(m) || inPool(m):
		return object.LaneFree
	case decisionRoute(m), engineFam.enabled() && m == strings.ToLower(engineModel):
		return object.LaneOurs
	case FamilyOf(m) != "":
		return ""
	}
	return object.LanePremium
}

// laneOfAnswer is the kind of rung an answer of a Hanzo family names: the family's
// word, free where the rung billed nothing, else the route's (LaneOf).
func laneOfAnswer(sv serving, free bool, model string) string {
	switch {
	case sv.class != "":
		return sv.class
	case free:
		return object.LaneFree
	}
	return LaneOf(model)
}
