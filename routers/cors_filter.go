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
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/web"
	iam "github.com/hanzoai/iam-v1"
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
	"hanzo.cloud",
	"lux.network",
	"lux.id",
	"lux.cloud",
	"zoo.ngo",
	"zoo.network",
	"zoo.id",
	"zoo.cloud",
	"pars.id",
	"pars.ai",
	"pars.cloud",
	"bootno.de",
	"ad.nexus",
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

func setCorsHeaders(ctx *web.Context, origin string) {
	// Skip CORS when behind the KrakenD API gateway, which adds its own
	// CORS headers.  KrakenD forwards requests with X-Forwarded-Host set;
	// direct requests to the cloud-api service don't have this header.
	if ctx.Input.Header("X-Forwarded-Host") != "" {
		if ctx.Input.Method() == "OPTIONS" {
			ctx.ResponseWriter.WriteHeader(http.StatusOK)
		}
		return
	}
	ctx.Output.Header(headerAllowOrigin, origin)
	ctx.Output.Header(headerAllowMethods, "GET, POST, DELETE, PUT, PATCH, OPTIONS")
	ctx.Output.Header(
		headerAllowHeaders,
		"Origin, X-Requested-With, Content-Type, Accept, Authorization, X-Org-Id, X-Project-Id, X-Environment, X-User-Id, X-User-Email, X-API-Version, X-SDK-Name, X-SDK-Version",
	)
	ctx.Output.Header(headerExposeHeaders, "Content-Length")
	ctx.Output.Header(headerAllowCredentials, "true")

	if ctx.Input.Method() == "OPTIONS" {
		ctx.ResponseWriter.WriteHeader(http.StatusOK)
	}
}

func CorsFilter(ctx *web.Context) {
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
	iamEndpoint := conf.GetConfigString("IAM_URL")
	iamApplication := conf.GetConfigString("IAM_APP_NAME")

	if iamEndpoint == "" || iamApplication == "" {
		return false, fmt.Errorf("IAM_URL or IAM_APP_NAME is empty")
	}

	application, err := iam.GetApplication(iamApplication)
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
