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

package routers

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/ai/address"
	"github.com/hanzoai/ai/controllers"
	"github.com/hanzoai/ai/internal/authtest"
	iam "github.com/hanzoai/ai/internal/iam"
)

// The two ceilings, armed on the free tier and watching.
//
// It returns what they have SEEN — how many requests each was asked about — because
// that is the property under test: a ceiling that is never asked bounds nothing, and
// a request that reaches neither is served free however small the numbers are.
func ceilings(t *testing.T) func() (rate, quota int) {
	t.Helper()
	rl, q := rateLimiterInstance, quotaInstance
	rateLimiterInstance = NewRateLimiter(func(string) Tier { return TierZenFree }, time.Hour)
	quotaInstance = NewQuota(func(string) Tier { return TierZenFree }, time.Hour)
	t.Cleanup(func() {
		rateLimiterInstance.Stop()
		quotaInstance.Stop()
		rateLimiterInstance, quotaInstance = rl, q
	})
	return func() (int, int) {
		a, d := rateLimiterInstance.Metrics()
		qa, qd := quotaInstance.Metrics()
		return int(a + d), int(qa + qd)
	}
}

// billing arms the balance gate, which is what lets a billing subject resolve at
// all. Its endpoint is never reached — every test here seeds what it needs.
func billing(t *testing.T) {
	t.Helper()
	prev := balanceGate
	balanceGate = newTestGate("http://unused", "", balanceCacheTTL)
	t.Cleanup(func() { balanceGate = prev })
}

// burst is what the free tier admits at once — the number a caller can spend
// before the ceiling is the thing answering.
func burst() int { return tierLimits[TierZenFree] / 5 }

// browser is a request carrying the first-party cookie a sign-in leaves, and
// nothing else: no Authorization, no X-API-Key, no api_key. It is how the console
// and the chat surface call this API.
func browser(t *testing.T, u iam.User, path string) probe {
	t.Helper()
	return ask(http.MethodPost, path).
		body([]byte(`{}`)).
		with("Cookie", "hanzo_iam_token="+authtest.Token(t, u))
}

// A COOKIE SESSION IS A CALLER, AND A CALLER MEETS THE CEILINGS.
//
// The browser holds a signed token in a first-party cookie and sends no header the
// rate limiter used to look for. The balance gate resolves a real payer from that
// same cookie (resolveBillingKey source 1), so the console, the chat surface and
// every other browser caller have a perfectly good identity — and used to run with
// no rate and no quota at all, because the limiter asked the transport instead of
// asking who was calling.
func TestACookieSessionMeetsBothCeilings(t *testing.T) {
	billing(t)
	seen := ceilings(t)

	user := iam.User{Owner: "acme", Name: "alice"}
	p := browser(t, user, "/v1/chat/completions").through(RateLimitFilter)
	if p.status() != http.StatusOK {
		t.Fatalf("the session request was answered %d; it is inside its ceilings and must be served", p.status())
	}

	rate, quota := seen()
	if rate != 1 || quota != 1 {
		t.Fatalf("a cookie session met the rate ceiling %d time(s) and the quota %d time(s), want 1 and 1", rate, quota)
	}

	// And it is counted against the SUBJECT THAT PAYS for it, so rate, quota and
	// money all name one tenant.
	want := user.PayerSubject("")
	rateLimiterInstance.mu.RLock()
	_, bucketed := rateLimiterInstance.keys[want]
	rateLimiterInstance.mu.RUnlock()
	if !bucketed {
		t.Fatalf("the session was not counted against its payer %q", want)
	}
}

// iamDoor stands up the IAM this estate asks about a key, answering for the keys it
// is handed and refusing everything else — which is the half that matters, since the
// bucket a key gets must depend on IAM having issued it.
func iamDoor(t *testing.T, users map[string]iam.User, orgs map[string]string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/iam/keys/principal":
			if u, ok := users[r.URL.Query().Get("accessKey")]; ok {
				json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": u})
				return
			}
		case "/v1/iam/keys/org":
			if org, ok := orgs[r.URL.Query().Get("accessKey")]; ok {
				json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": map[string]string{"org": org}})
				return
			}
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "error", "code": "key_unknown", "msg": "no such key"})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("IAM_URL", srv.URL)
	t.Setenv("IAM_CLIENT_ID", "hanzo-ai")
	t.Setenv("IAM_CLIENT_SECRET", "secret")
}

