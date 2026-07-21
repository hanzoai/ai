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
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	web "github.com/hanzoai/ai/web"
	iam "github.com/hanzoai/ai/internal/iam"
)

// newUsageController builds an ApiController for GetCloudUsages auth+scope tests:
// a full request URL (carrying ?org=/?owner= query params), an optional
// Authorization header, an optional X-Org-Id tenant header, and an optional
// session principal. It reuses ctrlFakeSession (index_auth_test.go) so
// GetSessionUser resolves without a live session manager.
func newUsageController(url, authHeader, orgHeader string, user *iam.User) (*ApiController, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", url, nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	if orgHeader != "" {
		req.Header.Set("X-Org-Id", orgHeader)
	}
	ctx := web.NewContext()
	ctx.Reset(rec, req)
	sess := &ctrlFakeSession{data: map[interface{}]interface{}{}}
	if user != nil {
		sess.data["user"] = iam.Claims{User: *user}
	}
	ctx.Input.CruSession = sess
	c := &ApiController{}
	c.Init(ctx, "ApiController", "GetCloudUsages", nil)
	return c, rec
}

// installUsageCert installs a fresh self-signed RSA cert as the IAM verification
// key and returns the matching private key, so a test can mint tokens the REAL
// principal path (credentialUser → ParseAndValidateJWT → iam.ParseJwtToken)
// verifies. Each call rotates the installed cert, so use a minted token before
// installing the next. Cleanup resets to a benign empty cert (fail-closed).
func installUsageCert(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "usage-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	iam.InitConfig("", "", "", string(certPEM), "", "")
	t.Cleanup(func() { iam.InitConfig("", "", "", "", "", "") })
	return key
}

// usageClaims builds a standard claim set: owner/name identity + iss/aud + a fresh
// validity window. Callers override individual keys (iss for a cross-brand token,
// exp for an expired one) before signing.
func usageClaims(owner, name, iss, aud string) jwt.MapClaims {
	return jwt.MapClaims{
		"owner": owner,
		"name":  name,
		"iss":   iss,
		"aud":   aud,
		"iat":   time.Now().Unix(),
		"exp":   time.Now().Add(time.Hour).Unix(),
	}
}

// mintUsageJWT installs a fresh cert and returns a valid OWN-brand (iss=hanzo.id)
// RS256 token for owner/name — the common case for the accept-and-scope tests.
func mintUsageJWT(t *testing.T, owner, name string) string {
	t.Helper()
	key := installUsageCert(t)
	tok, err := jwt.NewWithClaims(jwt.SigningMethodRS256, usageClaims(owner, name, "https://hanzo.id", "hanzo-cloud")).SignedString(key)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return tok
}

