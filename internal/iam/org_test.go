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

package iam

import "testing"

func member(orgs ...string) []OrgRef {
	refs := make([]OrgRef, 0, len(orgs))
	for _, o := range orgs {
		refs = append(refs, OrgRef{Org: o, Role: "member"})
	}
	return refs
}

// TestEffectiveOrg pins the membership gate. The load-bearing property is the
// NO-OP one: every credential that does not ask for a switch, and every one whose
// signed claim does not back the ask, resolves to the home org — the exact value
// the whole path keyed on before the switch existed.
func TestEffectiveOrg(t *testing.T) {
	cases := []struct {
		name      string
		home      string
		orgs      []OrgRef
		requested string
		want      string
	}{
		// ── the no-op set: byte-identical to keying on the home org ──────────
		{"no header at all", "hanzo", member("hanzo", "acme"), "", "hanzo"},
		{"header names the home org", "hanzo", member("hanzo", "acme"), "hanzo", "hanzo"},
		{"nil claim, no ask", "hanzo", nil, "", "hanzo"},
		{"nil claim, asks for home", "hanzo", nil, "hanzo", "hanzo"},
		{"empty claim, no ask", "hanzo", []OrgRef{}, "", "hanzo"},

		// ── the switch ───────────────────────────────────────────────────────
		{"member switches to a team org", "hanzo", member("hanzo", "acme"), "acme", "acme"},
		{"member switches, home not first in set", "hanzo", member("acme", "zoo", "hanzo"), "zoo", "zoo"},

		// ── refusal, and it is SILENT: identical to "no switch requested" ────
		{"non-member is refused", "hanzo", member("hanzo"), "acme", "hanzo"},
		{"legacy token (no orgs claim) cannot switch", "hanzo", nil, "acme", "hanzo"},
		{"empty claim set admits nothing", "hanzo", []OrgRef{}, "acme", "hanzo"},

		// ── byte-exact: no fold, no trim. "acme" and "ACME" are DISTINCT orgs,
		//    and a padded value is not the slug the membership row names. ─────
		{"case differs — not a member", "hanzo", member("hanzo", "acme"), "ACME", "hanzo"},
		{"claim case differs — not a match", "hanzo", member("hanzo", "ACME"), "acme", "hanzo"},
		{"leading space — not a match", "hanzo", member("hanzo", "acme"), " acme", "hanzo"},
		{"trailing space — not a match", "hanzo", member("hanzo", "acme"), "acme ", "hanzo"},

		// ── an empty membership entry can never be the answer ────────────────
		{"empty org in claim does not admit an empty ask", "hanzo", []OrgRef{{Org: ""}}, "", "hanzo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveOrg(tc.home, tc.orgs, tc.requested); got != tc.want {
				t.Fatalf("EffectiveOrg(%q, %v, %q) = %q, want %q", tc.home, tc.orgs, tc.requested, got, tc.want)
			}
		})
	}
}

// TestLedgerOrg pins the two-branch spend policy: a masquerading super admin
// spends its OWN ledger, everyone else spends the org they are acting in.
func TestLedgerOrg(t *testing.T) {
	cases := []struct {
		name       string
		effective  string
		home       string
		masquerade bool
		want       string
	}{
		{"ordinary caller, no switch", "hanzo", "hanzo", false, "hanzo"},
		{"ordinary caller, switched — the selected org pays", "acme", "hanzo", false, "acme"},
		{"admin not switched", "admin", "admin", true, "admin"},
		{"admin inside a customer tenant spends its own wallet", "acme", "admin", true, "admin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := LedgerOrg(tc.effective, tc.home, tc.masquerade); got != tc.want {
				t.Fatalf("LedgerOrg(%q, %q, %v) = %q, want %q", tc.effective, tc.home, tc.masquerade, got, tc.want)
			}
		})
	}
}

// TestLedgerComposition pins the exact expression the three money sites evaluate
// — the JWT provider resolver, the controller's billingOrg, and the router's
// balance filter all compute LedgerOrg(EffectiveOrg(home, orgs, requested), home,
// admin). Testing the composition is what proves they agree: three copies of a
// money predicate that disagree gate one wallet and drain another.
func TestLedgerComposition(t *testing.T) {
	ledger := func(home string, orgs []OrgRef, requested string, admin bool) string {
		return LedgerOrg(EffectiveOrg(home, orgs, requested), home, admin)
	}
	cases := []struct {
		name      string
		home      string
		orgs      []OrgRef
		requested string
		admin     bool
		want      string
	}{
		{"no switch requested", "hanzo", member("hanzo", "acme"), "", false, "hanzo"},
		{"switch to home", "hanzo", member("hanzo", "acme"), "hanzo", false, "hanzo"},
		{"member switch — the selected org pays", "hanzo", member("hanzo", "acme"), "acme", false, "acme"},
		{"non-member switch — home pays, silently", "hanzo", member("hanzo"), "acme", false, "hanzo"},
		{"no claim (hk-/sk-/hz-/session) — home pays", "acme", nil, "zoo", false, "acme"},
		{"admin masquerade — admin pays", "admin", nil, "acme", true, "admin"},
		{"admin at home", "admin", nil, "", true, "admin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ledger(tc.home, tc.orgs, tc.requested, tc.admin); got != tc.want {
				t.Fatalf("ledger = %q, want %q", got, tc.want)
			}
		})
	}
}
