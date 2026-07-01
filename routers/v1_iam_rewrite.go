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
	"strings"

	"github.com/beego/beego/context"
)

// iamAccountSurface is the set of session/account endpoints the Hanzo Cloud
// console addresses under the organized /v1/iam/ namespace. Each maps to the
// SAME handler already registered at /v1/<name> in router.go — so the cloud
// session cookie (cloud_session_id), the OAuth code+state exchange, and the CSRF
// state check are the one working flow, reachable under either prefix.
//
// This is deliberately a fixed allowlist, NOT a blanket /v1/iam/* strip: cloud
// owns ONLY the account surface here. Identity management (get-user,
// mint-user-keys, issue-user-token, oauth, jwks, …) is the separate IAM service's
// concern at hanzo.id/v1/iam/* and must never be shadowed by this alias.
var iamAccountSurface = map[string]struct{}{
	"signin":             {},
	"signout":            {},
	"get-account":        {},
	"update-preferences": {},
}

// V1IamRewriteFilter rewrites /v1/iam/<account-endpoint> to /v1/<account-endpoint>
// so the console's organized account surface resolves to the canonical handlers
// WITHOUT duplicating every route in router.go — the twin of V1CloudRewriteFilter.
//
// It is registered FIRST in the BeforeRouter chain (right after
// V1CloudRewriteFilter, before AutoSignin/BalanceGate/Authz), so every downstream
// filter's path allowlist (the authz demo-mode signin exemption and the
// balance-gate signin/signout/get-account exemptions) and the beego router all
// see the canonical /v1/<endpoint> path — the exchange, the cookie, and the state
// check run exactly as they do for a direct /v1/<endpoint> call.
//
// Example: /v1/iam/signin?code=…&state=… → /v1/signin?code=…&state=…
func V1IamRewriteFilter(ctx *context.Context) {
	path := ctx.Request.URL.Path
	rest := strings.TrimPrefix(path, "/v1/iam/")
	if rest == path {
		return // not under /v1/iam/ (filter is scoped to /v1/iam/*, but stay defensive)
	}
	if _, ok := iamAccountSurface[rest]; !ok {
		return // an identity-management path — leave it for the IAM service
	}

	newPath := "/v1/" + rest
	ctx.Request.URL.Path = newPath
	ctx.Request.RequestURI = newPath
	if ctx.Request.URL.RawQuery != "" {
		ctx.Request.RequestURI = newPath + "?" + ctx.Request.URL.RawQuery
	}
}
