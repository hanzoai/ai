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

// Native ZAP handlers for the router-policy / router-stats / routing group.
//
// STRANGLER: each beego method (ApiController.Get/UpdateRouterPolicy,
// GetRouterStats, PublishRouterArtifactMeta, GetRoutingDefaults,
// ExportRoutingLedger, AddRoutingReward, ExportRoutingRewards, GetTrafficGlobe,
// Get/UpdateTrainingContribution) is re-implemented here as a pure ZAP handler
// against object/ + iam, mirroring zap_native.go:zapChatHandler. The beego
// routes stay live in router.go until Integrate flips dispatch to these.
//
// Registration convention (recipe): this group exposes two group-local dispatch
// tables (zapRouterPolicyStatsCloud / …Gateway) keyed by native cloud method and
// gateway path, via a private type ALIAS — mirroring zap_responses.go. It declares
// NO shared symbol and edits NO shared file; Integrate ranges over these tables to
// wire the ONE canonical registry when it flips native dispatch on.
//
// Query/context parity: the beego handlers read query params (scope, org, hours,
// since, window) and the request principal. Both native cloud (MsgType 100) and
// the gateway convention (registerGatewayPath's zapHandler) carry only
// method+auth+body, so these params are decoded from the JSON body — the same
// pattern zapBalanceHandler uses to read params.User from the body. Identity is
// ALWAYS the resolved credential (auth), NEVER a body field.

package controllers

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/luxfi/zap"

	iam "github.com/hanzoai/iam"

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

// ── ZAP dispatch tables (group-local, collision-free) ────────────────────
//
// A type ALIAS (not a new named type) so no shared symbol is declared, mirroring
// zap_responses.go. Integrate ranges over the two tables below to wire this
// group into the ONE canonical registry (registerCloud(method, h) /
// registerGatewayPath(path, h)) when it flips native dispatch on — no group edits
// a shared registration file. /v1/feedback aliases /v1/add-routing-reward onto
// the ONE reward handler (parity with router.go), and the gateway paths are full
// and non-overlapping so a prefix-match dispatcher is unambiguous.

type zapRouterPolicyStatsFn = func(ctx context.Context, auth string, body []byte) (*zap.Message, error)

// zapRouterPolicyStatsCloud maps native cloud method → handler (MsgType 100).
// init self-registers this group's dispatch tables into the canonical registry.
func init() {
	for method, h := range zapRouterPolicyStatsCloud {
		registerCloud(method, h)
	}
	for path, h := range zapRouterPolicyStatsGateway {
		registerGatewayPath(path, h)
	}
}

var zapRouterPolicyStatsCloud = map[string]zapRouterPolicyStatsFn{
	"router.get-policy":            zapGetRouterPolicyHandler,
	"router.update-policy":         zapUpdateRouterPolicyHandler,
	"router.stats":                 zapGetRouterStatsHandler,
	"router.publish-artifact-meta": zapPublishRouterArtifactMetaHandler,
	"routing.get-defaults":         zapGetRoutingDefaultsHandler,
	"routing.export-ledger":        zapExportRoutingLedgerHandler,
	"routing.add-reward":           zapAddRoutingRewardHandler,
	"routing.feedback":             zapAddRoutingRewardHandler,
	"routing.export-rewards":       zapExportRoutingRewardsHandler,
	"traffic.globe":                zapGetTrafficGlobeHandler,
	"training.get-contribution":    zapGetTrainingContributionHandler,
	"training.update-contribution": zapUpdateTrainingContributionHandler,
}

