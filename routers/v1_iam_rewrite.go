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

	"github.com/hanzoai/ai/web"
)

// iamAccountRoutes is the auth/account surface: the endpoints that establish or
// read the session. They belong under the organized /v1/iam/ namespace (one
// place for auth), NOT scattered at the /v1/ top level.
//
// Their canonical HANDLER path stays top-level because every request filter
// (authz_filter, filter_balance, demo mode) keys its policy off the bare
// controller name — `strings.TrimPrefix(path, "/v1/")`. Serving them at
// /v1/iam/<name> is therefore a single canonicalization here, BEFORE those
// gates run — not a second copy of every route, and not a per-filter policy
// fork that could silently drift (e.g. a missed balance exemption 402-ing a
// zero-credit user on their own get-account).
var iamAccountRoutes = map[string]struct{}{
	"signin":             {},
	"signout":            {},
	"get-account":        {},
	"update-preferences": {},
}

// V1IamRewriteFilter canonicalizes the organized auth namespace
// /v1/iam/<account> → /v1/<account> for the account surface, so /v1/iam/ is the
// ONE way the consoles reach auth while the proven authz/balance gates keep
// operating unchanged on the canonical path (one policy, zero duplication).
//
// Mirrors V1CloudRewriteFilter; registered FIRST in the BeforeRouter chain so
// AutoSigninFilter, BalanceGateFilter and AuthzFilter all see the canonical
// path.
func V1IamRewriteFilter(ctx *web.Context) {
	const prefix = "/v1/iam/"
	path := ctx.Request.URL.Path
	if !strings.HasPrefix(path, prefix) {
		return
	}
	if _, ok := iamAccountRoutes[strings.TrimPrefix(path, prefix)]; !ok {
		return
	}
	newPath := "/v1/" + strings.TrimPrefix(path, prefix)
	ctx.Request.URL.Path = newPath
	ctx.Request.RequestURI = newPath
	if ctx.Request.URL.RawQuery != "" {
		ctx.Request.RequestURI = newPath + "?" + ctx.Request.URL.RawQuery
	}
}
