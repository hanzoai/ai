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

// envelope_test.go — what a customer is allowed to read off an answer.
//
// The bodies below are the shape measured in production on 2026-08-13: a normal,
// SUCCESSFUL enso-flash completion naming a third party the customer has no
// relationship with, in an id shape that is not ours, carrying a field we do not
// define. Each test asserts on the bytes that actually reach the wire, through the
// function that writes them, for both the streamed and the buffered path.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/web"
	"github.com/hanzoai/go-openai"
)

// The upstream chain as it arrives: aggregator id, the sub-provider it picked this
// second, its own invented finish reason and name for reasoning text — and, on the
// usage object, OUR side of the trade. What we were billed for the call, what the
// aggregator paid under us, whose account it ran on, and who it bought from.
const (
	upstreamID  = "gen-1786649556-XzO5kn4LGTuTXJkWC0FO"
	subProvider = "GMICloud"
	sku         = "enso-flash"
	ourID       = "chatcmpl-6f2b5e2a-0d1c-4f39-9c4a-8d5f7b1e2a30"
	// buyPrice is what this call cost US. It reaches the customer today.
	buyPrice      = 0.00123
	buyPriceNano  = int64(1_230_000)
	upstreamUsage = `"usage":{"prompt_tokens":14,"completion_tokens":7,"total_tokens":21,` +
		`"cost":0.00123,"is_byok":false,"provider_name":"` + subProvider + `",` +
		`"cost_details":{"upstream_inference_cost":0.00098},` +
		`"prompt_tokens_details":{"cached_tokens":2},"completion_tokens_details":{"reasoning_tokens":3}}`
	upstreamBody = `{"id":"` + upstreamID + `","provider":"` + subProvider + `",` +
		`"model":"` + sku + `","object":"chat.completion","created":1786649556,` +
		`"x_groq":{"id":"req_01jabc"},"citations":["https://example.com"],` +
		`"choices":[{"index":0,"finish_reason":"stop","native_finish_reason":"stop","logprobs":null,` +
		`"message":{"role":"assistant","content":"2 + 2 = 4","refusal":null,` +
		`"reasoning":"the user wants arithmetic","reasoning_details":[{"text":"add"}]}}],` +
		upstreamUsage + `}`
)

// upstreamRefusal is the other 200: an aggregator answers HTTP 200 and puts the
// refusal in the body, naming the sub-provider in metadata, quoting the upstream's
// raw error, and restating the upstream HTTP status as the code.
const upstreamRefusal = `{"id":"` + upstreamID + `","provider":"` + subProvider + `",` +
	`"model":"` + sku + `","object":"chat.completion","created":1786649556,` +
	`"error":{"message":"Insufficient credits.","type":"payment_error","code":402,` +
	`"metadata":{"provider_name":"` + subProvider + `","raw":"upstream said 402"}},` +
	`"choices":[]}`

// The Anthropic dialect as an aggregator relays it — the shape Claude Code and
// every agent on that SDK reads. Same tells as the chat envelope, in that dialect's
// own vocabulary, plus its own name for reasoning text.
const anthropicBody = `{"id":"` + upstreamID + `","type":"message","role":"assistant",` +
	`"provider":"` + subProvider + `","model":"qwen/qwen3-235b-a22b",` +
	`"content":[{"type":"text","text":"2 + 2 = 4","reasoning_details":[{"text":"add"}]}],` +
	`"stop_reason":"end_turn","native_finish_reason":"stop","x_groq":{"id":"req_01jabc"},` +
	`"usage":{"input_tokens":14,"output_tokens":7,"cost":0.00123,` +
	`"cost_details":{"upstream_inference_cost":0.00098},"provider_name":"` + subProvider + `"}}`

var anthropicStream = strings.Join([]string{
	`data: {"type":"message_start","message":{"id":"` + upstreamID + `","type":"message","role":"assistant","provider":"` + subProvider + `","model":"qwen/qwen3-235b-a22b","content":[],"x_groq":{"id":"req_01jabc"},"usage":{"input_tokens":14,"output_tokens":0,"cost":0.00123}}}`,
	``,
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"","native_finish_reason":null}}`,
	``,
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"2 + 2 = 4","reasoning_details":[{"text":"add"}]}}`,
	``,
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7,"cost":0.00123,"provider_name":"` + subProvider + `"}}`,
	``,
	`data: {"type":"message_stop"}`,
	``,
}, "\n")

// The embeddings dialect: an object list whose model used to be the upstream's own
// name for it, carrying the same trade on its usage.
const embeddingsBody = `{"object":"list","provider":"` + subProvider + `",` +
	`"model":"qwen/qwen3-embedding","x_groq":{"id":"req_01jabc"},` +
	`"data":[{"object":"embedding","index":0,"embedding":[0.1,0.2],"native_finish_reason":"stop"}],` +
	`"usage":{"prompt_tokens":14,"total_tokens":14,"cost":0.00123,"is_byok":false,` +
	`"provider_name":"` + subProvider + `"}}`

// upstreamStream is the same completion as SSE — most traffic is this one.
var upstreamStream = strings.Join([]string{
	`data: {"id":"` + upstreamID + `","provider":"` + subProvider + `","model":"` + sku + `","object":"chat.completion.chunk","created":1786649556,"x_groq":{"id":"req_01jabc"},"choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null,"native_finish_reason":null}]}`,
	``,
	`data: {"id":"` + upstreamID + `","provider":"` + subProvider + `","model":"` + sku + `","object":"chat.completion.chunk","created":1786649556,"x_groq":{"id":"req_01jabc"},"choices":[{"index":0,"delta":{"content":"2 + 2 "},"native_finish_reason":null}]}`,
	``,
	`data: {"id":"` + upstreamID + `","provider":"` + subProvider + `","model":"` + sku + `","object":"chat.completion.chunk","created":1786649556,"x_groq":{"id":"req_01jabc"},"choices":[{"index":0,"delta":{"content":"= 4","reasoning":"adding"},"native_finish_reason":null}]}`,
	``,
	`data: {"id":"` + upstreamID + `","provider":"` + subProvider + `","model":"` + sku + `","object":"chat.completion.chunk","created":1786649556,"x_groq":{"id":"req_01jabc"},"choices":[{"index":0,"delta":{},"finish_reason":"stop","native_finish_reason":"stop"}]}`,
	``,
	`data: {"id":"` + upstreamID + `","provider":"` + subProvider + `","model":"` + sku + `","object":"chat.completion.chunk","created":1786649556,"x_groq":{"id":"req_01jabc"},"choices":[],` + upstreamUsage + `}`,
	``,
	`data: [DONE]`,
	``,
}, "\n")

