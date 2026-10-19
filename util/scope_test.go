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
	super := func(org string) *iam.User { return &iam.User{Owner: org, Name: "z", Sudo: true} }
	tenant := func(org string) *iam.User { return &iam.User{Owner: org, Name: "z"} }
	for _, tc := range []struct {
		what      string
		caller    *iam.User
		requested string
		want      string
	}{
		{"a tenant asking for nothing gets its own", tenant("acme"), "", "acme"},
		{"a tenant asking for another gets its own", tenant("acme"), "victim", "acme"},
		{"a tenant asking for itself gets its own", tenant("acme"), "acme", "acme"},
		{"an admin-org row without the verdict gets its own", tenant("admin"), "victim", "admin"},
		{"the SuperAdmin asking for nothing gets its own", super("admin"), "", "admin"},
		{"the SuperAdmin may name one", super("admin"), "victim", "victim"},
		{"whitespace is not a name", super("admin"), "   ", "admin"},
		{"and is trimmed when it is one", super("admin"), "  victim  ", "victim"},
		{"a caller with no org gets no org", tenant(""), "victim", ""},
		{"no caller gets no org", nil, "victim", ""},
	} {
		t.Run(tc.what, func(t *testing.T) {
			if got := ScopeOwner(tc.caller, tc.requested); got != tc.want {
				t.Errorf("ScopeOwner(%+v, %q) = %q, want %q", tc.caller, tc.requested, got, tc.want)
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
	if got := ScopeOwner(orgAdmin, "victim"); got != "acme" {
		t.Errorf("an org admin named another org and got %q", got)
	}
}
