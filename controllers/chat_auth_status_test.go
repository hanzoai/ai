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

package controllers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	beecontext "github.com/beego/beego/context"
	"github.com/hanzoai/ai/object"
	iam "github.com/hanzoai/iam"
)

// This suite pins the money-path auth boundary for POST /v1/chat/completions.
//
// The bug it locks out: on prod cloud v1.786.223 (ai v1.805.9) an UNATTRIBUTED
// request — no principal resolved: no credential, a bad/expired token, or a probe
// — returned a fast pre-model 500 instead of a clean 401/400. A 500 tells a caller
// (and every uptime probe) the SERVER is broken when the real story is "you sent
// no valid credential". The contract enforced here:
//
//	no / empty / unknown / malformed credential  -> 401  (never 500)
//	valid credential, malformed body             -> 400
//	authed, unfunded (prepaid gate)              -> 402
//	authed, funded                               -> proceeds past the gate
//	genuine internal fault (balance backend down)-> 500  (real faults still 500)
//
// The last two lines are the guard-rail the fix must NOT cross: it must not weaken
// the prepaid gate, and it must not swallow a real server fault into a silent 401.

const (
	chatWidgetKey = "hz_chat_auth_probe"
	chatFundedKey = "sk-chat-auth-funded"
	chatModel     = "test-model-x" // deliberately NOT in any route table
	chatOrg       = "acme"
)

var chatAuthSeq int64

// balReader builds a BalanceReaderFunc that always returns the given cents/err —
// the native seam the gate reads, injected so the test needs no Commerce.
func balReader(cents int64, err error) object.BalanceReaderFunc {
	return func(_ context.Context, _, _, _ string) (int64, error) { return cents, err }
}

// newChatController wires an ApiController to a recorder with a NON-nil beego
// session (prod runs SessionOn=true, so CruSession is normally populated) plus the
// given Authorization header and body — the faithful shape of a gateway-forwarded
// chat request.
func newChatController(authHeader, body string) (*ApiController, *httptest.ResponseRecorder) {
	return newChatControllerSession(authHeader, body, true)
}

// newChatControllerSession is newChatController with an explicit knob for whether
// the beego session store (CruSession) is populated. withSession=false reproduces
// the embedded-cloud failure mode the bootstrap comment describes: beego's
// SessionStart hook did not run, so CruSession is nil and any session read
// dereferences nil — the pre-model panic beego renders as a 500.
func newChatControllerSession(authHeader, body string, withSession bool) (*ApiController, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ctx := beecontext.NewContext()
	ctx.Reset(rec, req)
	ctx.Input.RequestBody = []byte(body)
	if withSession {
		ctx.Input.CruSession = &ctrlFakeSession{data: map[interface{}]interface{}{}}
	}
	c := &ApiController{}
	c.Init(ctx, "ApiController", "X", nil)
	return c, rec
}

// driveChat runs the real ChatCompletions handler and returns its HTTP status. A
// panic (which beego's recover renders as 500) is converted to 500 so a regression
// that reintroduces the pre-model panic fails LOUD as "got 500, want 401" rather
// than crashing the suite.
func driveChat(t *testing.T, authHeader, body string) int {
	return driveChatSession(t, authHeader, body, true)
}

func driveChatSession(t *testing.T, authHeader, body string, withSession bool) int {
	t.Helper()
	c, rec := newChatControllerSession(authHeader, body, withSession)
	func() {
		defer func() {
			if r := recover(); r != nil {
				rec.Code = 500
				t.Logf("HANDLER PANIC (rendered 500): %v", r)
			}
		}()
		c.ChatCompletions()
	}()
	if rec.Code == 0 {
		return http.StatusOK // beego default when no explicit status was set
	}
	return rec.Code
}

