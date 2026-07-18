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

package routers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/ai/web"
)

func rewriteIam(method, target string) *web.Context {
	req := httptest.NewRequest(method, "https://console.hanzo.ai"+target, nil)
	ctx := web.NewContext()
	ctx.Reset(httptest.NewRecorder(), req)
	V1IamRewriteFilter(ctx)
	return ctx
}

func TestV1IamRewriteCanonicalizesAccountSurface(t *testing.T) {
	// Every account route is reachable under the organized /v1/iam/ namespace and
	// canonicalizes to its top-level handler path so the authz/balance gates (which
	// key off the bare controller name) see the exact path they already allow.
	cases := map[string]string{
		"/v1/iam/get-account":        "/v1/get-account",
		"/v1/iam/signin":             "/v1/signin",
		"/v1/iam/signout":            "/v1/signout",
		"/v1/iam/update-preferences": "/v1/update-preferences",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			if got := rewriteIam(http.MethodPost, in).Request.URL.Path; got != want {
				t.Errorf("path: got %q, want %q", got, want)
			}
		})
	}
}

func TestV1IamRewritePreservesQuery(t *testing.T) {
	// The OAuth code exchange rides the query string — it must survive the rewrite
	// on RequestURI (?code&state).
	ctx := rewriteIam(http.MethodPost, "/v1/iam/signin?code=abc&state=xyz")
	if got, want := ctx.Request.URL.Path, "/v1/signin"; got != want {
		t.Fatalf("path: got %q, want %q", got, want)
	}
	if got, want := ctx.Request.RequestURI, "/v1/signin?code=abc&state=xyz"; got != want {
		t.Errorf("RequestURI: got %q, want %q", got, want)
	}
}

func TestV1IamRewriteLeavesEverythingElseUntouched(t *testing.T) {
	// Not the account surface (a non-auth /v1/iam/* name) and the top-level paths
	// must pass through byte-for-byte — the filter owns ONLY the 4 account routes.
	for _, p := range []string{"/v1/iam/get-user", "/v1/get-account", "/v1/chat/completions", "/v1/iam/"} {
		t.Run(p, func(t *testing.T) {
			if got := rewriteIam(http.MethodGet, p).Request.URL.Path; got != p {
				t.Errorf("expected %q unchanged, got %q", p, got)
			}
		})
	}
}
