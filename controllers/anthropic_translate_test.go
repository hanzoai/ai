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

import (
	"encoding/json"
	"strings"
	"testing"

	openai "github.com/hanzoai/go-openai"
)

// collectSSE runs the streaming translator over a fixed set of OpenAI SSE lines
// and returns the emitted Anthropic events as (event, decoded-data) pairs.
type sseEvent struct {
	Event string
	Data  map[string]interface{}
}

func runTranslator(t *testing.T, openaiSSE string) ([]sseEvent, int, int) {
	t.Helper()
	var events []sseEvent
	emit := func(event string, data interface{}) error {
		b, _ := json.Marshal(data)
		var m map[string]interface{}
		_ = json.Unmarshal(b, &m)
		events = append(events, sseEvent{Event: event, Data: m})
		return nil
	}
	prompt, completion, _ := translateOpenAIStream(strings.NewReader(openaiSSE), emit, "glm-5.2", "req_test")
	return events, prompt, completion
}

func eventTypes(events []sseEvent) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Event
	}
	return out
}

// ── Streaming: plain text ────────────────────────────────────────────────────

func TestTranslateStream_TextOnly(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"id":"c1","model":"glm","choices":[{"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}`,
		`data: {"id":"c1","model":"glm","choices":[{"delta":{"content":" world"},"finish_reason":null}]}`,
		`data: {"id":"c1","model":"glm","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"c1","model":"glm","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}}`,
		`data: [DONE]`,
	}, "\n") + "\n"

	events, prompt, completion := runTranslator(t, sse)
	types := eventTypes(events)

	want := []string{"message_start", "content_block_start", "content_block_delta", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("event sequence = %v, want %v", types, want)
	}

	// content_block_start is a text block at index 0.
	cbs := events[1]
	if cbs.Data["content_block"].(map[string]interface{})["type"] != "text" {
		t.Fatalf("first block not text: %v", cbs.Data)
	}
	// text deltas carry text_delta.
	d0 := events[2].Data["delta"].(map[string]interface{})
	if d0["type"] != "text_delta" || d0["text"] != "Hello" {
		t.Fatalf("delta0 = %v", d0)
	}
	// message_delta stop_reason end_turn + usage.
	md := events[5].Data
	if md["delta"].(map[string]interface{})["stop_reason"] != "end_turn" {
		t.Fatalf("stop_reason = %v", md["delta"])
	}
	if prompt != 7 || completion != 2 {
		t.Fatalf("usage prompt=%d completion=%d, want 7/2", prompt, completion)
	}
}

// ── Streaming: tool call (the P0) ────────────────────────────────────────────

