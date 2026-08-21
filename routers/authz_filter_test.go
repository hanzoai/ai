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

	"github.com/hanzoai/ai/controllers"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/ai/internal/authtest"
)

// asUser builds a request carrying user as the authenticated principal, by SIGNING
// a credential for them.
//
// There is no other way to be authenticated here, and that is the point: identity
// comes off a verified token, so a test presents one rather than seeding a session
// this service no longer has. A nil user is an anonymous request.
func asUser(t *testing.T, method, path string, user *iam.User) probe {
	t.Helper()
	q := ask(method, path)
	if user != nil {
		q = q.with("Authorization", authtest.Bearer(t, *user))
	}
	return q
}

// TestRequiresGlobalAdminClassification locks in which endpoints are platform
// (super-admin) sensitive and which stay public — crucially get-models stays out.
func TestRequiresGlobalAdminClassification(t *testing.T) {
	sensitive := []string{
		"get-providers", "get-provider", "get-global-providers",
		"add-provider", "update-provider", "delete-provider",
		"get-storage-providers", "get-model-routes", "admin/reload-model-config", "admin/refresh-model-pricing",
		"admin/usage/backfill-do",
		"get-nodes", "get-k8s-status",
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
		q := asUser(t, "GET", path, nil)
		q = q.through(permissionFilter)
		if q.status() != http.StatusUnauthorized {
			t.Errorf("unauth %s = %d, want 401", path, q.status())
		}
	}
}

// TestSensitiveOrgAdminForbidden403 — an ORG admin (not global) is 403 on
// provider config.
func TestSensitiveOrgAdminForbidden403(t *testing.T) {
	orgAdmin := &iam.User{Owner: "maxpower", Name: "dave", IsAdmin: true}
	q := asUser(t, "GET", "/v1/get-providers", orgAdmin)
	q = q.through(permissionFilter)
	if q.status() != http.StatusForbidden {
		t.Errorf("org-admin get-providers = %d, want 403", q.status())
	}
}

// TestSensitiveGlobalAdminAllowed — a super admin passes the gate untouched.
func TestSensitiveGlobalAdminAllowed(t *testing.T) {
	globalAdmin := &iam.User{Owner: "admin", Name: "admin", IsAdmin: true}
	q := asUser(t, "GET", "/v1/get-providers", globalAdmin)
	q = q.through(permissionFilter)
	if q.status() != http.StatusOK || len(q.said()) != 0 {
		t.Errorf("super-admin get-providers wrote a denial (code=%d, body=%q); want pass-through", q.status(), q.said())
	}
}

// TestDenyRequestForbidden403 — explicit denials are 403, not 200 (#9).
func TestDenyRequestForbidden403(t *testing.T) {
	q := asUser(t, "POST", "/v1/some-op", nil)
	controllers.DenyRequest(q.Ctx)
	if q.status() != http.StatusForbidden {
		t.Errorf("DenyRequest = %d, want 403", q.status())
	}
}

// withKey builds a request presenting an opaque key, for the gate that asks only
// whether a credential is PRESENT and never has to verify it.
func withKey(method, path, key string) probe {
	q := ask(method, path)
	if key != "" {
		q = q.with("Authorization", "Bearer "+key)
	}
	return q
}

// TestWriteEndpointsRequireCredential is the F1 defense-in-depth assertion: the
// central filter fails closed (401) for a FULLY ANONYMOUS request to the
// write / ingest / scrape / RAG endpoints that previously fell through it with no
// central gate. This is the second layer behind requireIndexAuth.
func TestWriteEndpointsRequireCredential(t *testing.T) {
	cases := []struct{ method, path string }{
		{"POST", "/v1/ai/rag/ingest"},
		{"POST", "/v1/ai/rag/embed"},
		{"POST", "/v1/ai/rag/query"},
		{"POST", "/v1/ai/rag/delete"},
		{"GET", "/v1/ai/rag/context"},
		{"POST", "/v1/index"},
		{"POST", "/v1/search"},
		{"GET", "/v1/search/stats"},
		{"POST", "/v1/embed"},
	}
	for _, tc := range cases {
		q := asUser(t, tc.method, tc.path, nil) // no user, no bearer
		q = q.through(permissionFilter)
		if q.status() != http.StatusUnauthorized {
			t.Errorf("anonymous %s %s = %d, want 401 (fail closed)", tc.method, tc.path, q.status())
		}
	}
}

