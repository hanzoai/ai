// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package object

import (
	"sync"
	"testing"
)

// useBillingEnv installs a fresh billing-org environment for one test, defeating
// the process-once memoization so the case observes exactly (personal, org). It
// resets the caches before AND after (t.Cleanup) so no test leaks its allowlist
// into another. sync.Once{} is a zero composite literal — vet-clean to assign.
func useBillingEnv(t *testing.T, personal, org string) {
	t.Helper()
	reset := func() {
		personalBillingOrgsOnce = sync.Once{}
		orgBillingOrgsOnce = sync.Once{}
		personalBillingOrgs = nil
		orgBillingOrgs = nil
	}
	reset()
	t.Cleanup(reset)
	t.Setenv("PERSONAL_BILLING_ORGS", personal)
	t.Setenv("ORG_BILLING_ORGS", org)
}

// TestBillingSubject locks the per-user vs per-org billing identity. The default
// personal-billing org set is {hanzo} (PERSONAL_BILLING_ORGS unset), so members
// of the shared "hanzo" catch-all bill per-user while any dedicated org pools.
func TestBillingSubject(t *testing.T) {
	cases := []struct {
		name  string
		owner string
		uname string
		want  string
	}{
		// hanzo catch-all → per-user (the new-signup fix).
		{"hanzo personal", "hanzo", "h60379666@gmail.com", "hanzo/h60379666@gmail.com"},
		// Mixed case is lowercased so usage (verbatim SourceId) nets against the
		// lowercased balance/deposit reads.
		{"lowercased", "Hanzo", "Alice@GMAIL.com", "hanzo/alice@gmail.com"},
		// Pooled org (e.g. a real team) is unchanged: one balance for the org.
		{"pooled org", "maxpower", "davelorenzini", "maxpower"},
		// Exempt house user still resolves to its per-user subject (exemption is
		// applied separately, by matching BALANCE_EXEMPT_USERS).
		{"hanzo z", "hanzo", "z", "hanzo/z"},
		// Defensive: empty name in a personal org degrades to the org slug.
		{"personal empty name", "hanzo", "", "hanzo"},
		// Defensive: empty owner cannot be billed.
		{"empty owner", "", "alice", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := BillingSubject(c.owner, c.uname); got != c.want {
				t.Fatalf("BillingSubject(%q,%q) = %q, want %q", c.owner, c.uname, got, c.want)
			}
		})
	}
}

// TestBillingSubjectFromUserKey checks the convenience form used by call sites
// that hold the combined "owner/name" key (usage records, ZAP params, searchAuth).
func TestBillingSubjectFromUserKey(t *testing.T) {
	cases := []struct {
		name    string
		owner   string
		userKey string
		want    string
	}{
		{"personal full key", "hanzo", "hanzo/h60379666@gmail.com", "hanzo/h60379666@gmail.com"},
		{"pooled full key", "maxpower", "maxpower/davelorenzini", "maxpower"},
		{"owner derived from key", "", "hanzo/bob@example.com", "hanzo/bob@example.com"},
		{"bare org key", "maxpower", "maxpower", "maxpower"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := BillingSubjectFromUserKey(c.owner, c.userKey); got != c.want {
				t.Fatalf("BillingSubjectFromUserKey(%q,%q) = %q, want %q", c.owner, c.userKey, got, c.want)
			}
		})
	}
}

// TestBillingSubjectForPrincipal locks the M2M carve-out: an application
// principal (client_credentials, User.Type=="application") bills its OWNER ORG
// in BOTH billing models, so the hanzo-cloud service token can never resolve to
// the unfunded per-app "hanzo/hanzo-cloud" subject and 402 service traffic. A
// human principal resolves exactly as BillingSubject.
func TestBillingSubjectForPrincipal(t *testing.T) {
	cases := []struct {
		name     string
		owner    string
		uname    string
		userType string
		want     string
	}{
		// M2M in the personal-billing "hanzo" org bills the ORG, not the app.
		{"m2m hanzo-cloud", "hanzo", "hanzo-cloud", "application", "hanzo"},
		// Case-insensitive on the type; still the org.
		{"m2m mixed-case type", "hanzo", "hanzo-cloud", "Application", "hanzo"},
		// M2M in a pooled org is the org too (BillingSubject already pools).
		{"m2m pooled org", "maxpower", "some-app", "application", "maxpower"},
		// Human in the personal org is unchanged: per-user.
		{"human personal", "hanzo", "z", "normal-user", "hanzo/z"},
		// Human in a pooled org is unchanged: per-org.
		{"human pooled", "maxpower", "davelorenzini", "normal-user", "maxpower"},
		// Empty type is treated as a human (fail-safe: never silently repoint a
		// real user's spend to the org just because a type claim is missing).
		{"empty type is human", "hanzo", "alice", "", "hanzo/alice"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := BillingSubjectForPrincipal(c.owner, c.uname, c.userType); got != c.want {
				t.Fatalf("BillingSubjectForPrincipal(%q,%q,%q) = %q, want %q", c.owner, c.uname, c.userType, got, c.want)
			}
		})
	}
}

