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

// public_chat.go — a completion for someone with no account.
//
// Every fact a caller normally presents is decided here instead, from facts the
// request cannot state. The visitor holds no credential and is issued none: they are
// identified by the address the edge observed, they name no model, and the call is
// recorded against a reserved org that owns no data.
//
// THE CEILING IS THE SWITCH. publicChatDaily is completions per visitor per UTC day
// and zero — the default — means the lane does not exist. One number, so a deployment
// cannot arm the lane and forget the bound, which on an unauthenticated inference
// route is the only mistake that matters. Every other deployment of this binary —
// every white-label reseller — stays closed until it says otherwise.
//
// IT FAILS CLOSED, AND ALONE. The bound is counted in this process and depends on no
// other service, because a bound that asks something else a question admits the call
// whenever the answer does not arrive. The host's allowance is read as well, so a
// visitor's usage lands in the one counter that reports a free tier, but a lane that
// admitted on its silence would be unbounded exactly when the host is down.
//
// A DAY IS SPENT ON ANSWERS. Both counts are read at the door and raised where the
// call is served, so a visitor pays for what they got and never for a route that was
// missing, a vendor that hung, or a pod being rolled.
//
// CORS is not decided here. The edge that fronts this binary owns which browser
// origins may read it, and a second opinion in this file would be a second answer to
// one question.
package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/ai/conf"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
)

// publicOrg is the account a credential-less call is recorded against. It is not a
// customer and it is spelled so it can never become one: an IAM org slug starts
// alphanumeric, so no signup can mint this name and collide with the lane. cloud
// reserves the same value for the same reason (apps/tenant.Public) and refuses it at
// the mint of every tenant-scoped read, so nothing filed under it reaches real data.
const publicOrg = "$public"

// publicVisitors bounds how many distinct visitors one day holds at once. The map is
// keyed by an address a stranger influences, so without a bound it is a memory
// exhaustion primitive on an endpoint that needs no credential to reach. At the bound
// the lane stops admitting visitors it has not already seen — turning away a newcomer
// is a failure this endpoint is allowed to have.
const publicVisitors = 200_000

// publicMaxTokens caps one public answer. It is the widget cap: the same reason — an
// unattributed completion cannot be billed to anyone — and therefore the same number.
const publicMaxTokens = widgetMaxTokens

// publicChatDaily is the ceiling and the switch at once: completions one visitor may
// take per UTC day. 0 closes the lane.
func publicChatDaily() int { return conf.GetConfigInt("PUBLIC_CHAT_DAILY") }

// publicOpen reports whether this deployment serves the lane at all.
func publicOpen() bool { return publicChatDaily() > 0 }

// ---- the count ------------------------------------------------------------

// dayCount is one UTC day of visitor counts. THE DAY IS THE WHOLE MAP, so nothing
// resets anything: when the day turns the map is dropped and every visitor is new at
// the same instant. There is no sweep, and no window in which a job has not yet run.
type dayCount struct {
	mu   sync.Mutex
	day  string
	seen map[string]int
}

var publicCount = &dayCount{}

