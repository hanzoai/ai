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

// GetVmDashboardUrl returns the URL of the Hanzo VM control plane dashboard.
//
// @Title GetVmDashboardUrl
// @Tag VM API
// @Description get VM dashboard URL
// @Success 200 {object} Response The Response object
// @router /get-vm-dashboard-url [get]
func (c *ApiController) GetVmDashboardUrl() {
	dashboardUrl := os.Getenv("VM_DASHBOARD_URL")
	if dashboardUrl == "" {
		dashboardUrl = "http://localhost:19000"
	}

	c.ResponseOk(dashboardUrl)
}