// TestResolveCloudUsageScope is the scope-safety invariant, proved as a pure
// function of the RESOLVED principal + request params. Because
// resolveCloudUsageScope only ever sees the *iam.User (never how it was
// authenticated), this table holds identically for a session OR a Bearer
// principal: a non-super-admin is ALWAYS pinned to its own org, and only a
// super-admin (Owner=="admin") can target another org / the all-orgs view.
func TestResolveCloudUsageScope(t *testing.T) {
	nonAdmin := &iam.User{Owner: "maxpower", Name: "dave", IsAdmin: true} // org-admin, NOT super
	superAdmin := &iam.User{Owner: "admin", Name: "z", IsAdmin: true}

	cases := []struct {
		name       string
		url        string
		orgHeader  string
		user       *iam.User
		wantOrg    string
		wantAllOrg bool
	}{
		// A non-super-admin can never escape its own org, whatever it asks for.
		{"non-admin + ?org=all -> pinned", "/x?org=all", "", nonAdmin, "maxpower", false},
		{"non-admin + ?org=hanzo (other) -> pinned", "/x?org=hanzo", "", nonAdmin, "maxpower", false},
		{"non-admin + ?org=* -> pinned", "/x?org=*", "", nonAdmin, "maxpower", false},
		{"non-admin + ?owner=hanzo -> pinned", "/x?owner=hanzo", "", nonAdmin, "maxpower", false},
		{"non-admin + X-Org-Id: hanzo -> pinned", "/x", "hanzo", nonAdmin, "maxpower", false},
		{"non-admin + ?org=all + X-Org-Id: hanzo -> pinned", "/x?org=all", "hanzo", nonAdmin, "maxpower", false},
		// A super-admin drives the god-view / targeting.
		{"super + ?org=all -> god-view", "/x?org=all", "", superAdmin, "", true},
		{"super + no scope -> god-view", "/x", "", superAdmin, "", true},
		{"super + ?org=maxpower -> targeted", "/x?org=maxpower", "", superAdmin, "maxpower", false},
		{"super + ?owner=zoo -> targeted", "/x?owner=zoo", "", superAdmin, "zoo", false},
		{"super + X-Org-Id: maxpower -> targeted", "/x", "maxpower", superAdmin, "maxpower", false},
	}
	for _, tc := range cases {
		// Seed the principal as a SESSION user: a session is own-brand, so the scope
		// mapping is exercised for a same-brand principal (the brand gate is proved
		// separately in TestResolveCloudUsageScope_BrandScopedGodView).
		c, _ := newUsageController(tc.url, "", tc.orgHeader, tc.user)
		gotOrg, gotAll := c.resolveCloudUsageScope(tc.user)
		if gotOrg != tc.wantOrg || gotAll != tc.wantAllOrg {
			t.Errorf("%s: scope = (%q, %v), want (%q, %v)", tc.name, gotOrg, gotAll, tc.wantOrg, tc.wantAllOrg)
		}
	}
}

// TestRequirePrincipal_NoCredentialIs401 — task test (d): a request with NEITHER
// a session NOR a Bearer is denied a real 401 (never a 200, never a fabricated
// principal). This is the fail-closed floor of the Bearer-enabled read.
func TestRequirePrincipal_NoCredentialIs401(t *testing.T) {
	c, rec := newUsageController("/v1/get-cloud-usages?org=all", "", "hanzo", nil)
	user, ok := c.RequirePrincipal()
	if ok || user != nil {
		t.Fatalf("RequirePrincipal with no credential = (%+v, %v), want (nil, false)", user, ok)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no-credential status = %d, want 401", rec.Code)
	}
}

// TestRequirePrincipal_SessionUnchanged — task test (c): the session-cookie path
// is unchanged. A session principal resolves to itself and (being non-super)
// scopes to its own org even with ?org=all — identical to the pre-change behavior.
func TestRequirePrincipal_SessionUnchanged(t *testing.T) {
	sessionUser := &iam.User{Owner: "maxpower", Name: "dave"}
	c, rec := newUsageController("/v1/get-cloud-usages?org=all", "", "", sessionUser)
	user, ok := c.RequirePrincipal()
	if !ok || user == nil {
		t.Fatalf("RequirePrincipal with a session = (%+v, %v), want a user", user, ok)
	}
	if user.Owner != "maxpower" {
		t.Errorf("resolved Owner = %q, want maxpower (the session principal)", user.Owner)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("session pass wrote a body %q, want none", rec.Body.String())
	}
	if org, allOrgs := c.resolveCloudUsageScope(user); org != "maxpower" || allOrgs {
		t.Errorf("session non-admin + ?org=all scope = (%q, %v), want (maxpower, false)", org, allOrgs)
	}
}

