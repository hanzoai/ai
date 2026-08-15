// Copyright 2026 Hanzo AI, Inc. All rights reserved.

package iam

import "testing"

// The spend gate, the debit and the balance read all address a wallet through
// User.PayerSubject, so this is where "who pays" becomes a string. The one answer
// it must never give is the SIGNUP ORG'S OWN account for a credential that failed
// to name a person: that account holds the platform's balance, and its members are
// strangers to each other who each hold their own wallet.
//
// A nameless credential is not hypothetical. IAM used to mint one whenever a
// token's subject stopped resolving — a deleted user, or one re-keyed while a
// refresh token still pinned the old key — and each rotation copied the dead key
// forward, so it renewed itself. That is fixed at the mint (iam ≥ v1.34.54), and
// this is the second line: even handed such a credential, the money layer refuses
// it rather than reading "no name" as "not a person, so the org pays".
//
// Refusal here is safe because it is not "free". An unattributable subject is the
// empty string, and the controller gate (enforceBalanceGate) reads a zero balance
// for it and denies — a request that cannot be attributed is refused, never billed
// to somebody who did not make it.
func TestPayerSubject_UnattributableIsEmptyNeverThePool(t *testing.T) {
	for _, c := range []struct {
		what string
		u    User
	}{
		{"no name at all", User{Owner: "hanzo"}},
		{"a blank name", User{Owner: "hanzo", Name: "   "}},
		// A claim naming another org is not this credential's to spend, so it is
		// discarded and the shape rule decides — which is still nameless.
		{"a foreign billing claim", User{Owner: "hanzo", BillingAccount: "org:maxpower"}},
	} {
		if got := (&c.u).PayerSubject(""); got != "" {
			t.Errorf("%s: PayerSubject = %q, want \"\" (never the pool)", c.what, got)
		}
	}
	// The specific regression: the answer must not be the bare org slug.
	if got := (&User{Owner: "hanzo"}).PayerSubject(""); got == "hanzo" {
		t.Fatal(`a nameless credential resolved to "hanzo" — the platform's own balance`)
	}
}

// Everything that legitimately has no name keeps its answer. A machine IS the org
// acting, so it spends the org pool — and it reaches that answer before the
// nameless rule is ever consulted, by its type and by the billing_account claim
// IAM signs for it. Hanzo's own first-party services authenticate this way and all
// live in the signup org, so this is the case that must not move.
func TestPayerSubject_MachinesAndTenantsAreUnaffected(t *testing.T) {
	for _, c := range []struct {
		what string
		u    User
		want string
	}{
		// The live pool consumers: first-party machines in the signup org.
		{"a machine by type", User{Owner: "hanzo", Name: "hanzo-cloud", Type: "application"}, "hanzo"},
		{"a service account", User{Owner: "hanzo", Name: "insights", Type: "service-account"}, "hanzo"},
		{"a nameless machine", User{Owner: "hanzo", Type: "application"}, "hanzo"},
		{"a machine by signed claim", User{Owner: "hanzo", Name: "platform", BillingAccount: "org:hanzo"}, "hanzo"},
		// A funded customer tenant. Its org is not the signup org, so every member
		// pools — the name is never read there, and namelessness cannot be a defect.
		{"the funded tenant", User{Owner: "maxpower", Name: "davelorenzini"}, "maxpower"},
		{"that tenant's admin claim", User{Owner: "maxpower", Name: "davelorenzini", BillingAccount: "org:maxpower"}, "maxpower"},
		{"a nameless credential elsewhere", User{Owner: "maxpower"}, "maxpower"},
		// And an ordinary person who signed up: their OWN wallet, not the pool.
		{"a signup person", User{Owner: "hanzo", Name: "alice"}, "hanzo/alice"},
	} {
		if got := (&c.u).PayerSubject(""); got != c.want {
			t.Errorf("%s: PayerSubject = %q, want %q", c.what, got, c.want)
		}
	}
}