func ourMark() *mark { return &mark{id: ourID, model: sku, seller: "hanzo"} }

// discloses names every upstream tell found in bytes headed for a customer. It reads
// the raw text on purpose: a leak that survives by hiding in a field nobody thought
// to parse is still a leak.
func discloses(t *testing.T, what string, out []byte) {
	t.Helper()
	for _, tell := range []string{subProvider, "gen-1786649556", "native_finish_reason",
		"x_groq", "req_01jabc", "citations", "reasoning_details",
		// Our side of the trade. A customer reading their own receipt must not be
		// able to read what we paid for it.
		`"cost"`, "cost_details", "upstream_inference_cost", "is_byok", "provider_name",
		"0.00123", "0.00098"} {
		if strings.Contains(string(out), tell) {
			t.Errorf("%s discloses %q:\n%s", what, tell, out)
		}
	}
}

// chunks returns the JSON payload of every SSE data event, `[DONE]` excluded.
func chunks(t *testing.T, sse string) []map[string]json.RawMessage {
	t.Helper()
	var out []map[string]json.RawMessage
	for _, line := range strings.Split(sse, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if raw == "[DONE]" {
			continue
		}
		var event map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			t.Fatalf("relayed a non-JSON event %q: %v", raw, err)
		}
		out = append(out, event)
	}
	if len(out) == 0 {
		t.Fatal("no events relayed")
	}
	return out
}

func field(t *testing.T, event map[string]json.RawMessage, key string) string {
	t.Helper()
	var s string
	if err := json.Unmarshal(event[key], &s); err != nil {
		t.Fatalf("%s = %s, want a string: %v", key, event[key], err)
	}
	return s
}

// TestFamilyStreamIsOurs is the one that matters: SSE is how a chat is served, so a
// fix that only covers the buffered body leaks on nearly every request. This drives
// the real relay — the function that writes the customer's bytes.
func TestFamilyStreamIsOurs(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx := web.NewContext()
	ctx.Reset(rec, httptest.NewRequest("POST", "/v1/chat/completions", nil))
	c := &ApiController{}
	c.Init(ctx, "ApiController", "X", nil)

	mk := ourMark()
	prompt, completion, _, _ := c.relayZenStream(strings.NewReader(upstreamStream), mk)

	out := rec.Body.String()
	discloses(t, "streamed chat", []byte(out))

	// Every chunk names us, the SKU, and one id — a client correlates on it, so it
	// cannot drift mid-stream.
	var content strings.Builder
	for i, event := range chunks(t, out) {
		if got := field(t, event, "id"); got != ourID {
			t.Errorf("chunk %d id = %q, want %q on every chunk", i, got, ourID)
		}
		if got := field(t, event, "provider"); got != "hanzo" {
			t.Errorf("chunk %d provider = %q, want hanzo", i, got)
		}
		if got := field(t, event, "model"); got != sku {
			t.Errorf("chunk %d model = %q, want the SKU %q", i, got, sku)
		}
		var choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		}
		if err := json.Unmarshal(event["choices"], &choices); err != nil {
			t.Fatalf("chunk %d choices unreadable: %v", i, err)
		}
		for _, ch := range choices {
			content.WriteString(ch.Delta.Content)
		}
	}

	// The wire still carries what the customer bought.
	if content.String() != "2 + 2 = 4" {
		t.Errorf("assembled content = %q, want %q", content.String(), "2 + 2 = 4")
	}
	if !strings.Contains(out, `"usage":{"completion_tokens":7,"prompt_tokens":14,"total_tokens":21}`) &&
		!strings.Contains(out, `"prompt_tokens":14`) {
		t.Errorf("usage must reach the client unchanged, got:\n%s", out)
	}
	if !strings.Contains(out, `"finish_reason":"stop"`) {
		t.Errorf("finish_reason must survive, got:\n%s", out)
	}
	if prompt != 14 || completion != 7 {
		t.Errorf("billing read (%d,%d) off the stream, want (14,7)", prompt, completion)
	}

	// The upstream is kept, just not published — this is the value the usage row's
	// origin column records.
	if mk.upstream != subProvider {
		t.Errorf("mark.upstream = %q, want %q kept for metering", mk.upstream, subProvider)
	}
	// Streaming is where usage actually arrives — on the last chunk — so it is the
	// path the capture has to work on.
	if mk.cost == nil || *mk.cost != buyPriceNano {
		t.Errorf("mark.cost = %v, want %d nano-USD off the usage chunk", mk.cost, buyPriceNano)
	}
	for _, event := range chunks(t, out) {
		usage := map[string]json.RawMessage{}
		if len(event["usage"]) == 0 {
			continue
		}
		if err := json.Unmarshal(event["usage"], &usage); err != nil {
			t.Fatalf("usage unreadable: %v", err)
		}
		for key := range usage {
			if !usageFields[key] {
				t.Errorf("streamed usage carries %q — our buy price rides the last chunk of "+
					"nearly every chat, so a buffered-only prune leaks on almost all of them", key)
			}
		}
	}
}

