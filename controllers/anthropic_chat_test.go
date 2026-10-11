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
	"errors"
	"io"
	"strings"
	"testing"
)

// The two kinds of model a chat is translated for: one that thinks adaptively,
// takes an effort and no sampling parameter, and one that thinks within a budget
// and takes sampling parameters.
var (
	adaptive = claude{ID: "claude-new", Out: 128000, Adaptive: true, Vision: true, Schema: true,
		Efforts: map[string]bool{"low": true, "medium": true, "high": true, "max": true}}
	budgeted = claude{ID: "claude-old", Out: 64000, Budget: true, Vision: true, Efforts: map[string]bool{}}
)

// asMessages translates a chat body for m and decodes what the vendor is sent.
func asMessages(t *testing.T, body string, m claude) map[string]any {
	t.Helper()
	out, _, err := chatAsMessages([]byte(body), m)
	if err != nil {
		t.Fatalf("not translated: %v\n%s", err, body)
	}
	var v map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// same reports whether got, re-encoded, is the JSON want.
func same(t *testing.T, what string, got any, want string) {
	t.Helper()
	g, _ := json.Marshal(got)
	var w any
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("%s: the case's own JSON: %v", what, err)
	}
	wb, _ := json.Marshal(w)
	if string(g) != string(wb) {
		t.Errorf("%s:\n got %s\nwant %s", what, g, wb)
	}
}

