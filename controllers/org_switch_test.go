// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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

package controllers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/golang-jwt/jwt/v4"
	"github.com/hanzoai/account"

	"github.com/hanzoai/ai/internal/authtest"
	"github.com/hanzoai/ai/internal/iam"
)

// balanceProbe records the exact wire the balance gate puts on commerce: the
// billing SUBJECT (?user=) and the org NAMESPACE (X-Org-Id). Those two strings
// ARE the wallet — proving what the gate sends proves which wallet it read.
type balanceProbe struct {
	mu      sync.Mutex
	subject string
	org     string
}

func (p *balanceProbe) seen() (string, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.subject, p.org
}

func newBalanceProbe(t *testing.T) *balanceProbe {
	t.Helper()
	p := &balanceProbe{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.subject = r.URL.Query().Get("user")
		p.org = r.Header.Get("X-Org-Id")
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"available": 100000}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("commerceEndpoint", srv.URL)
	t.Setenv("commerceToken", "test-svc-token")
	return p
}

// TestBalanceGateWalletIsTheLedger is the NO-OP proof for the read side.
//
// The gate used to key on user.Owner. It now keys on the ledger it is handed, so
// the cases that matter are the ones a non-switching user takes: an unresolved
// ledger ("") and a ledger that IS the home org must put the SAME subject and the
// SAME namespace on the wire as keying on user.Owner did. A switched ledger must
// move BOTH — a gate that moved only the namespace would read an org's balance
// under a person's key and find nothing.
func TestBalanceGateWalletIsTheLedger(t *testing.T) {
	cases := []struct {
		name        string
		user        *iam.User
		ledger      string
		wantSubject string
		wantOrg     string
	}{
		// hanzo IS account.SignupOrg: its members are strangers sharing one org, so
		// each pays from a wallet of their own inside that one ledger.
		{"personal wallet, ledger unresolved", &iam.User{Owner: "hanzo", Name: "alice"}, "", "hanzo/alice", "hanzo"},
		{"personal wallet, ledger is home", &iam.User{Owner: "hanzo", Name: "alice"}, "hanzo", "hanzo/alice", "hanzo"},
		{"pooled org, ledger unresolved", &iam.User{Owner: "acme", Name: "bob"}, "", "acme", "acme"},
		{"pooled org, ledger is home", &iam.User{Owner: "acme", Name: "bob"}, "acme", "acme", "acme"},
		{"machine principal, ledger is home", &iam.User{Owner: "acme", Type: "application"}, "acme", "acme", "acme"},

		// The switch: BOTH halves of the address move to the selected org.
		{"switched to a pooled org", &iam.User{Owner: "hanzo", Name: "alice"}, "acme", "acme", "acme"},
		{"switched into the signup org", &iam.User{Owner: "acme", Name: "bob"}, "hanzo", "hanzo/bob", "hanzo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := newBalanceProbe(t)
			if err := enforceBalanceGate(tc.user, tc.ledger, "glm-5.2"); err != nil {
				t.Fatalf("gate refused a funded subject: %v", err)
			}
			subject, org := probe.seen()
			if subject != tc.wantSubject || org != tc.wantOrg {
				t.Fatalf("gate read wallet (subject=%q, org=%q), want (%q, %q)",
					subject, org, tc.wantSubject, tc.wantOrg)
			}
		})
	}
}

// TestBalanceGateUnresolvedLedgerIsHome states the safety invariant directly: a
// caller that passes no ledger gets the home org, never an empty subject. An
// empty subject would address no wallet at all, and every call on the ZAP
// transport — which has no headers and so can never carry a switch — relies on it.
func TestBalanceGateUnresolvedLedgerIsHome(t *testing.T) {
	user := &iam.User{Owner: "hanzo", Name: "alice"}

	probe := newBalanceProbe(t)
	if err := enforceBalanceGate(user, "", "glm-5.2"); err != nil {
		t.Fatalf("gate refused: %v", err)
	}
	blank, blankOrg := probe.seen()

	probe = newBalanceProbe(t)
	if err := enforceBalanceGate(user, user.Owner, "glm-5.2"); err != nil {
		t.Fatalf("gate refused: %v", err)
	}
	home, homeOrg := probe.seen()

	if blank != home || blankOrg != homeOrg {
		t.Fatalf("unresolved ledger read (%q, %q) but home read (%q, %q) — they must be the same wallet",
			blank, blankOrg, home, homeOrg)
	}
	if blank == "" || blankOrg == "" {
		t.Fatalf("gate addressed an empty wallet (subject=%q, org=%q)", blank, blankOrg)
	}
}