// B3. A refusal is an answer, so `error` is published — and it was published
// WHOLE, which made it the one field where everything the rest of the envelope
// removes could still walk out.
//
// The envelope then said provider:"hanzo" while error.metadata.provider_name said
// otherwise, which is worse than either alone: the answer contradicts itself and
// the customer is the one holding both halves.
func TestARefusalIsPrunedWithoutBeingGutted(t *testing.T) {
	mk := ourMark()
	out := mk.stamp([]byte(upstreamRefusal))
	discloses(t, "refusal inside a 200", out)

	var env map[string]json.RawMessage
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("stamped refusal is not JSON: %v", err)
	}
	if got := field(t, env, "provider"); got != "hanzo" {
		t.Errorf("provider = %q, want hanzo", got)
	}

	refusal := map[string]json.RawMessage{}
	if err := json.Unmarshal(env["error"], &refusal); err != nil {
		t.Fatalf("error unreadable: %v", err)
	}
	for key := range refusal {
		if !errorFields[key] {
			t.Errorf("the refusal carries %q — the envelope says one thing about who "+
				"served this and its error says another", key)
		}
	}

	// Not gutted: the diagnostic is the whole point of publishing it.
	if got := field(t, refusal, "message"); got != "Insufficient credits." {
		t.Errorf("message = %q — a refusal a customer cannot read helps nobody", got)
	}
	if got := field(t, refusal, "type"); got != "payment_error" {
		t.Errorf("type = %q, want it kept", got)
	}

	// The upstream status must not come back inside the body after the status line
	// declined to say it. 402 tells the customer THEY owe money; our account is the
	// empty one.
	if raw, ok := refusal["code"]; ok {
		t.Errorf("code = %s — a numeric code restates the upstream HTTP status, and this "+
			"one bills the customer for our empty vendor account", raw)
	}

	// A string code is a machine-readable diagnostic in our own schema and stays.
	kept := map[string]json.RawMessage{}
	textCode := mk.stamp([]byte(`{"error":{"message":"too long","code":"context_length_exceeded"}}`))
	if err := json.Unmarshal(textCode, &env); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if err := json.Unmarshal(env["error"], &kept); err != nil {
		t.Fatalf("error unreadable: %v", err)
	}
	if got := field(t, kept, "code"); got != "context_length_exceeded" {
		t.Errorf("code = %q, want the string code kept — that is the one an SDK switches on", got)
	}
}

// A tool call as it arrives, with everything a client actually acts on: the
// function it wants called, the arguments, the id it will answer under, and the
// finish_reason that tells an SDK to go and run it.
const toolBody = `{"id":"` + upstreamID + `","provider":"` + subProvider + `",` +
	`"model":"` + sku + `","object":"chat.completion","created":1786649556,` +
	`"system_fingerprint":"fp_01","x_groq":{"id":"req_01jabc"},` +
	`"choices":[{"index":0,"finish_reason":"tool_calls","native_finish_reason":"tool_calls",` +
	`"logprobs":{"content":[]},"content_filter_results":{"hate":{"filtered":false}},` +
	`"message":{"role":"assistant","content":null,"refusal":null,` +
	`"reasoning_content":"the user wants weather","reasoning_details":[{"text":"x"}],` +
	`"tool_calls":[{"id":"call_1","type":"function",` +
	`"function":{"name":"get_weather","arguments":"{\"city\":\"SF\"}"}}]}}]}`

const toolStream = `data: {"id":"` + upstreamID + `","provider":"` + subProvider + `","model":"` + sku +
	`","object":"chat.completion.chunk","created":1,"choices":[{"index":0,"native_finish_reason":null,` +
	`"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function",` +
	`"function":{"name":"get_weather","arguments":"{\"city\":\"SF\"}"}}]}}]}`