// spent reports whether visitor has already taken their day.
//
// IT READS. Asking costs a visitor nothing and a visitor at the ceiling keeps their
// count where it is, because refusals are not usage. The call itself is counted where
// it is served (recordUsage), so a request that dies short of a model leaves this
// map untouched.
//
// A closed lane and a caller with no address are spent by definition: neither names a
// visitor this count can hold.
func (d *dayCount) spent(visitor, day string, limit int) bool {
	if limit <= 0 || visitor == "" {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.day != day || d.seen == nil {
		return false // a count from another day is not this day's
	}
	used, known := d.seen[visitor]
	if !known {
		// At the bound there is no room for a newcomer, so the lane stops admitting
		// visitors it has not already seen. Turning away a stranger is a failure this
		// endpoint is allowed to have; growing without limit on an address a stranger
		// picks is not.
		return len(d.seen) >= publicVisitors
	}
	return used >= limit
}

// count records one call the lane SERVED.
//
// At the ceiling the count stops climbing, so what a visitor is shown is "5 of 5" and
// never "37 of 5".
//
// WHICH ALSO MEANS THE COUNT DOES NOT REPORT AN OVERSHOOT. Calls in flight for one
// visitor all read the ceiling before any of them is counted here, so all of them are
// served — the excess is the visitor's concurrency, bounded by the edge's per-IP flood
// cap ahead of this and by how long an answer takes. Generous is the direction to err
// in when the alternative is a stranger short a call they never got, and the size of
// the generosity is a number to watch rather than one this map can show.
func (d *dayCount) count(visitor, day string, limit int) {
	if limit <= 0 || visitor == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.seen == nil || day > d.day {
		// FORWARD ONLY. The day turns when a LATER one arrives, so a clock that steps
		// backwards — two calls straddling midnight, an NTP correction — cannot drop a
		// map every visitor is still counted in. Counting a late call into the day
		// already open is the harmless direction; wiping the open day is not.
		d.day, d.seen = day, make(map[string]int)
	}
	used, known := d.seen[visitor]
	if !known && len(d.seen) >= publicVisitors {
		return
	}
	if used >= limit {
		return
	}
	d.seen[visitor] = used + 1
}

// ---- whose day a call spends ----------------------------------------------

// visitorKey addresses the visitor a public call is counted for. The lane is its only
// writer, and the record of the call is its only reader (usageRecord.bind).
type visitorKey struct{}

// withVisitor returns ctx naming the visitor this call is counted for.
func withVisitor(ctx context.Context, visitor string) context.Context {
	return context.WithValue(ctx, visitorKey{}, visitor)
}

// visitorOf answers the visitor a call is counted for, or "" for a caller who counts
// against their own payer — which is everyone except a stranger on this lane. The
// lane needs its own subject because a visitor's payer collapses to the reserved org,
// and counting every stranger there would spend one shared day.
func visitorOf(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	visitor, _ := ctx.Value(visitorKey{}).(string)
	return visitor
}

// utcDay is the period a count belongs to. One rule, one timezone — a period that
// moved with the caller would let a traveller take two days' worth in one afternoon.
func utcDay(t time.Time) string { return t.UTC().Format("2006-01-02") }

// ---- who is asking --------------------------------------------------------

// publicVisitor is the only identity an anonymous caller has: a digest of the address
// the edge observed.
//
// THE FORWARDED ADDRESS IS BELIEVED ONLY FROM INSIDE. A caller sets X-Forwarded-For
// and CF-Connecting-IP freely, so trusting either from a public peer hands every
// request a fresh quota and the ceiling stops existing. The socket peer decides:
// reached through our own ingress the peer is private and the edge's header names the
// visitor; reached directly the peer IS the visitor and the header is ignored. A
// deployment with no edge in front therefore counts by socket — stricter, never looser.
//
// The address is hashed and never kept. It travels into a log line and a usage row,
// and an address in either is a record of who visited that nothing here needs.
func publicVisitor(r *http.Request) string {
	addr := publicAddr(r)
	if addr == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(addr))
	return "visitor:" + hex.EncodeToString(sum[:])[:32]
}

// publicAddr resolves the address to count by. See publicVisitor for why the peer
// decides whether the forwarded header is believed.
func publicAddr(r *http.Request) string {
	peer := peerAddr(r)
	if peer != "" && !internalAddr(peer) {
		return peer
	}
	// Inside our own network the edge's header names the visitor. CF-Connecting-IP
	// only: Cloudflare overwrites it at its edge, where X-Forwarded-For merely gains
	// an entry and keeps whatever the caller put in front of it.
	if cf := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); cf != "" {
		return cf
	}
	// No edge header. The peer is our own ingress, so every visitor shares one count
	// and the lane closes early. That is the safe direction, and it is deliberate.
	return peer
}

