// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/ai/object"
)

// newTestGate builds a BalanceGate pointed at the given Commerce base URL with a
// fresh, isolated ledger (no IAM dependency, no shared global state). It mirrors
// InitBalanceGate but avoids reading app config so the test is hermetic.
func newTestGate(endpoint, token string, ttl time.Duration) *BalanceGate {
	return &BalanceGate{
		ledger:       object.NewBalanceLedger(ttl),
		userKeyCache: make(map[string]*userKeyCacheEntry),
		inflight:     make(map[string]struct{}),
		endpoint:     strings.TrimRight(endpoint, "/"),
		token:        token,
		client:       &http.Client{Timeout: balanceHTTPTimeout},
	}
}

// TestFetchBalanceCallsCommerceCanonicalPath asserts the gate hits exactly the
// canonical Commerce read endpoint (/v1/billing/balance, never /api/), passes
// the user + currency, and forwards the bearer token.
func TestFetchBalanceCallsCommerceCanonicalPath(t *testing.T) {
	var gotPath, gotQueryUser, gotCurrency, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQueryUser = r.URL.Query().Get("user")
		gotCurrency = r.URL.Query().Get("currency")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"user":"hanzo/alice","currency":"usd","balance":5000,"holds":0,"available":4200}`))
	}))
	defer srv.Close()

	bg := newTestGate(srv.URL, "svc-token-xyz", balanceCacheTTL)
	balance, err := bg.fetchBalance("hanzo/alice", "hanzo")
	if err != nil {
		t.Fatalf("fetchBalance returned error: %v", err)
	}
	if balance != 4200 {
		t.Errorf("expected available=4200 cents, got %d", balance)
	}
	if gotPath != "/v1/billing/balance" {
		t.Errorf("expected canonical path /v1/billing/balance, got %q (no /api/ prefix allowed)", gotPath)
	}
	if gotQueryUser != "hanzo/alice" {
		t.Errorf("expected user=hanzo/alice, got %q", gotQueryUser)
	}
	if gotCurrency != "usd" {
		t.Errorf("expected currency=usd, got %q", gotCurrency)
	}
	if gotAuth != "Bearer svc-token-xyz" {
		t.Errorf("expected bearer token forwarded, got %q", gotAuth)
	}
}

// TestFetchBalanceEscapesUserKey ensures the owner/name slash is URL-encoded.
func TestFetchBalanceEscapesUserKey(t *testing.T) {
	var rawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		w.Write([]byte(`{"available":1}`))
	}))
	defer srv.Close()

	bg := newTestGate(srv.URL, "", balanceCacheTTL)
	if _, err := bg.fetchBalance("hanzo/bob", "hanzo"); err != nil {
		t.Fatalf("fetchBalance error: %v", err)
	}
	if !strings.Contains(rawQuery, "user="+url.QueryEscape("hanzo/bob")) {
		t.Errorf("user key not URL-escaped in query: %q", rawQuery)
	}
}

// TestCheckBalanceGatesOnInsufficientFunds is the core enforcement assertion:
// a zero/negative balance must gate; a positive balance must pass.
func TestCheckBalanceGatesOnInsufficientFunds(t *testing.T) {
	cases := []struct {
		name           string
		available      string
		wantSufficient bool
		wantCents      int64
	}{
		{"positive balance passes", `{"available":1500}`, true, 1500},
		{"zero balance gates", `{"available":0}`, false, 0},
		{"negative balance gates", `{"available":-100}`, false, -100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(tc.available))
			}))
			defer srv.Close()

			bg := newTestGate(srv.URL, "", balanceCacheTTL)
			sufficient, cents := bg.checkBalance("hanzo/user-"+tc.name, "hanzo", "hanzo/user-"+tc.name)
			if sufficient != tc.wantSufficient {
				t.Errorf("sufficient=%v, want %v", sufficient, tc.wantSufficient)
			}
			if cents != tc.wantCents {
				t.Errorf("cents=%d, want %d", cents, tc.wantCents)
			}
		})
	}
}

// TestCheckBalanceReservationAware proves the gate subtracts outstanding
// reservations: a fully-reserved balance gates even though the raw balance is
// positive — the double-spend fix is visible at the gate, not only the controller.
func TestCheckBalanceReservationAware(t *testing.T) {
	bg := newTestGate("http://unused", "", balanceCacheTTL)
	bg.ledger.SetBalance("hanzo/acct", 100)
	if !bg.ledger.Reserve("hanzo/acct", 100) {
		t.Fatal("reserve of the full balance must succeed")
	}
	sufficient, cents := bg.checkBalance("hanzo/acct", "hanzo", "hanzo/acct")
	if sufficient {
		t.Error("fully-reserved balance must gate (no spendable funds)")
	}
	if cents != 0 {
		t.Errorf("available=%d, want 0", cents)
	}
}

// TestCheckBalanceStaleWindowReflectsSettle proves the stale-cache window is
// closed: after the local balance is drained via settles, the gate reports
// insufficient WITHOUT a fresh Commerce fetch.
func TestCheckBalanceStaleWindowReflectsSettle(t *testing.T) {
	bg := newTestGate("http://unused", "", balanceCacheTTL)
	bg.ledger.SetBalance("hanzo/acct", 100)
	if !bg.ledger.Reserve("hanzo/acct", 100) {
		t.Fatal("reserve must pass")
	}
	bg.ledger.Settle("hanzo/acct", 100, 100)
	if sufficient, cents := bg.checkBalance("hanzo/acct", "hanzo", "hanzo/acct"); sufficient || cents != 0 {
		t.Errorf("drained balance must gate within the cache window, got sufficient=%v cents=%d", sufficient, cents)
	}
}

// TestCheckBalanceFailsClosedOnColdNonExemptOrg: a cold subject whose Commerce
// lookup errors must fail CLOSED for a non-exempt org (outage != free inference).
func TestCheckBalanceFailsClosedOnColdNonExemptOrg(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	bg := newTestGate(srv.URL, "", balanceCacheTTL)
	sufficient, cents := bg.checkBalance("acme", "acme", "acme/user")
	if sufficient {
		t.Error("expected fail-CLOSED for a cold non-exempt org when Commerce errors")
	}
	if cents != 0 {
		t.Errorf("expected 0 cents on error, got %d", cents)
	}
}

// TestCheckBalanceNoExemption: the exempt concept is REMOVED. Subjects formerly
// listed in BALANCE_EXEMPT_USERS (admin/hanzo-cloud; hanzo/z — a normal hanzo
// customer, never an admin) get NO special treatment: on a Commerce error a cold
// subject fails CLOSED like any other. There is no per-user, per-org, or fail-open
// escape — AI is prepaid for everyone, so an outage can never hand out free
// inference to a "house" identity.
func TestCheckBalanceNoExemption(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	bg := newTestGate(srv.URL, "", balanceCacheTTL)
	for _, s := range []struct{ subject, ns, key string }{
		{"admin/hanzo-cloud", "admin", "admin/hanzo-cloud"}, // formerly exempt service account
		{"hanzo/z", "hanzo", "hanzo/z"},                     // formerly exempt — a normal customer
		{"house", "house", "house/anyone"},                  // formerly a whole-org exemption
		{"acme", "acme", "acme/user"},                       // never exempt
	} {
		if sufficient, cents := bg.checkBalance(s.subject, s.ns, s.key); sufficient || cents != 0 {
			t.Errorf("%s must fail-CLOSED (no exemption), got sufficient=%v cents=%d", s.subject, sufficient, cents)
		}
	}
}

// TestCheckBalanceServesStaleOnErrorForActiveOrg: an active (cached, funded) org
// is NOT blocked by a transient Commerce blip — the stale entry is served while
// the async refresh fails harmlessly. A zero-TTL ledger makes the entry stale.
func TestCheckBalanceServesStaleOnErrorForActiveOrg(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	bg := newTestGate(srv.URL, "", 0) // ttl=0 => any entry is immediately stale
	bg.ledger.SetBalance("acme", 500)
	sufficient, cents := bg.checkBalance("acme", "acme", "acme/user")
	if !sufficient || cents != 500 {
		t.Errorf("expected stale-serve (sufficient=true, cents=500) on a blip, got (%v, %d)", sufficient, cents)
	}
}

// TestCheckBalanceCachesWithinTTL asserts a second lookup inside the TTL does
// not re-hit Commerce — the hot path must not make a network call per request.
func TestCheckBalanceCachesWithinTTL(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`{"available":900}`))
	}))
	defer srv.Close()

	bg := newTestGate(srv.URL, "", balanceCacheTTL)
	if s, _ := bg.checkBalance("hanzo/cacheme", "hanzo", "hanzo/cacheme"); !s {
		t.Fatal("first check should pass")
	}
	if s, _ := bg.checkBalance("hanzo/cacheme", "hanzo", "hanzo/cacheme"); !s {
		t.Fatal("second check should pass from cache")
	}
	if calls != 1 {
		t.Errorf("expected exactly 1 Commerce call within TTL, got %d", calls)
	}
}

// TestBalanceExemptPaths locks in which paths bypass the gate.
func TestBalanceExemptPaths(t *testing.T) {
	exempt := []string{
		"/v1/health", "/health",
		"/v1/metrics", "/metrics",
		"/v1/get-version-info", "/v1/get-system-info",
		"/v1/signin", "/v1/signout", "/v1/get-account",
		// The model catalog is metadata, not metered inference: reading the
		// available-models list must never require a positive balance (gating it
		// 402s a funded-but-zero / M2M caller browsing the catalog). Still
		// authenticated — balance-exemption is not auth-exemption.
		"/v1/models", "/v1/models/gpt-4",
		// Usage/spend READS are account metadata like the model catalog — a
		// $0-balance org must be able to SEE its own usage (to learn it needs
		// credits), so the usage panel never 402s.
		"/v1/get-cloud-usages", "/v1/get-usages", "/v1/get-range-usages",
		// Routing configuration is metadata (clients read defaults on boot;
		// operators flip them) — never wallet-gated. Auth still applies.
		"/v1/get-routing-defaults",
		"/v1/get-org-settings", "/v1/add-org-settings",
		"/v1/update-org-settings", "/v1/delete-org-settings",
		"/v1/export-routing-ledger",
	}
	for _, p := range exempt {
		if !isBalanceExempt(p) {
			t.Errorf("path %q should be balance-exempt", p)
		}
	}
	gated := []string{
		"/v1/chat/completions", "/v1/completions",
		"/v1/embeddings", "/v1/images/generations",
	}
	for _, p := range gated {
		if isBalanceExempt(p) {
			t.Errorf("paid path %q must NOT be balance-exempt", p)
		}
	}
}

// TestUserKeyCacheRoundTrip verifies the token->(subject,namespace) cache stores
// and expires entries per userKeyCacheTTL.
func TestUserKeyCacheRoundTrip(t *testing.T) {
	bg := newTestGate("http://unused", "", balanceCacheTTL)
	if s, ns, uk, ok := bg.getUserKeyCached("tok"); ok || s != "" || ns != "" || uk != "" {
		t.Errorf("expected empty on cache miss, got (%q,%q,%q,%v)", s, ns, uk, ok)
	}
	bg.setUserKeyCache("tok", "hanzo/carol", "hanzo", "hanzo/carol")
	if s, ns, uk, ok := bg.getUserKeyCached("tok"); !ok || s != "hanzo/carol" || ns != "hanzo" || uk != "hanzo/carol" {
		t.Errorf("expected (hanzo/carol,hanzo,hanzo/carol,true) from cache, got (%q,%q,%q,%v)", s, ns, uk, ok)
	}
	// Force staleness.
	bg.userKeyMu.Lock()
	bg.userKeyCache["tok"].fetchedAt = time.Now().Add(-2 * userKeyCacheTTL)
	bg.userKeyMu.Unlock()
	if _, _, _, ok := bg.getUserKeyCached("tok"); ok {
		t.Error("expected miss on stale entry")
	}
}

// TestIsJwtTokenLike guards the cheap local JWT heuristic.
func TestIsJwtTokenLike(t *testing.T) {
	jwt := "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJ1c2VyIn0.signaturepart"
	if !isJwtTokenLike(jwt) {
		t.Error("expected a 3-segment JWT-like token to be recognized")
	}
	for _, notJwt := range []string{"hk-abcdef", "sk-xyz", "", "a.b", "a.b.c.d"} {
		if isJwtTokenLike(notJwt) {
			t.Errorf("token %q should not be JWT-like", notJwt)
		}
	}
}
