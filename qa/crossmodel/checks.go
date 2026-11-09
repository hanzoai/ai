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

package crossmodel

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The checks below are pure functions over bytes so they can be proven against a
// canned response in CI (checks_test.go) as well as against a live family
// (live_test.go). Each returns the contract violations it found; empty means the
// response conforms.

// openAIFinishReasons and anthropicStopReasons are the terminal reasons each API
// documents. A chunk that ends with anything else has invented its own vocabulary.
var (
	openAIFinishReasons  = []string{"stop", "length", "tool_calls", "content_filter", "function_call"}
	anthropicStopReasons = []string{"end_turn", "max_tokens", "stop_sequence", "tool_use", "pause_turn", "refusal"}

	// anthropicErrorTypes is the error.type set the Anthropic API documents.
	anthropicErrorTypes = []string{
		"authentication_error", "billing_error", "invalid_request_error", "not_found_error",
		"rate_limit_error", "overloaded_error", "api_error",
	}
)

// CheckOpenAIChatCompletion asserts the sync /v1/chat/completions contract.
func CheckOpenAIChatCompletion(raw []byte) []string {
	var problems []string
	body, err := decode(raw)
	if err != nil {
		return append(problems, err.Error())
	}
	if str(body, "id") == "" {
		problems = append(problems, `id is missing or empty`)
	}
	if got := str(body, "object"); got != "chat.completion" {
		problems = append(problems, fmt.Sprintf("object = %q, want \"chat.completion\"", got))
	}
	if _, ok := num(body, "created"); !ok {
		problems = append(problems, "created is missing or not a number")
	}
	choices := arr(body, "choices")
	if len(choices) == 0 {
		return append(problems, "choices is empty")
	}
	first, _ := choices[0].(map[string]any)
	if idx, ok := num(first, "index"); !ok || int64(idx) != 0 {
		problems = append(problems, "choices[0].index is missing or not 0")
	}
	msg, _ := first["message"].(map[string]any)
	if msg == nil {
		return append(problems, "choices[0].message is missing")
	}
	if got := str(msg, "role"); got != "assistant" {
		problems = append(problems, fmt.Sprintf("choices[0].message.role = %q, want \"assistant\"", got))
	}
	problems = append(problems, checkFinishReason(first)...)
	if str(msg, "content") == "" && len(arr(msg, "tool_calls")) == 0 {
		problems = append(problems, "message carries neither content nor tool_calls")
	}
	return append(problems, checkOpenAIUsage(body)...)
}

// CheckOpenAIStream asserts the OpenAI SSE contract over the frames that arrived:
// every payload is a chat.completion.chunk, at least one carries delta.content, the
// last data payload before the sentinel carries a valid finish_reason, and the
// stream ends with exactly `data: [DONE]`.
func CheckOpenAIStream(frames []Frame) []string {
	var problems []string
	if len(frames) == 0 {
		return append(problems, "stream carried no data frames")
	}
	if frames[len(frames)-1].Data != "[DONE]" {
		problems = append(problems, fmt.Sprintf("last frame is %q, want the sentinel \"data: [DONE]\"", frames[len(frames)-1].Data))
	}

	var contentSeen, finishSeen bool
	finishValue := ""
	for i, f := range frames {
		if f.Data == "[DONE]" {
			if i != len(frames)-1 {
				problems = append(problems, fmt.Sprintf("frame %d is the [DONE] sentinel but is not last", i))
			}
			continue
		}
		chunk, err := decode([]byte(f.Data))
		if err != nil {
			problems = append(problems, fmt.Sprintf("frame %d: %v", i, err))
			continue
		}
		if got := str(chunk, "object"); got != "chat.completion.chunk" {
			problems = append(problems, fmt.Sprintf("frame %d: object = %q, want \"chat.completion.chunk\"", i, got))
		}
		if i == 0 && str(chunk, "id") == "" {
			problems = append(problems, "first chunk carries no id")
		}
		choices := arr(chunk, "choices")
		if len(choices) == 0 {
			continue // usage-only final chunk
		}
		choice, ok := choices[0].(map[string]any)
		if !ok {
			problems = append(problems, fmt.Sprintf("frame %d: choices[0] is not an object", i))
			continue
		}
		delta, _ := choice["delta"].(map[string]any)
		if delta == nil {
			problems = append(problems, fmt.Sprintf("frame %d: choices[0].delta is missing", i))
			continue
		}
		if str(delta, "content") != "" {
			contentSeen = true
		}
		if fr, ok := choice["finish_reason"]; ok && fr != nil {
			if finishSeen {
				problems = append(problems, fmt.Sprintf("frame %d repeats finish_reason after %q", i, finishValue))
			}
			finishSeen, finishValue = true, str(choice, "finish_reason")
			if !inSet(finishValue, openAIFinishReasons) {
				problems = append(problems, fmt.Sprintf("finish_reason = %q, want one of %s", finishValue, strings.Join(openAIFinishReasons, "|")))
			}
		}
	}
	if !contentSeen {
		problems = append(problems, "no frame carried delta.content")
	}
	if !finishSeen {
		problems = append(problems, "stream ended without a finish_reason")
	}
	return problems
}

