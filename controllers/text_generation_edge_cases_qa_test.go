// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controllers

// text_generation_edge_cases_qa_test.go — the QA suite for issue #132: the six ways a
// text generation request goes sideways, exercised end to end through the relay.
//
// The pieces are unit-tested apart (outbound builds the body, envelope stamps the
// answer, billing_reserve copies a stream). What was never proven together is what the
// issue asks about: that a 400-turn conversation reaches the vendor whole with its
// needle in the turn it was written into; that strict JSON modes arrive as written and
// a vendor that answers them with prose is caught rather than billed quietly; that tool
// call ids survive both a buffered answer and a stream that splits one call across
// deltas; that reasoning text is neither invented nor lost, and an inlined thinking
// block is stripped from the answer while still being paid for; that a truncated answer
// says so with finish_reason "length"; and that a stream is closed honestly — the
// sentinel owed when the vendor finished, the truth when it did not.
//
// Everything here is hermetic: scripted OpenAI-compatible upstreams on httptest, the
// same relayWorld the failover tests drive. Nothing needs live api.hanzo.ai
// credentials; each case is one standard /v1/chat/completions body away from being
// replayed against it.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/hanzoai/ai/object"
)

// ── scripted answers this suite needs and forward_test.go does not have ──────

// answersBody is a buffered 200 whose whole body the test wrote, so a case can be
// about an answer shape (two tool calls, a thinking block, a length finish) rather
// than about how a vendor chooses to spell one.
func answersBody(body string) func(http.ResponseWriter, int) {
	return func(w http.ResponseWriter, _ int) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}
}

// sseOf writes each frame as one SSE data event exactly as written — including none at
// the end, which is how a vendor that forgot `[DONE]` and one that was cut off both
// look to everything downstream of them.
func sseOf(frames ...string) func(http.ResponseWriter, int) {
	return func(w http.ResponseWriter, _ int) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, f := range frames {
			fmt.Fprintf(w, "data: %s\n\n", f)
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
		}
	}
}

// chunkFrame is one streamed delta, in the vendor's own envelope.
func chunkFrame(delta string, finish string) string {
	ch := map[string]any{"index": 0, "delta": json.RawMessage(delta)}
	if finish != "" {
		ch["finish_reason"] = finish
	}
	body, err := json.Marshal(map[string]any{
		"id": "up-1", "object": "chat.completion.chunk", "created": 1, "model": "a-up", "choices": []any{ch},
	})
	if err != nil {
		panic(err)
	}
	return string(body)
}

// usageFrame is the chunk include_usage asks a vendor for.
func usageFrame(prompt, completion int) string {
	return fmt.Sprintf(`{"id":"up-1","object":"chat.completion.chunk","created":1,"model":"a-up","choices":[],`+
		`"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d}}`, prompt, completion, prompt+completion)
}

// relayRequest is a request for one of the relay SKUs, written the way a caller writes
// it: the required parts first, then whatever fields the case is about.
func relayRequest(sku string, fields ...string) string {
	parts := append([]string{`"model": ` + strconv.Quote(sku),
		`"messages": [{"role": "user", "content": "hi"}]`}, fields...)
	return "{" + strings.Join(parts, ",") + "}"
}

// askedBody is the body vendor relay-a was handed, parsed.
func askedBody(t *testing.T, w *relayWorld, i int) map[string]json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(w.a.sent(t, i), &fields); err != nil {
		t.Fatalf("the upstream was sent a body that is not JSON: %v\n%s", err, w.a.sent(t, i))
	}
	return fields
}

// ── long context ──────────────────────────────────────────────────────────────

