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
	"net/http"
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

// The Jev answer the service writes on /v1/systemone.
const joneAnswer = `{"model":"kai-2026-09","answers":{"is_bug":{"type":"noul","noul":0.96}},"usage":{"input_tokens":42,"output_tokens":0}}`

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
	// Naming the other org in the id reaches nothing of theirs either.
	if status, body := driveDecisions(t, "Bearer "+otherKey, `{"model":"kai","handle":"acme/s1"}`); status != http.StatusBadRequest {
		t.Fatalf("an id spelling another org's name reached its state: %d %s", status, body)
	}
	if status, body := driveDecisions(t, "Bearer "+decisionsKey, `{"model":"kai","handle":"s1"}`); status != http.StatusOK {
		t.Fatalf("the org that observed s1 could not decide over it: %d %s", status, body)
	}

	want := []string{"observe acme/s1", "handle globex/s1", "handle globex/acme/s1", "handle acme/s1"}
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

// POST /v1/systemone is the same call on Jev's wire: the body reaches the service's
// /v1/systemone unchanged, Jev's answer comes back unchanged, and it bills exactly
// as /v1/decisions does.
func TestSystemoneForwardsAndMeters(t *testing.T) {
	fake, events := setupDecisions(t)
	fake.answer = joneAnswer
	status, body, c := drive(t, systemonePath, "Bearer "+decisionsKey, decisionBody, nil)
	if status != http.StatusOK || body != joneAnswer {
		t.Fatalf("systemone => %d %s", status, body)
	}
	calls, path, sentBody := fake.seen()
	if calls != 1 || path != systemonePath || string(sentBody) != decisionBody {
		t.Fatalf("service saw %d call(s) at %q with %s", calls, path, sentBody)
	}
	if replied(c, "X-Request-Id") == "" {
		t.Fatal("the answer carries no X-Request-Id")
	}
	if len(*events) != 1 {
		t.Fatalf("debits = %d, want 1", len(*events))
	}
	if e := (*events)[0]; e.Model != "kai" || e.Namespace != decisionsOrg || e.USD != nanoToUSD(42*21) {
		t.Fatalf("debit = %+v, want kai on acme at 42 input tokens × $0.021/M", e)
	}
}

// No Jev id is ever mapped to Kai: every Jev spelling is an unknown model on
// /v1/systemone, in FastAPI's words, and nothing is sent or billed.
func TestSystemoneRefusesJevIds(t *testing.T) {
	fake, events := setupDecisions(t)
	for _, model := range []string{"jev-latest", "jev-preview", "jev-1.13.0", "jev-1.13", "typesafe/jev-1.13", "~typesafe/jev-latest", "laya"} {
		status, body, _ := drive(t, systemonePath, "Bearer "+decisionsKey,
			`{"model":"`+model+`","state":"x","questions":{"q":{"type":"noul"}}}`, nil)
		if status != http.StatusBadRequest || body != `{"detail":"Unknown model: `+model+`"}` {
			t.Errorf("%s => %d %s", model, status, body)
		}
	}
	// The hidden Jev routes keep reaching Jev on /v1/decisions.
	if status, body := driveDecisions(t, "Bearer "+decisionsKey,
		strings.Replace(decisionBody, `"model":"kai"`, `"model":"typesafe/jev-1.13"`, 1)); status != http.StatusOK {
		t.Fatalf("typesafe/jev-1.13 on /v1/decisions => %d %s", status, body)
	}
	if calls, path, _ := fake.seen(); calls != 1 || path != decisionsPath {
		t.Fatalf("service saw %d call(s), last at %q; want only the /v1/decisions one", calls, path)
	}
	if len(*events) != 1 {
		t.Fatalf("debits = %d, want only the served /v1/decisions call", len(*events))
	}
}

// Every refusal on /v1/systemone is FastAPI's: a missing field is a 422 naming it,
// and the rest are {"detail": "..."}.
func TestSystemoneRefusalsAreFastAPIs(t *testing.T) {
	setupDecisions(t)
	status, body, _ := drive(t, systemonePath, "Bearer "+decisionsKey, `{"state":"x"}`, nil)
	if status != http.StatusUnprocessableEntity || body != `{"detail":[{"loc":["body","model"],"msg":"Field required","type":"missing"}]}` {
		t.Fatalf("no model => %d %s", status, body)
	}
	for auth, want := range map[string]int{"": 401, "Bearer sk-nobody-issued-this": 401, "Bearer pk-publishable": 403} {
		status, body, _ := drive(t, systemonePath, auth, decisionBody, nil)
		var r struct{ Detail string }
		if status != want || json.Unmarshal([]byte(body), &r) != nil || r.Detail == "" {
			t.Errorf("%q => %d %s, want %d {\"detail\": ...}", auth, status, body, want)
		}
	}
}

