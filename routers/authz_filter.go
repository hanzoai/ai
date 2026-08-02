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

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/controllers"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
	"github.com/hanzoai/ai/web"
)

func AuthzFilter(ctx *web.Context) {
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

// demoModeAllowed is what a demo visitor may change: talk to the assistant, and
// drive the few things a conversation needs. Everything else that WRITES is
// denied.
//
// Keys are policy names, not URLs, so they are the same strings the super-admin
// gate uses and they follow a resource when its route changes.
var demoModeAllowed = map[string]struct{}{
	"signout": {},
	// A conversation and its messages.
	"add-chat": {}, "update-chat": {}, "delete-chat": {},
	"add-message": {}, "update-message": {}, "delete-welcome-message": {},
	// Retrieval and speech used by that conversation.
	"search-docs": {}, "chat-docs": {}, "generate-text-to-speech-audio": {},
	// Session plumbing a live demo needs.
	"add-node-tunnel": {}, "start-connection": {}, "stop-connection": {},
	"commit-record": {}, "commit-record-second": {},
}

// isAllowedInDemoMode reports whether a demo visitor may make this request.
//
// EVERY mutating method is checked, not just POST. That distinction used to be
// invisible because every write was a POST; once updates became PATCH and
// deletes became DELETE, a "POST-only" check would have waved through the delete
// of any resource in the system — a demo visitor emptying the provider list.
// Reads stay open, which is the point of a demo.
func isAllowedInDemoMode(method string, urlPath string) bool {
	switch strings.ToUpper(method) {
	case "POST", "PUT", "PATCH", "DELETE":
	default:
		return true
	}
	// Sign-in carries its own suffixes (provider callbacks) — prefix-matched.
	if strings.HasPrefix(path.Clean(urlPath), "/v1/ai/signin") {
		return true
	}
	name, ok := normalizedControllerName(urlPath, method)
	if !ok {
		return true
	}
	_, allowed := demoModeAllowed[name]
	return allowed
}

// superAdminEndpoints are platform-sensitive operations — they expose or mutate
// upstream provider config (which holds upstream API keys), model routing,
// storage credentials, or cluster topology. They ALWAYS require a GLOBAL admin
// (util.IsSuperAdmin) and are NEVER relaxed by preview mode or the benign-read
// exempt list. This closes two issues at once:
//   - preview-mode default-open: with disablePreviewMode=false (the default) the
//     old filter let ALL get-* through, disclosing provider/topology config to
//     unauthenticated callers.
//   - org-admin-as-platform-admin: an org owner (IsAdmin within their own org)
//     could read/modify platform provider config.
//
// get-models is deliberately NOT here — it is the public model catalog and stays
// reachable (it has its own per-request auth in ListModels).
var superAdminEndpoints = map[string]struct{}{
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
	// DO usage backfill — writes the platform-wide financial ledger (cloud_usage).
	"admin/usage/backfill-do": {},
	// Per-org settings is served ZAP-native (/v1/org/settings, super-admin gated in the
	// handler) — no beego route, so no filter entry.
	// Routing-decision + reward training exports are NOT hard super-admin-gated here:
	// they accept EITHER a super admin OR a KMS-provisioned ROUTER_ADMIN_TOKEN service
	// token (spark's retrain). The handler (routerAdminAuthorized) is authoritative;
	// the filter only requires a present credential (below), so anonymous still 401s.
	// Storage provider credentials.
	"get-storage-providers": {},
	// Cluster topology / infrastructure.
	"get-nodes": {}, "get-node": {}, "add-node": {}, "update-node": {}, "delete-node": {},
	"get-k8s-status": {},
}

func requiresSuperAdmin(controllerName string) bool {
	_, ok := superAdminEndpoints[controllerName]
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
	"index": {}, "search": {}, "search/stats": {}, // doc index write + search
	"docs/ingest": {},                                    // unified RAG ingest (github/crawl/s3)
	"embed":       {}, "query": {}, "query_multiple": {}, // librechat-compat RAG
	"documents": {}, // librechat-compat DELETE documents
	// The routing-defaults read (/v1/router/defaults), the router-policy read/write
	// (/v1/router/policy), and the training-data exports (/v1/router/{ledger,rewards})
	// are ZAP-native now — self-authing in their handlers, no beego route to gate here.
	"export-my-routing-data": {}, // self-scoped routing-data export — org-admin gated in controller, never anonymous
	"delete-my-routing-data": {}, // self-scoped routing-data delete — org-admin gated in controller, never anonymous
}

// requiresPresentCredential reports whether controllerName is a write/ingest/
// scrape/RAG endpoint that must fail closed for an anonymous caller. It matches
// the explicit set plus the native /v1/ai/* family, the librechat-compat
// /v1/documents/{id}/context read, and the AI login-manager /v1/ai/connections*
// family (org-scoped: a present credential is required at the filter; the
// controller does the authoritative per-org check — NOT a super-admin gate).
func requiresPresentCredential(controllerName string) bool {
	if _, ok := authRequiredEndpoints[controllerName]; ok {
		return true
	}
	// The OAuth callback is authenticated by the HMAC-signed, org-bound state (not a
	// session): the provider redirects the browser to it and our cookie may not ride
	// along, so it must be reachable without a present credential. The authorize +
	// CRUD paths still require one; the callback re-derives the org from the state.
	if strings.HasPrefix(controllerName, "ai/connections/") && strings.HasSuffix(controllerName, "/callback") {
		return false
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
func hasPresentCredential(ctx *web.Context) bool {
	if GetSessionUser(ctx) != nil {
		return true
	}
	return parseBearerToken(ctx) != ""
}

// sessionOrBearerUser resolves the request principal for the authz gate: the
// session user (cookie auth) if present, else the VERIFIED Bearer credential's
// user. Both Bearer forms resolve to the SAME principal so tenant scoping and
// billing are identical on either — one way to authenticate, one way to bill:
//
//   - a JWT is signature- AND issuer/audience-validated via
//     object.ParseAndValidateJWT (never raw iam.ParseJwtToken), so a forged token
//     cannot pose as an admin.
//   - an hk- IAM API key is resolved to its owning user via IAM (getUserByAccessKey,
//     the same get-user?accessKey= lookup the balance gate bills against). Without
//     this, an hk- key authenticated (e.g. /v1/models) but GetOrg saw no principal,
//     fell back to the empty IAM_ORG default, and every chat call 402'd at zen with
//     "a billable tenant is required" — the `hanzo code` failure.
//
// AutoSigninFilter no-ops for /v1/ paths, so a console call that authenticates
// with a Bearer credential (no cookie) would otherwise present no principal here.
func sessionOrBearerUser(ctx *web.Context) *iam.User {
	if u := GetSessionUser(ctx); u != nil {
		return u
	}
	token := parseBearerToken(ctx)
	if token == "" {
		return nil
	}
	if isJwtLike(token) {
		claims, err := object.ParseAndValidateJWT(token)
		if err != nil {
			return nil
		}
		return &claims.User
	}
	// Non-JWT Bearer: an hk- IAM API key. Resolve it to its owner via IAM so the
	// key path carries the same verified principal (and thus the same tenant) as
	// the JWT path. GetUserByAccessKey returns (nil, nil) for an unknown key (IAM
	// 200 + data:null), so guard BOTH the error and a nil user — fail-secure, no
	// principal on an unrecognized key.
	user, err := controllers.GetUserByAccessKey(token)
	if err != nil || user == nil {
		return nil
	}
	return user
}

// normalizedControllerName derives the controllerName the gate keys on from the
// SAME normalized path Beego dispatches to. Beego path.Cleans the request path
// before router matching (collapsing "//", "/./", "/../" and a trailing slash),
// so a filter that keyed on the RAW path (strings.TrimPrefix of ctx.Request.URL.Path)
// disagreed with the router: variants like "/v1/admin/providers/",
// "/v1//admin/providers", "/v1/./admin/providers" and "/v1/admin/../admin/providers"
// all dispatch to the gated controller yet, un-normalized, produced a controllerName
// ("admin/providers/", …) that missed the superAdminEndpoints map — falling through
// to the fully-open default. Cleaning here makes the gate and the router agree on
// ONE canonical name, closing the entire slash/dot variant set for every gated
// endpoint (admin/providers*, the get-*/*-provider CRUD, topology reads).
//
// A REST resource route resolves to the name its flat predecessor had:
// GET /v1/ai/usages/user-names keys on "get-users", DELETE
// /v1/ai/deployments/:owner/:name on "delete-application". That mapping is
// derived from the SAME table that registers the routes (routers/resources.go),
// not restated here — so the policy sets below, which are keyed by those names,
// cannot drift out of sync with the surface they govern. Had the names been
// rewritten by hand across these maps instead, one missed line would be an
// ungated admin endpoint.
//
// Returns ok=false only for non-/v1 paths (which the caller lets pass, unchanged).
func normalizedControllerName(rawPath, method string) (name string, ok bool) {
	// path.Clean resolves ".", ".." and duplicate slashes on the absolute request
	// path exactly as Beego does before dispatch. path.Clean("/v1/admin/providers/")
	// == "/v1/admin/providers"; path.Clean("/v1//admin/providers") == "/v1/admin/providers".
	cleaned := path.Clean(rawPath)
	if !strings.HasPrefix(cleaned, "/v1/") {
		return "", false
	}
	if key := policyKey(cleaned, method); key != "" {
		return key, true
	}
	return strings.TrimPrefix(cleaned, "/v1/"), true
}

func permissionFilter(ctx *web.Context) {
	controllerName, ok := normalizedControllerName(ctx.Request.URL.Path, ctx.Request.Method)
	if !ok {
		return
	}

	// Platform-sensitive endpoints are gated FIRST — before the preview-mode
	// bypass and the benign-read exempt list — so they are admin-gated regardless
	// of configuration. Fail-secure: no principal => 401, wrong principal => 403.
	if requiresSuperAdmin(controllerName) {
		user := sessionOrBearerUser(ctx)
		if user == nil {
			denyUnauthorized(ctx, "auth:authentication required")
		} else if !util.IsSuperAdmin(user) {
			denyForbidden(ctx, "auth:this operation requires super admin privilege")
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
		// get-cloud-usages is the dual-use usage read (tenant own-org + super-admin
		// god-view). It self-authenticates (RequirePrincipal: session OR verified
		// Bearer) and self-scopes (resolveCloudUsageScope pins a non-super-admin to
		// their own org, ignoring ?org=/X-Org-Id). It is exempt from this coarse
		// session-only IsAdmin gate for the SAME reason get-account is: the gate
		// reads only the session (so it would 403 every Bearer caller) AND it demands
		// org-admin (so it would 403 a regular tenant reading its OWN usage) — both
		// wrong for a Bearer-reachable, own-org read. The handler is the authority:
		// fail-closed 401 with no principal, cross-org only for a verified super
		// admin. (Preview-ON already returns above; this fixes the preview-OFF path.)
		"get-cloud-usages",
		// get-/update-training-contribution are the model-improvement consent read+write.
		// Self-scoped to the caller's OWN org (GetOrg forces the principal's owner; a
		// spoofed X-Org-Id is ignored for a non-super-admin) and Bearer-reachable via
		// RequirePrincipal, so the account-settings opt-in toggle works from Hanzo World
		// (IAM Bearer, no console cookie) exactly as it does from the console cookie.
		// Exempt from the coarse session-only IsAdmin gate for the SAME reason
		// get-cloud-usages is: that gate reads only the cookie session, so it would 403
		// every Bearer caller; the handler is the authority (fail-closed 401 with no
		// principal, and it can only ever read/write the principal's own org).
		"get-training-contribution",
		"update-training-contribution",
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
