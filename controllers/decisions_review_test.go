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

package controllers

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luxfi/zap"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

// A decision service that cannot be reached is a 502 that names no address: where
// the service lives is ours to know.
func TestDecisionRefusalNamesNoAddress(t *testing.T) {
	setupDecisions(t)
	t.Setenv("KAI_URL", "http://127.0.0.1:1")
	for _, p := range []string{decisionsPath, systemonePath} {
		status, body, _ := drive(t, p, "Bearer "+decisionsKey, decisionBody, nil)
		if status != http.StatusBadGateway || strings.Contains(body, "127.0.0.1") || strings.Contains(body, "http") {
			t.Fatalf("%s => %d %s", p, status, body)
		}
	}
}

// One org asking more than its share of the cache evicts its own answers, never
// another org's: acme's held answer survives globex filling the cache.
func TestDecisionCacheKeepsEachOrgsShare(t *testing.T) {
	fake, _ := setupDecisions(t)
	fake.answer = heldAnswer
	kai := object.KaiProvider()
	ctx := context.Background()
	recall(ctx, kai, decisionsPath, "kai", "acme", "r", []byte(decisionBody))
	recall(ctx, kai, decisionsPath, "kai", "acme", "r", []byte(decisionBody))
	for i := 0; i < decisionMax; i++ {
		b := strings.Replace(decisionBody, "twice", fmt.Sprintf("twice %d", i), 1)
		recall(ctx, kai, decisionsPath, "kai", "globex", "r", []byte(b))
	}
	before, _, _ := fake.seen()
	recall(ctx, kai, decisionsPath, "kai", "acme", "r", []byte(decisionBody))
	if after, _, _ := fake.seen(); after != before {
		t.Fatalf("globex's %d requests evicted acme's held answer", decisionMax)
	}
	decisionCache.mu.Lock()
	defer decisionCache.mu.Unlock()
	if n := decisionCache.orgs["globex"].n; n > decisionMax/decisionShare {
		t.Fatalf("globex holds %d answers, over its share of %d", n, decisionMax/decisionShare)
	}
}

// gatewayCall is a MsgType 200 request carrying headers and a body, as the ZAP
// gateway sends one.
func gatewayCall(t *testing.T, path string, headers map[string]string, body string) *zap.Message {
	t.Helper()
	h, _ := json.Marshal(headers)
	b := zap.NewBuilder(len(body) + len(h) + 256)
	obj := b.StartObject(40)
	obj.SetText(0, http.MethodPost)
	obj.SetText(8, path)
	obj.SetBytes(16, h)
	obj.SetBytes(24, []byte(body))
	obj.FinishAsRoot()
	msg, err := zap.Parse(b.FinishWithFlags(object.MsgTypeHTTPRequest << 8))
	if err != nil {
		t.Fatalf("build gateway request: %v", err)
	}
	return msg
}

