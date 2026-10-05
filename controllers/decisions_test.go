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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/ai/object"
)

// POST /v1/decisions against a fake decision service. The vehicle is an sk-
// provider key: the one credential that carries a billable principal with no
// IAM round trip, exactly as the chat money-path suite drives it.

const (
	decisionsKey = "sk-decisions-funded"
	decisionsOrg = "acme"
)

// decisionAnswer is a Decisions response as the service writes it.
const decisionAnswer = `{"id":"dec_1","model":"kai","provider":"Hanzo","answers":{"is_bug":{"type":"noul","noul":0.96,"answer_confidence":0.96}},"usage":{"input_tokens":42,"output_tokens":0,"cost":0.00002},"routing":{"backend":"kai","checkpoint":"hanzoai/kai","reason":"explicit model='kai'"},"state_hash":"sha256:00","latency_ms":1.5}`

const decisionBody = `{"model":"kai","state":{"message":"Payment failed twice"},"questions":{"is_bug":{"type":"noul","instructions":"Is this a bug?"}}}`

var decisionsSeq atomic.Int64

// fakeDecisions is the decision service: it records what reached it and answers
// with status, body and header — or, when answer is set, with what it says.
type fakeDecisions struct {
	mu    sync.Mutex
	calls int
	path  string
	body  []byte
	rid   string
	org   string // X-Org-Id
	set   string // X-Org-Capabilities
	// capture is the X-Capture header the last call carried.
	capture string
	status  int
	answer  string
	header  map[string]string
	serve   func(path string, body []byte) (int, string)
}

func (f *fakeDecisions) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls++
	f.path, f.body, f.rid, f.org = r.URL.Path, b, r.Header.Get("X-Request-Id"), r.Header.Get("X-Org-Id")
	f.capture = r.Header.Get("X-Capture")
	f.set = r.Header.Get("X-Org-Capabilities")
	status, answer, serve := f.status, f.answer, f.serve
	for k, v := range f.header {
		w.Header().Set(k, v)
	}
	f.mu.Unlock()
	if serve != nil {
		status, answer = serve(r.URL.Path, b)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(answer))
}

func (f *fakeDecisions) seen() (int, string, []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.path, f.body
}

// settled waits for every debit filed after its reply.
func settled(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := Settled(ctx); err != nil {
		t.Fatalf("debits filed after their reply did not land: %v", err)
	}
}

// setupDecisions installs a funded provider-key principal, a fake decision
// service at KAI_URL, and a recorder for every debit, and restores all of it.
func setupDecisions(t *testing.T) (*fakeDecisions, *[]object.UsageEvent) {
	t.Helper()
	forget := func() {
		decisionCache.mu.Lock()
		decisionCache.lru, decisionCache.m, decisionCache.orgs, decisionCache.rev, decisionCache.size = nil, nil, nil, nil, 0
		decisionCache.mu.Unlock()
	}
	forget()
	t.Cleanup(forget)
	handled.mu.Lock()
	handled.orgs = nil
	handled.mu.Unlock()
	dsn := fmt.Sprintf("file:decisions_%d?mode=memory&cache=shared", decisionsSeq.Add(1))
	restore, err := object.UseMemoryDB(dsn, &object.Provider{})
	if err != nil {
		t.Fatalf("UseMemoryDB: %v", err)
	}
	t.Cleanup(restore)
	if _, err := object.AddProvider(&object.Provider{
		Owner: decisionsOrg, Name: "acme-openai", Category: "Model", Type: "OpenAI",
		ProviderKey: decisionsKey, ProviderUrl: "http://127.0.0.1:1/v1",
		ClientSecret: "upstream-secret", State: "Active",
	}); err != nil {
		t.Fatalf("seed provider: %v", err)
	}

	fake := &fakeDecisions{status: http.StatusOK, answer: decisionAnswer}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	t.Setenv("KAI_URL", srv.URL)
	t.Setenv("KAI_API_KEY", "")

	prevBalance := object.BalanceReader()
	object.SetBalanceReader(balReader(100_00, nil))
	t.Cleanup(func() { object.SetBalanceReader(prevBalance) })
	// The in-process ledger outlives a test; each one starts both tenants funded.
	for _, org := range []string{decisionsOrg, "globex"} {
		user, _ := providerKeyBillingUser(&object.Provider{Owner: org})
		object.GlobalBalanceLedger.SetBalance(user.PayerSubject(org), 100_00)
	}

	var mu sync.Mutex
	events := &[]object.UsageEvent{}
	prevUsage := object.UsageRecorder()
	object.SetUsageRecorder(func(_ context.Context, u object.UsageEvent) error {
		mu.Lock()
		defer mu.Unlock()
		*events = append(*events, u)
		return nil
	})
	t.Cleanup(func() { settled(t); object.SetUsageRecorder(prevUsage) })
	return fake, events
}

