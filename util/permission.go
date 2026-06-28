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
	"strings"

	"github.com/hanzoai/ai/conf"
	iam "github.com/hanzoai/iam"
)

const (
	UserTypeChatAdmin       = "chat-admin"
	UserTypeVideoNormalUser = "video-normal-user"

	// defaultGlobalAdminOrg is the org whose admins are platform-wide (global)
	// admins. IAM's User has no IsGlobalAdmin flag — global authority is org
	// membership: only members of this org (admins of the global tenant) may
	// perform platform-wide AI ops. Overridable via the `globalAdminOrgs` app
	// config (comma-separated) so additional platform orgs can be designated
	// without a rebuild.
	defaultGlobalAdminOrg = "admin"
)

func IsAnonymousUserByUsername(username string) bool {
	return strings.HasPrefix(username, "u-") && len(username) == 10
}

// IsAdmin checks if the user is an ORG-level admin (or a chat-admin). This is
// org-scoped: it is true for the admin/owner of ANY org (e.g. a customer org
// owner). It must NOT be used to gate platform-wide operations — see
// IsGlobalAdmin for that.
func IsAdmin(user *iam.User) bool {
	if user == nil {
		return false
	}
	return user.IsAdmin || user.Type == UserTypeChatAdmin || user.Tag == "教师"
}

// globalAdminOrgSet returns the set of org slugs whose admins are GLOBAL
// (platform) admins, from the `globalAdminOrgs` app config (comma-separated),
// defaulting to {defaultGlobalAdminOrg}.
func globalAdminOrgSet() map[string]struct{} {
	out := map[string]struct{}{}
	raw := strings.TrimSpace(conf.GetConfigString("globalAdminOrgs"))
	if raw == "" {
		out[defaultGlobalAdminOrg] = struct{}{}
		return out
	}
	for _, o := range strings.Split(raw, ",") {
		if o = strings.ToLower(strings.TrimSpace(o)); o != "" {
			out[o] = struct{}{}
		}
	}
	return out
}

// IsGlobalAdmin reports whether the user is a PLATFORM (global) admin: an admin
// AND a member of a configured global-admin org. This is strictly narrower than
// IsAdmin — an org owner like maxpower/dave (IsAdmin within their own org) is
// NOT a global admin. Platform-wide AI ops (provider/upstream-key config,
// topology disclosure) require this.
func IsGlobalAdmin(user *iam.User) bool {
	if user == nil || !user.IsAdmin {
		return false
	}
	_, ok := globalAdminOrgSet()[strings.ToLower(user.Owner)]
	return ok
}

// IsVideoNormalUser checks if the user has the video-normal-user role
func IsVideoNormalUser(user *iam.User) bool {
	if user == nil {
		return false
	}
	return user.Type == UserTypeVideoNormalUser
}
