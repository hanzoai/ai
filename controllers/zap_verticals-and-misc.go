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

// Native ZAP handlers for the "verticals + misc" route group (strangler migration
// of form.go / form_data.go / article.go / scale.go / system_info.go /
// prometheus.go / activity.go / org_settings.go / agent.go). Each re-implements its the controller layer
// controller's logic against object/ + iam directly — it NEVER wraps the the controller layer
// controller — mirroring controllers/zap_native.go:zapChatHandler and the CRUD
// template in controllers/zap_chat-graph-crud.go.
//
// Identity is resolved from the Bearer credential through the ONE shared seam
// (zapPrincipalUser: verified JWT via object.ParseAndValidateJWT / pk-/sk- IAM key
// via getUserByAccessKey) — never from the body. Org scope is always the resolved
// user's Owner (zapMiscScopedOwner mirrors ApiController.GetScopedOwner: a member of
// the admin org may target a specific ?owner, everyone else is pinned to their own).
//
// Auth parity: the native ZAP path never runs routers/authz_filter.go, so
// zapMiscAuthz re-enforces that filter's permissionFilter decision verbatim for each
// route (super-admin endpoints first, then preview-mode read bypass, the benign-read
// exempt list, and the org-admin gate for the non-exempt reads/writes), then each
// handler re-runs its OWN in-controller check (scoped-owner sign-in, per-row
// ownership via username match, RequireAdmin for the prometheus surface). This
// mirrors the exact belt-and-suspenders the HTTP path applies.
//
// Envelope parity: success/error use the SAME {status,msg,data,data2} Response the
// ResponseOk / ResponseError emit (the console frontend contract), via the
// group-local zapMiscOk / zapMiscError builders (kept group-local, unique names, so
// they never collide with another group's envelope helpers).
//
// Metering: none of these are billable terminal paths (no recordUsage in any the controller layer
// controller in the group), so STEP 6 (meter-once) does not apply here — there is
// deliberately no meter call, exactly like the chat/graph CRUD group.
//
// Registration: this file OWNS its wiring. init() self-registers each route into the
// package-level registries (registerCloud / registerGatewayPath) defined in
// zap_registry.go; it never edits a shared registration file. The same routes stay
// live on routers.App, which also backs the gateway fallback.
//
// wecom_bot.go is intentionally NOT migrated here: /v1/wecom-bot/callback/:botId is
// a public external WeChat-Work webhook whose inputs are a :botId PATH segment and
// msg_signature/timestamp/nonce/echostr QUERY params carrying an encrypted body —
// none of which the canonical zapHandler(ctx, auth, body) signature can carry, and
// there is no native ZAP client for a WeChat webhook. It stays on routers.App, which
// resolves the :botId segment and the query; re-implementing its crypto here uncalled
// would be dead code.

package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/luxfi/zap"

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

func init() {
	registerZapVerticalsAndMisc()
}

