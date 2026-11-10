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
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestQA132_LongContextWindowGuaranteesAndDiscoveryMerging verifies 1M-token
// flagship context window guarantees across zenFam and ensoFam, discovery-backed
// mergeModels window resolution, and duplicate-free catalog merging.
func TestQA132_LongContextWindowGuaranteesAndDiscoveryMerging(t *testing.T) {
	if flagshipWindow != 1000000 {
		t.Fatalf("flagshipWindow = %d, want 1000000", flagshipWindow)
	}
	for _, sku := range []string{"zen5", "zen5-coder", "zen5-pro"} {
		if got := zenFam.windows[sku]; got != flagshipWindow {
			t.Errorf("zenFam.windows[%q] = %d, want %d", sku, got, flagshipWindow)
		}
	}
	if got := ensoFam.windows["enso-ultra"]; got != flagshipWindow {
		t.Errorf("ensoFam.windows[enso-ultra] = %d, want %d", got, flagshipWindow)
	}

	t.Setenv("TEST_QA132_ZEN_URL", "http://zen.invalid")
	fam := &modelFamily{
		name:   "zen",
		prefix: "zen",
		owner:  "zenlm",
		urlKey: "TEST_QA132_ZEN_URL",
		windows: map[string]int{
			"zen5":       flagshipWindow,
			"zen5-coder": flagshipWindow,
			"zen5-pro":   flagshipWindow,
		},
		byID: map[string]zenModel{
			"zen5":      {ID: "zen5", MaxCtx: 262144},
			"zen5-mini": {ID: "zen5-mini", MaxCtx: 131072},
		},
		ids:       []string{"zen5", "zen5-mini"},
		loaded:    true,
		fetchedAt: time.Now(),
	}

	base := []modelInfo{{ID: "zen5", Object: "model", OwnedBy: "zenlm", ContextWindow: 65536}}
	merged := fam.mergeModels(base)
	byID := indexModels(merged)

	if len(merged) != 2 {
		t.Fatalf("mergeModels returned %d models, want 2 (deduped zen5 + zen5-mini)", len(merged))
	}
	if byID["zen5"].ContextWindow != flagshipWindow {
		t.Errorf("zen5 context_window = %d, want %d", byID["zen5"].ContextWindow, flagshipWindow)
	}
	if byID["zen5-mini"].ContextWindow != 131072 {
		t.Errorf("zen5-mini context_window = %d, want 131072", byID["zen5-mini"].ContextWindow)
	}
	if _, phantom := byID["zen5-pro"]; phantom {
		t.Error("undiscovered SKU zen5-pro must not be synthesized into /v1/models")
	}
}

