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
// of hospital.go / doctor.go / patient.go / caase.go / consultation.go / form.go /
// form_data.go / article.go / scale.go / system_info.go / prometheus.go /
// activity.go / org_settings.go / agent.go). Each re-implements its beego
// controller's logic against object/ + iam directly — it NEVER wraps the beego
// controller — mirroring controllers/zap_native.go:zapChatHandler and the CRUD
// template in controllers/zap_chat-graph-crud.go.
//
// Identity is resolved from the Bearer credential through the ONE shared seam
// (zapPrincipalUser: verified JWT via object.ParseAndValidateJWT / hk-/pk- IAM key
// via getUserByAccessKey) — never from the body. Org scope is always the resolved
// user's Owner (zapMiscScopedOwner mirrors ApiController.GetScopedOwner: a member of
// the admin org may target a specific ?owner, everyone else is pinned to their own).
//
// Auth parity: the native ZAP path never runs routers/authz_filter.go, so
// zapMiscAuthz re-enforces that filter's permissionFilter decision verbatim for each
// route (super-admin endpoints first, then preview-mode read bypass, the benign-read
// exempt list, and the org-admin gate for the non-exempt reads/writes), then each
// handler re-runs its OWN in-controller check (scoped-owner sign-in, per-row
// ownership via CanEditPatient / username match, RequireAdmin for the prometheus
// surface). This mirrors the exact belt-and-suspenders the HTTP path applies.
//
// Envelope parity: success/error use the SAME {status,msg,data,data2} Response the
// beego ResponseOk / ResponseError emit (the console frontend contract), via the
// group-local zapMiscOk / zapMiscError builders (kept group-local, unique names, so
// they never collide with another group's envelope helpers at Integrate).
//
// Metering: none of these are billable terminal paths (no recordUsage in any beego
// controller in the group), so STEP 6 (meter-once) does not apply here — there is
// deliberately no meter call, exactly like the chat/graph CRUD group.
//
// Registration: this file OWNS its wiring. init() self-registers each route into the
// package-level registries (registerCloud / registerGatewayPath) born in
// zap_audio.go; it never edits a shared registration file. The beego routes in
// router.go stay live in parallel (strangler) until Integrate flips
// handleCloudService / handleGatewayHTTPRequest to consult the registries.
//
// wecom_bot.go is intentionally NOT migrated here: /v1/wecom-bot/callback/:botId is
// a public external WeChat-Work webhook whose inputs are a :botId PATH segment and
// msg_signature/timestamp/nonce/echostr QUERY params carrying an encrypted body —
// none of which the canonical zapHandler(ctx, auth, body) signature can carry, and
// there is no native ZAP client for a WeChat webhook. It stays on beego (the correct
// strangler posture) until a path/query-carrying gateway seam exists (Integrate owns
// that); re-implementing its crypto here uncalled would be dead code.