// zapRouterPolicyStatsGateway maps gateway path → handler (MsgType 200).
var zapRouterPolicyStatsGateway = map[string]zapRouterPolicyStatsFn{
	"/v1/get-router-policy":            zapGetRouterPolicyHandler,
	"/v1/update-router-policy":         zapUpdateRouterPolicyHandler,
	"/v1/router/stats":                 zapGetRouterStatsHandler,
	"/v1/router/publish-artifact-meta": zapPublishRouterArtifactMetaHandler,
	"/v1/get-routing-defaults":         zapGetRoutingDefaultsHandler,
	"/v1/export-routing-ledger":        zapExportRoutingLedgerHandler,
	"/v1/add-routing-reward":           zapAddRoutingRewardHandler,
	"/v1/feedback":                     zapAddRoutingRewardHandler,
	"/v1/export-routing-rewards":       zapExportRoutingRewardsHandler,
	"/v1/traffic/globe":                zapGetTrafficGlobeHandler,
	"/v1/get-training-contribution":    zapGetTrainingContributionHandler,
	"/v1/update-training-contribution": zapUpdateTrainingContributionHandler,
}

// ── Shared seams (identity + response envelope parity) ───────────────────

// zapRPSPrincipal resolves the request principal STRICTLY from its verified
// credential — the ZAP analogue of principalUser (there is no session cookie on
// the ZAP path). hk-/pk- IAM keys route through getUserByAccessKey; JWTs through
// object.ParseAndValidateJWT (signature + iss/aud, never raw iam.ParseJwtToken).
// Returns nil for an empty/invalid/unsupported credential — fail-secure.
func zapRPSPrincipal(auth string) *iam.User {
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

// zapRPSOrg mirrors GetOrg for the ZAP path: the org is the verified principal's
// Owner, never a client field. No verified principal → the IAM_ORG default (as
// GetOrg does when there is no principal).
func zapRPSOrg(user *iam.User) string {
	if user != nil && user.Owner != "" {
		return user.Owner
	}
	return conf.GetConfigString("IAM_ORG")
}

// zapRPSOk renders the beego ResponseOk envelope ({status:"ok", data}) as a 200
// cloud response, preserving the exact JSON shape the HTTP handlers return.
func zapRPSOk(data interface{}) (*zap.Message, error) {
	b, _ := json.Marshal(Response{Status: "ok", Data: data})
	return object.BuildCloudResponse(http.StatusOK, b, "")
}

// zapRPSError renders the beego ResponseError envelope ({status:"error", msg}) at
// the given status, mirroring ResponseError / ResponseErrorWithStatus body shape.
func zapRPSError(status int, msg string) (*zap.Message, error) {
	b, _ := json.Marshal(Response{Status: "error", Msg: msg})
	return object.BuildCloudResponse(uint32(status), b, "")
}

// zapRPSRequireAdmin mirrors ApiController.RequireAdmin: preview mode passes; else
// an ORG-level admin principal is required. Returns (user, nil) on pass, or
// (nil, errResponse) on refusal (403 for a non-admin, matching ResponseForbidden).
func zapRPSRequireAdmin(auth string) (*iam.User, *zap.Message) {
	user := zapRPSPrincipal(auth)
	if conf.IsPreviewMode() {
		return user, nil
	}
	if !util.IsAdmin(user) {
		msg, _ := zapRPSError(http.StatusForbidden, "auth:this operation requires admin privilege")
		return nil, msg
	}
	return user, nil
}

// zapRPSRequireSuperAdmin mirrors ApiController.RequireSuperAdmin: no principal →
// 401, authenticated non-super-admin → 403.
func zapRPSRequireSuperAdmin(auth string) (*iam.User, *zap.Message) {
	user := zapRPSPrincipal(auth)
	if user == nil {
		msg, _ := zapRPSError(http.StatusUnauthorized, "auth:Please sign in first")
		return nil, msg
	}
	if !util.IsSuperAdmin(user) {
		msg, _ := zapRPSError(http.StatusForbidden, "auth:this operation requires super admin privilege")
		return nil, msg
	}
	return user, nil
}

// zapRPSRouterAdminAuthorized mirrors ApiController.routerAdminAuthorized: a
// service holding ROUTER_ADMIN_TOKEN (constant-time compared) OR a super admin.
func zapRPSRouterAdminAuthorized(auth string) (*iam.User, *zap.Message) {
	tok := strings.TrimSpace(os.Getenv("ROUTER_ADMIN_TOKEN"))
	if tok == "" {
		tok = strings.TrimSpace(conf.GetConfigString("ROUTER_ADMIN_TOKEN"))
	}
	if tok != "" {
		bearer := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
		if bearer != "" && subtle.ConstantTimeCompare([]byte(bearer), []byte(tok)) == 1 {
			return nil, nil
		}
	}
	return zapRPSRequireSuperAdmin(auth)
}

// ── router policy ────────────────────────────────────────────────────────

func zapGetRouterPolicyHandler(_ context.Context, auth string, _ []byte) (*zap.Message, error) {
	user, deny := zapRPSRequireAdmin(auth)
	if deny != nil {
		return deny, nil
	}
	policy, err := resolvedRouterPolicy(zapRPSOrg(user))
	if err != nil {
		return zapRPSError(http.StatusOK, err.Error())
	}
	return zapRPSOk(policy)
}

func zapUpdateRouterPolicyHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user, deny := zapRPSRequireAdmin(auth)
	if deny != nil {
		return deny, nil
	}
	org := zapRPSOrg(user)
	if org == "" {
		return zapRPSError(http.StatusOK, "auth:Please sign in first")
	}

	var reqBody routerPolicyBody
	if err := json.Unmarshal(body, &reqBody); err != nil {
		return zapRPSError(http.StatusOK, err.Error())
	}

	existing, err := object.GetOrgSettings(org)
	if err != nil {
		return zapRPSError(http.StatusOK, err.Error())
	}
	row := object.OrgSettings{
		Owner:             org,
		RouterPrefer:      object.JSONMap[[]string](reqBody.Prefer),
		RouterCostCeiling: reqBody.CostCeiling,
	}
	if existing == nil {
		if _, err := object.AddOrgSettings(&row); err != nil {
			return zapRPSError(http.StatusOK, err.Error())
		}
	} else {
		row.AutoRouting = existing.AutoRouting
		row.DefaultSessionRouting = existing.DefaultSessionRouting
		row.CreatedTime = existing.CreatedTime
		if _, err := object.UpdateOrgSettings(org, &row); err != nil {
			return zapRPSError(http.StatusOK, err.Error())
		}
	}

	policy, err := resolvedRouterPolicy(org)
	if err != nil {
		return zapRPSError(http.StatusOK, err.Error())
	}
	return zapRPSOk(policy)
}

