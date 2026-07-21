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

// Native ZAP handlers for the security assets / scans / patch / permission
// route-group — the strangler migration of asset.go, scan.go, patch.go and
// permission.go off beego.
//
// STRANGLER: each beego method (ApiController.GetAssets, GetAsset, UpdateAsset,
// AddAsset, DeleteAsset, ScanAsset, ScanAssets, GetScans, GetScan, UpdateScan,
// AddScan, DeleteScan, InstallPatch, GetPermissions, GetPermission,
// UpdatePermission, AddPermission, DeletePermission) is re-implemented here as a
// pure ZAP handler against object/ + iam directly — never wrapping the beego
// controller — mirroring zap_native.go:zapChatHandler and the app-deploy group
// (zap_application-deploy.go). The beego routes in router.go stay live in
// parallel (strangler) until Integrate flips native dispatch to these.
//
// Identity/scope parity: these endpoints have NO session filter on the ZAP path
// (there is no beego filter chain), so every handler fails closed — a verified
// credential is required (STEP 1). None of these routes is a super-admin or
// balance-gated endpoint (they are absent from authz_filter.go
// superAdminEndpoints / authRequiredEndpoints), and the beego controllers only
// require sign-in: the list handlers scope by owner via GetScopedOwner (owner =
// principal.Owner; the reserved `admin` org may target another owner), the rest
// require a verified principal and pass through 1:1. There is no LLM call and no
// meter on this group — these are security/infra CRUD, so STEP 6 does not apply
// (the beego path meters nothing either). Query params (pageSize, p, field,
// value, sortField, sortOrder, id, owner, provider, …) are decoded from the JSON
// body — the same pattern zapBalanceHandler / zap_application-deploy.go use —
// NEVER trusted for identity.
//
// REGISTRATION: this file self-registers from its own init() via the shared
// registry seam (registerCloud / registerGatewayPath, defined once in
// zap_audio.go). It declares NO shared symbol and edits NO shared file, so it
// never contends with another group's file. Handlers use the canonical native
// handler signature func(ctx, auth string, body []byte) (*zap.Message, error).

package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/luxfi/zap"

	iam "github.com/hanzoai/iam-v1"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

func init() { registerZapSecurityAssetsScans() }

// registerZapSecurityAssetsScans wires this group's routes into the shared
// dispatch registry. Invoked from init() so the group self-registers from its
// own file; InitZapHandlers stays the single Node.Handle bootstrap and gains no
// per-group arm. Named so init-order can be pinned explicitly if ever needed.
func registerZapSecurityAssetsScans() {
	// Cloud (MsgType 100) — native method names.
	registerCloud("assets.list", zapGetAssetsHandler)
	registerCloud("asset.get", zapGetAssetHandler)
	registerCloud("asset.update", zapUpdateAssetHandler)
	registerCloud("asset.add", zapAddAssetHandler)
	registerCloud("asset.delete", zapDeleteAssetHandler)
	registerCloud("asset.scan", zapScanAssetHandler)
	registerCloud("assets.scan", zapScanAssetsHandler)

	registerCloud("scans.list", zapGetScansHandler)
	registerCloud("scan.get", zapGetScanHandler)
	registerCloud("scan.update", zapUpdateScanHandler)
	registerCloud("scan.add", zapAddScanHandler)
	registerCloud("scan.delete", zapDeleteScanHandler)

	registerCloud("patch.install", zapInstallPatchHandler)

	registerCloud("permissions.list", zapGetPermissionsHandler)
	registerCloud("permission.get", zapGetPermissionHandler)
	registerCloud("permission.update", zapUpdatePermissionHandler)
	registerCloud("permission.add", zapAddPermissionHandler)
	registerCloud("permission.delete", zapDeletePermissionHandler)

	// Gateway (MsgType 200) — /v1 path prefixes routing to the SAME handlers.
	// lookupGatewayHandler resolves by longest matching prefix with a "+/" guard,
	// so "/v1/scan-asset" never shadows "/v1/scan-assets" and "/v1/get-asset"
	// never shadows "/v1/get-assets".
	registerGatewayPath("/v1/get-assets", zapGetAssetsHandler)
	registerGatewayPath("/v1/get-asset", zapGetAssetHandler)
	registerGatewayPath("/v1/update-asset", zapUpdateAssetHandler)
	registerGatewayPath("/v1/add-asset", zapAddAssetHandler)
	registerGatewayPath("/v1/delete-asset", zapDeleteAssetHandler)
	registerGatewayPath("/v1/scan-assets", zapScanAssetsHandler)
	registerGatewayPath("/v1/scan-asset", zapScanAssetHandler)

	registerGatewayPath("/v1/get-scans", zapGetScansHandler)
	registerGatewayPath("/v1/get-scan", zapGetScanHandler)
	registerGatewayPath("/v1/update-scan", zapUpdateScanHandler)
	registerGatewayPath("/v1/add-scan", zapAddScanHandler)
	registerGatewayPath("/v1/delete-scan", zapDeleteScanHandler)

	registerGatewayPath("/v1/install-patch", zapInstallPatchHandler)

	registerGatewayPath("/v1/get-permissions", zapGetPermissionsHandler)
	registerGatewayPath("/v1/get-permission", zapGetPermissionHandler)
	registerGatewayPath("/v1/update-permission", zapUpdatePermissionHandler)
	registerGatewayPath("/v1/add-permission", zapAddPermissionHandler)
	registerGatewayPath("/v1/delete-permission", zapDeletePermissionHandler)
}