// TestWriteEndpointWithCredentialPassesFilter — when SOME credential is present
// the filter passes through (the controller does the authoritative validation);
// the coarse presence gate must never reject a credential-bearing request.
func TestWriteEndpointWithCredentialPassesFilter(t *testing.T) {
	// Bearer present (validity is the controller's job).
	q := withKey("POST", "/v1/ai/rag/embed", "sk-some-key")
	q = q.through(permissionFilter)
	if q.status() != http.StatusOK || len(q.said()) != 0 {
		t.Errorf("credentialed /v1/ai/rag/embed wrote a denial (code=%d, body=%q); want filter pass-through", q.status(), q.said())
	}

	// Session present, no Bearer — also passes.
	sessionUser := &iam.User{Owner: "acme", Name: "bob"}
	q2 := asUser(t, "POST", "/v1/scrape", sessionUser)
	q2 = q2.through(permissionFilter)
	if q2.status() != http.StatusOK || len(q2.said()) != 0 {
		t.Errorf("session-auth /v1/scrape wrote a denial (code=%d, body=%q); want filter pass-through", q2.status(), q2.said())
	}
}

// TestBenignPathsNotCredentialGated — intentionally-anonymous paths (health,
// metrics) must NOT be caught by the write-endpoint gate. The gate is an
// explicit set, not a blanket default.
//
// Memory is deliberately absent: it is per-person, so there is no anonymous
// reading of it — see TestMemoryIsNeverAnonymous.
func TestBenignPathsNotCredentialGated(t *testing.T) {
	for _, p := range []struct{ method, path string }{
		{"GET", "/v1/health"},
		{"GET", "/v1/metrics"},
	} {
		q := asUser(t, p.method, p.path, nil)
		q = q.through(permissionFilter)
		if q.status() == http.StatusUnauthorized {
			t.Errorf("benign %s %s = 401; must not be credential-gated", p.method, p.path)
		}
	}
}

// TestRequiresPresentCredentialClassification locks exactly which endpoints are
// credential-gated — the write/ingest/scrape/RAG/memory set — and which are not
// (chat/models/health and the unrelated query-record family).
func TestRequiresPresentCredentialClassification(t *testing.T) {
	gated := []string{
		"index", "search", "search/stats", "embed",
		"ai/rag/ingest", "ai/rag/embed", "ai/rag/query", "ai/rag/query-multiple",
		"ai/rag/delete", "ai/rag/context",
		"ai/memory/remember", "ai/memory/search", "ai/memory/list", "ai/memory/recall",
		"ai/memory/facts", "ai/memory/update", "ai/memory/delete",
	}
	for _, e := range gated {
		if !requiresPresentCredential(e) {
			t.Errorf("%q must require a present credential", e)
		}
	}
	ungated := []string{
		"chat/completions", "completions", "models", "messages",
		"health", "metrics",
		"get-account", "query-record", "query-record-second",
		// "ai/memory" alone is not the memory family; only the "ai/memory/" prefix
		// is, so an unrelated endpoint spelled with that word is unaffected.
		"ai/memory", "ai/memorydump",
	}
	for _, e := range ungated {
		if requiresPresentCredential(e) {
			t.Errorf("%q must NOT require a present credential (benign/self-authing/unrelated)", e)
		}
	}
}

