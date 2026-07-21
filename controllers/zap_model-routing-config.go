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

// Native ZAP handlers for the model-routing / model-config / model-access group
// (strangler migration of model_route.go, model_config.go, model_config_live.go,
// model_pricing.go, model_access.go). Pure ZAP — no beego controller, no HTTP
// writer. Each handler re-implements the beego method's logic against object/ +
// iam, preserving identity, authz, and response shape EXACTLY.
//
// These are HTTP-shaped admin/self routes (query strings, a :model path param,
// GET vs POST), so they ride the gateway HTTP-over-ZAP projection (MsgType 200):
// method(0) + path(8) + headers(16) + body(24) + query(32). The group
// self-registers its path prefixes from THIS file's init() — no edit to
// zap_native.go or any shared registration file. The beego routes in router.go
// keep serving in parallel until Integrate flips the gateway dispatch here.

package controllers

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/luxfi/zap"
)

// The canonical gateway registry (zapGatewayHandler + registerGatewayRoute) lives
// in zap_registry.go. This HTTP-shaped group registers rich handlers (method/path/
// query aware) via registerGatewayRoute from its own init().

func init() {
	// Model routing (SuperAdmin) — one handler for the five *-model-route paths.
	registerGatewayRoute("/v1/get-model-routes", zapModelRouteHandler)
	registerGatewayRoute("/v1/get-model-route", zapModelRouteHandler)
	registerGatewayRoute("/v1/add-model-route", zapModelRouteHandler)
	registerGatewayRoute("/v1/update-model-route", zapModelRouteHandler)
	registerGatewayRoute("/v1/delete-model-route", zapModelRouteHandler)

	// Per-model access (self-scoped) and admin access grants (SuperAdmin).
	registerGatewayRoute("/v1/models/", zapModelAccessSelfHandler) // /v1/models/{model}/access
	registerGatewayRoute("/v1/admin/model-access", zapAdminModelAccessHandler)

	// Config reload / pricing refresh (SuperAdmin).
	registerGatewayRoute("/v1/admin/reload-model-config", zapModelConfigAdminHandler)
	registerGatewayRoute("/v1/admin/refresh-model-pricing", zapModelConfigAdminHandler)
}

// ── Response helpers: the SAME Response envelope the beego path returns ────────
//
// beego ResponseOk → HTTP 200 {status:"ok",data,data2}; beego ResponseError →
// HTTP 200 {status:"error",msg} (a logic error is 200-with-error-body); auth
// denials → real 401/403. These mirror that exactly over the gateway projection.

func zapGwOk(data ...interface{}) (*zap.Message, error) {
	resp := Response{Status: "ok"}
	switch len(data) {
	case 2:
		resp.Data2 = data[1]
		fallthrough
	case 1:
		resp.Data = data[0]
	}
	b, _ := json.Marshal(resp)
	return object.BuildGatewayResponse(200, b, nil)
}

// zapGwError renders {status:"error",msg} at an explicit HTTP status. Logic errors
// use 200 (beego ResponseError parity); auth denials use 401/403.
func zapGwError(status uint32, msg string) (*zap.Message, error) {
	b, _ := json.Marshal(Response{Status: "error", Msg: msg})
	return object.BuildGatewayResponse(status, b, nil)
}

// ── Identity: the ONE auth seam, returning the full principal ──────────────────
//
// zapResolveUser (zap_native.go) returns only "owner/name"; the authz checks here
// need the whole *iam.User (IsSuperAdmin, name/email for access keying). This
// composes the SAME verified resolvers — object.ParseAndValidateJWT (signature +
// iss/aud) for JWTs, getUserByAccessKey for hk-/pk- IAM keys — and never trusts
// the body. Mirrors credentialUser (org_resolver.go) for the Bearer-only path.
func zapPrincipalUser(auth string) *iam.User {
	token := strings.TrimPrefix(auth, "Bearer ")
	if token == "" {
		return nil
	}
	if isJwtToken(token) {
		claims, err := object.ParseAndValidateJWT(token)
		if err != nil || claims == nil {
			return nil
		}
		return &claims.User
	}
	if isIAMApiKey(token) {
		if user, err := getUserByAccessKey(token); err == nil && user != nil {
			return user
		}
	}
	return nil
}

