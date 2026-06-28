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
	"testing"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
	iam "github.com/hanzoai/iam"
)

// TestProviderSecretsMaskedForNonAdmin locks the secret-non-disclosure invariant
// behind GET /v1/get-providers (which is exempt from the admin filter, so ANY
// signed-in user can call it): the provider's ClientSecret is ALWAYS redacted,
// and the other credential fields are redacted for non-admins. A non-admin must
// never see a usable provider/upstream key.
func TestProviderSecretsMaskedForNonAdmin(t *testing.T) {
	raw := func() *object.Provider {
		return &object.Provider{
			Owner: "acme", Name: "openai",
			ClientSecret: "sk-super-secret-upstream-key",
			ProviderKey:  "pk-live-xxx",
			UserKey:      "user-key-xxx",
			ConfigText:   "secret-config",
			SignKey:      "sign-secret",
		}
	}
	nonAdmin := &iam.User{Owner: "acme", Name: "alice"}
	admin := &iam.User{Owner: "admin", Name: "root", IsAdmin: true}

	// Non-admin: every credential field redacted.
	p := object.GetMaskedProvider(raw(), true, nonAdmin)
	for field, got := range map[string]string{
		"ClientSecret": p.ClientSecret, "ProviderKey": p.ProviderKey,
		"UserKey": p.UserKey, "ConfigText": p.ConfigText, "SignKey": p.SignKey,
	} {
		if got != "***" {
			t.Errorf("non-admin: %s=%q, want redacted ***", field, got)
		}
	}

	// Admin: ClientSecret is STILL masked (never returned in plaintext), other
	// fields visible for management.
	pa := object.GetMaskedProvider(raw(), true, admin)
	if pa.ClientSecret != "***" {
		t.Errorf("admin: ClientSecret=%q, want *** (never plaintext over the wire)", pa.ClientSecret)
	}
	if pa.ProviderKey == "***" {
		t.Errorf("admin: ProviderKey should be visible for management, got redacted")
	}

	// Anonymous (nil user) is treated as non-admin → fully redacted.
	pn := object.GetMaskedProvider(raw(), true, nil)
	if pn.ProviderKey != "***" || pn.ClientSecret != "***" {
		t.Errorf("nil user: secrets not fully redacted: clientSecret=%q providerKey=%q", pn.ClientSecret, pn.ProviderKey)
	}
}

// TestIsAdminPredicate is the table for the single admin predicate every gate
// hinges on. Forwards-compatible: chat-admin and the teacher tag are admins too.
func TestIsAdminPredicate(t *testing.T) {
	cases := []struct {
		name string
		user *iam.User
		want bool
	}{
		{"nil is not admin", nil, false},
		{"plain user", &iam.User{Owner: "acme", Name: "alice"}, false},
		{"isAdmin flag", &iam.User{IsAdmin: true}, true},
		{"chat-admin type", &iam.User{Type: "chat-admin"}, true},
		{"teacher tag", &iam.User{Tag: "教师"}, true},
		{"wrong type/tag", &iam.User{Type: "chat-user", Tag: "student"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := util.IsAdmin(tc.user); got != tc.want {
				t.Fatalf("IsAdmin(%+v)=%v want %v", tc.user, got, tc.want)
			}
		})
	}
}

// TestGetEffectiveOrgResolution covers org resolution precedence: the
// gateway-injected X-IAM-Org-Id header wins, else the session user's owner, and
// an anonymous request with no header resolves to no session-derived org.
func TestGetEffectiveOrgResolution(t *testing.T) {
	// Header present → header wins (trusted gateway boundary).
	c, _ := newControllerWithUser(&iam.User{Owner: "acme", Name: "alice"}, map[string]string{"X-IAM-Org-Id": "header-org"})
	if got := c.GetEffectiveOrg(); got != "header-org" {
		t.Errorf("header present: GetEffectiveOrg=%q want header-org", got)
	}

	// No header → session user's owner.
	c, _ = newControllerWithUser(&iam.User{Owner: "acme", Name: "alice"}, nil)
	if got := c.GetEffectiveOrg(); got != "acme" {
		t.Errorf("no header: GetEffectiveOrg=%q want acme (session owner)", got)
	}

	// Anonymous, no header, no IAM_ORG config → empty (never panics — session
	// hardening). Falls back to conf IAM_ORG which is unset in test.
	c, _ = newControllerWithUser(nil, nil)
	if got := c.GetEffectiveOrg(); got != "" {
		t.Errorf("anonymous: GetEffectiveOrg=%q want empty", got)
	}
}

// TestGetScopedOwnerCrossOrgDenied is the multi-tenant scoping invariant: a
// non-admin is ALWAYS pinned to their own org and cannot target another owner
// via the ?owner= param; only a global admin (owner=="admin") may.
func TestGetScopedOwnerCrossOrgDenied(t *testing.T) {
	// Non-admin attempts to target owner=victim → forced back to own org.
	c, _ := newControllerWithUser(&iam.User{Owner: "acme", Name: "alice"}, map[string]string{})
	c.Ctx.Request.Form = nil
	c.Ctx.Request.URL.RawQuery = "owner=victim-org"
	owner, ok := c.GetScopedOwner()
	if !ok {
		t.Fatal("signed-in non-admin should resolve a scoped owner")
	}
	if owner != "acme" {
		t.Fatalf("cross-org escalation: non-admin got owner=%q, want acme (own org)", owner)
	}

	// Global admin may target another owner.
	c, _ = newControllerWithUser(&iam.User{Owner: "admin", Name: "root", IsAdmin: true}, map[string]string{})
	c.Ctx.Request.URL.RawQuery = "owner=acme"
	owner, ok = c.GetScopedOwner()
	if !ok || owner != "acme" {
		t.Fatalf("admin targeting owner=acme: got (%q,%v), want (acme,true)", owner, ok)
	}
}
