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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/ai/web"
)

// The lane is CLOSED unless a deployment arms it. Every other deployment of this
// binary — every white-label reseller — must not start serving anonymous inference
// because it upgraded.
func TestPublicLaneIsClosedUnlessArmed(t *testing.T) {
	t.Setenv("PUBLIC_CHAT_DAILY", "")
	if publicOpen() {
		t.Fatal("the public lane is open with no ceiling configured; it must be closed by default")
	}
	t.Setenv("PUBLIC_CHAT_DAILY", "0")
	if publicOpen() {
		t.Fatal("PUBLIC_CHAT_DAILY=0 must close the lane")
	}
	t.Setenv("PUBLIC_CHAT_DAILY", "not-a-number")
	if publicOpen() {
		t.Fatal("an unreadable ceiling must close the lane, never open it")
	}
	t.Setenv("PUBLIC_CHAT_DAILY", "5")
	if !publicOpen() || publicChatDaily() != 5 {
		t.Fatalf("PUBLIC_CHAT_DAILY=5 must arm the lane at 5, got open=%v n=%d", publicOpen(), publicChatDaily())
	}
}

// THE ONE THAT MATTERS. A stranger sets CF-Connecting-IP freely. If a public peer's
// header were believed, every request would arrive as a new visitor, the ceiling
// would not exist, and an unauthenticated endpoint would sit in front of an
// uncapped upstream key.
func TestForwardedAddressIsIgnoredFromAPublicPeer(t *testing.T) {
	forged := req("203.0.113.9:41000", "198.51.100.77") // routable peer: a stranger, not our ingress
	if got := publicAddr(forged); got != "203.0.113.9" {
		t.Fatalf("publicAddr believed a forged header from a public peer: got %q, want the socket peer 203.0.113.9", got)
	}

	// Two requests from one stranger, each naming a different address, must be ONE
	// visitor — otherwise the quota resets on demand.
	a := publicVisitor(req("203.0.113.9:1", "1.1.1.1"))
	b := publicVisitor(req("203.0.113.9:2", "2.2.2.2"))
	if a != b {
		t.Fatalf("one stranger became two visitors by rewriting a header: %q != %q", a, b)
	}
}

// Reached through our own ingress the peer is private, and the edge's header is the
// only thing that names the visitor.
func TestForwardedAddressIsBelievedFromOurOwnIngress(t *testing.T) {
	viaIngress := func(cf string) *http.Request { return req("10.244.1.7:52000", cf) }
	if got := publicAddr(viaIngress("198.51.100.77")); got != "198.51.100.77" {
		t.Fatalf("publicAddr = %q, want the edge-observed 198.51.100.77", got)
	}
	if publicVisitor(viaIngress("198.51.100.1")) == publicVisitor(viaIngress("198.51.100.2")) {
		t.Fatal("two visitors behind the ingress collapsed into one bucket")
	}
	// No edge header behind the ingress: everyone shares the ingress's count. Safe
	// direction — the lane closes early rather than becoming unbounded.
	if got := publicAddr(req("10.244.1.7:1", "")); got != "10.244.1.7" {
		t.Fatalf("with no edge header the peer must be the key, got %q", got)
	}
}

// The visitor id is a digest, so no address reaches a log line or a usage row.
func TestVisitorIsHashedAndNeverTheAddress(t *testing.T) {
	v := publicVisitor(req("203.0.113.9:1", ""))
	if v == "" {
		t.Fatal("no visitor derived from a request that has an address")
	}
	if got, want := len(v), len("visitor:")+32; got != want {
		t.Fatalf("visitor id length = %d, want %d", got, want)
	}
	for _, raw := range []string{"203.0.113.9", "203.0.113", "113.9"} {
		if strings.Contains(v, raw) {
			t.Fatalf("visitor id %q carries the raw address %q", v, raw)
		}
	}
	if publicVisitor(req("", "")) != "" {
		t.Fatal("a request with no address must yield no visitor, so the lane refuses it")
	}
}

// The ceiling holds, refusals do not count as usage, and each visitor has their own
// bucket — one shared bucket would let a single caller starve every visitor.
func TestCeilingHoldsPerVisitor(t *testing.T) {
	d := &dayCount{}
	const day, limit = "2026-08-15", 3

	for i := 1; i <= limit; i++ {
		if !d.take("visitor:a", day, limit) {
			t.Fatalf("call %d of %d was refused; the ceiling admitted too few", i, limit)
		}
	}
	if d.take("visitor:a", day, limit) {
		t.Fatal("call 4 of 3 was admitted; the ceiling does not hold")
	}
	// A second visitor is untouched by the first's exhaustion.
	if !d.take("visitor:b", day, limit) {
		t.Fatal("one visitor's exhaustion refused another; the buckets are shared")
	}
	// Refusals are not usage: the count stopped at the ceiling.
	if got := d.seen["visitor:a"]; got != limit {
		t.Fatalf("count kept climbing past the ceiling: %d, want %d", got, limit)
	}
	// A ceiling of zero admits nothing at all.
	if (&dayCount{}).take("visitor:a", day, 0) {
		t.Fatal("a zero ceiling admitted a call")
	}
}