// ── Shared seams (group-local: identity + response envelope parity) ──────────

// zapSecPrincipal resolves the request principal STRICTLY from its verified
// credential — the ZAP analogue of the session/GetScopedOwner principal (there
// is no session cookie on the ZAP path). hk-/pk- IAM keys route through
// getUserByAccessKey; JWTs through object.ParseAndValidateJWT (signature +
// iss/aud, never raw iam.ParseJwtToken). Returns nil for an empty / invalid /
// unsupported credential — fail-secure.
func zapSecPrincipal(auth string) *iam.User {
	token := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	if token == "" {
		return nil
	}
	if isIAMApiKey(token) {
		if user, err := getUserByAccessKey(token); err == nil && user != nil {
			return user
		}
		return nil
	}
	if isJwtToken(token) {
		if claims, err := object.ParseAndValidateJWT(token); err == nil && claims != nil {
			u := claims.User
			return &u
		}
	}
	return nil
}

// zapSecOk renders the beego ResponseOk envelope ({status:"ok", data[, data2]})
// as a 200 cloud response, preserving the exact JSON shape the HTTP handlers
// return. The variadic mirrors ResponseOk: a second arg becomes Data2 (the
// pagination count for the list handlers).
func zapSecOk(data ...interface{}) (*zap.Message, error) {
	resp := Response{Status: "ok"}
	switch len(data) {
	case 2:
		resp.Data2 = data[1]
		fallthrough
	case 1:
		resp.Data = data[0]
	}
	b, _ := json.Marshal(resp)
	return object.BuildCloudResponse(http.StatusOK, b, "")
}

// zapSecErr renders the beego ResponseError envelope ({status:"error", msg}) at
// the given status. Business errors mirror ResponseError (HTTP 200 body); auth
// failures use 401 like ResponseUnauthorized.
func zapSecErr(status int, msg string) (*zap.Message, error) {
	b, _ := json.Marshal(Response{Status: "error", Msg: msg})
	return object.BuildCloudResponse(uint32(status), b, msg)
}

// zapSecScopedOwner mirrors ApiController.GetScopedOwner: a non-admin principal
// is always scoped to its own org; the reserved `admin` org may target a
// specific owner via the body's `owner` field. The scope decision derives ONLY
// from the verified principal, never from a client owner field for a tenant.
func zapSecScopedOwner(user *iam.User, requestedOwner string) string {
	if user.Owner == "admin" {
		if o := strings.TrimSpace(requestedOwner); o != "" {
			return o
		}
	}
	return user.Owner
}

// ── asset.go parity ──────────────────────────────────────────────────────────

