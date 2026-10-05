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
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/ai/object"
)

// A second tenant, with a key of its own.
const (
	otherKey = "sk-decisions-globex"
	otherOrg = "globex"
)

func seedOther(t *testing.T) {
	t.Helper()
	if _, err := object.AddProvider(&object.Provider{
		Owner: otherOrg, Name: "globex-openai", Category: "Model", Type: "OpenAI",
		ProviderKey: otherKey, ProviderUrl: "http://127.0.0.1:1/v1",
		ClientSecret: "upstream-secret", State: "Active",
	}); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
}

// heldAnswer is an answer Kai gave in process: routing names the weights.
const heldAnswer = `{"id":"dec_1","model":"kai","provider":"Hanzo","answers":{"is_bug":{"type":"noul","noul":0.96,"answer_confidence":0.96}},"usage":{"input_tokens":42,"output_tokens":0},"routing":{"backend":"kai","checkpoint":"hanzoai/kai","sha256":"abc","reason":"explicit model='kai'"},"state_hash":"sha256:00","latency_ms":1.5}`

// jevAnswer is an answer Jev gave, as the service relays it.
const jevAnswer = `{"id":"dec_2","model":"typesafe/jev-1.13","provider":"OpenRouter","answers":{"is_bug":{"type":"noul","noul":0.96}},"usage":{"input_tokens":42,"output_tokens":0}}`

// top reads one top-level field of a JSON body.
func top(t *testing.T, body, key string) string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("not a JSON object: %s", body)
	}
	return string(m[key])
}

// A handle belongs to the org that observed it: the service is sent every id
// named by the org that pays, so one org's id reaches only its own states, and the
// org never appears in what comes back.
func TestDecisionHandlesBelongToTheirOrg(t *testing.T) {
	fake, _ := setupDecisions(t)
	seedOther(t)
	var mu sync.Mutex
	observed := map[string]bool{}
	var sent []string
	fake.serve = func(_ string, body []byte) (int, string) {
		var b struct{ Observe, Handle string }
		_ = json.Unmarshal(body, &b)
		mu.Lock()
		defer mu.Unlock()
		if b.Observe != "" {
			sent = append(sent, "observe "+b.Observe)
			observed[b.Observe] = true
			return http.StatusOK, decisionAnswer
		}
		sent = append(sent, "handle "+b.Handle)
		if !observed[b.Handle] {
			q, _ := json.Marshal(b.Handle)
			return http.StatusBadRequest, `{"error":{"code":400,"message":"no state observed as ` + strings.ReplaceAll(string(q), `"`, `\"`) + `"}}`
		}
		return http.StatusOK, decisionAnswer
	}

	observe := `{"model":"kai","state":"Payment failed twice","questions":{"is_bug":{"type":"noul"}},"observe":"s1"}`
	if status, body := driveDecisions(t, "Bearer "+decisionsKey, observe); status != http.StatusOK {
		t.Fatalf("observe => %d %s", status, body)
	}
	status, body := driveDecisions(t, "Bearer "+otherKey, `{"model":"kai","handle":"s1"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("another org decided over acme's handle: %d %s", status, body)
	}
	if strings.Contains(body, otherOrg) || !strings.Contains(body, `\"s1\"`) {
		t.Fatalf("the refusal names the org, or not the id asked for: %s", body)
	}
	// An id cannot spell another org's name: a slash is outside the id's alphabet.
	if status, body := driveDecisions(t, "Bearer "+otherKey, `{"model":"kai","handle":"acme/s1"}`); status != http.StatusUnprocessableEntity {
		t.Fatalf("an id spelling another org's name was not refused: %d %s", status, body)
	}
	if status, body := driveDecisions(t, "Bearer "+decisionsKey, `{"model":"kai","handle":"s1"}`); status != http.StatusOK {
		t.Fatalf("the org that observed s1 could not decide over it: %d %s", status, body)
	}

	want := []string{"observe acme/s1", "handle globex/s1", "handle acme/s1"}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(sent, "; ") != strings.Join(want, "; ") {
		t.Fatalf("the service was sent\n  %s\nwant\n  %s", strings.Join(sent, "; "), strings.Join(want, "; "))
	}
}

