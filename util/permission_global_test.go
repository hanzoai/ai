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

	iam "github.com/hanzoai/iam"
)

// TestIsGlobalAdminVsOrgAdmin proves the core distinction: an org owner who is
// admin within their OWN org is NOT a global admin, while an admin in the global
// admin org IS. This is the gate for platform-wide AI ops (provider config).
func TestIsGlobalAdminVsOrgAdmin(t *testing.T) {
	cases := []struct {
		name string
		user *iam.User
		want bool
	}{
		{"nil user", nil, false},
		{"org admin (maxpower/dave) is NOT global", &iam.User{Owner: "maxpower", Name: "dave", IsAdmin: true}, false},
		{"org admin (hanzo/z) is NOT global by default", &iam.User{Owner: "hanzo", Name: "z", IsAdmin: true}, false},
		{"global-org member who is admin IS global", &iam.User{Owner: "admin", Name: "admin", IsAdmin: true}, true},
		{"global-org member who is NOT admin is NOT global", &iam.User{Owner: "admin", Name: "viewer", IsAdmin: false}, false},
		{"non-admin in customer org is NOT global", &iam.User{Owner: "maxpower", Name: "bob", IsAdmin: false}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsGlobalAdmin(tc.user); got != tc.want {
				t.Errorf("IsGlobalAdmin(%+v) = %v, want %v", tc.user, got, tc.want)
			}
		})
	}

	// Org-level IsAdmin must still report true for an org owner — proving the two
	// checks are distinct, not aliases.
	orgAdmin := &iam.User{Owner: "maxpower", Name: "dave", IsAdmin: true}
	if !IsAdmin(orgAdmin) {
		t.Error("org admin must still satisfy IsAdmin (org-scoped)")
	}
	if IsGlobalAdmin(orgAdmin) {
		t.Error("org admin must NOT satisfy IsGlobalAdmin (global-scoped)")
	}
}