// TestGetCloudUsagesBearerScope is the end-to-end proof over a REAL signed Bearer
// (no session): the principal — and thus the super-admin decision — is derived
// SOLELY from the verified token, never from a header or param.
//
//   - task test (a): a NON-admin Bearer asking for ?org=all with a spoofed
//     X-Org-Id is pinned to its OWN org; it cannot read all-orgs or another org.
//   - task test (b): a super-admin Bearer gets the god-view, and can target a
//     single org.
func TestGetCloudUsagesBearerScope(t *testing.T) {
	// Deterministic audience policy: a non-empty allowlist that includes our aud.
	t.Setenv("GATEWAY_ALLOWED_AUDIENCES", "hanzo-cloud")

	t.Run("non-admin bearer pinned despite ?org=all + X-Org-Id", func(t *testing.T) {
		tok := mintUsageJWT(t, "maxpower", "dave")
		c, rec := newUsageController("/v1/get-cloud-usages?org=all", "Bearer "+tok, "hanzo", nil)

		user, ok := c.RequirePrincipal()
		if !ok || user == nil {
			t.Fatalf("RequirePrincipal(bearer) = (%+v, %v), want the token principal", user, ok)
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("bearer auth wrote a denial body %q; the token must be accepted", rec.Body.String())
		}
		if user.Owner != "maxpower" {
			t.Fatalf("bearer resolved Owner = %q, want maxpower (from the token, not the X-Org-Id header)", user.Owner)
		}
		org, allOrgs := c.resolveCloudUsageScope(user)
		if org != "maxpower" || allOrgs {
			t.Errorf("non-admin bearer scope = (%q, %v), want (maxpower, false) — a tenant must never reach ?org=all", org, allOrgs)
		}
	})

	t.Run("super-admin bearer god-view", func(t *testing.T) {
		tok := mintUsageJWT(t, "admin", "z")
		c, _ := newUsageController("/v1/get-cloud-usages?org=all", "Bearer "+tok, "", nil)

		user, ok := c.RequirePrincipal()
		if !ok || user == nil || user.Owner != "admin" {
			t.Fatalf("RequirePrincipal(super bearer) = (%+v, %v), want Owner=admin", user, ok)
		}
		if org, allOrgs := c.resolveCloudUsageScope(user); org != "" || !allOrgs {
			t.Errorf("super-admin bearer + ?org=all scope = (%q, %v), want (\"\", true) god-view", org, allOrgs)
		}
	})

	t.Run("super-admin bearer can target one org", func(t *testing.T) {
		tok := mintUsageJWT(t, "admin", "z")
		c, _ := newUsageController("/v1/get-cloud-usages?org=maxpower", "Bearer "+tok, "", nil)

		user, ok := c.RequirePrincipal()
		if !ok || user == nil {
			t.Fatalf("RequirePrincipal(super bearer) failed")
		}
		if org, allOrgs := c.resolveCloudUsageScope(user); org != "maxpower" || allOrgs {
			t.Errorf("super-admin bearer + ?org=maxpower scope = (%q, %v), want (maxpower, false)", org, allOrgs)
		}
	})
}

// TestGetCloudUsagesRejectsForgedBearer — the tenant-safety of the whole feature
// rests on ParseAndValidateJWT rejecting anything it did not sign. A token signed
// by a DIFFERENT key than the installed IAM cert (a forgery, e.g. an attacker
// claiming owner=admin) resolves to NO principal, so RequirePrincipal 401s and no
// scope is ever computed from it.
func TestGetCloudUsagesRejectsForgedBearer(t *testing.T) {
	t.Setenv("GATEWAY_ALLOWED_AUDIENCES", "hanzo-cloud")

	// Install a legitimate cert for one identity...
	_ = mintUsageJWT(t, "maxpower", "dave")

	// ...then forge a token for a DIFFERENT key claiming owner=admin.
	attackerKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa key: %v", err)
	}
	forged, err := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"owner": "admin", "name": "z", "iss": "https://hanzo.id", "aud": "hanzo-cloud",
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString(attackerKey)
	if err != nil {
		t.Fatalf("sign forged: %v", err)
	}

	c, rec := newUsageController("/v1/get-cloud-usages?org=all", "Bearer "+forged, "", nil)
	if user, ok := c.RequirePrincipal(); ok || user != nil {
		t.Fatalf("forged bearer resolved a principal %+v — signature verification failed to reject it", user)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("forged bearer status = %d, want 401", rec.Code)
	}
}