// driveDecisions runs the real Decisions handler, waits for the debit it filed
// after replying, and returns its status and body.
func driveDecisions(t *testing.T, authorization, body string) (int, string) {
	t.Helper()
	status, body, _ := drive(t, authorization, body, nil)
	return status, body
}

// drive runs the Decisions handler with the given request headers, waits for the
// debit it filed after replying, and returns its status, body and reply.
func drive(t *testing.T, authorization, body string, header map[string]string) (int, string, *ApiController) {
	t.Helper()
	c := presenting(visit(http.MethodPost, decisionsPath), authorization)
	for k, v := range header {
		c.Fiber().Request().Header.Set(k, v)
	}
	c.Fiber().Request().SetBody([]byte(body))
	status := answering(t, c, c.Decisions)
	settled(t)
	return status, sent(c), c
}

// replied is a header the handler set on its reply.
func replied(c *ApiController, name string) string {
	return string(c.Fiber().Response().Header.Peek(name))
}

// The body reaches the service unchanged, the answer comes back unchanged, and
// the call is debited once, at $0.021 per million input tokens. Output is free.
func TestDecisionsForwardsAndMeters(t *testing.T) {
	fake, events := setupDecisions(t)

	status, body := driveDecisions(t, "Bearer "+decisionsKey, decisionBody)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", status, body)
	}
	if body != decisionAnswer {
		t.Fatalf("answer changed on the way back:\n got %s\nwant %s", body, decisionAnswer)
	}
	calls, path, sentBody := fake.seen()
	if calls != 1 || path != "/v1/decisions" {
		t.Fatalf("service saw %d call(s) at %q, want 1 at /v1/decisions", calls, path)
	}
	if string(sentBody) != decisionBody {
		t.Fatalf("body changed on the way out:\n got %s\nwant %s", sentBody, decisionBody)
	}

	if len(*events) != 1 {
		t.Fatalf("debits = %d, want exactly 1", len(*events))
	}
	e := (*events)[0]
	if e.Model != "kai" || e.Provider != object.KaiName {
		t.Fatalf("debit names %s/%s, want kai/kai", e.Provider, e.Model)
	}
	if e.USD != nanoToUSD(42*21) {
		t.Fatalf("debit = $%s, want 42 input tokens at $0.021/M", e.USD)
	}
	if e.Namespace != decisionsOrg {
		t.Fatalf("debit lands on %q, want the key's org %q", e.Namespace, decisionsOrg)
	}
}

// A model whose route names another id upstream reaches the service under that id,
// and the answer and the debit name the model asked for, at its own row. Its alias
// is neither listed nor a route of its own: it names that model.
func TestDecisionsServeTheModelAskedFor(t *testing.T) {
	fake, events := setupDecisions(t)
	path := filepath.Join(t.TempDir(), "models.yaml")
	catalog := `version: 1
models:
  hanzo/kai:
    provider: kai
    upstream: kai
    owned_by: hanzo
    outputs: [decision]
    pricing: {input: 0.021, output: 0}
  hanzoai/kai:
    alias_of: hanzo/kai
`
	if err := os.WriteFile(path, []byte(catalog), 0o600); err != nil {
		t.Fatal(err)
	}
	useCatalog(t, path)

	if id, ok := Canonical("HanzoAI/Kai"); !ok || id != "hanzo/kai" {
		t.Fatalf("Canonical(hanzoai/kai) = %q, %v; want hanzo/kai", id, ok)
	}
	if id, ok := Canonical("hanzo/kai"); ok {
		t.Fatalf("hanzo/kai is not an alias, yet names %q", id)
	}
	if r := resolveModelRoute("hanzoai/kai"); r != nil {
		t.Fatalf("an alias is a route of its own: %+v", r)
	}
	listed := map[string]bool{}
	for _, m := range listAvailableModels() {
		listed[m.ID] = true
	}
	if !listed["hanzo/kai"] || listed["hanzoai/kai"] {
		t.Fatalf("listed hanzo/kai=%v hanzoai/kai=%v, want the model listed and its alias not", listed["hanzo/kai"], listed["hanzoai/kai"])
	}

	asked := strings.Replace(decisionBody, `"model":"kai"`, `"model":"hanzo/kai"`, 1)
	status, body := driveDecisions(t, "Bearer "+decisionsKey, asked)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", status, body)
	}
	var answer, served map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &answer); err != nil || string(answer["model"]) != `"hanzo/kai"` {
		t.Fatalf("answer names %s, want hanzo/kai (body %s)", answer["model"], body)
	}
	var sent, in map[string]json.RawMessage
	_, _, raw := fake.seen()
	if err := json.Unmarshal(raw, &sent); err != nil || string(sent["model"]) != `"kai"` {
		t.Fatalf("service was sent %s, want kai", raw)
	}
	_ = json.Unmarshal([]byte(asked), &in)
	_ = json.Unmarshal([]byte(decisionAnswer), &served)
	for _, k := range []string{"state", "questions"} {
		if string(sent[k]) != string(in[k]) {
			t.Fatalf("%s changed on the way out: %s", k, sent[k])
		}
	}
	for _, k := range []string{"answers", "usage", "routing"} {
		if string(answer[k]) != string(served[k]) {
			t.Fatalf("%s changed on the way back: %s", k, answer[k])
		}
	}

	if len(*events) != 1 {
		t.Fatalf("debits = %d, want exactly 1", len(*events))
	}
	if e := (*events)[0]; e.Model != "hanzo/kai" || e.USD != nanoToUSD(42*21) {
		t.Fatalf("debit = %s at $%s, want hanzo/kai at 42 input tokens × $0.021/M", e.Model, e.USD)
	}
}