// CheckOpenAIToolCalls asserts the sync tool-call contract: finish_reason
// tool_calls, and wantCount calls each with an id, type function, a name from the
// requested set, and arguments that serialize to a JSON object.
func CheckOpenAIToolCalls(raw []byte, wantNames []string, minCalls int) []string {
	var problems []string
	body, err := decode(raw)
	if err != nil {
		return append(problems, err.Error())
	}
	choices := arr(body, "choices")
	if len(choices) == 0 {
		return append(problems, "choices is empty")
	}
	first, _ := choices[0].(map[string]any)
	if got := str(first, "finish_reason"); got != "tool_calls" {
		problems = append(problems, fmt.Sprintf("finish_reason = %q, want \"tool_calls\"", got))
	}
	msg, _ := first["message"].(map[string]any)
	calls := arr(msg, "tool_calls")
	if len(calls) < minCalls {
		problems = append(problems, fmt.Sprintf("tool_calls has %d entries, want at least %d", len(calls), minCalls))
	}
	for i, rawCall := range calls {
		call, _ := rawCall.(map[string]any)
		if str(call, "id") == "" {
			problems = append(problems, fmt.Sprintf("tool_calls[%d].id is missing or empty", i))
		}
		if got := str(call, "type"); got != "function" {
			problems = append(problems, fmt.Sprintf("tool_calls[%d].type = %q, want \"function\"", i, got))
		}
		fn, _ := call["function"].(map[string]any)
		name := str(fn, "name")
		if name == "" {
			problems = append(problems, fmt.Sprintf("tool_calls[%d].function.name is missing or empty", i))
		} else if len(wantNames) > 0 && !inSet(name, wantNames) {
			problems = append(problems, fmt.Sprintf("tool_calls[%d].function.name = %q, not one of the requested tools %s", i, name, strings.Join(wantNames, "|")))
		}
		problems = append(problems, checkArguments(fmt.Sprintf("tool_calls[%d].function.arguments", i), fn["arguments"])...)
	}
	return problems
}

