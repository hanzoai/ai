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
	tenantContextProjectIDKey = "tenant.projectId"
	tenantContextTenantIDKey  = "tenant.tenantId"
	tenantContextActorIDKey   = "tenant.actorId"
	tenantContextEnvKey       = "tenant.env"
)

func normalizeTenantHeader(value string) string {
	return strings.TrimSpace(value)
}

func getTenantHeader(ctx *context.Context, name string) string {
	return normalizeTenantHeader(ctx.Input.Header(name))
}

func setTenantContextValue(ctx *context.Context, key string, value string) {
	if value == "" {
		return
	}
	ctx.Input.SetData(key, value)
}

// TenantContextFilter captures upstream multi-tenant routing headers so
// downstream handlers can scope operations without depending on query params.
func TenantContextFilter(ctx *context.Context) {
	orgID := getTenantHeader(ctx, "X-Org-ID")
	projectID := getTenantHeader(ctx, "X-Project-ID")
	tenantID := getTenantHeader(ctx, "X-Tenant-ID")
	actorID := getTenantHeader(ctx, "X-Actor-ID")
	env := getTenantHeader(ctx, "X-Env")

	if tenantID == "" {
		tenantID = orgID
	}

	setTenantContextValue(ctx, tenantContextOrgIDKey, orgID)
	setTenantContextValue(ctx, tenantContextProjectIDKey, projectID)
	setTenantContextValue(ctx, tenantContextTenantIDKey, tenantID)
	setTenantContextValue(ctx, tenantContextActorIDKey, actorID)
	setTenantContextValue(ctx, tenantContextEnvKey, env)
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

// GetTenantOrgID resolves org context from request metadata.
func GetTenantOrgID(ctx *context.Context) string {
	value := getTenantContextValue(ctx, tenantContextOrgIDKey)
	if value != "" {
		return value
	}
	return getTenantHeader(ctx, "X-Org-ID")
}

// GetTenantProjectID resolves project context from request metadata.
func GetTenantProjectID(ctx *context.Context) string {
	value := getTenantContextValue(ctx, tenantContextProjectIDKey)
	if value != "" {
		return value
	}
	return getTenantHeader(ctx, "X-Project-ID")
}

// GetTenantActorID resolves actor/user context from request metadata.
func GetTenantActorID(ctx *context.Context) string {
	value := getTenantContextValue(ctx, tenantContextActorIDKey)
	if value != "" {
		return value
	}
	return getTenantHeader(ctx, "X-Actor-ID")
}

