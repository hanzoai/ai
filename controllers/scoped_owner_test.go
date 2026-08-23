// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import "testing"

// Which org an HTTP read acts on, when nobody is signed in.
//
// This gate is the first thing every list handler in this package asks, so a
// wrong answer is not one endpoint reading another tenant's rows — it is all of
// them. It had no test at all.
//
// What is asserted here is the half that lives here: no principal, no scope, and
// the caller is told rather than quietly answered for somebody. The other half —
// which org a signed-in caller is scoped to — is the rule in util.ScopeOwner and
// is asserted there, because identity on this path comes from a signature-checked
// JWT rather than from anything a test can set. A test that minted its own token
// to reach the same rule would be asserting the rule twice and the signature
// never.
func TestAnHttpReadWithNoPrincipalIsRefusedRatherThanScoped(t *testing.T) {
	for _, target := range []string{
		"/v1/ai/get-forms",
		"/v1/ai/get-forms?owner=victim", // naming an org does not substitute for being one
	} {
		t.Run(target, func(t *testing.T) {
			c := visit("GET", target)
			owner, allowed := c.GetScopedOwner()
			if allowed {
				t.Errorf("an anonymous caller was scoped to %q", owner)
			}
			if owner != "" {
				t.Errorf("owner = %q on a refusal, want empty — an empty org is every org", owner)
			}
			if code := c.Fiber().Response().StatusCode(); code != 401 {
				t.Errorf("status = %d, want 401", code)
			}
		})
	}
}
