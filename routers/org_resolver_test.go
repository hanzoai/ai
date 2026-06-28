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
	"testing"

	iam "github.com/hanzoai/iam"
)

// TestGetEffectiveOrgIgnoresSpoofedHeaderWhenUnauth is the core #8 assertion: an
// UNAUTHENTICATED caller cannot select a tenant via X-Org-Id on the direct
// ingress — the spoofed org is never returned.
func TestGetEffectiveOrgIgnoresSpoofedHeaderWhenUnauth(t *testing.T) {
	t.Setenv("IAM_ORG", "hanzo")
	ctx, _ := newFilterCtx("POST", "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Org-Id", "victim-org")

	if got := GetEffectiveOrg(ctx); got == "victim-org" {
		t.Fatal("spoofed X-Org-Id must NOT be honored without a verified principal")
	} else if got != "hanzo" {
		t.Errorf("unauth effective org = %q, want config default hanzo", got)
	}
}

// TestGetEffectiveOrgNonAdminScopedToOwnOrg: a non-admin's spoofed header is
// ignored — they are always scoped to their own org.
func TestGetEffectiveOrgNonAdminScopedToOwnOrg(t *testing.T) {
	user := &iam.User{Owner: "maxpower", Name: "dave", IsAdmin: true} // org admin, NOT global
	ctx, _ := newFilterCtx("POST", "/v1/chat/completions", user)
	ctx.Request.Header.Set("X-Org-Id", "victim-org")

	if got := GetEffectiveOrg(ctx); got != "maxpower" {
		t.Errorf("non-global principal effective org = %q, want own org maxpower (spoof ignored)", got)
	}
}

// TestGetEffectiveOrgOwnOrgHeaderHonored: a header matching the principal's own
// org is fine (the gateway-injection case).
func TestGetEffectiveOrgOwnOrgHeaderHonored(t *testing.T) {
	user := &iam.User{Owner: "maxpower", Name: "dave"}
	ctx, _ := newFilterCtx("POST", "/v1/chat/completions", user)
	ctx.Request.Header.Set("X-Org-Id", "maxpower")
	if got := GetEffectiveOrg(ctx); got != "maxpower" {
		t.Errorf("own-org header = %q, want maxpower", got)
	}
}

// TestGetEffectiveOrgGlobalAdminCrossOrg: only a global admin may target another
// org via the header (platform cross-org access).
func TestGetEffectiveOrgGlobalAdminCrossOrg(t *testing.T) {
	admin := &iam.User{Owner: "admin", Name: "admin", IsAdmin: true}
	ctx, _ := newFilterCtx("POST", "/v1/chat/completions", admin)
	ctx.Request.Header.Set("X-Org-Id", "some-tenant")
	if got := GetEffectiveOrg(ctx); got != "some-tenant" {
		t.Errorf("global-admin cross-org = %q, want some-tenant", got)
	}
}
