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

// Native ZAP handlers for the router-policy / router-stats / routing group — the
// ONE and ONLY implementation of these routes. The controller twins (router.go
// registrations + the Get/UpdateRouterPolicy, GetRoutingDefaults, ExportRouting*,
// PublishRouterArtifactMeta controller methods) were DELETED: one route, one
// handler, no split-brain, no drift. Each handler works against object/ + iam
// directly, mirroring zap_native.go:zapChatHandler.
//
// Routes are RESTful nouns (method-aware where a noun carries several verbs):
//   GET|PUT /v1/ai/router/policy        — read the effective policy / upsert the org override
//   GET     /v1/ai/router/defaults      — the caller's resolved routing defaults
//   GET     /v1/ai/router/rewards       — rewarded training tuples (JSONL, admin/token)
//   GET     /v1/ai/router/ledger        — the routing-decision ledger (JSONL, admin/token)
//   POST    /v1/ai/router/artifact-meta — record a retrain run (super admin)
//   GET     /v1/ai/router/stats · POST /v1/ai/feedback · GET /v1/ai/traffic/globe (unchanged)
// The group self-registers into the ONE canonical registry (zap_registry.go) from
// init(): the multi-verb policy noun as an HTTP-shaped ROUTE (method/path/query
// aware, registerGatewayRoute), the single-verb nouns as body-only PATHS
// (registerGatewayPath). It declares NO shared symbol and edits NO shared file.
//
// Query/context parity: params (scope, org, hours, since, window) are decoded from
// the JSON body — native cloud (MsgType 100) and the body-only gateway path carry
// only method+auth+body (the zapBalanceHandler pattern); the HTTP-shaped policy
// route additionally has the real HTTP method. Identity is ALWAYS the resolved
// credential (auth), NEVER a body field.

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

	iam "github.com/hanzoai/ai/internal/iam"

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

// ── ZAP dispatch registration ────────────────────────────────────────────
//
// Self-registers into the ONE canonical registry (zap_registry.go) from init():
//   - the multi-verb policy noun (/v1/ai/router/policy) as an HTTP-shaped ROUTE
//     (registerGatewayRoute → zapGatewayHandler: method/path/query aware, so GET-read
//     and PUT-write share the one noun), and
//   - the single-verb nouns as body-only PATHS (registerGatewayPath → zapHandler).
//
// The gateway paths are full and non-overlapping so the longest-prefix dispatcher is
// unambiguous. The native-cloud (MsgType 100) method names are internal RPC only (no
// HTTP client calls them) — kept for parity with the sibling ZAP groups.
func init() {
	// Native cloud (MsgType 100) — internal RPC names.
	registerCloud("router.stats", zapGetRouterStatsHandler)
	registerCloud("router.defaults", zapGetRoutingDefaultsHandler)
	registerCloud("router.ledger", zapExportRoutingLedgerHandler)
	registerCloud("router.rewards", zapExportRoutingRewardsHandler)
	registerCloud("router.artifact-meta", zapPublishRouterArtifactMetaHandler)
	registerCloud("feedback", zapAddRoutingRewardHandler)
	registerCloud("traffic.globe", zapGetTrafficGlobeHandler)
	registerCloud("training.get-contribution", zapGetTrainingContributionHandler)
	registerCloud("training.update-contribution", zapUpdateTrainingContributionHandler)

	// Gateway (MsgType 200) — RESTful nouns. Method-aware policy route first.
	registerGatewayRoute("/v1/ai/router/policy", zapRouterPolicyHandler)
	registerGatewayPath("/v1/ai/router/stats", zapGetRouterStatsHandler)
	registerGatewayPath("/v1/ai/router/defaults", zapGetRoutingDefaultsHandler)
	registerGatewayPath("/v1/ai/router/ledger", zapExportRoutingLedgerHandler)
	registerGatewayPath("/v1/ai/router/rewards", zapExportRoutingRewardsHandler)
	registerGatewayPath("/v1/ai/router/artifact-meta", zapPublishRouterArtifactMetaHandler)
	registerGatewayPath("/v1/ai/traffic/globe", zapGetTrafficGlobeHandler)
	// No gateway path for /v1/ai/feedback: it is a resource COLLECTION address, and
	// a body-only prefix there would claim the member URLs of any row the resource
	// table ever generates at that noun (zap_gateway_fallback_test.go holds the
	// line). The reward POST reaches routers.App through the fallback, with every
	// filter intact; the native method above is unaffected.
}

// ── Shared seams (identity + response envelope parity) ───────────────────

