// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v4"
	"github.com/hanzoai/account"

	"github.com/hanzoai/ai/internal/authtest"
)

// mintScopeJWT signs owner/name with an admin flag and a membership set given as
// "org:role" pairs, home org first — the order IAM signs them in.
func mintScopeJWT(t *testing.T, owner, name string, isAdmin bool, orgs ...string) string {
	t.Helper()
	key := authtest.Signing(t)
	claims := usageClaims(owner, name, "https://hanzo.id", "hanzo-cloud")
	claims["isAdmin"] = isAdmin
	refs := []map[string]string{}
	for _, pair := range orgs {
		org, role, _ := strings.Cut(pair, ":")
		refs = append(refs, map[string]string{"org": org, "role": role})
	}
	claims["orgs"] = refs
	tok, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return tok
}

func selecting(org string) context.Context {
	h, _ := json.Marshal(map[string]string{"X-Org-Id": org})
	return context.WithValue(context.Background(), gatewayHeaders{}, h)
}

// The router policy an admin reads and writes is the org the request selects —
// the org the router reads for that org's traffic — and the admin bit is the role
// held THERE, never the row flag of the caller's home org.
func TestRouterPolicyScope(t *testing.T) {
	staff := mintScopeJWT(t, "hanzo", "z", false, "hanzo:admin", "webby-ai:admin", "acme:member")
	owner := mintScopeJWT(t, "joshuafl369", "joshuafl369", true, "joshuafl369:owner", "webby-ai:owner")
	super := mintScopeJWT(t, "admin", "root", false, "admin:admin")

	cases := []struct {
		name      string
		auth      string
		selected  string
		wantOrg   string
		wantAdmin bool
		wantErr   bool
	}{
		{"member-org admin acts in the org it selects", staff, "webby-ai", "webby-ai", true, false},
		{"no selection stays home, home row flag decides", staff, "", "hanzo", false, false},
		{"owner of a team org acts there, not in the personal org", owner, "webby-ai", "webby-ai", true, false},
		{"owner with no selection stays in the personal org", owner, "", "joshuafl369", true, false},
		{"a plain member does not administer", staff, "acme", "acme", false, false},
		{"an org outside the claim is refused, never reinterpreted", staff, "globex", "", false, true},
		{"SuperAdmin reaches any org", super, "webby-ai", "webby-ai", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, org, admin, err := zapRPSScope(selecting(tc.selected), "Bearer "+tc.auth)
			if tc.wantErr {
				if !errors.Is(err, account.ErrOrgForbidden) {
					t.Fatalf("err = %v, want ErrOrgForbidden", err)
				}
				return
			}
			if err != nil || org != tc.wantOrg || admin != tc.wantAdmin {
				t.Fatalf("got (%q, admin=%v, %v), want (%q, admin=%v)", org, admin, err, tc.wantOrg, tc.wantAdmin)
			}
		})
	}
}

// The admin gate refuses on the selected org's role, and refuses a selection the
// claim does not admit, before any settings are read.
func TestRouterPolicyRequiresAdminOfSelectedOrg(t *testing.T) {
	t.Setenv("DISABLE_PREVIEW_MODE", "true")
	staff := mintScopeJWT(t, "hanzo", "z", false, "hanzo:admin", "webby-ai:admin", "acme:member")
	for _, sel := range []string{"", "acme", "globex"} {
		if _, _, deny := zapRPSRequireOrgAdmin(selecting(sel), "Bearer "+staff); deny == nil {
			t.Fatalf("selection %q admitted a caller who does not administer it", sel)
		}
	}
	if _, org, deny := zapRPSRequireOrgAdmin(selecting("webby-ai"), "Bearer "+staff); deny != nil || org != "webby-ai" {
		t.Fatalf("admin of webby-ai refused or misrouted: org=%q deny=%v", org, deny != nil)
	}
}