// CheckOpenAIStreamToolCalls reassembles delta.tool_calls fragments the way a client
// must, and asserts the stream serializes complete, parseable calls. This is where a
// provider that splits arguments across chunks without an index, or emits truncated
// JSON, gets caught.
func CheckOpenAIStreamToolCalls(frames []Frame) []string {
	var problems []string
	type acc struct{ id, name, args string }
	calls := map[int]*acc{}
	var order []int
	slot := 0 // the call slot fragments without an index are landing in
	finish := ""
	for i, f := range frames {
		if f.Data == "[DONE]" {
			continue
		}
		chunk, err := decode([]byte(f.Data))
		if err != nil {
			problems = append(problems, fmt.Sprintf("frame %d: %v", i, err))
			continue
		}
		for _, rawChoice := range arr(chunk, "choices") {
			choice, _ := rawChoice.(map[string]any)
			if fr, ok := choice["finish_reason"]; ok && fr != nil {
				finish = str(choice, "finish_reason")
			}
			delta, _ := choice["delta"].(map[string]any)
			for j, rawCall := range arr(delta, "tool_calls") {
				call, _ := rawCall.(map[string]any)
				key, indexed := num(call, "index")
				if !indexed {
					// Without an index a client cannot tell which call a fragment
					// belongs to. Position is only safe while one call is in flight:
					// a second id appearing in the same delta, or replacing the id
					// already accumulated in this slot, is the ambiguity caught here.
					current := calls[slot]
					id := str(call, "id")
					if j > 0 || (current != nil && current.id != "" && id != "" && current.id != id) {
						problems = append(problems, fmt.Sprintf("frame %d: delta.tool_calls[%d] carries no index", i, j))
					}
					key = float64(slot)
				} else {
					slot = int(key)
				}
				a := calls[int(key)]
				if a == nil {
					a = &acc{}
					calls[int(key)], order = a, append(order, int(key))
				}
				if id := str(call, "id"); id != "" {
					a.id += id
				}
				fn, _ := call["function"].(map[string]any)
				if name := str(fn, "name"); name != "" {
					a.name += name
				}
				if frag, ok := fn["arguments"].(string); ok {
					a.args += frag
				}
			}
		}
	}
	if finish != "tool_calls" {
		problems = append(problems, fmt.Sprintf("stream finish_reason = %q, want \"tool_calls\"", finish))
	}
	if len(order) == 0 {
		return append(problems, "stream carried no delta.tool_calls")
	}
	for _, key := range order {
		a := calls[key]
		if a.id == "" {
			problems = append(problems, fmt.Sprintf("assembled call %d has no id", key))
		}
		if a.name == "" {
			problems = append(problems, fmt.Sprintf("assembled call %d has no function name", key))
		}
		problems = append(problems, checkArguments(fmt.Sprintf("assembled call %d arguments", key), a.args)...)
	}
	return problems
}

// CheckJSONObjectContent asserts that the assistant message is parseable JSON: the
// response_format json_object / json_schema contract. requiredKeys are only checked
// when given (the json_schema case).
func CheckJSONObjectContent(raw []byte, requiredKeys ...string) []string {
	var problems []string
	body, err := decode(raw)
	if err != nil {
		return append(problems, err.Error())
	}
	choices := arr(body, "choices")
	if len(choices) == 0 {
		return append(problems, "choices is empty")
	}
	first, _ := choices[0].(map[string]any)
	msg, _ := first["message"].(map[string]any)
	content := str(msg, "content")
	if content == "" {
		return append(problems, "message.content is empty; a json response_format must return JSON text")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return append(problems, fmt.Sprintf("message.content is not a JSON object: %v (%s)", err, clip(content)))
	}
	for _, k := range requiredKeys {
		if _, ok := parsed[k]; !ok {
			problems = append(problems, fmt.Sprintf("json output is missing the required key %q", k))
		}
	}
	return problems
}

