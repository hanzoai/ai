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
	"strings"

	"github.com/beego/beego/context"
)

// isJwtLike returns true if the token looks like a JWT (three dot-separated segments).
func isJwtLike(token string) bool {
	parts := strings.Split(token, ".")
	return len(parts) == 3 && len(parts[0]) > 10 && len(parts[1]) > 10
}

func AutoSigninFilter(ctx *context.Context) {
	urlPath := ctx.Request.URL.Path

	// Skip endpoints that handle their own auth (chat completions, models,
	// search/index/scrape, and /v1/ routes). These controllers validate
	// hk-*/pk-*/sk-* keys and JWTs directly, so the legacy MD5-based
	// access-token check here would incorrectly reject them.
	// Skip endpoints that handle their own auth (chat completions,
	// search/index/scrape, and /v1/ routes). These controllers validate
	// hk-*/pk-*/sk-* keys and JWTs directly, so the legacy MD5-based
	// access-token check here would incorrectly reject them.
	//
	// NOTE: /api/models was removed from this skip list (R-04 fix).
	// It now runs through AutoSigninFilter so session-based users are
	// resolved, and the controller validates Bearer tokens directly.
	if strings.HasSuffix(urlPath, "/chat/completions") ||
		strings.HasSuffix(urlPath, "/completions") ||
		strings.HasPrefix(urlPath, "/v1/") ||
		strings.HasPrefix(urlPath, "/api/search-docs") ||
		strings.HasPrefix(urlPath, "/api/index-docs") ||
		strings.HasPrefix(urlPath, "/api/chat-docs") ||
		strings.HasPrefix(urlPath, "/api/scrape-docs") {
		return
	}

	// Run for API paths and /storage paths only.
	if !strings.HasPrefix(urlPath, "/api/") && !strings.HasPrefix(urlPath, "/storage") {
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
		// IAM API keys (hk-*), publishable keys (pk-*), secret keys (sk-*),
		// and JWT tokens are validated by each controller's own auth logic.
		// Only legacy MD5-based access tokens should be handled here.
		if strings.HasPrefix(accessToken, "hk-") ||
			strings.HasPrefix(accessToken, "pk-") ||
			strings.HasPrefix(accessToken, "sk-") ||
			isJwtLike(accessToken) {
			return
		}

		userId, err := getUsernameByAccessToken(accessToken)
		if err != nil {
			responseError(ctx, err.Error())
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
		responseError(ctx, err.Error())
		return
	}
	if userId != "" {
		setSessionUser(ctx, userId)
		return
	}
}
