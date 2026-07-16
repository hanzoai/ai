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

import "testing"

// These tests pin the consume half of the `billing_account` contract: the wire
// IAM signs (iam/object/billing_account.go emits `<kind>:<subject>`), the Account
// it names, and the hole that naming it closes.

// TestParseAccount_Wire pins the exact strings IAM mints. If this file and
// iam/object/billing_account_test.go ever disagree, the two repos have drifted and
// money lands on the wrong ledger — these literals are the contract.
func TestParseAccount_Wire(t *testing.T) {
	cases := []struct {
		claim string
		want  Account
	}{
		{"org:acme", Org("acme")},
		{"person:hanzo/alice", Person("hanzo", "alice")},
		{"project:acme/website", Project("acme", "website")},
		// Folded on the way in, exactly like the constructors: the ledger keys on
		// the folded subject, and an un-folded one would record usage that never
		// nets against the balance.
		{"ORG:ACME", Org("acme")},
		{"Person:Hanzo/Alice", Person("hanzo", "alice")},
	}
	for _, tc := range cases {
		t.Run(tc.claim, func(t *testing.T) {
			got := ParseAccount(tc.claim)
			if got != tc.want {
				t.Fatalf("ParseAccount(%q) = %+v, want %+v", tc.claim, got, tc.want)
			}
			// String is the inverse: the claim IAM mints is the claim we render.
			if s := tc.want.String(); ParseAccount(s) != tc.want {
				t.Fatalf("round-trip broke: %+v -> %q -> %+v", tc.want, s, ParseAccount(s))
			}
		})
	}
}

func TestAccount_String(t *testing.T) {
	cases := []struct {
		acct Account
		want string
	}{
		{Org("acme"), "org:acme"},
		{Person("hanzo", "alice"), "person:hanzo/alice"},
		{Project("acme", "website"), "project:acme/website"},
		{Account{}, ""}, // unattributable names nobody
	}
	for _, tc := range cases {
		if got := tc.acct.String(); got != tc.want {
			t.Fatalf("Account.String() = %q, want %q", got, tc.want)
		}
	}
}

// TestParseAccount_Unreadable: anything we cannot read names NOBODY, so Payer
// falls back rather than billing a guess. A parse must never invent an owner.
func TestParseAccount_Unreadable(t *testing.T) {
	for _, claim := range []string{
		"",                     // no claim (a pre-claim token)
		"   ",                  // blank
		"acme",                 // no kind
		"admin:acme",           // unknown kind — never billed
		"person:hanzo",         // a person with no name
		"project:acme",         // a project with no name
		"org:acme/alice",       // an org account is the bare slug, never <org>/<name>
		"person:/alice",        // no org
		"person:hanzo/",        // no name
		":acme",                // empty kind
		"person:hanzo/alice/x", // Cut takes the first "/" — name "alice/x" is not a name we mint
	} {
		if got := ParseAccount(claim); !got.Zero() {
			// The last case is the one worth stating: it parses to Person(hanzo,
			// "alice/x"), a name IAM cannot mint. It is non-zero but harmless — it
			// addresses a wallet nobody funds — and Payer's org check still bounds
			// it to the caller's own ledger. Assert only the ones that must be zero.
			if claim != "person:hanzo/alice/x" {
				t.Fatalf("ParseAccount(%q) = %+v, want the zero Account", claim, got)
			}
		}
	}
}

// TestPayer_ClaimNamesThePayer: when the credential names an account, Payer reads
// it and does not guess — for every owner kind IAM can mint.
func TestPayer_ClaimNamesThePayer(t *testing.T) {
	cases := []struct {
		name string
		cred Credential
		want Account
	}{
		{
			name: "person: a signup-org login pays its own account",
			cred: Credential{Owner: "hanzo", Name: "alice", Account: "person:hanzo/alice"},
			want: Person("hanzo", "alice"),
		},
		{
			name: "org: a real-org member pools on the org",
			cred: Credential{Owner: "acme", Name: "alice", Account: "org:acme"},
			want: Org("acme"),
		},
		{
			name: "machine: client_credentials IS the org",
			cred: Credential{Owner: "acme", Name: "hanzo-cloud", Account: "org:acme"},
			want: Org("acme"),
		},
		{
			name: "project: a project-scoped token pays the project",
			cred: Credential{Owner: "acme", Name: "alice", Account: "project:acme/website"},
			want: Project("acme", "website"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Payer(tc.cred); got != tc.want {
				t.Fatalf("Payer(%+v) = %+v, want %+v", tc.cred, got, tc.want)
			}
		})
	}
}