// The allowlist tests elsewhere in this file walk the OUTPUT and check each key
// against the same set the door filtered by. That answers "did the door apply its
// list", which it always did — it cannot answer "is the list right", because
// narrowing the list narrows the output and the check with it.
//
// Measured: delete "tool_calls" from messageFields and every one of those tests
// stays green while function calling stops working for every relayed response.
//
// So this one names what must SURVIVE, and asserts the values, not just the keys.
func TestTheDoorKeepsWhatAClientActsOn(t *testing.T) {
	t.Run("buffered", func(t *testing.T) {
		mk := ourMark()
		out := mk.stamp([]byte(toolBody))
		discloses(t, "tool call", out)

		var env map[string]json.RawMessage
		if err := json.Unmarshal(out, &env); err != nil {
			t.Fatalf("not JSON: %v", err)
		}
		for _, need := range []string{"id", "object", "created", "model", "provider", "choices",
			"system_fingerprint"} {
			if _, ok := env[need]; !ok {
				t.Errorf("the envelope lost %q", need)
			}
		}
		var choices []map[string]json.RawMessage
		if err := json.Unmarshal(env["choices"], &choices); err != nil || len(choices) != 1 {
			t.Fatalf("choices unreadable: %v", err)
		}
		for _, need := range []string{"index", "message", "finish_reason", "logprobs",
			"content_filter_results"} {
			if _, ok := choices[0][need]; !ok {
				t.Errorf("the choice lost %q", need)
			}
		}
		if got := field(t, choices[0], "finish_reason"); got != "tool_calls" {
			t.Errorf("finish_reason = %q — an SDK reads this to decide whether to run the tool", got)
		}
		var message map[string]json.RawMessage
		if err := json.Unmarshal(choices[0]["message"], &message); err != nil {
			t.Fatalf("message unreadable: %v", err)
		}
		for _, need := range []string{"role", "content", "refusal", "reasoning_content", "tool_calls"} {
			if _, ok := message[need]; !ok {
				t.Errorf("the message lost %q", need)
			}
		}
		// The values, not just the key: a tool_calls array that survived as an empty
		// shape is the same outage as one that did not survive.
		call := string(message["tool_calls"])
		for _, need := range []string{`"id":"call_1"`, `"name":"get_weather"`, `"city\":\"SF`} {
			if !strings.Contains(call, need) {
				t.Errorf("tool_calls lost %s: %s", need, call)
			}
		}
	})

	t.Run("streamed delta", func(t *testing.T) {
		rec := httptest.NewRecorder()
		ctx := web.NewContext()
		ctx.Reset(rec, httptest.NewRequest("POST", "/v1/chat/completions", nil))
		c := &ApiController{}
		c.Init(ctx, "ApiController", "X", nil)

		c.relayZenStream(strings.NewReader(toolStream+"\n\ndata: [DONE]\n\n"), ourMark())
		out := rec.Body.String()
		discloses(t, "streamed tool call", []byte(out))
		for _, need := range []string{`"name":"get_weather"`, `"id":"call_1"`, `"tool_calls"`} {
			if !strings.Contains(out, need) {
				t.Errorf("the streamed delta lost %s:\n%s", need, out)
			}
		}
	})
}

// B1. The Anthropic dialect had no door at all — it was relayed verbatim, which
// means every tell the chat path stopped publishing kept going out on the
// endpoint agents actually use.
func TestTheAnthropicDialectIsOurs(t *testing.T) {
	ourMsg := "msg_6f2b5e2a"

	t.Run("buffered", func(t *testing.T) {
		mk := &mark{id: ourMsg, model: sku, seller: "hanzo", speaks: messageShape}
		out := mk.stamp([]byte(anthropicBody))
		discloses(t, "anthropic message", out)

		var msg map[string]json.RawMessage
		if err := json.Unmarshal(out, &msg); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, out)
		}
		for key := range msg {
			if !bodyFields[key] {
				t.Errorf("message carries unpublished field %q", key)
			}
		}
		if got := field(t, msg, "id"); got != ourMsg {
			t.Errorf("id = %q, want ours — a gen- id is not an id we can be asked about", got)
		}
		if got := field(t, msg, "model"); got != sku {
			t.Errorf("model = %q, want the SKU the caller asked for", got)
		}
		if got := field(t, msg, "stop_reason"); got != "end_turn" {
			t.Errorf("stop_reason = %q, want it kept — it is the dialect's own field", got)
		}
		if !strings.Contains(string(out), "2 + 2 = 4") {
			t.Errorf("the answer itself was lost:\n%s", out)
		}
		// The price is taken on the way past here too.
		if mk.cost == nil || *mk.cost != buyPriceNano {
			t.Errorf("mark.cost = %v, want %d — the COGS is stated in every dialect", mk.cost, buyPriceNano)
		}
		// Token counts survive under this dialect's own names.
		usage := map[string]json.RawMessage{}
		if err := json.Unmarshal(msg["usage"], &usage); err != nil {
			t.Fatalf("usage unreadable: %v", err)
		}
		if string(usage["input_tokens"]) != "14" || string(usage["output_tokens"]) != "7" {
			t.Errorf("usage = %s, want the counts kept", msg["usage"])
		}
		for key := range usage {
			if !tokenFields[key] {
				t.Errorf("usage carries %q — our buy price, in the other dialect", key)
			}
		}
	})

	t.Run("streamed", func(t *testing.T) {
		rec := httptest.NewRecorder()
		ctx := web.NewContext()
		ctx.Reset(rec, httptest.NewRequest("POST", "/v1/messages", nil))
		c := &ApiController{}
		c.Init(ctx, "ApiController", "X", nil)

		mk := &mark{id: ourMsg, model: sku, seller: "hanzo", speaks: messageShape}
		c.relayZenStream(strings.NewReader(anthropicStream), mk)

		out := rec.Body.String()
		discloses(t, "anthropic stream", []byte(out))
		if !strings.Contains(out, "2 + 2 = 4") {
			t.Errorf("the streamed answer was lost:\n%s", out)
		}
		if !strings.Contains(out, ourMsg) {
			t.Errorf("message_start never carried our id:\n%s", out)
		}
		if mk.upstream != subProvider {
			t.Errorf("mark.upstream = %q, want %q kept for metering", mk.upstream, subProvider)
		}
		for _, event := range chunks(t, out) {
			for key := range event {
				if !eventFields[key] && !bodyFields[key] {
					t.Errorf("event carries unpublished field %q", key)
				}
			}
		}
	})
}

