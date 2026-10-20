// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/account"
	"github.com/luxfi/zap"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
)

// The org a request acts in is the signed membership set's answer, by the one rule
// cloud's boundary also asks (authz.Claims.EffectiveOrg): the selection when the set
// opens that org, else the first org it opens. A membership opens its org unless it
// is the admin org (only the SuperAdmin's), a brand's directory (nobody's tenant) or
// an org the platform runs held as a plain member (its customers share none of it).
func TestGetOrgIsTheSignedSetsAnswer(t *testing.T) {
	withStore(t)
	people := withIAM(t)
	ref := func(org, role string) account.OrgRef { return account.OrgRef{Org: org, Role: role} }
	cases := []struct {
		name      string
		user      iam.User
		orgs      []account.OrgRef
		requested string
		sudo      bool
		want      string
	}{
		{"a founder acts in their own org", iam.User{Owner: "hanzo", Name: "bob"}, []account.OrgRef{ref("bob", "owner")}, "", false, "bob"},
		{"a member selects an org of their set", iam.User{Owner: "hanzo", Name: "bob"}, []account.OrgRef{ref("bob", "owner"), ref("acme", "member")}, "acme", false, "acme"},
		{"an org outside the set is not granted", iam.User{Owner: "hanzo", Name: "bob"}, []account.OrgRef{ref("bob", "owner")}, "victim", false, "bob"},
		{"a plain member of a brand org acts in none of it", iam.User{Owner: "hanzo", Name: "carol"}, []account.OrgRef{ref("hanzo", "member")}, "", false, ""},
		{"a plain member of a brand org acts in the next org of their set", iam.User{Owner: "hanzo", Name: "carol"}, []account.OrgRef{ref("hanzo", "member"), ref("carol", "owner")}, "", false, "carol"},
		{"a plain member of a brand org cannot select it", iam.User{Owner: "hanzo", Name: "bob"}, []account.OrgRef{ref("bob", "owner"), ref("hanzo", "member")}, "hanzo", false, "bob"},
		{"a directory customer acts in their personal org", iam.User{Owner: "hanzo", Name: "dana"}, []account.OrgRef{ref("hanzo-id", "member"), ref("dana", "owner")}, "", false, "dana"},
		{"a brand org's admin acts in it", iam.User{Owner: "hanzo", Name: "erin", IsAdmin: true}, []account.OrgRef{ref("hanzo", "admin")}, "", false, "hanzo"},
		{"a membership of the admin org confers nothing", iam.User{Owner: "hanzo", Name: "bob"}, []account.OrgRef{ref("bob", "owner"), ref("admin", "admin")}, "admin", false, "bob"},
		{"the SuperAdmin selects any org", iam.User{Owner: "admin", Name: "z"}, []account.OrgRef{ref("admin", "owner")}, "acme", true, "acme"},
		{"an admin-org person without the host's verdict stays home", iam.User{Owner: "admin", Name: "z"}, []account.OrgRef{ref("admin", "owner")}, "acme", false, "admin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.sudo {
				host(t)
			}
			u := tc.user
			c := as(visit(http.MethodGet, "/v1/ai/connections"), people.signedInOrgs(t, &u, tc.orgs...))
			if tc.requested != "" {
				c.Fiber().Request().Header.Set("X-Org-Id", tc.requested)
			}
			if tc.sudo {
				c.Fiber().Request().Header.Set("X-User-IsAdmin", "true")
			}
			if got := c.GetOrg(); got != tc.want {
				t.Fatalf("GetOrg() = %q, want %q", got, tc.want)
			}
		})
	}
}

// A credential that names its org itself acts in it and selects nothing: a
// program's token is its application's org. A person's token that carries no
// membership set acts in no org, as at cloud's boundary — its `owner` claim is the
// application it signed in through, never the person's tenant.
func TestGetOrgOfACredentialWithoutAMembershipSet(t *testing.T) {
	withStore(t)
	people := withIAM(t)
	program := people.signedIn(t, &iam.User{Owner: "acme", Name: "provisioner", Type: "application"})
	c := as(visit(http.MethodGet, "/v1/ai/connections"), program)
	c.Fiber().Request().Header.Set("X-Org-Id", "victim")
	if got := c.GetOrg(); got != "acme" {
		t.Errorf("a program acts in %q, want its application's org acme", got)
	}
	legacy := people.signedInOrgs(t, &iam.User{Owner: "hanzo", Name: "bob"})
	if got := as(visit(http.MethodGet, "/v1/ai/connections"), legacy).GetOrg(); got != "" {
		t.Errorf("a person's token without a membership set acts in %q, want no org", got)
	}
}

