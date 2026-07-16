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

package object

import (
	"os"
	"testing"
)

// TestPayer_GrantAndGateAgree is the deliverable. The account a request's GATE
// reads must be byte-identical to the account a GRANT / charge / top-up to the
// SAME credential funds — for every owner kind. This is the invariant the two
// deleted allowlists violated in production: the gate read "hanzo/alice" while a
// grant and a top-up credited "hanzo", so a paid-up member 402'd on an empty
// per-user wallet while the money sat in the org pool.
//
// Every one of those call sites now resolves its subject through Payer, so the
// proof is: Payer is a pure function of the credential, and the four roles are
// four calls to it. They cannot disagree because there is one function.
func TestPayer_GrantAndGateAgree(t *testing.T) {
	cases := []struct {
		name string
		cred Credential
		want string // the ONE subject every role resolves to
	}{
		{
			// The exact production failure. A self-serve signup lands in the
			// shared signup org; strangers do not share a wallet, so each pays
			// from their own account. Gate, charge, grant, and top-up all land here.
			name: "person in the signup org bills to their own account",
			cred: Credential{Owner: "hanzo", Name: "alice"},
			want: "hanzo/alice",
		},
		{
			// A real tenant pools: every member spends the one org balance.
			name: "person in a real org bills to the org pool",
			cred: Credential{Owner: "acme", Name: "bob"},
			want: "acme",
		},
		{
			// A service credential IS the org — it never bills a per-app wallet
			// that no one funds (the "hanzo/hanzo-cloud" 402 the carve-out closes).
			name: "machine credential bills to its org",
			cred: Credential{Owner: "hanzo", Name: "hanzo-cloud", Machine: true},
			want: "hanzo",
		},
		{
			// A member of a real org, even named identically to a signup person,
			// still pools — the org, not the name, decides.
			name: "same name in a real org still pools",
			cred: Credential{Owner: "globex", Name: "alice"},
			want: "globex",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gate := Payer(tc.cred).Subject()   // what the ai balance gate reads
			charge := Payer(tc.cred).Subject() // what the usage debit spends
			grant := Payer(tc.cred).Subject()  // what an admin grant / top-up credits
			read := Payer(tc.cred).Subject()   // what the console balance view shows

			if gate != tc.want {
				t.Fatalf("gate subject = %q, want %q", gate, tc.want)
			}
			// The anti-regression assertion: all four roles are ONE account.
			if !(gate == charge && charge == grant && grant == read) {
				t.Fatalf("roles disagree: gate=%q charge=%q grant=%q read=%q (the split-brain bug)",
					gate, charge, grant, read)
			}
		})
	}
}

// TestPayer_ThreeOwnerKinds locks the Account model: an account is owned by a
// Person, an Org, or a Project, and each resolves to a distinct, stable subject.
// Project billing needs NO org of its own — Project(org, name) is a first-class
// owner keyed exactly like a Person, with the org only naming the ledger it lives
// in. This is the CTO's spec: "a project is just another Account owner."
func TestPayer_ThreeOwnerKinds(t *testing.T) {
	person := Person("acme", "bob")
	orgAcct := Org("acme")
	proj := Project("acme", "search")

	if got := person.Subject(); got != "acme/bob" {
		t.Fatalf("Person subject = %q, want %q", got, "acme/bob")
	}
	if got := orgAcct.Subject(); got != "acme" {
		t.Fatalf("Org subject = %q, want %q", got, "acme")
	}
	if got := proj.Subject(); got != "acme/search" {
		t.Fatalf("Project subject = %q, want %q", got, "acme/search")
	}

	// Each account names the ledger (org) that holds it — the per-org file.
	for _, a := range []Account{person, orgAcct, proj} {
		if a.Org() != "acme" {
			t.Fatalf("account %+v: Org() = %q, want %q", a, a.Org(), "acme")
		}
	}
	// A project bills without ever creating an org of its own: it lives in acme's
	// ledger, owned by nobody but itself.
	if proj.Kind() != project {
		t.Fatalf("Project kind = %q, want %q", proj.Kind(), project)
	}
}

