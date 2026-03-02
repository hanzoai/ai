// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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

package routers

import (
	"strings"

	"github.com/beego/beego/context"
	"github.com/hanzoai/cloud/conf"
)

// GetEffectiveOrg resolves the organization for data-scoping in filters.
// Resolution order:
//  1. X-Hanzo-Org-Id header (injected by gateway auth middleware from JWT)
//  2. Authenticated session user's Owner field
//  3. Config default (iamOrganization env/config value)
func GetEffectiveOrg(ctx *context.Context) string {
	// 1. Gateway-injected header (trusted, set after JWT validation)
	if orgID := strings.TrimSpace(ctx.Input.Header("X-Hanzo-Org-Id")); orgID != "" {
		return orgID
	}

	// 2. Authenticated session user's organization
	user := GetSessionUser(ctx)
	if user != nil && user.Owner != "" {
		return user.Owner
	}

	// 3. Config fallback
	return conf.GetConfigString("iamOrganization")
}