// zapRPSPrincipal resolves the request principal STRICTLY from its verified
// credential — the ZAP analogue of principalUser (there is no session cookie on
// the ZAP path). sk- IAM keys route through getUserByAccessKey; JWTs through
// object.ParseAndValidateJWT (signature + iss/aud, never raw iam.ParseJwtToken).
// A pk- resolves to NO principal here and is meant to: it names an org, never a
// person, so there is nothing for this function to return and nothing to be
// mistaken for a role. Returns nil for an empty/invalid/unsupported credential —
// fail-secure.
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

// zapRPSOk renders the ResponseOk envelope ({status:"ok", data}) as a 200
// cloud response, preserving the exact JSON shape the HTTP handlers return.
func zapRPSOk(data interface{}) (*zap.Message, error) {
	b, _ := json.Marshal(Response{Status: "ok", Data: data})
	return object.BuildCloudResponse(http.StatusOK, b, "")
}

// zapRPSError renders the ResponseError envelope ({status:"error", msg}) at
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
	// The FOUR router-policy fields this endpoint owns (empty/nil CLEARS that override).
	prefer := object.JSONMap[[]string](reqBody.Prefer)
	enabled := enabledModelsSet(reqBody.EnabledModels)
	if existing == nil {
		row := object.OrgSettings{
			Owner:               org,
			RouterPrefer:        prefer,
			RouterCostCeiling:   reqBody.CostCeiling,
			RouterEnabledModels: enabled,
			RouterQualityBias:   reqBody.QualityBias,
		}
		if _, err := object.AddOrgSettings(&row); err != nil {
			return zapRPSError(http.StatusOK, err.Error())
		}
	} else {
		// MUTATE the existing row — touch ONLY the four owned fields, so every OTHER
		// OrgSettings field is preserved. dbx Model(s).Update() writes ALL columns
		// (including nil), so a fresh-row + preserve-list — as this handler used to do —
		// silently NULLs the fields it forgets: RouterEnabledModels, RouterQualityBias,
		// RouterStrategy, RouterOverrides, TrainingContribution. This IS the live gateway
		// path (served natively over ZAP), so that bug dropped the allowlist/dial on every
		// customer save. Mirrors the the router UpdateRouterPolicy.
		existing.RouterPrefer = prefer
		existing.RouterCostCeiling = reqBody.CostCeiling
		existing.RouterEnabledModels = enabled
		existing.RouterQualityBias = reqBody.QualityBias
		if _, err := object.UpdateOrgSettings(org, existing); err != nil {
			return zapRPSError(http.StatusOK, err.Error())
		}
	}

	policy, err := resolvedRouterPolicy(org)
	if err != nil {
		return zapRPSError(http.StatusOK, err.Error())
	}
	return zapRPSOk(policy)
}

// zapRouterPolicyHandler is the HTTP-shaped entrypoint for /v1/ai/router/policy: GET
// reads the effective policy, PUT upserts the caller's own org override. One noun,
// two verbs — the reason this route is method-aware (registerGatewayRoute) rather
// than body-only. Both delegate to the same read/write logic the router and ZAP shared.
func zapRouterPolicyHandler(ctx context.Context, method, _, _, auth string, body []byte) (*zap.Message, error) {
	switch strings.ToUpper(method) {
	case http.MethodGet:
		return zapGetRouterPolicyHandler(ctx, auth, body)
	case http.MethodPut:
		return zapUpdateRouterPolicyHandler(ctx, auth, body)
	}
	return zapRPSError(http.StatusMethodNotAllowed, "method not allowed: "+method)
}

// ── router stats + artifact meta ─────────────────────────────────────────

// zapRouterStatsParams is the body-decoded projection of the query params.
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
		stats := computeRouterStats(events, blendedPriceForOrg(""), windowStart, now, scopePlatform, "", false, tunedRouterPrefer(""))
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
	stats := computeRouterStats(events, blendedPriceForOrg(org), windowStart, now, scopeOrg, org, true, tunedRouterPrefer(org))
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
	// Parity with the (deleted) the router PublishRouterArtifactMeta: also append to the
	// IMMUTABLE retrain timeline the world.hanzo.ai Model-Improvement panel plots. The
	// upsert above is the source of truth for "latest per scope"; this append is the
	// history. Best-effort — a log failure must never fail the publish.
	_ = object.AppendRouterTrainingLog(object.NewRouterTrainingLog(&meta))
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
	// Raw JSONL stream (not the {status:ok} envelope), matching the the router export.
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
