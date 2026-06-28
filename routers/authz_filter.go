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

	"github.com/beego/beego"
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
	// "Host == adminDomain → return before the gates" shortcut (a Casdoor
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
	// Model routing config.
	"get-model-routes": {}, "get-model-route": {},
	"add-model-route": {}, "update-model-route": {}, "delete-model-route": {},
	"reload-model-config": {},
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

func permissionFilter(ctx *context.Context) {
	path := ctx.Request.URL.Path
	if !strings.HasPrefix(path, "/v1/") {
		return
	}
	controllerName := strings.TrimPrefix(path, "/v1/")

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

	disablePreviewMode, _ := beego.AppConfig.Bool("disablePreviewMode")

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
