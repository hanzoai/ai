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
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/beego/beego/context"
	"github.com/hanzoai/cloud/conf"
	"github.com/hanzoai/cloud/object"
	iamsdk "github.com/hanzoid/go-sdk/casdoorsdk"
)

const (
	headerOrigin           = "Origin"
	headerAllowOrigin      = "Access-Control-Allow-Origin"
	headerAllowMethods     = "Access-Control-Allow-Methods"
	headerAllowHeaders     = "Access-Control-Allow-Headers"
	headerAllowCredentials = "Access-Control-Allow-Credentials"
	headerExposeHeaders    = "Access-Control-Expose-Headers"
)

// allowedOriginSuffixes is the static allowlist of trusted origin domain
// suffixes. An origin is allowed if its hostname equals or is a subdomain of
// one of these entries. This list is evaluated BEFORE the dynamic IAM
// RedirectUri check so that first-party origins always pass even when IAM is
// unreachable.
var allowedOriginSuffixes = []string{
	"hanzo.ai",
	"hanzo.app",
	"hanzo.bot",
	"hanzo.chat",
	"hanzo.id",
	"hanzo.agency",
	"hanzo.industries",
	"lux.network",
	"zoo.ngo",
	"zenlm.org",
}

// isStaticAllowedOrigin checks the origin against the hard-coded allowlist
// and also permits any localhost/127.0.0.1 origin (for local development).
func isStaticAllowedOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	host := u.Hostname() // strips port

	// Allow localhost / 127.0.0.1 for local dev regardless of port
	if host == "localhost" || host == "127.0.0.1" {
		return true
	}

	for _, suffix := range allowedOriginSuffixes {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

func setCorsHeaders(ctx *context.Context, origin string) {
	// Skip if the upstream proxy (KrakenD gateway) already set CORS headers
	// to avoid duplicate Access-Control-Allow-Origin which browsers reject.
	if existing := ctx.ResponseWriter.Header().Get(headerAllowOrigin); existing != "" {
		return
	}
	ctx.Output.Header(headerAllowOrigin, origin)
	ctx.Output.Header(headerAllowMethods, "GET, POST, DELETE, PUT, PATCH, OPTIONS")
	ctx.Output.Header(
		headerAllowHeaders,
		"Origin, X-Requested-With, Content-Type, Accept, Authorization, X-IAM-Org-Id, X-IAM-User-Id, X-IAM-User-Email, X-IAM-Project-Id, X-IAM-Env, X-API-Version, X-SDK-Name, X-SDK-Version",
	)
	ctx.Output.Header(headerExposeHeaders, "Content-Length")
	ctx.Output.Header(headerAllowCredentials, "true")

	if ctx.Input.Method() == "OPTIONS" {
		ctx.ResponseWriter.WriteHeader(http.StatusOK)
	}
}

func CorsFilter(ctx *context.Context) {
	origin := ctx.Input.Header(headerOrigin)

	// Reject empty and literal "null" origins (sandboxed iframes, data: URIs).
	if origin == "" {
		return
	}
	if origin == "null" {
		ctx.ResponseWriter.WriteHeader(http.StatusForbidden)
		responseError(ctx, "CORS error: null origin is not allowed")
		return
	}

	// 1. Static allowlist — always works, even when IAM is down.
	if isStaticAllowedOrigin(origin) {
		setCorsHeaders(ctx, origin)
		if object.CloudHost == "" {
			object.CloudHost = origin
		}
		return
	}

	// 2. Widget keys (hz_*) are public credentials validated by the gateway's
	// widget security middleware (origin + rate limit). They don't use IAM
	// OAuth flows, so skip the RedirectUri-based origin check.
	if token := parseBearerToken(ctx); strings.HasPrefix(token, "hz_") {
		setCorsHeaders(ctx, origin)
		return
	}

	// 3. Dynamic check via IAM application RedirectUris.
	ok, err := isOriginAllowed(origin)
	if err != nil {
		// If IAM is not configured at all, reject — no more open fallback.
		ctx.ResponseWriter.WriteHeader(http.StatusForbidden)
		responseError(ctx, fmt.Sprintf("CORS error: %s, path: %s", err.Error(), ctx.Request.URL.Path))
		return
	}

	if !ok {
		ctx.ResponseWriter.WriteHeader(http.StatusForbidden)
		responseError(ctx, fmt.Sprintf("CORS error: origin [%s] is not allowed, path: %s", origin, ctx.Request.URL.Path))
		return
	}

	setCorsHeaders(ctx, origin)
	if object.CloudHost == "" {
		object.CloudHost = origin
	}
}

func isOriginAllowed(origin string) (bool, error) {
	iamEndpoint := conf.GetConfigString("iamEndpoint")
	iamApplication := conf.GetConfigString("iamApplication")

	if iamEndpoint == "" || iamApplication == "" {
		return false, fmt.Errorf("iamEndpoint or iamApplication is empty")
	}

	application, err := iamsdk.GetApplication(iamApplication)
	if err != nil {
		return false, err
	}
	if application == nil {
		return false, fmt.Errorf("The application: %s does not exist", iamApplication)
	}

	// Check if origin matches any RedirectUri
	for _, redirectUri := range application.RedirectUris {
		parsedUrl, err := url.Parse(redirectUri)
		if err != nil {
			continue
		}
		allowedOrigin := parsedUrl.Scheme + "://" + parsedUrl.Host
		if origin == allowedOrigin {
			return true, nil
		}
	}

	return false, nil
}