// The row carries the answer's usage: input tokens set the price, output tokens
// do not, and the stated upstream cost is COGS.
func TestDecisionRecordMetersUsage(t *testing.T) {
	cost := 0.00002
	u := decisionsUsage{InputTokens: 42, OutputTokens: 3, Cost: &cost}
	var answer decisionsResponse
	if err := json.Unmarshal([]byte(decisionAnswer), &answer); err != nil {
		t.Fatalf("the declared response does not read the service's answer: %v", err)
	}
	if answer.Usage.InputTokens != 42 || answer.Usage.Cost == nil || *answer.Usage.Cost != cost {
		t.Fatalf("usage read from the answer = %+v", answer.Usage)
	}

	rec := &usageRecord{Model: "kai", Provider: object.KaiName, PromptTokens: u.InputTokens,
		CompletionTokens: u.OutputTokens, DecisionCount: 1}
	want := int64(u.InputTokens) * 21
	if got := usageCostNano(rec); got != want {
		t.Fatalf("cost = %d nano, want %d (42 input tokens at $0.021/M)", got, want)
	}
	rec.CompletionTokens = 1_000_000
	if got := usageCostNano(rec); got != want {
		t.Fatalf("output tokens moved the price to %d nano", got)
	}
	rec.PromptTokens = 1_000_000
	rec.CompletionTokens = u.OutputTokens
	if got := usageCostNano(rec); got != 21_000_000 {
		t.Fatalf("a million input tokens cost %d nano, want $0.021", got)
	}
	rec.PromptTokens = u.InputTokens
	if recordUnpriced(rec) {
		t.Fatal("a decision is priced on its input tokens; it must not read as unpriced")
	}
	stated := usdToNano(cost)
	rec.CostNanoExact = &stated
	if m := usageMargin(rec); m.MarginNano == nil || *m.MarginNano != want-stated {
		t.Fatalf("margin = %v, want price − stated cost", m.MarginNano)
	}
}

// A decision is priced from the model price table like every model: the row the
// loaded config states, a live catalog refresh included, sets the price. Jev is a
// row like Kai's, at its list price.
func TestDecisionPriceIsTheTableRow(t *testing.T) {
	prev := globalModelConfig
	globalModelConfig = &ModelConfig{
		routes:   map[string]modelRoute{},
		pricing:  map[string]modelPrice{"kai": {InputPerMillion: 0.042}, "typesafe/jev-1.13": {InputPerMillion: 0.084}},
		defaults: modelPrice{InputPerMillion: 1, OutputPerMillion: 4},
		stopCh:   make(chan struct{}),
	}
	t.Cleanup(func() { globalModelConfig = prev })

	rec := &usageRecord{Model: "kai", Provider: object.KaiName, PromptTokens: 1000, CompletionTokens: 50, DecisionCount: 1}
	if got := usageCostNano(rec); got != 1000*42 {
		t.Fatalf("cost = %d nano, want 1000 input tokens at the table's $0.042/M", got)
	}
	if recordUnpriced(rec) {
		t.Fatal("kai has a row in the table; it must not read as unpriced")
	}
	rec.Model = "typesafe/jev-1.13"
	if got := usageCostNano(rec); got != 1000*84 {
		t.Fatalf("jev = %d nano, want 1000 input tokens at the table's $0.084/M", got)
	}
	if recordUnpriced(rec) {
		t.Fatal("jev has a row in the table; it must not read as unpriced")
	}
	rec.Model = "laya"
	if !recordUnpriced(rec) {
		t.Fatal("a decision model with no row reads as unpriced")
	}
}

