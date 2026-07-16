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

package object

import (
	"os"
	"strings"
	"sync"
)

// ── Per-user vs per-org billing identity ─────────────────────────────────────
//
// Commerce scopes balance to a namespace (X-Org-Id = the IAM `owner` slug)
// and, WITHIN that namespace, to a subject (the ?user= / SourceId / DestinationId
// key) that nets deposits against usage. The subject is the single billing
// account; everything (the gate, the usage debit, an explicit credit grant) must
// agree on it or a user tops up one account and spends from another.
//
// Two billing models, distinguished only by the org:
//
//   - PERSONAL-billing org (default: the shared "hanzo" catch-all, the home of
//     every unaffiliated individual signup): subject = "owner/name", so each
//     member has their OWN balance and can never
//     drain another member. This is the fix for new gmail signups, who all land
//     in owner=hanzo and previously shared the single (hanzo,hanzo) balance.
//
//   - POOLED org (any dedicated customer/team org, e.g. "maxpower"): subject =
//     "owner", one balance for the whole org — unchanged, so the proven per-org
//     billing keeps working with no regression.
//
//   - ORG-BILLING override (ORG_BILLING_ORGS allowlist): an org that WOULD be
//     personal-billing but is named here bills as ONE shared pool (subject =
//     "owner"), e.g. promoting the "hanzo" catch-all to a single org credit pool.
//     Empty/unset ⇒ no override; scoped, so no other tenant is affected. This is
//     the explicit switch that wins over the personal default (see isOrgBilling),
//     never the PERSONAL_BILLING_ORGS sentinel that would flip every tenant.

var (
	personalBillingOrgsOnce sync.Once
	personalBillingOrgs     map[string]struct{}

	orgBillingOrgsOnce sync.Once
	orgBillingOrgs     map[string]struct{}
)

// parseOrgSet parses a comma-separated list of org slugs into a lowercased set —
// the ONE parser both billing-org allowlists share.
func parseOrgSet(raw string) map[string]struct{} {
	m := make(map[string]struct{})
	for _, o := range strings.Split(raw, ",") {
		if o = strings.ToLower(strings.TrimSpace(o)); o != "" {
			m[o] = struct{}{}
		}
	}
	return m
}

// loadPersonalBillingOrgs parses PERSONAL_BILLING_ORGS (comma-separated org
// slugs, default "hanzo"). Lazy + cached so tests can set the env before first
// use. Fail-safe default keeps the shared catch-all per-user even if unset.
func loadPersonalBillingOrgs() map[string]struct{} {
	personalBillingOrgsOnce.Do(func() {
		raw := os.Getenv("PERSONAL_BILLING_ORGS")
		if strings.TrimSpace(raw) == "" {
			raw = "hanzo"
		}
		personalBillingOrgs = parseOrgSet(raw)
	})
	return personalBillingOrgs
}

// loadOrgBillingOrgs parses ORG_BILLING_ORGS (comma-separated org slugs, default
// EMPTY). Lazy + cached, same as the personal set. An org listed here shares ONE
// pooled wallet even when it would otherwise be personal-billing — the scoped,
// additive override; unset ⇒ no org is promoted (zero behavior change).
func loadOrgBillingOrgs() map[string]struct{} {
	orgBillingOrgsOnce.Do(func() {
		orgBillingOrgs = parseOrgSet(os.Getenv("ORG_BILLING_ORGS"))
	})
	return orgBillingOrgs
}

// IsPersonalBillingOrg reports whether members of org are billed individually
// (per-user) rather than sharing one org-pooled balance. The org-billing
// allowlist (isOrgBilling) overrides this in BillingSubject.
func IsPersonalBillingOrg(owner string) bool {
	_, ok := loadPersonalBillingOrgs()[strings.ToLower(strings.TrimSpace(owner))]
	return ok
}

// isOrgBilling reports whether org is on the ORG_BILLING_ORGS allowlist — its
// members share ONE pooled wallet (subject = owner). This is the explicit switch
// that WINS over the personal-billing default, so promoting the shared "hanzo"
// catch-all to a single org credit pool is a scoped env flip, not a code fork
// and not the every-tenant PERSONAL_BILLING_ORGS sentinel.
func isOrgBilling(owner string) bool {
	_, ok := loadOrgBillingOrgs()[strings.ToLower(strings.TrimSpace(owner))]
	return ok
}

// BillingSubject returns the canonical Commerce billing subject for an IAM
// (owner, name) identity — the value passed as ?user= / posted as the usage
// `user` / granted to as DestinationId. The namespace (X-Org-Id) is always
// `owner`; this is only the subject WITHIN that namespace:
//
//   - ORG_BILLING_ORGS org  → "owner"      (per-org, override — wins)
//   - personal-billing org  → "owner/name" (per-user)
//   - pooled org            → "owner"      (per-org)
//
// Always lowercased: the read paths lowercase ?user=, and RecordUsage stores
// SourceId verbatim, so an un-lowercased subject would record usage that never
// nets against the balance (a silent leak). Lowercasing at the single source of
// the subject closes that.
//
// Empty owner yields "" (caller fails open / cannot bill).
func BillingSubject(owner, name string) string {
	owner = strings.ToLower(strings.TrimSpace(owner))
	if owner == "" {
		return ""
	}
	// ORG-billing allowlist wins: the org shares ONE pooled wallet (subject =
	// owner), regardless of the personal-billing default. Scoped + additive —
	// only orgs named in ORG_BILLING_ORGS switch; every other tenant is unchanged.
	if isOrgBilling(owner) {
		return owner
	}
	if IsPersonalBillingOrg(owner) {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			return owner
		}
		return owner + "/" + name
	}
	return owner
}

// BillingSubjectForPrincipal returns the billing subject for an authenticated
// principal, accounting for machine (M2M) identities. A client_credentials /
// application principal — Casdoor User.Type == "application", e.g. the
// hanzo-cloud service token minted for cloud→cloud calls — is billed to its
// OWNER ORG, never to a per-app "owner/app-name" subject: the app name is not a
// funded account, so in a personal-billing org (the "hanzo" catch-all) it would
// resolve to the unfunded "hanzo/hanzo-cloud" and 402 legitimate service
// traffic. Human principals resolve exactly as BillingSubject (per-user for a
// personal-billing org, else the org). This is the ONE place the M2M carve-out
// lives, shared by the router gate and the controller backstop so they can
// never disagree on who a service token bills.
func BillingSubjectForPrincipal(owner, name, userType string) string {
	if strings.EqualFold(strings.TrimSpace(userType), "application") {
		return BillingSubject(owner, "")
	}
	return BillingSubject(owner, name)
}

// BillingSubjectFromUserKey is BillingSubject for callers that already hold the
// "owner/name" user key as a single string (e.g. usage records, ZAP params,
// searchAuth.UserID). If owner is empty it is derived from the key's prefix.
func BillingSubjectFromUserKey(owner, userKey string) string {
	name := ""
	if i := strings.IndexByte(userKey, '/'); i >= 0 {
		if strings.TrimSpace(owner) == "" {
			owner = userKey[:i]
		}
		name = userKey[i+1:]
	} else if strings.TrimSpace(owner) == "" {
		owner = userKey
	}
	return BillingSubject(owner, name)
}
