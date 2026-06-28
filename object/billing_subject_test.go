// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package object

import "testing"

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