// A handle that is not a string is refused before anything is sent.
func TestDecisionHandleIsAString(t *testing.T) {
	fake, _ := setupDecisions(t)
	status, body := driveDecisions(t, "Bearer "+decisionsKey, `{"model":"kai","handle":{"org":"globex","id":"s1"}}`)
	if status != http.StatusBadRequest || top(t, body, "error") == "" {
		t.Fatalf("object handle => %d %s", status, body)
	}
	if calls, _, _ := fake.seen(); calls != 0 {
		t.Fatalf("service saw %d call(s)", calls)
	}
}

// The reply does not wait on the books: a debit that takes 400 ms to land is filed
// after the answer has been handed back, and it still lands, once.
func TestDecisionSettlesAfterReply(t *testing.T) {
	_, _ = setupDecisions(t)
	const slow = 400 * time.Millisecond
	var mu sync.Mutex
	var landed []object.UsageEvent
	var at time.Time
	object.SetUsageRecorder(func(_ context.Context, u object.UsageEvent) error {
		time.Sleep(slow)
		mu.Lock()
		defer mu.Unlock()
		landed = append(landed, u)
		at = time.Now()
		return nil
	})

	c := presenting(visit(http.MethodPost, decisionsPath), "Bearer "+decisionsKey)
	c.Fiber().Request().SetBody([]byte(decisionBody))
	start := time.Now()
	status := answering(t, c, c.Decisions)
	replied := time.Since(start)
	if status != http.StatusOK {
		t.Fatalf("status = %d (%s)", status, sent(c))
	}
	mu.Lock()
	early := len(landed)
	mu.Unlock()
	if early != 0 {
		t.Fatal("the debit landed before the reply: the reply waited on it")
	}
	if replied >= slow/2 {
		t.Fatalf("the reply took %v with a %v settle: it waited on the books", replied, slow)
	}
	settled(t)
	mu.Lock()
	defer mu.Unlock()
	if len(landed) != 1 || landed[0].USD != nanoToUSD(42*21) {
		t.Fatalf("debits = %+v, want one at 42 input tokens × $0.021/M", landed)
	}
	t.Logf("reply after %v; debit landed %v after the request began (settle %v)", replied, at.Sub(start), slow)
}

// Jev by its vendor ids reaches Jev itself on /v1/decisions, forwarded under the id
// asked and billed at Jev's list price. Every bare Jev spelling is an unknown model,
// and nothing is sent.
func TestDecisionsServeJevByItsVendorIds(t *testing.T) {
	fake, events := setupDecisions(t)
	fake.answer = jevAnswer
	for _, model := range []string{"typesafe/jev-1.13", "~typesafe/jev-latest"} {
		asked := strings.Replace(decisionBody, `"model":"kai"`, `"model":"`+model+`"`, 1)
		status, body := driveDecisions(t, "Bearer "+decisionsKey, asked)
		if status != http.StatusOK || body != jevAnswer {
			t.Fatalf("%s => %d %s", model, status, body)
		}
		_, path, sent := fake.seen()
		if path != decisionsPath || top(t, string(sent), "model") != `"`+model+`"` {
			t.Fatalf("%s reached %s as %s", model, path, top(t, string(sent), "model"))
		}
	}
	if len(*events) != 2 || (*events)[0].USD != nanoToUSD(42*42) || (*events)[1].USD != nanoToUSD(42*42) {
		t.Fatalf("debits = %+v, want two at 42 input tokens × Jev's $0.042/M", *events)
	}

	calls, _, _ := fake.seen()
	for _, model := range []string{"jev-latest", "jev-preview", "jev-1.13.0", "jev-1.13", "laya"} {
		status, body := driveDecisions(t, "Bearer "+decisionsKey,
			`{"model":"`+model+`","state":"x","questions":{"q":{"type":"noul"}}}`)
		want := string(decisionsFailure(400, fmt.Sprintf("unknown model %q; use one of kai, typesafe/jev-1.13, ~typesafe/jev-latest", model)))
		if status != http.StatusBadRequest || body != want {
			t.Errorf("%s => %d %s", model, status, body)
		}
	}
	if now, _, _ := fake.seen(); now != calls || len(*events) != 2 {
		t.Fatalf("a bare Jev id reached the service (%d calls) or was billed (%d debits)", now-calls, len(*events)-2)
	}
}

