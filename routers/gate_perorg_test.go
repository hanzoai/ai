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

package routers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/beego/beego/context"
	"github.com/hanzoai/ai/object"
)

// iamGetUserStub answers /v1/iam/get-user for a single known hk- key, returning
// owner/name. Any other key → data:null (unknown).
func iamGetUserStub(t *testing.T, key, owner, name string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("accessKey") == key {
			_, _ = w.Write([]byte(`{"status":"ok","data":{"owner":"` + owner + `","name":"` + name + `"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok","data":null}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newCtx(method, path, bearer string, hdrs map[string]string) (*context.Context, *httptest.ResponseRecorder) {
	r := httptest.NewRequest(method, path, nil)
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	for k, v := range hdrs {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	ctx := context.NewContext()
	ctx.Reset(w, r)
	return ctx, w
}

// TestResolveIAMKeySubjectPerOrg proves an hk- key resolves to the caller's own
// org namespace (owner) and the per-subject billing key — the foundation of
// per-org isolation: every downstream balance read and usage debit is scoped to
// THIS org, never a shared/global one.
func TestResolveIAMKeySubjectPerOrg(t *testing.T) {
	srv := iamGetUserStub(t, "hk-acme-1", "acme", "alice")
	bg := newTestGate("http://unused", "")
	bg.iamEndpoint = srv.URL
	bg.clientId, bg.clientSecret = "hanzo-cloud", "secret"

	subject, namespace := bg.resolveIAMKeySubject("hk-acme-1")
	if namespace != "acme" {
		t.Fatalf("namespace=%q want acme (per-org scope)", namespace)
	}
	if want := object.BillingSubject("acme", "alice"); subject != want {
		t.Fatalf("subject=%q want %q", subject, want)
	}

	// Unknown key → no identity (fail-open at the gate; downstream auth rejects).
	if s, ns := bg.resolveIAMKeySubject("hk-unknown"); s != "" || ns != "" {
		t.Fatalf("unknown key resolved to (%q,%q), want empty", s, ns)
	}
}

// TestResolveBillingKeyIgnoresClientOrgHeader is the anti-spoof invariant: a
// client-supplied X-Hanzo-Org header MUST NOT change the billing namespace — it
// is derived solely from the authenticated identity (the IAM lookup of the hk-
// key), so a caller cannot bill another org by setting a header.
func TestResolveBillingKeyIgnoresClientOrgHeader(t *testing.T) {
	srv := iamGetUserStub(t, "hk-acme-1", "acme", "alice")
	// Install the package singleton the filter/key-resolver read.
	balanceGate = newTestGate("http://unused", "")
	balanceGate.iamEndpoint = srv.URL
	balanceGate.clientId, balanceGate.clientSecret = "hanzo-cloud", "secret"
	t.Cleanup(func() { balanceGate = nil })

	ctx, _ := newCtx(http.MethodPost, "/v1/chat/completions", "hk-acme-1", map[string]string{
		"X-Hanzo-Org": "victim-org", // spoof attempt
	})
	subject, namespace := resolveBillingKey(ctx)
	if namespace != "acme" {
		t.Fatalf("billing namespace=%q — client X-Hanzo-Org spoof leaked through (want acme)", namespace)
	}
	if want := object.BillingSubject("acme", "alice"); subject != want {
		t.Fatalf("subject=%q want %q", subject, want)
	}
}

// TestResolveBillingKeySkipsNonOrgKeys asserts provider (sk-), publishable (pk-)
// and widget (hz_) keys do NOT map to an org billing namespace — they must be
// skipped so the gate never mis-attributes their usage to some default org.
func TestResolveBillingKeySkipsNonOrgKeys(t *testing.T) {
	balanceGate = newTestGate("http://unused", "")
	t.Cleanup(func() { balanceGate = nil })
	for _, tok := range []string{"sk-provider-key", "pk-publishable", "hz_widget"} {
		ctx, _ := newCtx(http.MethodPost, "/v1/chat/completions", tok, nil)
		if subject, namespace := resolveBillingKey(ctx); subject != "" || namespace != "" {
			t.Errorf("token %q resolved to billing (%q,%q), want empty (skipped)", tok, subject, namespace)
		}
	}
}

// TestBalanceGateFilterReturns402 is the gate-level 402 path: an authenticated
// org with zero balance is blocked at the BeforeRouter filter, before the
// handler runs — the production 402 surface for completions.
func TestBalanceGateFilterReturns402(t *testing.T) {
	iam := iamGetUserStub(t, "hk-broke-1", "brokeorg", "bob")
	// Commerce returns zero available for everyone.
	commerce := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"available":0}`))
	}))
	t.Cleanup(commerce.Close)

	balanceGate = newTestGate(commerce.URL, "")
	balanceGate.iamEndpoint = iam.URL
	balanceGate.clientId, balanceGate.clientSecret = "hanzo-cloud", "secret"
	t.Cleanup(func() { balanceGate = nil })

	ctx, w := newCtx(http.MethodPost, "/v1/chat/completions", "hk-broke-1", nil)
	BalanceGateFilter(ctx)
	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("zero-balance org: status=%d want 402; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "insufficient_balance") {
		t.Errorf("expected insufficient_balance error body, got %s", w.Body.String())
	}
}