// peerAddr is the socket peer — the one address on the request nobody but the network
// can set.
func peerAddr(r *http.Request) string {
	addr := strings.TrimSpace(r.RemoteAddr)
	if addr == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// internalAddr reports whether addr is one of our own hops rather than a caller.
// Loopback and private space is what an in-cluster ingress presents; anything
// routable is a stranger, whatever it writes in a header.
func internalAddr(addr string) bool {
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
}

// ---- the lane -------------------------------------------------------------

// ChatCompletionsPublic serves one completion to a caller with no account.
//
// @Title ChatCompletionsPublic
// @Tag OpenAI Compatible API
// @Description Anonymous completion on the free pool. No credential is presented and
// none is issued; the model is the platform's free route and cannot be chosen.
// @Param   body    body    openai.ChatCompletionRequest  true    "messages; any model field is ignored"
// @Success 200 {object} openai.ChatCompletionResponse
// @router /chat/public [post]
func (c *ApiController) ChatCompletionsPublic() {
	if !publicOpen() {
		// A closed deployment does not advertise the lane: there is nothing here to
		// describe, so the honest answer is that the route does not exist.
		c.publicRefuse(http.StatusNotFound, "invalid_request_error", "public_lane_closed",
			"This deployment does not serve anonymous completions.")
		return
	}

	visitor := publicVisitor(c.Ctx.Request)
	if visitor == "" {
		c.publicRefuse(http.StatusForbidden, "invalid_request_error", "public_no_address",
			"This request carries no address to count against.")
		return
	}

	// A LANE THAT CANNOT ANSWER SAYS SO PLAINLY. The pool is asked whether it has a
	// route before the call goes any further, so a visitor arriving during a
	// misconfiguration reads "the free pool is down" rather than an authorization
	// error thrown by provider setup. It is two map lookups, and it is the same
	// question resolveProviderForPublic asks below, through the same function, so the
	// two cannot drift.
	if _, err := publicPoolRoute(); err != nil {
		c.publicRefuse(http.StatusServiceUnavailable, "server_error", "public_pool_unavailable",
			"The free pool is not available right now. Nothing was counted against you; try again.")
		return
	}

	// BOTH COUNTS ARE READ HERE AND TAKEN NOWHERE. A day is spent on answers, so the
	// call is counted where it is served — recordUsage, which runs only for a call
	// that came back — and this asks only whether the visitor is already out. A
	// request that dies short of a model leaves them every call they arrived with.
	//
	// OUR OWN BOUND FIRST, and it decides. It is kept in this process and asks
	// nothing, so it holds while anything else is down.
	if publicCount.spent(visitor, utcDay(time.Now()), publicChatDaily()) {
		c.publicSpent(visitor, "count")
		return
	}

	// The host's allowance is the counter that REPORTS a free tier, read under a
	// subject of the visitor's own — one shared subject would let a single caller
	// starve every visitor's tier. It may refuse; it may not admit, which is why its
	// silence is not consulted.
	if spent := object.Spent(); spent != nil {
		if out, err := spent(c.Ctx.Request.Context(), visitor, publicOrg); err == nil && out {
			c.publicSpent(visitor, "allowance")
			return
		}
	}

	// The visitor rides on the request, because the record of the call is what counts
	// it and the record has no other way to know which stranger this was.
	c.Ctx.Request = c.Ctx.Request.WithContext(withVisitor(c.Ctx.Request.Context(), visitor))

	c.chatCompletions(callerPublic)
}

// publicSpent refuses a visitor who has taken their day.
func (c *ApiController) publicSpent(visitor, by string) {
	log.Info("public_chat: allowance spent visitor=%s by=%s", visitor, by)
	c.publicRefuse(http.StatusPaymentRequired, "insufficient_quota", "public_allowance_spent",
		"You've used today's free messages. They reset at midnight UTC — sign in at https://hanzo.ai to keep going.")
}

// publicRefuse writes the house error envelope — the shape the balance gate, the
// rolling cap and every auth refusal on this surface already answer with, so a client
// reads one error and not a fourth dialect of one.
func (c *ApiController) publicRefuse(status int, kind, code, message string) {
	c.Ctx.Output.SetStatus(status)
	c.Ctx.Output.Header("Content-Type", "application/json")
	c.Ctx.Output.Header("Cache-Control", "no-store")
	c.Ctx.Output.Body(publicErrorJSON(kind, code, message))
	c.EnableRender = false
}

// publicErrorJSON renders the house error envelope.
func publicErrorJSON(kind, code, message string) []byte {
	var e struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	e.Error.Message, e.Error.Type, e.Error.Code = message, kind, code
	b, err := json.Marshal(e)
	if err != nil {
		return []byte(`{"error":{"message":"public lane refused the request","type":"invalid_request_error","code":"public_lane_closed"}}`)
	}
	return b
}

// ---- who serves it --------------------------------------------------------

// publicPoolRoute answers whether the free pool can serve a call right now, and with
// what. It is ONE definition asked twice — once when the lane decides it can answer at
// all, once when the call is actually resolved — so the question that opens the lane
// and the question that picks the upstream can never drift apart.
//
// It fails closed on both halves. A price that is not FOUND to be zero closes the lane
// rather than guessing, and a pool with no route closes it rather than reaching for
// something billable.
//
// NO ORG GOES INTO ROUTE SELECTION, and the empty argument is the whole point.
// resolveModelRouteForOrg degrades a route when the caller's org yields a subject: that
// is the AUTO-ROUTER's preference gate, which walks a caller down to the next servable
// SKU. This lane is the direct path resolving its ONE authoritative route, and the
// resolver says so itself — "the direct call path (which resolves its authoritative
// route with orgId "" ⇒ empty subject ⇒ admit) is never degraded here". Passing $public
// degraded it to nil, and a direct path with no route left is not a downgrade, it is a
// lane that answers nothing. It refused for a reason that cannot apply: the funding
// gate asks the catalog what a family SKU costs, and the pool's id is a FRONT DOOR
// rather than a discovered SKU, so the lookup misses and an undescribed id is refused
// by design. The pool IS the routes that cost nothing; there is no spend to protect.
func publicPoolRoute() (*modelRoute, error) {
	if !ModelCostsNothing(freeID, publicOrg) {
		return nil, authError("the free pool is not currently free, so the public lane is closed")
	}
	route := resolveFreeRoute(freeID, "")
	if route == nil {
		return nil, authError("the free pool has no route on this deployment")
	}
	return route, nil
}

// resolveFreeRoute is the route resolver, as a seam. The ARGUMENT is what this lane
// shipped wrong, so the argument is the thing worth pinning — a test can hold "no org
// reaches route selection" without standing up family discovery, which no unit test
// has.
var resolveFreeRoute = resolveModelRouteForOrg

// resolveProviderForPublic settles the model, the upstream that runs it and the
// account it is recorded against — with no credential, and without reading the request.
//
// THE MODEL IS ASSIGNED, NEVER CHOSEN. freeID is the platform's own name for the free
// pool, and publicPoolRoute has verified it costs nothing before it is served. A pool
// that stopped being free — a catalog edit, a price that failed to load — closes the
// lane rather than quietly spending on the house, which is the fail-closed rule the
// balance gate already keeps one layer up.
func (c *ApiController) resolveProviderForPublic() (provider *object.Provider, authUser *iam.User, upstream string, err error) {
	route, err := publicPoolRoute()
	if err != nil {
		return nil, nil, "", err
	}
	provider, perr := object.GetModelProviderByName(route.providerName)
	if perr != nil || provider == nil {
		return nil, nil, "", authError("the free pool's provider is unavailable")
	}
	// Type "application" marks a machine, so account.Payer resolves the billing
	// subject to the org rather than looking for a person who does not exist.
	authUser = &iam.User{Owner: publicOrg, Type: "application"}
	c.Ctx.Input.SetParam("recordUserId", publicOrg+"/visitor")
	return provider, authUser, route.upstreamModel, nil
}
