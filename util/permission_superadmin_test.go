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
	"testing"

	iam "github.com/hanzoai/iam-v1"
)

// TestIsSuperAdminVsOrgAdmin pins the ONE super-admin rule: membership in the
// reserved `admin` org (AdminOrg) IS super admin — nothing else. An org owner who
// is admin within their OWN org (hanzo/z, maxpower/dave) is NOT a super admin;
// the retired "built-in" org is NOT super admin; and — matching IAM's
// user.IsGlobalAdmin (owner == conf.AdminOrg) and the console isSuperAdminAccount
// gate — admin-org membership ALONE is authoritative, with no isAdmin requirement
// and no configurable org list.
func TestIsSuperAdminVsOrgAdmin(t *testing.T) {
	cases := []struct {
		name string
		user *iam.User
		want bool
	}{
		{"nil user", nil, false},
		{"customer org owner (maxpower/dave) is NOT super", &iam.User{Owner: "maxpower", Name: "dave", IsAdmin: true}, false},
		{"tenant org admin (hanzo/z) is NOT super", &iam.User{Owner: "hanzo", Name: "z", IsAdmin: true}, false},
		{"admin-org member (admin/z) IS super", &iam.User{Owner: AdminOrg, Name: "z", IsAdmin: true}, true},
		{"admin-org member without isAdmin IS super (membership alone)", &iam.User{Owner: AdminOrg, Name: "viewer", IsAdmin: false}, true},
		{"retired built-in org is NOT super", &iam.User{Owner: "built-in", Name: "admin", IsAdmin: true}, false},
		{"non-admin in customer org is NOT super", &iam.User{Owner: "maxpower", Name: "bob", IsAdmin: false}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsSuperAdmin(tc.user); got != tc.want {
				t.Errorf("IsSuperAdmin(%+v) = %v, want %v", tc.user, got, tc.want)
			}
		})
	}

	// Org-level IsAdmin must still report true for an org owner — proving the two
	// checks are distinct, not aliases: org admin != super admin.
	orgAdmin := &iam.User{Owner: "maxpower", Name: "dave", IsAdmin: true}
	if !IsAdmin(orgAdmin) {
		t.Error("org admin must still satisfy IsAdmin (org-scoped)")
	}
	if IsSuperAdmin(orgAdmin) {
		t.Error("org admin must NOT satisfy IsSuperAdmin (admin-org-scoped)")
	}
}