// THE WIRE A KEY ARRIVES ON DOES NOT CHANGE WHO IS CALLING.
//
// /v1/messages is the Anthropic protocol and carries its sk- key on x-api-key. That
// is a published endpoint and the key behind it is an ordinary IAM key, so the
// ceilings must count the call against the tenant that owns it — the same tenant,
// and the same bucket, as if the very same key had come in on Authorization.
func TestAKeyNamesItsTenantOnEveryTransport(t *testing.T) {
	const key = "sk-owned-by-acme"
	iamDoor(t, map[string]iam.User{key: {Owner: "acme", Name: "alice"}}, nil)
	billing(t)
	seen := ceilings(t)

	for _, header := range []string{"x-api-key", "Authorization"} {
		value := key
		if header == "Authorization" {
			value = "Bearer " + key
		}
		p := ask(http.MethodPost, "/v1/messages").body([]byte(`{}`)).with(header, value).through(RateLimitFilter)
		if p.status() != http.StatusOK {
			t.Fatalf("%s: answered %d", header, p.status())
		}
	}

	rate, quota := seen()
	if rate != 2 || quota != 2 {
		t.Fatalf("two calls met the rate ceiling %d time(s) and the quota %d time(s), want 2 and 2", rate, quota)
	}

	want := (&iam.User{Owner: "acme", Name: "alice"}).PayerSubject("")
	rateLimiterInstance.mu.RLock()
	keys := len(rateLimiterInstance.keys)
	_, bucketed := rateLimiterInstance.keys[want]
	rateLimiterInstance.mu.RUnlock()
	if !bucketed || keys != 1 {
		t.Fatalf("one key on two transports made %d bucket(s); it must make one, %q", keys, want)
	}
}

// A CALLER CANNOT MINT AN ALLOWANCE.
//
// /v1/messages is the Anthropic wire protocol and carries its sk- key on x-api-key,
// a header the caller writes. Counting a request against the string it presented
// hands anyone who varies that string a fresh, empty bucket every time — an
// unbounded lane, and an unbounded map behind it.
//
// Thirty-six requests, each with a different credential, from one caller. The
// ceiling is twelve.
func TestAMintedCredentialGetsNoFreshAllowance(t *testing.T) {
	billing(t)
	ceilings(t)

	served := 0
	for i := 0; i < 3*burst(); i++ {
		p := ask(http.MethodPost, "/v1/messages").
			body([]byte(`{}`)).
			with("x-api-key", fmt.Sprintf("sk-mint-%d", i)).
			through(RateLimitFilter)
		if p.status() != http.StatusTooManyRequests {
			served++
		}
	}

	if served != burst() {
		t.Fatalf("a caller minting a credential per request was served %d of %d; the ceiling is %d",
			served, 3*burst(), burst())
	}
}

// BILLING UNCONFIGURED IS NOT ONE BUCKET FOR THE WORLD.
//
// A deployment with no commerce endpoint has no balance gate, and resolveBillingKey
// then names no subject for ANY request — that is its documented fail-open. Bucketing
// every one of those requests together would hold the whole deployment to one free
// tier's worth of traffic, so the callers a subject cannot name are told apart by the
// address they arrived from instead.
func TestWithBillingUnconfiguredCallersAreStillToldApart(t *testing.T) {
	prev := balanceGate
	balanceGate = nil
	t.Cleanup(func() { balanceGate = prev })
	ceilings(t)

	// What the subject resolves to with no gate: nothing, for everyone.
	c := ask(http.MethodPost, "/v1/messages").with("x-api-key", "sk-anything").Ctx
	if s, ns, uk := resolveBillingKey(c); s != "" || ns != "" || uk != "" {
		t.Fatalf("with no balance gate resolveBillingKey = (%q, %q, %q), want three empties", s, ns, uk)
	}

	drive := func(addr string) int {
		served := 0
		for i := 0; i < 2*burst(); i++ {
			p := ask(http.MethodPost, "/v1/messages").
				body([]byte(`{}`)).
				with(address.Header, addr).
				through(RateLimitFilter)
			if p.status() != http.StatusTooManyRequests {
				served++
			}
		}
		return served
	}

	if got := drive("198.51.100.1"); got != burst() {
		t.Fatalf("the first caller was served %d of %d, want its whole burst of %d and no more", got, 2*burst(), burst())
	}
	// THE POINT: the second caller's lane was never touched by the first.
	if got := drive("198.51.100.2"); got != burst() {
		t.Fatalf("a second caller was served %d of %d — the deployment is one bucket", got, burst())
	}
}