// TestResolveCloudUsageScope_BrandScopedGodView is RED hardening #1: the all-orgs
// god-view and cross-org targeting are granted ONLY to a SAME-BRAND super admin.
// A sibling white-label brand's super admin (a lux.id owner=admin token that the
// auth layer trusts for sign-in) is pinned to its own org — it can NEVER read this
// brand's all-customer spend, independent of whatever the IAM SDK later verifies.
func TestResolveCloudUsageScope_BrandScopedGodView(t *testing.T) {
	// Non-empty allowlist folds in every brand aud (withBrandAudiences), so a lux
	// token's aud=lux-cloud is accepted for SIGN-IN — proving the brand gate, not an
	// audience reject, is what pins it.
	t.Setenv("GATEWAY_ALLOWED_AUDIENCES", "hanzo-cloud")

	t.Run("same-brand super-admin SESSION -> god-view", func(t *testing.T) {
		superAdmin := &iam.User{Owner: "admin", Name: "z"}
		c, _ := newUsageController("/v1/get-cloud-usages?org=all", "", "", superAdmin)
		if org, allOrgs := c.resolveCloudUsageScope(superAdmin); org != "" || !allOrgs {
			t.Errorf("same-brand session super-admin scope = (%q, %v), want (\"\", true)", org, allOrgs)
		}
	})

	t.Run("same-brand super-admin BEARER (iss=hanzo.id) -> god-view", func(t *testing.T) {
		tok := mintUsageJWT(t, "admin", "z") // iss=hanzo.id == expectedJWTIssuer()
		c, _ := newUsageController("/v1/get-cloud-usages?org=all", "Bearer "+tok, "", nil)
		user, ok := c.RequirePrincipal()
		if !ok || user == nil {
			t.Fatalf("own-brand super-admin bearer not accepted")
		}
		if org, allOrgs := c.resolveCloudUsageScope(user); org != "" || !allOrgs {
			t.Errorf("same-brand bearer super-admin scope = (%q, %v), want (\"\", true)", org, allOrgs)
		}
	})

	t.Run("cross-brand super-admin BEARER (iss=lux.id) -> PINNED, not god-view", func(t *testing.T) {
		key := installUsageCert(t)
		tok, err := jwt.NewWithClaims(jwt.SigningMethodRS256,
			usageClaims("admin", "z", "https://lux.id", "lux-cloud")).SignedString(key)
		if err != nil {
			t.Fatalf("sign lux token: %v", err)
		}
		c, _ := newUsageController("/v1/get-cloud-usages?org=all", "Bearer "+tok, "", nil)

		// The lux token is a VALID sign-in credential (trusted issuer + brand aud)...
		user, ok := c.RequirePrincipal()
		if !ok || user == nil || user.Owner != "admin" {
			t.Fatalf("cross-brand admin bearer = (%+v, %v); expected it to authenticate as owner=admin (the whole point: auth trusts it, scope must not)", user, ok)
		}
		// ...but it is NOT own-brand, so the god-view is denied and it is pinned.
		if org, allOrgs := c.resolveCloudUsageScope(user); org != "admin" || allOrgs {
			t.Errorf("cross-brand admin scope = (%q, %v), want (\"admin\", false) — sibling brand admin MUST NOT reach the all-orgs god-view", org, allOrgs)
		}
	})

	t.Run("cross-brand super-admin BEARER cannot target another org", func(t *testing.T) {
		key := installUsageCert(t)
		tok, err := jwt.NewWithClaims(jwt.SigningMethodRS256,
			usageClaims("admin", "z", "https://lux.id", "lux-cloud")).SignedString(key)
		if err != nil {
			t.Fatalf("sign lux token: %v", err)
		}
		c, _ := newUsageController("/v1/get-cloud-usages?org=maxpower", "Bearer "+tok, "", nil)
		user, _ := c.RequirePrincipal()
		if org, allOrgs := c.resolveCloudUsageScope(user); org != "admin" || allOrgs {
			t.Errorf("cross-brand admin + ?org=maxpower scope = (%q, %v), want (\"admin\", false) — cannot target another tenant", org, allOrgs)
		}
	})
}