// A chat completion is asked of Claude in the Messages API's own words: the system
// prompt apart, a tool call as a tool_use block and its results as tool_result
// blocks in ONE user turn with what the user said after them, an image as an image
// block, tools by their input schema, and the answer's ceiling as it was reserved.
func TestAChatIsAskedOfClaudeInItsOwnWords(t *testing.T) {
	v := asMessages(t, `{
	  "model":"anthropic/claude-new","max_tokens":900,"stream":false,"temperature":0.2,"seed":7,"logit_bias":{"1":2},
	  "stop":["END","  "],
	  "messages":[
	    {"role":"system","content":"Be brief."},
	    {"role":"user","content":[
	      {"type":"text","text":"What is in this picture?"},
	      {"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}},
	      {"type":"image_url","image_url":{"url":"https://example.com/cat.jpg"}}]},
	    {"role":"assistant","content":null,"tool_calls":[
	      {"id":"call_1","type":"function","function":{"name":"look","arguments":"{\"zoom\":2}"}},
	      {"id":"call_2","type":"function","function":{"name":"look","arguments":""}}]},
	    {"role":"tool","tool_call_id":"call_1","content":"a cat"},
	    {"role":"tool","tool_call_id":"call_2","content":[{"type":"text","text":"still a cat"}]},
	    {"role":"user","content":"So?"},
	    {"role":"developer","content":[{"type":"text","text":"Answer in English.","cache_control":{"type":"ephemeral"}}]}
	  ],
	  "tools":[{"type":"function","function":{"name":"look","description":"Look closer.","parameters":{"type":"object","properties":{"zoom":{"type":"integer"}}},"strict":true}},
	           {"type":"function","function":{"name":"wait"}}],
	  "tool_choice":"auto","parallel_tool_calls":false
	}`, adaptive)

	same(t, "model", v["model"], `"claude-new"`)
	same(t, "max_tokens", v["max_tokens"], `900`)
	same(t, "stream", v["stream"], `true`)
	same(t, "system", v["system"], `[{"type":"text","text":"Be brief."},{"type":"text","text":"Answer in English.","cache_control":{"type":"ephemeral"}}]`)
	same(t, "messages", v["messages"], `[
	  {"role":"user","content":[
	    {"type":"text","text":"What is in this picture?"},
	    {"type":"image","source":{"type":"base64","media_type":"image/png","data":"QUJD"}},
	    {"type":"image","source":{"type":"url","url":"https://example.com/cat.jpg"}}]},
	  {"role":"assistant","content":[
	    {"type":"tool_use","id":"call_1","name":"look","input":{"zoom":2}},
	    {"type":"tool_use","id":"call_2","name":"look","input":{}}]},
	  {"role":"user","content":[
	    {"type":"tool_result","tool_use_id":"call_1","content":"a cat"},
	    {"type":"tool_result","tool_use_id":"call_2","content":[{"type":"text","text":"still a cat"}]},
	    {"type":"text","text":"So?"}]}
	]`)
	same(t, "tools", v["tools"], `[
	  {"name":"look","description":"Look closer.","input_schema":{"type":"object","properties":{"zoom":{"type":"integer"}}},"strict":true},
	  {"name":"wait","input_schema":{"type":"object","properties":{}}}]`)
	same(t, "tool_choice", v["tool_choice"], `{"type":"auto","disable_parallel_tool_use":true}`)
	same(t, "stop_sequences", v["stop_sequences"], `["END"]`)
	// A model that thinks only adaptively takes no sampling parameter, and the
	// vendor has no word for a seed or a logit bias.
	for _, gone := range []string{"temperature", "top_p", "seed", "logit_bias", "stop", "parallel_tool_calls", "thinking", "output_config"} {
		if _, sent := v[gone]; sent {
			t.Errorf("%q was sent to the vendor", gone)
		}
	}
	// The caller marked its own cache breakpoint, so none is added.
	if _, sent := v["cache_control"]; sent {
		t.Error("a breakpoint was added beside the caller's own")
	}

	// A request that marks none is asked to cache its prompt, and a caller's own
	// top-level mark travels as written.
	plain := asMessages(t, `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, budgeted)
	same(t, "the added breakpoint", plain["cache_control"], `{"type":"ephemeral"}`)
	same(t, "the floor ceiling", plain["max_tokens"], `4096`)
	own := asMessages(t, `{"model":"m","cache_control":{"type":"ephemeral","ttl":"1h"},"messages":[{"role":"user","content":"hi"}]}`, budgeted)
	same(t, "the caller's breakpoint", own["cache_control"], `{"type":"ephemeral","ttl":"1h"}`)

	// A model that takes sampling parameters is sent them: a temperature within
	// the vendor's range, and a temperature or a top_p, never both.
	hot := asMessages(t, `{"model":"m","max_tokens":999999,"temperature":1.7,"top_p":0.9,"top_k":40,"stop":"END","messages":[{"role":"user","content":"hi"}]}`, budgeted)
	same(t, "temperature", hot["temperature"], `1`)
	same(t, "top_k", hot["top_k"], `40`)
	same(t, "stop", hot["stop_sequences"], `["END"]`)
	same(t, "the model's own ceiling", hot["max_tokens"], `64000`)
	if _, sent := hot["top_p"]; sent {
		t.Error("top_p was sent beside a temperature")
	}

	// A named tool is the vendor's tool choice, a required one its any; a JSON
	// schema is the answer's format where the model can be held to one.
	forced := asMessages(t, `{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"look"}}],
	  "tool_choice":{"type":"function","function":{"name":"look"}},
	  "response_format":{"type":"json_schema","json_schema":{"name":"n","schema":{"type":"object","additionalProperties":false}}}}`, adaptive)
	same(t, "a named tool", forced["tool_choice"], `{"type":"tool","name":"look"}`)
	same(t, "the format", forced["output_config"], `{"format":{"type":"json_schema","schema":{"type":"object","additionalProperties":false}}}`)
	required := asMessages(t, `{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"look"}}],"tool_choice":"required"}`, adaptive)
	same(t, "a required tool", required["tool_choice"], `{"type":"any"}`)
}

// Reasoning is asked the way the model takes it: an effort and adaptive thinking
// of a model that thinks adaptively, a budget of tokens of one that thinks within
// one — and of that one never in the middle of a tool loop or beside a forced
// tool, where the vendor refuses it. Nothing asked is nothing sent.
func TestReasoningIsAskedTheWayTheModelTakesIt(t *testing.T) {
	ask := func(extra string) string {
		return `{"model":"m","max_tokens":10000,"temperature":0.5,` + extra + `"messages":[{"role":"user","content":"hi"}]}`
	}
	for _, tc := range []struct {
		what, extra  string
		m            claude
		thinking     string // "" for none sent
		config       string
		temperature  bool
		wantReasoned bool
	}{
		{"nothing asked, adaptive", ``, adaptive, ``, ``, false, true},
		{"nothing asked, budget", ``, budgeted, ``, ``, true, true},
		{"an effort, adaptive", `"reasoning_effort":"high",`, adaptive, `{"type":"adaptive"}`, `{"effort":"high"}`, false, true},
		{"an effort the model lacks falls to the nearest below", `"reasoning":{"effort":"xhigh"},`, adaptive, `{"type":"adaptive"}`, `{"effort":"high"}`, false, true},
		{"minimal is the least effort", `"reasoning_effort":"minimal",`, adaptive, `{"type":"adaptive"}`, `{"effort":"low"}`, false, true},
		{"none asks for the least and no thinking", `"reasoning_effort":"none",`, adaptive, ``, `{"effort":"low"}`, false, true},
		{"a budget alone, adaptive", `"reasoning":{"max_tokens":2000},`, adaptive, `{"type":"adaptive"}`, ``, false, true},
		{"an effort, budget: its share of the ceiling", `"reasoning_effort":"medium",`, budgeted, `{"type":"enabled","budget_tokens":5000}`, ``, false, true},
		{"a stated budget", `"reasoning":{"max_tokens":3000,"exclude":true},`, budgeted, `{"type":"enabled","budget_tokens":3000}`, ``, false, false},
		{"a budget below the vendor's least is the least", `"reasoning":{"max_tokens":10},`, budgeted, `{"type":"enabled","budget_tokens":1024}`, ``, false, true},
		{"reasoning turned off", `"reasoning":{"enabled":false,"effort":"high"},`, budgeted, ``, ``, true, true},
		{"none, budget", `"reasoning_effort":"none",`, budgeted, ``, ``, true, true},
	} {
		out, hide, err := chatAsMessages([]byte(ask(tc.extra)), tc.m)
		if err != nil {
			t.Fatalf("%s: %v", tc.what, err)
		}
		var v map[string]any
		_ = json.Unmarshal(out, &v)
		check := func(field, want string) {
			got, sent := v[field]
			if want == "" {
				if sent {
					t.Errorf("%s: %s was sent: %v", tc.what, field, got)
				}
				return
			}
			same(t, tc.what+": "+field, got, want)
		}
		check("thinking", tc.thinking)
		check("output_config", tc.config)
		if _, sent := v["temperature"]; sent != tc.temperature {
			t.Errorf("%s: temperature sent = %v, want %v", tc.what, sent, tc.temperature)
		}
		if hide == tc.wantReasoned {
			t.Errorf("%s: hide = %v", tc.what, hide)
		}
	}

	// In the middle of a tool loop, and beside a forced tool, a model that thinks
	// within a budget is not asked to think; one that thinks adaptively still is.
	loop := `{"model":"m","max_tokens":10000,"reasoning_effort":"high","messages":[
	  {"role":"user","content":"hi"},
	  {"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},
	  {"role":"tool","tool_call_id":"c1","content":"done"}],
	  "tools":[{"type":"function","function":{"name":"f"}}]}`
	if v := asMessages(t, loop, budgeted); v["thinking"] != nil {
		t.Errorf("thinking was asked in the middle of a tool loop: %v", v["thinking"])
	}
	if v := asMessages(t, loop, adaptive); v["thinking"] == nil {
		t.Error("an adaptive model was not asked to think in a tool loop")
	}
	forced := `{"model":"m","max_tokens":10000,"reasoning_effort":"high","messages":[{"role":"user","content":"hi"}],
	  "tools":[{"type":"function","function":{"name":"f"}}],"tool_choice":"required"}`
	if v := asMessages(t, forced, budgeted); v["thinking"] != nil {
		t.Errorf("thinking was asked beside a forced tool: %v", v["thinking"])
	}
	// A ceiling with no room for the least budget asks for none.
	if v := asMessages(t, `{"model":"m","max_tokens":1024,"reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`, budgeted); v["thinking"] != nil {
		t.Errorf("a budget was asked of a ceiling it does not fit: %v", v["thinking"])
	}
}