// ── router stats + artifact meta ─────────────────────────────────────────

// zapRouterStatsParams is the body-decoded projection of the beego query params.
type zapRouterStatsParams struct {
	Scope  string `json:"scope"`
	Org    string `json:"org"`
	OrgAll bool   `json:"org_all"`
	Hours  int    `json:"hours"`
	Since  string `json:"since"`
}

// zapResolveStatsWindow mirrors ApiController.resolveStatsWindow off body params.
func zapResolveStatsWindow(p zapRouterStatsParams) (windowStart, now time.Time, sinceStr string) {
	now = time.Now().UTC()
	if p.Since != "" {
		if t, err := time.Parse(time.RFC3339, p.Since); err == nil {
			return t.UTC(), now, t.UTC().Format(time.RFC3339)
		}
	}
	hours := routerStatsDefaultHours
	if p.Hours > 0 {
		hours = p.Hours
	}
	if hours > routerStatsMaxHours {
		hours = routerStatsMaxHours
	}
	windowStart = now.Add(-time.Duration(hours) * time.Hour)
	return windowStart, now, windowStart.Format(time.RFC3339)
}

func zapGetRouterStatsHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	var p zapRouterStatsParams
	if len(body) > 0 {
		_ = json.Unmarshal(body, &p)
	}
	windowStart, now, sinceStr := zapResolveStatsWindow(p)

	if p.Scope == scopePlatform {
		events, err := getRoutingEvents("", sinceStr)
		if err != nil {
			return zapRPSError(http.StatusOK, err.Error())
		}
		stats := computeRouterStats(events, blendedPriceForOrg(""), windowStart, now, scopePlatform, "", false)
		attachRetrainMeta(&stats, object.GlobalDefaultOwner)
		return zapRPSOk(stats)
	}

	user := zapRPSPrincipal(auth)
	if user == nil {
		return zapRPSError(http.StatusUnauthorized, "auth:Please sign in first")
	}

	org := user.Owner
	if util.IsSuperAdmin(user) {
		if p.Org != "" || p.OrgAll {
			org = p.Org
		}
	}

	events, err := getRoutingEvents(org, sinceStr)
	if err != nil {
		return zapRPSError(http.StatusOK, err.Error())
	}
	stats := computeRouterStats(events, blendedPriceForOrg(org), windowStart, now, scopeOrg, org, true)
	metaOwner := org
	if org == "" {
		metaOwner = object.GlobalDefaultOwner
	}
	attachRetrainMeta(&stats, metaOwner)
	if stats.Retrain == nil && metaOwner != object.GlobalDefaultOwner {
		attachRetrainMeta(&stats, object.GlobalDefaultOwner)
	}
	return zapRPSOk(stats)
}

func zapPublishRouterArtifactMetaHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if _, deny := zapRPSRequireSuperAdmin(auth); deny != nil {
		return deny, nil
	}
	var meta object.RouterArtifactMeta
	if err := json.Unmarshal(body, &meta); err != nil {
		return zapRPSError(http.StatusOK, err.Error())
	}
	if meta.Owner == "" {
		meta.Owner = object.GlobalDefaultOwner
	}
	if err := object.UpsertRouterArtifactMeta(&meta); err != nil {
		return zapRPSError(http.StatusOK, err.Error())
	}
	return zapRPSOk(meta)
}

// ── routing defaults ─────────────────────────────────────────────────────

func zapGetRoutingDefaultsHandler(_ context.Context, auth string, _ []byte) (*zap.Message, error) {
	org := zapRPSOrg(zapRPSPrincipal(auth))

	autoActive := false
	if cfg := GetModelConfig(); cfg != nil {
		autoActive = cfg.AutoRoutingActive(effectiveAutoRouting(org))
	}
	return zapRPSOk(routingDefaults{
		AutoRoutingActive:     autoActive,
		DefaultSessionRouting: effectiveSessionRouting(org) == object.AutoRoutingEnabled,
	})
}

// ── ledger + rewards exports (JSONL, buffered — no HTTP writer) ───────────

func zapExportRoutingLedgerHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if _, deny := zapRPSRouterAdminAuthorized(auth); deny != nil {
		return deny, nil
	}
	var p zapRouterStatsParams
	if len(body) > 0 {
		_ = json.Unmarshal(body, &p)
	}
	events, err := getRoutingEvents(p.Org, p.Since)
	if err != nil {
		return zapRPSError(http.StatusOK, err.Error())
	}
	var buf bytes.Buffer
	if err := writeRoutingLedgerJSONL(&buf, events); err != nil {
		return zapRPSError(http.StatusInternalServerError, err.Error())
	}
	// Raw JSONL stream (not the {status:ok} envelope), matching the beego export.
	return object.BuildCloudResponse(http.StatusOK, buf.Bytes(), "")
}

func zapExportRoutingRewardsHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if _, deny := zapRPSRouterAdminAuthorized(auth); deny != nil {
		return deny, nil
	}
	var p zapRouterStatsParams
	if len(body) > 0 {
		_ = json.Unmarshal(body, &p)
	}
	events, err := getRewardedRoutingEvents(p.Org, p.Since)
	if err != nil {
		return zapRPSError(http.StatusOK, err.Error())
	}
	var buf bytes.Buffer
	if err := writeRoutingRewardsJSONL(&buf, events); err != nil {
		return zapRPSError(http.StatusInternalServerError, err.Error())
	}
	return object.BuildCloudResponse(http.StatusOK, buf.Bytes(), "")
}