// A conversation the length of a real session reaches the vendor as one conversation:
// no turn trimmed, no paraphrase, and the fact planted in an early turn still sitting in
// that turn rather than in a summary of it. The retrieval tests prove the model finds
// the needle; this proves we did not throw away the haystack on the way.
func TestALongConversationReachesTheVendorWholeWithItsNeedle(t *testing.T) {
	const turns = 400
	var b strings.Builder
	b.WriteString(`{"model": "relay-sku", "messages": [`)
	for i := 0; i < turns; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(fmt.Sprintf(`{"role": "user", "content": "turn %d: %s"},`, i, strings.Repeat("filler ", 12)))
		b.WriteString(fmt.Sprintf(`{"role": "assistant", "content": "noted %d"}`, i))
		if i == 7 {
			// The needle, in a turn 393 turns before the question that asks for it.
			b.WriteString(`,{"role": "user", "content": "reminder: the launch code is mango-tango-7"}`)
			b.WriteString(`,{"role": "assistant", "content": "mango-tango-7 noted"}`)
		}
	}
	b.WriteString(`,{"role": "user", "content": "what is the launch code?"}],`)
	b.WriteString(`"max_tokens": 32, "temperature": 0}`)

	w := newRelayWorld(t, completes("mango-tango-7", 40_000, 0, 3), never(t))
	c := w.chat(b.String())
	if answered(c) != http.StatusOK {
		t.Fatalf("status %d: %s", answered(c), sent(c))
	}

	got := askedBody(t, w, 0)
	var msgs []map[string]json.RawMessage
	if err := json.Unmarshal(got["messages"], &msgs); err != nil {
		t.Fatal(err)
	}
	if want := 2*turns + 3; len(msgs) != want {
		t.Errorf("the vendor was sent %d turns, want all %d — a long conversation was trimmed", len(msgs), want)
	}
	if !strings.Contains(string(got["messages"]), "mango-tango-7") {
		t.Error("the needle turn did not survive the relay")
	}

	// The whole request, not just its messages: one long body in, the same body out.
	var asked map[string]json.RawMessage
	if err := json.Unmarshal([]byte(b.String()), &asked); err != nil {
		t.Fatal(err)
	}
	if canonical(t, got["messages"]) != canonical(t, asked["messages"]) {
		t.Errorf("the conversation was rewritten on its way to the vendor:\ncaller:   %s\nupstream: %s", asked["messages"], got["messages"])
	}
}

// The boundary condition the issue names: a prompt past the vendor's window is the
// caller's problem to fix, so its 400 comes back as a 400 naming the window. It is not
// quietly answered by a second vendor whose window happens to be larger — that answer
// would be a different model's answer to a question nobody asked.
func TestAPromptPastTheWindowIsRefusedNotAnsweredByAnotherVendor(t *testing.T) {
	quick(t)
	const tooLong = `This model's maximum context length is 8192 tokens. However, you requested 9000 tokens.`
	w := newRelayWorld(t, declines(http.StatusBadRequest, tooLong), never(t))

	c := w.chat(relayRequest(relaySku, `"max_tokens": 4000`))
	if answered(c) != http.StatusBadRequest {
		t.Errorf("status %d, want the vendor's own 400: %s", answered(c), sent(c))
	}
	if !strings.Contains(sent(c), "maximum context length") {
		t.Errorf("the refusal did not reach the caller with its reason:\n%s", sent(c))
	}
	if w.b.asked() != 0 {
		t.Errorf("a prompt too long for one vendor was answered by another %d time(s)", w.b.asked())
	}
	if got := w.spent(t); got != 0 {
		t.Errorf("an unanswered prompt cost the caller %d cents", got)
	}
}

// ── strict JSON modes ─────────────────────────────────────────────────────────

// Both JSON modes, in the shapes callers actually send: `json_object` plain, and a
// `json_schema` whose schema nests $defs, enums and required arrays. Neither is
// flattened, and neither buys a 400 on the way in — a request whose response format
// this API cannot name is refused before any vendor is asked, which is how a caller
// learns the format was dropped rather than getting prose back and wondering.
func TestStrictJsonModesReachTheVendorExactlyAsWritten(t *testing.T) {
	w := newRelayWorld(t, completes(`{"verdict":"ok","score":7}`, 20, 0, 6), never(t))

	bodies := []string{
		relayRequest(relaySku, `"response_format": {"type": "json_object"}`),
		relayRequest(relaySku, `"response_format": {"type": "json_schema", "json_schema": {"name": "verdict", "strict": true,`+
			`"schema": {"type": "object", "additionalProperties": false, "required": ["verdict", "score"],`+
			`"properties": {"verdict": {"enum": ["ok", "no"]}, "score": {"type": "integer", "minimum": 0, "maximum": 10},`+
			`"refs": {"$ref": "#/$defs/row"}, "$defs": {"row": {"type": "object", "properties": {"id": {"type": "string"}}}}}}}}`),
	}
	for i, body := range bodies {
		c := w.chat(body)
		if answered(c) != http.StatusOK {
			t.Fatalf("case %d: status %d, want 200: %s", i, answered(c), sent(c))
		}
		got := canonical(t, w.a.sent(t, i), ourFields...)
		want := canonical(t, []byte(body), ourFields...)
		if got != want {
			t.Errorf("case %d: the JSON mode was rewritten\ncaller:   %s\nupstream: %s", i, body, w.a.sent(t, i))
		}
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(w.a.sent(t, i), &fields)
		if !json.Valid(fields["response_format"]) {
			t.Errorf("case %d: response_format left as %s", i, fields["response_format"])
		}
	}
}