// zapRequireSuperAdmin mirrors ApiController.RequireSuperAdmin: no verified
// principal → 401, authenticated non-super-admin → 403. Returns the user when
// authorized, else the refusal message to return verbatim.
func zapRequireSuperAdmin(auth string) (*iam.User, *zap.Message) {
	user := zapPrincipalUser(auth)
	if user == nil {
		m, _ := zapGwError(401, "auth:Please sign in first")
		return nil, m
	}
	if !util.IsSuperAdmin(user) {
		m, _ := zapGwError(403, "auth:this operation requires super admin privilege")
		return nil, m
	}
	return user, nil
}

// ── Model routes (model_route.go) ──────────────────────────────────────────────

func zapModelRouteHandler(ctx context.Context, method, path, query, auth string, body []byte) (*zap.Message, error) {
	if _, refuse := zapRequireSuperAdmin(auth); refuse != nil {
		return refuse, nil
	}
	q, _ := url.ParseQuery(query)

	switch {
	case strings.HasPrefix(path, "/v1/get-model-routes"):
		return zapGetModelRoutes(q)
	case strings.HasPrefix(path, "/v1/get-model-route"):
		return zapGetModelRoute(q)
	case strings.HasPrefix(path, "/v1/add-model-route"):
		return zapAddModelRoute(body)
	case strings.HasPrefix(path, "/v1/update-model-route"):
		return zapUpdateModelRoute(q, body)
	case strings.HasPrefix(path, "/v1/delete-model-route"):
		return zapDeleteModelRoute(body)
	}
	return zapGwError(404, "not found: "+path)
}

func zapGetModelRoutes(q url.Values) (*zap.Message, error) {
	owner := q.Get("owner")
	if owner == "" {
		owner = "admin"
	}
	limit := q.Get("pageSize")
	page := q.Get("p")
	field := q.Get("field")
	value := q.Get("value")
	sortField := q.Get("sortField")
	sortOrder := q.Get("sortOrder")

	if limit == "" || page == "" {
		routes, err := object.GetModelRoutes(owner)
		if err != nil {
			return zapGwError(200, err.Error())
		}
		return zapGwOk(routes)
	}

	lim := util.ParseInt(limit)
	count, err := object.GetModelRouteCount(owner, field, value)
	if err != nil {
		return zapGwError(200, err.Error())
	}
	offset := zapPageOffset(page, lim)
	routes, err := object.GetPaginationModelRoutes(owner, offset, lim, field, value, sortField, sortOrder)
	if err != nil {
		return zapGwError(200, err.Error())
	}
	return zapGwOk(routes, count)
}

// zapPageOffset mirrors beego pagination.SetPaginator().Offset(): page is 1-based,
// clamped to >= 1 (unset or garbage → 1), and the offset is (page-1)*perPage.
func zapPageOffset(page string, perPage int) int {
	p, err := util.ParseIntWithError(page)
	if err != nil || p < 1 {
		p = 1
	}
	return (p - 1) * perPage
}

func zapGetModelRoute(q url.Values) (*zap.Message, error) {
	owner := q.Get("owner")
	modelName := q.Get("modelName")
	if owner == "" || modelName == "" {
		return zapGwError(200, "owner and modelName are required")
	}
	route, err := object.GetModelRoute(owner, modelName)
	if err != nil {
		return zapGwError(200, err.Error())
	}
	return zapGwOk(route)
}

func zapAddModelRoute(body []byte) (*zap.Message, error) {
	var route object.ModelRoute
	if err := json.Unmarshal(body, &route); err != nil {
		return zapGwError(200, err.Error())
	}
	if route.Owner == "" {
		route.Owner = "admin"
	}
	success, err := object.AddModelRoute(&route)
	if err != nil {
		return zapGwError(200, err.Error())
	}
	return zapGwOk(success)
}

func zapUpdateModelRoute(q url.Values, body []byte) (*zap.Message, error) {
	owner := q.Get("owner")
	modelName := q.Get("modelName")
	var route object.ModelRoute
	if err := json.Unmarshal(body, &route); err != nil {
		return zapGwError(200, err.Error())
	}
	success, err := object.UpdateModelRoute(owner, modelName, &route)
	if err != nil {
		return zapGwError(200, err.Error())
	}
	return zapGwOk(success)
}

func zapDeleteModelRoute(body []byte) (*zap.Message, error) {
	var route object.ModelRoute
	if err := json.Unmarshal(body, &route); err != nil {
		return zapGwError(200, err.Error())
	}
	success, err := object.DeleteModelRoute(&route)
	if err != nil {
		return zapGwError(200, err.Error())
	}
	return zapGwOk(success)
}

