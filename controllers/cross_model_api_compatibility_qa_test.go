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
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	openai "github.com/hanzoai/go-openai"
)

// TestQA138_CrossModelDialectTranslationAndToolChoiceParity verifies request and
// response schema translation between Anthropic (/v1/messages) and OpenAI
// (/v1/chat/completions) across model families (GLM, DeepSeek, OpenAI, Qwen, Kimi, Zen).
func TestQA138_CrossModelDialectTranslationAndToolChoiceParity(t *testing.T) {
	// 1. Tool definitions & tool_choice mapping (auto -> auto, any -> required, tool -> ToolChoice).
	tools := []AnthropicTool{
		{
			Name:        "query_cluster",
			Description: "Query cluster health",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"cluster":{"type":"string"}},"required":["cluster"]}`),
		},
	}
	oaTools := anthropicToolsToOpenAI(tools)
	if len(oaTools) != 1 || oaTools[0].Type != openai.ToolTypeFunction || oaTools[0].Function.Name != "query_cluster" {
		t.Fatalf("anthropicToolsToOpenAI = %+v", oaTools)
	}
	if got := anthropicToolChoiceToOpenAI(json.RawMessage(`{"type":"auto"}`)); got != "auto" {
		t.Errorf("tool_choice auto = %v, want auto", got)
	}
	if got := anthropicToolChoiceToOpenAI(json.RawMessage(`{"type":"any"}`)); got != "required" {
		t.Errorf("tool_choice any = %v, want required", got)
	}
	tcSpecific := anthropicToolChoiceToOpenAI(json.RawMessage(`{"type":"tool","name":"query_cluster"}`))
	if oc, ok := tcSpecific.(openai.ToolChoice); !ok || oc.Function.Name != "query_cluster" {
		t.Errorf("tool_choice specific = %+v, want query_cluster", tcSpecific)
	}

	// 2. Multimodal image block + text + tool_use/tool_result round-trip across dialects.
	req := &AnthropicRequest{
		System: json.RawMessage(`"Respond concisely across all model families."`),
		Messages: []AnthropicMessage{
			{
				Role: "user",
				Content: json.RawMessage(`[
					{"type":"text","text":"Inspect this architecture diagram."},
					{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"/9j/4AAQ"}}
				]`),
			},
			{
				Role: "assistant",
				Content: json.RawMessage(`[
					{"type":"text","text":"Querying cluster state."},
					{"type":"tool_use","id":"toolu_99","name":"query_cluster","input":{"cluster":"prod-us"}}
				]`),
			},
			{
				Role: "user",
				Content: json.RawMessage(`[
					{"type":"tool_result","tool_use_id":"toolu_99","content":"{\"healthy\":true}"}
				]`),
			},
		},
	}
	msgs := anthropicToOpenAIMessages(req)
	if len(msgs) != 4 {
		t.Fatalf("expected 4 OpenAI messages, got %d", len(msgs))
	}
	if len(msgs[1].MultiContent) != 2 || msgs[1].MultiContent[1].ImageURL.URL != "data:image/jpeg;base64,/9j/4AAQ" {
		t.Fatalf("multimodal image URL translation failed: %+v", msgs[1].MultiContent)
	}

	// 3. Cross-model non-streaming response translation across upstream model families.
	upstreams := []string{"glm-5.2", "deepseek-v4-pro", "gpt-5.2", "qwen3.5-397b-a17b", "kimi-k2.6", "zen5-pro"}
	for _, modelID := range upstreams {
		oaResp := []byte(`{
			"choices":[{
				"message":{
					"role":"assistant",
					"content":"",
					"tool_calls":[{"id":"call_42","type":"function","function":{"name":"query_cluster","arguments":"{\"cluster\":\"prod-us\"}"}}]
				},
				"finish_reason":"tool_calls"
			}],
			"usage":{"prompt_tokens":28,"completion_tokens":11}
		}`)
		resp, prompt, completion := openAIResponseToAnthropic(oaResp, modelID, "req_"+modelID)
		if resp["model"] != modelID || resp["stop_reason"] != "tool_use" {
			t.Fatalf("%s: resp header = %#v", modelID, resp)
		}
		if prompt != 28 || completion != 11 {
			t.Fatalf("%s: usage = %d/%d, want 28/11", modelID, prompt, completion)
		}
		content := resp["content"].([]any)
		block := content[0].(map[string]any)
		if block["type"] != "tool_use" || block["id"] != "call_42" || block["name"] != "query_cluster" {
			t.Fatalf("%s: tool_use block = %#v", modelID, block)
		}
	}
}

// TestQA138_CrossModelStreamingSSEProtocolParity verifies SSE streaming protocol
// parity across OpenAI -> Anthropic (/v1/messages) and OpenAI -> Responses (/v1/responses).
func TestQA138_CrossModelStreamingSSEProtocolParity(t *testing.T) {
	// 1. OpenAI SSE -> Anthropic SSE with reasoning + text + tool_use in one unified stream.
	sse := strings.Join([]string{
		`data: {"choices":[{"delta":{"role":"assistant","reasoning_content":"Checking cluster topology."},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{"content":"Running diagnostics."},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_diag","type":"function","function":{"name":"query_cluster","arguments":"{\"cluster\":"}}]},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"eu-central\"}"}}]},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":40,"completion_tokens":19,"total_tokens":59}}`,
		`data: [DONE]`,
	}, "\n") + "\n"

	evs, prompt, completion := runTranslator(t, sse)
	if prompt != 40 || completion != 19 {
		t.Fatalf("Anthropic stream usage = %d/%d, want 40/19", prompt, completion)
	}
	if evs[0].Event != "message_start" || evs[len(evs)-1].Event != "message_stop" {
		t.Fatalf("Anthropic stream bookends = (%s, %s)", evs[0].Event, evs[len(evs)-1].Event)
	}

	// 2. OpenAI Chat Completion -> OpenAI Responses API (/v1/responses) streaming & non-streaming parity.
	chatBody := []byte(`{
		"choices":[{"message":{"role":"assistant","content":"Cross-model parity verified."}}],
		"usage":{"prompt_tokens":18,"completion_tokens":6,"total_tokens":24}
	}`)
	respReq := &OpenAIResponsesRequest{Model: "zen5-pro", Stream: true}

	streamBytes, err := responsesStreamBody(chatBody, respReq, map[string]string{})
	if err != nil {
		t.Fatalf("responsesStreamBody: %v", err)
	}
	streamStr := string(streamBytes)
	for _, ev := range []string{
		"event: response.created",
		"event: response.output_text.delta",
		"event: response.output_text.done",
		"event: response.completed",
	} {
		if !strings.Contains(streamStr, ev) {
			t.Fatalf("Responses SSE missing %q", ev)
		}
	}
	completed := lastEventData(t, streamStr, "response.completed")
	u := completed["response"].(map[string]any)["usage"].(map[string]any)
	if u["input_tokens"].(float64) != 18 || u["output_tokens"].(float64) != 6 || u["total_tokens"].(float64) != 24 {
		t.Fatalf("Responses SSE usage = %#v, want 18/6/24", u)
	}

	nonStreamBytes, err := openAIChatResponseToResponses(chatBody, &OpenAIResponsesRequest{Model: "zen5-pro"}, map[string]string{})
	if err != nil {
		t.Fatalf("openAIChatResponseToResponses: %v", err)
	}
	var nonStreamObj map[string]any
	if err := json.Unmarshal(nonStreamBytes, &nonStreamObj); err != nil {
		t.Fatalf("unmarshal Responses object: %v", err)
	}
	if nonStreamObj["object"] != "response" || nonStreamObj["status"] != "completed" {
		t.Fatalf("Responses envelope = %#v", nonStreamObj)
	}
}

