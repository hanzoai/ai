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
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"

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
			sufficient, _, cents := bg.checkBalance("api.hanzo.ai", "hanzo/user-"+tc.name, "hanzo", "hanzo/user-"+tc.name)
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
	sufficient, _, cents := bg.checkBalance("api.hanzo.ai", "hanzo/acct", "hanzo", "hanzo/acct")
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
	if sufficient, _, cents := bg.checkBalance("api.hanzo.ai", "hanzo/acct", "hanzo", "hanzo/acct"); sufficient || cents != 0 {
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
	sufficient, _, cents := bg.checkBalance("api.hanzo.ai", "acme", "acme", "acme/user")
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
		if sufficient, _, cents := bg.checkBalance("api.hanzo.ai", s.subject, s.ns, s.key); sufficient || cents != 0 {
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
	sufficient, _, cents := bg.checkBalance("api.hanzo.ai", "acme", "acme", "acme/user")
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
	if s, _, _ := bg.checkBalance("api.hanzo.ai", "hanzo/cacheme", "hanzo", "hanzo/cacheme"); !s {
		t.Fatal("first check should pass")
	}
	if s, _, _ := bg.checkBalance("api.hanzo.ai", "hanzo/cacheme", "hanzo", "hanzo/cacheme"); !s {
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
		"/v1/ai/version", "/v1/ai/system",
		"/v1/ai/signin", "/v1/ai/signout", "/v1/ai/account",
		// The model catalog is metadata, not metered inference: reading the
		// available-models list must never require a positive balance (gating it
		// 402s a funded-but-zero / M2M caller browsing the catalog). Still
		// authenticated — balance-exemption is not auth-exemption.
		"/v1/models", "/v1/models/gpt-4",
		// Usage/spend READS are account metadata like the model catalog — a
		// $0-balance org must be able to SEE its own usage (to learn it needs
		// credits), so the usage panel never 402s.
		"/v1/get-cloud-usages", "/v1/get-usages", "/v1/get-range-usages",
		// Public marketing aggregate (router flywheel stats) — 200 on both the anon
		// and authed path, same class as /v1/ai/traffic/.
		"/v1/ai/router/stats",
		// Feedback (the reward signal) is training metadata, not metered inference — a
		// $0-balance caller (and the internal self-probe on :8000) must still score a past
		// request.
		"/v1/ai/feedback",
		// The REST of the router-config surface — per-org policy/defaults/exports + org
		// settings — is served over the router via RouterConfigBridge (→ the ONE native ZAP
		// handler), so it DOES traverse this filter and MUST be exempt: config metadata,
		// not metered inference. A $0-balance org has to read/write its own router config
		// from the console; without these entries every unfunded org's Router → Policy tab
		// 402s. /v1/ai/org/settings is HasPrefix so /list is covered too.
		"/v1/ai/router/policy", "/v1/ai/router/defaults", "/v1/ai/router/ledger",
		"/v1/ai/router/rewards", "/v1/ai/router/artifact-meta",
		"/v1/ai/org/settings", "/v1/ai/org/settings/list",
	}
	for _, p := range exempt {
		if !isBalanceExempt(p, "GET") {
			t.Errorf("path %q should be balance-exempt", p)
		}
	}
	gated := []string{
		"/v1/chat/completions", "/v1/completions",
		"/v1/embeddings", "/v1/images/generations",
	}
	for _, p := range gated {
		if isBalanceExempt(p, "GET") {
			t.Errorf("paid path %q must NOT be balance-exempt", p)
		}
	}
}

// TestReadMethodExemption locks in the "reads never spend" rule: GET/HEAD/OPTIONS are
// read methods (balance-exempt); every mutating method stays gated.
func TestReadMethodExemption(t *testing.T) {
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		if !isReadMethod(m) {
			t.Errorf("%s must be a read method (balance-exempt)", m)
		}
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if isReadMethod(m) {
			t.Errorf("%s must NOT be read-exempt (mutating/metered paths stay gated)", m)
		}
	}
}