// ── Per-model access, self-scoped (model_access.go) ────────────────────────────
//
// GET  /v1/models/{model}/access → the caller's standing for a gated model.
// POST /v1/models/{model}/access → record the caller's waitlist request.
// The row is ALWAYS keyed to the caller's own verified identity — never a body
// field. Uses the shared principalAccessIdentity (model_access.go).

func zapModelAccessSelfHandler(ctx context.Context, method, path, query, auth string, body []byte) (*zap.Message, error) {
	model := zapModelFromAccessPath(path)
	if model == "" {
		// Not an access sub-path — let the caller fall through (e.g. /v1/models list).
		return zapGwError(404, "not found: "+path)
	}
	user := zapPrincipalUser(auth)
	if user == nil {
		return zapGwError(401, "auth:Please sign in first")
	}
	owner, key, email := principalAccessIdentity(user)

	switch strings.ToUpper(method) {
	case "POST":
		row, err := object.RequestModelAccess(owner, key, email, model)
		if err != nil {
			return zapGwError(200, err.Error())
		}
		return zapGwOk(row)
	default: // GET
		return zapGwOk(map[string]string{
			"model":  model,
			"status": object.ModelAccessStatus(owner, user.Name, email, model),
		})
	}
}

// zapModelFromAccessPath extracts {model} from "/v1/models/{model}/access", or ""
// when the path is not that shape (so /v1/models and other sub-paths are ignored).
func zapModelFromAccessPath(path string) string {
	const pre = "/v1/models/"
	if !strings.HasPrefix(path, pre) {
		return ""
	}
	rest, ok := strings.CutSuffix(strings.TrimPrefix(path, pre), "/access")
	if !ok || rest == "" || strings.Contains(rest, "/") {
		return ""
	}
	return rest
}

// ── Admin access grants (model_access.go, SuperAdmin) ──────────────────────────

func zapAdminModelAccessHandler(ctx context.Context, method, path, query, auth string, body []byte) (*zap.Message, error) {
	if _, refuse := zapRequireSuperAdmin(auth); refuse != nil {
		return refuse, nil
	}
	switch strings.ToUpper(method) {
	case "POST":
		return zapAdminGrantModelAccess(body)
	default: // GET
		q, _ := url.ParseQuery(query)
		rows, err := object.ListModelAccess(q.Get("owner"))
		if err != nil {
			return zapGwError(200, err.Error())
		}
		return zapGwOk(rows)
	}
}

func zapAdminGrantModelAccess(body []byte) (*zap.Message, error) {
	var b struct {
		Owner  string `json:"owner"`
		User   string `json:"user"`
		Email  string `json:"email"`
		Model  string `json:"model"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &b); err != nil {
		return zapGwError(200, err.Error())
	}
	if b.Owner == "" || b.Model == "" {
		return zapGwError(200, "owner and model are required")
	}
	status := b.Status
	if status == "" {
		status = object.ModelAccessGranted
	}
	row, err := object.UpsertModelAccess(b.Owner, b.User, b.Email, b.Model, status)
	if err != nil {
		return zapGwError(200, err.Error())
	}
	return zapGwOk(row)
}

// ── Config reload / pricing refresh (model_config.go, SuperAdmin) ──────────────

func zapModelConfigAdminHandler(ctx context.Context, method, path, query, auth string, body []byte) (*zap.Message, error) {
	if _, refuse := zapRequireSuperAdmin(auth); refuse != nil {
		return refuse, nil
	}
	cfg := GetModelConfig()
	if cfg == nil {
		return zapGwError(200, "model config not initialized")
	}

	switch {
	case strings.HasPrefix(path, "/v1/admin/reload-model-config"):
		if err := cfg.Reload(); err != nil {
			return zapGwError(200, "reload failed: "+err.Error())
		}
		return zapGwOk()
	case strings.HasPrefix(path, "/v1/admin/refresh-model-pricing"):
		cfg.fetchLivePricing()
		return zapGwOk(struct {
			LastPricingRefresh time.Time `json:"lastPricingRefresh"`
		}{LastPricingRefresh: cfg.LastPricingRefresh().UTC()})
	}
	return zapGwError(404, "not found: "+path)
}