// registerZapVerticalsAndMisc wires the group's native cloud methods (MsgType 100)
// and gateway path prefixes (MsgType 200) into the shared registries. Called by name
// so InitZapHandlers stays the single Node.Handle bootstrap without a per-group
// switch arm.
func registerZapVerticalsAndMisc() {
	// ── Cloud (MsgType 100) — native method names ──
	registerCloud("forms.global.list", zapGetGlobalFormsHandler)
	registerCloud("forms.list", zapGetFormsHandler)
	registerCloud("form.get", zapGetFormHandler)
	registerCloud("form.update", zapUpdateFormHandler)
	registerCloud("form.add", zapAddFormHandler)
	registerCloud("form.delete", zapDeleteFormHandler)
	registerCloud("form.data", zapGetFormDataHandler)

	registerCloud("articles.global.list", zapGetGlobalArticlesHandler)
	registerCloud("articles.list", zapGetArticlesHandler)
	registerCloud("article.get", zapGetArticleHandler)
	registerCloud("article.update", zapUpdateArticleHandler)
	registerCloud("article.add", zapAddArticleHandler)
	registerCloud("article.delete", zapDeleteArticleHandler)

	registerCloud("scales.global.list", zapGetGlobalScalesHandler)
	registerCloud("scales.list", zapGetScalesHandler)
	registerCloud("scale.get", zapGetScaleHandler)
	registerCloud("scales.public", zapGetPublicScalesHandler)
	registerCloud("scale.update", zapUpdateScaleHandler)
	registerCloud("scale.add", zapAddScaleHandler)
	registerCloud("scale.delete", zapDeleteScaleHandler)

	registerCloud("system.info", zapGetSystemInfoHandler)
	registerCloud("system.version", zapGetVersionInfoHandler)
	registerCloud("system.health", zapHealthHandler)
	registerCloud("prometheus.info", zapGetPrometheusInfoHandler)
	registerCloud("prometheus.metrics", zapGetMetricsHandler)
	registerCloud("activities.list", zapGetActivitiesHandler)

	// Per-org settings is now the method-aware HTTP-shaped route registered below
	// (GET/PUT/DELETE on one noun) — no native-cloud method table for it.

	registerCloud("agents.dashboard-url", zapGetAgentsDashboardUrlHandler)

	// ── Gateway (MsgType 200) — /v1 path prefixes routing to the SAME handlers ──
	// lookupGatewayHandler resolves by LONGEST matching prefix (path==p or p+"/"),
	// so the singular/plural pairs that share a stem (get-form ⊂ get-forms ⊂
	// get-form-data, get-scale ⊂ get-scales, …) never shadow each other — each
	// registers as its own distinct exact prefix.

	registerGatewayPath("/v1/health", zapHealthHandler)
	registerGatewayPath("/v1/metrics", zapGetMetricsHandler)

	// Per-org settings (super-admin) — ONE RESTful noun, method-aware: GET/PUT/DELETE
	// /v1/org/settings + GET /v1/org/settings/list. HTTP-shaped (registerGatewayRoute)
	// so the one prefix carries every verb; the handler dispatches by method + the
	// /list sub-path. The /v1/org/settings prefix also matches /v1/org/settings/list.
	registerGatewayRoute("/v1/org/settings", zapOrgSettingsHandler)

}

// ── Shared envelope + gates (parity with util.go + routers/authz_filter.go) ─────

// zapMiscOk mirrors ApiController.ResponseOk: the {status:"ok",data,data2} envelope
// with the SAME 0/1/2-arg semantics (data2 carries the pagination count).
func zapMiscOk(data ...interface{}) (*zap.Message, error) {
	resp := Response{Status: "ok"}
	switch len(data) {
	case 2:
		resp.Data2 = data[1]
		fallthrough
	case 1:
		resp.Data = data[0]
	}
	body, _ := json.Marshal(resp)
	return object.BuildCloudResponse(200, body, "")
}

// zapMiscDeny builds the {status:"error"} envelope at an explicit HTTP status.
func zapMiscDeny(status uint32, msg string) *zap.Message {
	body, _ := json.Marshal(Response{Status: "error", Msg: msg})
	m, _ := object.BuildCloudResponse(status, body, "")
	return m
}

// zapMiscError mirrors ApiController.ResponseError (business errors ride HTTP 200
// with status:"error" in the body, like ServeJSON; auth denials pass 401/403).
func zapMiscError(status uint32, msg string) (*zap.Message, error) {
	return zapMiscDeny(status, msg), nil
}

// zapMiscSuperAdmin is the group's slice of routers/authz_filter.go
// superAdminEndpoints — always super-admin gated, never relaxed by preview mode.
var zapMiscSuperAdmin = map[string]struct{}{
	"org/settings": {}, // the RESTful per-org settings noun (GET/PUT/DELETE + /list)
}

// zapMiscExempt is the group's slice of permissionFilter's benign-read/self-scoped
// exempt set: these skip the coarse org-admin gate because each handler performs its
// own sign-in / ownership check.
var zapMiscExempt = map[string]struct{}{
	"get-forms": {}, "get-public-scales": {},
}

