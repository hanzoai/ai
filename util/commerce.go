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

package util

import (
	"net/http"
	"os"
	"strings"
)

// Commerce billing is the single source of truth for prepaid balance and usage.
// This file holds the ONE way every commerce billing call in this service is
// authenticated and tenant-scoped, so the balance gate, the controller backstop,
// and the usage queue all speak commerce's canonical contract identically.
//
// Contract (commerce/api/billing, mounted under /v1 — see commerce
// cmd/commerced/main.go Router.Group("/v1")):
//
//	Authorization: Bearer {COMMERCE_SERVICE_TOKEN}   (admin-scoped S2S token)
//	X-IAM-Org-Id:  {org}                             (tenant namespace)
//
// Commerce resolves the tenant from X-IAM-Org-Id (commerce middleware
// /accesstoken.go); without it commerce falls back to COMMERCE_SERVICE_ORG,
// which cross-bills tenants. The user query param ("owner/name") identifies the
// ledger; the header identifies the namespace.
const HeaderIAMOrg = "X-IAM-Org-Id"

// OrgFromUserKey returns the IAM org (owner) segment of an "owner/name" user
// key, e.g. "hanzo/alice" -> "hanzo". Returns "" when userKey is empty or has
// no owner segment, in which case callers must omit the org header (commerce
// then applies its configured default).
func OrgFromUserKey(userKey string) string {
	owner, _, found := strings.Cut(userKey, "/")
	if !found {
		return ""
	}
	return strings.TrimSpace(owner)
}

// SetCommerceAuthHeaders applies the canonical commerce billing auth + tenant
// headers to req. token is the commerce service token (may be ""); userKey is
// the "owner/name" identity whose owner becomes X-IAM-Org-Id. This is the only
// place these headers are set for commerce calls.
func SetCommerceAuthHeaders(req *http.Request, token, userKey string) {
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if org := OrgFromUserKey(userKey); org != "" {
		req.Header.Set(HeaderIAMOrg, org)
	}
}

// BillingFailOpen reports whether commerce-unreachable should ALLOW paid traffic
// (revenue leak) instead of denying it. Default is false: the balance gate is
// fail-closed, matching the commerce metering client and the gateway's prepaid
// gate. Set BILLING_FAIL_OPEN=true only where availability deliberately outranks
// billing, and accept that commerce outages then bill nothing.
func BillingFailOpen() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("BILLING_FAIL_OPEN"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