func TestTranslateStream_ToolCall(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"id":"c1","model":"glm","choices":[{"delta":{"role":"assistant","content":null,"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"c1","model":"glm","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":"}}]},"finish_reason":null}]}`,
		`data: {"id":"c1","model":"glm","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"SF\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"c1","model":"glm","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c1","model":"glm","choices":[],"usage":{"prompt_tokens":20,"completion_tokens":8,"total_tokens":28}}`,
		`data: [DONE]`,
	}, "\n") + "\n"

	events, _, _ := runTranslator(t, sse)
	types := eventTypes(events)
	want := []string{"message_start", "content_block_start", "content_block_delta", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("event sequence = %v, want %v", types, want)
	}

	// content_block_start must be a tool_use block with id+name.
	cb := events[1].Data["content_block"].(map[string]interface{})
	if cb["type"] != "tool_use" || cb["id"] != "call_1" || cb["name"] != "get_weather" {
		t.Fatalf("tool_use block = %v", cb)
	}
	// argument deltas are input_json_delta, and concatenate to full JSON.
	var partial strings.Builder
	for _, e := range events {
		if e.Event == "content_block_delta" {
			d := e.Data["delta"].(map[string]interface{})
			if d["type"] != "input_json_delta" {
				t.Fatalf("tool delta type = %v, want input_json_delta", d["type"])
			}
			partial.WriteString(d["partial_json"].(string))
		}
	}
	if partial.String() != `{"city":"SF"}` {
		t.Fatalf("assembled tool args = %q, want %q", partial.String(), `{"city":"SF"}`)
	}
	// THE load-bearing assertion: stop_reason must be tool_use.
	md := events[5].Data["delta"].(map[string]interface{})
	if md["stop_reason"] != "tool_use" {
		t.Fatalf("stop_reason = %v, want tool_use", md["stop_reason"])
	}
}

// ── Streaming: text THEN tool call share one index space ─────────────────────

func TestTranslateStream_TextThenTool_IndexSpace(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"Let me check."},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_9","type":"function","function":{"name":"ls","arguments":"{}"}}]},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	}, "\n") + "\n"

	events, _, _ := runTranslator(t, sse)

	// Expect: text block at index 0 (start,delta,stop), tool block at index 1 (start,delta,stop).
	var starts []int
	for _, e := range events {
		if e.Event == "content_block_start" {
			starts = append(starts, int(e.Data["index"].(float64)))
		}
	}
	if len(starts) != 2 || starts[0] != 0 || starts[1] != 1 {
		t.Fatalf("block start indices = %v, want [0 1]", starts)
	}
	// The tool block delta must reference index 1.
	for _, e := range events {
		if e.Event == "content_block_delta" {
			d := e.Data["delta"].(map[string]interface{})
			if d["type"] == "input_json_delta" && int(e.Data["index"].(float64)) != 1 {
				t.Fatalf("input_json_delta at index %v, want 1", e.Data["index"])
			}
		}
	}
}

// ── Streaming: reasoning_content → thinking (never leaked raw) ────────────────

func TestTranslateStream_ReasoningToThinking(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"choices":[{"delta":{"reasoning_content":"The user"},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{"reasoning_content":" wants OK"},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{"content":"OK"},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	}, "\n") + "\n"

	events, _, _ := runTranslator(t, sse)

	// First block is thinking, second is text.
	var blockTypes []string
	for _, e := range events {
		if e.Event == "content_block_start" {
			blockTypes = append(blockTypes, e.Data["content_block"].(map[string]interface{})["type"].(string))
		}
	}
	if strings.Join(blockTypes, ",") != "thinking,text" {
		t.Fatalf("block types = %v, want [thinking text]", blockTypes)
	}
	// The reasoning must surface as thinking_delta, never as a raw reasoning field.
	sawThinkingDelta := false
	for _, e := range events {
		if e.Event == "content_block_delta" {
			d := e.Data["delta"].(map[string]interface{})
			if d["type"] == "thinking_delta" {
				sawThinkingDelta = true
			}
			if _, leaked := d["reasoning_content"]; leaked {
				t.Fatalf("reasoning_content leaked into Anthropic delta: %v", d)
			}
		}
	}
	if !sawThinkingDelta {
		t.Fatalf("no thinking_delta emitted")
	}
}

// ── Request translation: tool_result round-trip ──────────────────────────────

func TestAnthropicToOpenAI_ToolResultRoundTrip(t *testing.T) {
	req := &AnthropicRequest{
		System: json.RawMessage(`"You are helpful."`),
		Messages: []AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`"read file X"`)},
			{Role: "assistant", Content: json.RawMessage(`[{"type":"text","text":"I'll read it."},{"type":"tool_use","id":"toolu_1","name":"read","input":{"path":"X"}}]`)},
			{Role: "user", Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"toolu_1","content":"file contents"}]`)},
		},
	}
	msgs := anthropicToOpenAIMessages(req)

	// system, user, assistant(+tool_calls), tool
	if len(msgs) != 4 {
		t.Fatalf("got %d messages, want 4: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != "system" || msgs[1].Role != "user" {
		t.Fatalf("prefix roles = %s,%s", msgs[0].Role, msgs[1].Role)
	}
	// assistant carries the tool_call.
	asst := msgs[2]
	if asst.Role != "assistant" || len(asst.ToolCalls) != 1 {
		t.Fatalf("assistant tool_calls = %+v", asst)
	}
	if asst.ToolCalls[0].ID != "toolu_1" || asst.ToolCalls[0].Function.Name != "read" {
		t.Fatalf("tool_call = %+v", asst.ToolCalls[0])
	}
	if !strings.Contains(asst.ToolCalls[0].Function.Arguments, `"path"`) {
		t.Fatalf("tool_call args lost: %q", asst.ToolCalls[0].Function.Arguments)
	}
	// tool_result → role:"tool" with matching id.
	tool := msgs[3]
	if tool.Role != "tool" || tool.ToolCallID != "toolu_1" || tool.Content != "file contents" {
		t.Fatalf("tool message = %+v", tool)
	}
}

