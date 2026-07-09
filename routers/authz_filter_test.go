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
	"sync"
	"testing"

	"github.com/beego/beego"
	beegoctx "github.com/beego/beego/context"
	"github.com/beego/beego/session"
	"github.com/hanzoai/ai/controllers"
	iam "github.com/hanzoai/iam"
)

// fakeSession is a minimal in-memory session.Store for filter tests (the real
// session manager is not wired in unit tests, so CruSession would be nil).
type fakeSession struct{ data map[interface{}]interface{} }

func (s *fakeSession) Set(k, v interface{}) error         { s.data[k] = v; return nil }
func (s *fakeSession) Get(k interface{}) interface{}      { return s.data[k] }
func (s *fakeSession) Delete(k interface{}) error         { delete(s.data, k); return nil }
func (s *fakeSession) SessionID() string                  { return "test" }
func (s *fakeSession) SessionRelease(http.ResponseWriter) {}
func (s *fakeSession) Flush() error {
	s.data = map[interface{}]interface{}{}
	return nil
}

// newFilterCtx builds a beego context with a session optionally carrying user as
// the authenticated principal.
func newFilterCtx(method, path string, user *iam.User) (*beegoctx.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	ctx := beegoctx.NewContext()
	ctx.Reset(rec, req)
	sess := &fakeSession{data: map[interface{}]interface{}{}}
	if user != nil {
		sess.data["user"] = iam.Claims{User: *user}
	}
	ctx.Input.CruSession = sess
	return ctx, rec
}

// TestRequiresGlobalAdminClassification locks in which endpoints are platform
// (super-admin) sensitive and which stay public — crucially get-models stays out.
func TestRequiresGlobalAdminClassification(t *testing.T) {
	sensitive := []string{
		"get-providers", "get-provider", "get-global-providers",
		"add-provider", "update-provider", "delete-provider",
		"get-storage-providers", "get-model-routes", "admin/reload-model-config", "admin/refresh-model-pricing",
		"get-nodes", "get-pods", "get-k8s-status",
	}
	for _, e := range sensitive {
		if !requiresSuperAdmin(e) {
			t.Errorf("%q must require super admin", e)
		}
	}
	public := []string{"models", "get-models", "get-account", "get-chats", "get-messages", "chat/completions"}
	for _, e := range public {
		if requiresSuperAdmin(e) {
			t.Errorf("%q must NOT require super admin (public/benign)", e)
		}
	}
}

// TestSensitiveUnauthorized401 — an unauthenticated get-provider is 401 (not the
// old preview-mode default-open 200 disclosure).
func TestSensitiveUnauthorized401(t *testing.T) {
	for _, path := range []string{"/v1/get-provider", "/v1/get-providers"} {
		ctx, rec := newFilterCtx("GET", path, nil)
		permissionFilter(ctx)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("unauth %s = %d, want 401", path, rec.Code)
		}
	}
}

// TestSensitiveOrgAdminForbidden403 — an ORG admin (not global) is 403 on
// provider config.
func TestSensitiveOrgAdminForbidden403(t *testing.T) {
	orgAdmin := &iam.User{Owner: "maxpower", Name: "dave", IsAdmin: true}
	ctx, rec := newFilterCtx("GET", "/v1/get-providers", orgAdmin)
	permissionFilter(ctx)
	if rec.Code != http.StatusForbidden {
		t.Errorf("org-admin get-providers = %d, want 403", rec.Code)
	}
}

// TestSensitiveGlobalAdminAllowed — a super admin passes the gate untouched.
func TestSensitiveGlobalAdminAllowed(t *testing.T) {
	globalAdmin := &iam.User{Owner: "admin", Name: "admin", IsAdmin: true}
	ctx, rec := newFilterCtx("GET", "/v1/get-providers", globalAdmin)
	permissionFilter(ctx)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("super-admin get-providers wrote a denial (code=%d, body=%q); want pass-through", rec.Code, rec.Body.String())
	}
}

// TestDenyRequestForbidden403 — explicit denials are 403, not 200 (#9).
func TestDenyRequestForbidden403(t *testing.T) {
	ctx, rec := newFilterCtx("POST", "/v1/some-op", nil)
	controllers.DenyRequest(ctx)
	if rec.Code != http.StatusForbidden {
		t.Errorf("DenyRequest = %d, want 403", rec.Code)
	}
}

// newFilterCtxAuth builds a filter context with a Bearer header (no session), so
// the credential-presence gate can be exercised.
func newFilterCtxAuth(method, path, bearer string) (*beegoctx.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	ctx := beegoctx.NewContext()
	ctx.Reset(rec, req)
	sess := &fakeSession{data: map[interface{}]interface{}{}}
	ctx.Input.CruSession = sess
	return ctx, rec
}

