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
)

// newTestGate builds a BalanceGate pointed at the given Commerce base URL with
// no IAM dependency. It mirrors InitBalanceGate but avoids reading app config
// so the test is hermetic.
func newTestGate(endpoint, token string) *BalanceGate {
	return &BalanceGate{
		entries:      make(map[string]*balanceCacheEntry),
		userKeyCache: make(map[string]*userKeyCacheEntry),
		inflight:     make(map[string]struct{}),
		endpoint:     strings.TrimRight(endpoint, "/"),
		token:        token,
		client:       &http.Client{Timeout: balanceHTTPTimeout},
	}
}

// TestFetchBalanceCallsCommerceCanonicalPath asserts the gate hits exactly the
// canonical Commerce read endpoint (/v1/billing/balance, never /api/), passes
// the subject + currency, forwards the bearer token, AND scopes the read to the
// caller's org namespace via X-Hanzo-Org — the per-org isolation invariant: the
// balance read must land in the same namespace the credit and usage debit use.
func TestFetchBalanceCallsCommerceCanonicalPath(t *testing.T) {
	var gotPath, gotQueryUser, gotCurrency, gotAuth, gotNamespace string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQueryUser = r.URL.Query().Get("user")
		gotCurrency = r.URL.Query().Get("currency")
		gotAuth = r.Header.Get("Authorization")
		gotNamespace = r.Header.Get("X-Hanzo-Org")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"user":"hanzo/alice","currency":"usd","balance":5000,"holds":0,"available":4200}`))
	}))
	defer srv.Close()

	bg := newTestGate(srv.URL, "svc-token-xyz")
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
	if gotNamespace != "hanzo" {
		t.Errorf("expected X-Hanzo-Org=hanzo (per-org scope), got %q", gotNamespace)
	}
}

// TestFetchBalanceScopesToNamespace proves the balance read for org A is scoped
// to A's namespace and NOT B's — the no-cross-org-leak invariant at the gate.
func TestFetchBalanceScopesToNamespace(t *testing.T) {
	var gotNamespace, gotUser string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotNamespace = r.Header.Get("X-Hanzo-Org")
		gotUser = r.URL.Query().Get("user")
		w.Write([]byte(`{"available":1}`))
	}))
	defer srv.Close()

	bg := newTestGate(srv.URL, "")
	if _, err := bg.fetchBalance("acme", "acme"); err != nil {
		t.Fatalf("fetchBalance error: %v", err)
	}
	if gotNamespace != "acme" {
		t.Errorf("org acme balance read scoped to namespace %q, want acme", gotNamespace)
	}
	if gotUser != "acme" {
		t.Errorf("org acme balance read used subject %q, want acme", gotUser)
	}
	if gotNamespace == "other-org" {
		t.Errorf("cross-org leak: acme read used another org's namespace")
	}
}

// TestFetchBalanceEscapesUserKey ensures the owner/name slash is URL-encoded so
// Commerce receives the intended user identifier.
func TestFetchBalanceEscapesUserKey(t *testing.T) {
	var rawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		w.Write([]byte(`{"available":1}`))
	}))
	defer srv.Close()

	bg := newTestGate(srv.URL, "")
	if _, err := bg.fetchBalance("hanzo/bob", "hanzo"); err != nil {
		t.Fatalf("fetchBalance error: %v", err)
	}
	if !strings.Contains(rawQuery, "user="+url.QueryEscape("hanzo/bob")) {
		t.Errorf("user key not URL-escaped in query: %q", rawQuery)
	}
}

// TestCheckBalanceGatesOnInsufficientFunds is the core enforcement assertion:
// a zero balance must gate (sufficient=false); a positive balance must pass.
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

			bg := newTestGate(srv.URL, "")
			subject := "hanzo/user-" + tc.name
			sufficient, cents := bg.checkBalance(subject, "hanzo")
			if sufficient != tc.wantSufficient {
				t.Errorf("sufficient=%v, want %v", sufficient, tc.wantSufficient)
			}
			if cents != tc.wantCents {
				t.Errorf("cents=%d, want %d", cents, tc.wantCents)
			}
		})
	}
}

// TestCheckBalanceFailsClosedOnColdNonExemptOrg asserts the bleed fix: a hard
// cache-miss whose Commerce lookup errors (Commerce unreachable) must fail
// CLOSED (sufficient=false -> 402) for a non-exempt org, so a Commerce outage
// cannot become an unmetered bleed.
func TestCheckBalanceFailsClosedOnColdNonExemptOrg(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	bg := newTestGate(srv.URL, "")
	sufficient, cents := bg.checkBalance("acme", "acme")
	if sufficient {
		t.Error("expected fail-CLOSED (sufficient=false) for a cold non-exempt org when Commerce errors")
	}
	if cents != 0 {
		t.Errorf("expected 0 cents on error, got %d", cents)
	}
}

// TestCheckBalanceFailsOpenForExemptOrg asserts an org in BALANCE_EXEMPT_USERS
// (internal/house org) is never blocked by a Commerce-error cold miss — it
// keeps the legacy fail-open so internal traffic survives an outage.
func TestCheckBalanceFailsOpenForExemptOrg(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	bg := newTestGate(srv.URL, "")
	bg.exemptOrgs = parseExemptOrgs("admin/hanzo-cloud,hanzo/z")
	if sufficient, _ := bg.checkBalance("hanzo", "hanzo"); !sufficient {
		t.Error("expected fail-OPEN for exempt org 'hanzo' on Commerce error")
	}
	if sufficient, _ := bg.checkBalance("admin", "admin"); !sufficient {
		t.Error("expected fail-OPEN for exempt org 'admin' on Commerce error")
	}
	if sufficient, _ := bg.checkBalance("acme", "acme"); sufficient {
		t.Error("expected fail-CLOSED for non-exempt org 'acme' even when other orgs are exempt")
	}
}

// TestCheckBalanceFailOpenOverride asserts the BALANCE_GATE_FAIL_OPEN_ON_ERROR
// escape hatch restores the legacy fail-open for every org (no-rebuild rollback).
func TestCheckBalanceFailOpenOverride(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	bg := newTestGate(srv.URL, "")
	bg.failOpenOnError = true
	if sufficient, _ := bg.checkBalance("acme", "acme"); !sufficient {
		t.Error("expected fail-OPEN when BALANCE_GATE_FAIL_OPEN_ON_ERROR override is set")
	}
}

// TestCheckBalanceServesStaleOnErrorForActiveOrg asserts an active (already
// cached, funded) org is NOT blocked by a transient Commerce blip: the stale
// entry is served while the async refresh fails harmlessly. This is the blip
// half of the contract — only COLD non-exempt orgs fail closed.
func TestCheckBalanceServesStaleOnErrorForActiveOrg(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	bg := newTestGate(srv.URL, "")
	// Funded but stale (older than the 30s TTL) -> stale-serve path. Entries are
	// keyed by subject.
	bg.entries["acme"] = &balanceCacheEntry{balanceCents: 500, fetchedAt: time.Now().Add(-time.Minute)}
	sufficient, cents := bg.checkBalance("acme", "acme")
	if !sufficient || cents != 500 {
		t.Errorf("expected stale-serve (sufficient=true, cents=500) for an active funded org on a blip, got (%v, %d)", sufficient, cents)
	}
}

// TestParseExemptOrgs covers the owner-part extraction from BALANCE_EXEMPT_USERS.
func TestParseExemptOrgs(t *testing.T) {
	got := parseExemptOrgs(" admin/hanzo-cloud , hanzo/z ,, lux ")
	for _, want := range []string{"admin", "hanzo", "lux"} {
		if _, ok := got[want]; !ok {
			t.Errorf("expected exempt org %q to be parsed, got %v", want, got)
		}
	}
	if len(got) != 3 {
		t.Errorf("expected 3 exempt orgs, got %d (%v)", len(got), got)
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

	bg := newTestGate(srv.URL, "")
	if s, _ := bg.checkBalance("hanzo/cacheme", "hanzo"); !s {
		t.Fatal("first check should pass")
	}
	if s, _ := bg.checkBalance("hanzo/cacheme", "hanzo"); !s {
		t.Fatal("second check should pass from cache")
	}
	if calls != 1 {
		t.Errorf("expected exactly 1 Commerce call within TTL, got %d", calls)
	}
}

// TestBalanceExemptPaths locks in which paths bypass the gate. Health, metrics,
// version/system info, and auth endpoints are free; paid AI paths are not.
func TestBalanceExemptPaths(t *testing.T) {
	exempt := []string{
		"/v1/health", "/health",
		"/v1/metrics", "/metrics",
		"/v1/get-version-info", "/v1/get-system-info",
		"/v1/signin", "/v1/signout", "/v1/get-account",
	}
	for _, p := range exempt {
		if !isBalanceExempt(p) {
			t.Errorf("path %q should be balance-exempt", p)
		}
	}
	gated := []string{
		"/v1/chat/completions", "/v1/completions",
		"/v1/embeddings", "/v1/images/generations", "/v1/models",
	}
	for _, p := range gated {
		if isBalanceExempt(p) {
			t.Errorf("paid path %q must NOT be balance-exempt", p)
		}
	}
}

// TestUserKeyCacheRoundTrip verifies the token->(subject,namespace) cache stores
// and expires entries per userKeyCacheTTL, preserving the per-org namespace.
func TestUserKeyCacheRoundTrip(t *testing.T) {
	bg := newTestGate("http://unused", "")
	if _, _, ok := bg.getUserKeyCached("tok"); ok {
		t.Error("expected miss on empty cache")
	}
	bg.setUserKeyCache("tok", "hanzo/carol", "hanzo")
	subject, namespace, ok := bg.getUserKeyCached("tok")
	if !ok || subject != "hanzo/carol" || namespace != "hanzo" {
		t.Errorf("expected (hanzo/carol, hanzo, true) from cache, got (%q, %q, %v)", subject, namespace, ok)
	}
	// Force staleness.
	bg.userKeyMu.Lock()
	bg.userKeyCache["tok"].fetchedAt = time.Now().Add(-2 * userKeyCacheTTL)
	bg.userKeyMu.Unlock()
	if _, _, ok := bg.getUserKeyCached("tok"); ok {
		t.Error("expected miss on stale entry")
	}
}

// TestIsJwtTokenLike guards the cheap local JWT heuristic that decides whether
// to parse locally vs. resolve an hk- key against IAM.
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