// A yes/no question is a boolean, and Jev spells it noul. A boolean asked of Jev
// reaches it as a noul, every other byte of the request as written, and its answer
// comes back as exactly {"type":"boolean","probability":p}, p as Jev wrote it; a
// noul and a choice beside it go both ways untouched. Kai reads both spellings, so
// it is sent the body as written and its answer comes back as written.
func TestJevIsAskedABooleanAsANoul(t *testing.T) {
	fake, events := setupDecisions(t)
	const questions = `{"is_bug": {"type": "boolean", "instructions": "Is this a product defect?",
		"criteria": {"true": "a defect", "false": "working as intended"}},
	"urgent": {"type": "noul", "instructions": "Page on-call?"},
	"team": {"type": "choice", "criteria": ["payments", "account"]}}`
	ask := func(model string) string {
		return `{"model":"` + model + `","state":"I was charged twice.","questions":` + questions + `}`
	}
	choice := `"team":{"type":"choice","choice":"payments","confidence":0.2,"probabilities":{"payments":0.6,"account":0.4},"answer_confidence":0.6}`
	answer := func(model, isBug string) string {
		return `{"id":"dec_2","model":"` + model + `","provider":"OpenRouter","answers":{"is_bug":` + isBug +
			`,"urgent":{"type":"noul","noul":0.75},` + choice + `},"usage":{"input_tokens":42,"output_tokens":0}}`
	}
	fake.serve = func(_ string, body []byte) (int, string) {
		var b struct{ Model string }
		_ = json.Unmarshal(body, &b)
		if b.Model == "kai" {
			return http.StatusOK, answer("kai", `{"type":"boolean","probability":0.25}`)
		}
		return http.StatusOK, answer(b.Model, `{"type":"noul","noul":0.2500,"confidence":0.5,"answer_confidence":0.75}`)
	}

	for _, model := range []string{"typesafe/jev-1.13", "~typesafe/jev-latest"} {
		status, body := driveDecisions(t, "Bearer "+decisionsKey, ask(model))
		if want := answer(model, `{"type":"boolean","probability":0.2500}`); status != http.StatusOK || body != want {
			t.Fatalf("%s answered %d\n got %s\nwant %s", model, status, body, want)
		}
		_, _, sent := fake.seen()
		if want := strings.Replace(ask(model), `"is_bug": {"type": "boolean"`, `"is_bug": {"type": "noul"`, 1); string(sent) != want {
			t.Fatalf("%s was sent\n%s\nwant\n%s", model, sent, want)
		}
	}

	status, body := driveDecisions(t, "Bearer "+decisionsKey, ask("kai"))
	if want := answer("kai", `{"type":"boolean","probability":0.25}`); status != http.StatusOK || body != want {
		t.Fatalf("kai answered %d\n got %s\nwant %s", status, body, want)
	}
	if _, _, sent := fake.seen(); string(sent) != ask("kai") {
		t.Fatalf("kai was sent\n%s\nwant the body as written", sent)
	}
	if len(*events) != 3 {
		t.Fatalf("debits = %d, want one per answered call", len(*events))
	}
}