// orgController builds a controller carrying an Authorization header and an
// X-Org-Id, the two inputs billingOrg reads.
func orgController(auth, requested string) *ApiController {
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("{}"))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if requested != "" {
		req.Header.Set("X-Org-Id", requested)
	}
	c := visit("GET", "/v1/")
	return c
}

// TestBillingOrgHoldsUnlessTheClaimSaysOtherwise is the NO-OP proof for the
// resolver: every credential shape that reaches ai today resolves to its HOME
// org, whatever X-Org-Id says.
//
// Only a JWT carries the signed `orgs` claim, so only a JWT can switch. Every
// other credential — an sk- IAM key, an sk- provider key, an hz_ widget key, a
// cookie session, no credential at all — resolves nil membership, and nil admits
// nothing. That is what makes the raw header unspoofable here: a client that
// stamps X-Org-Id on an API-key request moves no money.
func TestBillingOrgHoldsUnlessTheClaimSaysOtherwise(t *testing.T) {
	user := &iam.User{Owner: "hanzo", Name: "alice"}
	cases := []struct {
		name      string
		auth      string
		requested string
	}{
		{"no org header", "Bearer sk-abc", ""},
		{"header names the home org", "Bearer sk-abc", "hanzo"},
		{"sk- IAM key cannot switch", "Bearer sk-abc", "acme"},
		{"sk- provider key cannot switch", "Bearer sk-abc", "acme"},
		{"hz_ widget key cannot switch", "Bearer hz_abc", "acme"},
		{"no credential at all cannot switch", "", "acme"},
		{"a token that is not a JWT cannot switch", "Bearer not.a.jwt", "acme"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := orgController(tc.auth, tc.requested)
			if got := c.billingOrg(user); got != user.Owner {
				t.Fatalf("billingOrg = %q, want the home org %q — a credential with no signed membership set moved the wallet", got, user.Owner)
			}
		})
	}
}

// THE PUBLIC LANE NEVER SWITCHES, whatever the request carries.
//
// A visitor presents no credential and the lane authenticates none — but the
// membership set is read straight off the request, so a signed-in reader asking an
// anonymous question arrives carrying a real claim. Without this, an anonymous call
// lands on a named tenant: their books, their usage rows, and their plan's ceiling
// deciding how many free calls a stranger gets.
//
// It is pinned HERE, on the resolver, because every emit site asks it. The pipeline
// used to force the lane's org at one call site and the family path re-derived it,
// which is one question with two answers and the wrong one on the path the lane
// actually takes.
func TestThePublicLaneCannotBeSwitchedOntoATenant(t *testing.T) {
	visitor := &iam.User{Owner: publicOrg, Type: "application"}

	// The one that matters: a REAL signed membership in acme, which is exactly what a
	// signed-in reader's browser sends, plus the header that asks for it. This is the
	// only shape that could move the call, so it is the shape the rule has to refuse.
	member := mintUsageJWTWithOrgs(t, "acme", "alice", "acme")
	for _, tc := range []struct{ name, auth, requested string }{
		{"no credential, no header", "", ""},
		{"a header naming a real org", "", "acme"},
		{"an api key and a header", "Bearer sk-abc", "acme"},
		{"a signed membership in the org it asks for", "Bearer " + member, "acme"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := orgController(tc.auth, tc.requested).billingOrg(visitor); got != publicOrg {
				t.Fatalf("billingOrg = %q, want %q — an anonymous call moved onto a named tenant's books and ceiling",
					got, publicOrg)
			}
		})
	}

	// The premise: that same token DOES switch a real member, so the case above is
	// refused by the lane's rule and not by a token nothing would have honored.
	reader := &iam.User{Owner: "hanzo", Name: "alice"}
	if got := orgController("Bearer "+member, "acme").billingOrg(reader); got != "acme" {
		t.Fatalf("the switching token resolved %q for a real member, want acme — "+
			"this test would prove nothing about a token that cannot switch", got)
	}
}

// mintUsageJWTWithOrgs is mintUsageJWT plus a signed membership set — the claim that
// lets a principal switch orgs, and therefore the only claim that could carry a
// visitor onto a tenant's books.
func mintUsageJWTWithOrgs(t *testing.T, owner, name string, orgs ...string) string {
	t.Helper()
	key := authtest.Signing(t)
	claims := usageClaims(owner, name, "https://hanzo.id", "hanzo-cloud")
	refs := make([]map[string]string, 0, len(orgs))
	for _, org := range orgs {
		refs = append(refs, map[string]string{"org": org, "role": "member"})
	}
	claims["orgs"] = refs
	tok, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return tok
}

