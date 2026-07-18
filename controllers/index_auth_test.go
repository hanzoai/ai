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
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/hanzoai/account"

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/object"
	web "github.com/hanzoai/ai/web"
	iam "github.com/hanzoai/iam"
)

// ctrlFakeSession is a minimal session.Store for controller auth tests — beego's
// GetSession reads c.Ctx.Input.CruSession, so this lets GetSessionUser resolve
// without a live session manager.
type ctrlFakeSession struct{ data map[interface{}]interface{} }

func (s *ctrlFakeSession) Set(k, v interface{}) error         { s.data[k] = v; return nil }
func (s *ctrlFakeSession) Get(k interface{}) interface{}      { return s.data[k] }
func (s *ctrlFakeSession) Delete(k interface{}) error         { delete(s.data, k); return nil }
func (s *ctrlFakeSession) SessionID() string                  { return "test" }
func (s *ctrlFakeSession) SessionRelease(http.ResponseWriter) {}
func (s *ctrlFakeSession) Flush() error {
	s.data = map[interface{}]interface{}{}
	return nil
}

// newAuthController builds an ApiController wired to a recorder, an optional
// session user, and an optional Authorization header.
func newAuthController(method, path, authHeader string, user *iam.User) (*ApiController, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ctx := web.NewContext()
	ctx.Reset(rec, req)
	sess := &ctrlFakeSession{data: map[interface{}]interface{}{}}
	if user != nil {
		sess.data["user"] = iam.Claims{User: *user}
	}
	ctx.Input.CruSession = sess
	c := &ApiController{}
	c.Init(ctx, "ApiController", "X", nil)
	return c, rec
}

// forcePreviewOn deterministically enables preview mode for the duration of a
// test, so the assertions below prove the auth denial holds EVEN under the
// (dangerous) preview relaxation — the exact condition that used to grant admin.
func forcePreviewOn(t *testing.T) {
	t.Helper()
	orig, had := os.LookupEnv("DISABLE_PREVIEW_MODE")
	os.Setenv("DISABLE_PREVIEW_MODE", "false") // not "true" → preview ON
	t.Cleanup(func() {
		if had {
			os.Setenv("DISABLE_PREVIEW_MODE", orig)
		} else {
			os.Unsetenv("DISABLE_PREVIEW_MODE")
		}
	})
	if !conf.IsPreviewMode() {
		t.Fatal("test precondition: preview mode must be ON to prove the fix holds under preview")
	}
}

// TestRequireIndexAuthNoCredentialIsUnauthorized is the F1 core assertion: a
// NO-CREDENTIAL caller to an index/scrape/ingest write must be denied 401 and
// must NOT resolve to the admin tenant — EVEN in preview mode. This is the exact
// hole (preview → {Owner:"admin"}) that let unauthenticated /v1/rag/embed and
// /v1/scrape reach the admin knowledge base + browser engine.
func TestRequireIndexAuthNoCredentialIsUnauthorized(t *testing.T) {
	forcePreviewOn(t)

	c, rec := newAuthController("POST", "/v1/rag/embed", "", nil)
	auth := c.requireIndexAuth()

	if auth != nil {
		t.Fatalf("requireIndexAuth for a no-credential caller returned %+v; want nil (deny)", auth)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no-credential requireIndexAuth status = %d, want 401", rec.Code)
	}
}

// TestRequireIndexAuthPublishableKeyForbidden — a pk- key is a VALID credential
// but read-only, so a write is 403 (not 401, not the old 200).
func TestRequireIndexAuthPublishableKeyForbidden(t *testing.T) {
	forcePreviewOn(t)

	c, rec := newAuthController("POST", "/v1/scrape", "Bearer pk-read-only-key", nil)
	auth := c.requireIndexAuth()

	if auth != nil {
		t.Fatalf("requireIndexAuth for a pk- key returned %+v; want nil (deny)", auth)
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("pk- write requireIndexAuth status = %d, want 403", rec.Code)
	}
}