// spell and unspell read the wire as the service does: an escaped type is the type,
// of a key written twice the last counts, an answer that is not a noul's
// probability is left as it came, and every byte they do not respell is kept.
func TestSpellReadsAsTheServiceReads(t *testing.T) {
	for _, c := range []struct {
		body, sent string
		asked      []string
	}{
		{`{"questions": {"q": {"type": "boolean", "instructions": "x"}}}`, `{"questions": {"q": {"type": "noul", "instructions": "x"}}}`, []string{"q"}},
		{`{"questions":{"q":{"type":"\u0062oolean"}}}`, `{"questions":{"q":{"type":"noul"}}}`, []string{"q"}},
		{`{"questions":{"q":{"type":"noul"},"q":{"type":"boolean"}}}`, `{"questions":{"q":{"type":"noul"},"q":{"type":"noul"}}}`, []string{"q"}},
		{`{"questions":{"q":{"type":"boolean"},"q":{"type":"score","criteria":["a"]}}}`, "", nil},
		{`{"questions":{"q":{"type":"noul"},"r":{"type":"Boolean"}}}`, "", nil},
		{`{"questions":[{"type":"boolean"}]}`, "", nil},
		{`{"questions":{"q":{"type":"boolean"}}} {}`, "", nil},
	} {
		sent, asked := spell([]byte(c.body))
		want := c.sent
		if want == "" {
			want = c.body
		}
		var names []string
		for n := range asked {
			names = append(names, n)
		}
		if string(sent) != want || fmt.Sprint(names) != fmt.Sprint(c.asked) {
			t.Errorf("spell(%s) = %s %v, want %s %v", c.body, sent, names, want, c.asked)
		}
	}

	asked := map[string]bool{"q": true}
	for in, want := range map[string]string{
		`{"answers": {"q": {"type": "noul", "noul": 1e-3}, "r": {"type": "noul", "noul": 0.5}}}`: `{"answers": {"q": {"type":"boolean","probability":1e-3}, "r": {"type": "noul", "noul": 0.5}}}`,
		`{"answers":{"q":{"type":"noul","noul":null}}}`:                                          "",
		`{"answers":{"q":{"type":"choice","choice":"a"}}}`:                                       "",
		`{"answers":{"q":{"type":"boolean","probability":0.5}}}`:                                 "",
		`{"error":{"code":422,"message":"no"}}`:                                                  "",
	} {
		if want == "" {
			want = in
		}
		if got := unspell([]byte(in), asked); string(got) != want {
			t.Errorf("unspell(%s) = %s, want %s", in, got, want)
		}
	}
}

// Nothing named Jev is ever answered by Kai: a route that would send a Jev id to
// Kai is an unknown model.
func TestJevIsNeverKai(t *testing.T) {
	fake, _ := setupDecisions(t)
	path := t.TempDir() + "/models.yaml"
	if err := os.WriteFile(path, []byte(`version: 1
models:
  kai:
    provider: kai
    upstream: kai
  jev-latest:
    provider: kai
    upstream: kai
  typesafe/jev-9:
    provider: kai
    upstream: kai@abc
`), 0o600); err != nil {
		t.Fatal(err)
	}
	useCatalog(t, path)
	for _, model := range []string{"jev-latest", "typesafe/jev-9"} {
		status, body := driveDecisions(t, "Bearer "+decisionsKey, strings.Replace(decisionBody, `"model":"kai"`, `"model":"`+model+`"`, 1))
		if status != http.StatusBadRequest {
			t.Errorf("%s => %d %s; a Jev id reached Kai", model, status, body)
		}
	}
	if calls, _, _ := fake.seen(); calls != 0 {
		t.Fatalf("the service saw %d call(s) for Jev ids routed to Kai", calls)
	}
}

// A 402 asks the caller to wait until a top-up is read, in both headers, in the
// service's error shape.
func TestDecisionPaymentRequiredSaysWhen(t *testing.T) {
	fake, events := setupDecisions(t)
	object.SetBalanceReader(balReader(0, nil))
	status, body, c := drive(t, "Bearer "+decisionsKey, decisionBody, nil)
	if status != http.StatusPaymentRequired {
		t.Fatalf("no balance => %d %s", status, body)
	}
	if replied(c, "Retry-After") != "30" || replied(c, "Retry-After-Ms") != "30000" {
		t.Errorf("402 Retry-After=%q Retry-After-Ms=%q, want 30 and 30000", replied(c, "Retry-After"), replied(c, "Retry-After-Ms"))
	}
	if top(t, body, "error") == "" || replied(c, "X-Request-Id") == "" {
		t.Errorf("402 = %s (request id %q)", body, replied(c, "X-Request-Id"))
	}
	if calls, _, _ := fake.seen(); calls != 0 || len(*events) != 0 {
		t.Fatalf("an unfunded call reached the service %d time(s) and was debited %d", calls, len(*events))
	}
}