// CheckAnthropicMessage asserts the sync /v1/messages contract.
func CheckAnthropicMessage(raw []byte) []string {
	var problems []string
	body, err := decode(raw)
	if err != nil {
		return append(problems, err.Error())
	}
	if got := str(body, "type"); got != "message" {
		problems = append(problems, fmt.Sprintf("type = %q, want \"message\"", got))
	}
	if str(body, "id") == "" {
		problems = append(problems, "id is missing or empty")
	}
	if got := str(body, "role"); got != "assistant" {
		problems = append(problems, fmt.Sprintf("role = %q, want \"assistant\"", got))
	}
	if str(body, "model") == "" {
		problems = append(problems, "model is not echoed back")
	}
	blocks := arr(body, "content")
	if len(blocks) == 0 {
		return append(problems, "content blocks are empty")
	}
	textSeen := false
	for i, rawBlock := range blocks {
		block, _ := rawBlock.(map[string]any)
		switch str(block, "type") {
		case "text":
			if str(block, "text") != "" {
				textSeen = true
			}
		case "tool_use", "thinking", "redacted_thinking":
		default:
			problems = append(problems, fmt.Sprintf("content[%d].type = %q, want text|tool_use|thinking", i, str(block, "type")))
		}
	}
	if !textSeen {
		problems = append(problems, "no content block carries text")
	}
	if got := str(body, "stop_reason"); !inSet(got, anthropicStopReasons) {
		problems = append(problems, fmt.Sprintf("stop_reason = %q, want one of %s", got, strings.Join(anthropicStopReasons, "|")))
	}
	usage, _ := body["usage"].(map[string]any)
	if usage == nil {
		return append(problems, "usage is missing")
	}
	for _, k := range []string{"input_tokens", "output_tokens"} {
		if _, ok := num(usage, k); !ok {
			problems = append(problems, fmt.Sprintf("usage.%s is missing or not a number", k))
		}
	}
	return problems
}

// CheckAnthropicStream asserts the Anthropic SSE contract: the documented event
// sequence arrives in order, every payload names its own event type, at least one
// text_delta carries text, and message_stop closes the stream.
func CheckAnthropicStream(frames []Frame) []string {
	var problems []string
	if len(frames) == 0 {
		return append(problems, "stream carried no data frames")
	}
	want := []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}

	events := make([]string, 0, len(frames))
	textDelta, stopClosed := false, false
	for i, f := range frames {
		payload, err := decode([]byte(f.Data))
		if err != nil {
			problems = append(problems, fmt.Sprintf("frame %d: %v", i, err))
			continue
		}
		typ := str(payload, "type")
		if f.Event != "" && typ != "" && typ != f.Event {
			problems = append(problems, fmt.Sprintf("frame %d: event: %s but payload type is %q", i, f.Event, typ))
		}
		events = append(events, typ)
		switch typ {
		case "content_block_delta":
			delta, _ := payload["delta"].(map[string]any)
			if str(delta, "type") != "text_delta" {
				continue
			}
			if str(delta, "text") == "" {
				problems = append(problems, fmt.Sprintf("frame %d: text_delta carries no text", i))
			}
			textDelta = true
		case "message_stop":
			stopClosed = true
		case "error":
			errObj, _ := payload["error"].(map[string]any)
			problems = append(problems, fmt.Sprintf("frame %d: stream errored: %s: %s", i, str(errObj, "type"), str(errObj, "message")))
		}
	}
	if pos := subsequencePos(events, want); pos < len(want) {
		problems = append(problems, fmt.Sprintf("event sequence never reached %q (got %s)", want[pos], strings.Join(dedupe(events), " ")))
	}
	if !textDelta {
		problems = append(problems, "no content_block_delta carried a text_delta")
	}
	if !stopClosed {
		problems = append(problems, "stream ended without message_stop")
	}
	return problems
}

// CheckOpenAIErrorEnvelope asserts the OpenAI error contract: {error:{message,type,
// param,code}}. It also names the gateway's own {status,msg,data,data2} envelope when
// that is what came back, because every SDK parsing this endpoint reads error.message
// and nothing else.
func CheckOpenAIErrorEnvelope(raw []byte) []string {
	var problems []string
	body, err := decode(raw)
	if err != nil {
		return append(problems, fmt.Sprintf("error response is not JSON: %s", clip(string(raw))))
	}
	if _, ok := body["msg"]; ok && body["error"] == nil {
		return []string{fmt.Sprintf("gateway envelope {status,msg,data,data2} instead of the OpenAI error envelope (msg=%q)", str(body, "msg"))}
	}
	errObj, _ := body["error"].(map[string]any)
	if errObj == nil {
		return append(problems, "error is missing or not an object")
	}
	if str(errObj, "message") == "" {
		problems = append(problems, "error.message is missing or empty")
	}
	if str(errObj, "type") == "" {
		problems = append(problems, "error.type is missing or empty")
	}
	if _, ok := errObj["code"]; !ok {
		problems = append(problems, "error.code is absent")
	}
	if p, ok := errObj["param"]; ok && p != nil {
		if _, isStr := p.(string); !isStr {
			problems = append(problems, "error.param is neither null nor a string")
		}
	}
	return problems
}

