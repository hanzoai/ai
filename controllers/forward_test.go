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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
	openai "github.com/hanzoai/go-openai"
)

// forwarded drives the relay the way chatCompletions hands it a request: the body as
// the caller wrote it, the request as decided, one provider and no route.
func forwarded(t *testing.T, c *ApiController, provider *object.Provider, req *openai.ChatCompletionRequest, hold *budgetHold) bool {
	t.Helper()
	cooled.forget()
	t.Cleanup(cooled.forget)
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return c.forward(pass{req: req, body: body, primary: provider, hold: hold, id: "req1", start: time.Now()})
}

// ── canonical JSON ───────────────────────────────────────────────────────────

// canonical is the SHA-256 of a JSON document rendered the RFC 8785 way: object
// keys sorted, no insignificant whitespace, numbers in their shortest form, strings
// with only the escapes JSON requires. Two bodies that mean the same thing hash the
// same, whatever order and spacing they were written in. drop names top-level
// fields left out of the comparison.
func canonical(t *testing.T, raw []byte, drop ...string) string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, raw)
	}
	if obj, ok := v.(map[string]any); ok {
		for _, k := range drop {
			delete(obj, k)
		}
	}
	var b strings.Builder
	jcs(&b, v)
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func jcs(b *strings.Builder, v any) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			jcsString(b, k)
			b.WriteByte(':')
			jcs(b, x[k])
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			jcs(b, e)
		}
		b.WriteByte(']')
	case string:
		jcsString(b, x)
	case json.Number:
		f, _ := x.Float64()
		if f == math.Trunc(f) && math.Abs(f) < 1e21 {
			b.WriteString(strconv.FormatFloat(f, 'f', -1, 64))
		} else {
			b.WriteString(strconv.FormatFloat(f, 'g', -1, 64))
		}
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case nil:
		b.WriteString("null")
	}
}

func jcsString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

// ── the world: a real handler, a real store, scripted upstreams ─────────────

const relaySku = "relay-sku"

// scripted is one OpenAI-compatible upstream. It keeps every body it is sent and
// answers the nth with serve.
type scripted struct {
	mu     sync.Mutex
	bodies [][]byte
	serve  func(w http.ResponseWriter, n int)
	url    string
}

func script(t *testing.T, serve func(w http.ResponseWriter, n int)) *scripted {
	t.Helper()
	s := &scripted{serve: serve}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		n := len(s.bodies)
		s.bodies = append(s.bodies, b)
		s.mu.Unlock()
		s.serve(w, n)
	}))
	t.Cleanup(srv.Close)
	s.url = srv.URL
	return s
}

func (s *scripted) asked() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bodies)
}

func (s *scripted) sent(t *testing.T, i int) []byte {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if i >= len(s.bodies) {
		t.Fatalf("upstream was asked %d time(s); no body %d", len(s.bodies), i)
	}
	return s.bodies[i]
}

// relayWorld is one signed-in customer with money, one SKU routed to a primary and a
// fallback upstream this test scripts, and the debits recordUsage files. The SKU is
// priced apart from both upstream ids, so a bill in the wrong name is a wrong number.
type relayWorld struct {
	cred    string
	subject string
	funds   int64
	a, b    *scripted
	mu      sync.Mutex
	debits  []object.UsageEvent
}

