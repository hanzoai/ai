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
// A JWT carries `billing_account` and needs no shape rule. An API key carries no
// claim at all: IAM's get-user?accessKey= answers with the user ROW, and its Type
// is the only thing left that says machine-or-person. IAM writes "service-account"
// there (internal/oidc/provision.go, /v1/iam/service-accounts), so a payer that
// knows only "application" reads a service credential as a person — and in the
// signup org a person pays personally, which for a service account is a wallet no
// funding path can name.
//
// Measured on hanzo/guest, the anonymous chat free tier: /v1/audio/speech and
// /v1/audio/transcriptions both answered 402 insufficient_balance against a pool
// holding ~$149k, because the gate read hanzo/guest and the money was in hanzo.
func TestServiceAccountKeySpendsThePool(t *testing.T) {
	guest := &User{Owner: "hanzo", Name: "guest", Type: "service-account"}
	if got := guest.PayerSubject(""); got != "hanzo" {
		t.Fatalf("service-account key pays %q, want the org pool %q", got, "hanzo")
	}
	// The OIDC client shape still resolves the same way — one predicate, two
	// spellings, not two rules.
	app := &User{Owner: "hanzo", Name: "svc", Type: "application"}
	if got := app.PayerSubject(""); got != "hanzo" {
		t.Fatalf("application key pays %q, want the org pool %q", got, "hanzo")
	}
	// And a PERSON in the same org still pays personally: the pool is not opened
	// to strangers, which is the only reason the signup org is special.
	human := &User{Owner: "hanzo", Name: "alice"}
	if got := human.PayerSubject(""); got != "hanzo/alice" {
		t.Fatalf("person pays %q, want %q", got, "hanzo/alice")
	}
}