// TestWriteEndpointsRequireCredential is the F1 defense-in-depth assertion: the
// central filter fails closed (401) for a FULLY ANONYMOUS request to the
// write / ingest / scrape / RAG endpoints that previously fell through it with no
// central gate. This is the second layer behind requireIndexAuth.
func TestWriteEndpointsRequireCredential(t *testing.T) {
	cases := []struct{ method, path string }{
		{"POST", "/v1/rag/embed"},
		{"POST", "/v1/rag/query"},
		{"POST", "/v1/rag/delete"},
		{"GET", "/v1/rag/context"},
		{"POST", "/v1/scrape"},
		{"POST", "/v1/scrape/preview"},
		{"POST", "/v1/index"},
		{"POST", "/v1/search"},
		{"GET", "/v1/search/stats"},
		{"POST", "/v1/docs/ingest"},
		{"POST", "/v1/embed"},
		{"POST", "/v1/query"},
		{"POST", "/v1/query_multiple"},
		{"DELETE", "/v1/documents"},
		{"GET", "/v1/documents/file-123/context"},
	}
	for _, tc := range cases {
		ctx, rec := newFilterCtx(tc.method, tc.path, nil) // no user, no bearer
		permissionFilter(ctx)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("anonymous %s %s = %d, want 401 (fail closed)", tc.method, tc.path, rec.Code)
		}
	}
}

// TestWriteEndpointWithCredentialPassesFilter — when SOME credential is present
// the filter passes through (the controller does the authoritative validation);
// the coarse presence gate must never reject a credential-bearing request.
func TestWriteEndpointWithCredentialPassesFilter(t *testing.T) {
	// Bearer present (validity is the controller's job).
	ctx, rec := newFilterCtxAuth("POST", "/v1/rag/embed", "hk-some-key")
	permissionFilter(ctx)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("credentialed /v1/rag/embed wrote a denial (code=%d, body=%q); want filter pass-through", rec.Code, rec.Body.String())
	}

	// Session present, no Bearer — also passes.
	sessionUser := &iam.User{Owner: "acme", Name: "bob"}
	ctx2, rec2 := newFilterCtx("POST", "/v1/scrape", sessionUser)
	permissionFilter(ctx2)
	if rec2.Code != http.StatusOK || rec2.Body.Len() != 0 {
		t.Errorf("session-auth /v1/scrape wrote a denial (code=%d, body=%q); want filter pass-through", rec2.Code, rec2.Body.String())
	}
}

// TestBenignPathsNotCredentialGated — intentionally-anonymous or
// gateway-header-authed paths (health, metrics, memory, wecom) must NOT be
// caught by the write-endpoint gate. The gate is an explicit set, not a blanket
// default.
func TestBenignPathsNotCredentialGated(t *testing.T) {
	for _, p := range []struct{ method, path string }{
		{"GET", "/v1/health"},
		{"GET", "/v1/metrics"},
		{"GET", "/v1/memory/search"},
		{"POST", "/v1/wecom-bot/callback/bot1"},
	} {
		ctx, rec := newFilterCtx(p.method, p.path, nil)
		permissionFilter(ctx)
		if rec.Code == http.StatusUnauthorized {
			t.Errorf("benign %s %s = 401; must not be credential-gated", p.method, p.path)
		}
	}
}

// TestRequiresPresentCredentialClassification locks exactly which endpoints are
// credential-gated — the write/ingest/scrape/RAG set — and which are not
// (chat/models/memory/health and the unrelated query-record family, which must
// NOT be caught by the "query" entry).
func TestRequiresPresentCredentialClassification(t *testing.T) {
	gated := []string{
		"scrape", "scrape/preview", "index", "search", "search/stats",
		"docs/ingest", "embed", "query", "query_multiple", "documents",
		"rag/embed", "rag/query", "rag/query-multiple", "rag/delete", "rag/context",
		"documents/file-123/context",
	}
	for _, e := range gated {
		if !requiresPresentCredential(e) {
			t.Errorf("%q must require a present credential", e)
		}
	}
	ungated := []string{
		"chat/completions", "completions", "models", "messages",
		"memory/search", "memory/remember", "health", "metrics",
		"get-account", "query-record", "query-record-second",
	}
	for _, e := range ungated {
		if requiresPresentCredential(e) {
			t.Errorf("%q must NOT require a present credential (benign/self-authing/unrelated)", e)
		}
	}
}

// ── C-1: path-normalization bypass regression ───────────────────────────────
//
// Beego path.Cleans the request path before dispatch, so slash/dot variants of a
// gated multi-segment route (admin/providers*) still reach the controller. The
// gate must key on the SAME normalized name, or every variant falls through to the
// fully-open default — an UNAUTHENTICATED takeover of provider routing. These tests
// FAIL against the pre-fix filter (which keyed on the raw TrimPrefix path) and pass
// after. httptest.NewRequest preserves the raw "//", "/./", "/../", trailing-slash
// forms in URL.Path (verified), so the filter sees exactly what a real server
// delivers to a BeforeRouter filter.

