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

import (
	"os"
	"strings"
)

// BalanceExemptSet matches the BALANCE_EXEMPT_USERS allowlist with the SAME
// granularity in every caller — the router balance gate and the controller
// backstop — so the two can never drift. Entry semantics:
//
//   - "owner/name" (contains '/')  → exempts ONLY that exact billing subject,
//     an individually-named service account (e.g. "admin/hanzo-cloud").
//   - "owner"      (no '/')        → exempts the WHOLE org.
//
// This is the fix for the prior gate behavior, which collapsed EVERY entry to its
// owner-part: "hanzo/z" wrongly exempted ALL of org "hanzo", so every catch-all
// signup fail-OPENed during a Commerce outage. The controller already matched
// per-user-exact OR per-org (openai_api.go); the gate now matches identically via
// this one shared definition.
type BalanceExemptSet struct {
	subjects map[string]struct{} // entries WITH a slash: exact "owner/name"
	orgs     map[string]struct{} // entries WITHOUT a slash: the whole org
}

// ParseBalanceExempt parses a comma-separated BALANCE_EXEMPT_USERS list into the
// exact-subject and whole-org sets. Entries are lowercased to match the casing of
// BillingSubject / IAM owner slugs.
func ParseBalanceExempt(raw string) *BalanceExemptSet {
	s := &BalanceExemptSet{subjects: map[string]struct{}{}, orgs: map[string]struct{}{}}
	for _, e := range strings.Split(raw, ",") {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if strings.IndexByte(e, '/') >= 0 {
			s.subjects[e] = struct{}{}
		} else {
			s.orgs[e] = struct{}{}
		}
	}
	return s
}

// MatchesKey reports whether the "owner/name" userKey (exact) or its org is
// exempt — mirroring the controller's `entry == userKey || entry == orgKey`.
func (s *BalanceExemptSet) MatchesKey(userKey, org string) bool {
	if s == nil {
		return false
	}
	if _, ok := s.subjects[strings.ToLower(strings.TrimSpace(userKey))]; ok {
		return true
	}
	_, ok := s.orgs[strings.ToLower(strings.TrimSpace(org))]
	return ok
}

// Matches reports whether the (owner, name) identity is exempt: the exact
// "owner/name" subject or the bare org.
func (s *BalanceExemptSet) Matches(owner, name string) bool {
	return s.MatchesKey(owner+"/"+name, owner)
}

// BalanceExempt returns the process exemption set parsed from BALANCE_EXEMPT_USERS.
// The env is tiny and fixed at deploy, so it is parsed per call; hot-path callers
// may keep the parsed set (the router gate does, at init).
func BalanceExempt() *BalanceExemptSet {
	return ParseBalanceExempt(os.Getenv("BALANCE_EXEMPT_USERS"))
}