// MEMORY IS NEVER ANONYMOUS.
//
// Every verb under /v1/ai/memory reads, writes or deletes ONE named person's store.
// A caller with no credential has no store here, so there is nothing for it to be
// doing — and letting it through meant a stranger could name any user in a header
// and read, overwrite or delete their memories. The filter refuses first; the
// controller resolves the person from the credential.
func TestMemoryIsNeverAnonymous(t *testing.T) {
	for _, p := range []struct{ method, path string }{
		{"POST", "/v1/ai/memory/remember"},
		{"GET", "/v1/ai/memory/search"},
		{"GET", "/v1/ai/memory/list"},
		{"GET", "/v1/ai/memory/recall"},
		{"GET", "/v1/ai/memory/facts"},
		{"POST", "/v1/ai/memory/update"},
		{"POST", "/v1/ai/memory/delete"},
	} {
		q := asUser(t, p.method, p.path, nil).through(permissionFilter)
		if q.status() != http.StatusUnauthorized {
			t.Errorf("anonymous %s %s = %d; want 401 — memory is per-person", p.method, p.path, q.status())
		}
	}

	// A credentialed caller still passes the filter; the controller decides who.
	q := asUser(t, "POST", "/v1/ai/memory/remember", &iam.User{Owner: "acme", Name: "bob"}).through(permissionFilter)
	if q.status() == http.StatusUnauthorized {
		t.Error("a session-authed memory write was refused at the filter")
	}
}

// ── C-1: path-normalization bypass regression ───────────────────────────────
//
// the router path.Cleans the request path before dispatch, so slash/dot variants of a
// gated multi-segment route (admin/providers*) still reach the controller. The
// gate must key on the SAME normalized name, or every variant falls through to the
// fully-open default — an UNAUTHENTICATED takeover of provider routing. These tests
// FAIL against the pre-fix filter (which keyed on the raw TrimPrefix path) and pass
// after. httptest.NewRequest preserves the raw "//", "/./", "/../", trailing-slash
// forms in URL.Path (verified), so the filter sees exactly what a real server
// delivers to a BeforeRouter filter.

// c1BypassVariants are the request paths that the router dispatches to a gated admin
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
		// The provider CRUD moved to the namespaced REST surface; the gate keys on
		// the same name as before because policyKey derives it from the resource
		// table. These are the cases that would have opened the gate if the rename
		// had been done by hand in the policy maps instead.
		{"/v1/ai/providers", "get-providers"},
		{"/v1/ai/providers/", "get-providers"},
		{"/v1//ai/providers", "get-providers"},
		{"/v1/ai/./providers", "get-providers"},
	}
	for _, c := range cases {
		got, ok := normalizedControllerName(c.raw, "GET")
		if !ok || got != c.want {
			t.Errorf("normalizedControllerName(%q) = (%q, %v), want (%q, true)", c.raw, got, ok, c.want)
		}
		// The normalized name MUST hit the gate for these gated endpoints.
		if !requiresSuperAdmin(got) {
			t.Errorf("normalized %q -> %q does NOT require super admin (gate bypass!)", c.raw, got)
		}
	}
	// Non-/v1 paths are correctly reported as pass-through.
	if _, ok := normalizedControllerName("/healthz", "GET"); ok {
		t.Error("/healthz must be reported non-/v1 (ok=false)")
	}
}

// TestAdminRoutesUnauthenticated401_AllVariants is the core C-1 regression: an
// UNAUTHENTICATED caller hitting ANY slash/dot variant of the admin routes must be
// denied (401), never fall through to 200. Pre-fix, the variants returned the
// controller (or its open default); post-fix they all 401.
func TestAdminRoutesUnauthenticated401_AllVariants(t *testing.T) {
	for _, v := range c1BypassVariants {
		q := asUser(t, v.method, v.path, nil)
		q = q.through(permissionFilter)
		if q.status() != http.StatusUnauthorized {
			t.Errorf("unauth %s %s (%s) = %d, want 401 (BYPASS if 200)", v.method, v.path, v.note, q.status())
		}
	}
}

