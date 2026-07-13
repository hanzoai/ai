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

import "strings"

// ── Billing subject: ONE rule ────────────────────────────────────────────────
//
// Commerce scopes balance to a namespace (X-Org-Id = the IAM `owner` slug) and,
// within that namespace, to a SUBJECT that nets deposits against usage. The
// subject is the single billing account; the gate, the usage debit, and every
// grant must agree on it or a user tops up one account and spends from another.
//
// THE RULE (there is only one): the subject is ALWAYS the org — `owner`. Every
// request from any member of org X bills to org X's ONE account. There is no
// per-user wallet for the gate, no personal-vs-pooled split, no allowlist, and
// no exemption. A member's identity is still recorded for METRICS (the usage
// warehouse keeps `owner/name` verbatim, independent of this subject), so "who
// used what" survives while the money draws from the ONE org account.
//
// This is a value, not a place: the subject is a pure function of `owner`. The
// former ORG_BILLING_ORGS / PERSONAL_BILLING_ORGS env allowlists — two knobs
// deciding one boolean — are gone; the mode they toggled is now the invariant.

// BillingSubject returns the canonical Commerce billing subject for an IAM
// (owner, name) identity — the value passed as ?user= / posted as the usage
// `user` / granted to as DestinationId. The subject is the ORG (`owner`),
// lowercased; `name` is ignored (kept in the signature only so the many call
// sites that hold a name need not change). Empty owner yields "" (the caller
// fails closed / cannot bill).
//
// Lowercasing is load-bearing: read paths lowercase ?user= and RecordUsage
// stores SourceId verbatim, so an un-lowercased subject would record usage that
// never nets against the balance (a silent leak). Lowercasing at the single
// source of the subject closes that.
func BillingSubject(owner, name string) string {
	_ = name // billing is org-scoped; identity is recorded for metrics, not billing.
	return strings.ToLower(strings.TrimSpace(owner))
}

// BillingSubjectForPrincipal returns the billing subject for an authenticated
// principal. Because the subject is always the org, a human and a machine (M2M /
// client_credentials `application`) principal in the same org bill to the SAME
// org account — the carve-out that once kept a service token off an unfunded
// per-app wallet is now structural: there are no per-user wallets to fall into.
// userType is retained for caller-signature parity.
func BillingSubjectForPrincipal(owner, name, userType string) string {
	_ = name
	_ = userType
	return BillingSubject(owner, "")
}

// BillingSubjectFromUserKey is BillingSubject for callers that hold the
// "owner/name" user key as a single string (usage records, ZAP params,
// searchAuth.UserID). If owner is empty it is derived from the key's prefix.
func BillingSubjectFromUserKey(owner, userKey string) string {
	if strings.TrimSpace(owner) == "" {
		if i := strings.IndexByte(userKey, '/'); i >= 0 {
			owner = userKey[:i]
		} else {
			owner = userKey
		}
	}
	return BillingSubject(owner, "")
}
