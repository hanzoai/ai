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

package controllers

import (
	"context"
	"strconv"
	"strings"
	"time"

	iam "github.com/hanzoai/iam"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

// GetCloudUsages
// @Title GetCloudUsages
// @Tag Usage API
// @Description Overview aggregation of the hanzo.cloud_usage ledger: totals +
// prior-period deltas, an evenly-spaced time series, spend-by-model (top-N +
// other), and the recent-activity feed, for a time range.
// @Param range query string false "24h | 7d | 30d | custom (default 24h)"
// @Param start query string false "custom range start (RFC3339 or unix seconds)"
// @Param end query string false "custom range end (RFC3339 or unix seconds)"
// @Param org query string false "super admin only: target org slug, 'all' for every org"
// @Param topModels query string false "spend-by-model top-N (default 6)"
// @Param activityType query string false "all | inference (default all)"
// @Param activityLimit query string false "recent-activity page size (default 20)"
// @Param activityOffset query string false "recent-activity page offset (default 0)"
// @Success 200 {object} object.CloudUsageOverview The Response object
// @router /get-cloud-usages [get]
//
// Auth is session-OR-Bearer (c.RequirePrincipal): the console sends a session
// cookie, while the token-bearing surfaces (app / chat / billing, which drive the
// unified UsagePanel) send an IAM Bearer. Both resolve to the SAME principal, and
// the scope below is decided from THAT principal alone.
//
// Scoping is the dual-use o11y read shared by tenant surfaces (own-org) and
// admin.hanzo.ai (god-view): a non-super-admin is pinned to their own org —
// request scope hints (X-Org-Id header, ?org=) are ignored, so a tenant can never
// read another org, no matter which auth it presents. A super admin targets one
// org via ?org=<slug> (or the X-Org-Id header console2 stamps), or omits it /
// passes ?org=all for the ALL-orgs view. The super-admin decision comes from the
// verified principal (util.IsSuperAdmin), never from a header or param.
func (c *ApiController) GetCloudUsages() {
	user, ok := c.RequirePrincipal()
	if !ok {
		return
	}

	// A financial read is never served to an anonymous guest. anonymousSignin binds
	// a synthesized u-<hash> record to the deployment org (IAM_ORG) — so a persisted
	// guest would otherwise read that org's AGGREGATE spend, and if IAM_ORG were ever
	// "admin" it would inherit the all-orgs god-view. Reject every anonymous
	// principal here; this endpoint requires a real signed-in identity.
	if util.IsAnonymousUser(user) {
		c.ResponseUnauthorized(c.T("auth:Please sign in first"))
		return
	}

	org, allOrgs := c.resolveCloudUsageScope(user)

	start, end, interval, err := object.ResolveCloudUsageWindow(
		c.Input().Get("range"), c.Input().Get("start"), c.Input().Get("end"), time.Now(),
	)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	params := object.CloudUsageParams{
		RangeLabel:     cloudUsageRangeLabel(c.Input().Get("range")),
		Start:          start,
		End:            end,
		Interval:       interval,
		Org:            org,
		AllOrgs:        allOrgs,
		TopModels:      cloudUsageIntParam(c.Input().Get("topModels"), 6, 1, 50),
		ActivityType:   strings.ToLower(strings.TrimSpace(c.Input().Get("activityType"))),
		ActivityLimit:  cloudUsageIntParam(c.Input().Get("activityLimit"), 20, 1, 200),
		ActivityOffset: cloudUsageIntParam(c.Input().Get("activityOffset"), 0, 0, 1_000_000),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	overview, err := object.GetCloudUsageOverview(ctx, params)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(overview)
}

// resolveCloudUsageScope decides whose data the caller may read. Non-global
// admins are pinned to their authenticated org (header/param hints ignored).
// Super admins may target one org (?org= / ?owner=, else the X-Org-Id header
// console2 sends) or get the all-orgs view (omitted, empty, "all", or "*").
func (c *ApiController) resolveCloudUsageScope(user *iam.User) (org string, allOrgs bool) {
	// The all-orgs god-view and cross-org targeting expose EVERY customer's spend,
	// so they require a SAME-BRAND super admin: a member of the reserved admin org
	// (util.IsSuperAdmin) whose principal was minted by THIS deployment's own IAM
	// (principalIsOwnBrand). A sibling white-label brand's super admin — a token the
	// auth layer trusts for sign-in but that belongs to another brand — is pinned to
	// its own org like any tenant. This binds all-customer financial disclosure to
	// the deployment's OWN issuer, independent of which brand issuers the IAM SDK is
	// later configured to VERIFY (today the cross-brand path is also shut by
	// signature, but this closes it by POLICY regardless — see brand.go SDK-bump note).
	if !util.IsSuperAdmin(user) || !c.principalIsOwnBrand() {
		return user.Owner, false
	}

	target := strings.TrimSpace(c.Input().Get("org"))
	if target == "" {
		target = strings.TrimSpace(c.Input().Get("owner"))
	}
	if target == "" {
		target = strings.TrimSpace(c.GetRequestTenantOrgID()) // X-Org-Id header
	}
	if target == "" || strings.EqualFold(target, "all") || target == "*" {
		return "", true
	}
	return target, false
}

// cloudUsageRangeLabel normalizes the echoed range label.
func cloudUsageRangeLabel(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "24h", "1d", "day", "today":
		return "24h"
	case "7d", "week":
		return "7d"
	case "30d", "month":
		return "30d"
	case "custom":
		return "custom"
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}

// cloudUsageIntParam parses a positive int query param with a default and clamp.
func cloudUsageIntParam(raw string, def, min, max int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
