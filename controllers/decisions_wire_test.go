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

// An answer is held under the weights and the capability that gave it. Once an
// org's capability answers, the base answers it held are not given again, nor the
// answers of a capability since republished; another org's are untouched. An
// answer no capability gave is held under the weights alone, the key it always had.
func TestHeldAnswersFollowTheCapability(t *testing.T) {
	fake, _ := setupDecisions(t)
	kai := object.KaiProvider()
	ctx := context.Background()
	ask := func(n int) []byte {
		return []byte(strings.Replace(decisionBody, "twice", fmt.Sprintf("twice %d", n), 1))
	}
	sent := func(org string, body []byte, want bool, answer string) {
		t.Helper()
		before, _, _ := fake.seen()
		d := recall(ctx, kai, "kai", org, "r", body)
		after, _, _ := fake.seen()
		if (after > before) != want {
			t.Fatalf("%s asked %s: reached the service %v, want %v", org, body, after > before, want)
		}
		if top(t, string(d.body), "routing") != top(t, answer, "routing") {
			t.Fatalf("%s was answered by %s, want %s", org, top(t, string(d.body), "routing"), top(t, answer, "routing"))
		}
	}
	holds := func(org string, body []byte, rev string) {
		t.Helper()
		norm, _ := holdable(body)
		decisionCache.mu.Lock()
		defer decisionCache.mu.Unlock()
		if _, ok := decisionCache.m[decisionKey(org, "kai", rev, norm)]; !ok {
			t.Fatalf("%s's answer to %s is not held under revision %q", org, body, rev)
		}
	}

	base, c1, c2 := heldAnswer, capabilityAnswer("c1"), capabilityAnswer("c2")
	fake.answer = base
	sent("acme", ask(1), true, base)
	sent("globex", ask(1), true, base)
	holds("acme", ask(1), "abc")
	sent("acme", ask(1), false, base)

	// acme's capability is installed and answers.
	fake.answer = c1
	sent("acme", ask(2), true, c1)
	holds("acme", ask(2), "abc\x00triage\x00c1")
	sent("acme", ask(1), true, c1)
	sent("acme", ask(1), false, c1)
	sent("globex", ask(1), false, base)

	// It is republished.
	fake.answer = c2
	sent("acme", ask(3), true, c2)
	sent("acme", ask(1), true, c2)
	sent("acme", ask(1), false, c2)

	// The base answers acme alone again: held, and asked for, under the weights alone.
	fake.answer = base
	sent("acme", ask(4), true, base)
	holds("acme", ask(4), "abc")
	sent("acme", ask(4), false, base)
	decisionCache.mu.Lock()
	defer decisionCache.mu.Unlock()
	if c := decisionCache.orgs["acme"].capability["kai"]; c != "" {
		t.Fatalf("acme is still answered by capability %q", c)
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
