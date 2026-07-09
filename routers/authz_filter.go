// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2023 The OpenAgent Authors. All Rights Reserved.
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
	"path"
	"strings"

	"github.com/beego/beego/context"
	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/controllers"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
	iam "github.com/hanzoai/iam"
)

func AuthzFilter(ctx *context.Context) {
	method := ctx.Request.Method
	urlPath := ctx.Request.URL.Path

	// NOTE: there is deliberately NO adminDomain Host bypass here. A
	// "Host == adminDomain → return before the gates" shortcut (an upstream
	// artifact) is a latent full-authz-bypass primitive — one ConfigMap edit from
	// CRITICAL, and trivially reachable via a spoofed Host header. The authz gates
	// below run for EVERY request, regardless of Host.

	if conf.IsDemoMode() {
		if !isAllowedInDemoMode(method, urlPath) {
			controllers.DenyRequest(ctx)
		}
	}
	permissionFilter(ctx)
}

func isAllowedInDemoMode(method string, urlPath string) bool {
	if method != "POST" {
		return true
	}

	if strings.HasPrefix(urlPath, "/v1/signin") || urlPath == "/v1/signout" || urlPath == "/v1/add-chat" || urlPath == "/v1/add-message" || urlPath == "/v1/update-message" || urlPath == "/v1/delete-welcome-message" || urlPath == "/v1/generate-text-to-speech-audio" || urlPath == "/v1/add-node-tunnel" || urlPath == "/v1/start-connection" || urlPath == "/v1/stop-connection" || urlPath == "/v1/commit-record" || urlPath == "/v1/commit-record-second" || urlPath == "/v1/update-chat" || urlPath == "/v1/delete-chat" || urlPath == "/v1/search-docs" || urlPath == "/v1/chat-docs" {
		return true
	}

	return false
}

// globalAdminEndpoints are platform-sensitive operations — they expose or mutate
// upstream provider config (which holds upstream API keys), model routing,
// storage credentials, or cluster topology. They ALWAYS require a GLOBAL admin
// (util.IsGlobalAdmin) and are NEVER relaxed by preview mode or the benign-read
// exempt list. This closes two issues at once:
//   - preview-mode default-open: with disablePreviewMode=false (the default) the
//     old filter let ALL get-* through, disclosing provider/topology config to
//     unauthenticated callers.
//   - org-admin-as-platform-admin: an org owner (IsAdmin within their own org)
//     could read/modify platform provider config.
//
// get-models is deliberately NOT here — it is the public model catalog and stays
// reachable (it has its own per-request auth in ListModels).
var globalAdminEndpoints = map[string]struct{}{
	// Upstream provider config (holds upstream API keys).
	"get-providers": {}, "get-provider": {}, "get-global-providers": {},
	"add-provider": {}, "update-provider": {}, "delete-provider": {},
	"refresh-mcp-tools": {},
	// Provider-admin management surface. These mutate/expose provider enable +
	// primary state (which governs routing to upstream keys), so they are gated
	// exactly like the CRUD routes above. Keys are the controllerName produced by
	// TrimPrefix(path,"/v1/"), so multi-segment paths appear verbatim. NOTE:
	// "provider-flags" is deliberately NOT here — it is the public, secret-free
	// enabled-name feed for the pricing sync (get-models-style public read).
	"admin/providers": {}, "admin/providers/toggle": {}, "admin/providers/primary": {},
	// Model routing config.
	"get-model-routes": {}, "get-model-route": {},
	"add-model-route": {}, "update-model-route": {}, "delete-model-route": {},
	"admin/reload-model-config": {}, "admin/refresh-model-pricing": {},
	// Per-org feature settings (auto-routing enable/disable, …).
	"get-org-settings-list": {}, "get-org-settings": {},
	"add-org-settings": {}, "update-org-settings": {}, "delete-org-settings": {},
	// Routing-decision ledger export (training data — platform-wide).
	"export-routing-ledger": {},
	// Storage provider credentials.
	"get-storage-providers": {},
	// Cluster topology / infrastructure.
	"get-nodes": {}, "get-node": {}, "add-node": {}, "update-node": {}, "delete-node": {},
	"get-machines": {}, "get-machine": {}, "add-machine": {}, "update-machine": {}, "delete-machine": {},
	"get-pods": {}, "get-pod": {}, "add-pod": {}, "update-pod": {}, "delete-pod": {},
	"get-containers": {}, "get-container": {}, "add-container": {}, "update-container": {}, "delete-container": {},
	"get-images": {}, "get-image": {}, "add-image": {}, "update-image": {}, "delete-image": {},
	"get-k8s-status": {},
}