func newRelayWorld(t *testing.T, a, b func(w http.ResponseWriter, n int)) *relayWorld {
	t.Helper()
	withStore(t)
	people := withIAM(t)
	cooled.forget()
	t.Cleanup(cooled.forget)

	w := &relayWorld{funds: 100000}
	t.Setenv("commerceEndpoint", fakeCommerceBalance(t, &w.funds))
	t.Setenv("commerceToken", "test-svc-token")

	w.a, w.b = script(t, a), script(t, b)
	for name, up := range map[string]*scripted{"relay-a": w.a, "relay-b": w.b} {
		if _, err := object.AddProvider(&object.Provider{
			Owner: "admin", Name: name, Category: "Model", Type: "OpenAI",
			ProviderUrl: up.url, ClientSecret: "k", State: "Active",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := object.AddModelRoute(&object.ModelRoute{
		Owner: "built-in", ModelName: relaySku, Enabled: true,
		Provider: "relay-a", Upstream: "a-up",
		Fallback1: "relay-b", Fallback1Up: "b-up",
		InputPrice: 2, OutputPrice: 8, Priced: true,
	}); err != nil {
		t.Fatal(err)
	}

	user := &iam.User{Owner: "relayco", Name: "val"}
	w.subject = user.PayerSubject("relayco")
	object.GlobalBalanceLedger.SetBalance(w.subject, w.funds)
	w.cred = people.signedIn(t, user)

	prev := object.UsageRecorder()
	object.SetUsageRecorder(func(_ context.Context, e object.UsageEvent) error {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.debits = append(w.debits, e)
		return nil
	})
	t.Cleanup(func() { object.SetUsageRecorder(prev) })
	return w
}

func (w *relayWorld) chat(body string) *ApiController {
	c := as(visit(http.MethodPost, "/v1/chat/completions"), w.cred)
	c.Fiber().Request().SetBody([]byte(body))
	c.ChatCompletions()
	return c
}

// spent is what left the customer's balance, with nothing still held.
func (w *relayWorld) spent(t *testing.T) int64 {
	t.Helper()
	balance, reserved, _, known := object.GlobalBalanceLedger.Snapshot(w.subject)
	if !known {
		t.Fatalf("the ledger holds nothing for %q", w.subject)
	}
	if reserved != 0 {
		t.Errorf("%d cents are still reserved after the answer", reserved)
	}
	return w.funds - balance
}

// paid is the one debit a served answer files.
func (w *relayWorld) paid(t *testing.T) object.UsageEvent {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.debits) != 1 {
		t.Fatalf("want one debit, got %d: %+v", len(w.debits), w.debits)
	}
	return w.debits[0]
}

// completes is an OpenAI chat completion reporting the usage the test wants billed.
// prompt includes cached, the way OpenAI counts it.
func completes(content string, prompt, cached, completion int) func(http.ResponseWriter, int) {
	return func(w http.ResponseWriter, _ int) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"up-1","object":"chat.completion","created":1,"model":"a-up",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d,"prompt_tokens_details":{"cached_tokens":%d}}}`,
			content, prompt, completion, prompt+completion, cached)
	}
}

// streams is the same answer as SSE, in parts, closed by the usage chunk a vendor
// sends when include_usage asks for it.
func streams(parts []string, prompt, cached, completion int) func(http.ResponseWriter, int) {
	return func(w http.ResponseWriter, _ int) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := func(s string) { fmt.Fprintf(w, "data: %s\n\n", s) }
		chunk(`{"id":"up-1","object":"chat.completion.chunk","created":1,"model":"a-up","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`)
		for _, p := range parts {
			chunk(fmt.Sprintf(`{"id":"up-1","object":"chat.completion.chunk","created":1,"model":"a-up","choices":[{"index":0,"delta":{"content":%q}}]}`, p))
		}
		chunk(`{"id":"up-1","object":"chat.completion.chunk","created":1,"model":"a-up","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		chunk(fmt.Sprintf(`{"id":"up-1","object":"chat.completion.chunk","created":1,"model":"a-up","choices":[],`+
			`"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d,"prompt_tokens_details":{"cached_tokens":%d}}}`,
			prompt, completion, prompt+completion, cached))
		chunk("[DONE]")
	}
}

