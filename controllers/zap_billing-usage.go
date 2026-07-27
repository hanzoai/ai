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
//
// zap_billing-usage.go — native ZAP handlers for the billing/usage route group,
// migrated STRANGLER-style off beego (controllers/usage.go, cloud_usage.go,
// do_backfill.go). Each handler re-implements its beego twin against object/ +
// iam directly — it never wraps or transforms the beego controller. The beego
// routes stay live in router.go until Integrate flips native dispatch on.
//
// Group routes (all HTTP-over-ZAP, gateway MsgType 200):
//   GET  /v1/get-usages
//   GET  /v1/get-range-usages
//   GET  /v1/get-users
//   GET  /v1/get-user-table-infos
//   GET  /v1/get-cloud-usages
//   POST /v1/admin/usage/backfill-do
//
// Identity is derived ONLY from the Bearer token (never the body) via the shared
// zapPrincipalUser seam (zap_model-routing-config.go): hk- IAM keys through
// getUserByAccessKey, JWTs through object.ParseAndValidateJWT (signature +
// iss/aud). Org scope is the principal's own Owner; only a same-brand super admin
// can widen scope, and only across orgs the god-view already exposes.
//
// The registry (registerGatewayPath / zapGatewayHandler), the identity seam
// (zapPrincipalUser / zapRequireSuperAdmin), and the envelope helpers (zapGwOk /
// zapGwError) all live in the sibling group file zap_model-routing-config.go —
// this file only self-registers its own routes from init() and adds its own
// handlers, so no two groups edit a shared registration file.

package controllers

import (
	"context"
	"net/url"
	"strings"
	"time"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/luxfi/zap"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
	"github.com/hanzoai/types"
)

// registerZapBillingUsage wires this group's gateway routes into the shared
// registry. Invoked from init so the group self-registers from its own file;
// InitZapHandlers stays the single Node.Handle bootstrap and gains no per-group
// arm. Every route is HTTP-over-ZAP (MsgType 200) — there is no native cloud
// method for these HTTP/query-driven admin reads.
func registerZapBillingUsage() {
	registerGatewayRoute("/v1/admin/usage/backfill-do", zapPostBackfillDOUsageHandler)
}

func init() { registerZapBillingUsage() }

// zapRequireUsageAdmin mirrors the beego usage endpoints' guard: no verified
// principal → 401 (ResponseUnauthorized), authenticated non-admin → the
// admin-privilege refusal at HTTP 200 (ResponseError parity). Returns the user
// when authorized, else the refusal message to return verbatim. Admin here is
// ORG-level (util.IsAdmin) — the same check c.IsAdmin() runs — NOT super admin.
func zapRequireUsageAdmin(auth string) (*iam.User, *zap.Message) {
	user := zapPrincipalUser(auth)
	if user == nil {
		m, _ := zapGwError(401, "auth:Please sign in first")
		return nil, m
	}
	if !util.IsAdmin(user) {
		m, _ := zapGwError(200, "auth:this operation requires admin privilege")
		return nil, m
	}
	return user, nil
}

// usageTokenOwnBrand mirrors ApiController.principalIsOwnBrand for the Bearer
// path (ZAP has no cookie session): own-brand ONLY when the token is a JWT minted
// by THIS deployment's own issuer. hk- keys and foreign-brand JWTs are NOT
// own-brand, so a sibling-brand super admin is pinned to its own org.
func usageTokenOwnBrand(auth string) bool {
	token := strings.TrimPrefix(auth, "Bearer ")
	if token == "" || !isJwtToken(token) {
		return false
	}
	return object.TokenIsOwnBrand(token)
}

// ── GET /v1/get-usages ──

func zapGetUsagesHandler(_ context.Context, _, _, query, auth string, _ []byte) (*zap.Message, error) {
	user, refuse := zapRequireUsageAdmin(auth)
	if refuse != nil {
		return refuse, nil
	}

	q, _ := url.ParseQuery(query)
	days := util.ParseInt(q.Get("days"))
	selectedUser := q.Get("selectedUser")
	storeName := q.Get("store")

	usages, err := object.GetUsages(days, selectedUser, storeName)
	if err != nil {
		return zapGwError(200, err.Error())
	}

	// Lang defaults to "en" on the ZAP wire (native-handler convention, matching
	// zapChatHandler): the gateway dispatch carries no Accept-Language header.
	usageMetadata, err := object.GetUsageMetadata("en", user.Owner)
	if err != nil {
		return zapGwError(200, err.Error())
	}

	return zapGwOk(usages, usageMetadata)
}

// ── GET /v1/get-range-usages ──

func zapGetRangeUsagesHandler(_ context.Context, _, _, query, auth string, _ []byte) (*zap.Message, error) {
	user, refuse := zapRequireUsageAdmin(auth)
	if refuse != nil {
		return refuse, nil
	}

	q, _ := url.ParseQuery(query)
	rangeType := q.Get("rangeType")
	count := util.ParseInt(q.Get("count"))
	selectedUser := q.Get("user")
	storeName := q.Get("store")

	usages, err := object.GetRangeUsages(rangeType, count, selectedUser, storeName, "en")
	if err != nil {
		return zapGwError(200, err.Error())
	}

	usageMetadata, err := object.GetUsageMetadata("en", user.Owner)
	if err != nil {
		return zapGwError(200, err.Error())
	}

	return zapGwOk(usages, usageMetadata)
}