// The shipped catalog and the static table say one price each: Kai $0.021 and Jev
// its list $0.042 per million input tokens, output free.
func TestDecisionPricesAgree(t *testing.T) {
	want := map[string]float64{"kai": 0.021, "typesafe/jev-1.13": 0.042, "~typesafe/jev-latest": 0.042}
	for model, in := range want {
		if p := modelPricing[model]; p.InputPerMillion != in || p.OutputPerMillion != 0 {
			t.Errorf("static %s = %+v, want $%v/M in, free out", model, p, in)
		}
	}
	useCatalog(t, "../conf/models.yaml")
	for model, in := range want {
		if p := getModelPrice(model); p.InputPerMillion != in || p.OutputPerMillion != 0 {
			t.Errorf("models.yaml %s = %+v, want $%v/M in, free out", model, p, in)
		}
		rec := &usageRecord{Model: model, Provider: object.KaiName, PromptTokens: 1_000_000, DecisionCount: 1}
		if got := usageCostNano(rec); got != int64(in*1e9) {
			t.Errorf("%s: a million input tokens bill %d nano, want $%v", model, got, in)
		}
	}
}

// No credential, or one nobody issued, is 401 — and nothing reaches the service.
func TestDecisionsAuthRequired(t *testing.T) {
	fake, events := setupDecisions(t)
	for _, auth := range []string{"", "Bearer ", "Bearer sk-nobody-issued-this", "Bearer pk-publishable"} {
		status, _ := driveDecisions(t, auth, decisionBody)
		if status != http.StatusUnauthorized && status != http.StatusForbidden {
			t.Errorf("%q => %d, want 401/403", auth, status)
		}
	}
	if calls, _, _ := fake.seen(); calls != 0 {
		t.Fatalf("service saw %d call(s) from unauthenticated callers", calls)
	}
	if len(*events) != 0 {
		t.Fatalf("unauthenticated callers were debited %d time(s)", len(*events))
	}
}

// A model this endpoint does not publish is refused in the service's error shape,
// naming what it does serve, without a call. Laya is the benchmark baseline the
// service can load; it is not published, so it is unknown here.
func TestDecisionsUnknownModel(t *testing.T) {
	fake, events := setupDecisions(t)
	for _, model := range []string{"laya", "laya-agent", "gpt-4o", "zen5", "bge-m3", "zen-scribe"} {
		status, body := driveDecisions(t, "Bearer "+decisionsKey,
			`{"model":"`+model+`","state":"x","questions":{"q":{"type":"noul","instructions":"?"}}}`)
		if status != http.StatusBadRequest {
			t.Fatalf("%s => %d, want 400", model, status)
		}
		want := string(decisionsFailure(400, fmt.Sprintf("unknown model %q; use one of kai, typesafe/jev-1.13, ~typesafe/jev-latest", model)))
		if body != want {
			t.Fatalf("%s refusal:\n got %s\nwant %s", model, body, want)
		}
	}
	// A body with no model says so, in the same shape.
	if status, body := driveDecisions(t, "Bearer "+decisionsKey, `{"state":"x"}`); status != 400 ||
		body != `{"error":{"code":400,"message":"the request needs a 'model'"}}` {
		t.Fatalf("no model => %d %s", status, body)
	}
	// Authentication still comes first.
	if status, _ := driveDecisions(t, "Bearer sk-nobody-issued-this", `{"model":"laya"}`); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated unknown model => %d, want 401", status)
	}
	if calls, _, _ := fake.seen(); calls != 0 {
		t.Fatalf("service saw %d call(s) for models it must never be sent", calls)
	}
	if len(*events) != 0 {
		t.Fatalf("refused calls were debited %d time(s)", len(*events))
	}
}

