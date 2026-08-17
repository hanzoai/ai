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
	"github.com/zap-proto/zip"
)

const (
	tenantContextOrgIDKey     = "tenant.orgId"
	tenantContextUserIDKey    = "tenant.userId"
	tenantContextProjectIDKey = "tenant.projectId"
	tenantContextEnvKey       = "tenant.env"
)

func getTenantHeader(c *zip.Ctx, name string) string {
	return strings.TrimSpace(c.Header(name))
}

// firstNonEmptyHeader returns the first non-empty value among the named headers.
// A session id may arrive under either X-Session-Id (Hanzo convention) or the
// OpenAI/librechat-style X-Conversation-Id — honor both so a client that sends
// either turns the o11y sessions view on.
func firstNonEmptyHeader(c *zip.Ctx, names ...string) string {
	for _, n := range names {
		if v := getTenantHeader(c, n); v != "" {
			return v
		}
	}
	return ""
}

// TenantContextFilter captures IAM identity context for downstream scoping and
// observability. The org is taken from the VERIFIED principal (GetOrg),
// NOT the raw X-Org-Id header: on the direct ingress that header is
// client-controlled, so storing it verbatim would let any caller spoof a tenant.
// GetOrg honors the header only for the principal's own org (or a global
// admin), so the stored org is always the caller's real tenant.
func TenantContextFilter(c *zip.Ctx) error {
	// Canonical gateway-minted identity + browser sub-scopes — no X-IAM-*
	// prefix (cloud middleware_identity injects X-User-Id/X-Org-Id; console2
	// stamps X-Project-Id/X-Environment). The X-IAM-* variants were never sent,
	// so user/project/env context was silently empty on the direct path.
	orgID := GetOrg(c)
	userID := getTenantHeader(c, "X-User-Id")
	projectID := getTenantHeader(c, "X-Project-Id")
	env := getTenantHeader(c, "X-Environment")

	if orgID != "" {
		c.Locals(tenantContextOrgIDKey, orgID)
	}
	if userID != "" {
		c.Locals(tenantContextUserIDKey, userID)
	}
	if projectID != "" {
		c.Locals(tenantContextProjectIDKey, projectID)
	}
	if env != "" {
		c.Locals(tenantContextEnvKey, env)
	}

	// Thread the observability attribution onto the Go REQUEST context so the
	// single telemetry funnel (controllers.recordTrace) can stamp the cloud_usage
	// ledger row + the gen_ai span WITHOUT re-reading the router state at each of the
	// ~15 emit sites: the project sub-scope, the client session id, and a
	// NON-reversible ref of the caller credential (SHA-256 of the bearer — never
	// the plaintext key). Replacing ctx.Request propagates to the handler's
	// c.Ctx.Request.Context().
	attr := object.GenAIAttribution{
		Org:         orgID,
		Project:     projectID,
		Session:     firstNonEmptyHeader(c, "X-Session-Id", "X-Conversation-Id"),
		Environment: env,
		APIKeyHash:  hashBearer(getTenantHeader(c, "Authorization")),
		// The person a service credential is acting for. Already read above for the
		// router state; carrying it here is what lets a spend row name a human when
		// the only credential on the call is an application's. Trusted only for a
		// machine credential — usageRecord.bind is where that rule lives.
		User: userID,
	}
	// Onto the request's own context, which is what the handler reads: zip.SetContext
	// replaces it, so every emit site downstream sees the attribution without
	// re-reading router state.
	c.SetContext(object.WithGenAIAttribution(c.Context(), attr))
	return c.Continue()
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

func getTenantContextValue(c *zip.Ctx, key string) string {
	if c == nil {
		return ""
	}
	value := c.Locals(key)
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

// GetTenantOrgID returns the org from IAM web.
func GetTenantOrgID(c *zip.Ctx) string {
	return getTenantContextValue(c, tenantContextOrgIDKey)
}

// GetTenantUserID returns the user ID from IAM web.
func GetTenantUserID(c *zip.Ctx) string {
	return getTenantContextValue(c, tenantContextUserIDKey)
}

// GetTenantProjectID returns the project ID from IAM web.
func GetTenantProjectID(c *zip.Ctx) string {
	return getTenantContextValue(c, tenantContextProjectIDKey)
}