// What the Messages API has no word for is not sent to it: the request goes where
// it went before, rather than being answered as something less than was asked.
func TestWhatClaudeHasNoWordForIsNotSentToIt(t *testing.T) {
	blind := claude{ID: "claude-blind", Budget: true}
	for what, tc := range map[string]struct {
		body string
		m    claude
	}{
		"audio":                                  {`{"messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"AAAA","format":"wav"}}]}]}`, adaptive},
		"an image for a model with no eyes":      {`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}}]}]}`, blind},
		"an image type the vendor does not read": {`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/tiff;base64,QUJD"}}]}]}`, adaptive},
		"a stored file":                          {`{"messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"file_1"}}]}]}`, adaptive},
		"a JSON mode with no schema":             {`{"messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_object"}}`, adaptive},
		"a schema the model cannot hold to":      {`{"messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_schema","json_schema":{"schema":{"type":"object"}}}}`, budgeted},
		"an answer begun for the model":          {`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"The answer is"}]}`, adaptive},
		"a conversation the model opens":         {`{"messages":[{"role":"assistant","content":"hello"},{"role":"user","content":"hi"}]}`, adaptive},
		"no conversation":                        {`{"messages":[{"role":"system","content":"Be brief."}]}`, adaptive},
		"a legacy function call":                 {`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","function_call":{"name":"f","arguments":"{}"}},{"role":"function","name":"f","content":"1"}]}`, adaptive},
		"a tool result naming no call":           {`{"messages":[{"role":"user","content":"hi"},{"role":"tool","content":"1"}]}`, adaptive},
		"a tool that is not a function":          {`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"web_search"}]}`, adaptive},
		"a body that is not JSON":                {`not json`, adaptive},
	} {
		if _, _, err := chatAsMessages([]byte(tc.body), tc.m); !errors.Is(err, errUntranslated) {
			t.Errorf("%s: err = %v, want errUntranslated", what, err)
		}
	}
	// A PDF sent whole is a document the vendor reads.
	v := asMessages(t, `{"messages":[{"role":"user","content":[{"type":"file","file":{"filename":"a.pdf","file_data":"data:application/pdf;base64,JVBERg=="}},{"type":"text","text":"Summarise."}]}]}`, adaptive)
	same(t, "a document", v["messages"], `[{"role":"user","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERg=="}},{"type":"text","text":"Summarise."}]}]`)
}