// TestResolveCloudUsageScope_EmptyOwner — a principal with an empty Owner scopes to
// organization=” (a real, empty tenant filter: whereClause adds `organization = ?`
// with p.Org=""), never the all-orgs view. Fail-secure: absence of an org is NOT
// the god-view.
func TestResolveCloudUsageScope_EmptyOwner(t *testing.T) {
	ghost := &iam.User{Owner: "", Name: "ghost"}
	c, _ := newUsageController("/v1/get-cloud-usages?org=all", "", "", ghost)
	if org, allOrgs := c.resolveCloudUsageScope(ghost); org != "" || allOrgs {
		t.Errorf("empty-Owner scope = (%q, %v), want (\"\", false) — empty org filter, NOT all-orgs", org, allOrgs)
	}
}

// TestGetCloudUsages_RejectsAnonymous is RED hardening #2: an anonymous guest
// (anonymousSignin binds Owner=IAM_ORG) must not read org-aggregate spend. The
// handler rejects it BEFORE any warehouse read, so this drives the real handler.
func TestGetCloudUsages_RejectsAnonymous(t *testing.T) {
	for _, anon := range []*iam.User{
		{Owner: "hanzo", Type: "anonymous-user", Name: "someone"},
		{Owner: "hanzo", Name: "u-12345678"}, // anonymous by u-<hash> username (len 10)
	} {
		c, rec := newUsageController("/v1/get-cloud-usages", "", "", anon)
		c.GetCloudUsages()
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("anonymous %+v -> GetCloudUsages status = %d, want 401 (rejected before any warehouse read)", anon, rec.Code)
		}
	}
}

// TestRequirePrincipal_RejectsMalformedBearer covers the RED-flagged token-forgery
// classes on the Bearer path: alg=none, the RS256→HS256 confusion (a symmetric
// token signed with the public-cert bytes as an HMAC key), and an expired token.
// Each must resolve NO principal (401), so no scope is ever computed from them.
func TestRequirePrincipal_RejectsMalformedBearer(t *testing.T) {
	t.Setenv("GATEWAY_ALLOWED_AUDIENCES", "hanzo-cloud")

	t.Run("alg=none", func(t *testing.T) {
		installUsageCert(t) // globalClient must be non-nil for the keyfunc to run
		tok, err := jwt.NewWithClaims(jwt.SigningMethodNone,
			usageClaims("admin", "z", "https://hanzo.id", "hanzo-cloud")).
			SignedString(jwt.UnsafeAllowNoneSignatureType)
		if err != nil {
			t.Fatalf("sign none: %v", err)
		}
		assertBearerRejected(t, tok)
	})

	t.Run("HS256 confusion", func(t *testing.T) {
		installUsageCert(t)
		tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256,
			usageClaims("admin", "z", "https://hanzo.id", "hanzo-cloud")).
			SignedString([]byte("attacker-chosen-hmac-secret"))
		if err != nil {
			t.Fatalf("sign hs256: %v", err)
		}
		assertBearerRejected(t, tok)
	})

	t.Run("expired", func(t *testing.T) {
		key := installUsageCert(t)
		claims := usageClaims("admin", "z", "https://hanzo.id", "hanzo-cloud")
		claims["iat"] = time.Now().Add(-2 * time.Hour).Unix()
		claims["exp"] = time.Now().Add(-time.Hour).Unix()
		tok, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
		if err != nil {
			t.Fatalf("sign expired: %v", err)
		}
		assertBearerRejected(t, tok)
	})
}

// assertBearerRejected drives RequirePrincipal with a Bearer token and asserts it
// resolves no principal and writes a 401.
func assertBearerRejected(t *testing.T, token string) {
	t.Helper()
	c, rec := newUsageController("/v1/get-cloud-usages?org=all", "Bearer "+token, "", nil)
	if user, ok := c.RequirePrincipal(); ok || user != nil {
		t.Fatalf("malformed bearer resolved a principal %+v; want none", user)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("malformed bearer status = %d, want 401", rec.Code)
	}
}
