package controllers

// run_test.go pins the properties that make a run key safe to hand to a sandbox.
// Every one of them is about what the token CANNOT do, because the token's whole
// justification is that it is weaker than every other bearer this door accepts.

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// installRun installs a resolver for one test and takes it away afterwards, so no
// test can leave a live run key behind for the next one.
func installRun(t *testing.T, f func(string) (Run, bool)) {
	t.Helper()
	SetRunResolver(f)
	t.Cleanup(func() { SetRunResolver(nil) })
}

// A run key is spelled so that no OTHER path can mistake it for its own kind of
// credential. This is the property that keeps a run key out of IAM: if it looked
// like an sk- key it would be sent there, and asking IAM to resolve it is the
// widening the whole design exists to prevent.
func TestRunKeyIsNotMistakenForAnyOtherBearer(t *testing.T) {
	const runKey = runPrefix + "abc"

	if !isRunKey(runKey) {
		t.Fatalf("%q is not recognised as a run key", runKey)
	}
	if isWidgetKey(runKey) {
		t.Fatal("a run key reads as a widget key — it would take the hz_ branch and inherit its caps")
	}
	if isJwtToken(runKey) {
		t.Fatal("a run key reads as a JWT — it would be sent to signature validation")
	}
	if isPublishableKey(runKey) {
		t.Fatal("a run key reads as a publishable key")
	}
	// And nothing else reads as a run key, so no existing credential is silently
	// re-routed onto the new branch.
	for _, other := range []string{"sk-live-123", "pk-live-123", "hz_widget_public", "a.b.c", ""} {
		if isRunKey(other) {
			t.Fatalf("%q reads as a run key", other)
		}
	}
}

// WITH NO RESOLVER INSTALLED THERE ARE NO RUN KEYS. A deployment with no run
// orchestrator must not admit a run key, and the failure direction matters: an
// uninstalled seam denies rather than admits.
func TestNoResolverMeansNoRunKeys(t *testing.T) {
	SetRunResolver(nil)
	if _, ok := resolveRun(runPrefix + "anything"); ok {
		t.Fatal("a run key resolved with no resolver installed — the seam fails OPEN")
	}
}

// A resolver that answers no org has not identified a payer, and an inference call
// with no payer is a free call on the shared upstream. It is refused.
func TestARunWithNoOrgIsNotARun(t *testing.T) {
	installRun(t, func(string) (Run, bool) { return Run{Org: "   ", ID: "r1"}, true })
	if _, ok := resolveRun(runPrefix + "x"); ok {
		t.Fatal("a run naming no org resolved — nobody would be billed for its tokens")
	}
}

// A run key resolves to the org the RESOLVER named, and to that org only. This is
// the cross-tenant property: the org is a property of the token, so there is no
// argument anywhere on this path that could name a different one.
func TestARunKeyResolvesOnlyToItsOwnOrg(t *testing.T) {
	installRun(t, func(tok string) (Run, bool) {
		switch tok {
		case runPrefix + "a":
			return Run{Org: "acme", ID: "run-a"}, true
		case runPrefix + "b":
			return Run{Org: "globex", ID: "run-b"}, true
		}
		return Run{}, false
	})

	for tok, want := range map[string]string{runPrefix + "a": "acme", runPrefix + "b": "globex"} {
		got, ok := resolveRun(tok)
		if !ok {
			t.Fatalf("%q did not resolve", tok)
		}
		if got.Org != want {
			t.Fatalf("%q billed %q, want %q — a run key reached another tenant's ledger", tok, got.Org, want)
		}
	}

	// A token the resolver does not know is not a run, however well-formed.
	if _, ok := resolveRun(runPrefix + "forged"); ok {
		t.Fatal("an unknown run key resolved")
	}
}

// A revoked run key stops resolving the moment its owner says so. The run's key
// dies with the run rather than with its own clock, which is what makes "the
// capability is gone when the work is done" a fact instead of an intention.
func TestARevokedRunKeyStopsResolving(t *testing.T) {
	live := true
	installRun(t, func(string) (Run, bool) {
		if !live {
			return Run{}, false
		}
		return Run{Org: "acme", ID: "r1"}, true
	})

	if _, ok := resolveRun(runPrefix + "x"); !ok {
		t.Fatal("a live run key did not resolve")
	}
	live = false
	if _, ok := resolveRun(runPrefix + "x"); ok {
		t.Fatal("a withdrawn run key still resolves — a finished run can still spend")
	}
}

// ── the tenant, which is what a 402 is actually about ────────────────────────

// A RUN KEY NAMES A BILLABLE TENANT. This is the property that separates working
// from 402: authResolveProvider settles the payer, but GetOrg asks principalUser
// who the tenant is, and a tenant it cannot name falls back to the empty IAM_ORG
// default — which reaches zen as no tenant at all and answers "a billable tenant
// is required". A run key that authenticates and then cannot be billed is the
// same split the sk- branch was added to close.
func TestARunKeyNamesTheTenantThatPays(t *testing.T) {
	installRun(t, func(string) (Run, bool) { return Run{Org: "acme", ID: "r1"}, true })

	c := runController(t, runPrefix+"live", "")
	u := c.principalUser()
	if u == nil {
		t.Fatal("a run key resolved to no tenant — every call it makes will 402 " +
			`"a billable tenant is required"`)
	}
	if u.Owner != "acme" {
		t.Fatalf("run key billed %q, want acme", u.Owner)
	}
	if u.Type != "application" {
		t.Fatalf("run principal is type %q, want application — a run is a machine, "+
			"and only a machine bills the ORG account", u.Type)
	}
	if got := c.GetOrg(); got != "acme" {
		t.Fatalf("GetOrg %q, want acme", got)
	}
}

// A HEADER CANNOT MOVE A RUN'S SPEND TO ANOTHER TENANT. The org is a property of
// the token; X-Org-Id is caller text. A run key presented with somebody else's org
// in the header still bills its own, which is the cross-tenant property the whole
// capability rests on.
func TestARunKeyCannotBeAimedAtAnotherTenantByHeader(t *testing.T) {
	installRun(t, func(string) (Run, bool) { return Run{Org: "acme", ID: "r1"}, true })

	c := runController(t, runPrefix+"live", "globex")
	if got := c.GetOrg(); got != "acme" {
		t.Fatalf("a run key for acme, presented with X-Org-Id: globex, resolved org %q — "+
			"one org's run reached another's ledger", got)
	}
}

// An unresolvable run key names no tenant at all, so it is refused rather than
// billed to a default.
func TestAnUnknownRunKeyNamesNoTenant(t *testing.T) {
	installRun(t, func(string) (Run, bool) { return Run{}, false })
	if u := runController(t, runPrefix+"forged", "").principalUser(); u != nil {
		t.Fatalf("an unknown run key resolved to tenant %q", u.Owner)
	}
}

// runController builds a controller holding one Bearer token and an optional
// client X-Org-Id, which is all these questions need.
func runController(t *testing.T, token, orgHeader string) *ApiController {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if orgHeader != "" {
		req.Header.Set("X-Org-Id", orgHeader)
	}
	c := visit("GET", "/v1/")
	return c
}
