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

// Native ZAP handlers for the provider-config route group (strangler migration
// of provider.go / provider_admin.go / provider_flags.go). Each handler
// re-implements the beego controller logic against object/ + iam directly —
// never wrapping the beego controller — mirroring
// controllers/zap_native.go:zapChatHandler and the shared seam in
// controllers/zap_account-auth.go (the registry + zapOk/zapErr + zapResolvePrincipal).
//
// Auth parity: every route in this group EXCEPT /v1/models/providers is gated at
// SUPER ADMIN (util.IsSuperAdmin, owner == "admin") — the SAME policy the beego
// authz filter applies (routers/authz_filter.go superAdminEndpoints). The native
// ZAP path never runs that filter, so the gate is re-enforced verbatim here via
// the shared zapResolvePrincipal identity seam (JWT → ParseAndValidateJWT, sk- →
// getUserByAccessKey). One verified principal; the body is never trusted for
// identity. /v1/models/providers is the public, secret-free projection of the
// served catalog that the filter deliberately leaves ungated.
//
// Registration: this file self-registers its routes into the shared registry
// (registerCloud / registerGatewayPath, defined once in zap_account-auth.go) from
// its OWN init() — it never edits a shared file. The beego routes in router.go
// stay live in parallel (strangler) until Integrate flips the dispatch.

package controllers

import (
	"context"
	"encoding/json"
	"strings"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/luxfi/zap"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

func init() {
	// Cloud (MsgType 100) — native method names.
	registerCloud("providers.global.list", zapGetGlobalProvidersHandler)
	registerCloud("providers.list", zapGetProvidersHandler)
	registerCloud("providers.get", zapGetProviderHandler)
	registerCloud("providers.update", zapUpdateProviderHandler)
	registerCloud("providers.add", zapAddProviderHandler)
	registerCloud("providers.delete", zapDeleteProviderHandler)
	registerCloud("providers.mcp.refresh", zapRefreshMcpToolsHandler)
	registerCloud("admin.providers.list", zapGetAdminProvidersHandler)
	registerCloud("admin.providers.toggle", zapToggleAdminProviderHandler)
	registerCloud("admin.providers.primary", zapSetPrimaryAdminProviderHandler)
	registerCloud("models.providers", zapGetModelProvidersHandler)

	// Gateway (MsgType 200) — /v1 path prefixes routing to the SAME handlers.
	// lookupGatewayHandler resolves by longest matching prefix, so the shorter
	// "/v1/admin/providers" never shadows "/v1/admin/providers/toggle".
	registerGatewayPath("/v1/admin/providers/toggle", zapToggleAdminProviderHandler)
	registerGatewayPath("/v1/admin/providers/primary", zapSetPrimaryAdminProviderHandler)
	registerGatewayPath("/v1/admin/providers", zapGetAdminProvidersHandler)
	registerGatewayPath("/v1/models/providers", zapGetModelProvidersHandler)
}

// ── auth gate ────────────────────────────────────────────────────────────────

// zapProviderSuperAdmin resolves the Bearer principal (shared zapResolvePrincipal)
// and enforces the super-admin gate (util.IsSuperAdmin) exactly like the beego
// authz filter's superAdminEndpoints path. Returns (user, nil) on success, or
// (nil, denial) with a fail-closed 401 (no/invalid credential) or 403
// (authenticated non-super-admin) response to return as-is.
func zapProviderSuperAdmin(auth string) (*iam.User, *zap.Message) {
	user, err := zapAccountPrincipal(auth)
	if err != nil || user == nil {
		msg, _ := zapProviderError(401, "authentication required")
		return nil, msg
	}
	if !util.IsSuperAdmin(user) {
		msg, _ := zapProviderError(403, "this operation requires super admin privilege")
		return nil, msg
	}
	return user, nil
}

// zapProviderOk emits the beego {status:"ok",data[,data2]} envelope (ResponseOk
// and its paginated ResponseOk(list, count) form). Shared by the provider and
// chat-graph groups — one provider envelope, not a per-group fork.
func zapProviderOk(data ...interface{}) (*zap.Message, error) {
	resp := Response{Status: "ok"}
	if len(data) > 0 {
		resp.Data = data[0]
	}
	if len(data) > 1 {
		resp.Data2 = data[1]
	}
	body, _ := json.Marshal(resp)
	return object.BuildCloudResponse(200, body, "")
}

// zapProviderError emits {status:"error",msg} at an explicit status (beego
// ResponseError is 200-with-error-body; auth denials use real 401/403).
func zapProviderError(status uint32, msg string) (*zap.Message, error) {
	body, _ := json.Marshal(Response{Status: "error", Msg: msg})
	return object.BuildCloudResponse(status, body, msg)
}

// ── provider.go parity ──────────────────────────────────────────────────────

// zapGetGlobalProvidersHandler mirrors ApiController.GetGlobalProviders.
func zapGetGlobalProvidersHandler(_ context.Context, auth string, _ []byte) (*zap.Message, error) {
	user, deny := zapProviderSuperAdmin(auth)
	if deny != nil {
		return deny, nil
	}
	providers, err := object.GetGlobalProviders()
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	return zapProviderOk(object.GetMaskedProviders(providers, true, user))
}

// zapGetProvidersRequest carries the GetProviders query params over the native
// body (the native contract has no URL query; the console sends these as JSON).
type zapGetProvidersRequest struct {
	PageSize  string `json:"pageSize"`
	Page      string `json:"p"`
	Field     string `json:"field"`
	Value     string `json:"value"`
	Store     string `json:"store"`
	SortField string `json:"sortField"`
	SortOrder string `json:"sortOrder"`
}

// zapGetProvidersHandler mirrors ApiController.GetProviders (org-scoped to the
// principal's Owner, store isolation enforced from the principal's Homepage, and
// the same list-vs-paginate branch on pageSize/p).
func zapGetProvidersHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user, deny := zapProviderSuperAdmin(auth)
	if deny != nil {
		return deny, nil
	}
	var req zapGetProvidersRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return zapProviderError(400, "invalid request: "+err.Error())
		}
	}

	owner := user.Owner // org scope — NEVER a client-supplied field

	// Store isolation from the principal's Homepage binding (parity with
	// EnforceStoreIsolation): a bound user is forced to their store; a request for
	// a different store is denied.
	storeName := req.Store
	if user.Homepage != "" {
		if storeName == "" || storeName == "All" {
			storeName = user.Homepage
		} else if storeName != user.Homepage {
			return zapProviderError(200, "You can only access data from your assigned store")
		}
	}

	if req.PageSize == "" || req.Page == "" {
		providers, err := object.GetProviders(owner)
		if err != nil {
			return zapProviderError(200, err.Error())
		}
		return zapProviderOk(object.GetMaskedProviders(providers, true, user))
	}

	limit := util.ParseInt(req.PageSize)
	count, err := object.GetProviderCount(owner, storeName, req.Field, req.Value)
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	page := util.ParseInt(req.Page)
	offset := (page - 1) * limit
	if offset < 0 {
		offset = 0
	}
	providers, err := object.GetPaginationProviders(owner, storeName, offset, limit, req.Field, req.Value, req.SortField, req.SortOrder)
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	return zapProviderOk(object.GetMaskedProviders(providers, true, user), count)
}