// ── routing reward (feedback) ────────────────────────────────────────────

func zapAddRoutingRewardHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapRPSPrincipal(auth)
	if user == nil {
		return zapRPSError(http.StatusUnauthorized, "auth:Please sign in first")
	}
	if util.IsAnonymousUser(user) {
		return zapRPSError(http.StatusUnauthorized, "auth:Please sign in first")
	}

	var reqBody routingRewardRequest
	if err := json.Unmarshal(body, &reqBody); err != nil {
		return zapRPSError(http.StatusBadRequest, "invalid request body")
	}
	requestId := normalizeRequestId(reqBody.RequestId)
	if requestId == "" {
		return zapRPSError(http.StatusBadRequest, "request_id is required")
	}
	reward, record, err := resolveReward(reqBody.Reward, reqBody.Rating, reqBody.Signal)
	if err != nil {
		return zapRPSError(http.StatusBadRequest, err.Error())
	}
	if !record {
		return zapRPSOk(routingRewardResult{RequestId: requestId, Recorded: false})
	}
	if !trainingOptedIn(user.Owner) {
		return zapRPSOk(routingRewardResult{RequestId: requestId, Recorded: false})
	}

	found, err := attachRoutingReward(user.Owner, requestId, reward)
	if err != nil {
		return zapRPSError(http.StatusOK, err.Error())
	}
	if !found {
		return zapRPSError(http.StatusNotFound, "unknown request_id")
	}
	forwardRewardToEngine(user.Owner, requestId, reward)
	return zapRPSOk(routingRewardResult{RequestId: requestId, Reward: reward, Recorded: true})
}

// ── traffic globe (public) ───────────────────────────────────────────────

func zapGetTrafficGlobeHandler(_ context.Context, _ string, body []byte) (*zap.Message, error) {
	windowMin := object.TrafficDefaultWindowMinutes
	if len(body) > 0 {
		var p struct {
			Window int `json:"window"`
		}
		if json.Unmarshal(body, &p) == nil && p.Window > 0 {
			windowMin = p.Window
		}
	}
	return zapRPSOk(object.GlobalTraffic.Globe(windowMin, time.Now()))
}

// ── training contribution opt-in ─────────────────────────────────────────

func zapGetTrainingContributionHandler(_ context.Context, auth string, _ []byte) (*zap.Message, error) {
	user, deny := zapRPSRequireAdmin(auth)
	if deny != nil {
		return deny, nil
	}
	org := zapRPSOrg(user)
	return zapRPSOk(trainingContributionBody{
		Enabled: object.GetCachedOrgTrainingContribution(org) == object.TrainingContributionEnabled,
	})
}

func zapUpdateTrainingContributionHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user, deny := zapRPSRequireAdmin(auth)
	if deny != nil {
		return deny, nil
	}
	org := zapRPSOrg(user)
	if org == "" {
		return zapRPSError(http.StatusOK, "auth:Please sign in first")
	}

	var reqBody trainingContributionBody
	if err := json.Unmarshal(body, &reqBody); err != nil {
		return zapRPSError(http.StatusOK, err.Error())
	}
	value := object.TrainingContributionDisabled
	if reqBody.Enabled {
		value = object.TrainingContributionEnabled
	}

	existing, err := object.GetOrgSettings(org)
	if err != nil {
		return zapRPSError(http.StatusOK, err.Error())
	}
	if existing == nil {
		row := object.OrgSettings{Owner: org, TrainingContribution: value}
		if _, err := object.AddOrgSettings(&row); err != nil {
			return zapRPSError(http.StatusOK, err.Error())
		}
	} else {
		existing.TrainingContribution = value
		if _, err := object.UpdateOrgSettings(org, existing); err != nil {
			return zapRPSError(http.StatusOK, err.Error())
		}
	}
	return zapRPSOk(trainingContributionBody{Enabled: reqBody.Enabled})
}
