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
	"strings"
)

// ── Who pays ─────────────────────────────────────────────────────────────────
//
// Commerce scopes balance to a namespace (X-Org-Id = the IAM `owner` slug) and,
// within it, to a SUBJECT — the account that nets deposits against usage. The
// gate, the usage debit and the starter-credit grant must all agree on the
// subject, or a user tops up one account and spends from another.
//
// A member of an org can have TWO accounts, and the org's comes first:
//
//	the ORG wallet      "owner"       funded by the org, shared by the team
//	the MEMBER's wallet "owner/name"  funded by the member, theirs alone
//
// So a request does not have one subject, it has an ordered CHAIN of them —
// BillingChain — and it is paid by the first account that can cover it. The org
// pays for its team; when the org's wallet is empty (or its spend limit is
// reached), a member who has their own credit keeps working on their own dime
// instead of being cut off. Nobody is blocked by someone else's budget, and no
// one silently spends the org's money after it has run out.
//
// The org's limit is what makes that useful: an org can cap what the pool will
// cover per member (OrgSettings.MemberLimitCents) — the Anthropic/Team model —
// and the overflow to personal is the release valve, not a failure.
//
// Which model an org uses is a property OF THE ORG, stored on the org
// (OrgSettings.Billing) and editable by an admin. It is not an environment
// variable, and no org's name appears in this source.
//
// This replaced two env allowlists that fought each other: PERSONAL_BILLING_ORGS
// defaulted to the literal string "hanzo", and ORG_BILLING_ORGS existed to undo
// that for the same org. The shipped default was therefore "bill each member
// separately" for the org holding the credits — so every request 402'd
// "Insufficient balance" against a funded account, because the gate reserved
// from a member's empty wallet while the money sat in the org's. Fixing it meant
// setting an env var in production, per tenant. Whether a tenant bills as a team
// is a customer fact, not a deployment fact.

// BillingChain is the ordered list of accounts a request may draw on: the first
// that can cover the cost pays it. Never empty for a valid owner.
type BillingChain []string

// Primary is the account a request bills when it can cover the cost — the one
// reported as THE subject where a single value is required.
func (c BillingChain) Primary() string {
	if len(c) == 0 {
		return ""
	}
	return c[0]
}

// BillingChainFor returns the accounts an (owner, name) identity can draw on, in
// the order they should be charged.
//
//	pooled org (default) → ["owner", "owner/name"]  org first, then the member
//	personal-billing org → ["owner/name"]           the member only
//
// A pooled org falls through to the member's own wallet, so a member with credit
// is never blocked by an exhausted org pool. A personal-billing org — a
// catch-all namespace of unaffiliated individual signups, who share a namespace
// but are not a team — has no shared wallet to fall back FROM, and one member
// must never be able to spend another's, so its chain is the member alone.
//
// Subjects are lowercased at this single source: the read paths lowercase
// ?user=, while RecordUsage stores SourceId verbatim, so an un-lowercased
// subject would record usage that never nets against the balance — a silent
// leak.
//
// An empty owner yields an empty chain (the caller cannot bill).
func BillingChainFor(owner, name string) BillingChain {
	owner = strings.ToLower(strings.TrimSpace(owner))
	if owner == "" {
		return nil
	}
	name = strings.ToLower(strings.TrimSpace(name))

	member := ""
	if name != "" {
		member = owner + "/" + name
	}

	if ResolveOrgBilling(owner) == BillingPersonal {
		if member == "" {
			return BillingChain{owner} // no member identity to bill
		}
		return BillingChain{member}
	}

	// Pooled (the default): the org pays for its team, and a member with their
	// own credit can keep going once the org's pool is spent.
	if member == "" {
		return BillingChain{owner}
	}
	return BillingChain{owner, member}
}

// BillingSubject returns the account an (owner, name) identity bills FIRST.
//
// Callers that can consult more than one account should use BillingChainFor and
// charge the first that covers the cost; this is the single-value answer for the
// places that record or report one subject.
func BillingSubject(owner, name string) string {
	return BillingChainFor(owner, name).Primary()
}

// BillingChainForPrincipal is BillingChainFor with the machine (M2M) carve-out.
//
// A client_credentials / application principal — Casdoor User.Type ==
// "application", e.g. the hanzo-cloud service token minted for cloud→cloud calls
// — bills its OWNER ORG and has no personal wallet to fall back to: an app name
// is not a funded account, so a chain through "owner/app-name" could only ever
// 402 legitimate service traffic. This is the ONE place that carve-out lives, so
// the router gate and the controller backstop can never disagree about who a
// service token bills.
func BillingChainForPrincipal(owner, name, userType string) BillingChain {
	if strings.EqualFold(strings.TrimSpace(userType), "application") {
		return BillingChainFor(owner, "")
	}
	return BillingChainFor(owner, name)
}

// BillingSubjectForPrincipal returns the account an authenticated principal bills
// first, accounting for machine (M2M) identities. See BillingChainForPrincipal.
func BillingSubjectForPrincipal(owner, name, userType string) string {
	return BillingChainForPrincipal(owner, name, userType).Primary()
}

// BillingChainFromUserKey is BillingChainFor for callers that already hold the
// "owner/name" user key as a single string (usage records, ZAP params,
// searchAuth.UserID). If owner is empty it is derived from the key's prefix.
func BillingChainFromUserKey(owner, userKey string) BillingChain {
	name := ""
	if i := strings.IndexByte(userKey, '/'); i >= 0 {
		if strings.TrimSpace(owner) == "" {
			owner = userKey[:i]
		}
		name = userKey[i+1:]
	} else {
		name = userKey
	}
	return BillingChainFor(owner, name)
}

// BillingSubjectFromUserKey returns the first account for an "owner/name" user
// key. See BillingChainFromUserKey.
func BillingSubjectFromUserKey(owner, userKey string) string {
	return BillingChainFromUserKey(owner, userKey).Primary()
}