// TestBalanceGateFilterExemptsReads is the end-to-end regression for the "$0 org can't
// view its own resources" outage: with a configured gate and a subject that has ZERO
// spendable balance, a READ (GET) to any path — even a metered one — is admitted (no
// 402), while a metered WRITE (POST) still 402s. Proves the fix closes the read hole
// WITHOUT freeing metered inference.
func TestBalanceGateFilterExemptsReads(t *testing.T) {
	bg := newTestGate("http://unused", "", balanceCacheTTL)
	bg.setUserKeyCache("tok", "", "acme", "acme", "acme/user") // resolveBillingKey → "acme", no network
	bg.ledger.SetBalance("acme", 0)                            // known + zero spendable ⇒ insufficient

	prev := balanceGate
	balanceGate = bg
	t.Cleanup(func() { balanceGate = prev })

	status := func(method, path string) int {
		p := ask(method, path)
		p = p.with("Authorization", "Bearer tok")
		p = p.through(BalanceGateFilter)
		return p.status()
	}

	// Reads to gated paths (a metered path plus product-subsystem reads) must NOT 402
	// at $0 balance — the whole point of the fix.
	for _, p := range []string{
		"/v1/chat/completions", // metered path, but a GET of it is still a read
		"/v1/get-chats", "/v1/kms/orgs/acme/secrets",
		"/v1/s3/buckets", "/v1/ai/router/stats", "/v1/marketplace/listings",
	} {
		if code := status(http.MethodGet, p); code == http.StatusPaymentRequired {
			t.Errorf("GET %s must not be 402'd at $0 (reads never spend)", p)
		}
	}

	// A metered WRITE at $0 still gates — the fix must never free inference.
	if code := status(http.MethodPost, "/v1/chat/completions"); code != http.StatusPaymentRequired {
		t.Errorf("POST /v1/chat/completions at $0 must 402 (metered write stays gated), got %d", code)
	}
}

// TestUserKeyCacheRoundTrip verifies the token->(subject,namespace) cache stores
// and expires entries per userKeyCacheTTL.
func TestUserKeyCacheRoundTrip(t *testing.T) {
	bg := newTestGate("http://unused", "", balanceCacheTTL)
	if s, ns, uk, ok := bg.getUserKeyCached("tok", ""); ok || s != "" || ns != "" || uk != "" {
		t.Errorf("expected empty on cache miss, got (%q,%q,%q,%v)", s, ns, uk, ok)
	}
	bg.setUserKeyCache("tok", "", "hanzo/carol", "hanzo", "hanzo/carol")
	if s, ns, uk, ok := bg.getUserKeyCached("tok", ""); !ok || s != "hanzo/carol" || ns != "hanzo" || uk != "hanzo/carol" {
		t.Errorf("expected (hanzo/carol,hanzo,hanzo/carol,true) from cache, got (%q,%q,%q,%v)", s, ns, uk, ok)
	}
	// Force staleness.
	bg.userKeyMu.Lock()
	bg.userKeyCache[userKeyCacheKey("tok", "")].fetchedAt = time.Now().Add(-2 * userKeyCacheTTL)
	bg.userKeyMu.Unlock()
	if _, _, _, ok := bg.getUserKeyCached("tok", ""); ok {
		t.Error("expected miss on stale entry")
	}
}

// TestIsJwtTokenLike guards the cheap local JWT heuristic.
func TestIsJwtTokenLike(t *testing.T) {
	jwt := "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJ1c2VyIn0.signaturepart"
	if !isJwtTokenLike(jwt) {
		t.Error("expected a 3-segment JWT-like token to be recognized")
	}
	for _, notJwt := range []string{"sk-abcdef", "sk-xyz", "", "a.b", "a.b.c.d"} {
		if isJwtTokenLike(notJwt) {
			t.Errorf("token %q should not be JWT-like", notJwt)
		}
	}
}

