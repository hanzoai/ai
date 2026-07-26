// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2025 The OpenAgent Authors. All Rights Reserved.
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

// isJwtLike returns true if the token looks like a JWT (three dot-separated segments).
// apiKeyPrefixes are the opaque Hanzo credential prefixes.
//
// pk- is the publishable lookup handle and sk- is the secret half — those are
// the only two a credential can be, and the only two IAM mints.
//
// hk- is LEGACY-ACCEPTED, never minted. IAM stopped issuing it in v1.33.9, when
// the third prefix was retired. Keys handed out before that are still live, and
// resolution is an exact-value lookup rather than a prefix match, so they keep
// authenticating. Do NOT drop hk- from this list to "finish the migration" —
// that silently 401s every key issued before v1.33.9. It comes out only once no
// hk- key remains in the store.
var apiKeyPrefixes = []string{"pk-", "sk-", "hk-"}

// isOpaqueAPIKey reports whether token is one of our opaque API keys, as opposed
// to a JWT or a legacy MD5 access token. Named and shared so the rule lives in
// ONE place rather than as an inline prefix-OR at each call site.
func isOpaqueAPIKey(token string) bool {
	for _, p := range apiKeyPrefixes {
		if strings.HasPrefix(token, p) {
			return true
		}
	}
	return false
}

func isJwtLike(token string) bool {
	parts := strings.Split(token, ".")
	return len(parts) == 3 && len(parts[0]) > 10 && len(parts[1]) > 10
}

func AutoSigninFilter(ctx *web.Context) {
	urlPath := ctx.Request.URL.Path

	// Skip endpoints that handle their own auth (chat completions, models,
	// search/index/scrape, and /v1/ routes). These controllers validate
	// pk-/sk- keys (and legacy hk-) and JWTs directly, so the legacy MD5-based
	// access-token check here would incorrectly reject them.
	//
	// NOTE: /api/models was removed from this skip list (R-04 fix).
	// It now runs through AutoSigninFilter so session-based users are
	// resolved, and the controller validates Bearer tokens directly.
	if strings.HasSuffix(urlPath, "/chat/completions") ||
		strings.HasSuffix(urlPath, "/completions") ||
		strings.HasPrefix(urlPath, "/v1/") ||
		strings.HasPrefix(urlPath, "/v1/search-docs") ||
		strings.HasPrefix(urlPath, "/v1/index-docs") ||
		strings.HasPrefix(urlPath, "/v1/chat-docs") ||
		strings.HasPrefix(urlPath, "/v1/scrape-docs") {
		return
	}

	// Run for API paths and /storage paths only.
	if !strings.HasPrefix(urlPath, "/v1/") && !strings.HasPrefix(urlPath, "/storage") {
		return
	}

	// HTTP Bearer token like "Authorization: Bearer 123"
	accessToken := ctx.Input.Query("accessToken")
	if accessToken == "" {
		accessToken = ctx.Input.Query("access_token")
	}
	if accessToken == "" {
		accessToken = parseBearerToken(ctx)
	}
	if accessToken != "" {
		// Opaque Hanzo keys and JWTs are validated by each controller's own auth
		// logic. Only legacy MD5-based access tokens are handled here.
		if isOpaqueAPIKey(accessToken) || isJwtLike(accessToken) {
			return
		}

		userId, err := getUsernameByAccessToken(accessToken)
		if err != nil {
			denyUnauthorized(ctx, err.Error())
			return
		}

		if userId != "" {
			setSessionUser(ctx, userId)
			return
		}
	}

	// HTTP Basic token like "Authorization: Basic 123"
	userId, err := getUsernameByClientIdSecret(ctx)
	if err != nil {
		denyUnauthorized(ctx, err.Error())
		return
	}
	if userId != "" {
		setSessionUser(ctx, userId)
		return
	}
}