func declines(status int, msg string) func(http.ResponseWriter, int) {
	return func(w http.ResponseWriter, _ int) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"error":{"message":%q}}`, msg)
	}
}

func never(t *testing.T) func(http.ResponseWriter, int) {
	return func(w http.ResponseWriter, _ int) {
		t.Errorf("the fallback was asked")
		w.WriteHeader(http.StatusInternalServerError)
	}
}

// ── fidelity ─────────────────────────────────────────────────────────────────

// conversation is a request whose every part used to be lost somewhere: three user
// turns (the pipeline kept the last), a system prompt and assistant turns (rebuilt),
// temperature 0 and top_p 0 (omitempty), a seed, stop sequences and a JSON schema
// (replaced by the provider row's defaults), a tool call and its result, and a field
// no struct names.
const conversation = `{
  "model": "relay-sku",
  "messages": [
    {"role": "system", "content": "Answer in JSON. <be terse> & exact."},
    {"role": "user", "content": "first question"},
    {"role": "assistant", "content": "first answer"},
    {"role": "user", "content": "second question"},
    {"role": "assistant", "content": null, "tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "lookup", "arguments": "{\"q\":\"x\"}"}}]},
    {"role": "tool", "tool_call_id": "call_1", "content": "42"},
    {"role": "user", "content": [{"type": "text", "text": "third question"}]}
  ],
  "temperature": 0,
  "top_p": 0,
  "seed": 7,
  "stop": ["\n\nEND", "###"],
  "max_tokens": 256,
  "logit_bias": {"50256": -100},
  "response_format": {"type": "json_schema", "json_schema": {"name": "answer", "strict": true,
    "schema": {"type": "object", "properties": {"a": {"type": "string"}}, "required": ["a"], "additionalProperties": false}}},
  "tools": [{"type": "function", "function": {"name": "lookup", "description": "find a thing",
    "parameters": {"type": "object", "properties": {"q": {"type": "string"}}, "required": ["q"]}}}],
  "tool_choice": "auto",
  "parallel_tool_calls": false,
  "vendor_extra": {"keep": [1, 2.5, "three"]}
}`

// The same request written differently: keys in another order, no whitespace
// between them, numbers spelled another way. It means the same thing, so it must
// arrive the same.
const rewritten = `{"vendor_extra":{"keep":[1,2.50,"three"]},"tool_choice":"auto","tools":[{"function":{"parameters":{"required":["q"],"properties":{"q":{"type":"string"}},"type":"object"},"description":"find a thing","name":"lookup"},"type":"function"}],"seed":7,"top_p":0.0,"temperature":0,` +
	`"messages":[{"content":"Answer in JSON. <be terse> & exact.","role":"system"},{"content":"first question","role":"user"},{"role":"assistant","content":"first answer"},{"content":"second question","role":"user"},{"tool_calls":[{"function":{"arguments":"{\"q\":\"x\"}","name":"lookup"},"type":"function","id":"call_1"}],"content":null,"role":"assistant"},{"content":"42","tool_call_id":"call_1","role":"tool"},{"content":[{"text":"third question","type":"text"}],"role":"user"}],` +
	`"parallel_tool_calls":false,"max_tokens":256,"logit_bias":{"50256":-100},"stop":["\n\nEND","###"],"response_format":{"json_schema":{"schema":{"additionalProperties":false,"required":["a"],"properties":{"a":{"type":"string"}},"type":"object"},"strict":true,"name":"answer"},"type":"json_schema"},"model":"relay-sku"}`

// The fields the relay is allowed to decide. Nothing else may differ.
var ourFields = []string{"model", "max_tokens", "max_completion_tokens", "stream_options"}

func TestTheRelaySendsTheConversationAsWritten(t *testing.T) {
	w := newRelayWorld(t, completes(`{"a":"ok"}`, 30, 0, 5), never(t))

	var want string
	for i, body := range []string{conversation, rewritten} {
		c := w.chat(body)
		if answered(c) != http.StatusOK {
			t.Fatalf("status %d: %s", answered(c), sent(c))
		}
		up := w.a.sent(t, i)

		// Everything the caller wrote, and nothing the relay may not decide, arrives.
		got := canonical(t, up, ourFields...)
		if asked := canonical(t, []byte(body), ourFields...); got != asked {
			t.Errorf("body %d: the upstream was sent a different request\ncaller:   %s\nupstream: %s", i, body, up)
		}
		if i == 0 {
			want = got
		} else if got != want {
			t.Errorf("the same request written two ways reached the upstream as two requests")
		}

		var fields map[string]json.RawMessage
		if err := json.Unmarshal(up, &fields); err != nil {
			t.Fatal(err)
		}
		if string(fields["model"]) != `"a-up"` {
			t.Errorf("model = %s, want the upstream's own id", fields["model"])
		}
		if string(fields["max_tokens"]) != "256" {
			t.Errorf("max_tokens = %s, want the caller's 256 — it is under the reserved ceiling", fields["max_tokens"])
		}
		var msgs []map[string]any
		if err := json.Unmarshal(fields["messages"], &msgs); err != nil || len(msgs) != 7 {
			t.Fatalf("messages = %s, want all seven turns", fields["messages"])
		}
		for j, role := range []string{"system", "user", "assistant", "user", "assistant", "tool", "user"} {
			if msgs[j]["role"] != role {
				t.Errorf("turn %d is %v, want %s — the conversation was reordered", j, msgs[j]["role"], role)
			}
		}

		// The answer is ours, and names what the caller bought.
		var env map[string]json.RawMessage
		if err := json.Unmarshal([]byte(sent(c)), &env); err != nil {
			t.Fatalf("answer is not JSON: %v\n%s", err, sent(c))
		}
		if got := field(t, env, "model"); got != relaySku {
			t.Errorf("model = %q, want the SKU", got)
		}
	}
}

// Two kinds of field stay here: our own API's, which a strict vendor refuses, and the
// ones that buy another model, endpoint, tool or queue on our account. A vendor's own
// extra goes through. A fast request is relayed once, not raced.
func TestTheRelayWithholdsWhatIsOursAndWhatWeWouldPayFor(t *testing.T) {
	w := newRelayWorld(t, completes("ok", 3, 0, 1), never(t))
	c := w.chat(`{"model":"relay-sku","messages":[{"role":"user","content":"hi"}],"fast":true,` +
		`"models":["vendor/dearer"],"provider":{"order":["x"]},"transforms":["middle-out"],"route":"fallback",` +
		`"plugins":[{"id":"web"}],"web_search_options":{},"service_tier":"priority","top_k":5}`)
	if answered(c) != http.StatusOK {
		t.Fatalf("status %d: %s", answered(c), sent(c))
	}
	if w.a.asked() != 1 {
		t.Errorf("a fast request was sent %d time(s), want once", w.a.asked())
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(w.a.sent(t, 0), &fields)
	for _, k := range withheld {
		if _, ok := fields[k]; ok {
			t.Errorf("%q was sent upstream: %s", k, w.a.sent(t, 0))
		}
	}
	if string(fields["top_k"]) != "5" {
		t.Errorf("a vendor's own parameter was dropped: %s", w.a.sent(t, 0))
	}
}

// A caller who names no ceiling gets the reserved one, under the key the vendor
// reads: OpenAI refuses max_tokens on its reasoning models.
func TestTheReservedCeilingGoesUnderTheKeyTheVendorReads(t *testing.T) {
	w := newRelayWorld(t, completes("ok", 3, 0, 1), never(t))
	c := w.chat(`{"model":"relay-sku","messages":[{"role":"user","content":"hi"}]}`)
	if answered(c) != http.StatusOK {
		t.Fatalf("status %d: %s", answered(c), sent(c))
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(w.a.sent(t, 0), &fields)
	if _, ok := fields["max_tokens"]; ok {
		t.Errorf("max_tokens was sent to an OpenAI row: %s", w.a.sent(t, 0))
	}
	if got := string(fields["max_completion_tokens"]); got != strconv.Itoa(reserveCompletionFloor) {
		t.Errorf("max_completion_tokens = %s, want the reserved %d", got, reserveCompletionFloor)
	}

	// A ceiling above what was reserved is lowered, under the caller's own key.
	c = w.chat(`{"model":"relay-sku","max_completion_tokens":999999,"messages":[{"role":"user","content":"hi"}]}`)
	if answered(c) != http.StatusOK {
		t.Fatalf("status %d: %s", answered(c), sent(c))
	}
	var lowered map[string]json.RawMessage
	_ = json.Unmarshal(w.a.sent(t, 1), &lowered)
	if got := string(lowered["max_completion_tokens"]); got != strconv.Itoa(reserveCompletionFloor) {
		t.Errorf("max_completion_tokens = %s, want it lowered to the reserved %d", got, reserveCompletionFloor)
	}
	if _, ok := lowered["max_tokens"]; ok {
		t.Errorf("a key the caller did not use was added: %s", w.a.sent(t, 1))
	}
}

// ── failover ─────────────────────────────────────────────────────────────────

func TestAVendorFaultFailsOverToTheFallback(t *testing.T) {
	quick(t)
	w := newRelayWorld(t, declines(http.StatusServiceUnavailable, "overloaded"), completes("from b", 3, 0, 2))

	c := w.chat(`{"model":"relay-sku","temperature":0,"messages":[{"role":"user","content":"hi"}]}`)
	if answered(c) != http.StatusOK {
		t.Fatalf("status %d: %s", answered(c), sent(c))
	}
	if !strings.Contains(sent(c), "from b") {
		t.Errorf("answer = %s, want the fallback's", sent(c))
	}
	if w.a.asked() != 1 || w.b.asked() != 1 {
		t.Fatalf("asked a=%d b=%d, want one each", w.a.asked(), w.b.asked())
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(w.b.sent(t, 0), &fields)
	if string(fields["model"]) != `"b-up"` || string(fields["temperature"]) != "0" {
		t.Errorf("the fallback was sent %s, want its own id and the caller's temperature", w.b.sent(t, 0))
	}
	if !cooled.cooling(credential{"relayco", "relay-a"}) {
		t.Error("the vendor that refused was not rested")
	}
}

func TestARequestFaultStopsAndIsRelayed(t *testing.T) {
	quick(t)
	w := newRelayWorld(t, declines(http.StatusBadRequest, "messages: invalid role"), never(t))

	c := w.chat(`{"model":"relay-sku","messages":[{"role":"wizard","content":"hi"}]}`)
	if answered(c) != http.StatusBadRequest {
		t.Errorf("status %d, want the upstream's 400: %s", answered(c), sent(c))
	}
	if !strings.Contains(sent(c), "invalid role") {
		t.Errorf("body %s does not carry the upstream's reason", sent(c))
	}
	if w.b.asked() != 0 {
		t.Errorf("a malformed request was offered to a second vendor")
	}
}

// A busy vendor is waited out before it is given up on.
func TestABusyVendorIsAskedAgain(t *testing.T) {
	savedPolicy, savedConfig := defaultRetryPolicy, globalModelConfig
	t.Cleanup(func() { defaultRetryPolicy, globalModelConfig = savedPolicy, savedConfig })
	globalModelConfig = nil
	defaultRetryPolicy = retryPolicy{attempts: 2, base: time.Millisecond, max: time.Millisecond}

	w := newRelayWorld(t, func(rw http.ResponseWriter, n int) {
		if n == 0 {
			declines(http.StatusTooManyRequests, "rate limit")(rw, n)
			return
		}
		completes("second time", 3, 0, 2)(rw, n)
	}, never(t))

	c := w.chat(`{"model":"relay-sku","messages":[{"role":"user","content":"hi"}]}`)
	if answered(c) != http.StatusOK || !strings.Contains(sent(c), "second time") {
		t.Fatalf("status %d: %s", answered(c), sent(c))
	}
	if w.a.asked() != 2 {
		t.Errorf("the busy vendor was asked %d time(s), want 2", w.a.asked())
	}
}

// The seam the cascade tests script: providers answer by name, and the order they
// are asked in is the property under test.
func TestTheDialIsTheSeam(t *testing.T) {
	quick(t)
	cooled.forget()
	t.Cleanup(cooled.forget)
	var asked []string
	saved := dial
	t.Cleanup(func() { dial = saved })
	dial = func(_ context.Context, _ string, _ *object.Provider, c candidate, body func(*object.Provider) []byte) (*http.Response, *object.Provider, error) {
		asked = append(asked, c.provider)
		row := &object.Provider{Owner: "admin", Name: c.provider, Type: "DigitalOcean"}
		if c.provider == "enso" {
			return nil, row, apiErr(402, "Insufficient credits.")
		}
		var sentBody map[string]json.RawMessage
		_ = json.Unmarshal(body(row), &sentBody)
		answer := fmt.Sprintf(`{"choices":[{"index":0,"message":{"role":"assistant","content":%s}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`, sentBody["model"])
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(answer)), Header: http.Header{}}, row, nil
	}

	c := visit(http.MethodPost, "/v1/chat/completions")
	req := &openai.ChatCompletionRequest{Model: "test-model", Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "hi"}}}
	body, _ := json.Marshal(req)
	c.forward(pass{req: req, body: body, route: route("enso", "do-ai"), id: "req1", start: time.Now()})

	if strings.Join(asked, ",") != "enso,do-ai" {
		t.Errorf("asked %v, want [enso do-ai]", asked)
	}
	if !strings.Contains(sent(c), "up-do-ai") {
		t.Errorf("answer %s, want do-ai's, asked under its own id", sent(c))
	}
}

// ── streaming ───────────────────────────────────────────────────────────────

func TestARelayedStreamSettlesAtTheSkuPrice(t *testing.T) {
	const prompt, cached, completion = 200_000, 150_000, 50_000
	w := newRelayWorld(t, streams([]string{"hel", "lo"}, prompt, cached, completion), never(t))

	c := w.chat(`{"model":"relay-sku","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	out := sent(c)
	if answered(c) != http.StatusOK || !strings.Contains(out, `"content":"hel"`) {
		t.Fatalf("status %d: %s", answered(c), out)
	}
	// The caller did not ask for usage, so the chunk the relay asked for is not theirs.
	if strings.Contains(out, `"usage"`) {
		t.Errorf("a usage chunk reached a caller that did not ask for one:\n%s", out)
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(w.a.sent(t, 0), &fields)
	if string(fields["stream_options"]) != `{"include_usage":true}` {
		t.Errorf("stream_options = %s, want include_usage asked of the vendor", fields["stream_options"])
	}

	want := calculateCostCentsWithCache(relaySku, prompt-cached, completion, cached, 0)
	if uncached := calculateCostCentsWithCache(relaySku, prompt, completion, 0, 0); uncached == want {
		t.Fatal("the cached and uncached prices agree; this test cannot tell them apart")
	}
	if got := w.spent(t); got != want {
		t.Errorf("the stream cost %d cents at the SKU's price with its cache, the ledger took %d", want, got)
	}
	debit := w.paid(t)
	if debit.Model != relaySku {
		t.Errorf("billed as %q, want the SKU", debit.Model)
	}
	if bill := usageBilledUSD(&usageRecord{Model: relaySku, PromptTokens: prompt - cached, CacheReadTokens: cached, CompletionTokens: completion}); debit.USD != bill {
		t.Errorf("debit %s, want %s — the cached prompt priced as cached", debit.USD, bill)
	}
}

// A stream nobody began answering holds nothing. The vendor's 200 carried an error
// frame before any answer (opening reads it as the refusal it is), the fallback
// refused outright, and the reservation comes back whole.
func TestAStreamRefusedBeforeItsFirstByteReleasesTheHold(t *testing.T) {
	quick(t)
	w := newRelayWorld(t, func(rw http.ResponseWriter, _ int) {
		rw.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(rw, ": keep-alive\n\n")
		fmt.Fprint(rw, `data: {"error":{"message":"upstream overloaded","code":502}}`+"\n\n")
	}, declines(http.StatusServiceUnavailable, "down"))

	c := w.chat(`{"model":"relay-sku","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if answered(c) != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503 — every provider refused before a byte was sent: %s", answered(c), sent(c))
	}
	if w.b.asked() != 1 {
		t.Errorf("an error inside a 200 did not move the request; fallback asked %d time(s)", w.b.asked())
	}
	if got := w.spent(t); got != 0 {
		t.Errorf("a refused stream cost the caller %d cents", got)
	}
}

func TestAStreamThatAskedForUsageGetsIt(t *testing.T) {
	w := newRelayWorld(t, streams([]string{"hi"}, 10, 0, 2), never(t))
	c := w.chat(`{"model":"relay-sku","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`)
	if out := sent(c); !strings.Contains(out, `"usage"`) {
		t.Errorf("the caller asked for usage and was not given it:\n%s", out)
	}
}

// Once the answer has begun it belongs to the provider that began it. A stream that
// breaks after its first byte is not replayed elsewhere: the caller has already read
// part of it, and a second answer would be spliced onto the first.
func TestAStreamNeverMovesAfterItsFirstByte(t *testing.T) {
	quick(t)
	w := newRelayWorld(t, func(rw http.ResponseWriter, _ int) {
		rw.Header().Set("Content-Type", "text/event-stream")
		rw.WriteHeader(http.StatusOK)
		fmt.Fprint(rw, `data: {"choices":[{"index":0,"delta":{"content":"half an ans"}}]}`+"\n\n")
		rw.(http.Flusher).Flush()
		if conn, _, err := rw.(http.Hijacker).Hijack(); err == nil {
			_ = conn.Close()
		}
	}, never(t))

	c := w.chat(`{"model":"relay-sku","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if out := sent(c); !strings.Contains(out, "half an ans") {
		t.Errorf("the caller lost what it had already been sent:\n%s", out)
	}
	if w.b.asked() != 0 {
		t.Error("a stream that had begun was moved to another vendor")
	}
	if _, reserved, _, _ := object.GlobalBalanceLedger.Snapshot(w.subject); reserved != 0 {
		t.Errorf("%d cents still held after a broken stream", reserved)
	}
}

// ── billing ─────────────────────────────────────────────────────────────────

func TestABufferedAnswerIsBilledAsTheSkuWithItsCache(t *testing.T) {
	const prompt, cached, completion = 200_000, 150_000, 50_000
	w := newRelayWorld(t, completes("ok", prompt, cached, completion), never(t))

	c := w.chat(`{"model":"relay-sku","messages":[{"role":"user","content":"hi"}]}`)
	if answered(c) != http.StatusOK {
		t.Fatalf("status %d: %s", answered(c), sent(c))
	}
	want := calculateCostCentsWithCache(relaySku, prompt-cached, completion, cached, 0)
	if upstream := calculateCostCentsWithCache("a-up", prompt-cached, completion, cached, 0); upstream == want {
		t.Fatal("the SKU and the upstream id price the same; this test cannot tell them apart")
	}
	if got := w.spent(t); got != want {
		t.Errorf("the answer cost %d cents as the SKU, the ledger took %d", want, got)
	}
	debit := w.paid(t)
	if debit.Model != relaySku {
		t.Errorf("billed as %q, want the SKU the caller asked for", debit.Model)
	}
	if bill := usageBilledUSD(&usageRecord{Model: relaySku, PromptTokens: prompt - cached, CacheReadTokens: cached, CompletionTokens: completion}); debit.USD != bill {
		t.Errorf("debit %s, want %s", debit.USD, bill)
	}
	// The id the caller holds is the one the ledger and the routing event carry.
	var env map[string]json.RawMessage
	_ = json.Unmarshal([]byte(sent(c)), &env)
	if id := field(t, env, "id"); id != "chatcmpl-"+debit.RequestID {
		t.Errorf("answer id %q, ledger request %q — they must be one id", id, debit.RequestID)
	}
}

// ── /v1/responses ───────────────────────────────────────────────────────────

func (w *relayWorld) responses(body string) *ApiController {
	c := as(visit(http.MethodPost, "/v1/responses"), w.cred)
	c.Fiber().Request().SetBody([]byte(body))
	c.Responses()
	return c
}

func TestResponsesTextGoesThroughTheRelay(t *testing.T) {
	w := newRelayWorld(t, completes("plain answer", 5, 0, 2), never(t))
	c := w.responses(`{"model":"relay-sku","instructions":"be brief","input":"hi"}`)
	out := sent(c)
	if answered(c) != http.StatusOK || !strings.Contains(out, `"object":"response"`) || !strings.Contains(out, "plain answer") {
		t.Fatalf("status %d, want a Responses object carrying the answer:\n%s", answered(c), out)
	}
	if strings.Contains(out, "chat.completion") {
		t.Errorf("a chat completion reached a Responses client:\n%s", out)
	}
	if !strings.Contains(string(w.a.sent(t, 0)), "be brief") {
		t.Errorf("the upstream was sent %s, want the instructions", w.a.sent(t, 0))
	}
}

func TestResponsesToolCallGoesThroughTheRelay(t *testing.T) {
	w := newRelayWorld(t, func(rw http.ResponseWriter, _ int) {
		rw.Header().Set("Content-Type", "application/json")
		fmt.Fprint(rw, `{"id":"up-1","object":"chat.completion","created":1,"model":"a-up","choices":[{"index":0,`+
			`"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_9","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]},`+
			`"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}}`)
	}, never(t))
	c := w.responses(`{"model":"relay-sku","input":"look it up","tools":[{"type":"function","name":"lookup",` +
		`"parameters":{"type":"object","properties":{"q":{"type":"string"}}}}]}`)
	out := sent(c)
	if answered(c) != http.StatusOK || !strings.Contains(out, `"function_call"`) || !strings.Contains(out, "lookup") {
		t.Fatalf("status %d, want a Responses function_call:\n%s", answered(c), out)
	}
	if !strings.Contains(string(w.a.sent(t, 0)), `"tools"`) {
		t.Errorf("the tools never reached the upstream: %s", w.a.sent(t, 0))
	}
}

func TestResponsesStreamGoesThroughTheRelay(t *testing.T) {
	w := newRelayWorld(t, streams([]string{"streamed ", "answer"}, 5, 0, 2), never(t))
	c := w.responses(`{"model":"relay-sku","input":"hi","stream":true}`)
	out := sent(c)
	if !strings.Contains(out, "event: response.completed") || !strings.Contains(out, "streamed ") {
		t.Fatalf("want Responses events carrying the answer:\n%s", out)
	}
	if strings.Contains(out, "chat.completion") {
		t.Errorf("a chat chunk reached a Responses client:\n%s", out)
	}
}