// CheckAnthropicErrorEnvelope asserts the Anthropic error contract: {type:"error",
// error:{type,message}} with a documented error type.
func CheckAnthropicErrorEnvelope(raw []byte) []string {
	var problems []string
	body, err := decode(raw)
	if err != nil {
		return append(problems, fmt.Sprintf("error response is not JSON: %s", clip(string(raw))))
	}
	if _, ok := body["msg"]; ok && body["error"] == nil {
		return []string{fmt.Sprintf("gateway envelope {status,msg,data,data2} instead of the Anthropic error envelope (msg=%q)", str(body, "msg"))}
	}
	if got := str(body, "type"); got != "error" {
		problems = append(problems, fmt.Sprintf("type = %q, want \"error\"", got))
	}
	errObj, _ := body["error"].(map[string]any)
	if errObj == nil {
		return append(problems, "error is missing or not an object")
	}
	if str(errObj, "message") == "" {
		problems = append(problems, "error.message is missing or empty")
	}
	if got := str(errObj, "type"); !inSet(got, anthropicErrorTypes) {
		problems = append(problems, fmt.Sprintf("error.type = %q, want one of %s", got, strings.Join(anthropicErrorTypes, "|")))
	}
	return problems
}

func checkFinishReason(choice map[string]any) []string {
	fr, ok := choice["finish_reason"]
	if !ok || fr == nil {
		return []string{"choices[0].finish_reason is missing or null"}
	}
	got, _ := fr.(string)
	if !inSet(got, openAIFinishReasons) {
		return []string{fmt.Sprintf("finish_reason = %q, want one of %s", got, strings.Join(openAIFinishReasons, "|"))}
	}
	return nil
}

func checkOpenAIUsage(body map[string]any) []string {
	usage, _ := body["usage"].(map[string]any)
	if usage == nil {
		return []string{"usage is missing"}
	}
	var problems []string
	for _, k := range []string{"prompt_tokens", "completion_tokens", "total_tokens"} {
		if _, ok := num(usage, k); !ok {
			problems = append(problems, fmt.Sprintf("usage.%s is missing or not a number", k))
		}
	}
	return problems
}

// checkArguments asserts tool arguments are a JSON object. An empty string is called
// out: #138 is about broken tool-call serialization, and an empty argument blob is
// how a dropped argument set shows up on the wire.
func checkArguments(what string, v any) []string {
	s, ok := v.(string)
	if !ok {
		return []string{fmt.Sprintf("%s is %T, want a JSON string", what, v)}
	}
	if strings.TrimSpace(s) == "" {
		return []string{fmt.Sprintf("%s is empty", what)}
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return []string{fmt.Sprintf("%s is not a JSON object: %v (%s)", what, err, clip(s))}
	}
	return nil
}

// subsequencePos reports how far the wanted event sequence got matched, in order,
// so a caller can name the first event that never arrived.
func subsequencePos(got, want []string) int {
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	return i
}

func dedupe(in []string) []string {
	var out []string
	for i, v := range in {
		if i == 0 || in[i-1] != v {
			out = append(out, v)
		}
	}
	return out
}

func decode(raw []byte) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("response body is not a JSON object: %v (%s)", err, clip(string(raw)))
	}
	return m, nil
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }

func num(m map[string]any, k string) (float64, bool) { n, ok := m[k].(float64); return n, ok }

func arr(m map[string]any, k string) []any { a, _ := m[k].([]any); return a }

func inSet(v string, set []string) bool {
	for _, s := range set {
		if v == s {
			return true
		}
	}
	return false
}

func clip(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}
