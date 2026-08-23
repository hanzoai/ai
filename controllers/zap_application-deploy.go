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

// Native ZAP handlers for the application-deploy route-group — the strangler
// migration of application.go / template_deploy.go off the controller layer.
//
// STRANGLER: each controller method (ApiController.GetApplications, GetApplication,
// UpdateApplication, AddApplication, DeleteApplication, DeployApplication,
// UndeployApplication, GetK8sStatus) is re-implemented here as a pure ZAP handler
// against object/ + iam, mirroring zap_native.go:zapChatHandler. The same routes
// stay live on routers.App, which also backs the gateway fallback.
//
// Registration convention (recipe): this group exposes two group-local dispatch
// tables (zapApplicationDeployCloud / …Gateway) keyed by native cloud method and
// gateway path, via a private type ALIAS — mirroring zap_responses.go /
// zap_router-policy-stats.go. It declares NO shared symbol and edits NO shared
// file; this file's init() ranges over these tables into the ONE canonical
// registry (registerCloud / registerGatewayPath, zap_registry.go).
//
// Identity/scope parity: these endpoints have NO session filter on the ZAP path
// (there is no filter chain), so every handler fails closed — a verified
// credential is required (STEP 1). Org scoping for GetApplications mirrors
// GetScopedOwner EXACTLY (owner = principal.Owner; the reserved `admin` org may
// target another owner). GetK8sStatus mirrors RequireSuperAdmin. There is no LLM
// call and no meter on this group — these are infra CRUD, so STEP 6 does not
// apply (the router path meters nothing either). Query params (pageSize, p, field,
// value, sortField, sortOrder, id, owner) are decoded from the JSON body — the
// same pattern zapBalanceHandler uses — NEVER trusted for identity.

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/luxfi/zap"

	"github.com/hanzoai/ai/cluster"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

// ── ZAP dispatch tables (group-local, collision-free) ────────────────────
//
// A type ALIAS (not a new named type) so no shared symbol is declared, mirroring
// zap_router-policy-stats.go. init ranges over the two tables below to wire this
// group into the ONE canonical registry (registerCloud(method, h) /
// registerGatewayPath(path, h), zap_registry.go).

type zapApplicationDeployFn = func(ctx context.Context, auth string, body []byte) (*zap.Message, error)

// zapApplicationDeployCloud maps native cloud method → handler (MsgType 100).
// init self-registers this group's dispatch tables into the canonical registry.
func init() {
	for method, h := range zapApplicationDeployCloud {
		registerCloud(method, h)
	}
	for path, h := range zapApplicationDeployGateway {
		registerGatewayPath(path, h)
	}
}

var zapApplicationDeployCloud = map[string]zapApplicationDeployFn{
	"application.list":     zapGetApplicationsHandler,
	"application.get":      zapGetApplicationHandler,
	"application.update":   zapUpdateApplicationHandler,
	"application.add":      zapAddApplicationHandler,
	"application.delete":   zapDeleteApplicationHandler,
	"application.deploy":   zapDeployApplicationHandler,
	"application.undeploy": zapUndeployApplicationHandler,
	"k8s.status":           zapGetK8sStatusHandler,
}

// zapApplicationDeployGateway maps gateway path → handler (MsgType 200).
var zapApplicationDeployGateway = map[string]zapApplicationDeployFn{}

// ── Shared seams (identity + response envelope parity) ───────────────────

// ── get-applications (org-scoped, mirrors GetScopedOwner) ────────────────

// zapListAppParams is the body-decoded projection of the query params.
type zapListAppParams struct {
	Owner     string `json:"owner"`
	PageSize  string `json:"pageSize"`
	P         string `json:"p"`
	Field     string `json:"field"`
	Value     string `json:"value"`
	SortField string `json:"sortField"`
	SortOrder string `json:"sortOrder"`
}

func zapGetApplicationsHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipal(auth)
	if user == nil {
		return zapError(http.StatusUnauthorized, "auth:Please sign in first")
	}

	var p zapListAppParams
	if len(body) > 0 {
		_ = json.Unmarshal(body, &p)
	}

	// GetScopedOwner parity: non-admin is scoped to its own org; the reserved
	// `admin` org may target a specific owner. The scope decision derives ONLY
	// from the verified principal, never from a client owner field for a tenant.
	owner := user.Owner
	if user.Owner == "admin" && strings.TrimSpace(p.Owner) != "" {
		owner = strings.TrimSpace(p.Owner)
	}

	if p.PageSize == "" || p.P == "" {
		applications, err := object.GetApplications(owner)
		if err != nil {
			return zapError(http.StatusOK, err.Error())
		}
		cluster.Describe(applications, "en")
		return zapOk(applications)
	}

	limit := util.ParseInt(p.PageSize)
	page := util.ParseInt(p.P)
	count, err := object.GetApplicationCount(owner, p.Field, p.Value)
	if err != nil {
		return zapError(http.StatusOK, err.Error())
	}
	offset := (page - 1) * limit
	applications, err := object.GetPaginationApplications(owner, offset, limit, p.Field, p.Value, p.SortField, p.SortOrder)
	if err != nil {
		return zapError(http.StatusOK, err.Error())
	}
	cluster.Describe(applications, "en")
	return zapOk(applications, count)
}

// ── get-application ──────────────────────────────────────────────────────

func zapGetApplicationHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if zapPrincipal(auth) == nil {
		return zapError(http.StatusUnauthorized, "auth:Please sign in first")
	}
	var p struct {
		Id string `json:"id"`
	}
	if len(body) > 0 {
		_ = json.Unmarshal(body, &p)
	}

	res, err := object.GetApplication(p.Id)
	if err != nil {
		return zapError(http.StatusOK, err.Error())
	}
	if res != nil {
		cluster.Describe([]*object.Application{res}, "en")
	}
	return zapOk(res)
}

// ── update-application ───────────────────────────────────────────────────

// zapAppPayload decodes the id (the router query param) alongside the Application body.
// A body-supplied id wins; else it is derived from the payload owner/name (the
// same owner/name key UpdateApplication uses).
func zapAppPayload(body []byte) (string, *object.Application, error) {
	var application object.Application
	if err := json.Unmarshal(body, &application); err != nil {
		return "", nil, err
	}
	var idHolder struct {
		Id string `json:"id"`
	}
	_ = json.Unmarshal(body, &idHolder)
	id := idHolder.Id
	if id == "" {
		id = util.GetIdFromOwnerAndName(application.Owner, application.Name)
	}
	return id, &application, nil
}

func zapUpdateApplicationHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if zapPrincipal(auth) == nil {
		return zapError(http.StatusUnauthorized, "auth:Please sign in first")
	}
	id, application, err := zapAppPayload(body)
	if err != nil {
		return zapError(http.StatusOK, err.Error())
	}

	application.Manifest, err = cluster.Manifest(application, "en")
	if err != nil {
		return zapError(http.StatusOK, err.Error())
	}

	success, err := object.UpdateApplication(id, application)
	if err != nil {
		return zapError(http.StatusOK, err.Error())
	}
	return zapOk(success)
}

// ── add-application ──────────────────────────────────────────────────────

func zapAddApplicationHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if zapPrincipal(auth) == nil {
		return zapError(http.StatusUnauthorized, "auth:Please sign in first")
	}
	var application object.Application
	if err := json.Unmarshal(body, &application); err != nil {
		return zapError(http.StatusOK, err.Error())
	}

	if application.Template == "" {
		return zapError(http.StatusOK, "application:Missing required parameters")
	}

	template, err := object.GetTemplate(util.GetIdFromOwnerAndName(application.Owner, application.Template))
	if err != nil {
		return zapError(http.StatusOK, err.Error())
	}
	if template == nil {
		return zapError(http.StatusOK, "application:The Template not found")
	}

	success, err := object.AddApplication(&application)
	if err != nil {
		return zapError(http.StatusOK, err.Error())
	}
	return zapOk(success)
}

