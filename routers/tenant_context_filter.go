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
// observability. The org is taken from the VERIFIED principal (GetEffectiveOrg),
// NOT the raw X-Org-Id header: on the direct ingress that header is
// client-controlled, so storing it verbatim would let any caller spoof a tenant.
// GetEffectiveOrg honors the header only for the principal's own org (or a global
// admin), so the stored org is always the caller's real tenant.
func TenantContextFilter(ctx *context.Context) {
	orgID := GetEffectiveOrg(ctx)
	userID := getTenantHeader(ctx, "X-IAM-User-Id")
	projectID := getTenantHeader(ctx, "X-IAM-Project-Id")
	env := getTenantHeader(ctx, "X-IAM-Env")

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