// TestBalanceGateFilterPassesFundedOrg asserts a funded org is NOT blocked —
// the filter writes nothing and lets the request proceed to the handler.
func TestBalanceGateFilterPassesFundedOrg(t *testing.T) {
	iam := iamGetUserStub(t, "hk-rich-1", "richorg", "rich")
	commerce := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"available":5000}`))
	}))
	t.Cleanup(commerce.Close)

	balanceGate = newTestGate(commerce.URL, "")
	balanceGate.iamEndpoint = iam.URL
	balanceGate.clientId, balanceGate.clientSecret = "hanzo-cloud", "secret"
	t.Cleanup(func() { balanceGate = nil })

	ctx, w := newCtx(http.MethodPost, "/v1/chat/completions", "hk-rich-1", nil)
	BalanceGateFilter(ctx)
	if w.Code != http.StatusOK { // recorder default 200; filter wrote nothing
		t.Fatalf("funded org blocked: status=%d body=%s", w.Code, w.Body.String())
	}
	if w.Body.Len() != 0 {
		t.Fatalf("funded org: filter wrote a body (%s); should pass silently", w.Body.String())
	}
}

// TestBalanceGateFilterScopesReadToCallerOrg proves two different orgs hit
// Commerce in their OWN namespace — no cross-org leak at the gate.
func TestBalanceGateFilterScopesReadToCallerOrg(t *testing.T) {
	iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("accessKey") {
		case "hk-a":
			w.Write([]byte(`{"status":"ok","data":{"owner":"org-a","name":"a"}}`))
		case "hk-b":
			w.Write([]byte(`{"status":"ok","data":{"owner":"org-b","name":"b"}}`))
		default:
			w.Write([]byte(`{"status":"ok","data":null}`))
		}
	}))
	t.Cleanup(iam.Close)

	var seen []string
	commerce := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("X-Hanzo-Org"))
		w.Write([]byte(`{"available":1000}`))
	}))
	t.Cleanup(commerce.Close)

	balanceGate = newTestGate(commerce.URL, "")
	balanceGate.iamEndpoint = iam.URL
	balanceGate.clientId, balanceGate.clientSecret = "hanzo-cloud", "secret"
	t.Cleanup(func() { balanceGate = nil })

	ctxA, _ := newCtx(http.MethodPost, "/v1/chat/completions", "hk-a", nil)
	BalanceGateFilter(ctxA)
	ctxB, _ := newCtx(http.MethodPost, "/v1/chat/completions", "hk-b", nil)
	BalanceGateFilter(ctxB)

	if len(seen) != 2 || seen[0] != "org-a" || seen[1] != "org-b" {
		t.Fatalf("per-org namespace scoping wrong: commerce saw X-Hanzo-Org=%v, want [org-a org-b]", seen)
	}
}