func requiresGlobalAdmin(controllerName string) bool {
	_, ok := globalAdminEndpoints[controllerName]
	return ok
}

// authRequiredEndpoints are the write / ingest / scrape / RAG endpoints that
// self-authenticate in-controller AND must never be reachable by a fully
// anonymous request. permissionFilter fails closed (401) when NO credential is
// present; the controller does the authoritative validation. Kept as an
// explicit set — plus the rag/ and documents/ prefixes below — so the many
// benign self-authing or intentionally-anonymous paths (chat, models, memory,
// health, metrics, wecom) are unaffected. Names are the path minus "/v1/".
var authRequiredEndpoints = map[string]struct{}{
	"scrape": {}, "scrape/preview": {}, // browser/crawl engine (SSRF + cost)
	"index": {}, "search": {}, "search/stats": {}, // doc index write + search
	"docs/ingest": {},                                    // unified RAG ingest (github/crawl/s3)
	"embed":       {}, "query": {}, "query_multiple": {}, // librechat-compat RAG
	"documents":            {}, // librechat-compat DELETE documents
	"get-routing-defaults": {}, // per-caller routing defaults — any authenticated user, never anonymous
}

// requiresPresentCredential reports whether controllerName is a write/ingest/
// scrape/RAG endpoint that must fail closed for an anonymous caller. It matches
// the explicit set plus the native /v1/rag/* family, the librechat-compat
// /v1/documents/{id}/context read, and the AI login-manager /v1/ai/connections*
// family (org-scoped: a present credential is required at the filter; the
// controller does the authoritative per-org check — NOT a global-admin gate).
func requiresPresentCredential(controllerName string) bool {
	if _, ok := authRequiredEndpoints[controllerName]; ok {
		return true
	}
	return strings.HasPrefix(controllerName, "rag/") ||
		strings.HasPrefix(controllerName, "documents/") ||
		controllerName == "ai/connections" ||
		strings.HasPrefix(controllerName, "ai/connections/")
}

// hasPresentCredential reports whether the request carries SOME credential — a
// session user (cookie auth) or a Bearer token. It is a coarse presence check
// (defense in depth); the controller validates the credential. It deliberately
// does not parse/verify, so it never rejects a valid credential type
// (hk-/pk-/sk-/hz_/JWT/session) and never adds an IAM round-trip to the filter.
func hasPresentCredential(ctx *context.Context) bool {
	if GetSessionUser(ctx) != nil {
		return true
	}
	return parseBearerToken(ctx) != ""
}

// sessionOrBearerUser resolves the request principal for the authz gate: the
// session user (cookie auth) if present, else the VERIFIED Bearer JWT user.
// AutoSigninFilter no-ops for /v1/ paths, so a console call that authenticates
// with a Bearer JWT (no cookie) would otherwise present no principal here — this
// resolves it. The JWT is signature- AND issuer/audience-validated via
// object.ParseAndValidateJWT (never raw iam.ParseJwtToken), so a forged token
// cannot pose as an admin.
func sessionOrBearerUser(ctx *context.Context) *iam.User {
	if u := GetSessionUser(ctx); u != nil {
		return u
	}
	token := parseBearerToken(ctx)
	if token == "" || !isJwtLike(token) {
		return nil
	}
	claims, err := object.ParseAndValidateJWT(token)
	if err != nil {
		return nil
	}
	return &claims.User
}

