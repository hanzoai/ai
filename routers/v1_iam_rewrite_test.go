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

	"github.com/beego/beego/context"
)

func rewriteCtx(method, target string) *context.Context {
	req := httptest.NewRequest(method, target, nil)
	resp := httptest.NewRecorder()
	ctx := context.NewContext()
	ctx.Reset(resp, req)
	return ctx
}

// The account surface the console addresses under /v1/iam/ must resolve to the
// canonical /v1/<endpoint> handler — path AND RequestURI (query preserved).
func TestV1IamRewrite_AccountSurface(t *testing.T) {
	cases := []struct {
		method, in, wantPath, wantURI string
	}{
		{http.MethodGet, "https://console.hanzo.ai/v1/iam/get-account", "/v1/get-account", "/v1/get-account"},
		{http.MethodPost, "https://console.hanzo.ai/v1/iam/signout", "/v1/signout", "/v1/signout"},
		{http.MethodPost, "https://console.hanzo.ai/v1/iam/update-preferences", "/v1/update-preferences", "/v1/update-preferences"},
		// signin carries the OAuth code+state in the query — it MUST survive.
		{
			http.MethodPost,
			"https://console.hanzo.ai/v1/iam/signin?code=abc123&state=hanzo-console",
			"/v1/signin",
			"/v1/signin?code=abc123&state=hanzo-console",
		},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			ctx := rewriteCtx(c.method, c.in)
			V1IamRewriteFilter(ctx)
			if got := ctx.Request.URL.Path; got != c.wantPath {
				t.Errorf("URL.Path = %q, want %q", got, c.wantPath)
			}
			if got := ctx.Request.RequestURI; got != c.wantURI {
				t.Errorf("RequestURI = %q, want %q", got, c.wantURI)
			}
		})
	}
}

// Identity-management endpoints under /v1/iam/ are the IAM service's concern and
// must be left UNTOUCHED — cloud must never shadow them via casibase.
func TestV1IamRewrite_LeavesIamServiceEndpoints(t *testing.T) {
	untouched := []string{
		"https://console.hanzo.ai/v1/iam/get-user?id=hanzo/dave",
		"https://console.hanzo.ai/v1/iam/mint-user-keys",
		"https://console.hanzo.ai/v1/iam/issue-user-token",
		"https://console.hanzo.ai/v1/iam/oauth/access_token",
		"https://console.hanzo.ai/v1/iam/get-organizations",
	}
	for _, in := range untouched {
		t.Run(in, func(t *testing.T) {
			ctx := rewriteCtx(http.MethodGet, in)
			before := ctx.Request.URL.Path
			V1IamRewriteFilter(ctx)
			if got := ctx.Request.URL.Path; got != before {
				t.Errorf("URL.Path rewritten to %q, want it left as %q", got, before)
			}
		})
	}
}

// A canonical top-level account call (no /iam/ prefix) is a no-op — the filter
// only normalizes the organized prefix, never the canonical form.
func TestV1IamRewrite_NoopOnCanonicalPaths(t *testing.T) {
	for _, in := range []string{
		"https://console.hanzo.ai/v1/get-account",
		"https://console.hanzo.ai/v1/signin?code=x&state=y",
		"https://console.hanzo.ai/v1/chat/completions",
	} {
		t.Run(in, func(t *testing.T) {
			ctx := rewriteCtx(http.MethodGet, in)
			before := ctx.Request.URL.Path
			V1IamRewriteFilter(ctx)
			if got := ctx.Request.URL.Path; got != before {
				t.Errorf("URL.Path = %q, want unchanged %q", got, before)
			}
		})
	}
}
