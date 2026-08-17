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
	"os"
	"testing"

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/internal/authtest"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
)

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

	c := visit(http.MethodPost, "/v1/rag/embed")
	auth := c.requireIndexAuth()

	if auth != nil {
		t.Fatalf("requireIndexAuth for a no-credential caller returned %+v; want nil (deny)", auth)
	}
	if answered(c) != http.StatusUnauthorized {
		t.Errorf("no-credential requireIndexAuth status = %d, want 401", answered(c))
	}
}

// TestRequireIndexAuthPublishableKeyForbidden — a pk- key is a VALID credential
// but read-only, so a write is 403 (not 401, not the old 200).
func TestRequireIndexAuthPublishableKeyForbidden(t *testing.T) {
	forcePreviewOn(t)

	c := presenting(visit(http.MethodPost, "/v1/scrape"), "Bearer pk-read-only-key")
	auth := c.requireIndexAuth()

	if auth != nil {
		t.Fatalf("requireIndexAuth for a pk- key returned %+v; want nil (deny)", auth)
	}
	if answered(c) != http.StatusForbidden {
		t.Errorf("pk- write requireIndexAuth status = %d, want 403", answered(c))
	}
}

// TestRequireIndexAuthAdminAllowed — a real admin still passes and
// is scoped to their OWN org (never a forced "admin" tenant).
func TestRequireIndexAuthAdminAllowed(t *testing.T) {
	forcePreviewOn(t)

	admin := iam.User{Owner: "acme", Name: "root", IsAdmin: true}
	c := presenting(visit(http.MethodPost, "/v1/index"), authtest.Bearer(t, admin))
	auth := c.requireIndexAuth()

	if auth == nil {
		t.Fatal("requireIndexAuth for an admin returned nil; want the admin's searchAuth")
	}
	if auth.Owner != "acme" {
		t.Errorf("admin resolved Owner=%q, want %q (own org, not a forced admin tenant)", auth.Owner, "acme")
	}
}

// TestResolveSearchAuthNoCredentialIsUnauthorized — the read path also fails
// closed with a real 401 (F4: auth failures were HTTP 200 via plain
// ResponseError; they are now ResponseUnauthorized).
func TestResolveSearchAuthNoCredentialIsUnauthorized(t *testing.T) {
	forcePreviewOn(t)

	c := visit(http.MethodPost, "/v1/search")
	auth := c.resolveSearchAuth()

	if auth != nil {
		t.Fatalf("resolveSearchAuth for a no-credential caller returned %+v; want nil", auth)
	}
	if answered(c) != http.StatusUnauthorized {
		t.Errorf("no-credential resolveSearchAuth status = %d, want 401 (was 200 via plain ResponseError)", answered(c))
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
	// Asked through PayerSubject, which is how the gate and the debit ask it. A
	// hand-built account.Credential here would omit Machine — the field that
	// carries "this is the org acting, not a person" — and answer a question
	// production never poses.
	if got := u.PayerSubject(""); got != "acme" {
		t.Errorf("PayerSubject(%q,%q) = %q, want %q (org-level ledger)", u.Owner, u.Name, got, "acme")
	}

	// The signup org, where a missing name is otherwise read as "not a person" and
	// REFUSED (account ≥ v0.3.2) so that a credential which failed to name anybody
	// cannot spend the platform's balance. A provider key is not that: it is the
	// org itself acting, said by Type "application", and a machine is answered by
	// the org ledger before the nameless rule is ever reached. So the shared do-ai
	// provider still bills the org — the zero-billing leak stays closed, and it
	// does not 402 on a phantom per-user subject.
	//
	// This is exactly why the question is asked through PayerSubject: it carries
	// Machine from Type, so the answer cannot depend on a caller remembering to.
	uh, err := providerKeyBillingUser(&object.Provider{Owner: "hanzo", Name: "do-ai"})
	if err != nil || uh == nil {
		t.Fatalf("providerKeyBillingUser(owner=hanzo) = (%v, %v); want a user", uh, err)
	}
	if got := uh.PayerSubject(""); got != "hanzo" {
		t.Errorf("PayerSubject(hanzo, %q) = %q, want %q (org-level, not per-user)", uh.Name, got, "hanzo")
	}
}
