// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package object

import "testing"

// TestBillingSubject locks the ONE rule: the billing subject is ALWAYS the org
// (`owner`), lowercased, regardless of which user made the call. No per-user
// wallets, no personal-vs-pooled split, no allowlist.
func TestBillingSubject(t *testing.T) {
	cases := []struct {
		name  string
		owner string
		uname string
		want  string
	}{
		// Any member of an org bills to the ONE org account.
		{"member bills org", "hanzo", "h60379666@gmail.com", "hanzo"},
		{"another member same org", "hanzo", "z", "hanzo"},
		// Mixed case is lowercased so usage (verbatim SourceId) nets against the
		// lowercased balance/deposit reads.
		{"lowercased", "Hanzo", "Alice@GMAIL.com", "hanzo"},
		// A dedicated org is unchanged: one balance for the org.
		{"dedicated org", "maxpower", "davelorenzini", "maxpower"},
		// Empty name is irrelevant — still the org.
		{"empty name", "hanzo", "", "hanzo"},
		// Empty owner cannot be billed.
		{"empty owner", "", "alice", ""},
		// Whitespace is trimmed.
		{"trimmed", "  hanzo  ", "z", "hanzo"},
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
// It resolves to the org in every form.
func TestBillingSubjectFromUserKey(t *testing.T) {
	cases := []struct {
		name    string
		owner   string
		userKey string
		want    string
	}{
		{"full key", "hanzo", "hanzo/h60379666@gmail.com", "hanzo"},
		{"dedicated full key", "maxpower", "maxpower/davelorenzini", "maxpower"},
		{"owner derived from key", "", "hanzo/bob@example.com", "hanzo"},
		{"bare org key", "maxpower", "maxpower", "maxpower"},
		{"owner derived bare", "", "hanzo", "hanzo"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := BillingSubjectFromUserKey(c.owner, c.userKey); got != c.want {
				t.Fatalf("BillingSubjectFromUserKey(%q,%q) = %q, want %q", c.owner, c.userKey, got, c.want)
			}
		})
	}
}

// TestBillingSubjectForPrincipal locks that human and machine (M2M) principals in
// the same org bill to the SAME org account — the subject is org-scoped, so there
// is no unfunded per-app wallet to fall into.
func TestBillingSubjectForPrincipal(t *testing.T) {
	cases := []struct {
		name     string
		owner    string
		uname    string
		userType string
		want     string
	}{
		{"m2m service token", "hanzo", "hanzo-cloud", "application", "hanzo"},
		{"m2m mixed-case type", "hanzo", "hanzo-cloud", "Application", "hanzo"},
		{"m2m dedicated org", "maxpower", "some-app", "application", "maxpower"},
		{"human", "hanzo", "z", "normal-user", "hanzo"},
		{"human dedicated", "maxpower", "davelorenzini", "normal-user", "maxpower"},
		{"empty type", "hanzo", "alice", "", "hanzo"},
		{"empty owner", "", "x", "application", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := BillingSubjectForPrincipal(c.owner, c.uname, c.userType); got != c.want {
				t.Fatalf("BillingSubjectForPrincipal(%q,%q,%q) = %q, want %q", c.owner, c.uname, c.userType, got, c.want)
			}
		})
	}
}
