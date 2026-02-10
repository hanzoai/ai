// Copyright 2024 Hanzo AI Inc. All Rights Reserved.
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
	"os"
)

// GetAgentsDashboardUrl returns the URL of the Hanzo Agents control plane dashboard.
//
// @Title GetAgentsDashboardUrl
// @Tag Agents API
// @Description get agents dashboard URL
// @Success 200 {object} Response The Response object
// @router /get-agents-dashboard-url [get]
func (c *ApiController) GetAgentsDashboardUrl() {
	dashboardUrl := os.Getenv("AGENTS_DASHBOARD_URL")
	if dashboardUrl == "" {
		dashboardUrl = "http://localhost:8080/ui"
	}

	c.ResponseOk(dashboardUrl)
}