// ── Request translation: parallel tool_results in one user turn ──────────────

func TestAnthropicToOpenAI_ParallelToolResults(t *testing.T) {
	req := &AnthropicRequest{
		Messages: []AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"a","content":"ra"},{"type":"tool_result","tool_use_id":"b","content":"rb"}]`)},
		},
	}
	msgs := anthropicToOpenAIMessages(req)
	if len(msgs) != 2 {
		t.Fatalf("got %d, want 2 tool messages", len(msgs))
	}
	if msgs[0].ToolCallID != "a" || msgs[1].ToolCallID != "b" {
		t.Fatalf("tool ids = %s,%s", msgs[0].ToolCallID, msgs[1].ToolCallID)
	}
}

// ── Request translation: image content block → OpenAI image_url ──────────────

func TestAnthropicToOpenAI_Image(t *testing.T) {
	req := &AnthropicRequest{
		Messages: []AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`[{"type":"text","text":"what is this"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]`)},
		},
	}
	msgs := anthropicToOpenAIMessages(req)
	if len(msgs) != 1 || len(msgs[0].MultiContent) != 2 {
		t.Fatalf("multicontent = %+v", msgs)
	}
	img := msgs[0].MultiContent[1]
	if img.Type != openai.ChatMessagePartTypeImageURL || img.ImageURL == nil {
		t.Fatalf("image part = %+v", img)
	}
	if img.ImageURL.URL != "data:image/png;base64,AAAA" {
		t.Fatalf("image url = %q", img.ImageURL.URL)
	}
}

// ── Tools translation ────────────────────────────────────────────────────────

func TestAnthropicToolsToOpenAI(t *testing.T) {
	tools := []AnthropicTool{
		{Name: "get_weather", Description: "Get weather", InputSchema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`)},
	}
	out := anthropicToolsToOpenAI(tools)
	if len(out) != 1 || out[0].Type != openai.ToolTypeFunction {
		t.Fatalf("tools = %+v", out)
	}
	if out[0].Function.Name != "get_weather" || out[0].Function.Description != "Get weather" {
		t.Fatalf("fn = %+v", out[0].Function)
	}
	if out[0].Function.Parameters == nil {
		t.Fatalf("parameters lost")
	}
}

func TestAnthropicToolChoice(t *testing.T) {
	if anthropicToolChoiceToOpenAI(json.RawMessage(`{"type":"auto"}`)) != "auto" {
		t.Fatalf("auto")
	}
	if anthropicToolChoiceToOpenAI(json.RawMessage(`{"type":"any"}`)) != "required" {
		t.Fatalf("any→required")
	}
	tc := anthropicToolChoiceToOpenAI(json.RawMessage(`{"type":"tool","name":"read"}`))
	oc, ok := tc.(openai.ToolChoice)
	if !ok || oc.Function.Name != "read" {
		t.Fatalf("tool choice = %+v", tc)
	}
}