// zapListParams is the body-decoded projection of the beego query params shared
// by the asset/scan list handlers.
type zapListParams struct {
	Owner     string `json:"owner"`
	PageSize  string `json:"pageSize"`
	P         string `json:"p"`
	Field     string `json:"field"`
	Value     string `json:"value"`
	SortField string `json:"sortField"`
	SortOrder string `json:"sortOrder"`
	Asset     string `json:"asset"` // scan list: filter by asset name
}

// zapGetAssetsHandler mirrors ApiController.GetAssets (org-scoped via
// GetScopedOwner, masked, list-vs-paginate branch on pageSize/p).
func zapGetAssetsHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapSecPrincipal(auth)
	if user == nil {
		return zapSecErr(http.StatusUnauthorized, "auth:Please sign in first")
	}
	var p zapListParams
	if len(body) > 0 {
		if err := json.Unmarshal(body, &p); err != nil {
			return zapSecErr(http.StatusBadRequest, "invalid request: "+err.Error())
		}
	}
	owner := zapSecScopedOwner(user, p.Owner)

	if p.PageSize == "" || p.P == "" {
		assets, err := object.GetAssets(owner)
		if err != nil {
			return zapSecErr(http.StatusOK, err.Error())
		}
		return zapSecOk(object.GetMaskedAssets(assets, true))
	}

	limit := util.ParseInt(p.PageSize)
	page := util.ParseInt(p.P)
	count, err := object.GetAssetCount(owner, p.Field, p.Value)
	if err != nil {
		return zapSecErr(http.StatusOK, err.Error())
	}
	offset := (page - 1) * limit
	if offset < 0 {
		offset = 0
	}
	assets, err := object.GetPaginationAssets(owner, offset, limit, p.Field, p.Value, p.SortField, p.SortOrder)
	if err != nil {
		return zapSecErr(http.StatusOK, err.Error())
	}
	return zapSecOk(object.GetMaskedAssets(assets, true), count)
}

// zapSecIDRequest carries a single `id` (owner/name) over the native body — the
// ZAP twin of the beego URL query param.
type zapSecIDRequest struct {
	ID string `json:"id"`
}

// zapGetAssetHandler mirrors ApiController.GetAsset.
func zapGetAssetHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if zapSecPrincipal(auth) == nil {
		return zapSecErr(http.StatusUnauthorized, "auth:Please sign in first")
	}
	var req zapSecIDRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return zapSecErr(http.StatusBadRequest, "invalid request: "+err.Error())
		}
	}
	asset, err := object.GetAsset(req.ID)
	if err != nil {
		return zapSecErr(http.StatusOK, err.Error())
	}
	return zapSecOk(object.GetMaskedAsset(asset, true))
}

// zapUpdateAssetRequest carries the UpdateAsset id (URL query in HTTP) plus the
// asset payload over the native body.
type zapUpdateAssetRequest struct {
	ID    string       `json:"id"`
	Asset object.Asset `json:"asset"`
}

// zapUpdateAssetHandler mirrors ApiController.UpdateAsset.
func zapUpdateAssetHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if zapSecPrincipal(auth) == nil {
		return zapSecErr(http.StatusUnauthorized, "auth:Please sign in first")
	}
	var req zapUpdateAssetRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return zapSecErr(http.StatusBadRequest, "invalid request: "+err.Error())
	}
	success, err := object.UpdateAsset(req.ID, &req.Asset)
	if err != nil {
		return zapSecErr(http.StatusOK, err.Error())
	}
	return zapSecOk(success)
}

// zapAddAssetHandler mirrors ApiController.AddAsset.
func zapAddAssetHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if zapSecPrincipal(auth) == nil {
		return zapSecErr(http.StatusUnauthorized, "auth:Please sign in first")
	}
	var asset object.Asset
	if err := json.Unmarshal(body, &asset); err != nil {
		return zapSecErr(http.StatusBadRequest, "invalid request: "+err.Error())
	}
	success, err := object.AddAsset(&asset)
	if err != nil {
		return zapSecErr(http.StatusOK, err.Error())
	}
	return zapSecOk(success)
}