// An org's AI connections are the org's keys to third-party accounts, shared by
// everyone who acts in it, so connecting, reconnecting or disconnecting one is its
// admins' to do (authz.Claims.OrgAdmin over the signed set). Reading whether one is
// connected stays every member's.
func TestConnectionWritesAreTheOrgAdmins(t *testing.T) {
	withStore(t)
	people := withIAM(t)
	member := people.signedInOrgs(t, &iam.User{Owner: "hanzo", Name: "bob"}, account.OrgRef{Org: "bob", Role: "owner"}, account.OrgRef{Org: "acme", Role: "member"})
	founder := people.signedInOrgs(t, &iam.User{Owner: "hanzo", Name: "bob"}, account.OrgRef{Org: "bob", Role: "owner"})
	brand := people.signedInOrgs(t, &iam.User{Owner: "hanzo", Name: "carol"}, account.OrgRef{Org: "hanzo", Role: "member"})

	writes := []struct {
		name string
		do   func(c *ApiController)
		path string
	}{
		{"connect a key", func(c *ApiController) { c.AddAIConnection() }, "/v1/ai/connections"},
		{"disconnect", func(c *ApiController) { c.DeleteAIConnection() }, "/v1/ai/connections/openai"},
		{"begin an OAuth connection", func(c *ApiController) { c.ConnectAIProvider() }, "/v1/ai/connections/openai/authorize"},
	}
	run := func(cred, org string, w struct {
		name string
		do   func(c *ApiController)
		path string
	}) *ApiController {
		c := as(visit(http.MethodPost, w.path), cred)
		c.Fiber().Request().SetBody([]byte(`{"provider":"openai","apiKey":"sk-not-a-real-key"}`))
		c.Fiber().Request().Header.SetContentType("application/json")
		if org != "" {
			c.Fiber().Request().Header.Set("X-Org-Id", org)
		}
		w.do(c)
		return c
	}
	for _, w := range writes {
		t.Run(w.name, func(t *testing.T) {
			if c := run(member, "acme", w); answered(c) != http.StatusForbidden {
				t.Errorf("a plain member of acme: %d %s, want 403", answered(c), sent(c))
			}
			if c := run(brand, "", w); answered(c) != http.StatusForbidden && answered(c) != http.StatusUnauthorized {
				t.Errorf("a plain member of the hanzo brand org reached hanzo's shared connection: %d %s", answered(c), sent(c))
			}
			if c := run(founder, "", w); answered(c) == http.StatusForbidden || answered(c) == http.StatusUnauthorized {
				t.Errorf("bob's own org admin was refused: %d %s", answered(c), sent(c))
			}
		})
	}

	c := as(visit(http.MethodGet, "/v1/ai/connections"), member)
	c.Fiber().Request().Header.Set("X-Org-Id", "acme")
	c.GetAIConnections()
	if answered(c) != http.StatusOK || strings.Contains(sent(c), `"error"`) {
		t.Errorf("a member could not read acme's connections: %d %s", answered(c), sent(c))
	}
}

// The ZAP door serves the same connections, so it asks the same two questions: the
// org is the signed set's (never the home a plain member of a brand org holds), and
// a write is that org's admins'.
func TestZapConnectionWritesAreTheOrgAdmins(t *testing.T) {
	withStore(t)
	people := withIAM(t)
	ctx := context.Background()
	status := func(m *zap.Message, err error) uint32 {
		t.Helper()
		if err != nil || m == nil {
			t.Fatalf("handler: %v", err)
		}
		return m.Root().Uint32(object.CloudRespStatus)
	}
	brand := people.signedInOrgs(t, &iam.User{Owner: "hanzo", Name: "carol"}, account.OrgRef{Org: "hanzo", Role: "member"})
	founder := people.signedInOrgs(t, &iam.User{Owner: "hanzo", Name: "bob"}, account.OrgRef{Org: "bob", Role: "owner"})

	if org, err := zapConnectionOrg(ctx, brand); err == nil {
		t.Errorf("a plain member of the hanzo brand org acts in %q on the ZAP door", org)
	}
	if org, err := zapConnectionOrg(ctx, founder); err != nil || org != "bob" {
		t.Errorf("bob's org on the ZAP door = %q, %v; want bob", org, err)
	}
	for _, tc := range []struct {
		name string
		call func(auth string) (*zap.Message, error)
	}{
		{"add", func(a string) (*zap.Message, error) {
			return zapConnectionsAddCloud(ctx, a, []byte(`{"provider":"openai","apiKey":"sk-not-a-real-key"}`))
		}},
		{"delete", func(a string) (*zap.Message, error) {
			return zapConnectionsDeleteCloud(ctx, a, []byte(`{"provider":"openai"}`))
		}},
	} {
		if got := status(tc.call(brand)); got != 401 && got != 403 {
			t.Errorf("%s by a plain member of the hanzo brand org: %d, want a refusal", tc.name, got)
		}
		if got := status(tc.call(founder)); got == 401 || got == 403 {
			t.Errorf("%s by bob's own admin was refused: %d", tc.name, got)
		}
	}
	member := people.signedInOrgs(t, &iam.User{Owner: "acme", Name: "dave"}, account.OrgRef{Org: "acme", Role: "member"})
	if got := status(zapConnectionsAddCloud(ctx, member, []byte(`{"provider":"openai","apiKey":"sk-not-a-real-key"}`))); got != 403 {
		t.Errorf("add by a plain member of acme: %d, want 403", got)
	}
}
