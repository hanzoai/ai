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
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/beego/context"
)

const (
	tenantContextOrgIDKey     = "tenant.orgId"
	tenantContextUserIDKey    = "tenant.userId"
	tenantContextProjectIDKey = "tenant.projectId"
	tenantContextEnvKey       = "tenant.env"
)

func getTenantHeader(ctx *context.Context, name string) string {
	return strings.TrimSpace(ctx.Input.Header(name))
}

// TenantContextFilter captures IAM identity context for downstream scoping and
// observability. The org is taken from the VERIFIED principal (GetOrg),
// NOT the raw X-Org-Id header: on the direct ingress that header is
// client-controlled, so storing it verbatim would let any caller spoof a tenant.
// GetOrg honors the header only for the principal's own org (or a global
// admin), so the stored org is always the caller's real tenant.
func TenantContextFilter(ctx *context.Context) {
	// Canonical gateway-minted identity + browser sub-scopes — no X-IAM-*
	// prefix (cloud middleware_identity injects X-User-Id/X-Org-Id; console2
	// stamps X-Project-Id/X-Environment). The X-IAM-* variants were never sent,
	// so user/project/env context was silently empty on the direct path.
	orgID := GetOrg(ctx)
	userID := getTenantHeader(ctx, "X-User-Id")
	projectID := getTenantHeader(ctx, "X-Project-Id")
	env := getTenantHeader(ctx, "X-Environment")

	if orgID != "" {
		ctx.Input.SetData(tenantContextOrgIDKey, orgID)
	}
	if userID != "" {
		ctx.Input.SetData(tenantContextUserIDKey, userID)
	}
	if projectID != "" {
		ctx.Input.SetData(tenantContextProjectIDKey, projectID)
	}
	if env != "" {
		ctx.Input.SetData(tenantContextEnvKey, env)
	}

	// Thread the observability attribution onto the Go REQUEST context so the
	// single telemetry funnel (controllers.recordTrace) can stamp the cloud_usage
	// ledger row + the gen_ai span WITHOUT re-reading beego state at each of the
	// ~15 emit sites: the project sub-scope, the client session id, and a
	// NON-reversible ref of the caller credential (SHA-256 of the bearer — never
	// the plaintext key). Replacing ctx.Request propagates to the handler's
	// c.Ctx.Request.Context().
	attr := object.GenAIAttribution{
		Project:    projectID,
		Session:    getTenantHeader(ctx, "X-Session-Id"),
		APIKeyHash: hashBearer(getTenantHeader(ctx, "Authorization")),
	}
	if ctx.Request != nil {
		ctx.Request = ctx.Request.WithContext(object.WithGenAIAttribution(ctx.Request.Context(), attr))
	}
}

// hashBearer returns a non-reversible ref for the caller credential: the SHA-256
// hex of the bearer token ("Bearer " stripped). Empty in → empty out (never a hash
// of ""). The plaintext key is never stored or logged — only this ref.
func hashBearer(authz string) string {
	tok := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(authz), "Bearer "))
	if tok == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

func getTenantContextValue(ctx *context.Context, key string) string {
	if ctx == nil {
		return ""
	}
	value := ctx.Input.GetData(key)
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

// GetTenantOrgID returns the org from IAM context.
func GetTenantOrgID(ctx *context.Context) string {
	return getTenantContextValue(ctx, tenantContextOrgIDKey)
}

// GetTenantUserID returns the user ID from IAM context.
func GetTenantUserID(ctx *context.Context) string {
	return getTenantContextValue(ctx, tenantContextUserIDKey)
}

// GetTenantProjectID returns the project ID from IAM context.
func GetTenantProjectID(ctx *context.Context) string {
	return getTenantContextValue(ctx, tenantContextProjectIDKey)
}
