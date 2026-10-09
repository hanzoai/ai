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
	"context"
	"math"
	"math/big"
	"net/http"
	"strings"
	"sync/atomic"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
	"github.com/zap-proto/zip"
)

// The host decides who pays for each request the usage policy governs before it is
// served (routers' BalanceGateFilter asks object.Limits) and leaves its grant on the
// request. A covered request — the plan's included usage, or a capped free request —
// meets no wallet and no free allowance; a Hanzo family may answer it from paid
// upstream within what the grant holds, and what it used settles against the plan
// when its usage is recorded. A grant that sends the request to the wallet rides the
// request too, so its debit draws only what the grant lets pay (UsageEvent.Cash).

// planKey is where the gate leaves the host's grant on a request.
type planKey struct{}

// Cover leaves the host's grant on a request: on its locals and on the context its
// handlers and their usage records read.
func Cover(c *zip.Ctx, g *object.LimitGrant) {
	if c != nil && g != nil {
		c.Locals(planKey{}, g)
		c.SetContext(context.WithValue(c.Context(), planKey{}, g))
	}
}

// grantOf is the grant that covers the request, nil when nothing covers it: a grant
// that sends the request to the wallet covers nothing.
func grantOf(c *zip.Ctx) *object.LimitGrant {
	if c == nil {
		return nil
	}
	g, _ := c.Locals(planKey{}).(*object.LimitGrant)
	if !g.Covered() {
		return nil
	}
	return g
}

// grantFrom is the host's grant on a request's context, covering or not; nil when
// the policy said nothing about the request.
func grantFrom(ctx context.Context) *object.LimitGrant {
	if ctx == nil {
		return nil
	}
	g, _ := ctx.Value(planKey{}).(*object.LimitGrant)
	return g
}

// covered reports whether the request on ctx is paid by the plan or a free cap, so
// no wallet is asked about it.
func covered(ctx context.Context) bool { return grantFrom(ctx).Covered() }

// ClassOf is the class this module's catalog sells model in: free when it costs
// nothing, ours when a Hanzo family serves it or Hanzo owns its route, premium
// otherwise. The decision service serves Kai and forwards Jev; Hanzo owns Kai, so Kai
// is ours, and Jev is TypeSafe's, so Jev is premium. The host's policy may put a
// model in another class.
func ClassOf(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case m == "":
		return ""
	case costsNothing(m, ""):
		return object.ClassFree
	case FamilyOf(m) != "":
		return object.ClassOurs
	}
	if r := resolveModelRoute(m); r != nil && strings.EqualFold(r.ownedBy, "hanzo") {
		return object.ClassOurs
	}
	return object.ClassPremium
}

// FreeModel is the free lane's model id: what a conversation in limited mode is
// answered by.
const FreeModel = freeID

// Entitled reports whether path is one whose requests the usage policy decides: a
// conversation, or a decision.
func Entitled(path string) bool {
	p := strings.ToLower(strings.TrimRight(path, "/"))
	return ChatPath(p) || DecisionPath(p) || meteredPaths[p] || strings.HasPrefix(p, transcriptPath+"/")
}