// TestQA138_FamilyShadowRoutingAndModelIDSniffingParity verifies withModel body
// rewriting, familyRoutingText extraction across OpenAI and Anthropic wire formats,
// and sniffZenModel / sniffZenId extraction across both dialects.
func TestQA138_FamilyShadowRoutingAndModelIDSniffingParity(t *testing.T) {
	for _, targetSKU := range []string{"enso-flash", "zen5-coder", "glm-5.2"} {
		rewrittenBody := withModel([]byte(`{"model":"auto","max_tokens":128,"temperature":0.2,"messages":[{"role":"user","content":"ping"}]}`), targetSKU)
		var parsed map[string]any
		if err := json.Unmarshal(rewrittenBody, &parsed); err != nil {
			t.Fatalf("withModel(%s) invalid JSON: %v", targetSKU, err)
		}
		if parsed["model"] != targetSKU || parsed["max_tokens"].(float64) != 128 || parsed["temperature"].(float64) != 0.2 {
			t.Fatalf("withModel(%s) = %#v", targetSKU, parsed)
		}
	}

	oaText, oaMedia := familyRoutingText([]byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"analyze chart"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA"}}]}]}`))
	anText, anMedia := familyRoutingText([]byte(`{"messages":[{"role":"user","content":[{"type":"image","source":{}},{"type":"text","text":"analyze chart"}]}]}`))
	if oaText != "analyze chart" || anText != "analyze chart" || !oaMedia || !anMedia {
		t.Fatalf("familyRoutingText parity mismatch: oa=(%q,%v) an=(%q,%v)", oaText, oaMedia, anText, anMedia)
	}

	if got := sniffZenModel([]byte(`{"id":"chatcmpl-9","model":"zen5-pro","choices":[]}`)); got != "zen5-pro" {
		t.Errorf("sniffZenModel OpenAI = %q, want zen5-pro", got)
	}
	if got := sniffZenModel([]byte(`{"type":"message_start","message":{"id":"msg_9","model":"enso-ultra"}}`)); got != "enso-ultra" {
		t.Errorf("sniffZenModel Anthropic = %q, want enso-ultra", got)
	}
	if got := sniffZenId([]byte(`{"id":"chatcmpl-9","model":"zen5-pro","choices":[]}`)); got != "chatcmpl-9" {
		t.Errorf("sniffZenId OpenAI = %q, want chatcmpl-9", got)
	}
	if got := sniffZenId([]byte(`{"type":"message_start","message":{"id":"msg_9","model":"enso-ultra"}}`)); got != "msg_9" {
		t.Errorf("sniffZenId Anthropic = %q, want msg_9", got)
	}
}

// TestQA138_EndToEndRelayFailoverAndErrorFormatConsistency verifies that upstream
// 5xx errors fail over cleanly to Fallback1 while preserving the requested SKU
// billing identity, whereas client 4xx errors do not trigger fallback or debit funds.
func TestQA138_EndToEndRelayFailoverAndErrorFormatConsistency(t *testing.T) {
	// 1. Upstream 503 fails over from primary (relay-a) to fallback (relay-b) transparently.
	w := newRelayWorld(t, declines(http.StatusServiceUnavailable, "primary overloaded"), completes("served by fallback", 20, 0, 5))
	c := w.chat(`{"model":"relay-sku","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`)
	if answered(c) != http.StatusOK {
		t.Fatalf("failover chat status = %d, want 200: %s", answered(c), sent(c))
	}
	if w.a.asked() != 1 || w.b.asked() != 1 {
		t.Fatalf("upstream call counts = (a=%d, b=%d), want (1, 1)", w.a.asked(), w.b.asked())
	}
	if !strings.Contains(sent(c), "served by fallback") {
		t.Fatalf("response body missing fallback content: %s", sent(c))
	}
	ev := w.paid(t)
	if ev.Model != relaySku {
		t.Errorf("UsageEvent.Model = %q, want %q", ev.Model, relaySku)
	}

	// 2. Client 400 Bad Request is returned directly without invoking fallback or charging the customer.
	w400 := newRelayWorld(t, declines(http.StatusBadRequest, "messages: invalid role"), never(t))
	c400 := w400.chat(`{"model":"relay-sku","max_tokens":32,"messages":[{"role":"user","content":"bad"}]}`)
	if answered(c400) != http.StatusBadRequest {
		t.Fatalf("400 status = %d, want 400: %s", answered(c400), sent(c400))
	}
	if w400.b.asked() != 0 {
		t.Error("fallback upstream was called on a 400 client error")
	}
	if spent := w400.spent(t); spent != 0 {
		t.Errorf("400 client error debited %d cents, want 0", spent)
	}
}