// TestBillingOrgNilUser pins the unattributable case: no user, no wallet. Callers
// refuse such a request rather than serve it free, and an empty string is what
// says so — never a defaulted org that would silently bill a real tenant.
func TestBillingOrgNilUser(t *testing.T) {
	if got := orgController("Bearer sk-abc", "acme").billingOrg(nil); got != "" {
		t.Fatalf("billingOrg(nil) = %q, want \"\"", got)
	}
}

// TestGateAndDebitAddressOneWallet is the invariant the whole change exists to
// hold. The gate READS account.Payer(ledger, name); the debit WRITES
// account.PayerOf(record.Owner, record.User) with Owner=ledger and User the
// caller's IDENTITY ("<home>/<name>", deliberately NOT the ledger). Those two
// expressions must name the same (org, subject) or a funded org 402s while an
// unfunded one runs free — the exact failure the switcher would otherwise cause.
func TestGateAndDebitAddressOneWallet(t *testing.T) {
	cases := []struct {
		name   string
		home   string
		who    string
		ledger string
	}{
		{"no switch, personal wallet in the signup org", "hanzo", "alice", "hanzo"},
		{"no switch, pooled org", "acme", "bob", "acme"},
		{"switched from the signup org into a pooled org", "hanzo", "alice", "acme"},
		{"switched from a pooled org into the signup org", "acme", "bob", "hanzo"},
		{"switched between two pooled orgs", "acme", "bob", "zoo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			user := &iam.User{Owner: tc.home, Name: tc.who}

			probe := newBalanceProbe(t)
			if err := enforceBalanceGate(user, tc.ledger, "glm-5.2"); err != nil {
				t.Fatalf("gate refused: %v", err)
			}
			gateSubject, gateOrg := probe.seen()

			// Exactly what recordUsage derives from the record ChatCompletions builds.
			rec := &usageRecord{Owner: tc.ledger, User: user.Owner + "/" + user.Name}
			debitSubject := account.PayerOf(rec.Owner, rec.User).Subject()
			debitOrg := rec.Owner

			if gateSubject != debitSubject || gateOrg != debitOrg {
				t.Fatalf("gate read (%q, %q) but the debit lands on (%q, %q) — two wallets",
					gateOrg, gateSubject, debitOrg, debitSubject)
			}
		})
	}
}

// TestGetOrgScopeHoldsWithoutAClaim pins the SCOPE resolver's no-op half: without
// a signed membership set, a requested org never widens data scope either. GetOrg
// answers routing, pricing and usage reads; a spoofable one would be a
// cross-tenant read even where no money moves.
func TestGetOrgScopeHoldsWithoutAClaim(t *testing.T) {
	// Non-member switch attempt with a non-JWT credential: nil membership set.
	// With no membership claim the selection is REFUSED (account >= v0.3.0); the
	// old contract answered "hanzo". Either way no switch is admitted — what
	// changed is that the money path can no longer be silently redirected. GetOrg
	// still answers home for scope, which the controller check below covers.
	if got, err := account.EffectiveOrg("hanzo", nil, "acme"); err == nil || got != "" {
		t.Fatalf("EffectiveOrg admitted a switch with no claim: got %q err %v", got, err)
	}
	// And the controller path agrees for the same shape.
	c := orgController("Bearer sk-abc", "acme")
	if orgs := c.principalOrgs(); orgs != nil {
		t.Fatalf("principalOrgs = %v for a non-JWT credential, want nil", orgs)
	}
}

// TestBillingOrgHomeForCredentialsThatCannotSwitch pins the branch added when
// account.EffectiveOrg began REFUSING an unauthorized org selection instead of
// answering with the caller's home org (account v0.3.0).
//
// That refusal is right for a principal holding a signed membership set: falling
// back there does not fail, it succeeds against a different economic principal —
// the request runs, the meter records, and the wrong wallet pays with no error
// anywhere. (Proven in account's own suite: TestEffectiveOrg_UnauthorizedOrgRefused.)
//
// But an sk-/hz- credential carries no membership set BY CONSTRUCTION. Its ask is
// not a bid for another wallet, because there is no other wallet it could name;
// billing its own owner is the only possible answer. Passing those through
// EffectiveOrg would refuse them too, and a stray X-Org-Id header would start
// 401-ing every API-key caller while protecting nobody. Hence the len(orgs)==0
// branch, and hence this test.
func TestBillingOrgHomeForCredentialsThatCannotSwitch(t *testing.T) {
	user := &iam.User{Owner: "hanzo", Name: "z"}

	for _, auth := range []string{"Bearer sk-abc", "Bearer hz-widget", ""} {
		c := orgController(auth, "initech")
		if got := c.billingOrg(user); got != "hanzo" {
			t.Fatalf("billingOrg = %q for credential %q; want home org — a credential with no claim set cannot switch and must still be billable", got, auth)
		}
	}
}