// The day is the whole map, so turnover happens for every visitor at one instant
// with no sweep and no job.
func TestTheDayTurnsOverForEveryone(t *testing.T) {
	d := &dayCount{}
	if !d.take("visitor:a", "2026-08-15", 1) {
		t.Fatal("first call refused")
	}
	if d.take("visitor:a", "2026-08-15", 1) {
		t.Fatal("second call in the same day admitted")
	}
	if !d.take("visitor:a", "2026-08-16", 1) {
		t.Fatal("the new day did not start the count again")
	}
	if utcDay(time.Date(2026, 8, 15, 23, 59, 59, 0, time.FixedZone("east", 14*3600))) != "2026-08-15" {
		t.Fatal("the period moved with the caller's timezone; it must be UTC for everyone")
	}
}

// The map is keyed by an address a stranger influences, so it is bounded. At the
// bound the lane turns away visitors it has not seen rather than growing.
func TestVisitorMapIsBounded(t *testing.T) {
	d := &dayCount{day: "2026-08-15", seen: make(map[string]int, publicVisitors)}
	for i := 0; i < publicVisitors; i++ {
		d.seen[strconv.Itoa(i)] = 0
	}
	if d.take("visitor:newcomer", "2026-08-15", 10) {
		t.Fatal("a newcomer was admitted past the visitor bound; the map grows without limit")
	}
	// A visitor already in the map still gets their allowance.
	known := ""
	for k := range d.seen {
		known = k
		break
	}
	if !d.take(known, "2026-08-15", 10) {
		t.Fatal("the bound refused a visitor already counted today")
	}
}

// Two calls racing for the same last unit must not both be admitted.
func TestCeilingIsNotRaceable(t *testing.T) {
	d := &dayCount{}
	const limit = 50
	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted := 0
	for i := 0; i < 500; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if d.take("visitor:a", "2026-08-15", limit) {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if admitted != limit {
		t.Fatalf("admitted %d of a %d ceiling under 500 concurrent calls", admitted, limit)
	}
}

// The refusal is the house envelope — the shape the balance gate and every auth
// refusal on this surface already answer with — carrying its own code.
func TestRefusalIsTheHouseEnvelope(t *testing.T) {
	var got struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	body := publicErrorJSON("insufficient_quota", "public_allowance_spent", "spent")
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("refusal is not JSON: %v (%s)", err, body)
	}
	if got.Error.Type != "insufficient_quota" || got.Error.Code != "public_allowance_spent" {
		t.Fatalf("refusal = %+v, want type insufficient_quota / code public_allowance_spent", got.Error)
	}
	if got.Error.Message == "" {
		t.Fatal("refusal carries no message for a client to render")
	}
}

// The lane records against the reserved org, which no signup can ever mint.
func TestPublicOrgCannotBeMintedBySignup(t *testing.T) {
	if publicOrg != "$public" {
		t.Fatalf("publicOrg = %q; cloud reserves $public and refuses it at the tenant mint", publicOrg)
	}
	if c := publicOrg[0]; c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
		t.Fatalf("publicOrg %q starts alphanumeric, so a signup could mint it and collide with the lane", publicOrg)
	}
}

// A public answer is capped, because nobody can be billed for a long one.
func TestPublicAnswerIsCapped(t *testing.T) {
	if publicMaxTokens <= 0 || publicMaxTokens != widgetMaxTokens {
		t.Fatalf("publicMaxTokens = %d, want the widget cap %d", publicMaxTokens, widgetMaxTokens)
	}
}

// req builds a request the way the server does — through Header.Set, so the key is
// canonicalised exactly as net/http canonicalises a parsed one. A raw http.Header
// literal is NOT equivalent: Get canonicalises its argument, so "CF-Connecting-IP"
// written as a literal key is invisible to it and every header test silently passes.
func req(remote, cf string) *http.Request {
	r := &http.Request{RemoteAddr: remote, Header: http.Header{}}
	if cf != "" {
		r.Header.Set("CF-Connecting-IP", cf)
	}
	return r
}

// ---- the handler itself ---------------------------------------------------

// publicCall drives the real ChatCompletionsPublic handler and returns the recorder.
func publicCall(t *testing.T, remote, cf string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/public",
		strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
	r.RemoteAddr = remote
	if cf != "" {
		r.Header.Set("CF-Connecting-IP", cf)
	}
	rec := httptest.NewRecorder()
	ctx := web.NewContext()
	ctx.Reset(rec, r)
	ctx.Input.RequestBody = []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	c := &ApiController{}
	c.Init(ctx, "ApiController", "ChatCompletionsPublic", nil)
	c.ChatCompletionsPublic()
	return rec
}