// chatRead translates a Messages stream and returns the chat chunks it reads as,
// each decoded, and what the stream said it used when it ended.
func chatRead(t *testing.T, hide bool, events ...string) (frames []map[string]any, raw string, used AnthropicUsage, final bool, ends int) {
	t.Helper()
	var src strings.Builder
	for _, e := range events {
		if strings.HasPrefix(e, ":") {
			src.WriteString(e + "\n\n")
			continue
		}
		src.WriteString("event: x\ndata: " + e + "\n\n")
	}
	s := newClaudeChat(io.NopCloser(strings.NewReader(src.String())), "anthropic/claude-new", hide, func(u AnthropicUsage, f bool, _ int) {
		used, final = u, f
		ends++
	})
	b, err := io.ReadAll(s)
	if err != nil {
		t.Fatal(err)
	}
	raw = string(b)
	for _, line := range strings.Split(raw, "\n") {
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok || payload == "[DONE]" {
			continue
		}
		var f map[string]any
		if err := json.Unmarshal([]byte(payload), &f); err != nil {
			t.Fatalf("a frame is not JSON: %v: %s", err, payload)
		}
		frames = append(frames, f)
	}
	return
}

// delta is the delta of a chunk's one choice, or nil.
func delta(f map[string]any) map[string]any {
	choices, _ := f["choices"].([]any)
	if len(choices) == 0 {
		return nil
	}
	d, _ := choices[0].(map[string]any)["delta"].(map[string]any)
	return d
}