// TestCheckBalanceDenialReason proves the funded-wallet-402 fix at its source: a
// COLD subject whose lookup ERRORS is denied as balance_unavailable (503, retry),
// NOT insufficient_balance — so a transient billing blip never tells a caller to add
// credits — while a KNOWN empty balance is denied as insufficient_balance (402, add
// credits). Both DENY (fail-CLOSED); only the reason differs.
func TestCheckBalanceDenialReason(t *testing.T) {
	// Cold subject, Commerce errors → unverifiable → 503 balance_unavailable.
	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer errSrv.Close()
	bg := newTestGate(errSrv.URL, "", balanceCacheTTL)
	sufficient, deny, _ := bg.checkBalance("api.hanzo.ai", "acme", "acme", "acme/user")
	if sufficient {
		t.Fatal("unverifiable balance must DENY (fail-CLOSED)")
	}
	if deny.Code != object.CodeBalanceUnavailable {
		t.Errorf("transient lookup failure: code=%q, want %q", deny.Code, object.CodeBalanceUnavailable)
	}
	if deny.Status != http.StatusServiceUnavailable {
		t.Errorf("transient lookup failure: status=%d, want 503 (retryable)", deny.Status)
	}
	if strings.Contains(strings.ToLower(deny.Message), "add credits") {
		t.Errorf("a funded caller behind a blip must NOT be told to add credits, got %q", deny.Message)
	}

	// Known empty balance → genuine insufficiency → 402 insufficient_balance + link.
	bg2 := newTestGate("http://unused", "", balanceCacheTTL)
	bg2.ledger.SetBalance("acme", 0)
	sufficient2, deny2, _ := bg2.checkBalance("api.hanzo.ai", "acme", "acme", "acme/user")
	if sufficient2 {
		t.Fatal("a zero known balance must DENY")
	}
	if deny2.Code != object.CodeInsufficientBalance {
		t.Errorf("empty balance: code=%q, want %q", deny2.Code, object.CodeInsufficientBalance)
	}
	if deny2.Status != http.StatusPaymentRequired {
		t.Errorf("empty balance: status=%d, want 402", deny2.Status)
	}
	if !strings.Contains(strings.ToLower(deny2.Message), "add credits") || !strings.Contains(deny2.Message, object.PayURL("api.hanzo.ai", "acme")) {
		t.Errorf("insufficient message must invite adding credits at the wallet link, got %q", deny2.Message)
	}
}

// TestBalanceGateFilterTransientReturns503 is the end-to-end regression for the
// funded-wallet-402 incident: with a configured gate and a cold subject whose
// Commerce lookup errors, a metered WRITE is denied 503 balance_unavailable (retry) —
// NOT 402 "add credits". The deny stays (fail-CLOSED); only the code/status/copy move.
func TestBalanceGateFilterTransientReturns503(t *testing.T) {
	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer errSrv.Close()

	bg := newTestGate(errSrv.URL, "", balanceCacheTTL)
	bg.setUserKeyCache("tok", "", "acme", "acme", "acme/user") // resolveBillingKey → "acme", no network
	// No ledger seed → the subject is COLD → checkBalance does a synchronous fetch,
	// which errors → balance_unavailable.

	prev := balanceGate
	balanceGate = bg
	t.Cleanup(func() { balanceGate = prev })

	p := ask(http.MethodPost, "/v1/chat/completions")
	p = p.with("Authorization", "Bearer tok")
	p = p.through(BalanceGateFilter)

	if p.status() != http.StatusServiceUnavailable {
		t.Fatalf("transient billing failure on a metered write: status=%d, want 503", p.status())
	}
	body := p.said()
	if !strings.Contains(body, `"code":"balance_unavailable"`) {
		t.Errorf("body must carry code balance_unavailable, got %s", body)
	}
	if !strings.Contains(body, `"type":"billing_error"`) {
		t.Errorf("wire type must remain billing_error (shape unchanged), got %s", body)
	}
	if strings.Contains(strings.ToLower(body), "add credits") {
		t.Errorf("transient denial must not say add credits, got %s", body)
	}
}