// The door has to be REACHED, not just be correct. Both dialects were already
// normalisable before this: what was missing was that pipeToFamily minted no mark
// for either, so every assertion above would have gone on passing while the wire
// carried the upstream whole. This drives the real relay for all three dialects.
func TestEveryRelayedDialectReachesTheDoor(t *testing.T) {
	for _, relay := range []struct {
		name    string
		apiPath string
		dialect string
		stream  bool
		answer  string
	}{
		{"chat buffered", "chat/completions", "openai", false, upstreamBody},
		{"chat streamed", "chat/completions", "openai", true, upstreamStream},
		{"anthropic buffered", "messages", "anthropic", false, anthropicBody},
		{"anthropic streamed", "messages", "anthropic", true, anthropicStream},
		{"embeddings", "embeddings", "openai", false, embeddingsBody},
	} {
		t.Run(relay.name, func(t *testing.T) {
			family := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if relay.stream {
					w.Header().Set("Content-Type", "text/event-stream")
				} else {
					w.Header().Set("Content-Type", "application/json")
				}
				_, _ = w.Write([]byte(relay.answer))
			}))
			defer family.Close()

			fam := &modelFamily{
				name: "enso", prefix: "enso",
				providerFn: func() *object.Provider {
					return &object.Provider{Owner: "admin", Name: "enso", Type: "Enso", ProviderUrl: family.URL}
				},
			}

			body := []byte(`{"model":"` + sku + `","messages":[{"role":"user","content":"2+2?"}]}`)
			rec := httptest.NewRecorder()
			ctx := web.NewContext()
			ctx.Reset(rec, httptest.NewRequest("POST", "/v1/x", strings.NewReader(string(body))))
			c := &ApiController{}
			c.Init(ctx, "ApiController", "X", nil)

			c.pipeToFamily(fam, relay.apiPath, relay.dialect, sku, body, relay.stream,
				"", nil, false, nil, time.Now())

			out := rec.Body.String()
			if out == "" {
				t.Fatal("nothing was relayed")
			}
			discloses(t, relay.name+" through the pipe", []byte(out))
			if strings.Contains(out, "qwen/qwen3-235b-a22b") || strings.Contains(out, "qwen/qwen3-embedding") {
				t.Errorf("%s relays the upstream's own model id:\n%s", relay.name, out)
			}
		})
	}
}

// B2. Embeddings had no door either, and its `model` was whatever the upstream
// called the model rather than the SKU the caller asked for.
func TestTheEmbeddingsDialectIsOurs(t *testing.T) {
	mk := &mark{model: "enso-embed", seller: "hanzo", speaks: listShape}
	out := mk.stamp([]byte(embeddingsBody))
	discloses(t, "embeddings list", out)

	var env map[string]json.RawMessage
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	for key := range env {
		if !listFields[key] {
			t.Errorf("list carries unpublished field %q", key)
		}
	}
	if got := field(t, env, "model"); got != "enso-embed" {
		t.Errorf("model = %q, want the SKU asked for — the caller never named the upstream's", got)
	}
	// The vectors are the answer and must survive intact.
	var data []map[string]json.RawMessage
	if err := json.Unmarshal(env["data"], &data); err != nil {
		t.Fatalf("data unreadable: %v", err)
	}
	if len(data) != 1 || string(data[0]["embedding"]) != "[0.1,0.2]" {
		t.Fatalf("the vectors changed: %s", env["data"])
	}
	for key := range data[0] {
		if !vectorFields[key] {
			t.Errorf("vector carries unpublished field %q", key)
		}
	}
	if mk.cost == nil || *mk.cost != buyPriceNano {
		t.Errorf("mark.cost = %v, want %d", mk.cost, buyPriceNano)
	}
}