// TestQA132_StructuredOutputJSONSchemaAndStrictCanonicalDigest verifies JSON schema
// structured outputs, zero-valued sampling parameters (temperature: 0, top_p: 0),
// stop sequences, and RFC 8785 canonical digest verification under X-Hanzo-Strict: 1.
func TestQA132_StructuredOutputJSONSchemaAndStrictCanonicalDigest(t *testing.T) {
	structuredOut := `{"service":"gateway","healthy":true,"latency_ms":14}`
	w := newRelayWorld(t, completes(structuredOut, 48, 12, 16), never(t))

	c := w.strictChat(conversation)
	if answered(c) != http.StatusOK {
		t.Fatalf("strictChat status = %d, want 200: %s", answered(c), sent(c))
	}

	wantDigest := independent(t, conversation)
	if got := header(c, requestSha); got != wantDigest {
		t.Errorf("%s = %q, want %q", requestSha, got, wantDigest)
	}
	if got := header(c, upstreamSha); got != wantDigest {
		t.Errorf("%s = %q, want %q", upstreamSha, got, wantDigest)
	}

	// Verify the upstream received the strict JSON schema response_format intact.
	upBody := w.a.sent(t, 0)
	var forwarded map[string]any
	if err := json.Unmarshal(upBody, &forwarded); err != nil {
		t.Fatalf("unmarshal upstream body: %v", err)
	}
	rf, ok := forwarded["response_format"].(map[string]any)
	if !ok || rf["type"] != "json_schema" {
		t.Fatalf("upstream response_format = %#v, want json_schema", forwarded["response_format"])
	}
	jsSchema, ok := rf["json_schema"].(map[string]any)
	if !ok || jsSchema["strict"] != true || jsSchema["name"] != "answer" {
		t.Fatalf("upstream json_schema = %#v, want strict=true name=answer", jsSchema)
	}

	// Verify strict invariant refusals return 409 Conflict with X-Hanzo-Strict-Violation.
	violations := []struct {
		name      string
		body      string
		invariant string
	}{
		{"missing max_tokens ceiling", `{"model":"relay-sku","messages":[{"role":"user","content":"hi"}]}`, "ceiling_unset"},
		{"ceiling exceeds reservation", `{"model":"relay-sku","max_tokens":10000000,"messages":[{"role":"user","content":"hi"}]}`, "ceiling_lowered"},
		{"gateway-only field dropped", `{"model":"relay-sku","max_tokens":16,"fast":true,"messages":[{"role":"user","content":"hi"}]}`, "param_dropped"},
		{"duplicate JSON key", `{"model":"relay-sku","max_tokens":16,"seed":1,"seed":2,"messages":[{"role":"user","content":"hi"}]}`, "not_canonical"},
		{"auto router model", `{"model":"auto","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, "route_auto"},
	}
	for _, tc := range violations {
		t.Run(tc.name, func(t *testing.T) {
			vw := newRelayWorld(t, never(t), never(t))
			vc := vw.strictChat(tc.body)
			if answered(vc) != http.StatusConflict {
				t.Fatalf("status = %d, want 409: %s", answered(vc), sent(vc))
			}
			if got := header(vc, violationHeader); got != tc.invariant {
				t.Errorf("%s = %q, want %q", violationHeader, got, tc.invariant)
			}
		})
	}
}

// TestQA132_SingleParallelAndMultiTurnFunctionCalling verifies single tool call,
// parallel tool calls in a single streaming turn, text-then-tool unified index
// allocation, and multi-turn tool_result round-trips.
func TestQA132_SingleParallelAndMultiTurnFunctionCalling(t *testing.T) {
	// 1. Parallel tool calls in one streaming turn (index 0 and index 1 after text block).
	sseParallel := strings.Join([]string{
		`data: {"choices":[{"delta":{"role":"assistant","content":"Checking both regions."},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_us","type":"function","function":{"name":"get_metrics","arguments":"{\"region\":\"us-east\"}"}}]},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_eu","type":"function","function":{"name":"get_metrics","arguments":"{\"region\":\"eu-west\"}"}}]},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":34,"completion_tokens":22,"total_tokens":56}}`,
		`data: [DONE]`,
	}, "\n") + "\n"

	evs, prompt, completion := runTranslator(t, sseParallel)
	if prompt != 34 || completion != 22 {
		t.Fatalf("usage = %d/%d, want 34/22", prompt, completion)
	}

	var startedTypes []string
	var startedIndices []int
	for _, e := range evs {
		if e.Event == "content_block_start" {
			cb := e.Data["content_block"].(map[string]any)
			startedTypes = append(startedTypes, cb["type"].(string))
			startedIndices = append(startedIndices, int(e.Data["index"].(float64)))
		}
	}
	if strings.Join(startedTypes, ",") != "text,tool_use,tool_use" {
		t.Fatalf("started block types = %v, want [text tool_use tool_use]", startedTypes)
	}
	if len(startedIndices) != 3 || startedIndices[0] != 0 || startedIndices[1] != 1 || startedIndices[2] != 2 {
		t.Fatalf("started block indices = %v, want [0 1 2]", startedIndices)
	}

	// 2. Multi-turn conversation with parallel tool_use and parallel tool_result blocks.
	req := &AnthropicRequest{
		System: json.RawMessage(`"You are an SRE assistant."`),
		Messages: []AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`"Compare US and EU latency."`)},
			{
				Role: "assistant",
				Content: json.RawMessage(`[
					{"type":"thinking","thinking":"Need metrics from both regions."},
					{"type":"text","text":"Checking both regions."},
					{"type":"tool_use","id":"call_us","name":"get_metrics","input":{"region":"us-east"}},
					{"type":"tool_use","id":"call_eu","name":"get_metrics","input":{"region":"eu-west"}}
				]`),
			},
			{
				Role: "user",
				Content: json.RawMessage(`[
					{"type":"tool_result","tool_use_id":"call_us","content":"{\"p99_ms\":18}"},
					{"type":"tool_result","tool_use_id":"call_eu","content":"{\"p99_ms\":24}"}
				]`),
			},
		},
	}
	msgs := anthropicToOpenAIMessages(req)
	if len(msgs) != 5 {
		t.Fatalf("got %d OpenAI messages, want 5 (system, user, assistant, tool, tool)", len(msgs))
	}
	if msgs[2].ReasoningContent != "Need metrics from both regions." || len(msgs[2].ToolCalls) != 2 {
		t.Fatalf("assistant turn lost reasoning_content or parallel tool_calls: %+v", msgs[2])
	}
	if msgs[3].Role != "tool" || msgs[3].ToolCallID != "call_us" || msgs[4].Role != "tool" || msgs[4].ToolCallID != "call_eu" {
		t.Fatalf("parallel tool_result messages = (%+v, %+v)", msgs[3], msgs[4])
	}
}

