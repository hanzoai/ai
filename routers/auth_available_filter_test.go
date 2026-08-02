// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package routers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/web"
)

// filterProbe runs the filter over one request and reports what it wrote.
func filterProbe(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx := web.NewContext()
	ctx.Reset(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	AuthAvailableFilter(ctx)
	return rec
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

	rec := filterProbe(t)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 — a 401 here blames the caller for our outage", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Error("no Retry-After: a client cannot tell a transient refusal from a permanent one")
	}
	if body := rec.Body.String(); body == "" {
		t.Error("the refusal says nothing about why")
	}
}

// A deployment with no IAM configured is deliberately running without auth, and the
// gate must not stand in front of it — otherwise every dev and test deployment
// answers 503 to everything.
func TestAuthAvailableFilter_PassesWhenNoIAMIsConfigured(t *testing.T) {
	object.ResetAuthReady()
	t.Setenv("IAM_URL", "")

	rec := filterProbe(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want the request to pass untouched (200 is the recorder's default)", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("the filter wrote a body on the pass path: %q", rec.Body.String())
	}
}