// A GUEST WITH NO WALLET REACHES THE ROUTES THAT COST NOTHING.
//
// This gate runs before any controller, so the in-controller rule that a $0 model
// needs no balance never got the chance to speak: a free route was refused here
// first, and the caller a free tier exists for — a guest with no wallet at all —
// could not reach the very routes published for them. Two gates asking one question
// have to give one answer.
//
// The paywall is unchanged in the same breath: a PRICED model at $0 still 402s, and
// so does anything this cannot price, because a gate that cannot read the price must
// not assume it is free.
func TestAZeroPricedModelIsNotRefusedByTheWalletGate(t *testing.T) {
	bg := newTestGate("http://unused", "", balanceCacheTTL)
	bg.setUserKeyCache("tok", "", "acme", "acme", "acme/user")
	bg.ledger.SetBalance("acme", 0) // no wallet at all

	prev := balanceGate
	balanceGate = bg
	t.Cleanup(func() { balanceGate = prev })

	post := func(body string) int {
		p := ask(http.MethodPost, "/v1/chat/completions").body([]byte(body))
		p = p.with("Authorization", "Bearer tok")
		// The SAME call the router makes at the top of ServeHTTP, above the filter
		// chain — so this proves the body is actually there when the gate looks,
		// rather than assuming it by setting the field by hand.
		p = p.through(BalanceGateFilter)
		return p.status()
	}

	if code := post(`{"model":"enso-free","messages":[]}`); code == http.StatusPaymentRequired {
		t.Error("a route priced at zero was 402'd at $0 balance — the free tier cannot reach the free routes")
	}
	if code := post(`{"model":"gpt-4o","messages":[]}`); code != http.StatusPaymentRequired {
		t.Errorf("a PRICED model at $0 returned %d, want 402 — the paywall must hold", code)
	}
	if code := post(`{"model":"no-such-model-anywhere","messages":[]}`); code != http.StatusPaymentRequired {
		t.Errorf("an unpriceable model returned %d, want 402 — a gate that cannot read a price must not assume free", code)
	}
	if code := post(`not json`); code != http.StatusPaymentRequired {
		t.Errorf("an unreadable body returned %d, want 402 — fail closed", code)
	}
}

