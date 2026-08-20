// Copyright 2026 Hanzo AI, Inc. All rights reserved.

package iam

import "github.com/hanzoai/account"

// Payer is THE money address a credential spends from — the one place that turns
// an authenticated identity into the (org, subject) pair a balance read, a spend
// gate and a usage debit all have to agree on.
//
// WHY THIS EXISTS. account.Payer already owns the RULE (org pool vs personal
// wallet). What kept going wrong was not the rule but its INPUT: every caller
// built its own account.Credential by hand, and a caller that forgets one field
// gets a confidently wrong answer instead of a compile error. Twenty-eight sites
// built that struct; exactly one passed Account, so the signed billing_account
// claim was discarded almost everywhere. The result was the same bug this
// codebase has now hit five times — two layers deriving one address two ways —
// except distributed across the whole surface: a spend gate read the personal
// wallet while the funded org pool went untouched, and nothing errored, because
// each layer was faithfully answering the question it was asked.
//
// So the fix is not "remember to pass the claim" — it is to stop asking callers
// to assemble the credential at all. Hand this method a ledger and it answers
// with the identity's own money address, claim included, every time.
//
// ledger names the org whose ledger pays (the X-Org-Id namespace). Empty means
// "this user's home org", which is the behavior that predates org switching.
// A nil receiver yields the zero Account — unattributable, which every caller
// must treat as "refuse", never as "free".
func (u *User) Payer(ledger string) account.Account {
	if u == nil {
		return account.Account{}
	}
	if ledger == "" {
		ledger = u.Owner
	}
	// User.Type is NOT passed. Who pays is answered by IAM at the identity
	// boundary and carried in the signed billing_account claim; the row's class is
	// a profile fact and stating it here made it an authority — in the signup org,
	// an authority over the platform's own balance. account.Credential no longer
	// has a field for it, so this cannot regress by omission.
	return account.Payer(account.Credential{
		Owner:   ledger,
		Name:    u.Name,
		Account: u.BillingAccount,
	})
}

// PayerSubject is Payer(...).Subject() — the string form the balance, gate and
// usage paths pass around as ?user=. It exists so those call sites read as one
// step rather than two, and so a subject can never be built from a Credential
// assembled somewhere other than Payer above.
func (u *User) PayerSubject(ledger string) string { return u.Payer(ledger).Subject() }