// TestFamilyBodyIsOurs covers the buffered path — the exact bytes measured in
// production.
func TestFamilyBodyIsOurs(t *testing.T) {
	mk := ourMark()
	out := mk.stamp([]byte(upstreamBody))
	discloses(t, "buffered chat", out)

	var env map[string]json.RawMessage
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("stamped body is not JSON: %v", err)
	}
	if got := field(t, env, "id"); got != ourID {
		t.Errorf("id = %q, want %q", got, ourID)
	}
	if got := field(t, env, "provider"); got != "hanzo" {
		t.Errorf("provider = %q, want hanzo", got)
	}
	if got := field(t, env, "model"); got != sku {
		t.Errorf("model = %q, want the SKU %q", got, sku)
	}
	// The customer's own numbers survive; our trade does not travel with them.
	usage := map[string]json.RawMessage{}
	if err := json.Unmarshal(env["usage"], &usage); err != nil {
		t.Fatalf("usage unreadable: %v", err)
	}
	for key := range usage {
		if !usageFields[key] {
			t.Errorf("usage carries %q — that is our side of the trade, not theirs", key)
		}
	}
	for _, want := range []string{"prompt_tokens", "completion_tokens", "total_tokens",
		"prompt_tokens_details", "completion_tokens_details"} {
		if _, ok := usage[want]; !ok {
			t.Errorf("usage lost %q — a customer bills, caches and budgets on these", want)
		}
	}
	if got := string(usage["prompt_tokens"]); got != "14" {
		t.Errorf("prompt_tokens = %s, want 14 — pruning must not touch the counts", got)
	}
	// Captured on the way past: the price we paid is what the ledger was missing.
	if mk.cost == nil {
		t.Fatal("the buy price was dropped without being kept — the COGS the business " +
			"cannot see is exactly this number, and it was in the answer all along")
	}
	if *mk.cost != buyPriceNano {
		t.Errorf("mark.cost = %d nano-USD, want %d (%v USD)", *mk.cost, buyPriceNano, buyPrice)
	}
	if got := string(env["object"]); got != `"chat.completion"` {
		t.Errorf("object = %s, want it unchanged", got)
	}

	// The answer itself is untouched; only what the envelope says about its origin
	// moves.
	var choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(env["choices"], &choices); err != nil {
		t.Fatalf("choices unreadable: %v", err)
	}
	if len(choices) != 1 || choices[0].Message.Content != "2 + 2 = 4" ||
		choices[0].Message.Role != "assistant" || choices[0].FinishReason != "stop" {
		t.Errorf("answer changed: %+v", choices)
	}
	if mk.upstream != subProvider {
		t.Errorf("mark.upstream = %q, want %q kept for metering", mk.upstream, subProvider)
	}

	// Nothing our schema does not publish survives, at any of the three levels an
	// aggregator writes into. Naming the leaked vendors instead would be a list that
	// is wrong the day a new sub-provider appears.
	for key := range env {
		if !chatFields[key] {
			t.Errorf("envelope carries unpublished field %q", key)
		}
	}
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(env["choices"], &raw); err != nil {
		t.Fatalf("choices unreadable: %v", err)
	}
	for key := range raw[0] {
		if !choiceFields[key] {
			t.Errorf("choice carries unpublished field %q", key)
		}
	}
	var message map[string]json.RawMessage
	if err := json.Unmarshal(raw[0]["message"], &message); err != nil {
		t.Fatalf("message unreadable: %v", err)
	}
	for key := range message {
		if !messageFields[key] {
			t.Errorf("message carries unpublished field %q", key)
		}
	}
}

// TestPipeToFamilyIsOurs drives the whole family relay — both modes — against a
// family answering exactly as production did. It tests the WIRING: without it,
// deleting the stamp at either call site leaves every other test here green.
func TestPipeToFamilyIsOurs(t *testing.T) {
	for _, mode := range []struct {
		name   string
		stream bool
		answer string
	}{
		{"buffered", false, upstreamBody},
		{"streamed", true, upstreamStream},
	} {
		t.Run(mode.name, func(t *testing.T) {
			family := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode.stream {
					w.Header().Set("Content-Type", "text/event-stream")
				} else {
					w.Header().Set("Content-Type", "application/json")
				}
				_, _ = w.Write([]byte(mode.answer))
			}))
			defer family.Close()

			fam := &modelFamily{
				name: "enso", prefix: "enso",
				providerFn: func() *object.Provider {
					return &object.Provider{Owner: "admin", Name: "enso", Type: "Enso", ProviderUrl: family.URL}
				},
			}

			body := []byte(`{"model":"` + sku + `","messages":[{"role":"user","content":"2+2?"}]}`)
			rec := httptest.NewRecorder()
			ctx := web.NewContext()
			ctx.Reset(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(body))))
			c := &ApiController{}
			c.Init(ctx, "ApiController", "X", nil)

			c.pipeToFamily(fam, "chat/completions", "openai", sku, body, mode.stream,
				"", nil, false, nil, time.Now())

			out := rec.Body.String()
			discloses(t, mode.name+" relay", []byte(out))

			events := []map[string]json.RawMessage{}
			if mode.stream {
				events = chunks(t, out)
			} else {
				var env map[string]json.RawMessage
				if err := json.Unmarshal([]byte(out), &env); err != nil {
					t.Fatalf("relayed body is not JSON: %v\n%s", err, out)
				}
				events = append(events, env)
			}
			var id string
			for i, env := range events {
				if got := field(t, env, "provider"); got != "hanzo" {
					t.Errorf("event %d provider = %q, want hanzo", i, got)
				}
				if got := field(t, env, "model"); got != sku {
					t.Errorf("event %d model = %q, want the SKU", i, got)
				}
				got := field(t, env, "id")
				if !strings.HasPrefix(got, "chatcmpl-") || got == upstreamID {
					t.Errorf("event %d id = %q, want one of ours", i, got)
				}
				if id == "" {
					id = got
				} else if got != id {
					t.Errorf("event %d id = %q, want %q — one id per completion", i, got, id)
				}
			}
		})
	}
}

