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
	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/util"
)

// GetEffectiveOrg resolves the organization for data-scoping in filters from the
// VERIFIED request principal — never a raw client header.
//
// On the direct (non-gateway) ingress the X-Org-Id header is fully
// client-controlled, so trusting it would let any caller act as any org
// (cross-tenant read/write + billing attribution). It is honored ONLY when it
// matches the authenticated principal's own org, or the principal is a global
// admin (cross-org platform access). A non-admin can never escape their own org
// via a header; an unauthenticated caller's header is ignored entirely.
//
// Behind the gateway the injected X-Org-Id equals the JWT owner, so this
// resolves identically — the gateway path is unaffected.
func GetEffectiveOrg(ctx *context.Context) string {
	requested := strings.TrimSpace(ctx.Input.Header("X-Org-Id"))

	user := sessionOrBearerUser(ctx)
	if user != nil && user.Owner != "" {
		if requested != "" && (requested == user.Owner || util.IsSuperAdmin(user)) {
			return requested
		}
		return user.Owner
	}

	// No verified principal: never trust a client-supplied org header.
	return conf.GetConfigString("IAM_ORG")
}