// The service's waits pass through, completed: a 429 that says seconds also says
// milliseconds, a 529 that says milliseconds also says seconds.
func TestDecisionWaitsPassThrough(t *testing.T) {
	fake, _ := setupDecisions(t)
	fake.status, fake.answer = http.StatusTooManyRequests, `{"error":{"code":429,"message":"queue full"}}`
	fake.header = map[string]string{"Retry-After": "2"}
	status, body, c := drive(t, "Bearer "+decisionsKey, decisionBody, nil)
	if status != 429 || body != fake.answer || replied(c, "Retry-After") != "2" || replied(c, "Retry-After-Ms") != "2000" {
		t.Fatalf("429 => %d %s Retry-After=%q ms=%q", status, body, replied(c, "Retry-After"), replied(c, "Retry-After-Ms"))
	}

	fake.status, fake.answer = 529, `{"error":{"code":529,"message":"overloaded"}}`
	fake.header = map[string]string{"Retry-After-Ms": "1500"}
	status, body, c = drive(t, "Bearer "+decisionsKey, decisionBody, nil)
	if status != 529 || body != fake.answer || replied(c, "Retry-After") != "2" || replied(c, "Retry-After-Ms") != "1500" {
		t.Fatalf("529 => %d %s Retry-After=%q ms=%q", status, body, replied(c, "Retry-After"), replied(c, "Retry-After-Ms"))
	}
}

// The request id: the caller's own is sent to the service and answered under; the
// service's own, when it states one, is the answer's; with neither, a fresh one.
func TestDecisionRequestID(t *testing.T) {
	fake, _ := setupDecisions(t)
	_, _, c := drive(t, "Bearer "+decisionsKey, decisionBody, map[string]string{"X-Request-Id": "req-abc"})
	if fake.rid != "req-abc" || replied(c, "X-Request-Id") != "req-abc" {
		t.Fatalf("caller's id: service saw %q, answer carries %q", fake.rid, replied(c, "X-Request-Id"))
	}
	fake.header = map[string]string{"X-Request-Id": "svc-1"}
	if _, _, c = drive(t, "Bearer "+decisionsKey, decisionBody, nil); replied(c, "X-Request-Id") != "svc-1" {
		t.Fatalf("service's id: answer carries %q", replied(c, "X-Request-Id"))
	}
	// An id no header should carry back is replaced, never echoed.
	if _, _, c = drive(t, "", decisionBody, map[string]string{"X-Request-Id": strings.Repeat("x", 300)}); len(replied(c, "X-Request-Id")) != 36 {
		t.Fatalf("an oversized id was echoed: %q", replied(c, "X-Request-Id"))
	}
}