// Claude's stream reads as a chat completion's: the role, reasoning and text as
// they arrive, a tool call's name and then its arguments piece by piece, the
// finish reason, then one usage that counts the prompt whole with what the cache
// served and what was written to it inside it, and [DONE].
func TestClaudesStreamReadsAsAChat(t *testing.T) {
	events := []string{
		`{"type":"message_start","message":{"id":"msg_1","model":"claude-new","usage":{"input_tokens":10,"cache_creation_input_tokens":20,"cache_read_input_tokens":30,"output_tokens":1}}}`,
		`: keep-alive from the vendor`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me look."}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Looking"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":" closer."}}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"look","input":{}}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"zoom\""}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":":2}"}}`,
		`{"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"toolu_2","name":"wait","input":{}}}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":40}}`,
		`{"type":"message_stop"}`,
	}
	frames, raw, used, final, ends := chatRead(t, false, events...)
	if !strings.HasSuffix(raw, "data: [DONE]\n\n") || !strings.Contains(raw, ":\n\n") || strings.Contains(raw, "keep-alive from the vendor") {
		t.Errorf("the stream does not end in [DONE] or carried the vendor's own words:\n%s", raw)
	}
	if ends != 1 || !final || used.OutputTokens != 40 || used.CacheReadTokens != 30 {
		t.Errorf("the end was told %d times: final=%v used=%+v", ends, final, used)
	}
	var reasoning, text, args strings.Builder
	var names []string
	finish := ""
	for _, f := range frames {
		if f["object"] != "chat.completion.chunk" || f["model"] != "anthropic/claude-new" {
			t.Fatalf("a frame is not a chat chunk of the model asked for: %v", f)
		}
		d := delta(f)
		if s, ok := d["reasoning_content"].(string); ok {
			reasoning.WriteString(s)
		}
		if s, ok := d["content"].(string); ok {
			text.WriteString(s)
		}
		for _, tc := range asList(d["tool_calls"]) {
			call := tc.(map[string]any)
			fn := call["function"].(map[string]any)
			if name, ok := fn["name"].(string); ok {
				names = append(names, name+"@"+jsonOf(call["index"])+"="+call["id"].(string))
			}
			if call["index"].(float64) == 0 {
				args.WriteString(fn["arguments"].(string))
			}
		}
		if choices := asList(f["choices"]); len(choices) > 0 {
			if r, ok := choices[0].(map[string]any)["finish_reason"].(string); ok {
				finish = r
			}
		}
	}
	if reasoning.String() != "Let me look." || text.String() != "Looking closer." || args.String() != `{"zoom":2}` {
		t.Errorf("reasoning %q, text %q, arguments %q", reasoning.String(), text.String(), args.String())
	}
	if strings.Join(names, ",") != "look@0=toolu_1,wait@1=toolu_2" || finish != "tool_calls" {
		t.Errorf("tool calls %v finished %q, want two calls indexed in order and tool_calls", names, finish)
	}
	if d := delta(frames[0]); d["role"] != "assistant" {
		t.Errorf("the first chunk is %v, want the role", frames[0])
	}
	last := frames[len(frames)-1]
	same(t, "usage", last["usage"], `{"prompt_tokens":60,"completion_tokens":40,"total_tokens":100,"prompt_tokens_details":{"cached_tokens":30,"cache_write_tokens":20}}`)

	// The pipe reads the same split from it that it reads from any chat usage.
	var tk tokens
	b, _ := json.Marshal(last)
	sniffZenUsage(b, &tk)
	if tk.fresh != 10 || tk.cached != 30 || tk.written != 20 || tk.completion != 40 || !tk.reported {
		t.Errorf("the pipe read %+v from the usage", tk)
	}

	// Reasoning the caller asked not to see is not written out.
	hidden, _, _, _, _ := chatRead(t, true, events...)
	for _, f := range hidden {
		if _, ok := delta(f)["reasoning_content"]; ok {
			t.Fatal("reasoning was written out to a caller who excluded it")
		}
	}
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// The vendor's stop reasons read as a chat's finish reasons, and a stream that
// breaks off — an error event, or the connection — ends once, says what it was
// told of its usage, and is never dressed as an answer that finished.
func TestAStreamThatBreaksOffIsNotAnAnswerThatFinished(t *testing.T) {
	for stop, want := range map[string]string{"end_turn": "stop", "stop_sequence": "stop", "max_tokens": "length", "tool_use": "tool_calls", "refusal": "content_filter", "pause_turn": "stop"} {
		if got := chatFinish(stop); got != want {
			t.Errorf("chatFinish(%q) = %q, want %q", stop, got, want)
		}
	}
	open := []string{
		`{"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Half an ans"}}`,
	}
	frames, raw, used, final, ends := chatRead(t, false, append(open, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)...)
	if ends != 1 || final || used.InputTokens != 10 || strings.Contains(raw, "[DONE]") {
		t.Errorf("an error event: ends=%d final=%v used=%+v raw=%s", ends, final, used, raw)
	}
	if e, ok := frames[len(frames)-1]["error"].(map[string]any); !ok || e["message"] != "Overloaded" {
		t.Errorf("the last frame is %v, want the error", frames[len(frames)-1])
	}
	// A vendor's name in its error is not the caller's to read.
	frames, _, _, _, _ = chatRead(t, false, append(open, `{"type":"error","error":{"type":"api_error","message":"Anthropic is having a bad day"}}`)...)
	if e := frames[len(frames)-1]["error"].(map[string]any); strings.Contains(strings.ToLower(e["message"].(string)), "anthropic") {
		t.Errorf("the error names the vendor: %v", e)
	}
	_, raw, _, final, ends = chatRead(t, false, open...)
	if ends != 1 || final || strings.Contains(raw, "[DONE]") || strings.Contains(raw, "finish_reason\":\"stop") {
		t.Errorf("a cut stream: ends=%d final=%v raw=%s", ends, final, raw)
	}
}