// The bug this suite reports rather than fixes: a vendor that answers JSON mode with
// prose — behind a 200, with no error anywhere — is relayed and billed as an answer.
// Nothing in the relay can tell "not JSON" from "the model's honest refusal", so the
// caller pays for text they cannot parse and learns it by failing to parse it. A fix
// has to weigh that against every vendor that answers json_object with a valid empty
// content when the model called a tool instead, which is why it is reported rather than
// changed here. This test pins what the relay does today so the day that changes, it
// changes on purpose.
func TestAJsonModeAnswerThatIsNotJsonIsRelayedAndBilled(t *testing.T) {
	w := newRelayWorld(t, completes("I could not answer in JSON.", 20_000, 0, 5_000), never(t))

	c := w.chat(relayRequest(relaySku, `"response_format": {"type": "json_object"}`))
	if answered(c) != http.StatusOK {
		t.Fatalf("status %d: %s", answered(c), sent(c))
	}
	var env struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(sent(c)), &env); err != nil || len(env.Choices) == 0 {
		t.Fatalf("answer is not a chat completion: %v\n%s", err, sent(c))
	}
	if env.Choices[0].Message.Content != "I could not answer in JSON." {
		t.Errorf("content = %q, want the vendor's prose relayed as it came", env.Choices[0].Message.Content)
	}
	debit := w.paid(t)
	if bill := usageBilledUSD(&usageRecord{Model: relaySku, PromptTokens: 20_000, CompletionTokens: 5_000}); debit.USD != bill {
		t.Errorf("the prose was billed %s, want %s — an unparseable answer is still a paid one today", debit.USD, bill)
	}
}

// ── function / tool calling ───────────────────────────────────────────────────

// A parallel tool call is a promise to run two functions, so the relay keeping the
// answer and losing the ids, the names, or the second call breaks the caller's loop
// while the 200 says it worked.
func TestBufferedToolCallsKeepTheirIdsNamesAndArguments(t *testing.T) {
	const answer = `{"id":"up-1","object":"chat.completion","created":1,"model":"a-up","choices":[{"index":0,` +
		`"message":{"role":"assistant","content":null,"tool_calls":[` +
		`{"id":"call_get_weather_sf","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"SF\"}"}},` +
		`{"id":"call_get_time_tyo","type":"function","function":{"name":"get_time","arguments":"{\"tz\":\"Asia/Tokyo\"}"}}]},` +
		`"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":30,"completion_tokens":12,"total_tokens":42}}`
	w := newRelayWorld(t, answersBody(answer), never(t))

	c := w.chat(relayRequest(relaySku, `"tools": [{"type":"function","function":{"name":"get_weather"}}]`,
		`"tool_choice": "auto"`, `"parallel_tool_calls": true`))
	if answered(c) != http.StatusOK {
		t.Fatalf("status %d: %s", answered(c), sent(c))
	}

	var env struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(sent(c)), &env); err != nil || len(env.Choices) == 0 {
		t.Fatalf("answer is not a chat completion: %v\n%s", err, sent(c))
	}
	calls := env.Choices[0].Message.ToolCalls
	if len(calls) != 2 {
		t.Fatalf("the caller was given %d tool call(s), want both parallel calls:\n%s", len(calls), sent(c))
	}
	for i, want := range [][3]string{
		{"call_get_weather_sf", "get_weather", `{"city":"SF"}`},
		{"call_get_time_tyo", "get_time", `{"tz":"Asia/Tokyo"}`},
	} {
		got := calls[i]
		if got.ID != want[0] || got.Function.Name != want[1] || got.Function.Arguments != want[2] {
			t.Errorf("call %d = (%q, %q, %s), want (%q, %q, %s)", i, got.ID, got.Function.Name, got.Function.Arguments,
				want[0], want[1], want[2])
		}
		if got.Type != "function" {
			t.Errorf("call %d lost its type: %s", i, sent(c))
		}
	}
	if env.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("finish_reason = %q, want tool_calls — a caller that dispatches on it cannot tell a call from an answer",
			env.Choices[0].FinishReason)
	}
}

