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
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/ai/object"
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

// Reached through our own ingress the peer is private, and the edge's header is the
// only thing that names the visitor.
func TestForwardedAddressIsBelievedFromOurOwnIngress(t *testing.T) {
	viaIngress := func(cf string) string { return publicAddr("10.244.1.7", cf) }
	if got := viaIngress("198.51.100.77"); got != "198.51.100.77" {
		t.Fatalf("publicAddr = %q, want the edge-observed 198.51.100.77", got)
	}
	if publicVisitor(viaIngress("198.51.100.1")) == publicVisitor(viaIngress("198.51.100.2")) {
		t.Fatal("two visitors behind the ingress collapsed into one bucket")
	}
	// No edge header behind the ingress: everyone shares the ingress's count. Safe
	// direction — the lane closes early rather than becoming unbounded.
	if got := publicAddr("10.244.1.7", ""); got != "10.244.1.7" {
		t.Fatalf("with no edge header the peer must be the key, got %q", got)
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
// at the door, and reading it can never raise it — so a visitor refused by a route
// that would not resolve, a vendor that never answered, or a pod mid-roll keeps every
// call they arrived with.
func TestReadingTheCeilingNeverRaisesIt(t *testing.T) {
	d := &dayCount{}
	const day, limit = "2026-08-15", 3

	for i := 0; i < 20; i++ {
		d.spent("visitor:a", day, limit)
	}
	if got := d.seen["visitor:a"]; got != 0 {
		t.Fatalf("twenty reads left a count of %d; a read must cost a visitor nothing", got)
	}
	if d.spent("visitor:a", day, limit) {
		t.Fatal("a visitor who was never served reads as spent")
	}

	// At the ceiling, a refusal leaves the count where it is.
	for i := 0; i < limit; i++ {
		serve(d, "visitor:a", day, limit)
	}
	for i := 0; i < 5; i++ {
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
	for i := 0; i < publicVisitors; i++ {
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
// ceiling is read at the door and raised where the call was served, so calls in
// flight for one visitor can all be admitted by the same last unit — a ceiling of
// five occasionally serving six. Overshoot is generous and bounded by how many calls
// one caller has open at once. The strict version — take at the door — charged a
// visitor for a route that never resolved and a vendor that never answered, which is
// a defect the visitor feels and we never see.
func TestTheCountNeverRunsPastTheCeiling(t *testing.T) {
	d := &dayCount{}
	const limit = 50
	var wg sync.WaitGroup
	for i := 0; i < 500; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			serve(d, "visitor:a", "2026-08-15", limit)
		}()
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
	if publicMaxTokens <= 0 || publicMaxTokens != widgetMaxTokens {
		t.Fatalf("publicMaxTokens = %d, want the widget cap %d", publicMaxTokens, widgetMaxTokens)
	}
}

// ---- the handler itself ---------------------------------------------------

// publicCall drives the real ChatCompletionsPublic handler and returns the recorder.
func publicCall(t *testing.T, remote, cf string) *ApiController {
	t.Helper()
	c := from(visit(http.MethodPost, "/v1/chat/public"), remote)
	c.Fiber().Request().SetBody([]byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	if cf != "" {
		c.Fiber().Request().Header.Set("CF-Connecting-IP", cf)
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

// ---- nothing is charged for a call we cannot serve -------------------------

// THE DEFECT THIS CLOSES. The lane took the visitor's call at the door, before it
// knew anything could answer one. A misconfigured pool therefore refused every caller
// AND spent their day on the way out — the ceiling emptied without a model ever being
// reached, and the host's half of that count PERSISTS, so a pod restart does not
// undo it.
//
// A day is spent on answers. Both ends of the lane are checked here: the pool with no
// route, which is refused at the door, and the pool that HAS a route whose provider
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
			object.SetSpent(func(_ context.Context, _, _ string) (bool, error) { return false, nil })
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
	c.Fiber().Request().Header.Set(object.ClientIPHeader, "198.51.100.7")
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
	c.Fiber().Request().Header.Set(object.ClientIPHeader, "198.51.100.255") // the forgery
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
