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

	iam "github.com/hanzoai/ai/internal/iam"
)

// TestIsSuperAdminVsOrgAdmin pins the ONE SuperAdmin rule here: the host
// boundary's verdict on the request (iam.User.Sudo), and nothing read off the row.
// A row in the admin org without the verdict is not the SuperAdmin — the boundary
// refuses an admin-org application, an admin-org person's API key and an org
// spelled like the reserved one, and this module must not admit what it refused.
// An org owner who administers their OWN org is never the SuperAdmin.
func TestIsSuperAdminVsOrgAdmin(t *testing.T) {
	cases := []struct {
		name string
		user *iam.User
		want bool
	}{
		{"nil user", nil, false},
		{"customer org owner (maxpower/dave) is NOT super", &iam.User{Owner: "maxpower", Name: "dave", IsAdmin: true}, false},
		{"tenant org admin (hanzo/z) is NOT super", &iam.User{Owner: "hanzo", Name: "z", IsAdmin: true}, false},
		{"an admin-org row without the host verdict is NOT super", &iam.User{Owner: "admin", Name: "z", IsAdmin: true}, false},
		{"an admin-org application is NOT super", &iam.User{Owner: "admin", Name: "provisioner", Type: "application"}, false},
		{"an org spelled Admin is NOT super", &iam.User{Owner: "Admin", Name: "z"}, false},
		{"an org spelled ADMIN is NOT super", &iam.User{Owner: "ADMIN", Name: "z"}, false},
		{"the host verdict IS super", &iam.User{Owner: "admin", Name: "z", Sudo: true}, true},
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
	// checks are distinct, not aliases: org admin != SuperAdmin.
	orgAdmin := &iam.User{Owner: "maxpower", Name: "dave", IsAdmin: true}
	if !IsAdmin(orgAdmin) {
		t.Error("org admin must still satisfy IsAdmin (org-scoped)")
	}
	if IsSuperAdmin(orgAdmin) {
		t.Error("org admin must NOT satisfy IsSuperAdmin")
	}
}