// ── delete-application ───────────────────────────────────────────────────

func zapDeleteApplicationHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if zapPrincipal(auth) == nil {
		return zapError(http.StatusUnauthorized, "auth:Please sign in first")
	}
	var application object.Application
	if err := json.Unmarshal(body, &application); err != nil {
		return zapError(http.StatusOK, err.Error())
	}

	// Best-effort teardown of what the record deployed, then drop the record. An
	// org with no Kubernetes provider still deletes its applications.
	_, _ = cluster.Undeploy(application.Owner, application.Name, application.Namespace, "en")

	success, err := object.DeleteApplication(&application)
	if err != nil {
		return zapError(http.StatusOK, err.Error())
	}
	return zapOk(success)
}

// ── deploy-application (update + synchronous deploy) ─────────────────────

func zapDeployApplicationHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if zapPrincipal(auth) == nil {
		return zapError(http.StatusUnauthorized, "auth:Please sign in first")
	}
	id, application, err := zapAppPayload(body)
	if err != nil {
		return zapError(http.StatusOK, err.Error())
	}

	originalApplication, err := object.GetApplication(id)
	if err != nil {
		return zapError(http.StatusOK, err.Error())
	}
	if originalApplication == nil {
		return zapError(http.StatusOK, fmt.Sprintf("The application: %s is not found", id))
	}

	application.Manifest, err = cluster.Manifest(application, "en")
	if err != nil {
		return zapError(http.StatusOK, err.Error())
	}

	success, err := object.UpdateApplication(id, application)
	if err != nil {
		return zapError(http.StatusOK, err.Error())
	}
	if !success {
		return zapError(http.StatusOK, "application:Failed to update application")
	}

	// Parity with UpdateApplication: only the error is checked (the sync deploy's
	// bool is advisory there too).
	_, err = cluster.DeploySync(application, "en")
	if err != nil {
		return zapError(http.StatusOK, err.Error())
	}

	updatedApplication, err := object.GetApplication(id)
	if err != nil {
		return zapError(http.StatusOK, err.Error())
	}
	return zapOk(updatedApplication)
}

// ── undeploy-application (synchronous undeploy) ──────────────────────────

func zapUndeployApplicationHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if zapPrincipal(auth) == nil {
		return zapError(http.StatusUnauthorized, "auth:Please sign in first")
	}
	var p struct {
		Id string `json:"id"`
	}
	if len(body) > 0 {
		_ = json.Unmarshal(body, &p)
	}

	application, err := object.GetApplication(p.Id)
	if err != nil {
		return zapError(http.StatusOK, err.Error())
	}
	if application == nil {
		return zapError(http.StatusOK, fmt.Sprintf("The application: %s is not found", p.Id))
	}

	owner, name, err := util.GetOwnerAndNameFromIdWithError(p.Id)
	if err != nil {
		return zapError(http.StatusOK, err.Error())
	}

	success, err := cluster.UndeploySync(owner, name, application.Namespace, "en")
	if err != nil {
		return zapError(http.StatusOK, err.Error())
	}
	return zapOk(success)
}

// ── get-k8s-status (super admin, mirrors RequireSuperAdmin) ──────────────

func zapGetK8sStatusHandler(_ context.Context, auth string, _ []byte) (*zap.Message, error) {
	user := zapPrincipal(auth)
	if user == nil {
		return zapError(http.StatusUnauthorized, "auth:Please sign in first")
	}
	if !util.IsSuperAdmin(user) {
		return zapError(http.StatusForbidden, "auth:this operation requires super admin privilege")
	}

	status, err := cluster.Status("en")
	if err != nil {
		return zapError(http.StatusOK, err.Error())
	}
	return zapOk(status)
}