// Both doors pay from, and scope handles by, the org a JWT asked to act in under
// X-Org-Id, and both refuse an org its signed membership does not cover.
func TestDecisionDoorsAgreeUnderOrgSwitch(t *testing.T) {
	fake, events := setupDecisions(t)
	var mu sync.Mutex
	var seen []string
	fake.serve = func(_ string, body []byte) (int, string) {
		var b struct{ Observe, Handle string }
		_ = json.Unmarshal(body, &b)
		mu.Lock()
		seen = append(seen, b.Observe+b.Handle)
		mu.Unlock()
		return http.StatusOK, decisionAnswer
	}
	tok := mintUsageJWTWithOrgs(t, "beta", "bob", "beta", "acme")
	observe := strings.Replace(decisionBody, `}}}`, `}},"observe":"s1"}`, 1)
	if status, body, _ := drive(t, decisionsPath, "Bearer "+tok, observe, map[string]string{"X-Org-Id": "acme"}); status != 200 {
		t.Fatalf("HTTP => %d %s", status, body)
	}
	msg, err := gateway(nil)(context.Background(), "", gatewayCall(t, decisionsPath,
		map[string]string{"Authorization": "Bearer " + tok, "X-Org-Id": "acme", "X-Request-Id": "zap-7"}, `{"model":"kai","handle":"s1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if st := msg.Root().Uint32(object.GatewayRespStatus); st != 200 {
		t.Fatalf("ZAP => %d %s", st, msg.Root().Bytes(object.GatewayRespBody))
	}
	var h map[string]string
	if json.Unmarshal(msg.Root().Bytes(object.GatewayRespHeaders), &h); h["X-Request-Id"] != "zap-7" || fake.rid != "zap-7" {
		t.Fatalf("the gateway's request id: answered %q, sent %q", h["X-Request-Id"], fake.rid)
	}
	settled(t)
	mu.Lock()
	if strings.Join(seen, " ") != "acme/s1 acme/s1" {
		t.Fatalf("the service was sent %v; both doors must name acme's handle", seen)
	}
	mu.Unlock()
	if len(*events) != 2 || (*events)[0].Namespace != "acme" || (*events)[1].Namespace != "acme" {
		t.Fatalf("debits = %+v; both doors must pay from acme", *events)
	}

	msg, _ = gateway(nil)(context.Background(), "", gatewayCall(t, decisionsPath,
		map[string]string{"Authorization": "Bearer " + tok, "X-Org-Id": "globex"}, decisionBody))
	if st := msg.Root().Uint32(object.GatewayRespStatus); st != http.StatusForbidden {
		t.Fatalf("ZAP in an org outside the membership => %d, want 403", st)
	}
}

// A debit the ledger does not take is tried again, each try bounded, and Settled
// waits long enough for every try: the answer went out, so the debit must land.
func TestDecisionDebitOutlivesAFailingLedger(t *testing.T) {
	_, _ = setupDecisions(t)
	prevTimeout, prevBackoff := usageTimeout, settleBackoff
	usageTimeout, settleBackoff = 100*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { usageTimeout, settleBackoff = prevTimeout, prevBackoff })
	if SettleBudget() <= usageTimeout*settleTries {
		t.Fatalf("SettleBudget %v does not cover %d tries of %v", SettleBudget(), settleTries, usageTimeout)
	}

	var tries atomic.Int32
	var landed atomic.Int32
	object.SetUsageRecorder(func(ctx context.Context, _ object.UsageEvent) error {
		if dl, ok := ctx.Deadline(); !ok || time.Until(dl) > usageTimeout {
			t.Errorf("a debit's hand-off is not bounded by usageTimeout")
		}
		if tries.Add(1) == 1 {
			<-ctx.Done() // the ledger does not answer
			return ctx.Err()
		}
		landed.Add(1)
		return nil
	})
	if status, _, _ := drive(t, decisionsPath, "Bearer "+decisionsKey, decisionBody, nil); status != 200 {
		t.Fatalf("status %d", status)
	}
	ctx, cancel := context.WithTimeout(context.Background(), SettleBudget())
	defer cancel()
	if err := Settled(ctx); err != nil {
		t.Fatalf("Settled gave up inside its budget: %v", err)
	}
	if tries.Load() != 2 || landed.Load() != 1 {
		t.Fatalf("tries = %d, landed = %d; want the timed-out debit tried again and landed once", tries.Load(), landed.Load())
	}
}

// However many answers go out at once, the debits behind them run on a fixed set
// of settlers, and one past the queue is filed where it was answered, not dropped.
func TestDecisionSettlersAreBounded(t *testing.T) {
	_, _ = setupDecisions(t)
	release := make(chan struct{})
	var landed atomic.Int32
	object.SetUsageRecorder(func(context.Context, object.UsageEvent) error {
		<-release
		landed.Add(1)
		return nil
	})
	before := runtime.NumGoroutine()
	rec := func() *usageRecord {
		return &usageRecord{Owner: decisionsOrg, Model: "kai", Provider: object.KaiName, PromptTokens: 1, DecisionCount: 1, Status: "success", RequestID: "r"}
	}
	const n = settleWorkers + settleDepth
	for i := 0; i < n; i++ {
		settleAfter(context.Background(), rec(), time.Now())
	}
	if grew := runtime.NumGoroutine() - before; grew > settleWorkers+8 {
		t.Fatalf("%d debits in flight grew %d goroutines; the settlers are %d", n, grew, settleWorkers)
	}
	inline := make(chan struct{})
	go func() {
		settleAfter(context.Background(), rec(), time.Now())
		close(inline)
	}()
	select {
	case <-inline:
		t.Fatal("a debit past a full queue returned before it was filed")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-inline
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := Settled(ctx); err != nil {
		t.Fatalf("%d debits did not land: %v", n+1, err)
	}
	if got := landed.Load(); got != n+1 {
		t.Fatalf("landed %d of %d debits", got, n+1)
	}
}

// A decision costs a fraction of a cent and the ledger holds cents, so each call
// holds at least one and its spend counts in nano: a balance of one cent pays for
// what it covers and no more.
func TestJevCannotOverdrawACent(t *testing.T) {
	fake, events := setupDecisions(t)
	fake.answer = `{"model":"typesafe/jev-1.13","answers":{"is_bug":{"type":"noul","noul":0.5}},"usage":{"input_tokens":110000,"output_tokens":0,"cost":0.00462}}`
	object.SetBalanceReader(balReader(1, nil))
	user, _ := providerKeyBillingUser(&object.Provider{Owner: decisionsOrg})
	subject := user.PayerSubject(decisionsOrg)
	object.GlobalBalanceLedger.SetBalance(subject, 1)
	t.Cleanup(func() { object.GlobalBalanceLedger.SetBalance(subject, 0) })
	asked := strings.Replace(decisionBody, `"model":"kai"`, `"model":"typesafe/jev-1.13"`, 1)
	ok := 0
	for i := 0; i < 20; i++ {
		if status, _, _ := drive(t, systemonePath, "Bearer "+decisionsKey, asked, nil); status == http.StatusOK {
			ok++
		}
	}
	total := 0.0
	for _, e := range *events {
		f, _ := strconv.ParseFloat(e.USD, 64)
		total += f
	}
	if ok == 0 || total > 0.01 {
		t.Fatalf("a one-cent balance admitted %d Jev calls billing $%.5f", ok, total)
	}
}

// A model asked in any case is priced, filed and forwarded as its route's id.
func TestDecisionCaseVariantsPriceAsTheirRoute(t *testing.T) {
	fake, events := setupDecisions(t)
	useCatalog(t, "../conf/models.yaml")
	fake.answer = `{"model":"x","answers":{"q":{"type":"noul","noul":0.5}},"usage":{"input_tokens":1000000,"output_tokens":0}}`
	want := map[string]string{"KAI": "0.021", "Kai": "0.021", "TYPESAFE/JEV-1.13": "0.042", "~TypeSafe/Jev-Latest": "0.042"}
	for model, usd := range want {
		body := strings.Replace(decisionBody, `"model":"kai"`, `"model":"`+model+`"`, 1)
		if status, out, _ := drive(t, systemonePath, "Bearer "+decisionsKey, body, nil); status != 200 {
			t.Fatalf("%s => %d %s", model, status, out)
		}
		_, _, sent := fake.seen()
		e := (*events)[len(*events)-1]
		if e.Model != strings.ToLower(model) || e.USD != usd || top(t, string(sent), "model") != `"`+strings.ToLower(model)+`"` {
			t.Fatalf("%s: debit %s at $%s per 1M, forwarded %s", model, e.Model, e.USD, top(t, string(sent), "model"))
		}
	}
}

// The gateway reads the body the way the service does, key for key: a second
// spelling of a field, in any case, or a key given twice, is refused before
// anything is priced or sent.
func TestDecisionBodyIsReadKeyForKey(t *testing.T) {
	fake, events := setupDecisions(t)
	seedOther(t)
	for _, body := range []string{
		strings.Replace(decisionBody, `"model":"kai"`, `"model":"typesafe/jev-1.13","MODEL":"kai"`, 1),
		strings.Replace(decisionBody, `"model":"kai"`, `"Model":"kai"`, 1),
		`{"model":"kai","handle":"s1","handle":null}`,
		`{"model":"kai","Handle":"s1"}`,
		`{"model":"kai","HANDLE":"s1","handle":null}`,
		`{"model":"kai","ſtate":"x","questions":{"q":{"type":"noul"}}}`,
		`{"model":"kai","state":"x","questions":{"a":{"type":"noul"}},"questions":{"b":{"type":"noul"}}}`,
		`{"model":"kai","state":"x","questions":{"q":{"type":"noul"}}} {"model":"typesafe/jev-1.13"}`,
	} {
		for _, p := range []string{decisionsPath, systemonePath} {
			if status, out, _ := drive(t, p, "Bearer "+otherKey, body, nil); status != http.StatusBadRequest {
				t.Errorf("%s %s => %d %s", p, body, status, out)
			}
		}
	}
	if calls, _, _ := fake.seen(); calls != 0 || len(*events) != 0 {
		t.Fatalf("an ambiguous body reached the service %d time(s) and was billed %d", calls, len(*events))
	}
}

// Jev's own request id header crosses with the rest, on HTTP and on the gateway.
func TestDecisionRelaysJevsRequestID(t *testing.T) {
	fake, _ := setupDecisions(t)
	fake.answer = joneAnswer
	fake.header = map[string]string{"X-Typesafe-Request-Id": "ts-1"}
	if _, _, c := drive(t, systemonePath, "Bearer "+decisionsKey, decisionBody, nil); replied(c, "X-Typesafe-Request-Id") != "ts-1" {
		t.Fatalf("HTTP X-Typesafe-Request-Id = %q", replied(c, "X-Typesafe-Request-Id"))
	}
	gw, _ := lookupGatewayHandler(systemonePath)
	msg, _ := gw(context.Background(), "Bearer "+decisionsKey, []byte(decisionBody))
	var h map[string]string
	_ = json.Unmarshal(msg.Root().Bytes(object.GatewayRespHeaders), &h)
	if h["X-Typesafe-Request-Id"] != "ts-1" {
		t.Fatalf("gateway headers = %v", h)
	}
}

// The usage row files under the id the caller was answered under, and keeps an id
// of its own: the caller's finds their bill, and never becomes the row's key.
func TestDecisionRowCarriesTheCallersRequestID(t *testing.T) {
	_, _ = setupDecisions(t)
	var mu sync.Mutex
	var posted []map[string]any
	commerce := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		mu.Lock()
		posted = append(posted, m)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(commerce.Close)
	object.SetUsageRecorder(nil)
	prevQueue := billingQueue
	billingQueue = util.NewBillingQueue(commerce.URL, "t")
	t.Cleanup(func() { billingQueue.Shutdown(); billingQueue = prevQueue })

	if status, _, _ := drive(t, decisionsPath, "Bearer "+decisionsKey, decisionBody, map[string]string{"X-Request-Id": "req-42"}); status != 200 {
		t.Fatalf("status %d", status)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(posted)
		mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(posted) != 1 || posted[0]["clientRequestId"] != "req-42" || posted[0]["requestId"] == "req-42" || posted[0]["requestId"] == "" {
		t.Fatalf("usage posted = %v", posted)
	}
	row := cloudUsageValues(&usageRecord{RequestID: "own", ClientRequestID: "req-42"}, time.Now())
	if row[0] != "own" {
		t.Fatalf("the row's own id is %v, want the one it minted", row[0])
	}
	shown := false
	for _, v := range row {
		if v == "req-42" {
			shown = true
		}
	}
	if !shown {
		t.Fatal("the row does not carry the caller's request id")
	}
}

// A recorder failure is an error recordUsage returns, not only a log line.
func TestRecordUsageReturnsTheLedgersRefusal(t *testing.T) {
	_, _ = setupDecisions(t)
	refusal := errors.New("ledger down")
	object.SetUsageRecorder(func(context.Context, object.UsageEvent) error { return refusal })
	rec := &usageRecord{Owner: decisionsOrg, Model: "kai", Provider: object.KaiName, PromptTokens: 10, DecisionCount: 1, Status: "success", RequestID: "r"}
	if err := recordUsage(rec); !errors.Is(err, refusal) {
		t.Fatalf("recordUsage = %v, want the recorder's refusal", err)
	}
}

// A body past the bound the gateway and the service share is refused by the gateway
// in the path's words — 422 request_too_long, under a request id — and nothing is
// sent.
func TestDecisionBodyPastTheBound(t *testing.T) {
	fake, events := setupDecisions(t)
	big := `{"model":"kai","state":"` + strings.Repeat("x", decisionBodyBytes) + `","questions":{"q":{"type":"noul"}}}`
	status, body, c := drive(t, decisionsPath, "Bearer "+decisionsKey, big, nil)
	var native struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if status != 422 || json.Unmarshal([]byte(body), &native) != nil || native.Error.Code != "request_too_long" || replied(c, "X-Request-Id") == "" {
		t.Fatalf("/v1/decisions => %d %s", status, body)
	}
	status, body, c = drive(t, systemonePath, "Bearer "+decisionsKey, big, nil)
	var jev struct {
		Detail []struct{ Type string } `json:"detail"`
	}
	if status != 422 || json.Unmarshal([]byte(body), &jev) != nil || len(jev.Detail) != 1 || jev.Detail[0].Type != "request_too_long" || replied(c, "X-Request-Id") == "" {
		t.Fatalf("/v1/systemone => %d %s", status, body)
	}
	// The bound costs a length and is the one thing asked before the credential.
	if status, _, _ := drive(t, systemonePath, "Bearer sk-nobody-issued-this", big, nil); status != 422 {
		t.Fatalf("an unauthenticated oversized body => %d, want 422", status)
	}
	gw, _ := lookupGatewayHandler(systemonePath)
	msg, _ := gw(context.Background(), "Bearer "+decisionsKey, []byte(big))
	if st := msg.Root().Uint32(object.GatewayRespStatus); st != 422 {
		t.Fatalf("ZAP => %d", st)
	}
	if calls, _, _ := fake.seen(); calls != 0 || len(*events) != 0 {
		t.Fatalf("an oversized body reached the service %d time(s), billed %d", calls, len(*events))
	}
}

// Kai's versioned id — kai- and the first 12 lowercase hex of the served weights'
// sha256 — is Kai on both paths: priced and filed as kai, sent to the service as
// asked. Anything else of that shape is an unknown model and is sent nowhere.
func TestKaiVersionedID(t *testing.T) {
	fake, events := setupDecisions(t)
	const version = "kai-0834a74f2d14"
	for _, p := range []string{decisionsPath, systemonePath} {
		asked := strings.Replace(decisionBody, `"model":"kai"`, `"model":"`+version+`"`, 1)
		status, body, _ := drive(t, p, "Bearer "+decisionsKey, asked, nil)
		if status != 200 {
			t.Fatalf("%s %s => %d %s", p, version, status, body)
		}
		_, _, sent := fake.seen()
		if string(sent) != asked {
			t.Fatalf("%s: the service was sent %s, want the body as asked", p, sent)
		}
		e := (*events)[len(*events)-1]
		if e.Model != "kai" || e.USD != nanoToUSD(42*21) {
			t.Fatalf("%s: debit %s at $%s, want kai at 42 tokens × $0.021/M", p, e.Model, e.USD)
		}
	}
	calls, _, _ := fake.seen()
	for _, model := range []string{"kai-0834a74f2d1", "kai-0834a74f2d145", "kai-0834A74F2D14", "kai-zzzzzzzzzzzz", "KAI-0834a74f2d14", "kai-", "kai-0834a74f2d1g"} {
		for _, p := range []string{decisionsPath, systemonePath} {
			body := strings.Replace(decisionBody, `"model":"kai"`, `"model":"`+model+`"`, 1)
			if status, out, _ := drive(t, p, "Bearer "+decisionsKey, body, nil); status != http.StatusBadRequest {
				t.Errorf("%s %s => %d %s", p, model, status, out)
			}
		}
	}
	if now, _, _ := fake.seen(); now != calls {
		t.Fatalf("a malformed versioned id reached the service %d time(s)", now-calls)
	}
}

// A body is read only once its sender is known, and reading it costs one pass over
// the handful of fields a decision has: a hundred thousand keys are refused at the
// first one the service does not know, and a body behind a refused credential is
// never read at all.
func TestDecisionBodyIsCheapAndReadAfterAuth(t *testing.T) {
	fake, _ := setupDecisions(t)
	var sb strings.Builder
	sb.WriteString(`{`)
	for i := 0; i < 100_000; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `"k%d":1`, i)
	}
	sb.WriteString(`}`)
	many := sb.String()
	for _, p := range []string{decisionsPath, systemonePath} {
		start := time.Now()
		status, body, _ := drive(t, p, "Bearer "+decisionsKey, many, nil)
		if took := time.Since(start); status != http.StatusBadRequest || took > 250*time.Millisecond {
			t.Fatalf("%s: 100k unknown keys => %d in %v (%s)", p, status, took, body)
		}
	}

	var reads atomic.Int32
	prev := readModel
	readModel = func(path string, body []byte) (string, string, *decisionRefusal) {
		reads.Add(1)
		return prev(path, body)
	}
	t.Cleanup(func() { readModel = prev })
	for _, p := range []string{decisionsPath, systemonePath} {
		if status, _, _ := drive(t, p, "Bearer sk-nobody-issued-this", many, nil); status != http.StatusUnauthorized {
			t.Fatalf("%s: unauthenticated => %d, want 401", p, status)
		}
	}
	gw, _ := lookupGatewayHandler(decisionsPath)
	if msg, _ := gw(context.Background(), "Bearer sk-nobody-issued-this", []byte(many)); msg.Root().Uint32(object.GatewayRespStatus) != 401 {
		t.Fatal("ZAP: unauthenticated was not 401")
	}
	if n := reads.Load(); n != 0 {
		t.Fatalf("a body behind a refused credential was read %d time(s)", n)
	}
	if calls, _, _ := fake.seen(); calls != 0 {
		t.Fatalf("the service saw %d call(s)", calls)
	}
}

// A decision over a handle bills the questions observed with it, not its few-byte
// body, so it is held at what its handle last billed: a balance that cannot cover
// that is refused before the service is asked.
func TestHandleCallHoldsWhatItsHandleBills(t *testing.T) {
	fake, events := setupDecisions(t)
	fake.answer = `{"id":"dec_1","model":"kai","provider":"Hanzo","answers":{"q":{"type":"noul","noul":0.5}},"usage":{"input_tokens":4000000,"output_tokens":0},"routing":{"backend":"kai","checkpoint":"k","reason":"r"},"state_hash":"s","latency_ms":1}`
	observe := strings.Replace(decisionBody, `}}}`, `}},"observe":"big"}`, 1)
	if status, body := driveDecisions(t, "Bearer "+decisionsKey, observe); status != 200 {
		t.Fatalf("observe => %d %s", status, body)
	}
	// 4M tokens at $0.021/M is 8.4¢; a 1¢ balance cannot hold a call on this handle.
	user, _ := providerKeyBillingUser(&object.Provider{Owner: decisionsOrg})
	object.GlobalBalanceLedger.SetBalance(user.PayerSubject(decisionsOrg), 1)
	calls, _, _ := fake.seen()
	if status, body := driveDecisions(t, "Bearer "+decisionsKey, `{"model":"kai","handle":"big"}`); status != http.StatusPaymentRequired {
		t.Fatalf("a handle billing 8.4¢ against a 1¢ balance => %d %s", status, body)
	}
	if now, _, _ := fake.seen(); now != calls || len(*events) != 1 {
		t.Fatalf("the refused handle call reached the service or was billed")
	}
	// Another org's handle of the same name is its own: it holds a cent.
	if handled.cost(otherOrg, "big") != 0 || handled.cost(decisionsOrg, "big") != 4_000_000 {
		t.Fatal("a handle's cost is not its own org's")
	}
}

// A record's debit carries one Ref however many times it is filed, and two records
// never share one: the key a host dedups a re-sent debit on, minted here.
func TestDebitRefIsStablePerRecord(t *testing.T) {
	_, _ = setupDecisions(t)
	var refs []string
	fail := true
	object.SetUsageRecorder(func(_ context.Context, u object.UsageEvent) error {
		refs = append(refs, u.Ref)
		if fail {
			fail = false
			return errors.New("answer lost")
		}
		return nil
	})
	rec := &usageRecord{Owner: decisionsOrg, Model: "kai", Provider: object.KaiName, PromptTokens: 10, DecisionCount: 1, Status: "success", RequestID: "same"}
	_ = recordUsage(rec)
	_ = recordUsage(rec)
	other := *rec
	other.ref = ""
	_ = recordUsage(&other)
	if len(refs) != 3 || refs[0] == "" || refs[0] != refs[1] || refs[2] == refs[0] {
		t.Fatalf("refs = %v; want one ref across a record's retries and another for another record", refs)
	}
}

// A handle id is 1 to 128 characters of A-Z, a-z, 0-9, '.', '_' and '-'. Anything
// else, a megabyte of id included, is refused 422 in the path's words under a request
// id on both doors, before the service is asked, billed or remembered; an id at the
// bound is served and remembered under a fixed-size key.
func TestHandleIDIsBounded(t *testing.T) {
	fake, events := setupDecisions(t)
	observe := func(id string) string {
		q, _ := json.Marshal(id)
		return strings.Replace(decisionBody, `}}}`, `}},"observe":`+string(q)+`}`, 1)
	}
	var native struct {
		Error struct {
			Code    int
			Message string
		} `json:"error"`
	}
	for i := 0; i < 48; i++ {
		status, body, c := drive(t, decisionsPath, "Bearer "+decisionsKey, observe(fmt.Sprintf("%d-", i)+strings.Repeat("A", 1<<20)), nil)
		if status != 422 || json.Unmarshal([]byte(body), &native) != nil || native.Error.Code != 422 || replied(c, "X-Request-Id") == "" {
			t.Fatalf("a 1 MiB id => %d %.200s", status, body)
		}
	}
	for _, id := range []string{"", strings.Repeat("a", handleIDBytes+1), "acme/s1", "a b", "é", "a\x00b", "a%2Fb", "a\nb"} {
		if status, body := driveDecisions(t, "Bearer "+decisionsKey, observe(id)); status != 422 {
			t.Errorf("observe %q => %d %.200s", id, status, body)
		}
		q, _ := json.Marshal(id)
		if status, body := driveDecisions(t, "Bearer "+decisionsKey, `{"model":"kai","handle":`+string(q)+`}`); status != 422 {
			t.Errorf("handle %q => %d %.200s", id, status, body)
		}
	}
	gw, _ := lookupGatewayHandler(decisionsPath)
	msg, _ := gw(context.Background(), "Bearer "+decisionsKey, []byte(observe(strings.Repeat("A", 1<<20))))
	if st := msg.Root().Uint32(object.GatewayRespStatus); st != 422 {
		t.Fatalf("ZAP: a 1 MiB id => %d", st)
	}
	handled.mu.Lock()
	held := len(handled.m)
	handled.mu.Unlock()
	if calls, _, _ := fake.seen(); calls != 0 || len(*events) != 0 || held != 0 {
		t.Fatalf("refused ids reached the service %d time(s), billed %d, remembered %d", calls, len(*events), held)
	}

	id := strings.Repeat("Az09._-", handleIDBytes/7) + "xy"
	if status, body := driveDecisions(t, "Bearer "+decisionsKey, observe(id)); status != 200 {
		t.Fatalf("an id of %d bytes => %d %s", len(id), status, body)
	}
	if _, _, sent := fake.seen(); top(t, string(sent), "observe") != `"`+decisionsOrg+"/"+id+`"` {
		t.Fatalf("the service was sent %s", sent)
	}
	if handled.cost(decisionsOrg, id) != 42 {
		t.Fatal("an id at the bound was not remembered")
	}
}

// gzipped is s as a gzip body.
func gzipped(s string) string {
	var b bytes.Buffer
	w, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
	_, _ = w.Write([]byte(s))
	_ = w.Close()
	return b.String()
}

// A compressed decision body is decoded once, after its credential, and never past
// the bound: an unauthenticated caller's 64 MiB of gzip is refused 401 without being
// decoded, an authenticated one inflating past 16 MiB is 422 request_too_long in
// the path's words, a coding nobody decodes is 415, and a small one is served as the
// JSON it decodes to.
func TestDecisionBodyIsDecodedOnceAfterItsCredential(t *testing.T) {
	fake, events := setupDecisions(t)
	gz := map[string]string{"Content-Encoding": "gzip"}

	var reads atomic.Int32
	prev := readModel
	readModel = func(path string, body []byte) (string, string, *decisionRefusal) {
		reads.Add(1)
		return prev(path, body)
	}
	t.Cleanup(func() { readModel = prev })
	bomb := gzipped(`{"state":1,` + strings.Repeat(" ", 64<<20) + `"model":"kai"}`)
	for _, p := range []string{decisionsPath, systemonePath} {
		var a, b runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&a)
		status, body, _ := drive(t, p, "Bearer sk-nobody-issued-this", bomb, gz)
		runtime.ReadMemStats(&b)
		if status != http.StatusUnauthorized {
			t.Fatalf("%s: unauthenticated gzip => %d %s", p, status, body)
		}
		if alloc := b.TotalAlloc - a.TotalAlloc; alloc > 2<<20 {
			t.Fatalf("%s: refusing %d KiB of unauthenticated gzip allocated %d MiB: it was decoded", p, len(bomb)>>10, alloc>>20)
		}
	}
	if n := reads.Load(); n != 0 {
		t.Fatalf("a body behind a refused credential was read %d time(s)", n)
	}

	over := gzipped(`{"model":"kai","state":"` + strings.Repeat("x", decisionBodyBytes+1024) + `","questions":{"q":{"type":"noul"}}}`)
	status, body, c := drive(t, decisionsPath, "Bearer "+decisionsKey, over, gz)
	var native struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if status != 422 || json.Unmarshal([]byte(body), &native) != nil || native.Error.Code != "request_too_long" || replied(c, "X-Request-Id") == "" {
		t.Fatalf("/v1/decisions gzip past the bound => %d %s", status, body)
	}
	status, body, c = drive(t, systemonePath, "Bearer "+decisionsKey, over, gz)
	var jev struct {
		Detail []struct{ Type string } `json:"detail"`
	}
	if status != 422 || json.Unmarshal([]byte(body), &jev) != nil || len(jev.Detail) != 1 || jev.Detail[0].Type != "request_too_long" || replied(c, "X-Request-Id") == "" {
		t.Fatalf("/v1/systemone gzip past the bound => %d %s", status, body)
	}
	for coding, want := range map[string]int{"compress": 415, "gzip, gzip": 415, "gzip": 400} {
		sent := gzipped(decisionBody)
		if coding == "gzip" {
			sent = "not gzip at all"
		}
		if status, body, _ := drive(t, decisionsPath, "Bearer "+decisionsKey, sent, map[string]string{"Content-Encoding": coding}); status != want {
			t.Errorf("Content-Encoding %q => %d %s, want %d", coding, status, body, want)
		}
	}
	if calls, _, _ := fake.seen(); calls != 0 || len(*events) != 0 {
		t.Fatalf("a refused body reached the service %d time(s), billed %d", calls, len(*events))
	}

	if status, body, _ := drive(t, decisionsPath, "Bearer "+decisionsKey, gzipped(decisionBody), gz); status != 200 {
		t.Fatalf("a gzip decision => %d %s", status, body)
	}
	if _, _, sent := fake.seen(); string(sent) != decisionBody || len(*events) != 1 {
		t.Fatalf("the service was sent %q, billed %d", sent, len(*events))
	}
}
