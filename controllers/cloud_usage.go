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
// @Param org query string false "global admin only: target org slug, 'all' for every org"
// @Param topModels query string false "spend-by-model top-N (default 6)"
// @Param activityType query string false "all | inference (default all)"
// @Param activityLimit query string false "recent-activity page size (default 20)"
// @Param activityOffset query string false "recent-activity page offset (default 0)"
// @Success 200 {object} object.CloudUsageOverview The Response object
// @router /get-cloud-usages [get]
//
// Scoping is the dual-use o11y read shared by console2 (tenant) and admin.hanzo.ai
// (god-view): a non-global-admin is pinned to their own session org — request
// scope hints (X-Org-Id header, ?org=) are ignored, so a tenant can never read
// another org. A global admin targets one org via ?org=<slug> (or the X-Org-Id
// header that console2 stamps from currentOrg()), or omits it / passes ?org=all
// for the ALL-orgs view.
func (c *ApiController) GetCloudUsages() {
	user, ok := c.RequireSignedInUser()
	if !ok {
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
// Global admins may target one org (?org= / ?owner=, else the X-Org-Id header
// console2 sends) or get the all-orgs view (omitted, empty, "all", or "*").
func (c *ApiController) resolveCloudUsageScope(user *iam.User) (org string, allOrgs bool) {
	if !util.IsGlobalAdmin(user) {
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