// normalizedControllerName derives the controllerName the gate keys on from the
// SAME normalized path Beego dispatches to. Beego path.Cleans the request path
// before router matching (collapsing "//", "/./", "/../" and a trailing slash),
// so a filter that keyed on the RAW path (strings.TrimPrefix of ctx.Request.URL.Path)
// disagreed with the router: variants like "/v1/admin/providers/",
// "/v1//admin/providers", "/v1/./admin/providers" and "/v1/admin/../admin/providers"
// all dispatch to the gated controller yet, un-normalized, produced a controllerName
// ("admin/providers/", …) that missed the globalAdminEndpoints map — falling through
// to the fully-open default. Cleaning here makes the gate and the router agree on
// ONE canonical name, closing the entire slash/dot variant set for every gated
// endpoint (admin/providers*, the get-*/*-provider CRUD, topology reads).
//
// Returns ok=false only for non-/v1 paths (which the caller lets pass, unchanged).
func normalizedControllerName(rawPath string) (name string, ok bool) {
	// path.Clean resolves ".", ".." and duplicate slashes on the absolute request
	// path exactly as Beego does before dispatch. path.Clean("/v1/admin/providers/")
	// == "/v1/admin/providers"; path.Clean("/v1//admin/providers") == "/v1/admin/providers".
	cleaned := path.Clean(rawPath)
	if !strings.HasPrefix(cleaned, "/v1/") {
		return "", false
	}
	return strings.TrimPrefix(cleaned, "/v1/"), true
}

func permissionFilter(ctx *context.Context) {
	controllerName, ok := normalizedControllerName(ctx.Request.URL.Path)
	if !ok {
		return
	}

	// Platform-sensitive endpoints are gated FIRST — before the preview-mode
	// bypass and the benign-read exempt list — so they are admin-gated regardless
	// of configuration. Fail-secure: no principal => 401, wrong principal => 403.
	if requiresGlobalAdmin(controllerName) {
		user := sessionOrBearerUser(ctx)
		if user == nil {
			denyUnauthorized(ctx, "auth:authentication required")
		} else if !util.IsGlobalAdmin(user) {
			denyForbidden(ctx, "auth:this operation requires global admin privilege")
		}
		return
	}

	// Write / ingest / scrape / RAG endpoints self-authenticate in their
	// controllers (requireIndexAuth / resolveSearchAuth), but the central filter
	// ALSO fails closed here so a fully-anonymous request can never reach them —
	// defense in depth for index-write (retrieval-poisoning / document deletion),
	// scrape (SSRF + cost), and RAG. Previously these fell through the "neither
	// get- nor update-" branch below with NO central gate. A present credential
	// (Bearer OR session) is required here; the controller performs the
	// authoritative validation. This is an explicit set, NOT a blanket default,
	// so health/metrics/wecom/memory stay reachable without a Bearer.
	if requiresPresentCredential(controllerName) && !hasPresentCredential(ctx) {
		denyUnauthorized(ctx, "auth:authentication required")
		return
	}

	disablePreviewMode := conf.DisablePreviewMode()

	isUpdateRequest := strings.HasPrefix(controllerName, "update-") || strings.HasPrefix(controllerName, "add-") || strings.HasPrefix(controllerName, "delete-") || strings.HasPrefix(controllerName, "refresh-") || strings.HasPrefix(controllerName, "deploy-")
	isGetRequest := strings.HasPrefix(controllerName, "get-")

	if !disablePreviewMode && isGetRequest {
		return
	}
	if !isGetRequest && !isUpdateRequest {
		return
	}

	exemptedPaths := []string{
		"get-account", "get-chats", "get-forms", "get-global-videos", "get-videos", "get-video", "get-messages",
		"delete-welcome-message", "get-message-answer", "get-answer",
		"get-store", "get-global-stores",
		"update-chat", "add-chat", "delete-chat", "update-message", "add-message",
		"get-chat", "get-message",
		"get-tasks", "get-task", "get-public-scales", "update-task", "add-task", "delete-task", "upload-task-document",
		"search-docs", "chat-docs", "search-docs/stats",
		// update-preferences is signed-in but NOT admin-gated: it is self-scoped
		// (writes only the caller's own IAM-user properties, per the session).
		"update-preferences",
	}

	for _, exemptPath := range exemptedPaths {
		if controllerName == exemptPath {
			return
		}
	}

	user := GetSessionUser(ctx)

	if !util.IsAdmin(user) {
		denyForbidden(ctx, "auth:this operation requires admin privilege")
	}
}