// A vendor whose model wrote malformed arguments is a broken model, not a broken proxy:
// the relay must hand back exactly what was written so the caller's own retry sees the
// same bad JSON the vendor produced. Repairing or dropping it would hide which side
// failed.
func TestMalformedToolArgumentsAreRelayedUntouched(t *testing.T) {
	const broken = `{"id":"up-1","object":"chat.completion","created":1,"model":"a-up","choices":[{"index":0,` +
		`"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_x","type":"function",` +
		`"function":{"name":"lookup","arguments":"{\"q\": \"unterminated"}}]},"finish_reason":"tool_calls"}]}`
	w := newRelayWorld(t, answersBody(broken), never(t))

	c := w.chat(relayRequest(relaySku, `"tools": [{"type":"function","function":{"name":"lookup"}}]`))
	if answered(c) != http.StatusOK {
		t.Fatalf("status %d: %s", answered(c), sent(c))
	}
	if !strings.Contains(sent(c), `"arguments":"{\"q\": \"unterminated"`) {
		t.Errorf("malformed arguments were rewritten on the way out:\n%s", sent(c))
	}
}

// Multi-turn tool feedback is the turn a caller cannot rebuild from what is left: the
// tool result is tied to its call by tool_call_id, and an assistant turn whose content
// is null carries only the calls. Flatten either one and the vendor refuses the whole
// request or answers the wrong question.
func TestAToolResultTurnIsRelayedAttachedToItsCall(t *testing.T) {
	body := `{
	  "model": "relay-sku",
	  "messages": [
	    {"role": "user", "content": "weather in SF?"},
	    {"role": "assistant", "content": null, "tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "get_weather", "arguments": "{\"city\":\"SF\"}"}}]},
	    {"role": "tool", "tool_call_id": "call_1", "content": "18C and foggy"},
	    {"role": "user", "content": "and now in Tokyo?"}
	  ],
	  "tools": [{"type": "function", "function": {"name": "get_weather", "parameters": {"type": "object"}}}],
	  "tool_choice": {"type": "function", "function": {"name": "get_weather"}}
	}`
	w := newRelayWorld(t, completes("18C and foggy in SF; Tokyo is 22C.", 40, 0, 9), never(t))

	c := w.chat(body)
	if answered(c) != http.StatusOK {
		t.Fatalf("status %d: %s", answered(c), sent(c))
	}
	got := askedBody(t, w, 0)
	var want map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &want); err != nil {
		t.Fatal(err)
	}
	if canonical(t, got["messages"]) != canonical(t, want["messages"]) {
		t.Errorf("the tool turns did not survive:\ncaller:   %s\nupstream: %s", want["messages"], got["messages"])
	}
	// A tool_choice naming one function is a constraint, not a hint; the vendor has to
	// be told it in the shape it reads.
	if canonical(t, got["tool_choice"]) != canonical(t, want["tool_choice"]) {
		t.Errorf("tool_choice = %s, want the caller's constraint", got["tool_choice"])
	}
	if w.a.asked() != 1 {
		t.Errorf("the vendor was asked %d time(s), want once", w.a.asked())
	}
}