// TestToolStreamIsOurs covers the second relay — tool calls and vision go out
// through streamCaptureUsage, which forwards an upstream's SSE while billing it.
func TestToolStreamIsOurs(t *testing.T) {
	var out strings.Builder
	mk := ourMark()
	prompt, completion, _, text := streamCaptureUsage(
		strings.NewReader(upstreamStream), &out, nil, true, nil, mk,
	)
	discloses(t, "streamed tool call", []byte(out.String()))

	for i, event := range chunks(t, out.String()) {
		if got := field(t, event, "id"); got != ourID {
			t.Errorf("chunk %d id = %q, want %q", i, got, ourID)
		}
		if got := field(t, event, "provider"); got != "hanzo" {
			t.Errorf("chunk %d provider = %q, want hanzo", i, got)
		}
		if got := field(t, event, "model"); got != sku {
			t.Errorf("chunk %d model = %q, want the SKU", i, got)
		}
	}
	if prompt != 14 || completion != 7 {
		t.Errorf("billing read (%d,%d), want (14,7) — stamping must not touch the money", prompt, completion)
	}
	if text != "2 + 2 = 4" {
		t.Errorf("billing text = %q, want the whole completion", text)
	}
	if mk.upstream != subProvider {
		t.Errorf("mark.upstream = %q, want %q kept for metering", mk.upstream, subProvider)
	}
	if mk.cost == nil || *mk.cost != buyPriceNano {
		t.Errorf("mark.cost = %v, want %d nano-USD — the tool relay meters from the same mark",
			mk.cost, buyPriceNano)
	}
}

// TestProxyToolRequestIsOurs drives the second relay end to end — the path a tool
// call or an image takes to an OpenAI-compatible upstream. Same wiring argument as
// the family test: the door has to be reached, not just be correct.
func TestProxyToolRequestIsOurs(t *testing.T) {
	for _, mode := range []struct {
		name   string
		stream bool
		answer string
	}{
		{"buffered", false, upstreamBody},
		{"streamed", true, upstreamStream},
	} {
		t.Run(mode.name, func(t *testing.T) {
			// This path dials the upstream under the upstream's OWN name for the
			// model, so that is the name the upstream echoes back.
			answer := strings.ReplaceAll(mode.answer, `"model":"`+sku+`"`, `"model":"qwen/qwen3-235b-a22b"`)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(answer))
			}))
			defer upstream.Close()

			rec := httptest.NewRecorder()
			ctx := web.NewContext()
			ctx.Reset(rec, httptest.NewRequest("POST", "/v1/chat/completions", nil))
			c := &ApiController{}
			c.Init(ctx, "ApiController", "X", nil)

			// The SKU the caller asked for. proxyToolRequest replaces it with the
			// upstream's own name for the model before dialling, which is exactly why
			// the response used to hand back a name the caller never asked for.
			request := openai.ChatCompletionRequest{Model: sku, Stream: mode.stream}
			provider := &object.Provider{
				Owner: "admin", Name: "shared", Type: "OpenAI",
				SubType: "qwen/qwen3-235b-a22b", ProviderUrl: upstream.URL,
			}
			c.proxyToolRequest(provider, &request, time.Now(), nil, false, "", nil)

			out := rec.Body.String()
			discloses(t, mode.name+" tool relay", []byte(out))
			if strings.Contains(out, "qwen/qwen3-235b-a22b") {
				t.Errorf("%s tool relay names the upstream's own model id:\n%s", mode.name, out)
			}

			events := []map[string]json.RawMessage{}
			if mode.stream {
				events = chunks(t, out)
			} else {
				var env map[string]json.RawMessage
				if err := json.Unmarshal([]byte(out), &env); err != nil {
					t.Fatalf("relayed body is not JSON: %v\n%s", err, out)
				}
				events = append(events, env)
			}
			for i, env := range events {
				if got := field(t, env, "provider"); got != "hanzo" {
					t.Errorf("event %d provider = %q, want hanzo", i, got)
				}
				if got := field(t, env, "model"); got != sku {
					t.Errorf("event %d model = %q, want the SKU the caller asked for", i, got)
				}
				if got := field(t, env, "id"); !strings.HasPrefix(got, "chatcmpl-") {
					t.Errorf("event %d id = %q, want one of ours", i, got)
				}
			}
		})
	}
}

// TestModelStaysTheSku: "the model" means different things at the two ends of the
// relay. When an upstream answers under the name of the arm it actually ran, the
// customer still bought enso-flash and their client still routes on that string.
func TestModelStaysTheSku(t *testing.T) {
	arm := `"model":"qwen/qwen3-235b-a22b"`
	for _, payload := range []string{
		strings.Replace(upstreamBody, `"model":"`+sku+`"`, arm, 1),
		strings.Replace(strings.SplitN(upstreamStream, "\n", 2)[0][len("data: "):],
			`"model":"`+sku+`"`, arm, 1),
	} {
		out := ourMark().stamp([]byte(payload))
		var env map[string]json.RawMessage
		if err := json.Unmarshal(out, &env); err != nil {
			t.Fatalf("stamped payload is not JSON: %v", err)
		}
		if got := field(t, env, "model"); got != sku {
			t.Errorf("model = %q, want the SKU %q", got, sku)
		}
		if strings.Contains(string(out), "qwen/qwen3-235b-a22b") {
			t.Errorf("the arm the upstream ran reached the customer:\n%s", out)
		}
	}
}