// The two doors pay from one principal: a ZAP call on a vendor key is gated,
// reserved and debited on the org that owns the key, exactly as over HTTP, and its
// gateway answer carries the request id.
func TestDecisionDoorsAgreeOnWhoPays(t *testing.T) {
	_, events := setupDecisions(t)
	if _, body := driveDecisions(t, "Bearer "+decisionsKey, decisionBody); body != decisionAnswer {
		t.Fatalf("HTTP => %s", body)
	}
	gateway, _ := lookupGatewayHandler(decisionsPath)
	msg, err := gateway(context.Background(), "Bearer "+decisionsKey, []byte(decisionBody))
	if err != nil {
		t.Fatal(err)
	}
	root := msg.Root()
	if root.Uint32(object.GatewayRespStatus) != 200 {
		t.Fatalf("ZAP => %d %s", root.Uint32(object.GatewayRespStatus), root.Bytes(object.GatewayRespBody))
	}
	var h map[string]string
	if json.Unmarshal(root.Bytes(object.GatewayRespHeaders), &h) != nil || h["X-Request-Id"] == "" {
		t.Fatalf("gateway headers = %s", root.Bytes(object.GatewayRespHeaders))
	}
	settled(t)
	if len(*events) != 2 {
		t.Fatalf("debits = %d, want one per door", len(*events))
	}
	if a, b := (*events)[0], (*events)[1]; a.Namespace != b.Namespace || a.Subject != b.Subject || a.USD != b.USD {
		t.Fatalf("the doors paid from different principals: %+v vs %+v", a, b)
	}

	// With no balance the ZAP door refuses as the HTTP one does.
	object.SetBalanceReader(balReader(0, nil))
	msg, _ = gateway(context.Background(), "Bearer "+decisionsKey, []byte(decisionBody))
	root = msg.Root()
	_ = json.Unmarshal(root.Bytes(object.GatewayRespHeaders), &h)
	if root.Uint32(object.GatewayRespStatus) != 402 || h["Retry-After"] != "30" || h["Retry-After-Ms"] != "30000" {
		t.Fatalf("ZAP 402 => %d %s headers %v", root.Uint32(object.GatewayRespStatus), root.Bytes(object.GatewayRespBody), h)
	}
}

// An answer Kai gave in process is given again to the same org for an identical
// request, billed exactly as the first, under an id of its own — and never to
// another org, whose request reaches the service.
func TestDecisionHeldAnswers(t *testing.T) {
	fake, events := setupDecisions(t)
	seedOther(t)
	fake.answer = heldAnswer

	_, first := driveDecisions(t, "Bearer "+decisionsKey, decisionBody)
	_, again := driveDecisions(t, "Bearer "+decisionsKey, decisionBody)
	if calls, _, _ := fake.seen(); calls != 1 {
		t.Fatalf("service saw %d calls for one org's identical requests, want 1", calls)
	}
	if top(t, first, "id") == top(t, again, "id") {
		t.Fatalf("two calls answered under one id %s", top(t, first, "id"))
	}
	for _, k := range []string{"answers", "usage", "routing", "model"} {
		if top(t, first, k) != top(t, again, k) {
			t.Fatalf("%s differs between a fresh answer and a held one", k)
		}
	}
	if len(*events) != 2 || (*events)[0].USD != (*events)[1].USD || (*events)[0].USD != nanoToUSD(42*21) {
		t.Fatalf("debits = %+v, want two, each at 42 input tokens × $0.021/M", *events)
	}

	driveDecisions(t, "Bearer "+otherKey, decisionBody)
	if calls, _, _ := fake.seen(); calls != 2 {
		t.Fatalf("another org's identical request was answered from acme's: service saw %d calls, want 2", calls)
	}
	if e := (*events)[2]; e.Namespace != otherOrg || e.USD != nanoToUSD(42*21) {
		t.Fatalf("globex debit = %+v", e)
	}

	// A request naming a handle is never held.
	observe := strings.Replace(decisionBody, `}}}`, `}},"observe":"s1"}`, 1)
	driveDecisions(t, "Bearer "+decisionsKey, observe)
	driveDecisions(t, "Bearer "+decisionsKey, observe)
	if calls, _, _ := fake.seen(); calls != 4 {
		t.Fatalf("service saw %d calls; a request naming a handle was answered from memory", calls)
	}
}