package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
	registerCloud("hospitals.list", zapGetHospitalsHandler)
	registerCloud("hospital.get", zapGetHospitalHandler)
	registerCloud("hospital.update", zapUpdateHospitalHandler)
	registerCloud("hospital.add", zapAddHospitalHandler)
	registerCloud("hospital.delete", zapDeleteHospitalHandler)

	registerCloud("doctors.list", zapGetDoctorsHandler)
	registerCloud("doctor.get", zapGetDoctorHandler)
	registerCloud("doctor.update", zapUpdateDoctorHandler)
	registerCloud("doctor.add", zapAddDoctorHandler)
	registerCloud("doctor.delete", zapDeleteDoctorHandler)

	registerCloud("patients.list", zapGetPatientsHandler)
	registerCloud("patient.get", zapGetPatientHandler)
	registerCloud("patient.update", zapUpdatePatientHandler)
	registerCloud("patient.add", zapAddPatientHandler)
	registerCloud("patient.delete", zapDeletePatientHandler)

	registerCloud("caases.list", zapGetCaasesHandler)
	registerCloud("caase.get", zapGetCaaseHandler)
	registerCloud("caase.update", zapUpdateCaaseHandler)
	registerCloud("caase.add", zapAddCaaseHandler)
	registerCloud("caase.delete", zapDeleteCaaseHandler)

	registerCloud("consultations.list", zapGetConsultationsHandler)
	registerCloud("consultation.get", zapGetConsultationHandler)
	registerCloud("consultation.update", zapUpdateConsultationHandler)
	registerCloud("consultation.add", zapAddConsultationHandler)
	registerCloud("consultation.delete", zapDeleteConsultationHandler)

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

	registerCloud("org-settings.list", zapGetOrgSettingsListHandler)
	registerCloud("org-settings.get", zapGetOrgSettingsHandler)
	registerCloud("org-settings.add", zapAddOrgSettingsHandler)
	registerCloud("org-settings.update", zapUpdateOrgSettingsHandler)
	registerCloud("org-settings.delete", zapDeleteOrgSettingsHandler)

	registerCloud("agents.dashboard-url", zapGetAgentsDashboardUrlHandler)

	// ── Gateway (MsgType 200) — /v1 path prefixes routing to the SAME handlers ──
	// lookupGatewayHandler resolves by LONGEST matching prefix (path==p or p+"/"),
	// so the singular/plural pairs that share a stem (get-hospital ⊂ get-hospitals,
	// get-form ⊂ get-forms ⊂ get-form-data, get-scale ⊂ get-scales, …) never shadow
	// each other — each registers as its own distinct exact prefix.
	registerGatewayPath("/v1/get-hospitals", zapGetHospitalsHandler)
	registerGatewayPath("/v1/get-hospital", zapGetHospitalHandler)
	registerGatewayPath("/v1/update-hospital", zapUpdateHospitalHandler)
	registerGatewayPath("/v1/add-hospital", zapAddHospitalHandler)
	registerGatewayPath("/v1/delete-hospital", zapDeleteHospitalHandler)

	registerGatewayPath("/v1/get-doctors", zapGetDoctorsHandler)
	registerGatewayPath("/v1/get-doctor", zapGetDoctorHandler)
	registerGatewayPath("/v1/update-doctor", zapUpdateDoctorHandler)
	registerGatewayPath("/v1/add-doctor", zapAddDoctorHandler)
	registerGatewayPath("/v1/delete-doctor", zapDeleteDoctorHandler)

	registerGatewayPath("/v1/get-patients", zapGetPatientsHandler)
	registerGatewayPath("/v1/get-patient", zapGetPatientHandler)
	registerGatewayPath("/v1/update-patient", zapUpdatePatientHandler)
	registerGatewayPath("/v1/add-patient", zapAddPatientHandler)
	registerGatewayPath("/v1/delete-patient", zapDeletePatientHandler)

	registerGatewayPath("/v1/get-caases", zapGetCaasesHandler)
	registerGatewayPath("/v1/get-caase", zapGetCaaseHandler)
	registerGatewayPath("/v1/update-caase", zapUpdateCaaseHandler)
	registerGatewayPath("/v1/add-caase", zapAddCaaseHandler)
	registerGatewayPath("/v1/delete-caase", zapDeleteCaaseHandler)

	registerGatewayPath("/v1/get-consultations", zapGetConsultationsHandler)
	registerGatewayPath("/v1/get-consultation", zapGetConsultationHandler)
	registerGatewayPath("/v1/update-consultation", zapUpdateConsultationHandler)
	registerGatewayPath("/v1/add-consultation", zapAddConsultationHandler)
	registerGatewayPath("/v1/delete-consultation", zapDeleteConsultationHandler)

	registerGatewayPath("/v1/get-global-forms", zapGetGlobalFormsHandler)
	registerGatewayPath("/v1/get-forms", zapGetFormsHandler)
	registerGatewayPath("/v1/get-form-data", zapGetFormDataHandler)
	registerGatewayPath("/v1/get-form", zapGetFormHandler)
	registerGatewayPath("/v1/update-form", zapUpdateFormHandler)
	registerGatewayPath("/v1/add-form", zapAddFormHandler)
	registerGatewayPath("/v1/delete-form", zapDeleteFormHandler)

	registerGatewayPath("/v1/get-global-articles", zapGetGlobalArticlesHandler)
	registerGatewayPath("/v1/get-articles", zapGetArticlesHandler)
	registerGatewayPath("/v1/get-article", zapGetArticleHandler)
	registerGatewayPath("/v1/update-article", zapUpdateArticleHandler)
	registerGatewayPath("/v1/add-article", zapAddArticleHandler)
	registerGatewayPath("/v1/delete-article", zapDeleteArticleHandler)

	registerGatewayPath("/v1/get-global-scales", zapGetGlobalScalesHandler)
	registerGatewayPath("/v1/get-public-scales", zapGetPublicScalesHandler)
	registerGatewayPath("/v1/get-scales", zapGetScalesHandler)
	registerGatewayPath("/v1/get-scale", zapGetScaleHandler)
	registerGatewayPath("/v1/update-scale", zapUpdateScaleHandler)
	registerGatewayPath("/v1/add-scale", zapAddScaleHandler)
	registerGatewayPath("/v1/delete-scale", zapDeleteScaleHandler)

	registerGatewayPath("/v1/get-system-info", zapGetSystemInfoHandler)
	registerGatewayPath("/v1/get-version-info", zapGetVersionInfoHandler)
	registerGatewayPath("/v1/health", zapHealthHandler)
	registerGatewayPath("/v1/get-prometheus-info", zapGetPrometheusInfoHandler)
	registerGatewayPath("/v1/metrics", zapGetMetricsHandler)
	registerGatewayPath("/v1/get-activities", zapGetActivitiesHandler)

	registerGatewayPath("/v1/get-org-settings-list", zapGetOrgSettingsListHandler)
	registerGatewayPath("/v1/get-org-settings", zapGetOrgSettingsHandler)
	registerGatewayPath("/v1/add-org-settings", zapAddOrgSettingsHandler)
	registerGatewayPath("/v1/update-org-settings", zapUpdateOrgSettingsHandler)
	registerGatewayPath("/v1/delete-org-settings", zapDeleteOrgSettingsHandler)

	registerGatewayPath("/v1/get-agents-dashboard-url", zapGetAgentsDashboardUrlHandler)
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
	"get-org-settings-list": {}, "get-org-settings": {},
	"add-org-settings": {}, "update-org-settings": {}, "delete-org-settings": {},
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
// (the beego path minus "/v1/"). Returns a denial to return as-is, or nil to proceed
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

