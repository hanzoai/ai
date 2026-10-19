// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
)

// SuperAdmin is the host boundary's verdict on a request, never a reading this
// module makes of an org name. These are the callers that used to be SuperAdmin
// here because the org a credential named was spelled "admin", and the callers
// that must never become one.

// signedInOrgs is signedIn for a PERSON: IAM opens a person's membership set
// with the org their row lives in, so the token carries it, home first.
func (p *people) signedInOrgs(t *testing.T, user *iam.User, orgs ...account.OrgRef) string {
	t.Helper()
	claims := &iam.Claims{
		User:      *user,
		Issuer:    p.issuer,
		Audience:  jwt.ClaimStrings{iamTestAudience},
		Subject:   user.Name,
		IssuedAt:  jwt.NewNumericDate(time.Now().Add(-time.Minute)),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		Orgs:      orgs,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = p.kid
	signed, err := token.SignedString(p.key)
	if err != nil {
		t.Fatal(err)
	}
	return "Bearer " + signed
}

// platformRead asks the two questions SuperAdmin answers: a listing across every
// tenant (RequireSuperAdmin) and an org switch into a tenant the caller does not
// belong to (GetOrg). It reports whether each was granted.
func platformRead(t *testing.T, credential string, header map[string]string) (listed bool, actedIn string) {
	t.Helper()
	c := as(visit("GET", "/v1/ai/get-global-stores"), credential)
	for k, v := range header {
		c.Fiber().Request().Header.Set(k, v)
	}
	c.GetGlobalStores()
	listed = answered(c) == 200 && !strings.Contains(sent(c), `"error"`)

	c = as(visit("GET", "/v1/ai/get-chats"), credential)
	for k, v := range header {
		c.Fiber().Request().Header.Set(k, v)
	}
	c.Fiber().Request().Header.Set("X-Org-Id", "victim")
	return listed, c.GetOrg()
}

// An application of the admin org is a machine: its token names no person and
// carries no membership set, so the org it states is the application's own. It is
// the platform's job, never the platform's authority.
func TestAnAdminOrgMachineIsNotTheSuperAdmin(t *testing.T) {
	withStore(t)
	people := withIAM(t)
	machine := people.signedIn(t, &iam.User{Owner: "admin", Name: "provisioner", Type: "application"})
	listed, org := platformRead(t, machine, nil)
	if listed {
		t.Error("an admin-org application listed every tenant's stores")
	}
	if org == "victim" {
		t.Error("an admin-org application acted inside another tenant")
	}
}

// A key resolves to the row of the person who holds it, and a key is a machine:
// an admin-org person's sk- key is not their platform authority.
func TestAnAdminOrgKeyIsNotTheSuperAdmin(t *testing.T) {
	withStore(t)
	people := withIAM(t)
	key := people.asUser(t, &iam.User{Owner: "admin", Name: "z", Type: "normal-user"})
	_, org := platformRead(t, key, nil)
	if org == "victim" {
		t.Error("an admin-org person's API key acted inside another tenant")
	}
}

// Even the operator's own token is not the verdict. The host's boundary decides
// SuperAdmin once and states it; this module, deciding again from the org the
// token names, is a second predicate, and the one standalone ai would believe.
func TestAnAdminOrgPersonIsNotTheSuperAdminWithoutTheHostVerdict(t *testing.T) {
	withStore(t)
	people := withIAM(t)
	operator := people.signedInOrgs(t, &iam.User{Owner: "admin", Name: "z"}, account.OrgRef{Org: "admin", Role: "owner"})
	listed, org := platformRead(t, operator, nil)
	if listed || org == "victim" {
		t.Errorf("SuperAdmin was decided here from the token's org (listed=%v, acted in %q)", listed, org)
	}
}

// A request can carry any header it likes; one this module believed would be a
// SuperAdmin anyone can be.
func TestAForgedVerdictHeaderIsNotTheSuperAdmin(t *testing.T) {
	withStore(t)
	people := withIAM(t)
	member := people.signedInOrgs(t, &iam.User{Owner: "hanzo", Name: "dave", IsAdmin: true}, account.OrgRef{Org: "hanzo", Role: "owner"})
	listed, org := platformRead(t, member, map[string]string{"X-User-IsAdmin": "true"})
	if listed || org == "victim" {
		t.Errorf("a forged X-User-IsAdmin was believed (listed=%v, acted in %q)", listed, org)
	}
}

// The reserved org is compared nowhere here, folded or not: an org spelled like it
// is just an org.
func TestAnOrgSpelledLikeTheAdminOrgIsNotTheSuperAdmin(t *testing.T) {
	withStore(t)
	people := withIAM(t)
	for _, spelling := range []string{"Admin", "ADMIN", "aDmin", "admin "} {
		cred := people.signedInOrgs(t, &iam.User{Owner: spelling, Name: "z"}, account.OrgRef{Org: spelling, Role: "owner"})
		if listed, org := platformRead(t, cred, nil); listed || org == "victim" {
			t.Errorf("owner %q was the SuperAdmin (listed=%v, acted in %q)", spelling, listed, org)
		}
	}
}

// host installs the seam hanzo-inc/cloud installs (principal.Super): the verdict
// the boundary minted into X-User-IsAdmin after deleting any copy a client sent.
// The tests below send it the way the boundary would; a test without host() is a
// process with no host — standalone aid, a raw ZAP door.
func host(t *testing.T) {
	t.Helper()
	object.SetSuper(func(c *zip.Ctx) bool { return c.Header("X-User-IsAdmin") == "true" })
	t.Cleanup(func() { object.SetSuper(nil) })
}

// With a host, its verdict is the SuperAdmin, whatever org the row names: the
// boundary decided it on the token it validated, and this module reads it.
func TestTheHostVerdictIsTheSuperAdmin(t *testing.T) {
	withStore(t)
	people := withIAM(t)
	host(t)
	operator := people.signedInOrgs(t, &iam.User{Owner: "admin", Name: "z"}, account.OrgRef{Org: "admin", Role: "owner"})
	listed, org := platformRead(t, operator, map[string]string{"X-User-IsAdmin": "true"})
	if !listed || org != "victim" {
		t.Errorf("the host's verdict was not the SuperAdmin (listed=%v, acted in %q)", listed, org)
	}
	// And without it, the same token is an ordinary member of its org.
	listed, org = platformRead(t, operator, nil)
	if listed || org == "victim" {
		t.Errorf("an admin-org person the host did not vouch for was the SuperAdmin (listed=%v, acted in %q)", listed, org)
	}
}

// /get-account says what every gate here reads: the host's verdict on this request.
func TestTheAccountSaysTheHostVerdict(t *testing.T) {
	withStore(t)
	people := withIAM(t)
	host(t)
	operator := people.signedInOrgs(t, &iam.User{Owner: "admin", Name: "z"}, account.OrgRef{Org: "admin", Role: "owner"})
	for _, tc := range []struct {
		verdict string
		want    string
	}{{"true", `"isSuperAdmin":true`}, {"", `"isSuperAdmin":false`}} {
		c := as(visit("GET", "/v1/ai/get-account"), operator)
		if tc.verdict != "" {
			c.Fiber().Request().Header.Set("X-User-IsAdmin", tc.verdict)
		}
		c.GetAccount()
		if body := sent(c); !strings.Contains(body, tc.want) {
			t.Errorf("verdict %q: get-account answered %d %s, want %s", tc.verdict, answered(c), body, tc.want)
		}
	}
}

// A ZAP door is not the host boundary: a verdict a peer framed into the envelope
// is deleted before the router sees the request.
func TestAZapDoorCarriesNoVerdict(t *testing.T) {
	var seen string
	h := target(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = r.Header.Get("X-User-IsAdmin") }))
	r := httptest.NewRequest("GET", "/v1/get-global-stores", nil)
	r.Header.Set("X-User-IsAdmin", "true")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if seen != "" {
		t.Errorf("a ZAP-built request reached the router carrying X-User-IsAdmin=%q", seen)
	}
}