// TestAdminRoutesOrgAdminForbidden403_AllVariants: a non-global (org) admin must be
// 403 on every variant — the gate normalizes, THEN checks super admin. This proves
// the variant can't be used to downgrade from the super-admin requirement either.
func TestAdminRoutesOrgAdminForbidden403_AllVariants(t *testing.T) {
	orgAdmin := &iam.User{Owner: "maxpower", Name: "dave", IsAdmin: true}
	for _, v := range c1BypassVariants {
		q := asUser(t, v.method, v.path, orgAdmin)
		q = q.through(permissionFilter)
		if q.status() != http.StatusForbidden {
			t.Errorf("org-admin %s %s (%s) = %d, want 403", v.method, v.path, v.note, q.status())
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
		q := asUser(t, p.method, p.path, globalAdmin)
		q = q.through(permissionFilter)
		if q.status() != http.StatusOK || len(q.said()) != 0 {
			t.Errorf("super-admin %s %s wrote a denial (code=%d, body=%q); want pass-through", p.method, p.path, q.status(), q.said())
		}
	}
}

// TestPublicModelProvidersNotGated: /v1/models/providers is the public secret-free
// enabled-name feed — it must NOT require super admin (the pricing sync reads it
// unauthenticated). Guards against accidentally gating the public feed.
func TestPublicModelProvidersNotGated(t *testing.T) {
	for _, raw := range []string{"/v1/models/providers", "/v1/models/providers/"} {
		name, ok := normalizedControllerName(raw, "GET")
		if !ok {
			t.Fatalf("%s should be a /v1 path", raw)
		}
		if requiresSuperAdmin(name) {
			t.Errorf("%q (%q) must NOT require super admin — it is the public feed", raw, name)
		}
	}
}

// ── get-cloud-usages: Bearer-reachable, self-scoping usage read ──────────────
//
// get-cloud-usages is the dual-use usage read (tenant own-org + super-admin
// god-view). It authenticates session-OR-Bearer and self-scopes in the handler
// (RequirePrincipal + resolveCloudUsageScope), so it must NOT be caught by the
// coarse session-only IsAdmin gate — which reads only the session (403s every
// Bearer caller) and demands org-admin (403s a regular tenant reading its OWN
// usage). These tests force preview OFF (the hardened prod posture where that
// gate is live) and assert the filter DEFERS to the handler.

// TestCloudUsagesClassification locks in that get-cloud-usages is neither a
// super-admin endpoint nor a present-credential (write/ingest) endpoint — it is a
// benign, self-authing read like get-account.
func TestCloudUsagesClassification(t *testing.T) {
	if requiresSuperAdmin("get-cloud-usages") {
		t.Error("get-cloud-usages must NOT require super admin (tenants read their own usage)")
	}
	if requiresPresentCredential("get-cloud-usages") {
		t.Error("get-cloud-usages must NOT be a present-credential write endpoint (it self-auths like get-account)")
	}
}

// TestCloudUsagesExemptFromAdminGate — preview OFF, get-cloud-usages passes the
// filter for a Bearer caller, a non-admin session, AND a no-principal request
// (the handler is the authority: it 401s no-principal and scopes the rest). This
// is exactly the deferral get-account gets.
func TestCloudUsagesExemptFromAdminGate(t *testing.T) {
	t.Setenv("DISABLE_PREVIEW_MODE", "true") // hardened: the IsAdmin gate is live

	// Bearer, no session — pre-change this 403'd (gate reads only the session).
	q := withKey("GET", "/v1/get-cloud-usages", "some.jwt.bearer")
	q = q.through(permissionFilter)
	if q.status() != http.StatusOK || len(q.said()) != 0 {
		t.Errorf("bearer get-cloud-usages wrote a denial (code=%d, body=%q); want filter pass-through", q.status(), q.said())
	}

	// Non-admin session — pre-change this 403'd (gate demands org-admin); a tenant
	// must be able to read its OWN usage.
	nonAdmin := &iam.User{Owner: "maxpower", Name: "dave"}
	q2 := asUser(t, "GET", "/v1/get-cloud-usages", nonAdmin)
	q2 = q2.through(permissionFilter)
	if q2.status() != http.StatusOK || len(q2.said()) != 0 {
		t.Errorf("non-admin session get-cloud-usages wrote a denial (code=%d, body=%q); want filter pass-through", q2.status(), q2.said())
	}

	// No principal at all — the filter defers; the HANDLER (RequirePrincipal) is
	// the fail-closed 401 authority, so the filter itself must not deny.
	q3 := asUser(t, "GET", "/v1/get-cloud-usages", nil)
	q3 = q3.through(permissionFilter)
	if q3.status() != http.StatusOK || len(q3.said()) != 0 {
		t.Errorf("anonymous get-cloud-usages filter wrote a denial (code=%d, body=%q); want pass-through to the handler", q3.status(), q3.said())
	}
}

// TestLegacyGetUsagesStillAdminGated is the anti-over-broadening regression: the
// exemption is for get-cloud-usages ONLY. The legacy get-usages / get-range-usages
// admin reads stay session-admin-gated under preview OFF — a non-admin session is
// still 403.
func TestLegacyGetUsagesStillAdminGated(t *testing.T) {
	t.Setenv("DISABLE_PREVIEW_MODE", "true")
	nonAdmin := &iam.User{Owner: "maxpower", Name: "dave"}
	for _, path := range []string{"/v1/get-usages", "/v1/get-range-usages"} {
		q := asUser(t, "GET", path, nonAdmin)
		q = q.through(permissionFilter)
		if q.status() != http.StatusForbidden {
			t.Errorf("non-admin %s = %d, want 403 (legacy admin read must stay gated)", path, q.status())
		}
	}
}

// roundTrip is the wired router used by the round-trip tests.
var roundTrip *zip.App

// roundTripOnce wires the real router + filter + a memory session manager exactly once.
var roundTripOnce sync.Once

func setupRoundTripRouter(t *testing.T) {
	t.Helper()
	roundTripOnce.Do(func() {
		// The REAL filter and the REAL routes, registered the way production registers
		// them — filter first, because the stack runs in order. No session manager: an
		// unauthenticated request carries no token, so the gate answers 401 from the
		// absence rather than from an empty store.
		app := zip.New(zip.Config{DisableStartupMessage: true, ReadBufferSize: 32 << 10})
		app.Use(zip.H(AuthzFilter))
		route(app, "/v1/admin/providers", "GET:GetAdminProviders")
		route(app, "/v1/admin/providers/toggle", "POST:ToggleAdminProvider")
		route(app, "/v1/admin/providers/primary", "POST:SetPrimaryAdminProvider")
		roundTrip = app
	})
}

// TestRouterRoundTrip_BypassBlocked drives requests through the REAL controller router +
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
		resp, err := roundTrip.Fiber().Test(httptest.NewRequest(v.method, "http://api.hanzo.ai"+v.path, nil))
		if err != nil {
			t.Fatalf("%s %s: %v", v.method, v.path, err)
		}
		// Unauthenticated → the gate denies before dispatch (401). Must NOT be 200
		// (bypass reaching the controller). 401 or 403 are both acceptable denials.
		if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
			t.Errorf("router %s %-34s (%s) = %d, want 401/403 (BYPASS if not a denial)", v.method, v.path, v.note, resp.StatusCode)
		}
	}
}

