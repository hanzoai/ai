// Copyright 2026 Hanzo AI, Inc. All rights reserved.

package iam

import (
	"encoding/json"
	"testing"
)

// TestClaimUnmarshalsIntoTheIdentity is the regression guard for the trap that
// made this bug invisible. `billing_account` must land on the embedded User, so
// every spend site (which holds a *User, never a *Claims) can see it.
//
// If someone re-declares BillingAccount on Claims, Go's field-promotion depth
// rule makes the OUTER field win: encoding/json fills Claims.BillingAccount,
// User.BillingAccount stays empty, `claims.BillingAccount` still reads correctly
// so the auth layer looks fine — and every Payer call silently bills the
// shape-rule fallback instead of the named account. Nothing errors. This test is
// the only thing that would notice.
func TestClaimUnmarshalsIntoTheIdentity(t *testing.T) {
	var c Claims
	if err := json.Unmarshal([]byte(`{"owner":"hanzo","name":"z","billing_account":"org:hanzo"}`), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := c.User.BillingAccount; got != "org:hanzo" {
		t.Fatalf("claim did not reach the identity: User.BillingAccount = %q, want %q "+
			"(is BillingAccount re-declared on Claims? the outer field shadows the embedded one)", got, "org:hanzo")
	}
	// Promotion still serves the auth layer's existing reads.
	if c.BillingAccount != "org:hanzo" {
		t.Fatalf("promoted read broke: claims.BillingAccount = %q", c.BillingAccount)
	}
}

// TestPayerHonoursTheClaim pins WHO PAYS across the shapes that actually occur.
// The admin case is the one that shipped wrong: a funded org pool sat unused
// while the admin's own empty wallet was charged.
func TestPayerHonoursTheClaim(t *testing.T) {
	for _, tc := range []struct {
		name    string
		user    *User
		ledger  string
		want    string
		wantOrg string
	}{{
		name: "org admin spends the pool named by the claim",
		user: &User{Owner: "hanzo", Name: "z", BillingAccount: "org:hanzo"},
		want: "hanzo",
	}, {
		name: "plain member has no claim and spends their own wallet",
		user: &User{Owner: "hanzo", Name: "zachkelling@gmail.com"},
		want: "hanzo/zachkelling@gmail.com",
	}, {
		name: "a claim naming ANOTHER tenant's ledger is refused, not billed",
		user: &User{Owner: "hanzo", Name: "z", BillingAccount: "org:acme"},
		want: "hanzo/z",
	}, {
		name:   "explicit ledger overrides the home org",
		user:   &User{Owner: "hanzo", Name: "z", BillingAccount: "org:acme"},
		ledger: "acme",
		want:   "acme",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.user.PayerSubject(tc.ledger); got != tc.want {
				t.Fatalf("PayerSubject(%q) = %q, want %q", tc.ledger, got, tc.want)
			}
		})
	}
}

// TestNilUserIsUnattributable — a caller must refuse, never bill free.
func TestNilUserIsUnattributable(t *testing.T) {
	var u *User
	if got := u.PayerSubject("hanzo"); got != "" {
		t.Fatalf("nil user resolved to %q, want the zero account", got)
	}
}

// TestServiceAccountKeySpendsThePool is the whole sk- thread in one assertion.
//
// Measured on hanzo/guest, the anonymous chat free tier: /v1/audio/speech and
// /v1/audio/transcriptions both answered 402 insufficient_balance against a pool
// holding ~$149k, because the gate read hanzo/guest and the money was in hanzo.
// IT IS FIXED BY THE CLAIM, NOT BY THE TYPE. IAM's key door answers with the
// keyUser projection (compat.resolveUserByAccessKey), and that projection carries
// billing_account and NOT type — the row's class never crosses the wire. So the
// payer is what IAM computed and signed, and the shape rule is never reached.
//
// Two fixes landed for this one defect: the key door learned to STATE the payer,
// and account.Payer learned to INFER one from User.Type. The second is the
// forgeable one and is gone; this pins that the first is what actually carries a
// first-party service key to its org pool.
func TestServiceAccountKeySpendsThePool(t *testing.T) {
	// Exactly what the key door returns for hanzo/guest: no type, a signed ledger.
	guest := &User{Owner: "hanzo", Name: "guest", BillingAccount: "org:hanzo"}
	if got := guest.PayerSubject(""); got != "hanzo" {
		t.Fatalf("service-account key pays %q, want the org pool %q", got, "hanzo")
	}
	// The OIDC client shape resolves the same way, for the same reason: its token
	// carries the claim too (oidc.machineBillingAccount).
	app := &User{Owner: "hanzo", Name: "svc", BillingAccount: "org:hanzo"}
	if got := app.PayerSubject(""); got != "hanzo" {
		t.Fatalf("application token pays %q, want the org pool %q", got, "hanzo")
	}
	// And a PERSON in the same org still pays personally: the pool is not opened
	// to strangers, which is the only reason the signup org is special.
	human := &User{Owner: "hanzo", Name: "alice"}
	if got := human.PayerSubject(""); got != "hanzo/alice" {
		t.Fatalf("person pays %q, want %q", got, "hanzo/alice")
	}
	// THE NEGATIVE that matters: a row that merely CLAIMS to be a machine, with
	// nothing signed, does not reach the platform's balance. This is the shape a
	// planted or re-classed row would have, and no door produces it.
	claimsToBeAMachine := &User{Owner: "hanzo", Name: "planted", Type: "service-account"}
	if got := claimsToBeAMachine.PayerSubject(""); got == "hanzo" {
		t.Fatal("an asserted class reached the platform pool")
	}
}
