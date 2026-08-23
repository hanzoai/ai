// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"

	"github.com/hanzoai/ai/util"
)

// SUPER-ADMIN IS ORG MEMBERSHIP AND NOTHING ELSE.
//
// There are two admin scopes and conflating them is a privilege escalation, not
// a naming quibble: an ORG admin administers their own tenant, and a super admin
// is the platform's own sudo, held by membership of the reserved admin org. A
// gate that accepted the first for the second would hand every customer's org
// admin the cross-tenant surface.
//
// So the boundary is asserted from the side that would be the bug: an org admin,
// flagged as admin, in a tenant that is not the reserved one, is not a super
// admin — and holds no more of the platform than an anonymous caller does.
func TestAnOrgAdminIsNotAPlatformSuperAdmin(t *testing.T) {
	for _, tc := range []struct {
		what string
		user *iam.User
		want bool
	}{
		{"nobody", nil, false},
		{"an ordinary member of a tenant", &iam.User{Owner: "acme", Name: "alice"}, false},
		{"that tenant's own admin", &iam.User{Owner: "acme", Name: "boss", IsAdmin: true}, false},
		{"a chat admin by type", &iam.User{Owner: "acme", Name: "mod", Type: util.UserTypeChatAdmin}, false},
		{"a member of the reserved org", &iam.User{Owner: util.AdminOrg, Name: "root"}, true},
		{"a member of the reserved org who is not flagged admin", &iam.User{Owner: util.AdminOrg, Name: "ops"}, true},
	} {
		t.Run(tc.what, func(t *testing.T) {
			if got := util.IsSuperAdmin(tc.user); got != tc.want {
				t.Errorf("IsSuperAdmin = %v, want %v", got, tc.want)
			}
		})
	}

	// And the two predicates genuinely differ, so this is a boundary rather than
	// two names for one answer.
	orgAdmin := &iam.User{Owner: "acme", Name: "boss", IsAdmin: true}
	if !util.IsAdmin(orgAdmin) || util.IsSuperAdmin(orgAdmin) {
		t.Error("an org admin must be an admin and must not be a super admin")
	}
}

// The super-admin routes are gated in EVERY posture — preview mode relaxes reads,
// and it does not relax these. That is the whole point of the set: it is the
// group's slice of the endpoints that are never opened.
func TestTheSuperAdminRoutesAreNeverRelaxedByPreviewMode(t *testing.T) {
	for _, preview := range []string{"true", "false"} {
		t.Run("DISABLE_PREVIEW_MODE="+preview, func(t *testing.T) {
			t.Setenv("DISABLE_PREVIEW_MODE", preview)
			for name := range zapMiscSuperAdmin {
				if deny := zapMiscAuthz(name, nil); deny == nil {
					t.Errorf("%q let an anonymous caller through", name)
				}
				orgAdmin := &iam.User{Owner: "acme", Name: "boss", IsAdmin: true}
				if deny := zapMiscAuthz(name, orgAdmin); deny == nil {
					t.Errorf("%q let a tenant's own admin through; it is platform sudo", name)
				}
				root := &iam.User{Owner: util.AdminOrg, Name: "root"}
				if deny := zapMiscAuthz(name, root); deny != nil {
					t.Errorf("%q refused a member of the reserved org", name)
				}
			}
		})
	}
}