// meteredPaths are the priced endpoints besides chat and decisions, each asked of
// the host's policy by the model its request names: retrieval, speech, the live
// transcript's open (its pushes and close are asked as Session), and media.
var meteredPaths = map[string]bool{
	"/v1/embeddings":           true,
	"/v1/rerank":               true,
	"/v1/audio/speech":         true,
	"/v1/audio/transcriptions": true,
	"/v1/audio/translations":   true,
	transcriptPath:             true,
	"/v1/audio/voice":          true,
	"/v1/audio/music":          true,
	"/v1/audio/foley":          true,
	"/v1/images/generations":   true,
	"/v1/videos/generations":   true,
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

// lineage is the `family` /v1/models names for a listed model: the Hanzo family it
// belongs to, or "" for anyone else's. FamilyOf names Enso's and Zen's SKUs, and is
// what the usage policy is asked about; two more are listed beside them: kai, the
// decision service's route Hanzo owns, and zoo, whatever Zoo owns. Jev, which the
// decision service forwards for TypeSafe, is not a Hanzo family.
func lineage(id, owner string) string {
	if f := FamilyOf(id); f != "" {
		return f
	}
	switch {
	case strings.EqualFold(owner, "zoo"), strings.EqualFold(owner, "zooai"):
		return "zoo"
	case strings.EqualFold(owner, "hanzo"):
		if r := resolveModelRoute(strings.ToLower(strings.TrimSpace(id))); r != nil && r.providerName == object.KaiName && strings.EqualFold(r.ownedBy, "hanzo") {
			return "kai"
		}
	}
	return ""
}

// planMark is what one covered request has done with its paid upstream. A family
// committed its answer to a request that may buy a paid rung (tried), which can cost
// at most bound, nano-dollars; owed is what the request is charged when that answer's
// cost is never stated — bound once a paid rung's upstream accepted, nothing before —
// and settled is set once a stated cost settled it. asked is a dispatch that carried
// the spend and got no answer. Either way the request buys no second paid answer.
type planMark struct {
	tried, asked, settled atomic.Bool
	bound, owed           atomic.Int64
}

// paid files that a paid rung's upstream accepted the request: its answer is charged
// its bound unless its cost is stated.
func (m *planMark) paid() { m.owed.Store(m.bound.Load()) }

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

// spendFor is the spend a dispatch of this request to f for sku carries, in USD: the
// plan's hold, to a Hanzo family only, for a request on the paid lane, never more than
// its seat holds (spendCap), for a SKU that family lists as plan-capable
// (zenModel.Plan), and only until a paid rung was tried — one request buys at most one
// paid answer. "" sends none.
//
// The listing is the family saying it speaks the committed answer; the TE: trailers
// every spend travels with is ai saying the same (planTE). A plan opens a paid rung
// only between two sides that both said so, so a catalog that gains plan rungs pays
// for nothing until both run the committed answer.
func spendFor(c *zip.Ctx, f *modelFamily, sku string) string {
	return spendOf(grantOf(c), seatOf(c.Context()), markOf(c), f, sku)
}

// spendOf is spendFor from the request's grant, seat and mark, read once, which a
// stream's writer may hold after the request itself is gone.
func spendOf(g *object.LimitGrant, s *seat, m *planMark, f *modelFamily, sku string) string {
	spend := spendCap(g, s)
	if spend <= 0 || !hanzoFamily(f) || m.tried.Load() || m.asked.Load() {
		return ""
	}
	if m, ok := f.lookup(sku); !ok || !m.Plan {
		return ""
	}
	return spendUSD(spend)
}

// planTE is the TE a spend travels with: ai reads the trailers a committed answer
// states its cost in (RFC 9110 §10.1.4).
const planTE = "trailers"

// hanzoFamily reports whether f is one of Hanzo's own families, whose routing is
// ours to choose.
func hanzoFamily(f *modelFamily) bool { return f == zenFam || f == ensoFam }

// Apps are the registered apps the request's validated token was minted for (its
// `aud`), empty for an API key or an unvalidated token. A plan covers its consumer
// apps' requests only, and this is the boundary's own answer to which app asked.
//
// A token delegated for model calls (iam.Inference) names none either. Its `aud` is
// the API it may call (RFC 8707), not an app, and what holds it is a program acting
// for a person, which named its model: like an API key, it gets that model or a
// refusal (402, saying what pays), never limited mode's free model in its place.
func Apps(c *zip.Ctx) []string {
	claims := (&ApiController{Ctx: c}).GetSessionClaims()
	if claims == nil || iam.Confined(bearerToken(c.Header("Authorization"), c.Fiber().Cookies(iamTokenCookieName))) {
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

// A family commits its answer to a request whose plan opened a paid rung at once,
// before it asks any rung, so the request's paid upstream is never bought after ai
// stopped waiting. It states three things, in USD:
//
//	costBoundHeader — on the commit: the most the request can cost.
//	paidMarker      — an SSE comment, the moment a paid rung's upstream accepted. A
//	                  whole answer carries none, so it is taken as accepted at the commit.
//	costHeader      — a trailer, at the end: what the request's paid upstream cost,
//	                  nothing when a free rung answered.
//
// An answer that ends with its cost settles that, never past the grant. One that does
// not — cut, or its caller gone — is charged its bound once a paid rung accepted it.
const (
	costBoundHeader = "X-Hanzo-Cost-Bound"
	costHeader      = "X-Hanzo-Cost"
	paidMarker      = ": paid"
)

// unstatedNanos is the most an answer is charged whose family committed it without a
// bound it can read: $1, never what the plan's family has left.
const unstatedNanos = 1_000_000_000

// usdNanos reads a family's USD figure in nano-dollars, rounded up; false when it is
// missing, malformed or negative.
func usdNanos(s string) (int64, bool) {
	v, ok := new(big.Rat).SetString(strings.TrimSpace(s))
	if !ok || v.Sign() < 0 {
		return 0, false
	}
	v.Mul(v, big.NewRat(1_000_000_000, 1))
	q, rem := new(big.Int).QuoRem(v.Num(), v.Denom(), new(big.Int))
	if rem.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return math.MaxInt64, true
	}
	return q.Int64(), true
}

// committedPlan reports whether a family's response is its commit to a request that
// may buy a paid rung: it states the bound, or declares the cost it will state (the
// client files a declared trailer under resp.Trailer, not the headers).
func committedPlan(resp *http.Response) bool {
	_, declared := resp.Trailer[costHeader]
	return declared || resp.Header.Get(costBoundHeader) != ""
}

// boundOf is the most a committed answer is charged, in nano-dollars: its stated
// bound, else unstatedNanos, never past the grant.
func boundOf(h http.Header, g *object.LimitGrant) int64 {
	if g == nil {
		return 0
	}
	if b, ok := usdNanos(h.Get(costBoundHeader)); ok && b > 0 {
		return min(b, g.Spend)
	}
	return min(unstatedNanos, g.Spend)
}

// planCost is what a covered answer's paid upstream cost, in nano-dollars: the cost
// its family stated, never past the grant; nothing when none was stated, since the
// request's mark charges an unstated cost (planMark).
func planCost(g *object.LimitGrant, stated *int64) int64 {
	if g == nil || stated == nil {
		return 0
	}
	return min(*stated, g.Spend)
}

// spendUSD renders nano-dollars as the decimal USD a family reads.
func spendUSD(nanos int64) string {
	return new(big.Rat).SetFrac(big.NewInt(nanos), big.NewInt(1_000_000_000)).FloatString(9)
}