// The service's own refusal comes back unchanged and is not charged.
func TestDecisionsServiceRefusalPassesThrough(t *testing.T) {
	fake, events := setupDecisions(t)
	fake.status = http.StatusUnprocessableEntity
	fake.answer = `{"error":{"code":422,"message":"question 'c' options exceed head_max_len=16"}}`

	status, body := driveDecisions(t, "Bearer "+decisionsKey, decisionBody)
	if status != http.StatusUnprocessableEntity || body != fake.answer {
		t.Fatalf("refusal => %d %s, want 422 %s", status, body, fake.answer)
	}
	if len(*events) != 0 {
		t.Fatalf("a refused decision was debited %d time(s)", len(*events))
	}
}

// With no decision service configured the answer is a 503 in the service's shape.
func TestDecisionsUnconfigured(t *testing.T) {
	setupDecisions(t)
	t.Setenv("KAI_URL", "")
	status, body := driveDecisions(t, "Bearer "+decisionsKey, decisionBody)
	if status != http.StatusServiceUnavailable ||
		body != `{"error":{"code":503,"message":"the decision service is not configured"}}` {
		t.Fatalf("unconfigured => %d %s", status, body)
	}
}

// The ZAP twin: 401 with no credential, the same refusal for an unknown model, and
// the answer forwarded unchanged.
func TestZapDecisions(t *testing.T) {
	fake, _ := setupDecisions(t)
	if _, ok := lookupGatewayHandler("/v1/decisions"); !ok {
		t.Fatal("gateway path /v1/decisions not registered")
	}
	if _, ok := lookupCloudHandler("decisions"); !ok {
		t.Fatal("cloud method \"decisions\" not registered")
	}

	zapDecisions, _ := lookupCloudHandler("decisions")
	msg, err := zapDecisions(context.Background(), "", []byte(decisionBody))
	if err != nil {
		t.Fatal(err)
	}
	if status, _, _ := cloudRespStatus(t, msg); status != 401 {
		t.Fatalf("no credential => %d, want 401", status)
	}

	msg, _ = zapDecisions(context.Background(), "Bearer "+decisionsKey, []byte(`{"model":"laya"}`))
	status, body, _ := cloudRespStatus(t, msg)
	if status != 400 || string(body) != string(decisionsFailure(400, `unknown model "laya"; use one of kai, typesafe/jev-1.13, ~typesafe/jev-latest`)) {
		t.Fatalf("unknown model => %d %s", status, body)
	}

	msg, _ = zapDecisions(context.Background(), "Bearer sk-nobody-issued-this", []byte(`{"model":"laya"}`))
	if status, _, _ := cloudRespStatus(t, msg); status != 401 {
		t.Fatalf("unauthenticated unknown model => %d, want 401", status)
	}

	msg, _ = zapDecisions(context.Background(), "Bearer "+decisionsKey, []byte(decisionBody))
	status, body, errText := cloudRespStatus(t, msg)
	if status != 200 || string(body) != decisionAnswer {
		t.Fatalf("forward => %d %s (%s)", status, body, errText)
	}
	if calls, _, sentBody := fake.seen(); calls != 1 || string(sentBody) != decisionBody {
		t.Fatalf("service saw %d call(s), body %s", calls, sentBody)
	}
}

// The shipped catalog publishes kai as a decision model, keeps the Jev ids
// callable and unlisted, and the static fallback agrees.
func TestDecisionsCatalog(t *testing.T) {
	want := []string{"kai", "typesafe/jev-1.13", "~typesafe/jev-latest"}
	if got := decisionModels(); !slices.Equal(got, want) {
		t.Fatalf("static decision models = %v, want %v", got, want)
	}

	useCatalog(t, "../conf/models.yaml")
	if got := decisionModels(); !slices.Equal(got, want) {
		t.Fatalf("catalog decision models = %v, want %v", got, want)
	}
	listed := map[string]modelInfo{}
	for _, m := range listAvailableModels() {
		listed[m.ID] = m
	}
	kai, ok := listed["kai"]
	if !ok {
		t.Fatal("kai is not in /v1/models")
	}
	if !slices.Equal(kai.Outputs, []string{"decision"}) || kai.OwnedBy != "hanzo" {
		t.Fatalf("kai lists as outputs=%v owned_by=%q, want [decision] hanzo", kai.Outputs, kai.OwnedBy)
	}
	for _, id := range want[1:] {
		if _, ok := listed[id]; ok {
			t.Errorf("%s is listed; it is callable, not published", id)
		}
	}
	for _, id := range []string{"laya", "laya-multilingual", "laya-agent"} {
		if r := resolveModelRoute(id); r != nil && r.providerName == object.KaiName {
			t.Errorf("%s routes to the decision service; the baseline is never published", id)
		}
	}
}
