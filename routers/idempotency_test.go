// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/hanzoai/ai/internal/authtest"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
	"github.com/hanzoai/dbx"
	kv "github.com/hanzokv/go/v9"
	"github.com/zap-proto/zip"
)

// ── Test harness helpers ─────────────────────────────────────────────────────

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func openSharedSQLite(t *testing.T) (*dbx.DB, string) {
	t.Helper()
	dsn := fmt.Sprintf("file:idem_router_%d?mode=memory&cache=shared&_busy_timeout=5000", time.Now().UnixNano())
	db, err := dbx.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("dbx.Open sqlite: %v", err)
	}
	db.DB().SetMaxOpenConns(1)
	if err := object.SyncSchema(db); err != nil {
		_ = db.Close()
		t.Fatalf("SyncSchema: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, "sqlite"
}

func newIdemTestApp(mw *IdempotencyMiddleware, handler func(*zip.Ctx) error) *zip.App {
	app := zip.New(zip.Config{DisableStartupMessage: true, ReadBufferSize: 32 << 10})
	app.Use(zip.H(Recovered))
	app.Use(zip.H(CallerFilter))
	app.Use(zip.H(TenantContextFilter))
	app.Use(zip.H(mw.Handle))
	app.Raw(zip.MethodAll, "/*", handler)
	return app
}

type idemResp struct {
	Status  int
	Body    string
	Headers http.Header
}

func doIdemReq(t *testing.T, app *zip.App, method, target, bearer, key, body string, extraHeaders map[string]string) idemResp {
	t.Helper()
	req := httptest.NewRequest(method, target, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", bearer)
	}
	if key != "" {
		req.Header.Set(headerIdempotencyKey, key)
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("app.Test(%s %s): %v", method, target, err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return idemResp{
		Status:  resp.StatusCode,
		Body:    string(b),
		Headers: resp.Header,
	}
}

func decodeErrorCode(t *testing.T, body string) map[string]any {
	t.Helper()
	var env struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("unmarshal error body %q: %v", body, err)
	}
	return env.Error
}

// ── Case 2: Two users in the same org (zero cross-principal disclosure) ──────

func TestQualification02_SameOrgCrossPrincipalIsolation(t *testing.T) {
	db, drv := openSharedSQLite(t)
	store := object.NewIdempotencyStore(db, drv, object.DefaultIdempotencyConfig())
	mw := NewIdempotencyMiddleware(store, "replica-1")

	var calls atomic.Int32
	app := newIdemTestApp(mw, func(c *zip.Ctx) error {
		n := calls.Add(1)
		u := sessionOrBearerUser(c)
		exec := object.IdempotencyExecutionFromContext(c.Context())
		return c.JSON(http.StatusCreated, map[string]any{
			"call":         n,
			"principal":    u.Owner + "/" + u.Name,
			"secret":       "secret-for-" + u.Name,
			"execution_id": exec.ExecutionID,
		})
	})

	alice := authtest.Bearer(t, iam.User{Owner: "acme", Name: "alice"})
	bob := authtest.Bearer(t, iam.User{Owner: "acme", Name: "bob"})

	rAlice := doIdemReq(t, app, http.MethodPost, "/v1/chat/completions", alice, "shared-key", `{"model":"zen6"}`, nil)
	if rAlice.Status != http.StatusCreated || !strings.Contains(rAlice.Body, "secret-for-alice") {
		t.Fatalf("alice = %d %s", rAlice.Status, rAlice.Body)
	}

	// Bob in the SAME org sends the EXACT same Idempotency-Key and body, AND attempts
	// to spoof X-User-Id: acme/alice. Must execute independently with zero disclosure!
	rBob := doIdemReq(t, app, http.MethodPost, "/v1/chat/completions", bob, "shared-key", `{"model":"zen6"}`, map[string]string{
		"X-User-Id": "acme/alice",
	})
	if rBob.Status != http.StatusCreated {
		t.Fatalf("bob = %d %s, want 201", rBob.Status, rBob.Body)
	}
	if strings.Contains(rBob.Body, "secret-for-alice") {
		t.Fatalf("CROSS-PRINCIPAL DISCLOSURE: bob received alice's body: %s", rBob.Body)
	}
	if !strings.Contains(rBob.Body, "secret-for-bob") {
		t.Fatalf("bob did not get own response: %s", rBob.Body)
	}
	if rAlice.Headers.Get(headerExecutionID) == rBob.Headers.Get(headerExecutionID) {
		t.Fatalf("alice and bob shared execution_id %q", rAlice.Headers.Get(headerExecutionID))
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2 (independent executions per principal)", calls.Load())
	}

	// Alice retries -> replays her own cached result without a 3rd execution.
	rAliceRetry := doIdemReq(t, app, http.MethodPost, "/v1/chat/completions", alice, "shared-key", `{"model":"zen6"}`, nil)
	if rAliceRetry.Status != http.StatusCreated || rAliceRetry.Body != rAlice.Body || rAliceRetry.Headers.Get(headerIdempotencyReplayed) != "true" {
		t.Fatalf("alice retry = %d replayed=%q body=%s", rAliceRetry.Status, rAliceRetry.Headers.Get(headerIdempotencyReplayed), rAliceRetry.Body)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls after alice retry = %d, want 2", calls.Load())
	}
}

// ── Case 3: Cross-org isolation + unverified header spoof defense ────────────

func TestQualification03_CrossOrgIsolation(t *testing.T) {
	db, drv := openSharedSQLite(t)
	store := object.NewIdempotencyStore(db, drv, object.DefaultIdempotencyConfig())
	mw := NewIdempotencyMiddleware(store, "replica-1")

	var calls atomic.Int32
	app := newIdemTestApp(mw, func(c *zip.Ctx) error {
		calls.Add(1)
		org := GetOrg(c)
		return c.JSON(http.StatusOK, map[string]any{"org": org})
	})

	acmeAlice := authtest.Bearer(t, iam.User{Owner: "acme", Name: "alice"})
	betaAlice := authtest.Bearer(t, iam.User{Owner: "beta", Name: "alice"})

	// Unauthenticated caller spoofing X-Org-Id: acme MUST be rejected 401.
	rUnauth := doIdemReq(t, app, http.MethodPost, "/v1/chat/completions", "", "org-key", `{"a":1}`, map[string]string{
		"X-Org-Id":  "acme",
		"X-User-Id": "acme/alice",
	})
	if rUnauth.Status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request with Idempotency-Key = %d, want 401", rUnauth.Status)
	}

	rAcme := doIdemReq(t, app, http.MethodPost, "/v1/chat/completions", acmeAlice, "org-key", `{"a":1}`, nil)
	if rAcme.Status != http.StatusOK || !strings.Contains(rAcme.Body, `"acme"`) {
		t.Fatalf("acme = %d %s", rAcme.Status, rAcme.Body)
	}

	// beta/alice spoofs X-Org-Id: acme with DIFFERENT body & SAME key: must NOT
	// replay acme's data and must NOT conflict with acme's key!
	rBeta := doIdemReq(t, app, http.MethodPost, "/v1/chat/completions", betaAlice, "org-key", `{"a":999}`, map[string]string{
		"X-Org-Id": "acme",
	})
	if rBeta.Status != http.StatusOK || !strings.Contains(rBeta.Body, `"beta"`) {
		t.Fatalf("beta = %d %s, want 200 with beta org", rBeta.Status, rBeta.Body)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
}

// ── Case 4: Same key + different payload -> 409 conflict ─────────────────────

func TestQualification04_SameKeyDifferentPayloadConflict(t *testing.T) {
	db, drv := openSharedSQLite(t)
	store := object.NewIdempotencyStore(db, drv, object.DefaultIdempotencyConfig())
	mw := NewIdempotencyMiddleware(store, "replica-1")

	var calls atomic.Int32
	app := newIdemTestApp(mw, func(c *zip.Ctx) error {
		calls.Add(1)
		return c.JSON(http.StatusOK, map[string]any{"ok": true})
	})

	alice := authtest.Bearer(t, iam.User{Owner: "acme", Name: "alice"})

	// First request succeeds.
	r1 := doIdemReq(t, app, http.MethodPost, "/v1/chat/completions?a=1&b=2", alice, "fp-key", `{"model":"zen6","n":1}`, nil)
	if r1.Status != http.StatusOK {
		t.Fatalf("r1 = %d %s", r1.Status, r1.Body)
	}

	// Semantically identical JSON (different key order / whitespace) + reordered query params -> REPLAYS 200!
	rSame := doIdemReq(t, app, http.MethodPost, "/v1/chat/completions?b=2&a=1", alice, "fp-key", `{ "n": 1, "model": "zen6" }`, nil)
	if rSame.Status != http.StatusOK || rSame.Headers.Get(headerIdempotencyReplayed) != "true" {
		t.Fatalf("canonical equivalent request failed to replay: %d %s", rSame.Status, rSame.Body)
	}

	// Different body -> 409 idempotency_key_conflict!
	rDiffBody := doIdemReq(t, app, http.MethodPost, "/v1/chat/completions?a=1&b=2", alice, "fp-key", `{"model":"zen6","n":2}`, nil)
	if rDiffBody.Status != http.StatusConflict {
		t.Fatalf("diff body status = %d, want 409 (%s)", rDiffBody.Status, rDiffBody.Body)
	}
	if code := decodeErrorCode(t, rDiffBody.Body)["code"]; code != "idempotency_key_conflict" {
		t.Fatalf("diff body error code = %v, want idempotency_key_conflict", code)
	}

	// Delimiter-collision attempt on query string (?a=1%26b%3D2 vs ?a=1&b=2) -> 409!
	rDiffQuery := doIdemReq(t, app, http.MethodPost, "/v1/chat/completions?a=1%26b%3D2", alice, "fp-key", `{"model":"zen6","n":1}`, nil)
	if rDiffQuery.Status != http.StatusConflict {
		t.Fatalf("delimiter-collision query status = %d, want 409 (%s)", rDiffQuery.Status, rDiffQuery.Body)
	}

	if calls.Load() != 1 {
		t.Fatalf("handler executed %d times, want 1", calls.Load())
	}
}

// ── Case 5: Concurrent requests across simulated replicas ────────────────────

func TestQualification05_ConcurrentReplicasSingleExecution(t *testing.T) {
	db, drv := openSharedSQLite(t)
	mr := miniredis.RunT(t)
	kvClient := kv.NewClient(&kv.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = kvClient.Close() })

	cfgWait := object.DefaultIdempotencyConfig()
	cfgWait.InFlightWaitTimeout = 3 * time.Second

	cfgNoWait := object.DefaultIdempotencyConfig()
	cfgNoWait.InFlightWaitTimeout = 0 // immediate explicit in-flight 409

	storeA := object.NewIdempotencyStore(db, drv, cfgWait)
	storeA.SetKV(object.NewRedisIdempotencyKV(kvClient))
	storeB := object.NewIdempotencyStore(db, drv, cfgWait)
	storeB.SetKV(object.NewRedisIdempotencyKV(kvClient))
	storeFastFail := object.NewIdempotencyStore(db, drv, cfgNoWait)
	storeFastFail.SetKV(object.NewRedisIdempotencyKV(kvClient))

	mwA := NewIdempotencyMiddleware(storeA, "replica-A")
	mwB := NewIdempotencyMiddleware(storeB, "replica-B")
	mwFast := NewIdempotencyMiddleware(storeFastFail, "replica-C")

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32

	handler := func(c *zip.Ctx) error {
		calls.Add(1)
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		exec := object.IdempotencyExecutionFromContext(c.Context())
		return c.JSON(http.StatusCreated, map[string]any{
			"execution_id": exec.ExecutionID,
			"result":       "single-execution-ok",
		})
	}

	appA := newIdemTestApp(mwA, handler)
	appB := newIdemTestApp(mwB, handler)
	appFast := newIdemTestApp(mwFast, handler)

	alice := authtest.Bearer(t, iam.User{Owner: "acme", Name: "alice"})

	// Launch first request on Replica A and wait until it enters the handler.
	const total = 8
	results := make([]idemResp, total)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		results[0] = doIdemReq(t, appA, http.MethodPost, "/v1/chat/completions", alice, "conc-key", `{"model":"zen6"}`, nil)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("first request never entered handler")
	}

	// While Replica A is executing: a no-wait replica gets explicit 409 idempotency_in_progress + Retry-After!
	inFlightResp := doIdemReq(t, appFast, http.MethodPost, "/v1/chat/completions", alice, "conc-key", `{"model":"zen6"}`, nil)
	if inFlightResp.Status != http.StatusConflict {
		t.Fatalf("in-flight status = %d, want 409 (%s)", inFlightResp.Status, inFlightResp.Body)
	}
	if code := decodeErrorCode(t, inFlightResp.Body)["code"]; code != "idempotency_in_progress" {
		t.Fatalf("in-flight code = %v, want idempotency_in_progress", code)
	}
	if inFlightResp.Headers.Get("Retry-After") == "" {
		t.Fatal("in-flight 409 missing Retry-After header")
	}

	// Launch 7 more concurrent duplicates across BOTH Replica A and Replica B.
	for i := 1; i < total; i++ {
		wg.Add(1)
		app := appA
		if i%2 == 1 {
			app = appB
		}
		go func(idx int, target *zip.App) {
			defer wg.Done()
			results[idx] = doIdemReq(t, target, http.MethodPost, "/v1/chat/completions", alice, "conc-key", `{"model":"zen6"}`, nil)
		}(i, app)
	}

	// Give waiters a moment to register on local channels + KV pub/sub, then release handler.
	time.Sleep(40 * time.Millisecond)
	close(release)
	wg.Wait()

	if calls.Load() != 1 {
		t.Fatalf("duplicate execution across replicas: calls = %d, want 1", calls.Load())
	}
	wantExecID := results[0].Headers.Get(headerExecutionID)
	for i, r := range results {
		if r.Status != http.StatusCreated {
			t.Fatalf("result[%d] status = %d, want 201 (%s)", i, r.Status, r.Body)
		}
		if r.Headers.Get(headerExecutionID) != wantExecID {
			t.Fatalf("result[%d] execution_id = %q, want %q", i, r.Headers.Get(headerExecutionID), wantExecID)
		}
		if !strings.Contains(r.Body, "single-execution-ok") {
			t.Fatalf("result[%d] unexpected body: %s", i, r.Body)
		}
	}
}