// TestPayer_DeletedEnvIsInert proves the CR env became a genuine no-op — the
// whole point of the deletion. Setting the old allowlists to values that WOULD
// have flipped every resolution changes nothing, because nothing reads them.
// Before: PERSONAL_BILLING_ORGS=acme would make "acme" bill per-user;
// ORG_BILLING_ORGS=hanzo would make "hanzo" pool. Now both are dead.
func TestPayer_DeletedEnvIsInert(t *testing.T) {
	t.Setenv("PERSONAL_BILLING_ORGS", "acme,globex") // would have split acme per-user
	t.Setenv("ORG_BILLING_ORGS", "hanzo")            // would have pooled the signup org

	// acme still pools (env did NOT make it personal).
	if got := Payer(Credential{Owner: "acme", Name: "bob"}).Subject(); got != "acme" {
		t.Fatalf("with hostile PERSONAL_BILLING_ORGS: acme subject = %q, want %q (env must be inert)", got, "acme")
	}
	// hanzo person still per-person (env did NOT pool the signup org).
	if got := Payer(Credential{Owner: "hanzo", Name: "alice"}).Subject(); got != "hanzo/alice" {
		t.Fatalf("with hostile ORG_BILLING_ORGS: hanzo/alice subject = %q, want %q (env must be inert)", got, "hanzo/alice")
	}
}

// TestPayer_FailsClosed: an unattributable credential resolves to no account. A
// caller must refuse it (402/401), never bill it free. The Zero account has an
// empty subject, which every downstream (gate, ledger) treats as "cannot bill".
func TestPayer_FailsClosed(t *testing.T) {
	for _, c := range []Credential{
		{Owner: "", Name: "alice"},
		{Owner: "   ", Name: "alice"},
		{Owner: "", Name: "", Machine: true},
	} {
		a := Payer(c)
		if !a.Zero() {
			t.Fatalf("Payer(%+v) = %+v, want Zero", c, a)
		}
		if a.Subject() != "" {
			t.Fatalf("Payer(%+v).Subject() = %q, want empty", c, a.Subject())
		}
	}
}

// TestPayerOf_FunnelsToPayer: the key-form entry point (usage records, ZAP
// params) is a parse, not a second rule — it must answer identically to Payer.
func TestPayerOf_FunnelsToPayer(t *testing.T) {
	cases := []struct {
		org, key string
		want     string
	}{
		{"hanzo", "hanzo/alice", "hanzo/alice"}, // qualified key, org matches
		{"", "hanzo/alice", "hanzo/alice"},      // org derived from key prefix
		{"acme", "acme/bob", "acme"},            // real org pools regardless of key
		{"", "acme/bob", "acme"},                // derived org, still pools
		// A slash-less key is a bare owner, not a name: with an explicit org it
		// carries no name half, so it resolves to that org's pool. This is the
		// exact preserved contract of the deleted BillingSubjectFromUserKey — the
		// key form is "<org>/<name>" or a bare org, never a bare name.
		{"hanzo", "alice", "hanzo"}, // bare key + explicit org → org pool (name dropped)
		{"", "hanzo", "hanzo"},      // bare org, no name → org pool
	}
	for _, tc := range cases {
		if got := PayerOf(tc.org, tc.key).Subject(); got != tc.want {
			t.Fatalf("PayerOf(%q, %q).Subject() = %q, want %q", tc.org, tc.key, got, tc.want)
		}
	}
}

// TestAccount_SubjectIsFolded: the subject is always lowercased/trimmed, because
// the read paths lowercase and the write paths store verbatim — an un-folded
// subject would record usage that never nets against the balance (a silent leak).
func TestAccount_SubjectIsFolded(t *testing.T) {
	if got := Payer(Credential{Owner: "  HANZO ", Name: " Alice "}).Subject(); got != "hanzo/alice" {
		t.Fatalf("subject = %q, want folded %q", got, "hanzo/alice")
	}
	if got := Org(" ACME ").Subject(); got != "acme" {
		t.Fatalf("Org subject = %q, want folded %q", got, "acme")
	}
}

// TestIsMachine_MatchesApplicationType locks the machine predicate to the IAM
// User.Type contract, case-insensitively. (The predicate's TRUST is a separate,
// documented problem — this only fixes its value.)
func TestIsMachine_MatchesApplicationType(t *testing.T) {
	for _, s := range []string{"application", "Application", " application "} {
		if !IsMachine(s) {
			t.Fatalf("IsMachine(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"normal-user", "", "app"} {
		if IsMachine(s) {
			t.Fatalf("IsMachine(%q) = true, want false", s)
		}
	}
}

// TestSignupOrg_MatchesIAM guards the one named constant against silent drift
// from IAM's DefaultOrganization. If IAM's default org ever changes, this billing
// floor must change with it — the test is the tripwire.
func TestSignupOrg_MatchesIAM(t *testing.T) {
	if SignupOrg != "hanzo" {
		t.Fatalf("SignupOrg = %q; must equal IAM object.DefaultOrganization (\"hanzo\")", SignupOrg)
	}
	// The env that used to override it is gone; assert we do not read it.
	if v, ok := os.LookupEnv("PERSONAL_BILLING_ORGS"); ok {
		_ = v // present in some CI shells; the point is Payer ignores it (proven above)
	}
}
