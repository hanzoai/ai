// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package util

import (
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"
)

// The rule that decides which org a request acts on, asserted once because it now
// lives once.
//
// It was written out five times — once per ZAP group — and the copies had already
// begun to differ on whether the reserved org is spelled by its constant. Five
// copies of a tenancy rule is five places for one of them to say yes where the
// others say no, and the failure that produces is a caller acting on somebody
// else's org.
func TestWhichOrgARequestActsOn(t *testing.T) {
	for _, tc := range []struct {
		what      string
		caller    string
		requested string
		want      string
	}{
		{"a tenant asking for nothing gets its own", "acme", "", "acme"},
		{"a tenant asking for another gets its own", "acme", "victim", "acme"},
		{"a tenant asking for itself gets its own", "acme", "acme", "acme"},
		{"the reserved org asking for nothing gets its own", AdminOrg, "", AdminOrg},
		{"the reserved org may name one", AdminOrg, "victim", "victim"},
		{"whitespace is not a name", AdminOrg, "   ", AdminOrg},
		{"and is trimmed when it is one", AdminOrg, "  victim  ", "victim"},
		{"a caller with no org gets no org", "", "victim", ""},
	} {
		t.Run(tc.what, func(t *testing.T) {
			if got := ScopeOwner(tc.caller, tc.requested); got != tc.want {
				t.Errorf("ScopeOwner(%q, %q) = %q, want %q", tc.caller, tc.requested, got, tc.want)
			}
		})
	}

	// The entitlement is the SAME one IsSuperAdmin reads. A tenant's own admin
	// administers that tenant and may not name another, so the flag that makes
	// somebody an org admin must not move this answer.
	orgAdmin := &iam.User{Owner: "acme", Name: "boss", IsAdmin: true}
	if !IsAdmin(orgAdmin) {
		t.Fatal("fixture is not an org admin")
	}
	if got := ScopeOwner(orgAdmin.Owner, "victim"); got != "acme" {
		t.Errorf("an org admin named another org and got %q", got)
	}
}