// ── Case 6: Worker crash before inference + lease expiry recovery ────────────

func TestQualification06_CrashBeforeInferenceLeaseRecovery(t *testing.T) {
	db, drv := openSharedSQLite(t)
	clk := newTestClock()

	cfg := object.DefaultIdempotencyConfig()
	cfg.LeaseTTL = 10 * time.Second
	cfg.InFlightWaitTimeout = 0
	cfg.Now = clk.Now

	storeA := object.NewIdempotencyStore(db, drv, cfg)
	storeB := object.NewIdempotencyStore(db, drv, cfg)
	mwA := NewIdempotencyMiddleware(storeA, "replica-A")
	mwB := NewIdempotencyMiddleware(storeB, "replica-B")

	// Simulate Worker A crashing right after CLAIMED, BEFORE MarkRunning / inference!
	var firstExecID string
	mwA.beforeRunning = func(ctx context.Context, rec *object.IdempotencyRecord) error {
		firstExecID = rec.ExecutionID
		return errors.New("simulated worker crash before inference")
	}

	var calls atomic.Int32
	handler := func(c *zip.Ctx) error {
		calls.Add(1)
		exec := object.IdempotencyExecutionFromContext(c.Context())
		return c.JSON(http.StatusOK, map[string]any{
			"execution_id": exec.ExecutionID,
			"billing_ref":  exec.BillingRef,
			"fence_token":  exec.FenceToken,
		})
	}
	appA := newIdemTestApp(mwA, handler)
	appB := newIdemTestApp(mwB, handler)

	alice := authtest.Bearer(t, iam.User{Owner: "acme", Name: "alice"})

	// Worker A crashes before inference (handler NOT called, record left CLAIMED).
	_ = doIdemReq(t, appA, http.MethodPost, "/v1/chat/completions", alice, "crash-pre", `{"model":"zen6"}`, nil)
	if calls.Load() != 0 || firstExecID == "" {
		t.Fatalf("expected 0 handler calls and non-empty execID, got calls=%d execID=%q", calls.Load(), firstExecID)
	}

	// Before lease expires: Replica B gets 409 in-flight (does NOT double-run).
	rEarly := doIdemReq(t, appB, http.MethodPost, "/v1/chat/completions", alice, "crash-pre", `{"model":"zen6"}`, nil)
	if rEarly.Status != http.StatusConflict {
		t.Fatalf("before lease expiry = %d, want 409", rEarly.Status)
	}

	// Advance clock past lease expiry -> Replica B takes over safely with fence=2 and SAME execution_id!
	clk.Advance(11 * time.Second)
	rRecovered := doIdemReq(t, appB, http.MethodPost, "/v1/chat/completions", alice, "crash-pre", `{"model":"zen6"}`, nil)
	if rRecovered.Status != http.StatusOK {
		t.Fatalf("recovered = %d %s, want 200", rRecovered.Status, rRecovered.Body)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
	if rRecovered.Headers.Get(headerExecutionID) != firstExecID {
		t.Fatalf("recovered execution_id = %q, want original %q", rRecovered.Headers.Get(headerExecutionID), firstExecID)
	}
	if !strings.Contains(rRecovered.Body, `"fence_token":2`) {
		t.Fatalf("expected fence_token=2 after takeover, body=%s", rRecovered.Body)
	}
}

// ── Case 7: Worker crash after inference before commit -> UNKNOWN reconciliation

func TestQualification07_CrashAfterInferenceUnknownReconciliation(t *testing.T) {
	db, drv := openSharedSQLite(t)
	clk := newTestClock()

	cfg := object.DefaultIdempotencyConfig()
	cfg.LeaseTTL = 10 * time.Second
	cfg.InFlightWaitTimeout = 0
	cfg.Now = clk.Now

	store := object.NewIdempotencyStore(db, drv, cfg)
	mw := NewIdempotencyMiddleware(store, "replica-B")

	var handlerCalls atomic.Int32
	app := newIdemTestApp(mw, func(c *zip.Ctx) error {
		handlerCalls.Add(1)
		return c.JSON(http.StatusOK, map[string]any{"should_not_run": true})
	})

	aliceUser := iam.User{Owner: "acme", Name: "alice"}
	alice := authtest.Bearer(t, aliceUser)
	body := `{"model":"zen6","prompt":"hello"}`

	// Simulate Worker A claiming, transitioning to RUNNING, completing inference,
	// and dying before writing COMPLETED.
	scope := object.IdempotencyScope{
		Org: "acme", Principal: "acme/alice", AuthScope: "jwt", Operation: "POST /v1/chat/completions",
	}
	fp := object.ComputeRequestFingerprint("POST", "/v1/chat/completions", nil, "application/json", []byte(body))
	c1, err := store.Claim(context.Background(), object.IdempotencyClaimRequest{
		Scope: scope, IdempotencyKey: "crash-post", RequestHash: fp, OwnerID: "dead-replica-A",
	})
	if err != nil || c1.Outcome != object.ClaimOwned {
		t.Fatalf("claim = %+v err=%v", c1, err)
	}
	if err := store.MarkRunning(context.Background(), c1.Record.ScopeHash, "crash-post", c1.Record.FenceToken); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	// Worker A crashes! Lease expires.
	clk.Advance(11 * time.Second)

	// Retry arrives at Replica B -> transitions RUNNING -> UNKNOWN (409), NEVER silently re-executes!
	rUnknown := doIdemReq(t, app, http.MethodPost, "/v1/chat/completions", alice, "crash-post", body, nil)
	if rUnknown.Status != http.StatusConflict {
		t.Fatalf("post-crash retry status = %d, want 409 (%s)", rUnknown.Status, rUnknown.Body)
	}
	errObj := decodeErrorCode(t, rUnknown.Body)
	if errObj["code"] != "idempotency_execution_unknown" || errObj["state"] != object.IdemStateUnknown {
		t.Fatalf("unexpected UNKNOWN error payload: %+v", errObj)
	}
	if handlerCalls.Load() != 0 {
		t.Fatalf("handler silently re-executed on UNKNOWN! calls=%d", handlerCalls.Load())
	}

	// Explicit reconciliation resolves UNKNOWN -> COMPLETED (fence=2 -> 3).
	rec, err := store.Get(context.Background(), c1.Record.ScopeHash, "crash-post")
	if err != nil || rec.State != object.IdemStateUnknown {
		t.Fatalf("stored row = %+v err=%v", rec, err)
	}
	if err := store.Reconcile(context.Background(), rec.ScopeHash, "crash-post", rec.FenceToken, object.IdemStateCompleted, object.IdempotencyOutcome{
		Status:               http.StatusOK,
		Headers:              map[string]string{"Content-Type": "application/json"},
		Body:                 []byte(`{"reconciled":"yes"}`),
		SideEffectsCommitted: true,
	}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Subsequent retry now replays reconciled COMPLETED outcome cleanly!
	rReplay := doIdemReq(t, app, http.MethodPost, "/v1/chat/completions", alice, "crash-post", body, nil)
	if rReplay.Status != http.StatusOK || rReplay.Body != `{"reconciled":"yes"}` || rReplay.Headers.Get(headerIdempotencyReplayed) != "true" {
		t.Fatalf("reconciled replay = %d replayed=%q body=%s", rReplay.Status, rReplay.Headers.Get(headerIdempotencyReplayed), rReplay.Body)
	}
	if handlerCalls.Load() != 0 {
		t.Fatalf("handler executed during replay: calls=%d", handlerCalls.Load())
	}
}

// ── Case 8: Worker crash before/after debit (single immutable debit ID) ──────

func TestQualification08_CrashBeforeAndAfterDebitSingleImmutableID(t *testing.T) {
	db, drv := openSharedSQLite(t)
	clk := newTestClock()

	cfg := object.DefaultIdempotencyConfig()
	cfg.LeaseTTL = 10 * time.Second
	cfg.InFlightWaitTimeout = 0
	cfg.Now = clk.Now

	store := object.NewIdempotencyStore(db, drv, cfg)
	ctx := context.Background()

	// Simulated Commerce ledger deduplicating by BillingRef.
	var debitMu sync.Mutex
	debits := make(map[string]int)
	debitAttempts := 0
	chargeCommerce := func(billingRef string) {
		debitMu.Lock()
		defer debitMu.Unlock()
		debitAttempts++
		debits[billingRef]++
	}

	scope := object.IdempotencyScope{
		Org: "acme", Principal: "acme/alice", AuthScope: "jwt", Operation: "POST /v1/images/generations",
	}
	fp := object.ComputeRequestFingerprint("POST", "/v1/images/generations", nil, "application/json", []byte(`{"prompt":"cat"}`))

	// Part A: Crash BEFORE debit (while CLAIMED) -> takeover preserves EXACT BillingRef, debits ONCE.
	cA1, err := store.Claim(ctx, object.IdempotencyClaimRequest{Scope: scope, IdempotencyKey: "debit-before-crash", RequestHash: fp, OwnerID: "w1"})
	if err != nil || cA1.Outcome != object.ClaimOwned {
		t.Fatalf("cA1 = %+v err=%v", cA1, err)
	}
	// w1 crashes before debit; lease expires.
	clk.Advance(11 * time.Second)
	cA2, err := store.Claim(ctx, object.IdempotencyClaimRequest{Scope: scope, IdempotencyKey: "debit-before-crash", RequestHash: fp, OwnerID: "w2"})
	if err != nil || cA2.Outcome != object.ClaimOwned {
		t.Fatalf("cA2 = %+v err=%v", cA2, err)
	}
	if cA2.Record.BillingRef != cA1.Record.BillingRef || cA2.Record.ExecutionID != cA1.Record.ExecutionID {
		t.Fatalf("billing identity drifted on takeover: %q vs %q", cA2.Record.BillingRef, cA1.Record.BillingRef)
	}
	if err := store.MarkRunning(ctx, cA2.Record.ScopeHash, "debit-before-crash", cA2.Record.FenceToken); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	chargeCommerce(cA2.Record.BillingRef)
	if err := store.MarkSideEffectsCommitted(ctx, cA2.Record.ScopeHash, "debit-before-crash", cA2.Record.FenceToken); err != nil {
		t.Fatalf("MarkSideEffectsCommitted: %v", err)
	}
	if err := store.Complete(ctx, cA2.Record.ScopeHash, "debit-before-crash", cA2.Record.FenceToken, object.IdempotencyOutcome{
		Status: 200, Body: []byte(`{"url":"ok"}`), SideEffectsCommitted: true,
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Part B: Crash AFTER debit (while RUNNING) -> lease expiry transitions to UNKNOWN,
	// preserving the SAME BillingRef; reconciliation confirms debit without minting a second ID.
	cB1, err := store.Claim(ctx, object.IdempotencyClaimRequest{Scope: scope, IdempotencyKey: "debit-after-crash", RequestHash: fp, OwnerID: "w1"})
	if err != nil || cB1.Outcome != object.ClaimOwned {
		t.Fatalf("cB1 = %+v err=%v", cB1, err)
	}
	if err := store.MarkRunning(ctx, cB1.Record.ScopeHash, "debit-after-crash", cB1.Record.FenceToken); err != nil {
		t.Fatalf("MarkRunning B: %v", err)
	}
	chargeCommerce(cB1.Record.BillingRef)
	_ = store.MarkSideEffectsCommitted(ctx, cB1.Record.ScopeHash, "debit-after-crash", cB1.Record.FenceToken)
	// w1 crashes before Complete! Lease expires.
	clk.Advance(11 * time.Second)
	cB2, err := store.Claim(ctx, object.IdempotencyClaimRequest{Scope: scope, IdempotencyKey: "debit-after-crash", RequestHash: fp, OwnerID: "w2"})
	if err != nil || cB2.Outcome != object.ClaimUnknown {
		t.Fatalf("expected ClaimUnknown after post-debit crash, got %+v err=%v", cB2, err)
	}
	if cB2.Record.BillingRef != cB1.Record.BillingRef || !cB2.Record.SideEffectsCommitted {
		t.Fatalf("post-debit UNKNOWN lost billing metadata: %+v", cB2.Record)
	}
	// Even if a reconciler retries Commerce settlement with cB2.Record.BillingRef, Commerce sees the exact same single key.
	chargeCommerce(cB2.Record.BillingRef)
	if len(debits) != 2 {
		t.Fatalf("expected exactly 2 distinct immutable billing refs (1 for Part A, 1 for Part B), got %d (%v)", len(debits), debits)
	}
	if debits[cA1.Record.BillingRef] != 1 {
		t.Fatalf("Part A debited %d times, want 1", debits[cA1.Record.BillingRef])
	}
	if _, ok := debits[cB1.Record.BillingRef]; !ok || debitAttempts != 3 {
		t.Fatalf("Part B single-key dedup verification failed: debits=%v attempts=%d", debits, debitAttempts)
	}
}

// ── Case 9: Authoritative store outage -> fail-closed 503 ────────────────────

func TestQualification09_AuthoritativeStoreOutageFailClosed503(t *testing.T) {
	var calls atomic.Int32
	alice := authtest.Bearer(t, iam.User{Owner: "acme", Name: "alice"})

	// Subcase A: Nil / uninitialized authoritative store.
	nilStore := object.NewIdempotencyStoreWithProvider(func() (*dbx.DB, string) { return nil, "" }, object.DefaultIdempotencyConfig())
	appNil := newIdemTestApp(NewIdempotencyMiddleware(nilStore, "r1"), func(c *zip.Ctx) error {
		calls.Add(1)
		return c.JSON(http.StatusOK, map[string]any{"ok": true})
	})

	r503 := doIdemReq(t, appNil, http.MethodPost, "/v1/chat/completions", alice, "outage-key", `{"a":1}`, nil)
	if r503.Status != http.StatusServiceUnavailable {
		t.Fatalf("nil store status = %d, want 503 (%s)", r503.Status, r503.Body)
	}
	if code := decodeErrorCode(t, r503.Body)["code"]; code != "idempotency_store_unavailable" {
		t.Fatalf("nil store error code = %v, want idempotency_store_unavailable", code)
	}
	if calls.Load() != 0 {
		t.Fatalf("handler executed during store outage! calls=%d", calls.Load())
	}

	// Mutations WITHOUT Idempotency-Key pass through unaffected.
	rNoKey := doIdemReq(t, appNil, http.MethodPost, "/v1/chat/completions", alice, "", `{"a":1}`, nil)
	if rNoKey.Status != http.StatusOK || calls.Load() != 1 {
		t.Fatalf("request without Idempotency-Key = %d calls=%d, want 200 & 1", rNoKey.Status, calls.Load())
	}

	// Subcase B: Live DB suddenly closes (mid-flight outage).
	dsn := fmt.Sprintf("file:idem_closed_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := dbx.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := object.SyncSchema(db); err != nil {
		t.Fatalf("SyncSchema: %v", err)
	}
	_ = db.Close() // simulate DB outage!

	closedStore := object.NewIdempotencyStore(db, "sqlite", object.DefaultIdempotencyConfig())
	appClosed := newIdemTestApp(NewIdempotencyMiddleware(closedStore, "r2"), func(c *zip.Ctx) error {
		calls.Add(1)
		return c.JSON(http.StatusOK, map[string]any{"ok": true})
	})
	rClosed := doIdemReq(t, appClosed, http.MethodPost, "/v1/chat/completions", alice, "outage-closed", `{"a":1}`, nil)
	if rClosed.Status != http.StatusServiceUnavailable {
		t.Fatalf("closed DB status = %d, want 503 (%s)", rClosed.Status, rClosed.Body)
	}
	if calls.Load() != 1 {
		t.Fatalf("handler executed with closed DB! calls=%d", calls.Load())
	}
}

// ── Case 10: Cache eviction -> still replays from durable store (0 re-exec) ──

func TestQualification10_CacheEvictionDurableReplay(t *testing.T) {
	db, drv := openSharedSQLite(t)
	mr := miniredis.RunT(t)
	kvClient := kv.NewClient(&kv.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = kvClient.Close() })

	var calls atomic.Int32
	handler := func(c *zip.Ctx) error {
		n := calls.Add(1)
		return c.JSON(http.StatusCreated, map[string]any{"call": n, "answer": "durable-42"})
	}

	store1 := object.NewIdempotencyStore(db, drv, object.DefaultIdempotencyConfig())
	store1.SetKV(object.NewRedisIdempotencyKV(kvClient))
	app1 := newIdemTestApp(NewIdempotencyMiddleware(store1, "pod-1"), handler)

	alice := authtest.Bearer(t, iam.User{Owner: "acme", Name: "alice"})

	r1 := doIdemReq(t, app1, http.MethodPost, "/v1/chat/completions", alice, "evict-key", `{"model":"zen6"}`, nil)
	if r1.Status != http.StatusCreated || calls.Load() != 1 {
		t.Fatalf("first = %d calls=%d", r1.Status, calls.Load())
	}

	// Wipe EVERY cache layer (Valkey/Redis FLUSHALL + L1 eviction) AND construct
	// a brand-new replica (simulating full pod restart with cold caches).
	mr.FlushAll()
	store1.EvictCaches()

	store2 := object.NewIdempotencyStore(db, drv, object.DefaultIdempotencyConfig())
	store2.SetKV(object.NewRedisIdempotencyKV(kvClient))
	app2 := newIdemTestApp(NewIdempotencyMiddleware(store2, "pod-2-restarted"), handler)

	r2 := doIdemReq(t, app2, http.MethodPost, "/v1/chat/completions", alice, "evict-key", `{"model":"zen6"}`, nil)
	if r2.Status != http.StatusCreated || r2.Body != r1.Body {
		t.Fatalf("post-eviction replay mismatch: status=%d body=%s want=%s", r2.Status, r2.Body, r1.Body)
	}
	if r2.Headers.Get(headerIdempotencyReplayed) != "true" {
		t.Fatalf("expected X-Idempotency-Replayed=true, got %q", r2.Headers.Get(headerIdempotencyReplayed))
	}
	if r2.Headers.Get(headerExecutionID) != r1.Headers.Get(headerExecutionID) {
		t.Fatalf("execution_id changed after eviction: %q vs %q", r2.Headers.Get(headerExecutionID), r1.Headers.Get(headerExecutionID))
	}
	if calls.Load() != 1 {
		t.Fatalf("re-executed after cache eviction! calls=%d", calls.Load())
	}
}

// ── Case 11: Expired lease + stale worker fencing rejection ──────────────────

func TestQualification11_ExpiredLeaseStaleWorkerFenced(t *testing.T) {
	db, drv := openSharedSQLite(t)
	clk := newTestClock()

	cfg := object.DefaultIdempotencyConfig()
	cfg.LeaseTTL = 10 * time.Second
	cfg.InFlightWaitTimeout = 0
	cfg.Now = clk.Now

	store := object.NewIdempotencyStore(db, drv, cfg)
	ctx := context.Background()

	scope := object.IdempotencyScope{
		Org: "acme", Principal: "acme/alice", AuthScope: "jwt", Operation: "POST /v1/chat/completions",
	}
	fp := object.ComputeRequestFingerprint("POST", "/v1/chat/completions", nil, "application/json", []byte(`{"m":"zen6"}`))

	// Worker A claims (fence=1) and stalls BEFORE MarkRunning.
	claimA, err := store.Claim(ctx, object.IdempotencyClaimRequest{Scope: scope, IdempotencyKey: "fence-key", RequestHash: fp, OwnerID: "stale-A"})
	if err != nil || claimA.Outcome != object.ClaimOwned || claimA.Record.FenceToken != 1 {
		t.Fatalf("claimA = %+v err=%v", claimA, err)
	}

	// Lease expires; Worker B takes over (fence=2), runs, and commits authoritative response B.
	clk.Advance(11 * time.Second)
	claimB, err := store.Claim(ctx, object.IdempotencyClaimRequest{Scope: scope, IdempotencyKey: "fence-key", RequestHash: fp, OwnerID: "fresh-B"})
	if err != nil || claimB.Outcome != object.ClaimOwned || claimB.Record.FenceToken != 2 {
		t.Fatalf("claimB = %+v err=%v", claimB, err)
	}
	if err := store.MarkRunning(ctx, claimB.Record.ScopeHash, "fence-key", claimB.Record.FenceToken); err != nil {
		t.Fatalf("claimB MarkRunning: %v", err)
	}
	if err := store.Complete(ctx, claimB.Record.ScopeHash, "fence-key", claimB.Record.FenceToken, object.IdempotencyOutcome{
		Status:  http.StatusOK,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    []byte(`{"winner":"worker-B"}`),
	}); err != nil {
		t.Fatalf("claimB Complete: %v", err)
	}

	// Stale Worker A wakes up and attempts every mutation with fence=1 -> ALL FENCED!
	if err := store.MarkRunning(ctx, claimA.Record.ScopeHash, "fence-key", claimA.Record.FenceToken); !errors.Is(err, object.ErrIdempotencyFenced) {
		t.Fatalf("stale MarkRunning = %v, want ErrIdempotencyFenced", err)
	}
	if err := store.Complete(ctx, claimA.Record.ScopeHash, "fence-key", claimA.Record.FenceToken, object.IdempotencyOutcome{
		Status: http.StatusOK, Body: []byte(`{"corrupted":"stale-A"}`),
	}); !errors.Is(err, object.ErrIdempotencyFenced) {
		t.Fatalf("stale Complete = %v, want ErrIdempotencyFenced", err)
	}
	if err := store.Fail(ctx, claimA.Record.ScopeHash, "fence-key", claimA.Record.FenceToken, object.IdempotencyOutcome{
		Status: http.StatusInternalServerError, ErrorMessage: "stale fail",
	}); !errors.Is(err, object.ErrIdempotencyFenced) {
		t.Fatalf("stale Fail = %v, want ErrIdempotencyFenced", err)
	}

	// Authoritative row and HTTP replay STILL hold Worker B's intact outcome!
	app := newIdemTestApp(NewIdempotencyMiddleware(store, "reader"), func(c *zip.Ctx) error {
		t.Fatal("handler must not execute on replay")
		return nil
	})
	alice := authtest.Bearer(t, iam.User{Owner: "acme", Name: "alice"})
	r := doIdemReq(t, app, http.MethodPost, "/v1/chat/completions", alice, "fence-key", `{"m":"zen6"}`, nil)
	if r.Status != http.StatusOK || r.Body != `{"winner":"worker-B"}` {
		t.Fatalf("authoritative outcome overwritten or missing: %d %s", r.Status, r.Body)
	}
}

// ── Case 12: Oversized response -> bounded, NEVER re-executes on retry ───────

func TestQualification12_OversizedResponseBoundedNoReexecute(t *testing.T) {
	db, drv := openSharedSQLite(t)
	cfg := object.DefaultIdempotencyConfig()
	cfg.MaxResponseBytes = 1024 // tight cap for deterministic test
	store := object.NewIdempotencyStore(db, drv, cfg)
	mw := NewIdempotencyMiddleware(store, "replica-1")

	bigPayload := strings.Repeat("Z", 4096)
	var calls atomic.Int32
	app := newIdemTestApp(mw, func(c *zip.Ctx) error {
		calls.Add(1)
		c.Status(http.StatusOK)
		c.SetHeader("Content-Type", "text/plain")
		c.Fiber().Response().SetBody([]byte(bigPayload))
		return nil
	})

	alice := authtest.Bearer(t, iam.User{Owner: "acme", Name: "alice"})

	// First request returns the full 4096-byte response.
	r1 := doIdemReq(t, app, http.MethodPost, "/v1/chat/completions", alice, "oversized-key", `{"a":1}`, nil)
	if r1.Status != http.StatusOK || len(r1.Body) != 4096 {
		t.Fatalf("r1 = %d len=%d, want 200 & 4096", r1.Status, len(r1.Body))
	}

	// Verify DB row stored ZERO response bytes and ResponseRetained=false.
	scope := object.IdempotencyScope{
		Org: "acme", Principal: "acme/alice", AuthScope: "jwt", Operation: "POST /v1/chat/completions",
	}
	rec, err := store.Get(context.Background(), object.ComputeScopeHash(scope), "oversized-key")
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if rec.ResponseRetained || len(rec.ResponseBody) != 0 || rec.NonReplayReason != object.IdemNonReplayOversized {
		t.Fatalf("oversized row not bounded properly: retained=%v bodyLen=%d reason=%q", rec.ResponseRetained, len(rec.ResponseBody), rec.NonReplayReason)
	}

	// Retry MUST NOT re-execute! Returns explicit non-replayable 409 with execution metadata.
	r2 := doIdemReq(t, app, http.MethodPost, "/v1/chat/completions", alice, "oversized-key", `{"a":1}`, nil)
	if r2.Status != http.StatusConflict {
		t.Fatalf("r2 status = %d, want 409 (%s)", r2.Status, r2.Body)
	}
	if r2.Headers.Get(headerIdempotencyReplayUnavailable) != object.IdemNonReplayOversized {
		t.Fatalf("replay-unavailable header = %q, want %q", r2.Headers.Get(headerIdempotencyReplayUnavailable), object.IdemNonReplayOversized)
	}
	errObj := decodeErrorCode(t, r2.Body)
	if errObj["code"] != "idempotency_response_not_retained" || errObj["execution_id"] != r1.Headers.Get(headerExecutionID) || errObj["replayable"] != false {
		t.Fatalf("unexpected non-replayable body: %+v", errObj)
	}
	if calls.Load() != 1 {
		t.Fatalf("handler re-executed after oversized response! calls=%d", calls.Load())
	}
}

// ── Case 13: Streaming disconnect -> durable execution & billing ID retained ─

func TestQualification13_StreamingDisconnectRetainsExecutionAndBillingID(t *testing.T) {
	db, drv := openSharedSQLite(t)
	store := object.NewIdempotencyStore(db, drv, object.DefaultIdempotencyConfig())
	mw := NewIdempotencyMiddleware(store, "replica-1")

	var calls atomic.Int32
	var billedRef string
	app := newIdemTestApp(mw, func(c *zip.Ctx) error {
		calls.Add(1)
		exec := object.IdempotencyExecutionFromContext(c.Context())
		billedRef = exec.BillingRef
		c.SetHeader("Content-Type", "text/event-stream")
		c.Status(http.StatusOK)
		c.Fiber().Response().SetBody([]byte("data: {\"delta\":\"partial\"}\n\n"))
		// Client disconnects mid-stream after partial tokens were generated & billed.
		if err := exec.MarkSideEffectsCommitted(c.Context()); err != nil {
			return err
		}
		if err := exec.MarkStreamDisconnected(c.Context()); err != nil {
			return err
		}
		return nil
	})

	alice := authtest.Bearer(t, iam.User{Owner: "acme", Name: "alice"})

	r1 := doIdemReq(t, app, http.MethodPost, "/v1/chat/completions", alice, "stream-disc-key", `{"model":"zen6","stream":true}`, nil)
	if r1.Status != http.StatusOK || billedRef == "" {
		t.Fatalf("r1 = %d billedRef=%q", r1.Status, billedRef)
	}
	execID := r1.Headers.Get(headerExecutionID)

	// Inspect durable SQL ledger: execution_id, billing_ref, streaming & disconnect flags retained!
	scope := object.IdempotencyScope{
		Org: "acme", Principal: "acme/alice", AuthScope: "jwt", Operation: "POST /v1/chat/completions",
	}
	rec, err := store.Get(context.Background(), object.ComputeScopeHash(scope), "stream-disc-key")
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if rec.State != object.IdemStateCompleted || !rec.Streaming || !rec.StreamDisconnected || rec.ResponseRetained {
		t.Fatalf("unexpected streaming disconnect record: %+v", rec)
	}
	if rec.ExecutionID != execID || rec.BillingRef != billedRef {
		t.Fatalf("durable IDs mismatch: got (%q,%q), want (%q,%q)", rec.ExecutionID, rec.BillingRef, execID, billedRef)
	}

	// Retry MUST NOT re-execute and MUST return durable execution_id + stream_disconnected metadata.
	r2 := doIdemReq(t, app, http.MethodPost, "/v1/chat/completions", alice, "stream-disc-key", `{"model":"zen6","stream":true}`, nil)
	if r2.Status != http.StatusConflict {
		t.Fatalf("streaming retry status = %d, want 409 (%s)", r2.Status, r2.Body)
	}
	errObj := decodeErrorCode(t, r2.Body)
	if errObj["code"] != "idempotency_response_not_retained" ||
		errObj["reason"] != object.IdemNonReplayStreamDisconnected ||
		errObj["execution_id"] != execID ||
		errObj["billing_ref"] != billedRef ||
		errObj["stream_disconnected"] != true {
		t.Fatalf("unexpected streaming retry error payload: %+v", errObj)
	}
	if calls.Load() != 1 {
		t.Fatalf("streaming request re-executed on retry! calls=%d", calls.Load())
	}
}

// ── Case 14: Failed response after committed side effects ────────────────────

func TestQualification14_FailedResponseAfterCommittedSideEffects(t *testing.T) {
	db, drv := openSharedSQLite(t)
	store := object.NewIdempotencyStore(db, drv, object.DefaultIdempotencyConfig())
	mw := NewIdempotencyMiddleware(store, "replica-1")

	var calls atomic.Int32
	app := newIdemTestApp(mw, func(c *zip.Ctx) error {
		calls.Add(1)
		exec := object.IdempotencyExecutionFromContext(c.Context())
		// Side effect (e.g. debit / job submission) succeeded before failure occurred!
		if err := exec.MarkSideEffectsCommitted(c.Context()); err != nil {
			return err
		}
		return c.JSON(http.StatusInternalServerError, map[string]any{
			"error": map[string]any{"code": "post_commit_failure", "message": "downstream response write failed"},
		})
	})

	alice := authtest.Bearer(t, iam.User{Owner: "acme", Name: "alice"})

	r1 := doIdemReq(t, app, http.MethodPost, "/v1/ai/finetune/jobs", alice, "fail-after-se", `{"baseModel":"zen6"}`, nil)
	if r1.Status != http.StatusInternalServerError {
		t.Fatalf("r1 = %d %s, want 500", r1.Status, r1.Body)
	}

	// Retry MUST NOT re-execute because side effects were committed!
	r2 := doIdemReq(t, app, http.MethodPost, "/v1/ai/finetune/jobs", alice, "fail-after-se", `{"baseModel":"zen6"}`, nil)
	if r2.Status != http.StatusInternalServerError {
		t.Fatalf("r2 = %d %s, want 500 replay", r2.Status, r2.Body)
	}
	if r2.Headers.Get(headerIdempotencySideEffects) != "true" {
		t.Fatalf("missing X-Idempotency-Side-Effects-Committed=true header on replay: %v", r2.Headers)
	}
	if r2.Headers.Get(headerExecutionID) != r1.Headers.Get(headerExecutionID) {
		t.Fatalf("execution_id changed on failed replay: %q vs %q", r2.Headers.Get(headerExecutionID), r1.Headers.Get(headerExecutionID))
	}
	if calls.Load() != 1 {
		t.Fatalf("handler re-executed after committed side effects! calls=%d", calls.Load())
	}
}

// ── Case 16: Duplicate Commerce settlement + bounded load/memory stress test ─

func TestQualification16_DuplicateCommerceSettlementAndBoundedStress(t *testing.T) {
	// Part A: Duplicate Commerce settlement attempt via BillingQueue carries stable
	// Idempotency-Key & ref, so Commerce settles ONCE even across duplicate enqueues.
	var settleMu sync.Mutex
	settled := make(map[string]int)
	var totalHTTP atomic.Int32
	commerceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		totalHTTP.Add(1)
		idemKey := r.Header.Get("Idempotency-Key")
		if idemKey == "" {
			http.Error(w, "missing Idempotency-Key", http.StatusBadRequest)
			return
		}
		settleMu.Lock()
		settled[idemKey]++
		settleMu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(commerceSrv.Close)

	q := util.NewBillingQueue(commerceSrv.URL, "svc-token")
	const ref1 = "exec:11111111-1111-1111-1111-111111111111"
	const ref2 = "exec:22222222-2222-2222-2222-222222222222"
	for i := 0; i < 4; i++ {
		q.Enqueue(&util.BillingRecord{
			Body:           []byte(`{"ref":"` + ref1 + `","amount":50}`),
			RequestID:      "req-1",
			IdempotencyKey: ref1,
			Org:            "acme",
			Model:          "zen6",
		})
	}
	q.Enqueue(&util.BillingRecord{
		Body:           []byte(`{"ref":"` + ref2 + `","amount":75}`),
		RequestID:      "req-2",
		IdempotencyKey: ref2,
		Org:            "acme",
		Model:          "zen6",
	})
	if rem := q.Shutdown(); rem != 0 {
		t.Fatalf("billing queue left %d pending records", rem)
	}
	settleMu.Lock()
	if len(settled) != 2 || settled[ref1] != 4 || settled[ref2] != 1 {
		t.Fatalf("commerce idempotency keys mismatch: %v (total=%d)", settled, totalHTTP.Load())
	}
	settleMu.Unlock()

	// Part B: Bounded load & memory-exhaustion stress test under -race.
	db, drv := openSharedSQLite(t)
	cfg := object.DefaultIdempotencyConfig()
	cfg.MaxKeyLength = 64
	cfg.MaxFingerprintBodyBytes = 1024
	cfg.MaxResponseBytes = 512
	cfg.MaxHeaderBytes = 256
	cfg.MaxKeysPerTenant = 25
	cfg.CacheMaxBytes = 4096 // tiny 4 KiB L1 cap to force constant eviction under load
	store := object.NewIdempotencyStore(db, drv, cfg)
	mw := NewIdempotencyMiddleware(store, "stress-replica")

	app := newIdemTestApp(mw, func(c *zip.Ctx) error {
		// Emit oversized allowlisted header to verify header capping.
		c.SetHeader("Location", "/v1/resources/"+strings.Repeat("x", 1024))
		return c.JSON(http.StatusOK, map[string]any{"pad": strings.Repeat("p", 200)})
	})

	alice := authtest.Bearer(t, iam.User{Owner: "stress-org", Name: "alice"})

	// 1) Oversized key (>64 bytes), whitespace-only key, & control-char key -> 400 / validation error.
	if r := doIdemReq(t, app, http.MethodPost, "/v1/op", alice, strings.Repeat("k", 65), `{"a":1}`, nil); r.Status != http.StatusBadRequest {
		t.Fatalf("oversized key status = %d, want 400", r.Status)
	}
	if r := doIdemReq(t, app, http.MethodPost, "/v1/op", alice, "   ", `{"a":1}`, nil); r.Status != http.StatusBadRequest {
		t.Fatalf("whitespace-only key status = %d, want 400", r.Status)
	}
	if err := object.ValidateIdempotencyKey("bad\nkey", 64); !errors.Is(err, object.ErrIdempotencyInvalidKey) {
		t.Fatalf("control-char key validation err = %v, want ErrIdempotencyInvalidKey", err)
	}

	// 2) Oversized request body (>1024 bytes) -> 413 Payload Too Large.
	if r := doIdemReq(t, app, http.MethodPost, "/v1/op", alice, "big-body", `{"d":"`+strings.Repeat("b", 1200)+`"}`, nil); r.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body status = %d, want 413", r.Status)
	}

	// 3) High-concurrency flood of 60 distinct keys against MaxKeysPerTenant=25.
	var okCount, capCount atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			key := fmt.Sprintf("flood-%03d", idx)
			r := doIdemReq(t, app, http.MethodPost, "/v1/op", alice, key, fmt.Sprintf(`{"i":%d}`, idx), nil)
			switch r.Status {
			case http.StatusOK:
				okCount.Add(1)
			case http.StatusTooManyRequests:
				capCount.Add(1)
			default:
				t.Errorf("flood[%d] unexpected status %d: %s", idx, r.Status, r.Body)
			}
		}(i)
	}
	wg.Wait()

	if okCount.Load() != 25 || capCount.Load() != 35 {
		t.Fatalf("cardinality cap mismatch: ok=%d cap429=%d, want 25 & 35", okCount.Load(), capCount.Load())
	}

	// Verify L1 cache memory stayed strictly within CacheMaxBytes!
	if used := store.L1BytesUsed(); used > cfg.CacheMaxBytes {
		t.Fatalf("L1 cache exceeded memory cap: used=%d > max=%d", used, cfg.CacheMaxBytes)
	}

	// Verify DB row count for tenant stayed strictly <= MaxKeysPerTenant and headers bounded <= MaxHeaderBytes.
	var dbRows int64
	if err := db.NewQuery("SELECT COUNT(*) FROM idempotency_record WHERE org = 'stress-org'").Row(&dbRows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if dbRows > int64(cfg.MaxKeysPerTenant) {
		t.Fatalf("DB rows %d exceeded MaxKeysPerTenant %d", dbRows, cfg.MaxKeysPerTenant)
	}
	var maxHdrLen int64
	if err := db.NewQuery("SELECT MAX(LENGTH(response_headers)) FROM idempotency_record WHERE org = 'stress-org'").Row(&maxHdrLen); err != nil {
		t.Fatalf("max header len: %v", err)
	}
	if maxHdrLen > int64(cfg.MaxHeaderBytes) {
		t.Fatalf("stored header len %d exceeded MaxHeaderBytes %d", maxHdrLen, cfg.MaxHeaderBytes)
	}
}
