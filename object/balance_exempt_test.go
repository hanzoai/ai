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

// TestBalanceExemptGranularity proves the SHARED exemption matcher used by both
// the router gate and the controller backstop: a slash entry exempts ONLY the
// exact "owner/name" subject (not the whole org); a bare entry exempts the org.
// This is the fix for the prior gate behavior where "hanzo/z" exempted ALL of
// org "hanzo".
func TestBalanceExemptGranularity(t *testing.T) {
	s := ParseBalanceExempt(" admin/hanzo-cloud , hanzo/z ,, house ")

	// Exact service accounts are exempt.
	if !s.Matches("admin", "hanzo-cloud") {
		t.Error("admin/hanzo-cloud (exact) must be exempt")
	}
	if !s.Matches("hanzo", "z") {
		t.Error("hanzo/z (exact) must be exempt")
	}

	// A DIFFERENT user in the same org is NOT exempt (per-user-exact, the fix).
	if s.Matches("hanzo", "alice") {
		t.Error("hanzo/alice must NOT be exempt — only the exact hanzo/z is")
	}
	if s.Matches("admin", "someone-else") {
		t.Error("admin/someone-else must NOT be exempt — only the exact admin/hanzo-cloud is")
	}

	// A bare-org entry exempts every member of that org.
	if !s.Matches("house", "anyone") {
		t.Error("bare-org entry 'house' must exempt every member")
	}
	if !s.MatchesKey("house/whatever", "house") {
		t.Error("bare-org entry 'house' must exempt via MatchesKey too")
	}

	// A wholly different org is not exempt.
	if s.Matches("acme", "bob") {
		t.Error("acme/bob must NOT be exempt")
	}
}

// TestBalanceExemptMatchesController locks the gate's matching to the controller's
// `entry == userKey || entry == orgKey` semantics via MatchesKey.
func TestBalanceExemptMatchesController(t *testing.T) {
	s := ParseBalanceExempt("admin/hanzo-cloud,hanzo/z")
	// userKey-exact match.
	if !s.MatchesKey("admin/hanzo-cloud", "admin") {
		t.Error("MatchesKey must match the exact userKey")
	}
	// org-only (bare) does NOT match a slash entry.
	if s.MatchesKey("admin/other", "admin") {
		t.Error("a slash entry must NOT match a different userKey in the same org")
	}
}

// TestBalanceExemptNilAndEmpty guard the zero values.
func TestBalanceExemptNilAndEmpty(t *testing.T) {
	var nilSet *BalanceExemptSet
	if nilSet.Matches("a", "b") || nilSet.MatchesKey("a/b", "a") {
		t.Error("nil set must never match (nil-safe)")
	}
	empty := ParseBalanceExempt("")
	if empty.Matches("a", "b") {
		t.Error("empty set must never match")
	}
}

// TestBalanceExemptCaseInsensitive: entries and lookups are case-folded.
func TestBalanceExemptCaseInsensitive(t *testing.T) {
	s := ParseBalanceExempt("Admin/Hanzo-Cloud,HOUSE")
	if !s.Matches("admin", "hanzo-cloud") {
		t.Error("exact match must be case-insensitive")
	}
	if !s.Matches("house", "x") {
		t.Error("org match must be case-insensitive")
	}
}