// TestQA132_ReasoningTokensAndInlineDeepSeekThinkStripping verifies reasoning
// effort mapping across model vocabularies, streaming thinking_delta isolation,
// and DeepSeek inline </think> stripping across split SSE chunks.
func TestQA132_ReasoningTokensAndInlineDeepSeekThinkStripping(t *testing.T) {
	// 1. Vocabulary & budget_tokens ordinal mapping.
	if got := thinkingVocabularyForUpstream("deepseek-v4-pro"); got != "glm" {
		t.Errorf("deepseek-v4-pro vocab = %q, want glm", got)
	}
	if got := thinkingVocabularyForUpstream("gpt-5.2"); got != "openai" {
		t.Errorf("gpt-5.2 vocab = %q, want openai", got)
	}
	if got := thinkingVocabularyForUpstream("qwen3.5-397b-a17b"); got != "" {
		t.Errorf("qwen3.5 vocab = %q, want empty", got)
	}

	if got := anthropicThinkingToReasoningEffort(json.RawMessage(`{"type":"enabled","budget_tokens":4096}`), "glm"); got != "high" {
		t.Errorf("glm 4k budget = %q, want high", got)
	}
	if got := anthropicThinkingToReasoningEffort(json.RawMessage(`{"type":"enabled","budget_tokens":24000}`), "glm"); got != "max" {
		t.Errorf("glm 24k budget = %q, want max", got)
	}
	if got := anthropicThinkingToReasoningEffort(json.RawMessage(`{"type":"enabled","budget_tokens":2048}`), "openai"); got != "low" {
		t.Errorf("openai 2k budget = %q, want low", got)
	}
	if got := anthropicThinkingToReasoningEffort(json.RawMessage(`{"type":"enabled","budget_tokens":8192}`), "openai"); got != "medium" {
		t.Errorf("openai 8k budget = %q, want medium", got)
	}
	if got := anthropicThinkingToReasoningEffort(json.RawMessage(`{"type":"enabled","budget_tokens":24000}`), "openai"); got != "high" {
		t.Errorf("openai 24k budget = %q, want high", got)
	}

	// 2. DeepSeek inline </think> split across three SSE chunks must strip reasoning
	// and never leak </think> into visible text deltas.
	sseSplitThink := strings.Join([]string{
		`data: {"choices":[{"delta":{"role":"assistant","content":"Step 1: analyze edge cases.</"},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{"content":"thi"},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{"content":"nk>Verified output."},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	}, "\n") + "\n"

	evs := runTranslatorModel(t, sseSplitThink, "deepseek-v4-pro")
	var visible strings.Builder
	for _, e := range evs {
		if e.Event == "content_block_delta" {
			d := e.Data["delta"].(map[string]any)
			if d["type"] == "text_delta" {
				visible.WriteString(d["text"].(string))
			}
		}
	}
	if visible.String() != "Verified output." {
		t.Fatalf("visible text after split </think> = %q, want %q", visible.String(), "Verified output.")
	}
}

// TestQA132_MaxTokensTruncationAndStreamInterruptionRecovery verifies finish_reason
// "length" -> "max_tokens" translation across streaming and non-streaming responses,
// plus AnthropicWriter stream reset safety on mid-stream interruption.
func TestQA132_MaxTokensTruncationAndStreamInterruptionRecovery(t *testing.T) {
	// 1. Streaming truncation ("finish_reason":"length" -> "stop_reason":"max_tokens").
	sseTruncated := strings.Join([]string{
		`data: {"choices":[{"delta":{"role":"assistant","content":"Partial token sequence"},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"length"}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":16,"total_tokens":28}}`,
		`data: [DONE]`,
	}, "\n") + "\n"

	evs, _, _ := runTranslator(t, sseTruncated)
	var stopReason string
	for _, e := range evs {
		if e.Event == "message_delta" {
			stopReason, _ = e.Data["delta"].(map[string]any)["stop_reason"].(string)
		}
	}
	if stopReason != "max_tokens" {
		t.Fatalf("streaming length finish_reason mapped to %q, want max_tokens", stopReason)
	}

	// 2. Non-streaming truncation ("finish_reason":"length" -> "stop_reason":"max_tokens").
	nonStreamTrunc := []byte(`{"choices":[{"message":{"role":"assistant","content":"Truncated"},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":16}}`)
	resp, prompt, completion := openAIResponseToAnthropic(nonStreamTrunc, "glm-5.2", "req_trunc")
	if resp["stop_reason"] != "max_tokens" || prompt != 10 || completion != 16 {
		t.Fatalf("non-streaming truncation = (%v, %d, %d), want (max_tokens, 10, 16)", resp["stop_reason"], prompt, completion)
	}

	// 3. Stream interruption & Reset invariants on AnthropicWriter.
	var emptyBuf bytes.Buffer
	unopened := streamer(&emptyBuf, true)
	if err := unopened.Close(0, 0, 0); err != nil {
		t.Fatalf("unopened Close: %v", err)
	}
	if emptyBuf.Len() != 0 {
		t.Fatalf("unopened stream emitted bytes on Close: %q", emptyBuf.String())
	}

	var interruptedBuf bytes.Buffer
	interrupted := streamer(&interruptedBuf, true)
	if _, err := interrupted.Write([]byte("partial response before upstream disconnect")); err != nil {
		t.Fatalf("interrupted Write: %v", err)
	}
	if !interrupted.StreamSent {
		t.Fatal("expected StreamSent=true after writing bytes to client")
	}
	interrupted.Reset()
	if interrupted.MessageString() != "" || len(interrupted.Buffer) != 0 {
		t.Fatalf("Reset did not clear buffered partial message: %q", interrupted.MessageString())
	}
	if !interrupted.StreamSent || !interrupted.headerSent {
		t.Fatal("Reset must preserve StreamSent and headerSent so failover cannot splice a second stream onto an already-opened client response")
	}
}