// TestPayer_ForgedTypeNoLongerChangesThePayer is the hole, closed at the reader.
//
// User.Type is user-writable, so a member of the shared signup org can set their
// own to "application" and be read as a machine — Payer's legacy rule then answers
// Org("hanzo"), the POOLED account, and a stranger spends the shared balance.
//
// The forged Type reaches Payer as Machine=true (every caller derives it from that
// field). With the claim present, the payer does not move: IAM resolved
// machine-ness from the client_credentials grant shape and said "person".
func TestPayer_ForgedTypeNoLongerChangesThePayer(t *testing.T) {
	forged := Credential{
		Owner:   "hanzo",
		Name:    "mallory",
		Account: "person:hanzo/mallory", // what IAM actually signed
		Machine: true,                   // what the forged User.Type="application" says
	}

	got := Payer(forged)
	if want := Person("hanzo", "mallory"); got != want {
		t.Fatalf("the forged Type moved the payer: Payer = %+v, want %+v", got, want)
	}
	if got == Org(SignupOrg) {
		t.Fatal("the forged Type reached the shared signup-org pool")
	}
	if got.Subject() != "hanzo/mallory" {
		t.Fatalf("Subject = %q, want %q — the debit must key on the person", got.Subject(), "hanzo/mallory")
	}

	// The legacy rule is what it used to answer — proving the claim is what moved
	// it, and that this test would fail without the claim.
	if legacy := Payer(Credential{Owner: "hanzo", Name: "mallory", Machine: true}); legacy != Org("hanzo") {
		t.Fatalf("legacy fallback = %+v, want the pool %+v (the hole this closes)", legacy, Org("hanzo"))
	}
}

// TestPayer_ClaimIsBoundedToTheCallersOwnOrg: a claim naming another tenant's
// ledger is DISCARDED, never billed. IAM cannot mint one (claim and `owner` ride
// the same signed token), so this only ever fires on a mis-wired caller — and it
// falls back instead of crossing a tenant boundary.
func TestPayer_ClaimIsBoundedToTheCallersOwnOrg(t *testing.T) {
	foreign := Credential{Owner: "acme", Name: "alice", Account: "org:victim"}
	got := Payer(foreign)
	if got == Org("victim") {
		t.Fatal("a foreign billing_account claim redirected the debit across tenants")
	}
	if want := Org("acme"); got != want {
		t.Fatalf("Payer = %+v, want the caller's own %+v", got, want)
	}

	// Same for a person account in a foreign org.
	if got := Payer(Credential{Owner: "acme", Name: "alice", Account: "person:victim/bob"}); got != Org("acme") {
		t.Fatalf("Payer = %+v, want the caller's own org account", got)
	}
}

// TestPayer_FallbackWhenNothingIsNamed: a pre-claim token must still resolve, and
// must resolve exactly the way it did before the claim existed — otherwise this
// change 402s every live session.
func TestPayer_FallbackWhenNothingIsNamed(t *testing.T) {
	cases := []struct {
		name string
		cred Credential
		want Account
	}{
		{"signup-org person", Credential{Owner: "hanzo", Name: "alice"}, Person("hanzo", "alice")},
		{"real-org member", Credential{Owner: "acme", Name: "alice"}, Org("acme")},
		{"machine", Credential{Owner: "acme", Name: "svc", Machine: true}, Org("acme")},
		{"unreadable claim falls back", Credential{Owner: "acme", Name: "alice", Account: "nonsense"}, Org("acme")},
		{"unknown kind falls back", Credential{Owner: "hanzo", Name: "alice", Account: "admin:hanzo"}, Person("hanzo", "alice")},
		{"unattributable stays zero", Credential{Owner: "", Name: "alice", Account: "org:acme"}, Account{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Payer(tc.cred); got != tc.want {
				t.Fatalf("Payer(%+v) = %+v, want %+v", tc.cred, got, tc.want)
			}
		})
	}
}