// ANONYMOUS TRAFFIC CANNOT CLOSE A PAYING CALLER'S LANE.
//
// Junk arrives from the same address as a customer — a shared egress, a proxy, a
// deliberate attempt. The customer resolves a subject, so they are counted against
// their own name and the address never enters into it.
func TestAnonymousTrafficCannotCloseAPayingCallersLane(t *testing.T) {
	billing(t)
	ceilings(t)

	const shared = "198.51.100.30"
	for i := 0; i < 4*burst(); i++ {
		ask(http.MethodPost, "/v1/messages").
			body([]byte(`{}`)).
			with(address.Header, shared).
			through(RateLimitFilter)
	}

	p := browser(t, iam.User{Owner: "acme", Name: "alice"}, "/v1/chat/completions").
		with(address.Header, shared).
		through(RateLimitFilter)
	if p.status() == http.StatusTooManyRequests {
		t.Fatal("anonymous traffic from a shared address closed a named caller's lane")
	}
}

// NOBODY NAMES THEIR OWN BUCKET.
//
// The address a caller arrived from is the one thing on a request they could not
// write — reached directly the socket peer decides and a stated address is ignored,
// so the bucket cannot be minted by setting a header. Reached through our own
// ingress the peer is private and the layer in front names the caller, which is the
// only way it is believed.
func TestNobodyNamesTheirOwnBucket(t *testing.T) {
	at := func(peer, stated string) string {
		c := zip.New(zip.Config{DisableStartupMessage: true}).TestCtx(http.MethodPost, "/v1/messages")
		sock, err := net.ResolveTCPAddr("tcp", peer+":41000")
		if err != nil {
			t.Fatalf("peer %q: %v", peer, err)
		}
		c.Fiber().RequestCtx().SetRemoteAddr(sock)
		if stated != "" {
			c.Fiber().Request().Header.Set(address.Header, stated)
		}
		return controllers.Visitor(c)
	}

	honest := at("203.0.113.9", "")
	forged := at("203.0.113.9", "198.51.100.255")
	if honest != forged {
		t.Fatal("a caller changed its own bucket by stating an address")
	}
	if at("203.0.113.9", "") == at("203.0.113.10", "") {
		t.Fatal("two callers on two addresses share one bucket")
	}
	// Behind our own ingress the peer is ours, so the edge's answer is the caller's.
	if at("10.244.1.7", "198.51.100.1") == at("10.244.1.7", "198.51.100.2") {
		t.Fatal("two callers the ingress told apart became one bucket")
	}
}

// A VISITOR IS NOT A QUESTION FOR COMMERCE.
//
// A plan belongs to an account, and an address is not one. Looking one up per address
// seen would answer nothing and would point every flood arriving here at the billing
// system, one lookup at a time.
func TestAVisitorIsNeverAQuestionForCommerce(t *testing.T) {
	asked := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked <- r.URL.Query().Get("user")
		w.Write([]byte(`{"tier":{"name":"zen-pro"}}`))
	}))
	t.Cleanup(srv.Close)

	prev := tierCache
	tierCache = &TierCache{
		entries:  map[string]*tierCacheEntry{},
		endpoint: srv.URL,
		client:   srv.Client(),
		inflight: map[string]struct{}{},
	}
	t.Cleanup(func() { tierCache = prev })

	for _, who := range []string{unaddressed, "visitor:" + fmt.Sprintf("%032x", 1)} {
		if got := DefaultTierFunc(who); got != TierZenFree {
			t.Errorf("%s resolved tier %q, want the free tier", who, got)
		}
	}
	select {
	case who := <-asked:
		t.Fatalf("commerce was asked about %q", who)
	case <-time.After(250 * time.Millisecond):
	}

	// An account IS asked about, so the lookup still works for the callers it is for.
	DefaultTierFunc("acme")
	select {
	case who := <-asked:
		if who != "acme" {
			t.Fatalf("commerce was asked about %q", who)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("commerce was never asked about an account")
	}
}

// An exempt path is exempt, and a path outside the API is not the API's to bound.
func TestExemptPathsAreStillExempt(t *testing.T) {
	billing(t)
	seen := ceilings(t)

	for _, path := range []string{"/v1/health", "/metrics", "/v1/ai/version", "/login"} {
		p := browser(t, iam.User{Owner: "acme", Name: "alice"}, path).through(RateLimitFilter)
		if p.status() != http.StatusOK {
			t.Errorf("%s was answered %d", path, p.status())
		}
	}
	if rate, quota := seen(); rate != 0 || quota != 0 {
		t.Fatalf("exempt paths met the ceilings %d and %d time(s), want none", rate, quota)
	}
}