// ── GET /v1/get-users ──

func zapGetUsersHandler(_ context.Context, _, _, query, auth string, _ []byte) (*zap.Message, error) {
	if _, refuse := zapRequireUsageAdmin(auth); refuse != nil {
		return refuse, nil
	}

	q, _ := url.ParseQuery(query)
	storeName := q.Get("store")
	// Parity with beego GetUsers: the user filter is forced empty.
	users, err := object.GetUsers(storeName, "")
	if err != nil {
		return zapGwError(200, err.Error())
	}

	return zapGwOk(users)
}

// ── GET /v1/get-user-table-infos ──

func zapGetUserTableInfosHandler(_ context.Context, _, _, query, auth string, _ []byte) (*zap.Message, error) {
	if _, refuse := zapRequireUsageAdmin(auth); refuse != nil {
		return refuse, nil
	}

	q, _ := url.ParseQuery(query)
	storeName := q.Get("store")
	// Parity with beego GetUserTableInfos: the user filter is forced empty.
	userInfos, err := object.GetUserTableInfos(storeName, "")
	if err != nil {
		return zapGwError(200, err.Error())
	}

	return zapGwOk(userInfos)
}

// ── GET /v1/get-cloud-usages ──

func zapGetCloudUsagesHandler(ctx context.Context, _, _, query, auth string, _ []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if user == nil {
		return zapGwError(401, "auth:Please sign in first")
	}
	// A financial read is never served to an anonymous guest (parity with the
	// beego IsAnonymousUser guard — a persisted u-<hash> could otherwise read the
	// deployment org's aggregate spend).
	if util.IsAnonymousUser(user) {
		return zapGwError(401, "auth:Please sign in first")
	}

	org, allOrgs := zapResolveCloudUsageScope(user, usageTokenOwnBrand(auth), query)

	q, _ := url.ParseQuery(query)
	w, err := types.ParseWindow(
		q.Get("range"), q.Get("start"), q.Get("end"), time.Now(),
	)
	if err != nil {
		return zapGwError(200, err.Error())
	}

	params := object.CloudUsageParams{
		RangeLabel:     cloudUsageRangeLabel(q.Get("range")),
		Start:          w.Start,
		End:            w.End,
		Interval:       w.Interval,
		Org:            org,
		AllOrgs:        allOrgs,
		TopModels:      cloudUsageIntParam(q.Get("topModels"), 6, 1, 50),
		ActivityType:   strings.ToLower(strings.TrimSpace(q.Get("activityType"))),
		ActivityLimit:  cloudUsageIntParam(q.Get("activityLimit"), 20, 1, 200),
		ActivityOffset: cloudUsageIntParam(q.Get("activityOffset"), 0, 0, 1_000_000),
	}

	qctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	overview, err := object.GetCloudUsageOverview(qctx, params)
	if err != nil {
		return zapGwError(200, err.Error())
	}

	return zapGwOk(overview)
}

// zapResolveCloudUsageScope mirrors ApiController.resolveCloudUsageScope. A
// non-super-admin (or a super admin whose token is NOT own-brand) is pinned to
// its own org — request hints (?org=/?owner=) ignored. A same-brand super admin
// targets one org via ?org= / ?owner=, or gets the all-orgs god-view (omitted,
// empty, "all", or "*"). ownBrand is passed in (computed from the token by the
// handler) so the policy is unit-testable without a real JWT. The X-Org-Id
// header fallback of the beego twin is intentionally absent: the gateway dispatch
// signature carries no headers, so on this wire super-admin targeting is by query
// only (the token owner is always the tenant floor).
func zapResolveCloudUsageScope(user *iam.User, ownBrand bool, query string) (org string, allOrgs bool) {
	if !util.IsSuperAdmin(user) || !ownBrand {
		return user.Owner, false
	}
	q, _ := url.ParseQuery(query)
	target := strings.TrimSpace(q.Get("org"))
	if target == "" {
		target = strings.TrimSpace(q.Get("owner"))
	}
	if target == "" || strings.EqualFold(target, "all") || target == "*" {
		return "", true
	}
	return target, false
}

// ── POST /v1/admin/usage/backfill-do ──

func zapPostBackfillDOUsageHandler(ctx context.Context, method, _, query, auth string, _ []byte) (*zap.Message, error) {
	// A WRITE to the platform-wide financial ledger — restrict to POST like the
	// beego route, then re-check super admin (defense in depth, mirrors the
	// controller self-guard). Fail-closed: no principal → 401, non-super → 403.
	if method != "" && !strings.EqualFold(method, "POST") {
		return zapGwError(404, "not found")
	}
	if _, refuse := zapRequireSuperAdmin(auth); refuse != nil {
		return refuse, nil
	}

	q, _ := url.ParseQuery(query)
	from, to := resolveDOBackfillWindow(q.Get("from"), q.Get("to"), time.Now())
	opts := DOBackfillOptions{
		From:   from,
		To:     to,
		DryRun: !doFlagFalse(q.Get("dryRun")), // WRITE requires an explicit dryRun=false
		Force:  doFlagTrue(q.Get("force")),
	}

	runCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	plan, err := RunDOBackfill(runCtx, opts)
	if err != nil {
		return zapGwError(200, err.Error())
	}
	return zapGwOk(plan)
}