// zapGetProviderRequest carries the GetProvider id over the native body.
type zapGetProviderRequest struct {
	ID string `json:"id"`
}

// zapGetProviderHandler mirrors ApiController.GetProvider.
func zapGetProviderHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user, deny := zapProviderSuperAdmin(auth)
	if deny != nil {
		return deny, nil
	}
	var req zapGetProviderRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return zapProviderError(400, "invalid request: "+err.Error())
		}
	}
	provider, err := object.GetProvider(req.ID)
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	return zapProviderOk(object.GetMaskedProvider(provider, true, user))
}

// zapUpdateProviderRequest carries the UpdateProvider id (URL query in HTTP) plus
// the provider payload over the native body.
type zapUpdateProviderRequest struct {
	ID       string          `json:"id"`
	Provider object.Provider `json:"provider"`
}

// zapUpdateProviderHandler mirrors ApiController.UpdateProvider (super-admin only).
func zapUpdateProviderHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if _, deny := zapProviderSuperAdmin(auth); deny != nil {
		return deny, nil
	}
	var req zapUpdateProviderRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return zapProviderError(400, "invalid request: "+err.Error())
	}
	success, err := object.UpdateProvider(req.ID, &req.Provider)
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	return zapProviderOk(success)
}

// zapAddProviderHandler mirrors ApiController.AddProvider: the org owner is the
// resolved principal's Owner (never client-supplied), and a tenant-supplied RAW
// key is sealed into KMS as a kms:// ref (fails closed) before persistence — the
// same BYOK seam as the beego path.
func zapAddProviderHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user, deny := zapProviderSuperAdmin(auth)
	if deny != nil {
		return deny, nil
	}
	var provider object.Provider
	if err := json.Unmarshal(body, &provider); err != nil {
		return zapProviderError(400, "invalid request: "+err.Error())
	}
	provider.Owner = user.Owner

	if user.Owner != "admin" && provider.ClientSecret != "" && !strings.HasPrefix(provider.ClientSecret, "kms://") {
		ref, err := object.StoreProviderSecret(byokSecretName(user.Owner, provider.Name), provider.ClientSecret)
		if err != nil {
			return zapProviderError(200, err.Error())
		}
		provider.ClientSecret = ref
	}

	success, err := object.AddProvider(&provider)
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	return zapProviderOk(success)
}

