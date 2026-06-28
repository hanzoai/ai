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

	iam "github.com/hanzoai/iam"
)

const (
	UserTypeChatAdmin       = "chat-admin"
	UserTypeVideoNormalUser = "video-normal-user"
)

func IsAnonymousUserByUsername(username string) bool {
	return strings.HasPrefix(username, "u-") && len(username) == 10
}

// IsAdmin checks if the user is either a system admin or a chat-admin
func IsAdmin(user *iam.User) bool {
	if user == nil {
		return false
	}
	return user.IsAdmin || user.Type == UserTypeChatAdmin || user.Tag == "教师"
}

// IsVideoNormalUser checks if the user has the video-normal-user role
func IsVideoNormalUser(user *iam.User) bool {
	if user == nil {
		return false
	}
	return user.Type == UserTypeVideoNormalUser
}

// IsBalanceExemptUser reports whether the caller is in the comma-separated
// BALANCE_EXEMPT_USERS allowlist. It matches on either the per-org "owner" key
// or the per-user "owner/name" key. This is the ONE place the exemption policy
// lives: both the gateway balance filter (routers) and the chat controller
// (controllers) call it, so a service account such as hanzo/z bypasses billing
// consistently — the filter no longer shadows the controller's exemption.
func IsBalanceExemptUser(exemptCSV, orgKey, userKey string) bool {
	if exemptCSV == "" {
		return false
	}
	for _, e := range strings.Split(exemptCSV, ",") {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if (orgKey != "" && e == orgKey) || (userKey != "" && e == userKey) {
			return true
		}
	}
	return false
}