// c1BypassVariants are the request paths that Beego dispatches to a gated admin
// controller but that the un-normalized gate missed. Canonical form is first.
var c1BypassVariants = []struct{ method, path, note string }{
	{"GET", "/v1/admin/providers", "canonical read"},
	{"GET", "/v1/admin/providers/", "trailing slash"},
	{"GET", "/v1//admin/providers", "double slash after v1"},
	{"GET", "/v1/admin//providers", "double slash mid-path"},
	{"GET", "/v1/admin/providers//", "trailing double slash"},
	{"GET", "/v1/./admin/providers", "dot segment"},
	{"GET", "/v1/admin/../admin/providers", "dotdot segment"},
	{"POST", "/v1/admin/providers/toggle", "canonical toggle"},
	{"POST", "/v1/admin/providers/toggle/", "toggle trailing slash"},
	{"POST", "/v1//admin/providers/toggle", "toggle double leading slash"},
	{"POST", "/v1/admin/providers/primary", "canonical primary"},
	{"POST", "/v1/admin/providers/primary/", "primary trailing slash"},
}

// TestNormalizedControllerName_CollapsesVariants asserts the helper maps every
// dispatched variant to the canonical controllerName the gate map is keyed on.
func TestNormalizedControllerName_CollapsesVariants(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"/v1/admin/providers", "admin/providers"},
		{"/v1/admin/providers/", "admin/providers"},
		{"/v1//admin/providers", "admin/providers"},
		{"/v1/admin//providers", "admin/providers"},
		{"/v1/admin/providers//", "admin/providers"},
		{"/v1/./admin/providers", "admin/providers"},
		{"/v1/admin/../admin/providers", "admin/providers"},
		{"/v1/admin/providers/toggle/", "admin/providers/toggle"},
		{"/v1/admin/providers/primary/", "admin/providers/primary"},
		{"/v1/get-providers/", "get-providers"},
		{"/v1//get-providers", "get-providers"},
	}
	for _, c := range cases {
		got, ok := normalizedControllerName(c.raw)
		if !ok || got != c.want {
			t.Errorf("normalizedControllerName(%q) = (%q, %v), want (%q, true)", c.raw, got, ok, c.want)
		}
		// The normalized name MUST hit the gate for these gated endpoints.
		if !requiresSuperAdmin(got) {
			t.Errorf("normalized %q -> %q does NOT require super admin (gate bypass!)", c.raw, got)
		}
	}
	// Non-/v1 paths are correctly reported as pass-through.
	if _, ok := normalizedControllerName("/healthz"); ok {
		t.Error("/healthz must be reported non-/v1 (ok=false)")
	}
}

// TestAdminRoutesUnauthenticated401_AllVariants is the core C-1 regression: an
// UNAUTHENTICATED caller hitting ANY slash/dot variant of the admin routes must be
// denied (401), never fall through to 200. Pre-fix, the variants returned the
// controller (or its open default); post-fix they all 401.
func TestAdminRoutesUnauthenticated401_AllVariants(t *testing.T) {
	for _, v := range c1BypassVariants {
		ctx, rec := newFilterCtx(v.method, v.path, nil)
		permissionFilter(ctx)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("unauth %s %s (%s) = %d, want 401 (BYPASS if 200)", v.method, v.path, v.note, rec.Code)
		}
	}
}

// TestAdminRoutesOrgAdminForbidden403_AllVariants: a non-global (org) admin must be
// 403 on every variant — the gate normalizes, THEN checks super admin. This proves
// the variant can't be used to downgrade from the super-admin requirement either.
func TestAdminRoutesOrgAdminForbidden403_AllVariants(t *testing.T) {
	orgAdmin := &iam.User{Owner: "maxpower", Name: "dave", IsAdmin: true}
	for _, v := range c1BypassVariants {
		ctx, rec := newFilterCtx(v.method, v.path, orgAdmin)
		permissionFilter(ctx)
		if rec.Code != http.StatusForbidden {
			t.Errorf("org-admin %s %s (%s) = %d, want 403", v.method, v.path, v.note, rec.Code)
		}
	}
}

// TestAdminRoutesGlobalAdminPass_Canonical: the fix must NOT over-block — a global
// admin still passes the gate untouched on the canonical routes.
func TestAdminRoutesGlobalAdminPass_Canonical(t *testing.T) {
	globalAdmin := &iam.User{Owner: "admin", Name: "admin", IsAdmin: true}
	for _, p := range []struct{ method, path string }{
		{"GET", "/v1/admin/providers"},
		{"POST", "/v1/admin/providers/toggle"},
		{"POST", "/v1/admin/providers/primary"},
	} {
		ctx, rec := newFilterCtx(p.method, p.path, globalAdmin)
		permissionFilter(ctx)
		if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
			t.Errorf("super-admin %s %s wrote a denial (code=%d, body=%q); want pass-through", p.method, p.path, rec.Code, rec.Body.String())
		}
	}
}