// zapMiscAuthz re-enforces permissionFilter's decision for this group's routes:
// super-admin endpoints first (401/403), then a preview-mode read is open, an
// exempt read is open, a route that is neither a read nor a write is open, and every
// other read/write requires an ORG admin (util.IsAdmin) — a non-admin is 403,
// exactly like permissionFilter's denyForbidden tail. name is the controllerName
// (the router path minus "/v1/"). Returns a denial to return as-is, or nil to proceed
// to the handler's own in-controller check.
func zapMiscAuthz(name string, user *iam.User) *zap.Message {
	if _, ok := zapMiscSuperAdmin[name]; ok {
		if user == nil {
			return zapMiscDeny(401, "authentication required")
		}
		if !util.IsSuperAdmin(user) {
			return zapMiscDeny(403, "this operation requires super admin privilege")
		}
		return nil
	}

	isGet := strings.HasPrefix(name, "get-")
	isWrite := hasAnyPrefix(name, "update-", "add-", "delete-", "refresh-", "deploy-")

	if !conf.DisablePreviewMode() && isGet {
		return nil
	}
	if !isGet && !isWrite {
		return nil
	}
	if _, ok := zapMiscExempt[name]; ok {
		return nil
	}
	if !util.IsAdmin(user) {
		return zapMiscDeny(403, "this operation requires admin privilege")
	}
	return nil
}

// zapMiscRequireAdmin mirrors ApiController.RequireAdmin: preview mode is open,
// otherwise an org admin is required (403). Used by the prometheus surface, whose
// controllers self-guard with RequireAdmin (the filter leaves get-prometheus-info /
// metrics preview-open).
func zapMiscRequireAdmin(user *iam.User) *zap.Message {
	if conf.IsPreviewMode() {
		return nil
	}
	if !util.IsAdmin(user) {
		return zapMiscDeny(403, "this operation requires admin privilege")
	}
	return nil
}

// zapMiscScopedOwner mirrors ApiController.GetScopedOwner: it requires a signed-in
// principal (401 when absent) and pins a non-admin-org caller to its own Owner; a
// member of the admin org may target a specific requested owner. Returns the scoped
// owner and nil, or ("", denial).
func zapMiscScopedOwner(user *iam.User, requestedOwner string) (string, *zap.Message) {
	if user == nil {
		return "", zapMiscDeny(401, "Please sign in first")
	}
	if user.Owner == "admin" {
		if r := strings.TrimSpace(requestedOwner); r != "" {
			return r, nil
		}
	}
	return user.Owner, nil
}

// zapMiscListRequest carries the list/pagination + filter params the GET
// handlers read from the URL query (the native contract has no URL query or response
// headers — the console sends these as JSON, the pagination count rides data2).
type zapMiscListRequest struct {
	Owner        string `json:"owner"`
	ID           string `json:"id"`
	Form         string `json:"form"`
	PageSize     string `json:"pageSize"`
	Page         string `json:"p"`
	Field        string `json:"field"`
	Value        string `json:"value"`
	SortField    string `json:"sortField"`
	SortOrder    string `json:"sortOrder"`
	Days         string `json:"days"`
	SelectedUser string `json:"selectedUser"`
}

func zapMiscDecodeList(body []byte) (zapMiscListRequest, *zap.Message) {
	var req zapMiscListRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return req, zapMiscDeny(400, "invalid request: "+err.Error())
		}
	}
	return req, nil
}

// ── form.go parity ─────────────────────────────────────────────────────────────

func zapGetGlobalFormsHandler(_ context.Context, auth string, _ []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("get-global-forms", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	forms, err := object.GetGlobalForms()
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	return zapMiscOk(object.GetMaskedForms(forms, true))
}

func zapGetFormsHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if deny := zapMiscAuthz("get-forms", user); deny != nil {
		return deny, nil
	}
	req, deny := zapMiscDecodeList(body)
	if deny != nil {
		return deny, nil
	}
	owner, deny := zapMiscScopedOwner(user, req.Owner)
	if deny != nil {
		return deny, nil
	}

	if req.PageSize == "" || req.Page == "" {
		forms, err := object.GetForms(owner)
		if err != nil {
			return zapMiscError(200, err.Error())
		}
		return zapMiscOk(object.GetMaskedForms(forms, true))
	}
	limit := util.ParseInt(req.PageSize)
	count, err := object.GetFormCount(owner, req.Field, req.Value)
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	offset := paginationOffset(util.ParseInt(req.Page), limit)
	forms, err := object.GetPaginationForms(owner, offset, limit, req.Field, req.Value, req.SortField, req.SortOrder)
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	return zapMiscOk(forms, count)
}

func zapGetFormHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("get-form", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	req, deny := zapMiscDecodeList(body)
	if deny != nil {
		return deny, nil
	}
	form, err := object.GetForm(req.ID)
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	return zapMiscOk(object.GetMaskedForm(form, true))
}

func zapUpdateFormHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("update-form", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	var form object.Form
	if err := json.Unmarshal(body, &form); err != nil {
		return zapMiscError(400, "invalid request: "+err.Error())
	}
	id := util.GetId(form.Owner, form.Name)
	success, err := object.UpdateForm(id, &form, "en")
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	return zapMiscOk(success)
}

func zapAddFormHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("add-form", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	var form object.Form
	if err := json.Unmarshal(body, &form); err != nil {
		return zapMiscError(400, "invalid request: "+err.Error())
	}
	success, err := object.AddForm(&form)
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	return zapMiscOk(success)
}

func zapDeleteFormHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("delete-form", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	var form object.Form
	if err := json.Unmarshal(body, &form); err != nil {
		return zapMiscError(400, "invalid request: "+err.Error())
	}
	success, err := object.DeleteForm(&form)
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	return zapMiscOk(success)
}

// ── form_data.go parity (proxies to the blockchain provider, returns raw JSON) ──

func zapGetFormDataHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if deny := zapMiscAuthz("get-form-data", user); deny != nil {
		return deny, nil
	}
	req, deny := zapMiscDecodeList(body)
	if deny != nil {
		return deny, nil
	}
	owner, deny := zapMiscScopedOwner(user, req.Owner)
	if deny != nil {
		return deny, nil
	}

	formObj, err := object.GetForm(util.GetId(owner, req.Form))
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	if formObj == nil {
		return zapMiscError(200, fmt.Sprintf("The form: %s is not found", util.GetId(owner, req.Form)))
	}

	jsonData, err := json.Marshal(formObj)
	if err != nil {
		return zapMiscError(200, "Failed to serialize formObj: "+err.Error())
	}

	blockchainProvider, err := object.GetActiveBlockchainProvider("admin")
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	if blockchainProvider == nil {
		return zapMiscError(200, "The default blockchain provider is not found")
	}
	chainserverUrl := blockchainProvider.ProviderUrl
	if chainserverUrl == "" {
		return zapMiscError(200, "The default blockchain providers' Provider URL cannot be empty. The default value is: 'http://localhost:13900'")
	}

	url := fmt.Sprintf("%s/api/get-form-data?pageSize=%s&p=%s", chainserverUrl, req.PageSize, req.Page)
	resp, err := http.Post(url, "application/json", bytes.NewReader(jsonData))
	if err != nil {
		return zapMiscError(200, "HTTP request failed: "+err.Error())
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return zapMiscError(200, "Failed to read response body: "+err.Error())
	}
	// The chainserver already returns the {status,data,...} JSON the console expects;
	// pass it through verbatim (the controller handler writes it to the body directly).
	return object.BuildCloudResponse(200, respBody, "")
}

// ── article.go parity ──────────────────────────────────────────────────────────

func zapGetGlobalArticlesHandler(_ context.Context, auth string, _ []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("get-global-articles", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	articles, err := object.GetGlobalArticles()
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	return zapMiscOk(object.GetMaskedArticles(articles, true))
}

func zapGetArticlesHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if deny := zapMiscAuthz("get-articles", user); deny != nil {
		return deny, nil
	}
	req, deny := zapMiscDecodeList(body)
	if deny != nil {
		return deny, nil
	}
	owner, deny := zapMiscScopedOwner(user, req.Owner)
	if deny != nil {
		return deny, nil
	}

	if req.PageSize == "" || req.Page == "" {
		articles, err := object.GetArticles(owner)
		if err != nil {
			return zapMiscError(200, err.Error())
		}
		return zapMiscOk(object.GetMaskedArticles(articles, true))
	}
	limit := util.ParseInt(req.PageSize)
	count, err := object.GetArticleCount(owner, req.Field, req.Value)
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	offset := paginationOffset(util.ParseInt(req.Page), limit)
	articles, err := object.GetPaginationArticles(owner, offset, limit, req.Field, req.Value, req.SortField, req.SortOrder)
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	return zapMiscOk(articles, count)
}

func zapGetArticleHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("get-article", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	req, deny := zapMiscDecodeList(body)
	if deny != nil {
		return deny, nil
	}
	article, err := object.GetArticle(req.ID)
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	return zapMiscOk(object.GetMaskedArticle(article, true))
}

func zapUpdateArticleHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("update-article", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	var article object.Article
	if err := json.Unmarshal(body, &article); err != nil {
		return zapMiscError(400, "invalid request: "+err.Error())
	}
	id := util.GetId(article.Owner, article.Name)
	success, err := object.UpdateArticle(id, &article)
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	return zapMiscOk(success)
}

func zapAddArticleHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("add-article", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	var article object.Article
	if err := json.Unmarshal(body, &article); err != nil {
		return zapMiscError(400, "invalid request: "+err.Error())
	}
	success, err := object.AddArticle(&article)
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	return zapMiscOk(success)
}

func zapDeleteArticleHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("delete-article", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	var article object.Article
	if err := json.Unmarshal(body, &article); err != nil {
		return zapMiscError(400, "invalid request: "+err.Error())
	}
	success, err := object.DeleteArticle(&article)
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	return zapMiscOk(success)
}

// ── scale.go parity (its own admin/preview/username ownership logic) ────────────

func zapGetGlobalScalesHandler(_ context.Context, auth string, _ []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("get-global-scales", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	scales, err := object.GetGlobalScales()
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	return zapMiscOk(object.GetMaskedScales(scales, true))
}

func zapGetScalesHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if deny := zapMiscAuthz("get-scales", user); deny != nil {
		return deny, nil
	}
	req, deny := zapMiscDecodeList(body)
	if deny != nil {
		return deny, nil
	}

	owner := req.Owner
	isAdmin := util.IsAdmin(user)
	if isAdmin {
		owner = ""
	} else {
		username := ""
		if user != nil {
			username = user.Name
		}
		if username != "" {
			owner = username
		}
	}

	if req.PageSize == "" || req.Page == "" {
		scales, err := object.GetScales(owner)
		if err != nil {
			return zapMiscError(200, err.Error())
		}
		return zapMiscOk(object.GetMaskedScales(scales, true))
	}
	limit := util.ParseInt(req.PageSize)
	count, err := object.GetScaleCount(owner, req.Field, req.Value)
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	offset := paginationOffset(util.ParseInt(req.Page), limit)
	scales, err := object.GetPaginationScales(owner, offset, limit, req.Field, req.Value, req.SortField, req.SortOrder)
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	return zapMiscOk(scales, count)
}

func zapGetScaleHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if deny := zapMiscAuthz("get-scale", user); deny != nil {
		return deny, nil
	}
	req, deny := zapMiscDecodeList(body)
	if deny != nil {
		return deny, nil
	}
	s, err := object.GetScale(req.ID)
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	if s == nil {
		return zapMiscOk(nil)
	}
	if !util.IsAdmin(user) && !conf.IsPreviewMode() {
		username := ""
		if user != nil {
			username = user.Name
		}
		if s.Owner != username {
			return zapMiscError(403, "Unauthorized operation")
		}
	}
	return zapMiscOk(object.GetMaskedScale(s, true))
}

func zapGetPublicScalesHandler(_ context.Context, auth string, _ []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if deny := zapMiscAuthz("get-public-scales", user); deny != nil {
		return deny, nil
	}
	// The controller requires a signed-in username (GetSessionUsername != "").
	if user == nil || user.Name == "" {
		return zapMiscError(401, "Please sign in first")
	}
	scales, err := object.GetPublicScales("admin")
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	return zapMiscOk(object.GetMaskedScales(scales, true))
}

func zapUpdateScaleHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if deny := zapMiscAuthz("update-scale", user); deny != nil {
		return deny, nil
	}
	var s object.Scale
	if err := json.Unmarshal(body, &s); err != nil {
		return zapMiscError(400, "invalid request: "+err.Error())
	}
	id := util.GetId(s.Owner, s.Name)
	existing, err := object.GetScale(id)
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	if existing == nil {
		return zapMiscError(200, "The task does not exist")
	}
	isAdmin := util.IsAdmin(user)
	if !isAdmin {
		s.State = existing.State
	} else if s.State == object.ScaleStateHidden {
		s.State = object.ScaleStateHidden
	} else {
		s.State = object.ScaleStatePublic
	}
	if !isAdmin && !conf.IsPreviewMode() {
		username := ""
		if user != nil {
			username = user.Name
		}
		if existing.Owner != username {
			return zapMiscError(403, "Unauthorized operation")
		}
	}
	success, err := object.UpdateScale(id, &s)
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	return zapMiscOk(success)
}

func zapAddScaleHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if deny := zapMiscAuthz("add-scale", user); deny != nil {
		return deny, nil
	}
	var s object.Scale
	if err := json.Unmarshal(body, &s); err != nil {
		return zapMiscError(400, "invalid request: "+err.Error())
	}
	if !util.IsAdmin(user) {
		s.State = object.ScaleStatePublic
	} else if s.State == object.ScaleStateHidden {
		s.State = object.ScaleStateHidden
	} else {
		s.State = object.ScaleStatePublic
	}
	success, err := object.AddScale(&s)
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	return zapMiscOk(success)
}

func zapDeleteScaleHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if deny := zapMiscAuthz("delete-scale", user); deny != nil {
		return deny, nil
	}
	var s object.Scale
	if err := json.Unmarshal(body, &s); err != nil {
		return zapMiscError(400, "invalid request: "+err.Error())
	}
	if !util.IsAdmin(user) {
		username := ""
		if user != nil {
			username = user.Name
		}
		existing, err := object.GetScale(s.GetId())
		if err != nil {
			return zapMiscError(200, err.Error())
		}
		if existing == nil {
			return zapMiscError(200, "The task does not exist")
		}
		if existing.Owner != username {
			return zapMiscError(403, "Unauthorized operation")
		}
	}
	success, err := object.DeleteScale(&s)
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	return zapMiscOk(success)
}

// ── system_info.go parity ──────────────────────────────────────────────────────