// setupChatMoneyPath installs the prod-faithful dependencies the money path needs:
// a non-nil iam client (so an invalid JWT is an ERROR, not a nil-deref), an
// in-memory provider store (so the sk- branch does a REAL lookup), one funded,
// attributable provider-key principal, and a valid widget key. It restores all
// global seams on cleanup.
func setupChatMoneyPath(t *testing.T) {
	t.Helper()
	// Non-nil globalClient: any invalid/expired JWT verifies to an error (→401),
	// never a nil-pointer panic. Mirrors prod, which always has an iam client.
	iam.InitConfig("", "", "", "", "", "")

	n := atomic.AddInt64(&chatAuthSeq, 1)
	dsn := fmt.Sprintf("file:chatauth_%d?mode=memory&cache=shared", n)
	restore, err := object.UseMemoryDB(dsn, &object.Provider{})
	if err != nil {
		t.Fatalf("UseMemoryDB: %v", err)
	}
	t.Cleanup(restore)

	if _, err := object.AddProvider(&object.Provider{
		Owner: chatOrg, Name: "acme-openai", Category: "Model", Type: "OpenAI",
		ProviderKey: chatFundedKey, ProviderUrl: "http://127.0.0.1:1/v1",
		ClientSecret: "upstream-secret", State: "Active",
	}); err != nil {
		t.Fatalf("seed provider: %v", err)
	}

	os.Setenv("WIDGET_KEYS", chatWidgetKey)
	t.Cleanup(func() {
		os.Unsetenv("WIDGET_KEYS")
		object.SetBalanceReader(nil)
	})
}

// TestChatCompletionsUnattributedNever500 is the headline regression: every
// no-valid-principal request resolves to a typed client status (401/400) and NEVER
// a 500. Each row is a distinct real-world "unattributed" shape from the prod logs.
func TestChatCompletionsUnattributedNever500(t *testing.T) {
	setupChatMoneyPath(t)

	const goodBody = `{"model":"` + chatModel + `","messages":[{"role":"user","content":"hi"}]}`

	cases := []struct {
		name string
		auth string
		body string
		want int
	}{
		{"no-credential", "", goodBody, http.StatusUnauthorized},
		{"empty-bearer", "Bearer ", goodBody, http.StatusUnauthorized},
		{"unknown-provider-key", "Bearer sk-totally-unknown-key", goodBody, http.StatusUnauthorized},
		{"garbage-opaque-token", "Bearer zzzzzzzzzzzzzzzzzzzz", goodBody, http.StatusUnauthorized},
		{"malformed-jwt", "Bearer aaaaaaaaaaaaa.bbbbbbbbbbbbb.cccccccccccc", goodBody, http.StatusUnauthorized},
		{"expired-jwt", "Bearer eyJhbGciOiJSUzI1NiJ9.eyJpc3MiOiJodHRwczovL2hhbnpvLmlkIiwiZXhwIjoxMDAwfQ.badsig", goodBody, http.StatusUnauthorized},
		{"unknown-iam-key", "Bearer hk-nonexistent-000000", goodBody, http.StatusUnauthorized},
		{"malformed-body-no-credential", "Bearer ", `{not json`, http.StatusUnauthorized},
		{"malformed-body-valid-widget", "Bearer " + chatWidgetKey, `{not json`, http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := driveChat(t, tc.auth, tc.body)
			if got == http.StatusInternalServerError {
				t.Fatalf("%s => 500 (the regression: an unattributed request must never be a server error)", tc.name)
			}
			if got != tc.want {
				t.Fatalf("%s => %d, want %d", tc.name, got, tc.want)
			}
		})
	}
}

