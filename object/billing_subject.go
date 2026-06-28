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
// account; everything (the gate, the usage debit, the starter-credit grant) must
// agree on it or a user tops up one account and spends from another.
//
// Two billing models, distinguished only by the org:
//
//   - PERSONAL-billing org (default: the shared "hanzo" catch-all, the home of
//     every unaffiliated individual signup): subject = "owner/name", so each
//     member has their OWN balance and their OWN starter credit and can never
//     drain another member. This is the fix for new gmail signups, who all land
//     in owner=hanzo and previously shared the single (hanzo,hanzo) balance.
//
//   - POOLED org (any dedicated customer/team org, e.g. "maxpower"): subject =
//     "owner", one balance for the whole org — unchanged, so the proven per-org
//     billing keeps working with no regression.

var personalBillingOrgsOnce sync.Once
var personalBillingOrgs map[string]struct{}

// loadPersonalBillingOrgs parses PERSONAL_BILLING_ORGS (comma-separated org
// slugs, default "hanzo"). Lazy + cached so tests can set the env before first
// use. Fail-safe default keeps the shared catch-all per-user even if unset.
func loadPersonalBillingOrgs() map[string]struct{} {
	personalBillingOrgsOnce.Do(func() {
		raw := os.Getenv("PERSONAL_BILLING_ORGS")
		if strings.TrimSpace(raw) == "" {
			raw = "hanzo"
		}
		m := make(map[string]struct{})
		for _, o := range strings.Split(raw, ",") {
			o = strings.ToLower(strings.TrimSpace(o))
			if o != "" {
				m[o] = struct{}{}
			}
		}
		personalBillingOrgs = m
	})
	return personalBillingOrgs
}

// IsPersonalBillingOrg reports whether members of org are billed individually
// (per-user) rather than sharing one org-pooled balance.
func IsPersonalBillingOrg(owner string) bool {
	_, ok := loadPersonalBillingOrgs()[strings.ToLower(strings.TrimSpace(owner))]
	return ok
}

// BillingSubject returns the canonical Commerce billing subject for an IAM
// (owner, name) identity — the value passed as ?user= / posted as the usage
// `user` / granted to as DestinationId. The namespace (X-Org-Id) is always
// `owner`; this is only the subject WITHIN that namespace:
//
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
	if IsPersonalBillingOrg(owner) {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			return owner
		}
		return owner + "/" + name
	}
	return owner
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
