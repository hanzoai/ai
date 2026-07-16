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
	"time"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

// GetTrafficGlobe returns the PUBLIC live request-geo aggregate for the
// world.hanzo.ai "Hanzo mode" globe: WHERE requests to api.hanzo.ai are coming from,
// as country/region points with per-service-class counts, plus headline throughput
// rates.
//
// It is AUTH-exempt and BALANCE-exempt exactly like /v1/router/stats?scope=platform:
//   - auth: the controller name "traffic/globe" is neither a get-/update- CRUD name
//     nor a super-admin/present-credential endpoint, so the authz filter passes it
//     through, and this handler requires no principal.
//   - balance: isBalanceExempt("/v1/traffic/...") returns true.
//
// It exposes ONLY aggregates — counts, rates, and country/region centroids — and
// NEVER any IP, per-request row, org, or user dimension (see object/traffic.go).
// Marketing telemetry; nothing sensitive.
//
// @Title GetTrafficGlobe
// @Tag Traffic API
// @Description public live request-geo aggregate (country/region points + throughput totals); aggregates only, no IPs, no auth
// @Param window query int false "window size in minutes (default 60, capped at 1440)"
// @Success 200 {object} object.TrafficGlobe The Response object
// @router /traffic/globe [get]
func (c *ApiController) GetTrafficGlobe() {
	windowMin := object.TrafficDefaultWindowMinutes
	if v := c.Input().Get("window"); v != "" {
		if n := util.ParseInt(v); n > 0 {
			windowMin = n
		}
	}
	c.ResponseOk(object.GlobalTraffic.Globe(windowMin, time.Now()))
}
