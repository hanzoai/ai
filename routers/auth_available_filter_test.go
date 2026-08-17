// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package routers

import (
	"net/http"
	"testing"

	"github.com/hanzoai/ai/object"
)

// filterProbe runs the filter over one request and reports what it wrote.
func filterProbe(t *testing.T) probe {
	t.Helper()
	p := ask(http.MethodGet, "/v1/models")
	p = p.through(AuthAvailableFilter)
	return p
}

// Identity unreachable is 503 — never 401, and never a served request.
//
// 401 is the failure that hides: it says "your credential is wrong" to a caller
// whose credential is fine, invites a client to throw away a good token, and reads
// as ordinary traffic on every dashboard. That is exactly how a build once served
// 401 to everything for a day while its pod stayed Running and green.
func TestAuthAvailableFilter_RefusesWith503WhenIdentityIsDown(t *testing.T) {
	object.ResetAuthReady()
	t.Setenv("IAM_URL", "http://127.0.0.1:1")
	t.Setenv("IAM_APP_NAME", "hanzo-cloud")

	p := filterProbe(t)
	if p.status() != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 — a 401 here blames the caller for our outage", p.status())
	}
	if got := p.replied("Retry-After"); got == "" {
		t.Error("no Retry-After: a client cannot tell a transient refusal from a permanent one")
	}
	if body := p.said(); body == "" {
		t.Error("the refusal says nothing about why")
	}
}

// A deployment with no IAM configured is deliberately running without auth, and the
// gate must not stand in front of it — otherwise every dev and test deployment
// answers 503 to everything.
func TestAuthAvailableFilter_PassesWhenNoIAMIsConfigured(t *testing.T) {
	object.ResetAuthReady()
	t.Setenv("IAM_URL", "")

	p := filterProbe(t)
	if p.status() != http.StatusOK {
		t.Fatalf("status = %d, want the request to pass untouched (200 is the recorder's default)", p.status())
	}
	if len(p.said()) != 0 {
		t.Fatalf("the filter wrote a body on the pass path: %q", p.said())
	}
}
