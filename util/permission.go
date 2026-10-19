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

// ScopeOwner answers WHICH ORG a request acts on: the one it asked for when the
// caller is entitled to ask, and the caller's own otherwise.
//
// The entitlement is SuperAdmin and nothing else — the same verdict IsSuperAdmin
// reads — so a tenant's own admin administers that tenant and may not name
// another. An owner arriving on a request is therefore a REQUEST rather than a
// fact, and this is where it stops being one.
//
// It takes the resolved caller and the requested owner. How the caller was
// resolved, where the requested owner arrived — a body, a query, a path — and what
// a refusal should look like are each the caller's own business; five groups had
// written this rule out five times to keep those differences local. One rule, one
// place; the shapes around it stay where they are.
func ScopeOwner(caller *iam.User, requested string) string {
	if caller == nil {
		return ""
	}
	if IsSuperAdmin(caller) {
		if r := strings.TrimSpace(requested); r != "" {
			return r
		}
	}
	return caller.Owner
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

// IsSuperAdmin reports whether the request this user was resolved from carries
// the host boundary's SuperAdmin verdict (iam.User.Sudo, stamped by object.Verify
// from object.SetSuper). It is the ONE SuperAdmin predicate here, and it compares
// no org: the boundary decided it once, from the token it validated — a person
// whose own org is the reserved admin org (authz.Claims.Sudo) — and this module
// reading the org a credential names a second time is how an admin-org
// application, an admin-org person's API key and an org spelled "admin " came to be
// SuperAdmin here while the boundary refused them. A user resolved with no request
// behind it — an API key, a raw ZAP door, a principal assembled from strings —
// carries no verdict and is never one.
//
// It is strictly narrower than IsAdmin: a tenant-org owner who administers their
// OWN org is never a SuperAdmin. Platform-wide AI ops (provider/upstream-key
// config, cross-tenant reads, topology disclosure) require this.
func IsSuperAdmin(user *iam.User) bool {
	return user != nil && user.Sudo
}

// IsVideoNormalUser checks if the user has the video-normal-user role
func IsVideoNormalUser(user *iam.User) bool {
	if user == nil {
		return false
	}
	return user.Type == UserTypeVideoNormalUser
}