// zapDeleteAssetHandler mirrors ApiController.DeleteAsset.
func zapDeleteAssetHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if zapSecPrincipal(auth) == nil {
		return zapSecErr(http.StatusUnauthorized, "auth:Please sign in first")
	}
	var asset object.Asset
	if err := json.Unmarshal(body, &asset); err != nil {
		return zapSecErr(http.StatusBadRequest, "invalid request: "+err.Error())
	}
	success, err := object.DeleteAsset(&asset)
	if err != nil {
		return zapSecErr(http.StatusOK, err.Error())
	}
	return zapSecOk(success)
}

// zapScanAssetRequest carries the ScanAsset params (beego URL query) over the
// native body.
type zapScanAssetRequest struct {
	Provider   string `json:"provider"`
	Scan       string `json:"scan"`
	TargetMode string `json:"targetMode"`
	Target     string `json:"target"`
	Asset      string `json:"asset"`
	Command    string `json:"command"`
	SaveToScan bool   `json:"saveToScan"`
}

// zapScanAssetHandler mirrors ApiController.ScanAsset. The Accept-Language the
// HTTP path passes has no analogue on the ZAP wire, so language defaults to "en"
// (parity with zapChatHandler).
func zapScanAssetHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if zapSecPrincipal(auth) == nil {
		return zapSecErr(http.StatusUnauthorized, "auth:Please sign in first")
	}
	var req zapScanAssetRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return zapSecErr(http.StatusBadRequest, "invalid request: "+err.Error())
	}
	scanResult, err := object.ScanAsset(req.Provider, req.Scan, req.TargetMode, req.Target, req.Asset, req.Command, req.SaveToScan, "en")
	if err != nil {
		return zapSecErr(http.StatusOK, err.Error())
	}
	return zapSecOk(scanResult)
}

// zapScanAssetsRequest carries the ScanAssets params over the native body. Owner
// is scoped from the principal (GetScopedOwner parity), never trusted from the
// body for a tenant.
type zapScanAssetsRequest struct {
	Owner    string `json:"owner"`
	Provider string `json:"provider"`
}

// zapScanAssetsHandler mirrors ApiController.ScanAssets.
func zapScanAssetsHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapSecPrincipal(auth)
	if user == nil {
		return zapSecErr(http.StatusUnauthorized, "auth:Please sign in first")
	}
	var req zapScanAssetsRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return zapSecErr(http.StatusBadRequest, "invalid request: "+err.Error())
		}
	}
	owner := zapSecScopedOwner(user, req.Owner)
	success, err := object.ScanAssetsFromProvider(owner, req.Provider)
	if err != nil {
		return zapSecErr(http.StatusOK, err.Error())
	}
	return zapSecOk(success)
}

// ── scan.go parity ───────────────────────────────────────────────────────────

// zapGetScansHandler mirrors ApiController.GetScans (org-scoped, optional asset
// filter, list-vs-paginate branch on pageSize/p).
func zapGetScansHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapSecPrincipal(auth)
	if user == nil {
		return zapSecErr(http.StatusUnauthorized, "auth:Please sign in first")
	}
	var p zapListParams
	if len(body) > 0 {
		if err := json.Unmarshal(body, &p); err != nil {
			return zapSecErr(http.StatusBadRequest, "invalid request: "+err.Error())
		}
	}
	owner := zapSecScopedOwner(user, p.Owner)

	// Asset filter short-circuit (parity with GetScansByAsset).
	if p.Asset != "" {
		scans, err := object.GetScansByAsset(owner, p.Asset)
		if err != nil {
			return zapSecErr(http.StatusOK, err.Error())
		}
		return zapSecOk(scans)
	}

	if p.PageSize == "" || p.P == "" {
		scans, err := object.GetScans(owner)
		if err != nil {
			return zapSecErr(http.StatusOK, err.Error())
		}
		return zapSecOk(scans)
	}

	limit := util.ParseInt(p.PageSize)
	page := util.ParseInt(p.P)
	count, err := object.GetScanCount(owner, p.Field, p.Value)
	if err != nil {
		return zapSecErr(http.StatusOK, err.Error())
	}
	offset := (page - 1) * limit
	if offset < 0 {
		offset = 0
	}
	scans, err := object.GetPaginationScans(owner, offset, limit, p.Field, p.Value, p.SortField, p.SortOrder)
	if err != nil {
		return zapSecErr(http.StatusOK, err.Error())
	}
	return zapSecOk(scans, count)
}

