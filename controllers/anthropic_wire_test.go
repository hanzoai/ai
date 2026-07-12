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
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestAnthropicSSE_WireFormat_ToolStream reproduces, at the byte level, exactly
// what a Claude Code client receives from /v1/messages when the upstream returns
// a streamed tool call. It drives translateOpenAIStream with the SAME emit
// closure the controller uses (proxyAnthropicViaOpenAI):
//
//	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, json)
//
// so the assertions are on the actual SSE wire the agent parses — the thing the
// old raw-OpenAI passthrough got wrong. This is the acceptance-1 curl, offline.
func TestAnthropicSSE_WireFormat_ToolStream(t *testing.T) {
	// A realistic upstream OpenAI tool-call stream (GLM/DeepSeek-shaped): a
	// reasoning preamble, the tool call with incrementally-streamed arguments,
	// the tool_calls finish_reason, then the forced usage chunk.
	upstream := strings.Join([]string{
		`data: {"id":"x","model":"glm-5.2","choices":[{"delta":{"role":"assistant","reasoning_content":"The user asked for weather; call the tool."},"finish_reason":null}]}`,
		`data: {"id":"x","model":"glm-5.2","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"x","model":"glm-5.2","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":"}}]},"finish_reason":null}]}`,
		`data: {"id":"x","model":"glm-5.2","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"SF\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"x","model":"glm-5.2","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"x","model":"glm-5.2","choices":[],"usage":{"prompt_tokens":42,"completion_tokens":11,"total_tokens":53}}`,
		`data: [DONE]`,
	}, "\n") + "\n"

	// The controller's exact wire emit.
	var wire bytes.Buffer
	emit := func(event string, data interface{}) error {
		b, err := json.Marshal(data)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(&wire, "event: %s\ndata: %s\n\n", event, b)
		return err
	}

	prompt, completion, _ := translateOpenAIStream(strings.NewReader(upstream), emit, "glm-5.2", "req_wire")
	got := wire.String()
	t.Logf("\n----- Anthropic SSE on the wire (what Claude Code receives) -----\n%s----------------------------------------------------------------", got)

	// 1. NOT a single raw OpenAI frame leaks (the bug: object=chat.completion.chunk,
	// choices[], raw reasoning_content). Note "delta" is a LEGITIMATE Anthropic key
	// (content_block_delta / message_delta each carry a delta object), so it is not
	// a leak marker — the OpenAI-specific tokens below are.
	for _, forbidden := range []string{"chat.completion.chunk", `"choices"`, "reasoning_content", `"object":"chat`} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("raw OpenAI token %q leaked onto the Anthropic wire:\n%s", forbidden, got)
		}
	}

	// 2a. The Anthropic EVENT skeleton Claude Code parses, strictly in order.
	mustOrder := []string{
		"event: message_start",
		"event: content_block_start", // thinking block
		"event: content_block_stop",
		"event: content_block_start", // tool_use block
		"event: content_block_delta", // input_json_delta
		"event: content_block_stop",
		"event: message_delta",
		`"stop_reason":"tool_use"`, // load-bearing: Claude Code runs the tool only on this
		"event: message_stop",
	}
	idx := 0
	for _, needle := range mustOrder {
		at := strings.Index(got[idx:], needle)
		if at < 0 {
			t.Fatalf("missing/out-of-order Anthropic frame %q after offset %d:\n%s", needle, idx, got)
		}
		idx += at + len(needle)
	}

	// 2b. The block payloads are present (JSON key order is alphabetical, so field
	// presence is asserted independent of intra-object ordering).
	for _, needle := range []string{
		`"type":"thinking"`,    // reasoning_content → thinking block, never raw
		`"type":"tool_use"`,    // the tool call block
		`"name":"get_weather"`, // the tool name Claude Code dispatches
		`"id":"call_abc"`,      // the tool_use id it echoes back in tool_result
		`"type":"input_json_delta"`,
	} {
		if !strings.Contains(got, needle) {
			t.Fatalf("missing Anthropic payload token %q:\n%s", needle, got)
		}
	}

	// 3. The streamed input_json_delta fragments reassemble to the full tool input.
	var partial strings.Builder
	for _, line := range strings.Split(got, "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var ev struct {
			Delta struct {
				Type        string `json:"type"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev) == nil && ev.Delta.Type == "input_json_delta" {
			partial.WriteString(ev.Delta.PartialJSON)
		}
	}
	if partial.String() != `{"city":"SF"}` {
		t.Fatalf("reassembled tool input = %q, want %q", partial.String(), `{"city":"SF"}`)
	}

	// 4. Usage is captured for billing (funded key + tool + stream is never free).
	if prompt != 42 || completion != 11 {
		t.Fatalf("captured usage = %d/%d, want 42/11", prompt, completion)
	}
}