// An endpoint whose name matches none of the verb prefixes used to pass with no
// credential, so whether it was reachable anonymously depended on what somebody
// had called it. A bare noun is the plain case — nothing about such a name says
// open, and nothing asked for a caller.
//
// The default is closed now. These are the shapes that used to fall through.
func TestAnUnprefixedEndpointNeedsACaller(t *testing.T) {
	for _, p := range []struct{ method, path string }{
		{"POST", "/v1/ai/feedback"},
		{"POST", "/v1/ai/org/settings"},
	} {
		q := asUser(t, p.method, p.path, nil)
		q = q.through(permissionFilter)
		if q.status() != http.StatusUnauthorized {
			t.Errorf("anonymous %s %s = %d; an unnamed endpoint must ask for a caller", p.method, p.path, q.status())
		}
	}
}

// The paired control: the open ones stay open, so the refusal above is about
// being unnamed and not about the branch being shut.
func TestTheNamedOpenEndpointsStayOpen(t *testing.T) {
	for _, p := range []struct{ method, path string }{
		{"GET", "/v1/models"},
		{"GET", "/v1/ai/traffic/globe"},
	} {
		q := asUser(t, p.method, p.path, nil)
		q = q.through(permissionFilter)
		if q.status() == http.StatusUnauthorized {
			t.Errorf("%s %s = 401; it is named open and must stay reachable", p.method, p.path)
		}
	}
}