// zapMiscListRequest carries the list/pagination + filter params the beego GET
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

// ── hospital.go parity ─────────────────────────────────────────────────────────

func zapGetHospitalsHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if deny := zapMiscAuthz("get-hospitals", user); deny != nil {
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
		hospitals, err := object.GetMaskedHospitals(object.GetHospitals(owner))
		if err != nil {
			return zapMiscError(200, err.Error())
		}
		return zapMiscOk(hospitals)
	}
	limit := util.ParseInt(req.PageSize)
	count, err := object.GetHospitalCount(owner, req.Field, req.Value)
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	offset := paginationOffset(util.ParseInt(req.Page), limit)
	hospitals, err := object.GetMaskedHospitals(object.GetPaginationHospitals(owner, offset, limit, req.Field, req.Value, req.SortField, req.SortOrder))
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	return zapMiscOk(hospitals, count)
}

func zapGetHospitalHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("get-hospital", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	req, deny := zapMiscDecodeList(body)
	if deny != nil {
		return deny, nil
	}
	hospital, err := object.GetMaskedHospital(object.GetHospital(req.ID))
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	return zapMiscOk(hospital)
}

func zapUpdateHospitalHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("update-hospital", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	var hospital object.Hospital
	if err := json.Unmarshal(body, &hospital); err != nil {
		return zapMiscError(400, "invalid request: "+err.Error())
	}
	id := util.GetId(hospital.Owner, hospital.Name)
	return zapMiscWrapAction(object.UpdateHospital(id, &hospital))
}

func zapAddHospitalHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("add-hospital", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	var hospital object.Hospital
	if err := json.Unmarshal(body, &hospital); err != nil {
		return zapMiscError(400, "invalid request: "+err.Error())
	}
	return zapMiscWrapAction(object.AddHospital(&hospital))
}

func zapDeleteHospitalHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("delete-hospital", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	var hospital object.Hospital
	if err := json.Unmarshal(body, &hospital); err != nil {
		return zapMiscError(400, "invalid request: "+err.Error())
	}
	return zapMiscWrapAction(object.DeleteHospital(&hospital))
}

// zapMiscWrapAction mirrors wrapActionResponse: the affected/unaffected/error
// {status,data} shape the beego wrapActionResponse handlers emit via ServeJSON.
func zapMiscWrapAction(affected bool, err error) (*zap.Message, error) {
	resp := wrapActionResponse(affected, err)
	body, _ := json.Marshal(resp)
	return object.BuildCloudResponse(200, body, "")
}

// ── doctor.go parity ───────────────────────────────────────────────────────────

func zapGetDoctorsHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if deny := zapMiscAuthz("get-doctors", user); deny != nil {
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
		doctors, err := object.GetMaskedDoctors(object.GetDoctors(owner))
		if err != nil {
			return zapMiscError(200, err.Error())
		}
		return zapMiscOk(doctors)
	}
	limit := util.ParseInt(req.PageSize)
	count, err := object.GetDoctorCount(owner, req.Field, req.Value)
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	offset := paginationOffset(util.ParseInt(req.Page), limit)
	doctors, err := object.GetMaskedDoctors(object.GetPaginationDoctors(owner, offset, limit, req.Field, req.Value, req.SortField, req.SortOrder))
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	return zapMiscOk(doctors, count)
}

func zapGetDoctorHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("get-doctor", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	req, deny := zapMiscDecodeList(body)
	if deny != nil {
		return deny, nil
	}
	doctor, err := object.GetMaskedDoctor(object.GetDoctor(req.ID))
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	return zapMiscOk(doctor)
}

func zapUpdateDoctorHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("update-doctor", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	var doctor object.Doctor
	if err := json.Unmarshal(body, &doctor); err != nil {
		return zapMiscError(400, "invalid request: "+err.Error())
	}
	id := util.GetId(doctor.Owner, doctor.Name)
	return zapMiscWrapAction(object.UpdateDoctor(id, &doctor))
}

func zapAddDoctorHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("add-doctor", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	var doctor object.Doctor
	if err := json.Unmarshal(body, &doctor); err != nil {
		return zapMiscError(400, "invalid request: "+err.Error())
	}
	return zapMiscWrapAction(object.AddDoctor(&doctor))
}

func zapDeleteDoctorHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("delete-doctor", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	var doctor object.Doctor
	if err := json.Unmarshal(body, &doctor); err != nil {
		return zapMiscError(400, "invalid request: "+err.Error())
	}
	return zapMiscWrapAction(object.DeleteDoctor(&doctor))
}

// ── patient.go parity (adds per-row FilterPatientsByUser / CanEditPatient) ──────

func zapGetPatientsHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if deny := zapMiscAuthz("get-patients", user); deny != nil {
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
		patients, err := object.GetMaskedPatients(object.GetPatients(owner))
		if err != nil {
			return zapMiscError(200, err.Error())
		}
		patients = object.FilterPatientsByUser(user, patients)
		return zapMiscOk(patients)
	}
	limit := util.ParseInt(req.PageSize)
	count, err := object.GetPatientCount(owner, req.Field, req.Value)
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	offset := paginationOffset(util.ParseInt(req.Page), limit)
	patients, err := object.GetMaskedPatients(object.GetPaginationPatients(owner, offset, limit, req.Field, req.Value, req.SortField, req.SortOrder))
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	patients = object.FilterPatientsByUser(user, patients)
	return zapMiscOk(patients, count)
}

func zapGetPatientHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("get-patient", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	req, deny := zapMiscDecodeList(body)
	if deny != nil {
		return deny, nil
	}
	patient, err := object.GetMaskedPatient(object.GetPatient(req.ID))
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	return zapMiscOk(patient)
}

func zapUpdatePatientHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if deny := zapMiscAuthz("update-patient", user); deny != nil {
		return deny, nil
	}
	var patient object.Patient
	if err := json.Unmarshal(body, &patient); err != nil {
		return zapMiscError(400, "invalid request: "+err.Error())
	}
	if !object.CanEditPatient(user, &patient) {
		return zapMiscError(403, "Unauthorized operation")
	}
	id := util.GetId(patient.Owner, patient.Name)
	return zapMiscWrapAction(object.UpdatePatient(id, &patient))
}

func zapAddPatientHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("add-patient", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	var patient object.Patient
	if err := json.Unmarshal(body, &patient); err != nil {
		return zapMiscError(400, "invalid request: "+err.Error())
	}
	if patient.Owners == nil {
		patient.Owners = []string{}
	}
	return zapMiscWrapAction(object.AddPatient(&patient))
}

func zapDeletePatientHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if deny := zapMiscAuthz("delete-patient", user); deny != nil {
		return deny, nil
	}
	var patient object.Patient
	if err := json.Unmarshal(body, &patient); err != nil {
		return zapMiscError(400, "invalid request: "+err.Error())
	}
	if !object.CanEditPatient(user, &patient) {
		return zapMiscError(403, "Unauthorized operation")
	}
	return zapMiscWrapAction(object.DeletePatient(&patient))
}

// ── caase.go parity (adds per-row FilterCaasesByUser) ───────────────────────────

func zapGetCaasesHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if deny := zapMiscAuthz("get-caases", user); deny != nil {
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
		caases, err := object.GetMaskedCaases(object.GetCaases(owner))
		if err != nil {
			return zapMiscError(200, err.Error())
		}
		caases = object.FilterCaasesByUser(user, caases)
		return zapMiscOk(caases)
	}
	limit := util.ParseInt(req.PageSize)
	count, err := object.GetCaaseCount(owner, req.Field, req.Value)
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	offset := paginationOffset(util.ParseInt(req.Page), limit)
	caases, err := object.GetMaskedCaases(object.GetPaginationCaases(owner, offset, limit, req.Field, req.Value, req.SortField, req.SortOrder))
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	caases = object.FilterCaasesByUser(user, caases)
	return zapMiscOk(caases, count)
}

func zapGetCaaseHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("get-caase", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	req, deny := zapMiscDecodeList(body)
	if deny != nil {
		return deny, nil
	}
	caase, err := object.GetMaskedCaase(object.GetCaase(req.ID))
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	return zapMiscOk(caase)
}

func zapUpdateCaaseHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("update-caase", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	var caase object.Caase
	if err := json.Unmarshal(body, &caase); err != nil {
		return zapMiscError(400, "invalid request: "+err.Error())
	}
	id := util.GetId(caase.Owner, caase.Name)
	return zapMiscWrapAction(object.UpdateCaase(id, &caase))
}

func zapAddCaaseHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("add-caase", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	var caase object.Caase
	if err := json.Unmarshal(body, &caase); err != nil {
		return zapMiscError(400, "invalid request: "+err.Error())
	}
	return zapMiscWrapAction(object.AddCaase(&caase))
}

func zapDeleteCaaseHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("delete-caase", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	var caase object.Caase
	if err := json.Unmarshal(body, &caase); err != nil {
		return zapMiscError(400, "invalid request: "+err.Error())
	}
	return zapMiscWrapAction(object.DeleteCaase(&caase))
}

// ── consultation.go parity (adds per-row FilterConsultationsByUser) ─────────────

func zapGetConsultationsHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if deny := zapMiscAuthz("get-consultations", user); deny != nil {
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
		consultations, err := object.GetMaskedConsultations(object.GetConsultations(owner))
		if err != nil {
			return zapMiscError(200, err.Error())
		}
		consultations = object.FilterConsultationsByUser(user, consultations)
		return zapMiscOk(consultations)
	}
	limit := util.ParseInt(req.PageSize)
	count, err := object.GetConsultationCount(owner, req.Field, req.Value)
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	offset := paginationOffset(util.ParseInt(req.Page), limit)
	consultations, err := object.GetMaskedConsultations(object.GetPaginationConsultations(owner, offset, limit, req.Field, req.Value, req.SortField, req.SortOrder))
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	consultations = object.FilterConsultationsByUser(user, consultations)
	return zapMiscOk(consultations, count)
}

func zapGetConsultationHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("get-consultation", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	req, deny := zapMiscDecodeList(body)
	if deny != nil {
		return deny, nil
	}
	consultation, err := object.GetMaskedConsultation(object.GetConsultation(req.ID))
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	return zapMiscOk(consultation)
}

func zapUpdateConsultationHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("update-consultation", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	var consultation object.Consultation
	if err := json.Unmarshal(body, &consultation); err != nil {
		return zapMiscError(400, "invalid request: "+err.Error())
	}
	id := util.GetId(consultation.Owner, consultation.Name)
	return zapMiscWrapAction(object.UpdateConsultation(id, &consultation))
}

func zapAddConsultationHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("add-consultation", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	var consultation object.Consultation
	if err := json.Unmarshal(body, &consultation); err != nil {
		return zapMiscError(400, "invalid request: "+err.Error())
	}
	return zapMiscWrapAction(object.AddConsultation(&consultation))
}

func zapDeleteConsultationHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("delete-consultation", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	var consultation object.Consultation
	if err := json.Unmarshal(body, &consultation); err != nil {
		return zapMiscError(400, "invalid request: "+err.Error())
	}
	return zapMiscWrapAction(object.DeleteConsultation(&consultation))
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
	// pass it through verbatim (the beego handler writes it to the body directly).
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
	// The beego controller requires a signed-in username (GetSessionUsername != "").
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
// scraper reads it verbatim, exactly like the beego ServeHTTP path.
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

// ── org_settings.go parity (super-admin gated, upsert on update) ────────────────

func zapGetOrgSettingsListHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("get-org-settings-list", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	req, deny := zapMiscDecodeList(body)
	if deny != nil {
		return deny, nil
	}
	owner := req.Owner
	if owner == "" {
		owner = "admin"
	}
	settings, err := object.GetOrgSettingsList(owner)
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	return zapMiscOk(settings)
}

func zapGetOrgSettingsHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("get-org-settings", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	req, deny := zapMiscDecodeList(body)
	if deny != nil {
		return deny, nil
	}
	if req.Owner == "" {
		return zapMiscError(200, "owner is required")
	}
	settings, err := object.GetOrgSettings(req.Owner)
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	return zapMiscOk(settings)
}

func zapAddOrgSettingsHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("add-org-settings", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	var settings object.OrgSettings
	if err := json.Unmarshal(body, &settings); err != nil {
		return zapMiscError(400, "invalid request: "+err.Error())
	}
	if settings.Owner == "" {
		return zapMiscError(200, "owner is required")
	}
	success, err := object.AddOrgSettings(&settings)
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	return zapMiscOk(success)
}

// zapUpdateOrgSettingsRequest carries the owner (URL query in HTTP) plus the settings
// payload; the beego handler reads ?owner and the JSON body separately.
type zapUpdateOrgSettingsRequest struct {
	Owner    string             `json:"owner"`
	Settings object.OrgSettings `json:"settings"`
}

func zapUpdateOrgSettingsHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("update-org-settings", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	// Accept both the {owner, settings} envelope and a bare OrgSettings body (whose
	// own Owner field then supplies the key), so either caller shape upserts.
	var req zapUpdateOrgSettingsRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return zapMiscError(400, "invalid request: "+err.Error())
	}
	settings := req.Settings
	owner := req.Owner
	if owner == "" {
		owner = settings.Owner
	}
	if owner == "" {
		return zapMiscError(200, "owner is required")
	}

	existing, err := object.GetOrgSettings(owner)
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	var success bool
	if existing == nil {
		settings.Owner = owner
		success, err = object.AddOrgSettings(&settings)
	} else {
		success, err = object.UpdateOrgSettings(owner, &settings)
	}
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	return zapMiscOk(success)
}

func zapDeleteOrgSettingsHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if deny := zapMiscAuthz("delete-org-settings", zapPrincipalUser(auth)); deny != nil {
		return deny, nil
	}
	var settings object.OrgSettings
	if err := json.Unmarshal(body, &settings); err != nil {
		return zapMiscError(400, "invalid request: "+err.Error())
	}
	success, err := object.DeleteOrgSettings(&settings)
	if err != nil {
		return zapMiscError(200, err.Error())
	}
	return zapMiscOk(success)
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