// A 402 asks the caller to wait until a top-up is read, in both headers, on both
// paths, each in its own words.
func TestDecisionPaymentRequiredSaysWhen(t *testing.T) {
	fake, events := setupDecisions(t)
	object.SetBalanceReader(balReader(0, nil))
	for _, path := range []string{decisionsPath, systemonePath} {
		status, body, c := drive(t, path, "Bearer "+decisionsKey, decisionBody, nil)
		if status != http.StatusPaymentRequired {
			t.Fatalf("%s with no balance => %d %s", path, status, body)
		}
		if replied(c, "Retry-After") != "30" || replied(c, "Retry-After-Ms") != "30000" {
			t.Errorf("%s 402 Retry-After=%q Retry-After-Ms=%q, want 30 and 30000", path, replied(c, "Retry-After"), replied(c, "Retry-After-Ms"))
		}
		key := "error"
		if path == systemonePath {
			key = "detail"
		}
		if top(t, body, key) == "" || replied(c, "X-Request-Id") == "" {
			t.Errorf("%s 402 = %s (request id %q)", path, body, replied(c, "X-Request-Id"))
		}
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
	status, body, c := drive(t, decisionsPath, "Bearer "+decisionsKey, decisionBody, nil)
	if status != 429 || body != fake.answer || replied(c, "Retry-After") != "2" || replied(c, "Retry-After-Ms") != "2000" {
		t.Fatalf("429 => %d %s Retry-After=%q ms=%q", status, body, replied(c, "Retry-After"), replied(c, "Retry-After-Ms"))
	}

	fake.status, fake.answer = 529, `{"detail":"overloaded"}`
	fake.header = map[string]string{"Retry-After-Ms": "1500"}
	status, body, c = drive(t, systemonePath, "Bearer "+decisionsKey, decisionBody, nil)
	if status != 529 || body != fake.answer || replied(c, "Retry-After") != "2" || replied(c, "Retry-After-Ms") != "1500" {
		t.Fatalf("529 => %d %s Retry-After=%q ms=%q", status, body, replied(c, "Retry-After"), replied(c, "Retry-After-Ms"))
	}
}

// The request id: the caller's own is sent to the service and answered under; the
// service's own, when it states one, is the answer's; with neither, a fresh one.
func TestDecisionRequestID(t *testing.T) {
	fake, _ := setupDecisions(t)
	_, _, c := drive(t, decisionsPath, "Bearer "+decisionsKey, decisionBody, map[string]string{"X-Request-Id": "req-abc"})
	if fake.rid != "req-abc" || replied(c, "X-Request-Id") != "req-abc" {
		t.Fatalf("caller's id: service saw %q, answer carries %q", fake.rid, replied(c, "X-Request-Id"))
	}
	fake.header = map[string]string{"X-Request-Id": "svc-1"}
	if _, _, c = drive(t, decisionsPath, "Bearer "+decisionsKey, decisionBody, nil); replied(c, "X-Request-Id") != "svc-1" {
		t.Fatalf("service's id: answer carries %q", replied(c, "X-Request-Id"))
	}
	// An id no header should carry back is replaced, never echoed.
	if _, _, c = drive(t, decisionsPath, "", decisionBody, map[string]string{"X-Request-Id": strings.Repeat("x", 300)}); len(replied(c, "X-Request-Id")) != 36 {
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
	systemone, _ := lookupGatewayHandler(systemonePath)
	msg, _ = systemone(context.Background(), "Bearer "+decisionsKey, []byte(decisionBody))
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

// Restate words any refusal in the path's own shape, and leaves one already in it
// alone.
func TestRestate(t *testing.T) {
	cases := []struct {
		path   string
		status int
		in     string
		out    string
	}{
		{decisionsPath, 401, `{"status":"error","msg":"invalid API key"}`, `{"error":{"code":401,"message":"invalid API key"}}`},
		{decisionsPath, 429, `{"error":{"message":"Rate limit exceeded.","type":"rate_limit_error","code":429}}`, `{"error":{"message":"Rate limit exceeded.","type":"rate_limit_error","code":429}}`},
		{systemonePath, 429, `{"error":{"message":"Rate limit exceeded.","type":"rate_limit_error","code":429}}`, `{"detail":"Rate limit exceeded."}`},
		{systemonePath, 503, `{"status":"error","msg":"identity is unavailable"}`, `{"detail":"identity is unavailable"}`},
		{systemonePath, 422, `{"detail":[{"loc":["body"],"msg":"m","type":"t"}]}`, `{"detail":[{"loc":["body"],"msg":"m","type":"t"}]}`},
		{systemonePath, 502, `upstream went away`, `{"detail":"upstream went away"}`},
		{decisionsPath, 200, `{"id":"dec_1"}`, `{"id":"dec_1"}`},
	}
	for _, c := range cases {
		header := map[string]string{}
		got, _ := Restate(c.path, c.status, []byte(c.in), header, "rid-1")
		if string(got) != c.out {
			t.Errorf("%s %d %s:\n got %s\nwant %s", c.path, c.status, c.in, got, c.out)
		}
		if header["X-Request-Id"] != "rid-1" {
			t.Errorf("%s %d: no request id", c.path, c.status)
		}
		_, waits := pause(c.status)
		if waits != (header["Retry-After"] != "" && header["Retry-After-Ms"] != "") {
			t.Errorf("%s %d: retry headers %v", c.path, c.status, header)
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