// TestPublicProviderFlagsNotGated: /v1/provider-flags is the public secret-free
// enabled-name feed — it must NOT require super admin (the pricing sync reads it
// unauthenticated). Guards against accidentally gating the public feed.
func TestPublicProviderFlagsNotGated(t *testing.T) {
	for _, raw := range []string{"/v1/provider-flags", "/v1/provider-flags/"} {
		name, ok := normalizedControllerName(raw)
		if !ok {
			t.Fatalf("%s should be a /v1 path", raw)
		}
		if requiresSuperAdmin(name) {
			t.Errorf("%q (%q) must NOT require super admin — it is the public feed", raw, name)
		}
	}
}

// roundTripOnce wires the real router + filter + a memory session manager exactly
// once (Beego router/session state is process-global; re-registering leaks).
var roundTripOnce sync.Once

func setupRoundTripRouter(t *testing.T) {
	t.Helper()
	roundTripOnce.Do(func() {
		beego.BConfig.RunMode = "prod"
		// Match prod (bootstrap.go): sessions ON with the in-memory provider, so an
		// unauthenticated request yields a nil session user cleanly (Session("user")
		// == nil) instead of panicking on a nil CruSession. This is what lets the
		// gate return a real 401 end-to-end.
		beego.BConfig.WebConfig.Session.SessionOn = true
		beego.BConfig.WebConfig.Session.SessionName = "cloud_session_id"
		beego.BConfig.WebConfig.Session.SessionProvider = "memory"
		beego.BConfig.WebConfig.Session.SessionGCMaxLifetime = 3600
		if beego.GlobalSessions == nil {
			mgr, err := session.NewManager("memory", &session.ManagerConfig{
				CookieName:      "cloud_session_id",
				EnableSetCookie: true,
				Gclifetime:      3600,
			})
			if err != nil {
				t.Fatalf("session.NewManager: %v", err)
			}
			beego.GlobalSessions = mgr
			go mgr.GC()
		}
		// Register the REAL admin routes + the REAL filter — production wiring, not a stub.
		beego.InsertFilter("*", beego.BeforeRouter, AuthzFilter)
		beego.Router("/v1/admin/providers", &controllers.ApiController{}, "GET:GetAdminProviders")
		beego.Router("/v1/admin/providers/toggle", &controllers.ApiController{}, "POST:ToggleAdminProvider")
		beego.Router("/v1/admin/providers/primary", &controllers.ApiController{}, "POST:SetPrimaryAdminProvider")
	})
}

// TestRouterRoundTrip_BypassBlocked drives requests through the REAL Beego router +
// the REAL AuthzFilter (BeforeRouter) + a real memory session — exactly as red's
// harness did — asserting every slash/dot variant of the admin routes is blocked
// (401) end-to-end and NEVER reaches the controller (which would 200 or, unseeded,
// otherwise run). This is the router-level proof (M-2) that the filter and router
// agree post-normalization. It FAILS against the pre-fix filter (variants returned
// the controller / open default) and passes after.
func TestRouterRoundTrip_BypassBlocked(t *testing.T) {
	setupRoundTripRouter(t)

	for _, v := range []struct{ method, path, note string }{
		{"GET", "/v1/admin/providers", "canonical read"},
		{"GET", "/v1/admin/providers/", "trailing slash"},
		{"GET", "/v1//admin/providers", "double leading slash"},
		{"GET", "/v1/./admin/providers", "dot segment"},
		{"GET", "/v1/admin/../admin/providers", "dotdot segment"},
		{"POST", "/v1/admin/providers/toggle", "canonical toggle"},
		{"POST", "/v1/admin/providers/toggle/", "toggle trailing slash"},
		{"POST", "/v1//admin/providers/toggle", "toggle double leading slash"},
		{"POST", "/v1/admin/providers/primary/", "primary trailing slash"},
	} {
		req := httptest.NewRequest(v.method, "http://api.hanzo.ai"+v.path, nil)
		rec := httptest.NewRecorder()
		beego.BeeApp.Handlers.ServeHTTP(rec, req)
		// Unauthenticated → the gate denies before dispatch (401). Must NOT be 200
		// (bypass reaching the controller). 401 or 403 are both acceptable denials.
		if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
			t.Errorf("router %s %-34s (%s) = %d, want 401/403 (BYPASS if not a denial)", v.method, v.path, v.note, rec.Code)
		}
	}
}
