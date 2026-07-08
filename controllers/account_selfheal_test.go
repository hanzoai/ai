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
	"testing"

	iam "github.com/hanzoai/iam"
)

// TestResolveAccountAction pins the get-account identity policy: a resolved
// subject (real, or self-healed from a verified IAM credential) ALWAYS gets its
// canonical identity — never an anonymous u-<hash> record — and an unresolved
// identity on a protected surface fails LOUD instead of fabricating a guest.
func TestResolveAccountAction(t *testing.T) {
	realUser := &iam.User{Owner: "hanzo", Name: "z", IsAdmin: true}
	cases := []struct {
		name             string
		sessionUser      *iam.User
		anonymousAllowed bool
		want             accountAction
	}{
		{"real subject, public surface -> canonical identity", realUser, true, serveIdentity},
		{"real subject, protected surface -> canonical identity", realUser, false, serveIdentity},
		{"no identity, public/preview surface -> anonymous", nil, true, serveAnonymous},
		{"no identity, protected surface -> fail loud", nil, false, failUnauthenticated},
	}
	for _, tc := range cases {
		if got := resolveAccountAction(tc.sessionUser, tc.anonymousAllowed); got != tc.want {
			t.Errorf("%s: resolveAccountAction = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestBearerTokenFromRequestCookieFallback verifies the self-heal credential
// source: the hanzo_iam_token cookie is honored when no Authorization header is
// present (the browser cookie-session case), the header still wins when both are
// present, and a credential-less request yields no token.
func TestBearerTokenFromRequestCookieFallback(t *testing.T) {
	const headerTok = "hdr.jwt.sig"
	const cookieTok = "cookie.jwt.sig"

	// Header present: header wins even if the cookie is also set.
	r := httptest.NewRequest(http.MethodGet, "/v1/get-account", nil)
	r.Header.Set("Authorization", "Bearer "+headerTok)
	r.AddCookie(&http.Cookie{Name: iamTokenCookieName, Value: cookieTok})
	if got := bearerTokenFromRequest(r); got != headerTok {
		t.Errorf("header precedence: got %q, want %q", got, headerTok)
	}

	// Cookie only (no Authorization header): the self-heal path must resolve it.
	r2 := httptest.NewRequest(http.MethodGet, "/v1/get-account", nil)
	r2.AddCookie(&http.Cookie{Name: iamTokenCookieName, Value: cookieTok})
	if got := bearerTokenFromRequest(r2); got != cookieTok {
		t.Errorf("cookie fallback: got %q, want %q (a cookie-only browser must still carry a credential)", got, cookieTok)
	}

	// Neither: no credential.
	r3 := httptest.NewRequest(http.MethodGet, "/v1/get-account", nil)
	if got := bearerTokenFromRequest(r3); got != "" {
		t.Errorf("no credential: got %q, want empty", got)
	}
}