// refusalOf reads the house envelope off a recorded response.
func refusalOf(t *testing.T, rec *httptest.ResponseRecorder) (status int, code string) {
	t.Helper()
	var got struct {
		Error struct{ Message, Type, Code string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not the house envelope: %v (%s)", err, rec.Body.String())
	}
	return rec.Code, got.Error.Code
}

// A deployment that has not armed the lane does not serve it, and does not describe
// it either — there is nothing there.
func TestClosedLaneServesNothing(t *testing.T) {
	t.Setenv("PUBLIC_CHAT_DAILY", "")
	status, code := refusalOf(t, publicCall(t, "203.0.113.5:1", ""))
	if status != http.StatusNotFound || code != "public_lane_closed" {
		t.Fatalf("closed lane answered %d/%s, want 404/public_lane_closed", status, code)
	}
}

// A request with no address cannot be counted, so it is refused rather than admitted
// into a bucket shared with everyone else who has none.
func TestUncountableRequestIsRefused(t *testing.T) {
	t.Setenv("PUBLIC_CHAT_DAILY", "5")
	status, code := refusalOf(t, publicCall(t, "", ""))
	if status != http.StatusForbidden || code != "public_no_address" {
		t.Fatalf("uncountable request answered %d/%s, want 403/public_no_address", status, code)
	}
}

// A visitor who has taken their day is refused with the house envelope and its own
// code — and the refusal happens in the lane, before any provider is resolved.
func TestSpentVisitorIsRefusedByTheLane(t *testing.T) {
	t.Setenv("PUBLIC_CHAT_DAILY", "2")
	const peer = "203.0.113.77:9000"
	visitor := publicVisitor(req(peer, ""))

	saved := publicCount
	t.Cleanup(func() { publicCount = saved })
	publicCount = &dayCount{day: utcDay(time.Now()), seen: map[string]int{visitor: 2}}

	status, code := refusalOf(t, publicCall(t, peer, ""))
	if status != http.StatusPaymentRequired || code != "public_allowance_spent" {
		t.Fatalf("spent visitor answered %d/%s, want 402/public_allowance_spent", status, code)
	}
	// The count did not climb: refusals are not usage.
	if got := publicCount.seen[visitor]; got != 2 {
		t.Fatalf("a refusal was counted as usage: %d, want 2", got)
	}
}

// A credential presented to the public lane changes nothing — the lane is the lane,
// and a bearer here must not buy a different model or another org's ledger.
func TestBearerOnThePublicLaneIsIgnored(t *testing.T) {
	t.Setenv("PUBLIC_CHAT_DAILY", "2")
	const peer = "203.0.113.78:9000"
	visitor := publicVisitor(req(peer, ""))

	saved := publicCount
	t.Cleanup(func() { publicCount = saved })
	publicCount = &dayCount{day: utcDay(time.Now()), seen: map[string]int{visitor: 2}}

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/public", strings.NewReader(`{"model":"claude-opus-5","messages":[]}`))
	r.RemoteAddr = peer
	r.Header.Set("Authorization", "Bearer sk-whatever")
	r.Header.Set("X-Org-Id", "some-other-tenant")
	rec := httptest.NewRecorder()
	ctx := web.NewContext()
	ctx.Reset(rec, r)
	ctx.Input.RequestBody = []byte(`{"model":"claude-opus-5","messages":[]}`)
	c := &ApiController{}
	c.Init(ctx, "ApiController", "ChatCompletionsPublic", nil)
	c.ChatCompletionsPublic()

	if status, code := refusalOf(t, rec); status != http.StatusPaymentRequired || code != "public_allowance_spent" {
		t.Fatalf("a bearer changed the lane's answer: %d/%s, want 402/public_allowance_spent", status, code)
	}
}

// ---- route selection ------------------------------------------------------

// THE DEFECT THIS CLOSES. The lane shipped resolving its route with orgId=$public.
// resolveModelRouteForOrg degrades a route whenever the org yields a subject — that is
// the AUTO-ROUTER's preference gate — and the free pool's id is a FRONT DOOR rather
// than a discovered SKU, so the funding gate's catalog lookup misses and refuses an
// id it cannot describe. The route came back nil and the armed lane answered "the free
// pool has no route on this deployment" for every visitor.
//
// The direct path resolves its ONE authoritative route and must not be degraded, which
// is what the resolver's own contract says. So: no org reaches route selection.
func TestRouteSelectionIsNotGivenAnOrg(t *testing.T) {
	var gotModel, gotOrg string
	called := false
	prev := resolveFreeRoute
	resolveFreeRoute = func(model, org string) *modelRoute {
		called, gotModel, gotOrg = true, model, org
		return nil // the rest of the resolver is not what this test is about
	}
	t.Cleanup(func() { resolveFreeRoute = prev })

	c := &ApiController{}
	_, _, _, _ = c.resolveProviderForPublic()

	if !called {
		// Either the price gate refused first (no catalog in a unit test) or the call
		// moved. Both are real, and neither lets this test claim the argument is right.
		t.Skip("route selection was not reached — the price gate refused first")
	}
	if gotOrg != "" {
		t.Fatalf("route selection was given org %q; an org degrades a direct-path route to nil "+
			"and closes the lane for every visitor", gotOrg)
	}
	if gotModel != freeID {
		t.Fatalf("route selection asked for model %q, want the free pool id %q", gotModel, freeID)
	}
}