// TestSellerNamesWhoSold pins the lens. Our key sells as Hanzo whatever it routes
// through; a customer's own connected account names the vendor they connected,
// because they pay that vendor directly and hiding it would be the same lie pointed
// the other way. BYOK is read from the row's ownership — the same fact metering
// bills on — never guessed from the request.
func TestSellerNamesWhoSold(t *testing.T) {
	user := &iam.User{Owner: "acme"}
	ours := &object.Provider{Owner: "admin", Name: "openrouter-shared", Type: "OpenRouter"}
	theirs := &object.Provider{Owner: "acme", Name: "acme-key", Type: "OpenRouter"}

	if got := seller(ours, user); got != "hanzo" {
		t.Errorf("seller(hanzo-owned) = %q, want hanzo", got)
	}
	if got := seller(theirs, user); got != "OpenRouter" {
		t.Errorf("seller(customer-owned) = %q, want the vendor they connected", got)
	}
	// No principal, or no provider row: nothing proves a customer relationship, so
	// the answer is the one that discloses less.
	if got := seller(theirs, nil); got != "hanzo" {
		t.Errorf("seller(no user) = %q, want hanzo", got)
	}

	// And the lens is what reaches the wire.
	byok := &mark{id: ourID, model: sku, seller: seller(theirs, user)}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(byok.stamp([]byte(upstreamBody)), &env); err != nil {
		t.Fatalf("stamped body is not JSON: %v", err)
	}
	if got := field(t, env, "provider"); got != "OpenRouter" {
		t.Errorf("BYOK provider = %q, want the vendor the customer connected", got)
	}
}

// TestOriginKeepsTheChain: the response stops naming the upstream, the usage row
// starts. Deleting it instead would leave nothing able to say which account the
// money was spent with.
func TestOriginOfKeepsTheChain(t *testing.T) {
	provider := &object.Provider{Owner: "admin", Name: "enso", ProviderUrl: "https://enso.hanzo.ai"}

	mk := ourMark()
	if got := originOf(provider, mk); got != "enso.hanzo.ai" {
		t.Errorf("originOf before any answer = %q, want the host we dialled", got)
	}
	mk.stamp([]byte(upstreamBody))
	if got := originOf(provider, mk); got != subProvider {
		t.Errorf("originOf after the answer = %q, want the upstream it disclosed (%q)", got, subProvider)
	}
	// A dialect we relay verbatim has no mark and still answers the question.
	if got := originOf(provider, nil); got != "enso.hanzo.ai" {
		t.Errorf("originOf(unmarked) = %q, want the host we dialled", got)
	}
}

// TestOurIdJoinsTheLedger: the id a client correlates on is the id this call is
// metered under, so a reward threaded back to /v1/feedback lands on the row that
// paid for it. Relaying the aggregator's id put the client on one key and the ledger
// on another.
func TestOurIdJoinsTheLedger(t *testing.T) {
	const reqID = "6f2b5e2a-0d1c-4f39-9c4a-8d5f7b1e2a30"
	mk := &mark{id: "chatcmpl-" + reqID, model: sku, seller: "hanzo"}

	var env map[string]json.RawMessage
	if err := json.Unmarshal(mk.stamp([]byte(upstreamBody)), &env); err != nil {
		t.Fatalf("stamped body is not JSON: %v", err)
	}
	if got := object.NormalizeRequestId(field(t, env, "id")); got != reqID {
		t.Errorf("id normalizes to %q, want the metered request id %q", got, reqID)
	}
}

// TestStampLeavesWhatIsNotOursAlone: the door rewrites completion envelopes and
// nothing else. An SSE terminator, an Anthropic event, an unmarked relay — all pass
// through as they arrived, because a door that rewrites what it does not understand
// breaks more than it fixes.
func TestStampLeavesWhatIsNotOursAlone(t *testing.T) {
	var none *mark
	for _, payload := range []string{"[DONE]", `"a string"`, `[1,2,3]`, upstreamBody} {
		if got := string(none.stamp([]byte(payload))); got != payload {
			t.Errorf("nil mark rewrote %q to %q", payload, got)
		}
	}
	mk := ourMark()
	for _, payload := range []string{"[DONE]", `"a string"`, `[1,2,3]`, ``} {
		if got := string(mk.stamp([]byte(payload))); got != payload {
			t.Errorf("stamp rewrote non-envelope %q to %q", payload, got)
		}
	}
	// A refusal is an answer: the error survives rather than being allowlisted away.
	// Its INTERIOR is normalised like every other level — see
	// TestARefusalIsPrunedWithoutBeingGutted, which is where "relayed whole" turned
	// out to mean "relayed with the vendor's name and the upstream's status in it".
	refusal := `{"error":{"code":429,"message":"rate limited"}}`
	var env map[string]json.RawMessage
	if err := json.Unmarshal(mk.stamp([]byte(refusal)), &env); err != nil {
		t.Fatalf("stamped refusal is not JSON: %v", err)
	}
	if string(env["error"]) != `{"message":"rate limited"}` {
		t.Errorf("error = %s, want the message kept and the restated HTTP status dropped", env["error"])
	}
}
