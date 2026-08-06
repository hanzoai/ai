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

// TestGetOrgIgnoresSpoofedHeaderWhenUnauth is the core #8 assertion: an
// UNAUTHENTICATED caller cannot select a tenant via X-Org-Id on the direct
// ingress — the spoofed org is never returned.
func TestGetOrgIgnoresSpoofedHeaderWhenUnauth(t *testing.T) {
	t.Setenv("IAM_ORG", "hanzo")
	ctx, _ := newFilterCtx("POST", "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Org-Id", "victim-org")

	if got := GetOrg(ctx); got == "victim-org" {
		t.Fatal("spoofed X-Org-Id must NOT be honored without a verified principal")
	} else if got != "hanzo" {
		t.Errorf("unauth effective org = %q, want config default hanzo", got)
	}
}

// TestGetOrgNonAdminScopedToOwnOrg: a non-admin's spoofed header is
// ignored — they are always scoped to their own org.
func TestGetOrgNonAdminScopedToOwnOrg(t *testing.T) {
	user := &iam.User{Owner: "maxpower", Name: "dave", IsAdmin: true} // org admin, NOT global
	ctx, _ := newFilterCtx("POST", "/v1/chat/completions", user)
	ctx.Request.Header.Set("X-Org-Id", "victim-org")

	if got := GetOrg(ctx); got != "maxpower" {
		t.Errorf("non-global principal effective org = %q, want own org maxpower (spoof ignored)", got)
	}
}

// TestGetOrgOwnOrgHeaderHonored: a header matching the principal's own
// org is fine (the gateway-injection case).
func TestGetOrgOwnOrgHeaderHonored(t *testing.T) {
	user := &iam.User{Owner: "maxpower", Name: "dave"}
	ctx, _ := newFilterCtx("POST", "/v1/chat/completions", user)
	ctx.Request.Header.Set("X-Org-Id", "maxpower")
	if got := GetOrg(ctx); got != "maxpower" {
		t.Errorf("own-org header = %q, want maxpower", got)
	}
}

// TestGetOrgSuperAdminCrossOrg: only a super admin may target another
// org via the header (platform cross-org access).
func TestGetOrgSuperAdminCrossOrg(t *testing.T) {
	admin := &iam.User{Owner: "admin", Name: "admin", IsAdmin: true}
	ctx, _ := newFilterCtx("POST", "/v1/chat/completions", admin)
	ctx.Request.Header.Set("X-Org-Id", "some-tenant")
	if got := GetOrg(ctx); got != "some-tenant" {
		t.Errorf("super-admin cross-org = %q, want some-tenant", got)
	}
}

// TestHkKeyTakesAccessKeyResolutionPath locks in the `hanzo code` 402 fix: an
// hk- IAM API key is NOT JWT-like, so sessionOrBearerUser must route it to the
// access-key resolver (GetUserByAccessKey), not the JWT parser. Before the fix
// it fell straight through to nil — GetOrg then returned the IAM_ORG default and
// every chat call 402'd at zen ("a billable tenant is required"). Here IAM is
// unconfigured, so resolution fails closed to nil and GetOrg falls back to the
// configured default rather than a spoofed header — proving both the routing and
// the fail-secure guard on an unresolvable key.
func TestHkKeyTakesAccessKeyResolutionPath(t *testing.T) {
	if isJwtLike("hk-902abd8e-9f5e-49c0-baf7-220f771d299f") {
		t.Fatal("an hk- API key must NOT classify as JWT-like (it has no dot-separated segments)")
	}
	t.Setenv("IAM_ORG", "hanzo")
	t.Setenv("IAM_URL", "") // unconfigured → GetUserByAccessKey errors → nil principal
	ctx, _ := newFilterCtxAuth("POST", "/v1/chat/completions", "hk-902abd8e-9f5e-49c0-baf7-220f771d299f")
	ctx.Request.Header.Set("X-Org-Id", "victim-org")
	if got := GetOrg(ctx); got == "victim-org" {
		t.Fatal("an unresolvable hk- key must NOT let a spoofed X-Org-Id through (fail-secure)")
	} else if got != "hanzo" {
		t.Errorf("unresolvable hk- key effective org = %q, want config default hanzo", got)
	}
}
