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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/ai/address"
	"github.com/hanzoai/ai/object"
	"github.com/zap-proto/zip"
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
	// A routable peer: a stranger, not our ingress.
	if got := publicAddr("203.0.113.9", "198.51.100.77"); got != "203.0.113.9" {
		t.Fatalf("publicAddr believed a forged header from a public peer: got %q, want the socket peer 203.0.113.9", got)
	}

	// Two requests from one stranger, each naming a different address, must be ONE
	// visitor — otherwise the quota resets on demand.
	a := publicVisitor(publicAddr("203.0.113.9", "1.1.1.1"))
	b := publicVisitor(publicAddr("203.0.113.9", "2.2.2.2"))
	if a != b {
		t.Fatalf("one stranger became two visitors by rewriting a header: %q != %q", a, b)
	}
}

// Reached from a peer of ours, the host's stamp is the only thing that names the
// visitor, and no stamp names nobody.
func TestTheStampIsBelievedFromOurOwnPeer(t *testing.T) {
	viaPod := func(stamp string) string { return publicAddr("10.244.1.7", stamp) }
	if got := viaPod("198.51.100.77"); got != "198.51.100.77" {
		t.Fatalf("publicAddr = %q, want the stamped 198.51.100.77", got)
	}
	if publicVisitor(viaPod("198.51.100.1")) == publicVisitor(viaPod("198.51.100.2")) {
		t.Fatal("two visitors the host told apart collapsed into one bucket")
	}
	// No stamp from a peer of ours: no public caller, so no visitor. Keyed on the
	// peer, every pod would hold a free allowance of its own.
	if got := publicAddr("10.244.1.7", ""); got != "" {
		t.Fatalf("an unstamped peer of ours must name no caller, got %q", got)
	}
}

// AN IN-CLUSTER WORKLOAD CANNOT NAME ITSELF EITHER. Reached from a peer of ours with
// no host stamp, CF-Connecting-IP is whatever that workload wrote; believed, it was
// a fresh free-lane identity per request. The lane refuses the request as carrying
// no public caller, whatever the header says.
func TestAnUnstampedPeerOfOursIsNoPublicCaller(t *testing.T) {
	for _, peer := range []string{"10.244.1.7:41000", "127.0.0.1:41000", "0.0.0.0:0"} {
		ask := func(cf string) *ApiController {
			c := from(visit(http.MethodPost, "/v1/chat/public"), peer)
			c.Fiber().Request().Header.Set("CF-Connecting-IP", cf)
			return c
		}
		a, b := ask("198.51.100.1"), ask("198.51.100.2")
		if got := a.stated(); got != "" {
			t.Fatalf("peer %s: stated = %q; only the host's stamp is a statement", peer, got)
		}
		if va, vb := Visitor(a.Ctx), Visitor(b.Ctx); va != "" || vb != "" {
			t.Fatalf("peer %s: CF-Connecting-IP minted visitors %q and %q", peer, va, vb)
		}
	}

	t.Setenv("PUBLIC_CHAT_DAILY", "5")
	servablePool(t)
	c := from(visit(http.MethodPost, "/v1/chat/public"), "10.244.1.7:41000")
	c.Fiber().Request().SetBody([]byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	c.Fiber().Request().Header.Set("CF-Connecting-IP", "198.51.100.3")
	c.ChatCompletionsPublic()
	if status, code := refusalOf(t, c); status != http.StatusForbidden || code != "public_no_address" {
		t.Fatalf("an unstamped in-cluster caller answered %d/%s, want 403/public_no_address", status, code)
	}
}

// The visitor id is a digest, so no address reaches a log line or a usage row.
func TestVisitorIsHashedAndNeverTheAddress(t *testing.T) {
	v := publicVisitor(publicAddr("203.0.113.9", ""))
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
	if publicVisitor(publicAddr("", "")) != "" {
		t.Fatal("a request with no address must yield no visitor, so the lane refuses it")
	}
}

// One IPv6 host holds a /64: rotating the interface identifier is the same visitor,
// and the next /64 over is somebody else.
func TestAnIPv6HostIsOneVisitorPerSlash64(t *testing.T) {
	a := publicVisitor("2001:db8:1:2::1")
	for _, same := range []string{"2001:db8:1:2::2", "2001:db8:1:2:ffff:ffff:ffff:ffff"} {
		if publicVisitor(same) != a {
			t.Fatalf("%s is a different visitor from 2001:db8:1:2::1 inside one /64", same)
		}
	}
	if publicVisitor("2001:db8:1:3::1") == a {
		t.Fatal("the neighbouring /64 was counted as the same visitor")
	}
	if publicVisitor("203.0.113.9") == publicVisitor("203.0.113.10") {
		t.Fatal("two IPv4 addresses were counted as one visitor")
	}
}

// The country is the host's stamp, believed only from one of our own peers, and a
// raw CF-IPCountry is never read.
func TestCountryIsTheHostsStamp(t *testing.T) {
	var got string
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Raw(http.MethodGet, "/probe", func(c *zip.Ctx) error {
		got = Country(c)
		return c.NoContent(http.StatusNoContent)
	})
	ask := func(h map[string]string) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		for k, v := range h {
			req.Header.Set(k, v)
		}
		if _, err := app.Test(req); err != nil {
			t.Fatal(err)
		}
		return got
	}
	// app.Test's peer is the unspecified address: a socket from our own host.
	if c := ask(map[string]string{address.Country: "DE"}); c != "DE" {
		t.Fatalf("the host's stamp: %q, want DE", c)
	}
	if c := ask(map[string]string{"CF-IPCountry": "US"}); c != "" {
		t.Fatalf("a raw CF-IPCountry was read as the country: %q", c)
	}
}