// TestChatCompletionsNilSessionNever500 reproduces the exact prod failure mode:
// the embedded cloud binary serves these routes without beego having populated the
// session store, so CruSession is nil. GetOrg/routingUserId read the session
// BEFORE auth resolution (pre-model), so a nil store used to panic → a fast 500 for
// EVERY request, unattributed ones included. The contract still holds with no
// session: no/invalid credential → 401, valid-widget + malformed body → 400. Never
// 500. (A real internal fault is covered separately and still 500s.)
func TestChatCompletionsNilSessionNever500(t *testing.T) {
	setupChatMoneyPath(t)

	const goodBody = `{"model":"` + chatModel + `","messages":[{"role":"user","content":"hi"}]}`
	cases := []struct {
		name string
		auth string
		body string
		want int
	}{
		{"no-credential", "", goodBody, http.StatusUnauthorized},
		{"unknown-provider-key", "Bearer sk-totally-unknown-key", goodBody, http.StatusUnauthorized},
		{"malformed-jwt", "Bearer aaaaaaaaaaaaa.bbbbbbbbbbbbb.cccccccccccc", goodBody, http.StatusUnauthorized},
		{"expired-jwt", "Bearer eyJhbGciOiJSUzI1NiJ9.eyJpc3MiOiJodHRwczovL2hhbnpvLmlkIiwiZXhwIjoxMDAwfQ.badsig", goodBody, http.StatusUnauthorized},
		{"malformed-body-valid-widget", "Bearer " + chatWidgetKey, `{not json`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := driveChatSession(t, tc.auth, tc.body, false /* nil session store */)
			if got == http.StatusInternalServerError {
				t.Fatalf("%s (nil session) => 500 (the regression: a nil session store must resolve to 'no principal', not a server error)", tc.name)
			}
			if got != tc.want {
				t.Fatalf("%s (nil session) => %d, want %d", tc.name, got, tc.want)
			}
		})
	}
}

// TestChatCompletionsPrepaidGate proves the fix leaves the prepaid gate EXACTLY as
// designed: an authed-but-unfunded principal is stopped at 402, a funded one is
// admitted past the gate, and a genuine balance-backend fault is a real 500 (never
// silently downgraded to 401/402). The vehicle is an sk- provider key — the one
// auth path that carries a billable principal without a JWT/IAM round trip.
func TestChatCompletionsPrepaidGate(t *testing.T) {
	setupChatMoneyPath(t)

	// authed + unfunded -> 402 (the no-exempt prepaid gate, unchanged).
	object.SetBalanceReader(balReader(0, nil))
	if got := driveChat(t, "Bearer "+chatFundedKey, `{"model":"`+chatModel+`","messages":[{"role":"user","content":"hi"}]}`); got != http.StatusPaymentRequired {
		t.Fatalf("authed-unfunded => %d, want 402", got)
	}

	// genuine internal fault (balance backend errors) -> 500, NOT a swallowed 401.
	object.SetBalanceReader(balReader(0, errors.New("commerce unreachable")))
	if got := driveChat(t, "Bearer "+chatFundedKey, `{"model":"`+chatModel+`","messages":[{"role":"user","content":"hi"}]}`); got != http.StatusInternalServerError {
		t.Fatalf("balance-backend fault => %d, want 500 (real faults must still 500)", got)
	}

	// authed + funded -> proceeds PAST the money gate. Empty messages make the
	// handler return at the post-gate message-validation stage WITHOUT an upstream
	// call, so the status is deterministic and provably beyond auth (401) and the
	// prepaid gate (402): reaching this stage at all requires a funded principal.
	object.SetBalanceReader(balReader(100_00, nil))
	got := driveChat(t, "Bearer "+chatFundedKey, `{"model":"`+chatModel+`","messages":[]}`)
	if got == http.StatusUnauthorized || got == http.StatusPaymentRequired || got == http.StatusBadRequest {
		t.Fatalf("authed-funded => %d, want a status past the money gate (not 401/402/400)", got)
	}
}

