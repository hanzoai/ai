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
	"math/big"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/hanzoai/ai/object"
	"github.com/zap-proto/zip"
)

// A plan is a subscription to Hanzo's own models. The host admits each Enso or Zen
// request a plan covers before it is served (routers' BalanceGateFilter asks
// object.Limits) and leaves the grant on the request. A covered request meets no
// wallet and no free allowance; its family may answer it from paid upstream within
// what the grant holds, and what that cost settles against the plan's budget when its
// usage is recorded (recordFamilyUsage).

// planKey is where the gate leaves the plan's grant on a request.
type planKey struct{}

// Cover marks a request its caller's plan covers.
func Cover(c *zip.Ctx, g *object.LimitGrant) {
	if c != nil && g != nil {
		c.Locals(planKey{}, g)
	}
}

// grantOf is the plan's grant on the request, nil when no plan covers it.
func grantOf(c *zip.Ctx) *object.LimitGrant {
	if c == nil {
		return nil
	}
	g, _ := c.Locals(planKey{}).(*object.LimitGrant)
	return g
}

// FamilyOf names the Hanzo family that serves model — "enso" or "zen" — or "" for a
// model that is not a Hanzo SKU. The platform's own name for the free pool is Enso's
// (freeDoor).
func FamilyOf(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case m == "":
		return ""
	case m == freeID:
		return ensoFam.name
	}
	if f, ok := familyServing(m); ok {
		if f == zenFam || f == ensoFam {
			return f.name
		}
		return ""
	}
	switch {
	case strings.HasPrefix(m, "enso"), strings.HasPrefix(m, "hanzo/enso"):
		return ensoFam.name
	case strings.HasPrefix(m, "zen"):
		return zenFam.name
	}
	return ""
}

// planMark is what one covered request has done with its paid upstream: a family
// answered it from a paid rung (tried), and that answer's cost was settled.
type planMark struct{ tried, settled atomic.Bool }

// markKey is where a covered request keeps its mark.
type markKey struct{}

// markOf is the request's mark, made on first use. A pointer, so a stream's writer,
// which outlives the request, keeps reading the same one.
func markOf(c *zip.Ctx) *planMark {
	if m, ok := c.Locals(markKey{}).(*planMark); ok {
		return m
	}
	m := &planMark{}
	c.Locals(markKey{}, m)
	return m
}

// spendFor is the spend a dispatch of this request to f carries, in USD: the plan's
// hold, to a Hanzo family only, and only until a paid rung was tried — one request
// buys at most one paid answer. "" sends none.
func spendFor(c *zip.Ctx, f *modelFamily) string {
	g := grantOf(c)
	if g == nil || g.Spend <= 0 || !hanzoFamily(f) || markOf(c).tried.Load() {
		return ""
	}
	return spendUSD(g.Spend)
}

// hanzoFamily reports whether f is one of Hanzo's own families, whose routing is
// ours to choose.
func hanzoFamily(f *modelFamily) bool { return f == zenFam || f == ensoFam }

// Apps are the registered apps the request's validated token was minted for (its
// `aud`), empty for an API key or an unvalidated token. A plan covers its consumer
// apps' requests only, and this is the boundary's own answer to which app asked.
func Apps(c *zip.Ctx) []string {
	claims := (&ApiController{Ctx: c}).GetSessionClaims()
	if claims == nil {
		return nil
	}
	return append([]string(nil), claims.Audience...)
}

// chatPaths are the endpoints a conversation is served on: a family routes them
// across its rungs, and every one of them is sent with a completion ceiling.
var chatPaths = map[string]bool{
	"/v1/chat/completions": true,
	"/v1/messages":         true,
	"/v1/responses":        true,
}

// ChatPath reports whether path is a chat endpoint.
func ChatPath(path string) bool { return chatPaths[strings.ToLower(strings.TrimRight(path, "/"))] }

// spendHeader is what a request's plan holds for its family's paid upstream, in USD,
// sent to the Enso or Zen service ai fronts. The family may answer from a paid rung
// whose estimated cost fits.
const spendHeader = "X-Hanzo-Spend"

// costRateHeader is how a family tells ai what the paid rung that answered costs:
// input, output and cache-read in USD per million tokens. It is set only when a paid
// rung answered a request that carried spend.
const costRateHeader = "X-Hanzo-Cost-Rate"

// costRate is a paid rung's cost as its family stated it, in USD per million tokens.
type costRate struct{ in, out, cacheRead *big.Rat }

// readCostRate reads a family's cost-rate header; nil when none was set, which means
// a free rung answered.
func readCostRate(h http.Header) *costRate {
	parts := strings.Split(h.Get(costRateHeader), ",")
	if len(parts) != 3 {
		return nil
	}
	var r costRate
	for i, dst := range []**big.Rat{&r.in, &r.out, &r.cacheRead} {
		v, ok := new(big.Rat).SetString(strings.TrimSpace(parts[i]))
		if !ok || v.Sign() < 0 {
			return nil
		}
		*dst = v
	}
	// A rung that states no cache price charges a cached token at its input rate,
	// the way the family prices it.
	if r.cacheRead.Sign() == 0 {
		r.cacheRead = r.in
	}
	return &r
}

// nanos is what tokens cost at the rate, in nano-dollars, rounded up: fresh prompt
// tokens at the input rate, cached ones at the cache-read rate, completion at the
// output rate.
func (r *costRate) nanos(fresh, cached, completion int) int64 {
	if r == nil {
		return 0
	}
	sum := new(big.Rat)
	for _, t := range []struct {
		n    int
		rate *big.Rat
	}{{fresh, r.in}, {cached, r.cacheRead}, {completion, r.out}} {
		sum.Add(sum, new(big.Rat).Mul(big.NewRat(int64(t.n), 1), t.rate))
	}
	// USD per million tokens × tokens = micro-dollars; × 1000 = nano-dollars.
	sum.Mul(sum, big.NewRat(1000, 1))
	q, rem := new(big.Int).QuoRem(sum.Num(), sum.Denom(), new(big.Int))
	if rem.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	return q.Int64()
}

// planCost is what a covered answer's paid upstream cost, in nano-dollars: nothing
// when a free rung answered (no rate), the tokens at the stated rate when the
// upstream reported the answer's usage, and the whole hold when it did not — what an
// answer cost that nobody stated is charged at the most it could have been.
func planCost(g *object.LimitGrant, rate *costRate, t tokens) int64 {
	switch {
	case g == nil || rate == nil:
		return 0
	case !t.reported:
		return g.Spend
	}
	return min(rate.nanos(t.fresh, t.cached, t.completion), g.Spend)
}

// spendUSD renders nano-dollars as the decimal USD a family reads.
func spendUSD(nanos int64) string {
	return new(big.Rat).SetFrac(big.NewInt(nanos), big.NewInt(1_000_000_000)).FloatString(9)
}