// zapGetScanHandler mirrors ApiController.GetScan.
func zapGetScanHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if zapSecPrincipal(auth) == nil {
		return zapSecErr(http.StatusUnauthorized, "auth:Please sign in first")
	}
	var req zapSecIDRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return zapSecErr(http.StatusBadRequest, "invalid request: "+err.Error())
		}
	}
	scan, err := object.GetScan(req.ID)
	if err != nil {
		return zapSecErr(http.StatusOK, err.Error())
	}
	return zapSecOk(scan)
}

// zapUpdateScanRequest carries the UpdateScan id plus the scan payload.
type zapUpdateScanRequest struct {
	ID   string      `json:"id"`
	Scan object.Scan `json:"scan"`
}

// zapUpdateScanHandler mirrors ApiController.UpdateScan.
func zapUpdateScanHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if zapSecPrincipal(auth) == nil {
		return zapSecErr(http.StatusUnauthorized, "auth:Please sign in first")
	}
	var req zapUpdateScanRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return zapSecErr(http.StatusBadRequest, "invalid request: "+err.Error())
	}
	success, err := object.UpdateScan(req.ID, &req.Scan)
	if err != nil {
		return zapSecErr(http.StatusOK, err.Error())
	}
	return zapSecOk(success)
}

// zapAddScanHandler mirrors ApiController.AddScan.
func zapAddScanHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if zapSecPrincipal(auth) == nil {
		return zapSecErr(http.StatusUnauthorized, "auth:Please sign in first")
	}
	var scan object.Scan
	if err := json.Unmarshal(body, &scan); err != nil {
		return zapSecErr(http.StatusBadRequest, "invalid request: "+err.Error())
	}
	success, err := object.AddScan(&scan)
	if err != nil {
		return zapSecErr(http.StatusOK, err.Error())
	}
	return zapSecOk(success)
}

// zapDeleteScanHandler mirrors ApiController.DeleteScan.
func zapDeleteScanHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if zapSecPrincipal(auth) == nil {
		return zapSecErr(http.StatusUnauthorized, "auth:Please sign in first")
	}
	var scan object.Scan
	if err := json.Unmarshal(body, &scan); err != nil {
		return zapSecErr(http.StatusBadRequest, "invalid request: "+err.Error())
	}
	success, err := object.DeleteScan(&scan)
	if err != nil {
		return zapSecErr(http.StatusOK, err.Error())
	}
	return zapSecOk(success)
}

// ── patch.go parity ──────────────────────────────────────────────────────────

// zapInstallPatchRequest carries the InstallPatch params over the native body.
type zapInstallPatchRequest struct {
	Provider string `json:"provider"`
	PatchId  string `json:"patchId"`
	Scan     string `json:"scan"`
}

// zapInstallPatchHandler mirrors ApiController.InstallPatch: validates the OS
// Patch provider, loads the scan, stamps the async install command + Pending
// state (clearing prior results), and persists it — 1:1 with the beego path.
func zapInstallPatchHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if zapSecPrincipal(auth) == nil {
		return zapSecErr(http.StatusUnauthorized, "auth:Please sign in first")
	}
	var req zapInstallPatchRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return zapSecErr(http.StatusBadRequest, "invalid request: "+err.Error())
	}

	if req.Provider == "" {
		return zapSecErr(http.StatusOK, "Provider is required")
	}
	if req.PatchId == "" {
		return zapSecErr(http.StatusOK, "Patch ID is required")
	}
	if req.Scan == "" {
		return zapSecErr(http.StatusOK, "Scan parameter is required")
	}

	providerId := util.GetIdFromOwnerAndName("admin", req.Provider)
	provider, err := object.GetProvider(providerId)
	if err != nil {
		return zapSecErr(http.StatusOK, err.Error())
	}
	if provider.Type != "OS Patch" {
		return zapSecErr(http.StatusOK, "Provider must be of type 'OS Patch'")
	}

	scanObj, err := object.GetScan(req.Scan)
	if err != nil {
		return zapSecErr(http.StatusOK, err.Error())
	}
	if scanObj == nil {
		return zapSecErr(http.StatusOK, "Scan not found")
	}

	scanObj.Command = "install:" + req.PatchId
	scanObj.State = "Pending"
	scanObj.UpdatedTime = util.GetCurrentTime()
	scanObj.Runner = ""
	scanObj.ErrorText = ""
	scanObj.RawResult = ""
	scanObj.Result = ""
	scanObj.ResultSummary = ""

	if _, err := object.UpdateScan(req.Scan, scanObj); err != nil {
		return zapSecErr(http.StatusOK, err.Error())
	}

	return zapSecOk(map[string]interface{}{"status": "Pending"})
}

