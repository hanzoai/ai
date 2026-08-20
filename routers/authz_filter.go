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
	"github.com/zap-proto/zip"
)

func AuthzFilter(c *zip.Ctx) error {
	method := c.Method()
	urlPath := c.Path()

	// NOTE: there is deliberately NO adminDomain Host bypass here. A
	// "Host == adminDomain → return before the gates" shortcut (an upstream
	// artifact) is a latent full-authz-bypass primitive — one ConfigMap edit from
	// CRITICAL, and trivially reachable via a spoofed Host header. The authz gates
	// below run for EVERY request, regardless of Host.

	if conf.IsDemoMode() && !isAllowedInDemoMode(method, urlPath) {
		// Denying and continuing was the old shape: DenyRequest wrote its refusal and
		// the permission filter below then ran and could answer over the top of it.
		// A refusal is the end of the request.
		controllers.DenyRequest(c)
		return nil
	}
	return permissionFilter(c)
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
	"search-docs": {}, "chat-docs": {},
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
	// "models/providers" is deliberately NOT here — it is the public, secret-free
	// projection of the served catalog (a get-models-style public read).
	"admin/providers": {}, "admin/providers/toggle": {}, "admin/providers/primary": {},
	// Model routing config.
	"get-model-routes": {}, "get-model-route": {},
	"add-model-route": {}, "update-model-route": {}, "delete-model-route": {},
	"admin/reload-model-config": {}, "admin/refresh-model-pricing": {},
	// DO usage backfill — writes the platform-wide financial ledger (cloud_usage).
	"admin/usage/backfill-do": {},
	// Per-org settings is served ZAP-native (/v1/org/settings, super-admin gated in the
	// handler) — no controller route, so no filter entry.
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
// benign self-authing or intentionally-anonymous paths (chat, models, health,
// metrics, wecom) are unaffected. Names are the path minus "/v1/".
// anonymousEndpoints are reachable with no credential, on purpose. Each is a
// surface whose whole job is to answer a stranger: the model catalogue a client
// reads before it has a key, a health probe, a public view, and a provider
// callback that authenticates by its own signature rather than by a caller.
//
// Measured against production before being written down — each of these answers
// an unauthenticated request today, so this list records what is, not what
// someone assumed. Anything not here needs a caller.
var anonymousEndpoints = map[string]struct{}{
	"models":           {}, // the catalogue a client reads before it has a key
	"models/providers": {},
	"health":           {}, // probes
	"voice/health":     {},
	"metrics":          {}, // scrape target
	"traffic/globe":    {}, // public view
	"chat/public":      {},
}

// isAnonymous reports whether an endpoint is reachable with no credential.
// The bot callback carries its own bot id, so it is a prefix rather than a
// name: the provider posts to a path this service does not choose, and the
// signature on the body is what authenticates it.
func isAnonymous(controllerName string) bool {
	if _, ok := anonymousEndpoints[controllerName]; ok {
		return true
	}
	return strings.HasPrefix(controllerName, "wecom-bot/callback")
}

var authRequiredEndpoints = map[string]struct{}{
	"index": {}, "search": {}, "search/stats": {}, // doc index write + search
	"docs/ingest": {},                                    // unified RAG ingest (github/crawl/s3)
	"embed":       {}, "query": {}, "query_multiple": {}, // librechat-compat RAG
	"documents": {}, // librechat-compat DELETE documents
	// The routing-defaults read (/v1/router/defaults), the router-policy read/write
	// (/v1/router/policy), and the training-data exports (/v1/router/{ledger,rewards})
	// are ZAP-native now — self-authing in their handlers, no controller route to gate here.
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
		// Memory is PER-PERSON: every verb reads, writes or deletes one named
		// user's own store. There is nothing here for a caller with no credential
		// to be doing, so an anonymous request is refused at the filter and the
		// controller resolves the person from the credential.
		strings.HasPrefix(controllerName, "memory/") ||
		controllerName == "ai/connections" ||
		strings.HasPrefix(controllerName, "ai/connections/")
}

// hasPresentCredential reports whether the request carries SOME credential — a
// session user (cookie auth) or a Bearer token. It is a coarse presence check
// (defense in depth); the controller validates the credential. It deliberately
// does not parse/verify, so it never rejects a valid credential type
// (pk-/sk-/hz_/JWT/session) and never adds an IAM round-trip to the filter.
func hasPresentCredential(c *zip.Ctx) bool {
	if GetSessionUser(c) != nil {
		return true
	}
	return parseBearerToken(c) != ""
}

// sessionOrBearerUser resolves the request principal for the authz gate: the
// session user (cookie auth) if present, else the VERIFIED Bearer credential's
// user. Both Bearer forms resolve to the SAME principal so tenant scoping and
// billing are identical on either — one way to authenticate, one way to bill:
//
//   - a JWT is signature- AND issuer/audience-validated via
//     object.ParseAndValidateJWT (never raw iam.ParseJwtToken), so a forged token
//     cannot pose as an admin.
//   - an sk- IAM API key is resolved to its owning user via IAM (getUserByAccessKey,
//     the same get-user?accessKey= lookup the balance gate bills against). Without
//     this, an API key authenticated (e.g. /v1/models) but GetOrg saw no principal,
//     fell back to the empty IAM_ORG default, and every chat call 402'd at zen with
//     "a billable tenant is required" — the `hanzo code` failure.
//
// AutoSigninFilter no-ops for /v1/ paths, so a console call that authenticates
// with a Bearer credential (no cookie) would otherwise present no principal here.
func sessionOrBearerUser(c *zip.Ctx) *iam.User {
	if u := GetSessionUser(c); u != nil {
		return u
	}
	token := parseBearerToken(c)
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
	// Non-JWT Bearer: an sk- IAM API key. Resolve it to its owner via IAM so the
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
// SAME normalized path the router dispatches to. the router path.Cleans the request path
// before router matching (collapsing "//", "/./", "/../" and a trailing slash),
// so a filter that keyed on the RAW path (strings.TrimPrefix of c.Path())
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
	// path exactly as the router does before dispatch. path.Clean("/v1/admin/providers/")
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

// isJwtLike reports whether a token looks like a JWT — three dot-separated
// segments. It lived beside the auto-signin filter, which is gone; this is its only
// caller, so it lives here now.
func isJwtLike(token string) bool {
	parts := strings.Split(token, ".")
	return len(parts) == 3 && len(parts[0]) > 10 && len(parts[1]) > 10
}

func permissionFilter(c *zip.Ctx) error {
	controllerName, ok := normalizedControllerName(c.Path(), c.Method())
	if !ok {
		return c.Continue()
	}

	// Platform-sensitive endpoints are gated FIRST — before the preview-mode
	// bypass and the benign-read exempt list — so they are admin-gated regardless
	// of configuration. Fail-secure: no principal => 401, wrong principal => 403.
	if requiresSuperAdmin(controllerName) {
		user := sessionOrBearerUser(c)
		if user == nil {
			return denyUnauthorized(c, "auth:authentication required")
		}
		if !util.IsSuperAdmin(user) {
			return denyForbidden(c, "auth:this operation requires super admin privilege")
		}
		return c.Continue()
	}

	// Write / ingest / scrape / RAG endpoints self-authenticate in their
	// controllers (requireIndexAuth / resolveSearchAuth), but the central filter
	// ALSO fails closed here so a fully-anonymous request can never reach them —
	// defense in depth for index-write (retrieval-poisoning / document deletion),
	// scrape (SSRF + cost), and RAG. Previously these fell through the "neither
	// get- nor update-" branch below with NO central gate. A present credential
	// (Bearer OR session) is required here; the controller performs the
	// authoritative validation. This is an explicit set, NOT a blanket default,
	// so health/metrics/wecom stay reachable without a Bearer.
	if requiresPresentCredential(controllerName) && !hasPresentCredential(c) {
		return denyUnauthorized(c, "auth:authentication required")
	}

	disablePreviewMode := conf.DisablePreviewMode()

	isUpdateRequest := strings.HasPrefix(controllerName, "update-") || strings.HasPrefix(controllerName, "add-") || strings.HasPrefix(controllerName, "delete-") || strings.HasPrefix(controllerName, "refresh-") || strings.HasPrefix(controllerName, "deploy-")
	isGetRequest := strings.HasPrefix(controllerName, "get-")

	if !disablePreviewMode && isGetRequest {
		return c.Continue()
	}
	if !isGetRequest && !isUpdateRequest {
		// A name that matches none of the verb prefixes used to pass with no
		// credential at all, so whether an endpoint was reachable anonymously
		// depended on what somebody called it. install-patch is the plain case:
		// nothing about that name says "open", and nothing asked for a caller.
		//
		// The default is closed and the open ones are named. A new endpoint is
		// gated by arriving, rather than by someone remembering to add it, and
		// opening one is a line in this list — visible, and reviewable as a
		// decision.
		if !isAnonymous(controllerName) && !hasPresentCredential(c) {
			return denyUnauthorized(c, "auth:authentication required")
		}
		return c.Continue()
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
			return c.Continue()
		}
	}

	if user := GetSessionUser(c); !util.IsAdmin(user) {
		return denyForbidden(c, "auth:this operation requires admin privilege")
	}
	return c.Continue()
}