// TestAuthResolveProviderFundedProceeds is the crisp seam proof for "authed-funded
// proceeds": the ONE shared auth+resolution+gate policy admits a funded principal —
// no error, a resolved provider, and the billed owner attributed. This is the exact
// boundary the fast-500 bug lived at, asserted directly.
func TestAuthResolveProviderFundedProceeds(t *testing.T) {
	setupChatMoneyPath(t)
	object.SetBalanceReader(balReader(100_00, nil))

	c, _ := newChatController("Bearer "+chatFundedKey, "")
	provider, authUser, _, _, _, err := c.authResolveProvider(chatFundedKey, chatModel, chatOrg)
	if err != nil {
		t.Fatalf("funded authResolveProvider returned error %v (statusOf=%d); want nil (proceeds)", err, statusOf(err))
	}
	if provider == nil {
		t.Fatal("funded authResolveProvider returned nil provider; want the resolved model provider")
	}
	if authUser == nil || authUser.Owner != chatOrg {
		t.Fatalf("funded authResolveProvider billed owner = %+v; want owner %q", authUser, chatOrg)
	}
}

// TestChatCompletionsAutoModelAuthBeforeRouting closes the LOW that the session
// fix unmasked: an `auto`/`zen-router` request resolves the concrete model via the
// router engine (an internal HTTP call with the caller's prompt) and records a
// RoutingEvent BEFORE the provider/billing resolution authenticates. An
// UNAUTHENTICATED caller must not drive that internal machinery. Proof: with
// auto-routing ENABLED, an unauth `auto` request must 401 and leave X-Routed-Model
// UNSET (routing never ran) — auth precedes every side effect. An authed caller
// still routes normally.
func TestChatCompletionsAutoModelAuthBeforeRouting(t *testing.T) {
	setupChatMoneyPath(t)
	prev := globalModelConfig
	defer func() { globalModelConfig = prev }()
	globalModelConfig = routerTestConfig(true) // auto-routing ON: routing WOULD set the header if reached

	body := `{"model":"auto","messages":[{"role":"user","content":"please refactor this function"}]}`

	// Unauthenticated `auto`: 401 BEFORE routing — no side effect leaked.
	c, rec := newChatController("Bearer sk-unknown-probe", body)
	func() {
		defer func() {
			if r := recover(); r != nil {
				rec.Code = 500
				t.Logf("HANDLER PANIC: %v", r)
			}
		}()
		c.ChatCompletions()
	}()
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth auto => %d, want 401", rec.Code)
	}
	if h := rec.Header().Get(RoutedModelHeader); h != "" {
		t.Fatalf("unauth auto set %s=%q — the router engine ran BEFORE auth (pre-auth side effect leaked)", RoutedModelHeader, h)
	}

	// Authed `auto` (funded sk-): routing still runs and rewrites the model.
	object.SetBalanceReader(balReader(100_00, nil))
	c2, rec2 := newChatController("Bearer "+chatFundedKey, body)
	func() {
		defer func() { _ = recover() }()
		c2.ChatCompletions()
	}()
	if rec2.Code == http.StatusUnauthorized {
		t.Fatalf("authed auto => 401; the pre-auth gate must not reject a valid credential")
	}
	if h := rec2.Header().Get(RoutedModelHeader); h == "" {
		t.Fatalf("authed auto did not set %s — routing must still run for an authenticated caller", RoutedModelHeader)
	}
}

// TestAuthErrorStatusContract locks the typed-error → HTTP-status mapping that IS
// the fix: each failure category carries its own status, and — critically — an
// UNTYPED error reaching the auth-gated renderer is fail-secure 401 (deny), NEVER
// 500 or 200. This is what converts a fall-through no-principal error from a raw
// 500 into a clean 401.
func TestAuthErrorStatusContract(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"authError", authError("invalid key"), http.StatusUnauthorized},
		{"billingError", billingError("no funds"), http.StatusPaymentRequired},
		{"modelError", modelError("no such model"), http.StatusBadRequest},
		{"serverError", serverError("provider down"), http.StatusInternalServerError},
		{"untyped-fail-secure", errors.New("something fell through"), http.StatusUnauthorized},
		{"wrapAuth-untyped", wrapAuth(errors.New("opaque")), http.StatusUnauthorized},
		{"wrapAuth-preserves-500", wrapAuth(serverError("real fault")), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		if got := statusOf(tc.err); got != tc.want {
			t.Errorf("statusOf(%s) = %d, want %d", tc.name, got, tc.want)
		}
	}
}