// The service is told the org that pays, as X-Org-Id, on every call it is sent —
// a body naming a handle, a body no answer is held for yet, and one asked once
// for everyone in flight — through either door. It is the org the gateway
// resolved, never the one the caller wrote.
func TestDecisionServiceIsToldTheOrg(t *testing.T) {
	fake, _ := setupDecisions(t)
	told := func(status int, body, want string) {
		t.Helper()
		fake.mu.Lock()
		got := fake.org
		fake.org = ""
		fake.mu.Unlock()
		if status != http.StatusOK || got != want {
			t.Fatalf("=> %d %s; the service was told org %q, want %q", status, body, got, want)
		}
	}

	// An sk- key carries no membership: the org it writes is not the org that pays.
	status, body, _ := drive(t, "Bearer "+decisionsKey, decisionBody, map[string]string{"X-Org-Id": otherOrg})
	told(status, body, decisionsOrg)
	fake.answer = heldAnswer
	status, body, _ = drive(t, "Bearer "+decisionsKey, decisionBody, nil)
	told(status, body, decisionsOrg)
	other := strings.Replace(decisionBody, "twice", "three times", 1)
	status, body, _ = drive(t, "Bearer "+decisionsKey, other, map[string]string{"X-Org-Id": otherOrg})
	told(status, body, decisionsOrg)
	observe := strings.Replace(decisionBody, `}}}`, `}},"observe":"s1"}`, 1)
	status, body, _ = drive(t, "Bearer "+decisionsKey, observe, map[string]string{"X-Org-Id": otherOrg})
	told(status, body, decisionsOrg)

	// A member acting in an org its signed membership covers is that org, on both doors.
	tok := mintUsageJWTWithOrgs(t, "beta", "bob", "beta", decisionsOrg)
	status, body, _ = drive(t, "Bearer "+tok, observe, map[string]string{"X-Org-Id": decisionsOrg})
	told(status, body, decisionsOrg)
	msg, err := gateway(nil)(context.Background(), "", gatewayCall(t, decisionsPath,
		map[string]string{"Authorization": "Bearer " + tok, "X-Org-Id": decisionsOrg}, observe))
	if err != nil {
		t.Fatal(err)
	}
	settled(t)
	told(int(msg.Root().Uint32(object.GatewayRespStatus)), string(msg.Root().Bytes(object.GatewayRespBody)), decisionsOrg)
}

// capabilityAnswer is heldAnswer as an org's capability gave it, at sha.
func capabilityAnswer(sha string) string {
	return strings.Replace(heldAnswer, `"sha256":"abc",`, `"sha256":"abc","capability":{"name":"triage","sha256":"`+sha+`"},`, 1)
}

// The org's published capabilities ride every decision it asks, and an answer is
// held under the weights and the set the request carried: once the org publishes,
// replaces or withdraws a capability, only answers held under the set it now has
// are given again. A set that cannot be read refuses the call.
func TestHeldAnswersFollowTheAttachedCapabilities(t *testing.T) {
	fake, _ := setupDecisions(t)
	sets := map[string]string{}
	var broken bool
	object.SetCapabilities(func(_ context.Context, org string) (string, error) {
		if broken {
			return "", errors.New("train unavailable")
		}
		return sets[org], nil
	})
	t.Cleanup(func() { object.SetCapabilities(nil) })
	sent := func(n int, want bool, answer string) {
		t.Helper()
		body := strings.Replace(decisionBody, "twice", fmt.Sprintf("twice %d", n), 1)
		before, _, _ := fake.seen()
		status, got, _ := drive(t, "Bearer "+decisionsKey, body, nil)
		after, _, _ := fake.seen()
		if status != http.StatusOK || (after > before) != want {
			t.Fatalf("asked %d: %d, reached the service %v, want %v", n, status, after > before, want)
		}
		if top(t, got, "routing") != top(t, answer, "routing") {
			t.Fatalf("answered by %s, want %s", top(t, got, "routing"), top(t, answer, "routing"))
		}
		fake.mu.Lock()
		defer fake.mu.Unlock()
		if want && fake.set != sets[decisionsOrg] && !(fake.set == "" && sets[decisionsOrg] == "[]") {
			t.Fatalf("the service was sent capabilities %q, want %q", fake.set, sets[decisionsOrg])
		}
	}

	base, c1, c2 := heldAnswer, capabilityAnswer("c1"), capabilityAnswer("c2")
	fake.answer = base
	sets[decisionsOrg] = "[]"
	sent(1, true, base)
	sent(1, false, base)

	// It publishes a capability, which answers.
	sets[decisionsOrg] = `[{"name":"triage","sha256":"c1"}]`
	fake.answer = c1
	sent(1, true, c1)
	sent(1, false, c1)

	// It is republished.
	sets[decisionsOrg] = `[{"name":"triage","sha256":"c2"}]`
	fake.answer = c2
	sent(1, true, c2)
	sent(1, false, c2)

	// It is withdrawn: the base's answer, held under the set it was given for.
	sets[decisionsOrg] = "[]"
	fake.answer = base
	sent(1, false, base)

	broken = true
	status, body, _ := drive(t, "Bearer "+decisionsKey, decisionBody, nil)
	if status != http.StatusServiceUnavailable || !strings.Contains(body, "capabilities") {
		t.Fatalf("a set that cannot be read: %d %s", status, body)
	}
}