// zapDeleteProviderHandler mirrors ApiController.DeleteProvider (super-admin only).
func zapDeleteProviderHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if _, deny := zapProviderSuperAdmin(auth); deny != nil {
		return deny, nil
	}
	var provider object.Provider
	if err := json.Unmarshal(body, &provider); err != nil {
		return zapProviderError(400, "invalid request: "+err.Error())
	}
	success, err := object.DeleteProvider(&provider)
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	return zapProviderOk(success)
}

// zapRefreshMcpToolsHandler mirrors ApiController.RefreshMcpTools (super-admin only).
func zapRefreshMcpToolsHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if _, deny := zapProviderSuperAdmin(auth); deny != nil {
		return deny, nil
	}
	var provider object.Provider
	if err := json.Unmarshal(body, &provider); err != nil {
		return zapProviderError(400, "invalid request: "+err.Error())
	}
	if err := object.RefreshMcpTools(&provider); err != nil {
		return zapProviderError(200, err.Error())
	}
	return zapProviderOk(&provider)
}

// ── provider_admin.go parity ─────────────────────────────────────────────────

// zapGetAdminProvidersHandler mirrors ApiController.GetAdminProviders.
func zapGetAdminProvidersHandler(_ context.Context, auth string, _ []byte) (*zap.Message, error) {
	if _, deny := zapProviderSuperAdmin(auth); deny != nil {
		return deny, nil
	}
	views, err := listAdminModelProviders()
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	return zapProviderOk(views)
}

// zapToggleAdminProviderHandler mirrors ApiController.ToggleAdminProvider.
func zapToggleAdminProviderHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if _, deny := zapProviderSuperAdmin(auth); deny != nil {
		return deny, nil
	}
	var req toggleProviderRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return zapProviderError(400, "invalid request: "+err.Error())
	}

	provider, err := getAdminModelProvider(req.Name)
	if err != nil {
		return zapProviderError(200, err.Error())
	}

	provider.State = stateForEnabled(req.Enabled)
	if _, err := object.UpdateProvider(provider.GetId(), provider); err != nil {
		return zapProviderError(200, err.Error())
	}
	object.InvalidateProviderNameCache(provider.Name)

	updated, err := getAdminModelProvider(req.Name)
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	return zapProviderOk(toAdminProviderView(updated, modelCountByProvider()))
}

// zapSetPrimaryAdminProviderHandler mirrors ApiController.SetPrimaryAdminProvider.
func zapSetPrimaryAdminProviderHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if _, deny := zapProviderSuperAdmin(auth); deny != nil {
		return deny, nil
	}
	var req setPrimaryRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return zapProviderError(400, "invalid request: "+err.Error())
	}

	provider, err := getAdminModelProvider(req.Name)
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	if !object.ModelProviderUsable(provider) {
		return zapProviderError(200, "the provider must be enabled before it can be made primary")
	}
	if err := object.SetPrimaryModelProvider(req.Name); err != nil {
		return zapProviderError(200, err.Error())
	}

	views, err := listAdminModelProviders()
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	return zapProviderOk(views)
}

// ── models_providers.go parity ───────────────────────────────────────────────

// zapGetModelProvidersHandler mirrors ApiController.GetModelProviders. PUBLIC:
// secret-free name set projected from the served catalog, deliberately ungated
// (parity with the route the authz filter leaves out of superAdminEndpoints).
func zapGetModelProvidersHandler(_ context.Context, _ string, _ []byte) (*zap.Message, error) {
	return zapProviderOk(modelProviders{Providers: listRouteProviders()})
}