func zapGetSystemInfoHandler(_ context.Context, auth string, _ []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("get-system-info", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	systemInfo, err := util.GetSystemInfo()
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	return zapMiscOk(systemInfo)
}

func zapGetVersionInfoHandler(_ context.Context, auth string, _ []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("get-version-info", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	errInfo := ""
	versionInfo, err := util.GetVersionInfo()
	if err != nil {
		errInfo = "Git error: " + err.Error()
	}
	if versionInfo.Version != "" || versionInfo.CommitId != "" {
		return zapMiscOk(versionInfo)
	}
	versionInfo, err = util.GetVersionInfoFromFile()
	if err != nil {
		errInfo = errInfo + ", File error: " + err.Error()
		return zapMiscError(200, errInfo)
	}
	return zapMiscOk(versionInfo)
}

func zapHealthHandler(_ context.Context, _ string, _ []byte) (*zap.Message, error) {
	return zapMiscOk()
}

// ── prometheus.go parity (self-guards with RequireAdmin) ────────────────────────

func zapGetPrometheusInfoHandler(_ context.Context, auth string, _ []byte) (*zap.Message, error) {
	if deny := zapMiscRequireAdmin(zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	prometheusInfo, err := object.GetPrometheusInfo()
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	return zapMiscOk(prometheusInfo)
}

// zapGetMetricsHandler mirrors ApiController.GetMetrics: the Prometheus text
// exposition. It drives object.MetricsHandler() through an in-memory recorder (no
// http.ResponseWriter held) and returns the raw exposition body — the console/
// scraper reads it verbatim, exactly like the the router ServeHTTP path.
func zapGetMetricsHandler(_ context.Context, auth string, _ []byte) (*zap.Message, error) {
	if deny := zapMiscRequireAdmin(zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/v1/metrics", nil)
	object.MetricsHandler().ServeHTTP(rec, req)
	return object.BuildCloudResponse(200, rec.Body.Bytes(), "")
}

// ── activity.go parity ─────────────────────────────────────────────────────────

func zapGetActivitiesHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("get-activities", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	req, deny := zapMiscDecodeList(body)
	if deny != nil {
		return deny, nil
	}
	days := util.ParseInt(req.Days)
	fields := strings.Split(req.Field, ",")
	activities, err := object.GetActivities(days, req.SelectedUser, fields, "en")
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	return zapMiscOk(activities)
}

// ── org_settings.go parity (super-admin gated, PATCH-merge upsert) ──────────────
//
// ONE RESTful noun, method-aware — the SOLE implementation of the per-org settings
// surface (the the router org_settings.go controller was deleted). GET/PUT/DELETE
// /v1/org/settings + GET /v1/org/settings/list. Super-admin gated ONCE at the top.
// Owner is the ?owner= query param (the controller handlers read ?owner; a bare
// OrgSettings body carries it as a fallback), never an identity source.
//
// PUT is a PATCH-MERGE upsert (mirrors the deleted the router UpdateOrgSettings): the body
// is unmarshaled ONTO the existing row (or a fresh one keyed by owner), so a field
// ABSENT from the body keeps its current value. dbx Model(s).Update() writes ALL
// columns, so a replace would NULL every field the body didn't restate — a partial
// write (e.g. just autoRouting from the admin toggle) must never wipe the org's
// RouterPrefer/RouterEnabledModels/RouterQualityBias/…. To CLEAR a field, send it
// explicitly ({"routerPrefer":{}}). This is the fix the old body-only ZAP twin lacked
// (it could not even see ?owner — the reason this route is now HTTP-shaped).
func zapOrgSettingsHandler(_ context.Context, method, path, query, auth string, body []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("org/settings", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}

	// /v1/org/settings/list — every row for an owner (default "admin").
	if strings.HasPrefix(path, "/v1/org/settings/list") {
		if strings.ToUpper(method) != http.MethodGet {
			return zapMiscError(http.StatusMethodNotAllowed, "method not allowed: "+method)
		}
		owner := zapOrgSettingsOwner(query, nil)
		if owner == "" {
			owner = "admin"
		}
		settings, err := object.GetOrgSettingsList(owner)
		if err != nil {
			return zapMiscError(200, err.Error())
		}
		return zapMiscOk(settings)
	}

	switch strings.ToUpper(method) {
	case http.MethodGet:
		owner := zapOrgSettingsOwner(query, nil)
		if owner == "" {
			return zapMiscError(200, "owner is required")
		}
		settings, err := object.GetOrgSettings(owner)
		if err != nil {
			return zapMiscError(200, err.Error())
		}
		return zapMiscOk(settings)

	case http.MethodPut:
		owner := zapOrgSettingsOwner(query, body)
		if owner == "" {
			return zapMiscError(200, "owner is required")
		}
		// Upsert keys on owner only, so a never-configured org still takes effect.
		existing, err := object.GetOrgSettings(owner)
		if err != nil {
			return zapMiscError(200, err.Error())
		}
		target := existing
		if target == nil {
			target = &object.OrgSettings{Owner: owner}
		}
		if err := json.Unmarshal(body, target); err != nil { // PATCH-merge onto the row
			return zapMiscError(400, "invalid request: "+err.Error())
		}
		target.Owner = owner
		var success bool
		if existing == nil {
			success, err = object.AddOrgSettings(target)
		} else {
			success, err = object.UpdateOrgSettings(owner, target)
		}
		if err != nil {
			return zapMiscError(200, err.Error())
		}
		return zapMiscOk(success)

	case http.MethodDelete:
		owner := zapOrgSettingsOwner(query, body)
		if owner == "" {
			return zapMiscError(200, "owner is required")
		}
		success, err := object.DeleteOrgSettings(&object.OrgSettings{Owner: owner})
		if err != nil {
			return zapMiscError(200, err.Error())
		}
		return zapMiscOk(success)
	}
	return zapMiscError(http.StatusMethodNotAllowed, "method not allowed: "+method)
}

// zapOrgSettingsOwner resolves the target org from the ?owner= query, falling back to
// the body's own Owner field (a bare OrgSettings body carries it). Never an identity
// source — the super-admin gate above is the authz.
func zapOrgSettingsOwner(query string, body []byte) string {
	if q, err := url.ParseQuery(query); err == nil {
		if owner := q.Get("owner"); owner != "" {
			return owner
		}
	}
	if len(body) > 0 {
		var probe object.OrgSettings
		if json.Unmarshal(body, &probe) == nil && probe.Owner != "" {
			return probe.Owner
		}
	}
	return ""
}

// ── agent.go parity ────────────────────────────────────────────────────────────

func zapGetAgentsDashboardUrlHandler(_ context.Context, auth string, _ []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("get-agents-dashboard-url", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	dashboardUrl := os.Getenv("AGENTS_DASHBOARD_URL")
	if dashboardUrl == "" {
		dashboardUrl = "http://localhost:8080/ui"
	}
	return zapMiscOk(dashboardUrl)
}