// Restate words any refusal in the service's error shape, and leaves one already in
// it alone.
func TestRestate(t *testing.T) {
	cases := []struct {
		status int
		in     string
		out    string
	}{
		{401, `{"status":"error","msg":"invalid API key"}`, `{"error":{"code":401,"message":"invalid API key"}}`},
		{429, `{"error":{"message":"Rate limit exceeded.","type":"rate_limit_error","code":429}}`, `{"error":{"message":"Rate limit exceeded.","type":"rate_limit_error","code":429}}`},
		{503, `{"status":"error","msg":"identity is unavailable"}`, `{"error":{"code":503,"message":"identity is unavailable"}}`},
		{502, `upstream went away`, `{"error":{"code":502,"message":"upstream went away"}}`},
		{200, `{"id":"dec_1"}`, `{"id":"dec_1"}`},
	}
	for _, c := range cases {
		header := map[string]string{}
		got, _ := Restate(c.status, []byte(c.in), header, "rid-1")
		if string(got) != c.out {
			t.Errorf("%d %s:\n got %s\nwant %s", c.status, c.in, got, c.out)
		}
		if header["X-Request-Id"] != "rid-1" {
			t.Errorf("%d: no request id", c.status)
		}
		_, waits := pause(c.status)
		if waits != (header["Retry-After"] != "" && header["Retry-After-Ms"] != "") {
			t.Errorf("%d: retry headers %v", c.status, header)
		}
	}
}

// A score's legend is Content — a string, an object or an array — and reading the
// answer around it must never fail: the usage beside it is what the call is billed on.
func TestDecisionLegendIsContent(t *testing.T) {
	fake, events := setupDecisions(t)
	fake.answer = `{"id":"dec_1","model":"kai","provider":"Hanzo","answers":{"u":{"type":"score","score":1.2,"legend":{"0":"calm","1":{"tone":"upset"},"2":["angry"]},"probabilities":{"0":0.1,"1":0.6,"2":0.3}}},"usage":{"input_tokens":77,"output_tokens":0},"routing":{"backend":"kai","checkpoint":"hanzoai/kai","reason":"r"},"state_hash":"sha256:00","latency_ms":1}`
	if status, body := driveDecisions(t, "Bearer "+decisionsKey, decisionBody); status != http.StatusOK || body != fake.answer {
		t.Fatalf("score => %d %s", status, body)
	}
	if len(*events) != 1 || (*events)[0].USD != nanoToUSD(77*21) {
		t.Fatalf("debits = %+v, want one at 77 input tokens", *events)
	}
	var r decisionsResponse
	if err := json.Unmarshal([]byte(fake.answer), &r); err != nil || string(r.Answers["u"].Legend["1"]) != `{"tone":"upset"}` {
		t.Fatalf("the declared response does not read a legend: %v", err)
	}
	out, err := json.Marshal(r.Answers["u"].Legend)
	if err != nil || string(out) != `{"0":"calm","1":{"tone":"upset"},"2":["angry"]}` {
		t.Fatalf("a legend marshals as %s (%v)", out, err)
	}
}