// serve is what the lane does with a call it answers: read the ceiling, and count the
// call only where it was served. The two are separate verbs on purpose — a request
// that dies between them costs the visitor nothing — so a test of the ceiling has to
// perform both, exactly as the lane does.
func serve(d *dayCount, visitor, day string, limit int) bool {
	if d.spent(visitor, day, limit) {
		return false
	}
	d.count(visitor, day, limit)
	return true
}

// The ceiling holds, refusals do not count as usage, and each visitor has their own
// bucket — one shared bucket would let a single caller starve every visitor.
func TestCeilingHoldsPerVisitor(t *testing.T) {
	d := &dayCount{}
	const day, limit = "2026-08-15", 3

	for i := 1; i <= limit; i++ {
		if !serve(d, "visitor:a", day, limit) {
			t.Fatalf("call %d of %d was refused; the ceiling admitted too few", i, limit)
		}
	}
	if serve(d, "visitor:a", day, limit) {
		t.Fatal("call 4 of 3 was admitted; the ceiling does not hold")
	}
	// A second visitor is untouched by the first's exhaustion.
	if !serve(d, "visitor:b", day, limit) {
		t.Fatal("one visitor's exhaustion refused another; the buckets are shared")
	}
	// Refusals are not usage: the count stopped at the ceiling.
	if got := d.seen["visitor:a"]; got != limit {
		t.Fatalf("count kept climbing past the ceiling: %d, want %d", got, limit)
	}
	// A ceiling of zero admits nothing at all.
	if serve(&dayCount{}, "visitor:a", day, 0) {
		t.Fatal("a zero ceiling admitted a call")
	}
}