// The gate reads a decision body's model once its sender is known, so it is where a
// compressed decision body is decoded: once, bounded, and left plain on the request
// with no coding, so the handler after it reads JSON and decodes nothing. One that
// inflates past the bound is refused here as request_too_long in the path's words,
// and a coding nobody decodes as 415; neither reaches the handler.
func TestTheGateDecodesADecisionBodyOnce(t *testing.T) {
	bg := newTestGate("http://unused", "", balanceCacheTTL)
	bg.setUserKeyCache("tok", "", "acme", "acme", "acme/user")
	bg.ledger.SetBalance("acme", 100)
	prev := balanceGate
	balanceGate = bg
	t.Cleanup(func() { balanceGate = prev })

	gz := func(s string) []byte {
		var b bytes.Buffer
		w, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
		_, _ = w.Write([]byte(s))
		_ = w.Close()
		return b.Bytes()
	}
	var coding, raw string
	var reached int
	handler := func(c *zip.Ctx) error {
		reached++
		coding, raw = c.Header("Content-Encoding"), string(c.Fiber().Request().Body())
		return c.Continue()
	}
	const plain = `{"model":"kai","state":"x","questions":{"q":{"type":"noul"}}}`
	p := ask(http.MethodPost, "/v1/decisions").body(gz(plain)).
		with("Content-Encoding", "gzip").with("Authorization", "Bearer tok").
		through(Dialect, BalanceGateFilter, handler)
	if p.status() != http.StatusOK || reached != 1 || coding != "" || raw != plain {
		t.Fatalf("a gzip decision => %d %s; the handler saw coding %q and %q", p.status(), p.said(), coding, raw)
	}

	over := gz(`{"model":"kai","state":"` + strings.Repeat("x", 16<<20) + `","questions":{"q":{"type":"noul"}}}`)
	p = ask(http.MethodPost, "/v1/systemone").body(over).
		with("Content-Encoding", "gzip").with("Authorization", "Bearer tok").
		through(Dialect, BalanceGateFilter, handler)
	if p.status() != 422 || !strings.Contains(p.said(), `"type":"request_too_long"`) || p.replied("X-Request-Id") == "" {
		t.Fatalf("gzip past the bound => %d %s", p.status(), p.said())
	}
	p = ask(http.MethodPost, "/v1/decisions").body([]byte(plain)).
		with("Content-Encoding", "compress").with("Authorization", "Bearer tok").
		through(Dialect, BalanceGateFilter, handler)
	if p.status() != http.StatusUnsupportedMediaType || !strings.Contains(p.said(), `"code":415`) {
		t.Fatalf("Content-Encoding compress => %d %s", p.status(), p.said())
	}
	if reached != 1 {
		t.Fatalf("a refused body reached the handler %d time(s)", reached-1)
	}
}

// A wallet the ledger already holds empty is refused on a decision path before its
// body is decoded, when no decision model is free to it: 64 MiB of gzip costs what
// its wire bytes cost, and the answer is the gate's 402 in the path's words, never
// the handler. When a decision model is free the body is decoded, because only the
// model it names says whether the call is one the wallet need not pay for.
func TestAnEmptyWalletIsRefusedBeforeItsDecisionBodyIsDecoded(t *testing.T) {
	bg := newTestGate("http://unused", "", balanceCacheTTL)
	bg.setUserKeyCache("tok", "", "acme", "acme", "acme/user")
	bg.ledger.SetBalance("acme", 0)
	prev, prevFree := balanceGate, decisionFree
	free := false
	balanceGate, decisionFree = bg, func(string) bool { return free }
	t.Cleanup(func() { balanceGate, decisionFree = prev, prevFree })

	var zb bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&zb, gzip.BestCompression)
	_, _ = zw.Write([]byte(`{"model":"kai","state":1,` + strings.Repeat(" ", 64<<20) + `"questions":{}}`))
	_ = zw.Close()
	var reached int
	handler := func(c *zip.Ctx) error { reached++; return c.Continue() }
	send := func(path string) probe {
		return ask(http.MethodPost, path).body(zb.Bytes()).
			with("Content-Encoding", "gzip").with("Authorization", "Bearer tok").
			through(Dialect, BalanceGateFilter, handler)
	}
	for _, path := range []string{"/v1/decisions", "/v1/systemone"} {
		var a, b runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&a)
		p := send(path)
		runtime.ReadMemStats(&b)
		if p.status() != http.StatusPaymentRequired || p.replied("X-Request-Id") == "" {
			t.Fatalf("%s: an empty wallet's gzip decision => %d %s", path, p.status(), p.said())
		}
		if alloc := b.TotalAlloc - a.TotalAlloc; alloc > 8<<20 {
			t.Fatalf("%s: refusing an empty wallet's %d KiB of gzip allocated %d MiB: it was decoded", path, zb.Len()>>10, alloc>>20)
		}
	}

	free = true
	if p := send("/v1/decisions"); p.status() != http.StatusUnprocessableEntity || !strings.Contains(p.said(), `"code":"request_too_long"`) {
		t.Fatalf("with a free decision model the body was not decoded: %d %s", p.status(), p.said())
	}
	if reached != 0 {
		t.Fatalf("a refused decision reached the handler %d time(s)", reached)
	}
}