// TestAnthropicThinkingToReasoningEffort covers the request-side half of the
// thinking round trip (the response half is TestTranslateStream_ReasoningToThinking).
// Anthropic budget_tokens collapses monotonically to the upstream's effort ordinal,
// in the vocabulary the target provider accepts. The disabled/absent/malformed cases
// all yield "" so the upstream keeps its native default. Direction is load-bearing:
// deep budget (ultrathink ~32k) → the HEAVIEST tier; light budget (think ~4k) → the
// LIGHTER tier. GLM-5.2 has only "max"|"high" — "low"/"medium" would be rejected.
func TestAnthropicThinkingToReasoningEffort(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		vocab string
		want  string
	}{
		// No-op cases are vocabulary-independent.
		{"absent glm", ``, "glm", ""},
		{"absent openai", ``, "openai", ""},
		{"disabled glm", `{"type":"disabled","budget_tokens":10000}`, "glm", ""},
		{"enabled zero budget glm", `{"type":"enabled","budget_tokens":0}`, "glm", ""},
		{"malformed glm", `{not json`, "glm", ""},

		// GLM / DO-AI family: two tiers, deep→max, light→high.
		{"glm think 4k → high", `{"type":"enabled","budget_tokens":4096}`, "glm", "high"},
		{"glm think-hard 10k → high", `{"type":"enabled","budget_tokens":10000}`, "glm", "high"},
		{"glm boundary 16383 → high", `{"type":"enabled","budget_tokens":16383}`, "glm", "high"},
		{"glm ultrathink 16384 → max", `{"type":"enabled","budget_tokens":16384}`, "glm", "max"},
		{"glm ultrathink 31999 → max", `{"type":"enabled","budget_tokens":31999}`, "glm", "max"},

		// OpenAI o-series: three tiers.
		{"openai think 2k → low", `{"type":"enabled","budget_tokens":2048}`, "openai", "low"},
		{"openai think 4k → medium", `{"type":"enabled","budget_tokens":4096}`, "openai", "medium"},
		{"openai think-hard 10k → medium", `{"type":"enabled","budget_tokens":10000}`, "openai", "medium"},
		{"openai ultrathink 16384 → high", `{"type":"enabled","budget_tokens":16384}`, "openai", "high"},
		{"openai ultrathink 31999 → high", `{"type":"enabled","budget_tokens":31999}`, "openai", "high"},

		// qwen/kimi/unknown ("" vocab): never emit reasoning_effort — those
		// upstreams use a native thinking param or none.
		{"none deep budget → empty", `{"type":"enabled","budget_tokens":31999}`, "", ""},
		{"none light budget → empty", `{"type":"enabled","budget_tokens":4096}`, "", ""},
	}
	for _, c := range cases {
		var raw json.RawMessage
		if c.raw != "" {
			raw = json.RawMessage(c.raw)
		}
		if got := anthropicThinkingToReasoningEffort(raw, c.vocab); got != c.want {
			t.Fatalf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

// TestThinkingVocabularyForUpstream pins the ONE place upstream model → reasoning
// vocabulary is decided. Only families that accept an OpenAI-style reasoning_effort
// string are named — openai (low|medium|high) and glm (max|high, shared by GLM-5.*
// and DeepSeek V4). qwen (native enable_thinking), kimi (native thinking.type), and
// models with no reasoning control return "" so no reasoning_effort is forwarded.
func TestThinkingVocabularyForUpstream(t *testing.T) {
	cases := []struct{ upstream, want string }{
		{"glm-5.2", "glm"},
		{"glm-5", "glm"},
		{"deepseek-v4-pro", "glm"},
		{"deepseek-4-flash", "glm"},
		{"gpt-5.2", "openai"},
		{"openai-gpt-5", "openai"},
		{"o3", "openai"},
		{"qwen3.5-397b-a17b", ""},
		{"alibaba-qwen3-32b", ""},
		{"kimi-k2.6", ""},
		{"minimax-m2.5", ""},
		{"nemotron-3-nano-omni", ""},
		{"llama3.3-70b-instruct", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := thinkingVocabularyForUpstream(c.upstream); got != c.want {
			t.Errorf("%s → %q want %q", c.upstream, got, c.want)
		}
	}
}

// TestAssistantThinkingRoundTrips proves an assistant thinking block maps to
// reasoning_content so a tool-call loop echoes the prior chain-of-thought back
// (DeepSeek V4 400s without it; Kimi loses continuity).
func TestAssistantThinkingRoundTrips(t *testing.T) {
	blocks := []anthropicBlock{
		{Type: "thinking", Thinking: "Need the weather, call the tool."},
		{Type: "text", Text: "Let me check."},
		{Type: "tool_use", ID: "call_1", Name: "get_weather", Input: json.RawMessage(`{"city":"SF"}`)},
	}
	msg := assistantBlocksToOpenAI(blocks)
	if msg.ReasoningContent != "Need the weather, call the tool." {
		t.Fatalf("reasoning_content = %q, want the thinking text", msg.ReasoningContent)
	}
	if msg.Content != "Let me check." {
		t.Fatalf("content = %q", msg.Content)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Name != "get_weather" {
		t.Fatalf("tool_calls not preserved: %+v", msg.ToolCalls)
	}
	// No thinking → reasoning_content omitted (empty).
	if m := assistantBlocksToOpenAI([]anthropicBlock{{Type: "text", Text: "hi"}}); m.ReasoningContent != "" {
		t.Fatalf("reasoning_content = %q, want empty when no thinking", m.ReasoningContent)
	}
}

// ── Non-streaming response translation ───────────────────────────────────────

func TestOpenAIResponseToAnthropic_ToolUse(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_7","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"SF\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":15,"completion_tokens":6}}`)
	resp, prompt, completion := openAIResponseToAnthropic(body, "glm-5.2", "req_1")

	if resp["stop_reason"] != "tool_use" {
		t.Fatalf("stop_reason = %v, want tool_use", resp["stop_reason"])
	}
	content := resp["content"].([]interface{})
	if len(content) != 1 {
		t.Fatalf("content len = %d", len(content))
	}
	block := content[0].(map[string]interface{})
	if block["type"] != "tool_use" || block["id"] != "call_7" || block["name"] != "get_weather" {
		t.Fatalf("block = %v", block)
	}
	// input must be a decoded object, not the raw string.
	input := block["input"].(map[string]interface{})
	if input["city"] != "SF" {
		t.Fatalf("input = %v", input)
	}
	if prompt != 15 || completion != 6 {
		t.Fatalf("usage = %d/%d", prompt, completion)
	}
}

func TestOpenAIResponseToAnthropic_Text(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"role":"assistant","content":"HANZO OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`)
	resp, _, _ := openAIResponseToAnthropic(body, "glm-5.2", "req_1")
	if resp["stop_reason"] != "end_turn" {
		t.Fatalf("stop_reason = %v", resp["stop_reason"])
	}
	content := resp["content"].([]interface{})
	block := content[0].(map[string]interface{})
	if block["type"] != "text" || block["text"] != "HANZO OK" {
		t.Fatalf("block = %v", block)
	}
}

// ── finish_reason mapping ────────────────────────────────────────────────────

func TestMapFinishReason(t *testing.T) {
	cases := map[string]string{
		"tool_calls":     "tool_use",
		"function_call":  "tool_use",
		"stop":           "end_turn",
		"length":         "max_tokens",
		"content_filter": "end_turn",
		"":               "end_turn",
	}
	for in, want := range cases {
		if got := mapFinishReason(in); got != want {
			t.Fatalf("mapFinishReason(%q) = %q, want %q", in, got, want)
		}
	}
}

// ── Reasoning normalization: DeepSeek inlines <think></think> in content ──────

// runTranslatorModel is runTranslator with an explicit upstream model id, so a
// reasoning-inlining upstream (DeepSeek) exercises the strip path.
func runTranslatorModel(t *testing.T, openaiSSE, modelID string) []sseEvent {
	t.Helper()
	var events []sseEvent
	emit := func(event string, data interface{}) error {
		b, _ := json.Marshal(data)
		var m map[string]interface{}
		_ = json.Unmarshal(b, &m)
		events = append(events, sseEvent{Event: event, Data: m})
		return nil
	}
	translateOpenAIStream(strings.NewReader(openaiSSE), emit, modelID, "req_test")
	return events
}

// TestTranslateStream_DeepSeekInlineReasoningStripped is the Claude-Code P0: a
// DeepSeek upstream buries its chain-of-thought as plain content ending in
// </think> (the opening <think> is eaten by the chat template). The translator
// must drop that reasoning and never leak </think> into a text block — even when
// </think> is split across two SSE chunks.
func TestTranslateStream_DeepSeekInlineReasoningStripped(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"choices":[{"delta":{"role":"assistant","content":"Let me think"},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{"content":" about this.</th"},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{"content":"ink>Hello"},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{"content":" world"},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	}, "\n") + "\n"

	events := runTranslatorModel(t, sse, "deepseek-v4-pro")

	// Only a text block — no thinking block, since DeepSeek used no reasoning_content.
	var blockTypes, textDeltas []string
	for _, e := range events {
		if e.Event == "content_block_start" {
			blockTypes = append(blockTypes, e.Data["content_block"].(map[string]interface{})["type"].(string))
		}
		if e.Event == "content_block_delta" {
			if d := e.Data["delta"].(map[string]interface{}); d["type"] == "text_delta" {
				textDeltas = append(textDeltas, d["text"].(string))
			}
		}
	}
	if strings.Join(blockTypes, ",") != "text" {
		t.Fatalf("block types = %v, want [text] (reasoning dropped, not a thinking block)", blockTypes)
	}
	if got := strings.Join(textDeltas, ""); got != "Hello world" {
		t.Fatalf("visible text = %q, want %q (reasoning stripped)", got, "Hello world")
	}
	// The template token and the reasoning prose must never reach the client.
	for _, e := range events {
		b, _ := json.Marshal(e.Data)
		if strings.Contains(string(b), "</think>") {
			t.Fatalf("</think> leaked into event %s: %s", e.Event, b)
		}
		if strings.Contains(string(b), "Let me think") {
			t.Fatalf("reasoning prose leaked into event %s: %s", e.Event, b)
		}
	}
}

// TestTranslateStream_NonDeepSeekContentUntouched locks the gate: a non-inlining
// upstream (GLM) is never stripped, so content passes byte-for-byte — even the
// (hypothetical) literal token — because the strip is DeepSeek-only.
func TestTranslateStream_NonDeepSeekContentUntouched(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"a</think>b"},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	}, "\n") + "\n"

	events := runTranslatorModel(t, sse, "glm-5.2")
	var text string
	for _, e := range events {
		if e.Event == "content_block_delta" {
			if d := e.Data["delta"].(map[string]interface{}); d["type"] == "text_delta" {
				text += d["text"].(string)
			}
		}
	}
	if text != "a</think>b" {
		t.Fatalf("non-DeepSeek content = %q, want it passed through verbatim", text)
	}
}

// TestOpenAIResponseToAnthropic_DeepSeekInlineReasoningStripped is the non-stream
// twin: a DeepSeek reply whose content is `reasoning</think>answer` yields a
// single text block holding only the answer.
func TestOpenAIResponseToAnthropic_DeepSeekInlineReasoningStripped(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"role":"assistant","content":"Reasoning here.</think>The final answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":9}}`)
	resp, _, _ := openAIResponseToAnthropic(body, "deepseek-v4-pro", "req_1")

	content := resp["content"].([]interface{})
	if len(content) != 1 {
		t.Fatalf("content len = %d, want 1 (reasoning dropped, no thinking block)", len(content))
	}
	block := content[0].(map[string]interface{})
	if block["type"] != "text" || block["text"] != "The final answer" {
		t.Fatalf("block = %v, want text 'The final answer'", block)
	}
}
