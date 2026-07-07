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
	// Force the default global-admin set ({admin, built-in}) deterministically,
	// independent of any ambient globalAdminOrgs config.
	t.Setenv("globalAdminOrgs", "")

	cases := []struct {
		name string
		user *iam.User
		want bool
	}{
		{"nil user", nil, false},
		{"org admin (maxpower/dave) is NOT global", &iam.User{Owner: "maxpower", Name: "dave", IsAdmin: true}, false},
		{"org admin (hanzo/z) is NOT global by default", &iam.User{Owner: "hanzo", Name: "z", IsAdmin: true}, false},
		{"admin-org member who is admin IS global", &iam.User{Owner: "admin", Name: "admin", IsAdmin: true}, true},
		{"built-in-org member who is admin IS global", &iam.User{Owner: "built-in", Name: "admin", IsAdmin: true}, true},
		{"admin-org member who is NOT admin is NOT global", &iam.User{Owner: "admin", Name: "viewer", IsAdmin: false}, false},
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

// TestGlobalAdminOrgsEnvOverride documents that the deployed globalAdminOrgs env
// override is authoritative over the default. The LIVE value is "admin,hanzo"
// (which grants hanzo-org admins global power) — this is FLAGGED: the canonical
// definition is {admin, built-in} (see defaultGlobalAdminOrgs / LLM.md), and the
// live env should be aligned to "admin,built-in" once the hanzo-org provider-config
// workflow is confirmed to run via the admin org.
func TestGlobalAdminOrgsEnvOverride(t *testing.T) {
	t.Setenv("globalAdminOrgs", "admin,hanzo")
	if !IsGlobalAdmin(&iam.User{Owner: "hanzo", Name: "z", IsAdmin: true}) {
		t.Error("override admin,hanzo: a hanzo-org admin IS global (current live behavior)")
	}
	if !IsGlobalAdmin(&iam.User{Owner: "admin", Name: "a", IsAdmin: true}) {
		t.Error("override admin,hanzo: admin-org admin remains global")
	}
	if IsGlobalAdmin(&iam.User{Owner: "built-in", Name: "a", IsAdmin: true}) {
		t.Error("override admin,hanzo: built-in is NOT listed, so not global under this override")
	}
	// The exact /get-account acceptance pairing under the live config: z@hanzo
	// (owner=hanzo) → true (its all-orgs masquerade works), a customer org owner
	// (maxpower/dave) → false (never a platform admin), even though maxpower/dave
	// IS an org-level admin.
	if IsGlobalAdmin(&iam.User{Owner: "maxpower", Name: "dave", IsAdmin: true}) {
		t.Error("override admin,hanzo: a customer org owner (maxpower) is NOT global")
	}
}
