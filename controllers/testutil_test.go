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

package controllers

// Shared, hermetic HTTP test harness for the OpenAI/Anthropic-compatible
// surface. Everything here is in-process: a beego ControllerRegister drives the
// real handlers, IAM and Commerce are httptest stubs, and the billing queue is
// pointed at a counting spy so "no usage emitted on failure" is provable. No
// live-prod dependency. Tests using these helpers MUST be serial (they rely on
// t.Setenv and the package-level billingQueue) — never call t.Parallel.

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/beego/beego"
	beecontext "github.com/beego/beego/context"
	"github.com/hanzoai/ai/util"
	iam "github.com/hanzoai/iam"
)

// ── Session injection ────────────────────────────────────────────────────────

// fakeSession is a minimal in-memory beego session.Store so controller methods
// that read the session (GetSessionUser, GetScopedOwner, …) can be tested with a
// chosen identity, without standing up beego's session middleware.
type fakeSession struct{ data map[interface{}]interface{} }

func (s *fakeSession) Set(k, v interface{}) error         { s.data[k] = v; return nil }
func (s *fakeSession) Get(k interface{}) interface{}      { return s.data[k] }
func (s *fakeSession) Delete(k interface{}) error         { delete(s.data, k); return nil }
func (s *fakeSession) SessionID() string                  { return "test-session" }
func (s *fakeSession) SessionRelease(http.ResponseWriter) {}
func (s *fakeSession) Flush() error                       { s.data = map[interface{}]interface{}{}; return nil }

// newControllerWithUser builds an *ApiController bound to a recorder, seeded with
// the given session user (nil = anonymous) and request headers. It is the seam
// for unit-testing controller methods that depend on the authenticated identity.
func newControllerWithUser(user *iam.User, hdrs map[string]string) (*ApiController, *httptest.ResponseRecorder) {
	r := httptest.NewRequest(http.MethodPost, "/v1/_test", nil)
	for k, v := range hdrs {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	ctx := beecontext.NewContext()
	ctx.Reset(w, r)
	sess := &fakeSession{data: map[interface{}]interface{}{}}
	if user != nil {
		_ = sess.Set("user", iam.Claims{User: *user})
	}
	ctx.Input.CruSession = sess
	c := &ApiController{}
	c.Init(ctx, "ApiController", "", nil)
	return c, w
}

// ── IAM stub ─────────────────────────────────────────────────────────────────

type iamStubUser struct{ owner, name string }

// startIAMStub stands up an httptest IAM answering GET /v1/iam/get-user. A key
// present in valid → {status:ok,data:{owner,name}}; any other key →
// {status:ok,data:null} (IAM's "unknown key" shape — the case the nil-user 401
// fix depends on). The returned counter is set to 1 iff a request ever arrived
// with BOTH clientId and clientSecret (the IAM service-auth requirement).
// IAM_URL/IAM_CLIENT_ID/IAM_CLIENT_SECRET are set via t.Setenv (auto-restored).
func startIAMStub(t *testing.T, valid map[string]iamStubUser) *int32 {
	t.Helper()
	var sawCreds int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("clientId") != "" && q.Get("clientSecret") != "" {
			atomic.StoreInt32(&sawCreds, 1)
		}
		w.Header().Set("Content-Type", "application/json")
		if u, ok := valid[q.Get("accessKey")]; ok {
			_, _ = fmt.Fprintf(w, `{"status":"ok","msg":"","data":{"owner":%q,"name":%q}}`, u.owner, u.name)
			return
		}
		_, _ = io.WriteString(w, `{"status":"ok","msg":"","data":null}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("IAM_URL", srv.URL)
	t.Setenv("IAM_CLIENT_ID", "hanzo-cloud")
	t.Setenv("IAM_CLIENT_SECRET", "test-secret")
	return &sawCreds
}

// ── Commerce stub ────────────────────────────────────────────────────────────

// startCommerceStub answers GET /v1/billing/balance with the given available
// cents (per-org namespace scoping is asserted at the gate level). Sets
// commerceEndpoint via t.Setenv.
func startCommerceStub(t *testing.T, availableCents int64) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"available":%d}`, availableCents)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("commerceEndpoint", srv.URL)
}

// ── Billing usage spy ────────────────────────────────────────────────────────

type billingUsage struct {
	org  string // X-Hanzo-Org namespace the debit was scoped to
	body string // raw JSON payload (carries "user" subject + amount)
}

type billingSpy struct {
	mu      sync.Mutex
	records []billingUsage
}

// startBillingSpy installs the package-level billingQueue pointed at a counting
// httptest sink for POST /v1/billing/usage. Use drain() to deterministically
// flush the async queue and read what was emitted. Failure paths must emit zero.
func startBillingSpy(t *testing.T) *billingSpy {
	t.Helper()
	spy := &billingSpy{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/billing/usage" && r.Method == http.MethodPost {
			b, _ := io.ReadAll(r.Body)
			spy.mu.Lock()
			spy.records = append(spy.records, billingUsage{org: r.Header.Get("X-Hanzo-Org"), body: string(b)})
			spy.mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	billingQueue = util.NewBillingQueue(srv.URL, "")
	t.Cleanup(func() {
		if billingQueue != nil {
			billingQueue.Shutdown()
			billingQueue = nil
		}
	})
	return spy
}

// drain shuts the billing queue (blocking until every enqueued record is
// delivered) and returns the recorded usage POSTs. Deterministic: nothing is
// in flight after Shutdown returns.
func (s *billingSpy) drain() []billingUsage {
	if billingQueue != nil {
		billingQueue.Shutdown()
		billingQueue = nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]billingUsage, len(s.records))
	copy(out, s.records)
	return out
}

// ── HTTP harness ─────────────────────────────────────────────────────────────

// newAPIHandler wires the real OpenAI/Anthropic-compatible handlers into a
// beego ControllerRegister so requests exercise the full controller lifecycle
// (Init → RequestBody → action → ServeJSON/status) in-process.
func newAPIHandler() *beego.ControllerRegister {
	beego.BConfig.CopyRequestBody = true
	h := beego.NewControllerRegister()
	h.Add("/v1/chat/completions", &ApiController{}, "POST:ChatCompletions")
	h.Add("/v1/embeddings", &ApiController{}, "POST:Embeddings")
	h.Add("/v1/rerank", &ApiController{}, "POST:Rerank")
	h.Add("/v1/messages", &ApiController{}, "POST:AnthropicMessages")
	h.Add("/v1/models", &ApiController{}, "GET:ListModels")
	return h
}

// fire sends one request and returns the recorder. A non-empty token is sent as
// "Authorization: Bearer <token>".
func fire(h http.Handler, method, path, token, body string, hdrs map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdrs {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// completionMarkers are success-only response fragments. A fail-secure error
// path must emit NONE of them — no model output, no usage object.
var completionMarkers = []string{`chatcmpl-`, `"choices"`, `"embedding"`, `"relevance_score"`, `"completion_tokens"`}

// assertNoCompletion fails if the body leaks any success-only content on what
// must be an error path.
func assertNoCompletion(t *testing.T, where, body string) {
	t.Helper()
	for _, m := range completionMarkers {
		if strings.Contains(body, m) {
			t.Errorf("%s: fail-secure violated — error response leaked completion marker %q: %s", where, m, body)
		}
	}
}
