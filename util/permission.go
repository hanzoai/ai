// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2025 The OpenAgent Authors. All Rights Reserved.
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
	"strings"

	iam "github.com/hanzoai/ai/internal/iam"
)

const (
	UserTypeChatAdmin       = "chat-admin"
	UserTypeVideoNormalUser = "video-normal-user"
)

// AdminOrg is the reserved IAM organization whose members are Hanzo SUPER admins
// (platform / cross-tenant). Membership in this ONE org — not a configurable
// list, not a "built-in" org — is the SOLE definition of super admin
// across the stack. It matches IAM's conf.AdminOrg (user.IsGlobalAdmin: owner ==
// AdminOrg) and the console super-admin gate (isSuperAdminAccount: owner ==
// 'admin'). It is deliberately NOT a TENANT org: a hanzo-org admin (e.g.
// hanzo/z) is an org admin, not a super admin, and must not read/modify platform
// provider config (upstream API keys) or cross-tenant data.
const AdminOrg = "admin"

// ScopeOwner answers WHICH ORG a request acts on: the one it asked for when the
// caller is entitled to ask, and the caller's own otherwise.
//
// The entitlement is membership of the reserved org and nothing else, which is
// the same predicate IsSuperAdmin reads — a tenant's own admin administers that
// tenant and may not name another. An owner arriving on a request is therefore a
// REQUEST rather than a fact, and this is where it stops being one.
//
// It takes the two values it needs and no more. How the caller was resolved,
// where the requested owner arrived — a body, a query, a path — and what a
// refusal should look like are each the caller's own business; five groups had
// written this rule out five times to keep those differences local, and had
// already drifted on whether the reserved org is spelled by its constant. One
// rule, one place; the shapes around it stay where they are.
func ScopeOwner(callerOrg, requested string) string {
	if callerOrg == AdminOrg {
		if r := strings.TrimSpace(requested); r != "" {
			return r
		}
	}
	return callerOrg
}

func IsAnonymousUserByUsername(username string) bool {
	return strings.HasPrefix(username, "u-") && len(username) == 10
}

// IsAnonymousUser reports whether a resolved user is an anonymous guest — the
// synthesized u-<hash> record from anonymousSignin. It is the ONE canonical
// anonymity predicate: a guest is stamped BOTH with Type "anonymous-user" and a
// u-<hash> username, and either signal alone is authoritative (a session copy or
// a JWT claim may carry one without the other). A nil user is NOT anonymous —
// absence of a user is a distinct "unauthenticated" state its callers handle.
func IsAnonymousUser(user *iam.User) bool {
	if user == nil {
		return false
	}
	return user.Type == "anonymous-user" || IsAnonymousUserByUsername(user.Name)
}

// IsAdmin checks if the user is an ORG-level admin (or a chat-admin). This is
// org-scoped: it is true for the admin/owner of ANY org (e.g. a customer org
// owner). It must NOT be used to gate platform-wide operations — see
// IsSuperAdmin for that.
func IsAdmin(user *iam.User) bool {
	if user == nil {
		return false
	}
	return user.IsAdmin || user.Type == UserTypeChatAdmin || user.Tag == "教师"
}

// IsSuperAdmin reports whether the user is a Hanzo SUPER admin: a member of the
// reserved `admin` org (AdminOrg). This is the ONE super-admin predicate — no
// configurable org list, no "built-in" org, no isAdmin flag: membership in the
// admin org alone is authoritative, matching IAM's user.IsGlobalAdmin (owner ==
// conf.AdminOrg) and the console isSuperAdminAccount gate. It is strictly
// narrower than IsAdmin — a tenant-org owner (hanzo/z, maxpower/dave), even one
// who is isAdmin of their OWN org, is NEVER a super admin. Platform-wide AI ops
// (provider/upstream-key config, cross-tenant reads, topology disclosure)
// require this.
func IsSuperAdmin(user *iam.User) bool {
	return user != nil && user.Owner == AdminOrg
}

// IsVideoNormalUser checks if the user has the video-normal-user role
func IsVideoNormalUser(user *iam.User) bool {
	if user == nil {
		return false
	}
	return user.Type == UserTypeVideoNormalUser
}