// A CALL THAT WAS NOT SERVED COSTS NOTHING. Reading the ceiling is what the lane does
// on arrival, and reading it can never raise it — so a visitor refused by a route
// that would not resolve, a vendor that never answered, or a pod mid-roll keeps every
// call they arrived with.
func TestReadingTheCeilingNeverRaisesIt(t *testing.T) {
	d := &dayCount{}
	const day, limit = "2026-08-15", 3

	for range 20 {
		d.spent("visitor:a", day, limit)
	}
	if got := d.seen["visitor:a"]; got != 0 {
		t.Fatalf("twenty reads left a count of %d; a read must cost a visitor nothing", got)
	}
	if d.spent("visitor:a", day, limit) {
		t.Fatal("a visitor who was never served reads as spent")
	}

	// At the ceiling, a refusal leaves the count where it is.
	for range limit {
		serve(d, "visitor:a", day, limit)
	}
	for range 5 {
		if !d.spent("visitor:a", day, limit) {
			t.Fatal("a visitor at the ceiling was admitted")
		}
	}
	if got := d.seen["visitor:a"]; got != limit {
		t.Fatalf("refusals raised the count to %d, want %d — refusals are not usage", got, limit)
	}
}

// The day is the whole map, so turnover happens for every visitor at one instant
// with no sweep and no job.
func TestTheDayTurnsOverForEveryone(t *testing.T) {
	d := &dayCount{}
	if !serve(d, "visitor:a", "2026-08-15", 1) {
		t.Fatal("first call refused")
	}
	if serve(d, "visitor:a", "2026-08-15", 1) {
		t.Fatal("second call in the same day admitted")
	}
	if !serve(d, "visitor:a", "2026-08-16", 1) {
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
	for i := range publicVisitors {
		d.seen[strconv.Itoa(i)] = 0
	}
	if serve(d, "visitor:newcomer", "2026-08-15", 10) {
		t.Fatal("a newcomer was admitted past the visitor bound; the map grows without limit")
	}
	if _, grew := d.seen["visitor:newcomer"]; grew {
		t.Fatal("a refused newcomer was written into the map anyway")
	}
	// A visitor already in the map still gets their allowance.
	known := ""
	for k := range d.seen {
		known = k
		break
	}
	if !serve(d, known, "2026-08-15", 10) {
		t.Fatal("the bound refused a visitor already counted today")
	}
}

// THE COUNT NEVER RUNS PAST THE CEILING, however many calls are in flight. It is the
// number a visitor is shown, and "51 of 50" is a lie about a limit that held.
//
// ADMISSION MAY OVERSHOOT, and that is the trade this lane makes deliberately. The
// ceiling is read on arrival and raised where the call was served, so calls in
// flight for one visitor can all be admitted by the same last unit — a ceiling of
// five occasionally serving six. Overshoot is generous and bounded by how many calls
// one caller has open at once. The strict version — take on arrival — charged a
// visitor for a route that never resolved and a vendor that never answered, which is
// a defect the visitor feels and we never see.
func TestTheCountNeverRunsPastTheCeiling(t *testing.T) {
	d := &dayCount{}
	const limit = 50
	var wg sync.WaitGroup
	for range 500 {
		wg.Go(func() {
			serve(d, "visitor:a", "2026-08-15", limit)
		})
	}
	wg.Wait()
	if got := d.seen["visitor:a"]; got != limit {
		t.Fatalf("500 concurrent calls left a count of %d against a ceiling of %d", got, limit)
	}
	if !d.spent("visitor:a", "2026-08-15", limit) {
		t.Fatal("the visitor is not spent after 500 calls against a ceiling of 50")
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
	if publicMaxTokens != 800 {
		t.Fatalf("publicMaxTokens = %d, want 800", publicMaxTokens)
	}
}

// ---- the handler itself ---------------------------------------------------

// publicCall drives the real ChatCompletionsPublic handler and returns the recorder.
func publicCall(t *testing.T, remote, stamp string) *ApiController {
	t.Helper()
	c := from(visit(http.MethodPost, "/v1/chat/public"), remote)
	c.Fiber().Request().SetBody([]byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	if stamp != "" {
		c.Fiber().Request().Header.Set(address.Header, stamp)
	}
	c.ChatCompletionsPublic()
	return c
}

// visitorAt is the key the lane will count a caller by — the same composition the
// handler performs, so a test seeds the count under the name the handler will look up.
// The port is dropped because a peer resolves to an address on the wire.
func visitorAt(peer, stated string) string {
	host, _, err := net.SplitHostPort(peer)
	if err != nil {
		host = peer
	}
	return publicVisitor(publicAddr(host, stated))
}

// refusalOf reads the house envelope off a recorded response.
func refusalOf(t *testing.T, c *ApiController) (status int, code string) {
	t.Helper()
	var got struct {
		Error struct{ Message, Type, Code string } `json:"error"`
	}
	if err := json.Unmarshal([]byte(sent(c)), &got); err != nil {
		t.Fatalf("response is not the house envelope: %v (%s)", err, sent(c))
	}
	return answered(c), got.Error.Code
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
	servablePool(t)
	const peer = "203.0.113.77:9000"
	visitor := visitorAt(peer, "")

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
	servablePool(t)
	const peer = "203.0.113.78:9000"
	visitor := visitorAt(peer, "")

	saved := publicCount
	t.Cleanup(func() { publicCount = saved })
	publicCount = &dayCount{day: utcDay(time.Now()), seen: map[string]int{visitor: 2}}

	c := from(visit(http.MethodPost, "/v1/chat/public"), peer)
	c.Fiber().Request().SetBody([]byte(`{"model":"claude-opus-5","messages":[]}`))
	c.Fiber().Request().Header.Set("Authorization", "Bearer sk-whatever")
	c.Fiber().Request().Header.Set("X-Org-Id", "some-other-tenant")
	c.ChatCompletionsPublic()

	if status, code := refusalOf(t, c); status != http.StatusPaymentRequired || code != "public_allowance_spent" {
		t.Fatalf("a bearer changed the lane's answer: %d/%s, want 402/public_allowance_spent", status, code)
	}
}

// ---- route selection ------------------------------------------------------

// THE DEFECT THIS CLOSES. The lane shipped resolving its route with orgId=$public.
// resolveModelRouteForOrg degrades a route whenever the org yields a subject — that is
// the AUTO-ROUTER's preference gate — and the free pool's id is an ALIAS rather
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

// ---- nothing is charged for a call we cannot serve -------------------------

// THE DEFECT THIS CLOSES. The lane took the visitor's call on arrival, before it
// knew anything could answer one. A misconfigured pool therefore refused every caller
// AND spent their day on the way out — the ceiling emptied without a model ever being
// reached, and the host's half of that count PERSISTS, so a pod restart does not
// undo it.
//
// A day is spent on answers. Both ends of the lane are checked here: the pool with no
// route, which is refused on arrival, and the pool that HAS a route whose provider
// then fails to stand up — the shape of every vendor outage. Neither costs a visitor
// anything, because neither reaches the record of a served call, which is the only
// thing that counts one.
func TestACallThatReachesNoModelChargesNobody(t *testing.T) {
	for _, c := range []struct {
		name  string
		route func(string, string) *modelRoute
	}{
		{"the pool has no route", func(string, string) *modelRoute { return nil }},
		{"the route's provider is down", func(string, string) *modelRoute {
			return &modelRoute{providerName: "a provider that does not exist"}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("PUBLIC_CHAT_DAILY", "5")

			prevRoute := resolveFreeRoute
			resolveFreeRoute = c.route
			t.Cleanup(func() { resolveFreeRoute = prevRoute })

			// The host allowance may be READ — reading costs nothing. What must not
			// happen is a usage record, which is what would raise the persistent count.
			object.SetSpent(func(_ context.Context, _, _ string) (object.Standing, error) { return object.Standing{}, nil })
			t.Cleanup(func() { object.SetSpent(nil) })

			prevRec := object.UsageRecorder()
			object.SetUsageRecorder(func(_ context.Context, u object.UsageEvent) error {
				t.Errorf("a call that reached no model was recorded as usage: %+v", u)
				return nil
			})
			t.Cleanup(func() { object.SetUsageRecorder(prevRec) })

			saved := publicCount
			publicCount = &dayCount{}
			t.Cleanup(func() { publicCount = saved })

			const peer = "203.0.113.90:9000"
			visitor := visitorAt(peer, "")

			publicCall(t, peer, "")
			if n := publicCount.seen[visitor]; n != 0 {
				t.Fatalf("the visitor was charged %d for a call that reached no model; want 0", n)
			}
		})
	}
}

// The lane still REFUSES at the ceiling, and the refusal leaves the count alone.
// Moving the count must not have removed the bound it moved.
func TestTheLaneRefusesAtTheCeiling(t *testing.T) {
	t.Setenv("PUBLIC_CHAT_DAILY", "5")
	servablePool(t)

	const peer = "203.0.113.91:9000"
	visitor := visitorAt(peer, "")

	saved := publicCount
	publicCount = &dayCount{day: utcDay(time.Now()), seen: map[string]int{visitor: 5}}
	t.Cleanup(func() { publicCount = saved })

	status, code := refusalOf(t, publicCall(t, peer, ""))
	if status != http.StatusPaymentRequired || code != "public_allowance_spent" {
		t.Fatalf("a visitor at the ceiling got %d/%s, want 402/public_allowance_spent", status, code)
	}
	if n := publicCount.seen[visitor]; n != 5 {
		t.Fatalf("the refusal moved the count to %d; refusals are not usage", n)
	}
}

// ONE SITE IS ONE ALLOWANCE AT SITE SCALE. A /48 holds 65,536 /64s and Hurricane
// Electric hands one out free, so a lane counting only the /64 gives one tunnel
// 65,536 days of free messages. The /48 lane every /64 inside it shares is held to
// siteDay visitors' worth; once it is spent, a /64 the lane has never seen is spent
// with it.
func TestASiteIsHeldByItsSlash48(t *testing.T) {
	t.Setenv("PUBLIC_CHAT_DAILY", "5")
	servablePool(t)

	sum := sha256.Sum256([]byte("2001:470:1f00::/48"))
	site := "visitor:" + hex.EncodeToString(sum[:])[:32]

	saved := publicCount
	publicCount = &dayCount{day: utcDay(time.Now()), seen: map[string]int{site: siteDay * 5}}
	t.Cleanup(func() { publicCount = saved })

	status, code := refusalOf(t, publicCall(t, "[2001:470:1f00:beef::1]:9000", ""))
	if status != http.StatusPaymentRequired || code != "public_allowance_spent" {
		t.Fatalf("a fresh /64 inside a spent /48 got %d/%s, want 402/public_allowance_spent", status, code)
	}
	if n := publicCount.seen[site]; n != siteDay*5 {
		t.Fatalf("the refusal moved the site's count to %d; refusals are not usage", n)
	}
}

// A CARRIER'S SEVENTEENTH VISITOR IS SERVED. Sixteen strangers behind one carrier or
// Private Relay /48 take their whole day; the seventeenth has sent nothing and is
// not refused for what they spent. The day holds a site to siteDay visitors, not to
// the sixteen a minute does.
func TestACarriersSeventeenthVisitorIsServed(t *testing.T) {
	d := &dayCount{}
	const day, limit = "2026-10-01", 5
	for v := range 16 {
		lanes := publicLanes(fmt.Sprintf("2607:fb90:a3c1:%x::1", v))
		for range limit {
			if d.out(lanes, day, limit) {
				t.Fatalf("visitor %d refused inside their own day", v+1)
			}
			d.serve(lanes, day, limit)
		}
	}
	if d.out(publicLanes("2607:fb90:a3c1:ffff::1"), day, limit) {
		t.Fatal("a visitor who sent nothing today was refused because sixteen strangers in their /48 spent theirs")
	}
}

// A served call rises on every lane the visitor is charged to: their /64 by one and
// their /48 by one, each against its own ceiling.
func TestAServedCallCountsOnEveryLane(t *testing.T) {
	d := &dayCount{}
	const day, limit = "2026-08-15", 2
	for i := range siteDay * limit {
		lanes := publicLanes(fmt.Sprintf("2001:470:1f00:%x::1", i))
		if d.out(lanes, day, limit) {
			t.Fatalf("call %d of %d across fresh /64s was refused before the /48 was spent", i+1, siteDay*limit)
		}
		d.serve(lanes, day, limit)
	}
	if !d.out(publicLanes("2001:470:1f00:ffff::1"), day, limit) {
		t.Fatal("a /48 that spent siteDay visitors' worth still admits a fresh /64")
	}
	if d.out(publicLanes("2001:470:1f01::1"), day, limit) {
		t.Fatal("the neighbouring /48 is spent by the first one's count")
	}
	v4 := publicLanes("203.0.113.9")
	if len(v4) != 1 {
		t.Fatalf("an IPv4 visitor is charged to %d lanes, want 1", len(v4))
	}
}

// lane is one visitor counted on one lane, as a test names one.
func lane(key string) []address.Bucket { return []address.Bucket{{Key: key, Scale: 1}} }

// servablePool stands up a pool that CAN answer, so a test whose subject is the
// ceiling is not answered by the route check instead. That check runs first by design
// — a lane that cannot answer says so plainly — which means every test about counting
// has to get past it.
func servablePool(t *testing.T) {
	t.Helper()
	prev := resolveFreeRoute
	resolveFreeRoute = func(string, string) *modelRoute { return &modelRoute{providerName: "stub"} }
	t.Cleanup(func() { resolveFreeRoute = prev })
}

// ---- who is calling ---------------------------------------------------------

// THE HOST'S ANSWER WINS, and a live defect is why this is asserted. Run as a
// subsystem, ai is reached over a unix socket: the peer is empty and identical for
// everyone, so a visitor derived from it is one visitor for the whole internet and
// the daily ceiling becomes one bucket. The host resolves the caller and stamps it.
func TestTheHostStatesWhoIsCalling(t *testing.T) {
	c := visit(http.MethodPost, "/v1/chat/public")
	c.Fiber().Request().Header.Set(address.Header, "198.51.100.7")
	c.Fiber().Request().Header.Set("CF-Connecting-IP", "203.0.113.200")
	if got := c.stated(); got != "198.51.100.7" {
		t.Fatalf("stated = %q; the host's hardened answer must win over the edge header", got)
	}
	// A socket names nobody, so the stated address is what the lane counts by.
	if got := publicAddr("", c.stated()); got != "198.51.100.7" {
		t.Fatalf("publicAddr over a socket = %q, want the stated address", got)
	}
	// Two callers the host tells apart must be two visitors. This is the property the
	// ceiling rests on, and the one that was lost.
	if publicVisitor(publicAddr("", "198.51.100.1")) == publicVisitor(publicAddr("", "198.51.100.2")) {
		t.Fatal("two callers the host told apart became one visitor")
	}
}

// A STRANGER CANNOT NAME ITSELF. Reached directly the peer is routable, and it is the
// one thing on the request the caller could not write, so it decides — otherwise a
// visitor mints a fresh ceiling per request by setting a header.
func TestACallerCannotStateItsOwnAddress(t *testing.T) {
	c := from(visit(http.MethodPost, "/v1/chat/public"), "203.0.113.9:41000")
	c.Fiber().Request().Header.Set(address.Header, "198.51.100.255") // the forgery
	if got := publicAddr(c.Fiber().IP(), c.stated()); got != "203.0.113.9" {
		t.Fatalf("publicAddr = %q; a routable peer must beat anything the caller states", got)
	}
}

// Nothing in front and nothing stated: the peer is all there is, and it is enough —
// otherwise a deployment with nothing before it has no visitor at all.
func TestServedDirectlyTheLaneReadsThePeer(t *testing.T) {
	c := from(visit(http.MethodPost, "/v1/chat/public"), "203.0.113.9:41000")
	if got := publicAddr(c.Fiber().IP(), c.stated()); got != "203.0.113.9" {
		t.Fatalf("direct publicAddr = %q, want the socket peer 203.0.113.9", got)
	}
}