// TestIsPersonalBillingOrg confirms the default carve-out and that a dedicated
// org pools (no regression for proven per-org billing).
func TestIsPersonalBillingOrg(t *testing.T) {
	if !IsPersonalBillingOrg("hanzo") {
		t.Fatal("hanzo must be a personal-billing org by default")
	}
	if IsPersonalBillingOrg("maxpower") {
		t.Fatal("maxpower must pool (not personal-billing) by default")
	}
}

// TestBillingSubject_OrgBillingOverride locks the ORG_BILLING_ORGS allowlist: an
// org named there shares ONE pooled wallet (subject = owner) even when it would
// otherwise bill per-user, and the switch is SCOPED — no other tenant moves, and
// an empty/unset allowlist is a zero-behavior change.
func TestBillingSubject_OrgBillingOverride(t *testing.T) {
	cases := []struct {
		name     string
		personal string // PERSONAL_BILLING_ORGS
		org      string // ORG_BILLING_ORGS
		owner    string
		uname    string
		want     string
	}{
		// hanzo promoted to org-billing → the shared pool (subject = org), even
		// though hanzo is a personal-billing org by default. The override wins.
		{"hanzo org-billed", "hanzo", "hanzo", "hanzo", "z", "hanzo"},
		// Same hanzo WITHOUT the override → unchanged, per-user (regression guard).
		{"hanzo personal when unset", "hanzo", "", "hanzo", "z", "hanzo/z"},
		// Empty allowlist = zero change: another hanzo member stays per-user.
		{"empty allowlist no change", "hanzo", "", "hanzo", "alice@gmail.com", "hanzo/alice@gmail.com"},
		// Scoped: promoting hanzo does NOT touch another personal-billing org.
		{"other personal org unaffected", "hanzo,acme", "hanzo", "acme", "bob", "acme/bob"},
		// A pooled org is unchanged whether or not it is on the allowlist.
		{"pooled org unaffected", "hanzo", "hanzo", "maxpower", "dave", "maxpower"},
		// The allowlist is case/space-insensitive, like the org slug.
		{"allowlist normalized", "hanzo", " Hanzo , lux ", "hanzo", "z", "hanzo"},
		// Override collapses an empty name to the org slug (no trailing slash).
		{"org-billed empty name", "hanzo", "hanzo", "hanzo", "", "hanzo"},
		// Override wins even for an org that is not personal-billing (still org).
		{"override on non-personal", "hanzo", "lux", "lux", "sam", "lux"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			useBillingEnv(t, c.personal, c.org)
			if got := BillingSubject(c.owner, c.uname); got != c.want {
				t.Fatalf("BillingSubject(%q,%q) [PERSONAL_BILLING_ORGS=%q ORG_BILLING_ORGS=%q] = %q, want %q",
					c.owner, c.uname, c.personal, c.org, got, c.want)
			}
		})
	}
}

// TestIsOrgBilling confirms the predicate and that the override does not disturb
// the personal predicate itself (the two allowlists are orthogonal; the override
// only wins where they intersect, inside BillingSubject).
func TestIsOrgBilling(t *testing.T) {
	useBillingEnv(t, "hanzo", "hanzo,lux")
	if !isOrgBilling("hanzo") {
		t.Fatal("hanzo must be org-billing when on ORG_BILLING_ORGS")
	}
	if !isOrgBilling("LUX") {
		t.Fatal("org-billing match must be case-insensitive")
	}
	if isOrgBilling("maxpower") {
		t.Fatal("maxpower (not on the allowlist) must not be org-billing")
	}
	if !IsPersonalBillingOrg("hanzo") {
		t.Fatal("hanzo is still a personal-billing org; the override only wins in BillingSubject")
	}
}

// TestBillingSubjectForPrincipal_OrgBillingOverride shows the override composes
// with the M2M carve-out: a human in a promoted org bills the org pool (was
// per-user), and an application principal still bills the org.
func TestBillingSubjectForPrincipal_OrgBillingOverride(t *testing.T) {
	useBillingEnv(t, "hanzo", "hanzo")
	if got := BillingSubjectForPrincipal("hanzo", "z", "normal-user"); got != "hanzo" {
		t.Fatalf("human in org-billed hanzo = %q, want hanzo", got)
	}
	if got := BillingSubjectForPrincipal("hanzo", "hanzo-cloud", "application"); got != "hanzo" {
		t.Fatalf("m2m in org-billed hanzo = %q, want hanzo", got)
	}
}