// A streamed tool call arrives in pieces: one frame names the call and its id, later
// frames only append characters to its arguments. The relay must not drop the id,
// reorder the calls, or touch the argument fragments — a caller reassembles a call from
// exactly those three things.
func TestAStreamedToolCallKeepsItsIdAcrossDeltas(t *testing.T) {
	w := newRelayWorld(t, sseOf(
		chunkFrame(`{"role":"assistant"}`, ""),
		chunkFrame(`{"tool_calls":[{"index":0,"id":"call_w","type":"function","function":{"name":"get_weather","arguments":""}}]}`, ""),
		chunkFrame(`{"tool_calls":[{"index":0,"function":{"arguments":"{\"ci"}}]}`, ""),
		chunkFrame(`{"tool_calls":[{"index":0,"function":{"arguments":"ty\":\"SF\"}"}}]}`, ""),
		chunkFrame(`{"tool_calls":[{"index":1,"id":"call_t","type":"function","function":{"name":"get_time","arguments":"{}"}}]}`, ""),
		chunkFrame(`{}`, "tool_calls"),
		usageFrame(30, 12),
		"[DONE]",
	), never(t))

	c := w.chat(relayRequest(relaySku, `"stream": true`, `"tools": [{"type":"function","function":{"name":"get_weather"}}]`))
	if answered(c) != http.StatusOK {
		t.Fatalf("status %d: %s", answered(c), sent(c))
	}

	events := chunks(t, sent(c))
	var (
		ids   []string
		args  = map[int]string{}
		order []int
	)
	for _, e := range events {
		var f struct {
			Choices []struct {
				Delta struct {
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(marshal(t, e), &f); err != nil {
			t.Fatal(err)
		}
		for _, ch := range f.Choices {
			for _, tc := range ch.Delta.ToolCalls {
				if tc.ID != "" {
					ids = append(ids, tc.ID)
					order = append(order, tc.Index)
				}
				args[tc.Index] += tc.Function.Arguments
			}
		}
	}

	if strings.Join(ids, ",") != "call_w,call_t" {
		t.Errorf("tool call ids that reached the caller: %v, want [call_w call_t] — an id dropped mid-stream cannot be answered", ids)
	}
	if ints(order) != "0, 1" {
		t.Errorf("tool call indexes in order: %s, want 0, 1 — two parallel calls spliced into one", ints(order))
	}
	if args[0] != `{"city":"SF"}` || args[1] != "{}" {
		t.Errorf("reassembled arguments = %v, want each call's own JSON back whole", args)
	}
	if n := strings.Count(sent(c), "[DONE]"); n != 1 {
		t.Errorf("the caller's stream carries %d [DONE]; want exactly one", n)
	}
}

// ── reasoning blocks ──────────────────────────────────────────────────────────

// A model that thinks in its own field (reasoning_content, not an inlined block) is
// telling the caller something they asked for. It is neither stripped by a stripper it
// did not ask for nor invented when there was none; and the reasoning tokens the vendor
// states stay visible on the receipt that priced them.
func TestReasoningTextSurvivesTheRelayAsWhatItSaid(t *testing.T) {
	t.Run("buffered", func(t *testing.T) {
		const answer = `{"id":"up-1","object":"chat.completion","created":1,"model":"a-up","choices":[{"index":0,` +
			`"message":{"role":"assistant","content":"42.","reasoning_content":"The user asked the ultimate question."},` +
			`"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":24,"total_tokens":36,` +
			`"completion_tokens_details":{"reasoning_tokens":20}}}`
		w := newRelayWorld(t, answersBody(answer), never(t))

		c := w.chat(relayRequest(relaySku))
		if answered(c) != http.StatusOK {
			t.Fatalf("status %d: %s", answered(c), sent(c))
		}
		if !strings.Contains(sent(c), `"reasoning_content":"The user asked the ultimate question."`) {
			t.Errorf("reasoning_content did not reach the caller:\n%s", sent(c))
		}
		if !strings.Contains(sent(c), `"reasoning_tokens":20`) {
			t.Errorf("the receipt dropped the reasoning it was priced from:\n%s", sent(c))
		}
	})

	t.Run("streamed", func(t *testing.T) {
		w := newRelayWorld(t, sseOf(
			chunkFrame(`{"role":"assistant","reasoning_content":"thinking..."}`, ""),
			chunkFrame(`{"reasoning_content":" still thinking"}`, ""),
			chunkFrame(`{"content":"42."}`, ""),
			chunkFrame(`{}`, "stop"),
			"[DONE]",
		), never(t))

		c := w.chat(relayRequest(relaySku, `"stream": true`))
		if answered(c) != http.StatusOK {
			t.Fatalf("status %d: %s", answered(c), sent(c))
		}
		out := sent(c)
		if !strings.Contains(out, `"reasoning_content":"thinking..."`) || !strings.Contains(out, `"reasoning_content":" still thinking"`) {
			t.Errorf("reasoning deltas were dropped or rewritten:\n%s", out)
		}
		if !strings.Contains(out, `"content":"42."`) {
			t.Errorf("the answer itself was lost behind its reasoning:\n%s", out)
		}
	})
}

// An upstream that hides its thinking inside content (DeepSeek's <think>) has it cut
// out of what the caller reads and kept in what the caller pays for. The same rule for
// a buffered body and a stream, where the block is split across frames and only the
// relay can tell the thinking from the answer.
func TestAnInlinedThinkingBlockIsStrippedFromTheAnswerButBilledWhole(t *testing.T) {
	t.Run("buffered", func(t *testing.T) {
		const answer = `{"id":"up-1","object":"chat.completion","created":1,"model":"deepseek-r1","choices":[{"index":0,` +
			`"message":{"role":"assistant","content":"<think>let me see: 6*9</think>The answer is 42."},` +
			`"finish_reason":"stop"}],"usage":{"prompt_tokens":1200,"completion_tokens":3000,"total_tokens":4200}}`
		w := newRelayWorld(t, answersBody(answer), never(t))
		thinkRoute(t)

		c := w.chat(relayRequest(thinkSku))
		if answered(c) != http.StatusOK {
			t.Fatalf("status %d: %s", answered(c), sent(c))
		}
		if strings.Contains(sent(c), "</think>") || !strings.Contains(sent(c), `"content":"The answer is 42."`) {
			t.Errorf("the thinking block reached the caller's content:\n%s", sent(c))
		}
		debit := w.paid(t)
		if bill := usageBilledUSD(&usageRecord{Model: thinkSku, PromptTokens: 1_200, CompletionTokens: 3_000}); debit.USD != bill {
			t.Errorf("a stripped answer was billed %s, want %s — the vendor wrote the thinking and billed it", debit.USD, bill)
		}
	})

	t.Run("streamed", func(t *testing.T) {
		w := newRelayWorld(t, sseOf(
			chunkFrame(`{"role":"assistant","content":"<think>let me "}`, ""),
			chunkFrame(`{"content":"see: 6*9</think>The answer"}`, ""),
			chunkFrame(`{"content":" is 42."}`, ""),
			chunkFrame(`{}`, "stop"),
			usageFrame(1_200, 3_000),
			"[DONE]",
		), never(t))
		thinkRoute(t)

		c := w.chat(relayRequest(thinkSku, `"stream": true`))
		if answered(c) != http.StatusOK {
			t.Fatalf("status %d: %s", answered(c), sent(c))
		}
		var forwarded strings.Builder
		for _, e := range chunks(t, sent(c)) {
			forwarded.WriteString(chunkContent(t, string(marshal(t, e))))
		}
		if got := forwarded.String(); got != "The answer is 42." {
			t.Errorf("forwarded content = %q, want the thinking cut out of it", got)
		}
		if strings.Contains(sent(c), "</think>") {
			t.Errorf("a stream still carrying </think>:\n%s", sent(c))
		}
		debit := w.paid(t)
		if bill := usageBilledUSD(&usageRecord{Model: thinkSku, PromptTokens: 1_200, CompletionTokens: 3_000}); debit.USD != bill {
			t.Errorf("a streamed thinking answer was billed %s, want %s", debit.USD, bill)
		}
	})
}

// ── truncation ────────────────────────────────────────────────────────────────

// A ceiling the caller named is theirs: kept when it fits the hold, lowered to the hold
// when it does not, and never raised. The key they wrote it under is the key the vendor
// is sent, and an unset ceiling cannot be left as 0 — to several vendors that means
// "write until the window is gone", which is a bill nobody reserved for.
func TestTheCeilingIsKeptLoweredAndNeverRaised(t *testing.T) {
	w := newRelayWorld(t, completes("ok", 12, 0, 4), never(t))

	// Under the hold: exactly what was asked, and no second key added beside it.
	c := w.chat(relayRequest(relaySku, `"max_tokens": 100`))
	if answered(c) != http.StatusOK {
		t.Fatalf("status %d: %s", answered(c), sent(c))
	}
	got := askedBody(t, w, 0)
	if string(got["max_tokens"]) != "100" {
		t.Errorf("max_tokens = %s, want the caller's own 100", got["max_tokens"])
	}
	if _, added := got["max_completion_tokens"]; added {
		t.Errorf("a key the caller did not write was sent: %s", w.a.sent(t, 0))
	}

	// Both keys, both past what one answer may spend: each lowered under the key it was
	// written under, neither moved across.
	c = w.chat(relayRequest(relaySku, `"max_tokens": 999999`, `"max_completion_tokens": 888888`))
	if answered(c) != http.StatusOK {
		t.Fatalf("status %d: %s", answered(c), sent(c))
	}
	got = askedBody(t, w, 1)
	ceiling := strconv.Itoa(maxReserveCompletionTokens)
	if string(got["max_tokens"]) != ceiling || string(got["max_completion_tokens"]) != ceiling {
		t.Errorf("ceilings = (%s, %s), want both lowered to %s", got["max_tokens"], got["max_completion_tokens"], ceiling)
	}

	// A ceiling of 0 is not "no limit" on our invoice: it becomes the reserved one.
	c = w.chat(relayRequest(relaySku, `"max_tokens": 0`))
	if answered(c) != http.StatusOK {
		t.Fatalf("status %d: %s", answered(c), sent(c))
	}
	got = askedBody(t, w, 2)
	if string(got["max_tokens"]) != strconv.Itoa(reserveCompletionFloor) {
		t.Errorf("max_tokens = %s, want it raised to the reserved %d rather than left at an unbounded 0",
			got["max_tokens"], reserveCompletionFloor)
	}
}

// An answer cut by the ceiling must say so. A caller that sees finish_reason "stop"
// will keep a truncated answer as the whole one — and, in a tool loop, dispatch on an
// answer that stopped mid-argument.
func TestACutAnswerSaysSoWithALengthFinishReason(t *testing.T) {
	t.Run("buffered", func(t *testing.T) {
		const answer = `{"id":"up-1","object":"chat.completion","created":1,"model":"a-up","choices":[{"index":0,` +
			`"message":{"role":"assistant","content":"a answer cut off mid-wo"},` +
			`"finish_reason":"length"}],"usage":{"prompt_tokens":9,"completion_tokens":128,"total_tokens":137}}`
		w := newRelayWorld(t, answersBody(answer), never(t))

		c := w.chat(relayRequest(relaySku, `"max_tokens": 8`))
		if answered(c) != http.StatusOK {
			t.Fatalf("status %d: %s", answered(c), sent(c))
		}
		if !strings.Contains(sent(c), `"finish_reason":"length"`) {
			t.Errorf("a cut answer reached the caller without its truncation flag:\n%s", sent(c))
		}
	})

	t.Run("streamed", func(t *testing.T) {
		w := newRelayWorld(t, sseOf(
			chunkFrame(`{"role":"assistant","content":"a answer "}`, ""),
			chunkFrame(`{"content":"cut off mid-wo"}`, ""),
			chunkFrame(`{}`, "length"),
			usageFrame(900, 1_200),
			"[DONE]",
		), never(t))

		c := w.chat(relayRequest(relaySku, `"stream": true`, `"max_tokens": 8`))
		if answered(c) != http.StatusOK {
			t.Fatalf("status %d: %s", answered(c), sent(c))
		}
		out := sent(c)
		if !strings.Contains(out, `"finish_reason":"length"`) {
			t.Errorf("a cut stream reached the caller without its truncation flag:\n%s", out)
		}
		debit := w.paid(t)
		if bill := usageBilledUSD(&usageRecord{Model: relaySku, PromptTokens: 900, CompletionTokens: 1_200}); debit.USD != bill {
			t.Errorf("a cut answer was billed %s, want %s — the vendor wrote to its ceiling and said so", debit.USD, bill)
		}
	})
}

// ── SSE stream interruptions ──────────────────────────────────────────────────

// A vendor that ends its stream without saying how the answer ended. One that stated
// its finish_reason is relayed as it came, with nothing added; one that stopped mid-
// sentence is told to the caller as an error frame (never as a finished answer, never
// closed with [DONE]), and what was relayed still bills.
func TestAStreamTheVendorLeftOpenIsClosedWithWhatIsTrue(t *testing.T) {
	t.Run("finished but no sentinel", func(t *testing.T) {
		w := newRelayWorld(t, sseOf(
			chunkFrame(`{"role":"assistant","content":"the whole answer"}`, ""),
			chunkFrame(`{}`, "stop"),
		), never(t))

		c := w.chat(relayRequest(relaySku, `"stream": true`))
		if answered(c) != http.StatusOK {
			t.Fatalf("status %d: %s", answered(c), sent(c))
		}
		out := sent(c)
		if !strings.Contains(out, `"finish_reason":"stop"`) || strings.Contains(out, "upstream_error") {
			t.Errorf("a finished answer was not relayed as it came:\n%s", out)
		}
	})

	t.Run("cut off mid-sentence", func(t *testing.T) {
		w := newRelayWorld(t, sseOf(
			chunkFrame(`{"role":"assistant","content":"half an "}`, ""),
			chunkFrame(`{"content":"answer bei"}`, ""),
		), never(t))

		// A prompt long enough that its price is a number, so "the text relayed still
		// bills" can be told from a call too small to meter.
		c := w.chat(fmt.Sprintf(`{"model": %q, "stream": true, "messages": [{"role": "user", "content": %q}]}`,
			relaySku, strings.Repeat("count the stars in this sky. ", 2_000)))
		if answered(c) != http.StatusOK {
			t.Fatalf("status %d: %s", answered(c), sent(c))
		}
		out := sent(c)
		if strings.Contains(out, "[DONE]") {
			t.Errorf("a cut answer was closed as though it had finished:\n%s", out)
		}
		var said bool
		for _, e := range chunks(t, out) {
			if _, isErr := e["error"]; isErr {
				said = true
			}
		}
		if !said {
			t.Errorf("the stream ended without saying why it ended:\n%s", out)
		}

		// What was relayed still bills — the vendor wrote those tokens and spent them —
		// and nothing is left held against the caller.
		w.spent(t)
		if debit := w.paid(t); debit.Model != relaySku || debit.USD == "" || debit.USD == "0" {
			t.Errorf("a cut answer billed %s under %q, want the text its caller kept paid for", debit.USD, debit.Model)
		}
	})

	t.Run("closed by the vendor itself", func(t *testing.T) {
		w := newRelayWorld(t, sseOf(
			chunkFrame(`{"role":"assistant","content":"done properly"}`, ""),
			chunkFrame(`{}`, "stop"),
			"[DONE]",
		), never(t))

		c := w.chat(relayRequest(relaySku, `"stream": true`))
		if out := sent(c); strings.Count(out, "[DONE]") != 1 {
			t.Errorf("a stream the vendor closed was closed again:\n%s", out)
		}
	})
}

// A vendor keeping its connection open in its own words: the beat goes on, the words do
// not — they are the vendor's, and this API does not publish who sold the inference.
func TestAVendorKeepAliveBeatIsRelayedWithoutItsWords(t *testing.T) {
	w := newRelayWorld(t, func(rw http.ResponseWriter, _ int) {
		rw.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(rw, ": PROCESSING WHILE YOU WAIT\n\n")
		fmt.Fprintf(rw, "data: %s\n\n", chunkFrame(`{"role":"assistant","content":"here"}`, ""))
		fmt.Fprintf(rw, "data: %s\n\n", chunkFrame(`{}`, "stop"))
		fmt.Fprint(rw, "data: [DONE]\n\n")
	}, never(t))

	c := w.chat(relayRequest(relaySku, `"stream": true`))
	if answered(c) != http.StatusOK {
		t.Fatalf("status %d: %s", answered(c), sent(c))
	}
	out := sent(c)
	if !strings.Contains(out, ":\n\n") {
		t.Errorf("the keep-alive beat was dropped:\n%s", out)
	}
	if strings.Contains(out, "PROCESSING WHILE YOU WAIT") {
		t.Errorf("the vendor's own words reached the caller:\n%s", out)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// thinkSku is priced like relaySku but routed to a reasoning-inlining upstream, so one
// world can test both kinds of thinking.
const thinkSku = "relay-think-sku"

// thinkRoute seeds that SKU's row. It is written after newRelayWorld because each new
// world starts from a fresh store, and a row added before it belongs to a database that
// no longer exists.
func thinkRoute(t *testing.T) {
	t.Helper()
	if _, err := object.AddModelRoute(&object.ModelRoute{
		Owner: "built-in", ModelName: thinkSku, Enabled: true,
		Provider: "relay-a", Upstream: "deepseek-r1",
		InputPrice: 2, OutputPrice: 8, Priced: true,
	}); err != nil {
		t.Fatal(err)
	}
}

// marshal re-encodes one parsed SSE event so a caller-side struct can read it.
func marshal(t *testing.T, event map[string]json.RawMessage) []byte {
	t.Helper()
	out, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