// TestRequireIndexAuthSessionAdminAllowed — a REAL session admin still passes and
// is scoped to their OWN org (never a forced "admin" tenant).
func TestRequireIndexAuthSessionAdminAllowed(t *testing.T) {
	forcePreviewOn(t)

	admin := &iam.User{Owner: "acme", Name: "root", IsAdmin: true}
	c, _ := newAuthController("POST", "/v1/index", "", admin)
	auth := c.requireIndexAuth()

	if auth == nil {
		t.Fatal("requireIndexAuth for a session admin returned nil; want the admin's searchAuth")
	}
	if auth.Owner != "acme" {
		t.Errorf("session admin resolved Owner=%q, want %q (own org, not a forced admin tenant)", auth.Owner, "acme")
	}
}

// TestResolveSearchAuthNoCredentialIsUnauthorized — the read path also fails
// closed with a real 401 (F4: auth failures were HTTP 200 via plain
// ResponseError; they are now ResponseUnauthorized).
func TestResolveSearchAuthNoCredentialIsUnauthorized(t *testing.T) {
	forcePreviewOn(t)

	c, rec := newAuthController("POST", "/v1/search", "", nil)
	auth := c.resolveSearchAuth()

	if auth != nil {
		t.Fatalf("resolveSearchAuth for a no-credential caller returned %+v; want nil", auth)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no-credential resolveSearchAuth status = %d, want 401 (was 200 via plain ResponseError)", rec.Code)
	}
}

// TestProviderKeyBillingUser is the F-sk assertion: an sk- provider-key call is
// attributed to the org that OWNS the provider row, and the billing subject
// collapses to that ORG ledger (Name empty) for BOTH pooled and personal-billing
// orgs — so reserveBudget + recordUsage (which gate on authUser != nil) run and
// bill someone. An unattributable provider is refused (fail-secure).
func TestProviderKeyBillingUser(t *testing.T) {
	// Unattributable → error, no identity (never spend the shared upstream free).
	if u, err := providerKeyBillingUser(nil); err == nil || u != nil {
		t.Errorf("providerKeyBillingUser(nil) = (%v, %v); want (nil, error)", u, err)
	}
	if u, err := providerKeyBillingUser(&object.Provider{Owner: ""}); err == nil || u != nil {
		t.Errorf("providerKeyBillingUser(owner=\"\") = (%v, %v); want (nil, error)", u, err)
	}
	if u, err := providerKeyBillingUser(&object.Provider{Owner: "   "}); err == nil || u != nil {
		t.Errorf("providerKeyBillingUser(owner=whitespace) = (%v, %v); want (nil, error)", u, err)
	}

	// Pooled org → the org itself is the billing subject.
	u, err := providerKeyBillingUser(&object.Provider{Owner: "acme", Name: "acme-openai"})
	if err != nil || u == nil {
		t.Fatalf("providerKeyBillingUser(owner=acme) = (%v, %v); want a user", u, err)
	}
	if u.Owner != "acme" {
		t.Errorf("billing user Owner = %q, want %q (the provider-row owner)", u.Owner, "acme")
	}
	if u.Type != "application" {
		t.Errorf("billing user Type = %q, want %q (M2M provider-key credential)", u.Type, "application")
	}
	if got := account.Payer(account.Credential{Owner: u.Owner, Name: u.Name}).Subject(); got != "acme" {
		t.Errorf("Payer(%q,%q).Subject() = %q, want %q (org-level ledger)", u.Owner, u.Name, got, "acme")
	}

	// Personal-billing org ("hanzo" is the default): the empty Name still yields
	// the ORG-level subject, NOT an unfunded per-user "hanzo/..." subject — so the
	// shared-do-ai provider (owned by the platform org) bills the org, closing the
	// zero-billing leak rather than 402-ing on a phantom subject.
	uh, err := providerKeyBillingUser(&object.Provider{Owner: "hanzo", Name: "do-ai"})
	if err != nil || uh == nil {
		t.Fatalf("providerKeyBillingUser(owner=hanzo) = (%v, %v); want a user", uh, err)
	}
	if got := account.Payer(account.Credential{Owner: uh.Owner, Name: uh.Name}).Subject(); got != "hanzo" {
		t.Errorf("Payer(hanzo, %q).Subject() = %q, want %q (org-level, not per-user)", uh.Name, got, "hanzo")
	}
}