// ── permission.go parity ─────────────────────────────────────────────────────

// zapGetPermissionsHandler mirrors ApiController.GetPermissions.
func zapGetPermissionsHandler(_ context.Context, auth string, _ []byte) (*zap.Message, error) {
	if zapSecPrincipal(auth) == nil {
		return zapSecErr(http.StatusUnauthorized, "auth:Please sign in first")
	}
	permissions, err := iam.GetPermissions()
	if err != nil {
		return zapSecErr(http.StatusOK, err.Error())
	}
	return zapSecOk(permissions)
}

// zapGetPermissionHandler mirrors ApiController.GetPermission (id → name split,
// then iam.GetPermission by name).
func zapGetPermissionHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if zapSecPrincipal(auth) == nil {
		return zapSecErr(http.StatusUnauthorized, "auth:Please sign in first")
	}
	var req zapSecIDRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return zapSecErr(http.StatusBadRequest, "invalid request: "+err.Error())
		}
	}
	_, name, err := util.GetOwnerAndNameFromIdWithError(req.ID)
	if err != nil {
		return zapSecErr(http.StatusOK, err.Error())
	}
	permission, err := iam.GetPermission(name)
	if err != nil {
		return zapSecErr(http.StatusOK, err.Error())
	}
	return zapSecOk(permission)
}

// zapUpdatePermissionHandler mirrors ApiController.UpdatePermission.
func zapUpdatePermissionHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if zapSecPrincipal(auth) == nil {
		return zapSecErr(http.StatusUnauthorized, "auth:Please sign in first")
	}
	var permission iam.Permission
	if err := json.Unmarshal(body, &permission); err != nil {
		return zapSecErr(http.StatusBadRequest, "invalid request: "+err.Error())
	}
	success, err := iam.UpdatePermission(&permission)
	if err != nil {
		return zapSecErr(http.StatusOK, err.Error())
	}
	return zapSecOk(success)
}

// zapAddPermissionHandler mirrors ApiController.AddPermission.
func zapAddPermissionHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if zapSecPrincipal(auth) == nil {
		return zapSecErr(http.StatusUnauthorized, "auth:Please sign in first")
	}
	var permission iam.Permission
	if err := json.Unmarshal(body, &permission); err != nil {
		return zapSecErr(http.StatusBadRequest, "invalid request: "+err.Error())
	}
	success, err := iam.AddPermission(&permission)
	if err != nil {
		return zapSecErr(http.StatusOK, err.Error())
	}
	return zapSecOk(success)
}

// zapDeletePermissionHandler mirrors ApiController.DeletePermission.
func zapDeletePermissionHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if zapSecPrincipal(auth) == nil {
		return zapSecErr(http.StatusUnauthorized, "auth:Please sign in first")
	}
	var permission iam.Permission
	if err := json.Unmarshal(body, &permission); err != nil {
		return zapSecErr(http.StatusBadRequest, "invalid request: "+err.Error())
	}
	success, err := iam.DeletePermission(&permission)
	if err != nil {
		return zapSecErr(http.StatusOK, err.Error())
	}
	return zapSecOk(success)
}
