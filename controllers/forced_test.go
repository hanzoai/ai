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

// A tool choice reaches each upstream family in that family's own words, and a
// forced one (required, a named tool) reaches it forced: never dropped, never
// sent on as one that forces nothing. Each test builds the body the upstream is
// sent for the six choices an agent harness writes.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanzoai/go-openai"
)

// weather is the one tool every case offers, in chat's words.
const weather = `[{"type":"function","function":{"name":"get_weather","description":"Get the weather for a city","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}]`

// strictWeather is the same tool held to its schema.
const strictWeather = `[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false},"strict":true}}]`

// reread is v as JSON reads it back, so two encodings of one value compare equal
// whatever order each wrote its keys in.
func reread(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

// chatWith is a chat body offering tools, with extra top-level fields.
func chatWith(tools, extra string) string {
	if extra != "" {
		extra = "," + extra
	}
	return `{"model":"enso-ultra","max_tokens":300,"reasoning_effort":"high","messages":[{"role":"user","content":"What is the weather in Paris?"}],"tools":` + tools + extra + `}`
}

// Anthropic's Messages API: required is any, a named function is tool, auto and
// none are themselves, parallel_tool_calls false rides the choice as
// disable_parallel_tool_use, strict rides the tool where the model takes a schema.
// A forced choice is never sent beside thinking, which the vendor refuses.
func TestAForcedChoiceReachesMessagesForced(t *testing.T) {
	for _, tc := range []struct{ what, extra, want string }{
		{"required", `"tool_choice":"required"`, `{"type":"any"}`},
		{"named", `"tool_choice":{"type":"function","function":{"name":"get_weather"}}`, `{"type":"tool","name":"get_weather"}`},
		{"auto", `"tool_choice":"auto"`, `{"type":"auto"}`},
		{"none", `"tool_choice":"none"`, `{"type":"none"}`},
		{"required, one call at a time", `"tool_choice":"required","parallel_tool_calls":false`, `{"type":"any","disable_parallel_tool_use":true}`},
		{"named, one call at a time", `"tool_choice":{"type":"function","function":{"name":"get_weather"}},"parallel_tool_calls":false`, `{"type":"tool","name":"get_weather","disable_parallel_tool_use":true}`},
		{"one call at a time alone", `"parallel_tool_calls":false`, `{"type":"auto","disable_parallel_tool_use":true}`},
	} {
		for _, m := range []claude{adaptive, budgeted} {
			v := asMessages(t, chatWith(weather, tc.extra), m)
			same(t, tc.what+" ("+m.ID+")", v["tool_choice"], tc.want)
			forced := strings.Contains(tc.want, `"any"`) || strings.Contains(tc.want, `"tool"`)
			if _, thinks := v["thinking"]; forced && thinks {
				t.Errorf("%s (%s): thinking was asked beside a forced tool: %v", tc.what, m.ID, v["thinking"])
			}
			if _, sent := v["parallel_tool_calls"]; sent {
				t.Errorf("%s (%s): parallel_tool_calls was sent in chat's words", tc.what, m.ID)
			}
		}
	}
	// A choice of none with one call at a time is none: the flag means nothing there.
	v := asMessages(t, chatWith(weather, `"tool_choice":"none","parallel_tool_calls":false`), adaptive)
	same(t, "none, one call at a time", v["tool_choice"], `{"type":"none"}`)

	// strict is the tool's, sent only to a model whose listing takes a schema.
	held := asMessages(t, chatWith(strictWeather, `"tool_choice":"required"`), adaptive)
	same(t, "strict, schema model", held["tools"].([]any)[0].(map[string]any)["strict"], `true`)
	same(t, "strict, forced", held["tool_choice"], `{"type":"any"}`)
	loose := asMessages(t, chatWith(strictWeather, `"tool_choice":"required"`), budgeted)
	if _, sent := loose["tools"].([]any)[0].(map[string]any)["strict"]; sent {
		t.Error("strict was sent to a model whose listing takes no schema")
	}
	same(t, "strict dropped, still forced", loose["tool_choice"], `{"type":"any"}`)
}

// A family (Enso, Zen) and a chat maker (OpenAI) speak chat: every tool field goes
// as the caller wrote it. This pins the Enso path: a forced choice reaches the
// engine forced, and the engine decides which rung can take it.
func TestAForcedChoiceReachesAChatUpstreamAsWritten(t *testing.T) {
	for _, extra := range []string{
		`"tool_choice":"required"`,
		`"tool_choice":{"type":"function","function":{"name":"get_weather"}}`,
		`"tool_choice":"auto"`,
		`"tool_choice":"none"`,
		`"tool_choice":"required","parallel_tool_calls":false`,
	} {
		for _, tools := range []string{weather, strictWeather} {
			body := chatWith(tools, extra)
			var in map[string]json.RawMessage
			if err := json.Unmarshal([]byte(body), &in); err != nil {
				t.Fatal(err)
			}
			family := decoded(t, familyBody([]byte(body), "chat/completions", 300))
			out, _, err := chatBody(nativeCall{path: "chat/completions", body: []byte(body), stream: true}, claude{ID: "gpt-6.1-sol"})
			if err != nil {
				t.Fatalf("%s: the maker refused to send it: %v", extra, err)
			}
			var maker map[string]json.RawMessage
			if err := json.Unmarshal(out, &maker); err != nil {
				t.Fatal(err)
			}
			for _, k := range []string{"tool_choice", "parallel_tool_calls", "tools"} {
				want, asked := in[k]
				for name, sent := range map[string]map[string]json.RawMessage{"family": family, "maker": maker} {
					got, ok := sent[k]
					if ok != asked {
						t.Errorf("%s: %s sent %s=%v, asked %v", extra, name, k, ok, asked)
						continue
					}
					if asked {
						same(t, extra+": "+name+" "+k, reread(json.RawMessage(got)), string(want))
					}
				}
			}
		}
	}
}

// responsesChat is the chat request a Responses body becomes.
func responsesChat(t *testing.T, body string) (*openai.ChatCompletionRequest, error) {
	t.Helper()
	var req OpenAIResponsesRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	chat, _, err := responsesToChatRequest(&req)
	return chat, err
}

// The Responses API names a function flat ({"type":"function","name":...}); chat
// nests it. Every shape a Responses caller forces with reaches chat forced: a named
// function or custom tool, one inside a namespace by its flat name, and
// allowed_tools as its mode over only the tools it allows.
func TestAResponsesChoiceReachesChatForced(t *testing.T) {
	const tools = `"tools":[
	  {"type":"function","name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}},"strict":true},
	  {"type":"function","name":"get_time"},
	  {"type":"custom","name":"apply_patch"},
	  {"type":"namespace","name":"mcp__maps","tools":[{"type":"function","name":"route"}]},
	  {"type":"web_search"}]`
	ask := func(choice string) string {
		return `{"model":"enso-ultra","input":"What is the weather in Paris?","parallel_tool_calls":false,` + tools + `,"tool_choice":` + choice + `}`
	}
	for _, tc := range []struct{ what, choice, want string }{
		{"required", `"required"`, `"required"`},
		{"auto", `"auto"`, `"auto"`},
		{"none", `"none"`, `"none"`},
		{"named", `{"type":"function","name":"get_weather"}`, `{"type":"function","function":{"name":"get_weather"}}`},
		{"custom", `{"type":"custom","name":"apply_patch"}`, `{"type":"function","function":{"name":"apply_patch"}}`},
		{"namespaced", `{"type":"function","name":"route","namespace":"mcp__maps"}`, `{"type":"function","function":{"name":"mcp__maps__route"}}`},
		{"namespaced, by its own name", `{"type":"function","name":"route"}`, `{"type":"function","function":{"name":"mcp__maps__route"}}`},
		{"allowed, required", `{"type":"allowed_tools","mode":"required","tools":[{"type":"function","name":"get_time"}]}`, `"required"`},
	} {
		chat, err := responsesChat(t, ask(tc.choice))
		if err != nil {
			t.Fatalf("%s: %v", tc.what, err)
		}
		same(t, tc.what, reread(chat.ToolChoice), tc.want)
		if p, ok := chat.ParallelToolCalls.(bool); !ok || p {
			t.Errorf("%s: parallel_tool_calls = %#v, want false", tc.what, chat.ParallelToolCalls)
		}
		wire, _ := json.Marshal(chat)
		if !strings.Contains(string(wire), `"parallel_tool_calls":false`) {
			t.Errorf("%s: parallel_tool_calls false did not reach the wire: %s", tc.what, wire)
		}
	}

	// strict is the function's own, carried to chat's function.
	chat, err := responsesChat(t, ask(`"required"`))
	if err != nil {
		t.Fatal(err)
	}
	if !chat.Tools[0].Function.Strict || chat.Tools[1].Function.Strict {
		t.Errorf("strict = %v, %v; want true on get_weather only", chat.Tools[0].Function.Strict, chat.Tools[1].Function.Strict)
	}

	// allowed_tools keeps only what it allows, under its mode.
	chat, err = responsesChat(t, ask(`{"type":"allowed_tools","mode":"auto","tools":[{"type":"function","name":"get_weather"},{"type":"custom","name":"apply_patch"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	same(t, "allowed, auto", reread(chat.ToolChoice), `"auto"`)
	var kept []string
	for _, tl := range chat.Tools {
		kept = append(kept, tl.Function.Name)
	}
	if strings.Join(kept, ",") != "get_weather,apply_patch" {
		t.Errorf("allowed_tools kept %v", kept)
	}

	// allowed_tools in auto mode passes over a tool chat has no function for; when
	// nothing it allows remains, the model is offered no tool and no choice.
	chat, err = responsesChat(t, ask(`{"type":"allowed_tools","mode":"auto","tools":[{"type":"web_search"},{"type":"function","name":"get_time"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	same(t, "allowed auto, hosted passed over", reread(chat.ToolChoice), `"auto"`)
	if len(chat.Tools) != 1 || chat.Tools[0].Function.Name != "get_time" {
		t.Errorf("allowed auto kept %d tools", len(chat.Tools))
	}
	chat, err = responsesChat(t, ask(`{"type":"allowed_tools","mode":"auto","tools":[{"type":"web_search"}]}`))
	if err != nil || chat.ToolChoice != nil || len(chat.Tools) != 0 || chat.ParallelToolCalls != nil {
		t.Errorf("allowed auto, only hosted: choice %#v, %d tools, parallel %#v (%v)", chat.ToolChoice, len(chat.Tools), chat.ParallelToolCalls, err)
	}
	// No tools, no parallel_tool_calls: a chat upstream refuses it alone.
	chat, err = responsesChat(t, `{"model":"m","input":"x","parallel_tool_calls":false}`)
	if err != nil || chat.ParallelToolCalls != nil {
		t.Errorf("parallel_tool_calls was sent with no tools: %#v (%v)", chat.ParallelToolCalls, err)
	}

	// What chat has no function for is refused, never sent on as a choice that
	// forces nothing.
	for what, choice := range map[string]string{
		"a hosted tool":      `{"type":"web_search"}`,
		"an mcp server":      `{"type":"mcp","server_label":"maps","name":"route"}`,
		"an undeclared name": `{"type":"function","name":"get_stock"}`,
		"a wrong namespace":  `{"type":"function","name":"route","namespace":"mcp__other"}`,
		"an unknown word":    `"any"`,
		"no allowed tool":    `{"type":"allowed_tools","mode":"required","tools":[]}`,
		"an unknown mode":    `{"type":"allowed_tools","mode":"some","tools":[{"type":"function","name":"get_time"}]}`,
		"a required hosted":  `{"type":"allowed_tools","mode":"required","tools":[{"type":"web_search"}]}`,
	} {
		if _, err := responsesChat(t, ask(choice)); err == nil {
			t.Errorf("%s: %s was translated", what, choice)
		}
	}
	// No choice is no choice.
	chat, err = responsesChat(t, `{"model":"m","input":"x",`+tools+`}`)
	if err != nil || chat.ToolChoice != nil {
		t.Errorf("no choice became %#v (%v)", chat.ToolChoice, err)
	}
}

// Anthropic's choices cross to chat as OpenAI spells them, and
// disable_parallel_tool_use is chat's parallel_tool_calls: false.
func TestAnAnthropicChoiceReachesChatForced(t *testing.T) {
	for raw, want := range map[string]string{
		`{"type":"auto"}`:                      `"auto"`,
		`{"type":"none"}`:                      `"none"`,
		`{"type":"any"}`:                       `"required"`,
		`{"type":"tool","name":"get_weather"}`: `{"type":"function","function":{"name":"get_weather"}}`,
	} {
		same(t, raw, reread(anthropicToolChoiceToOpenAI(json.RawMessage(raw))), want)
	}
	for raw, serial := range map[string]bool{
		`{"type":"any","disable_parallel_tool_use":true}`:                       true,
		`{"type":"tool","name":"get_weather","disable_parallel_tool_use":true}`: true,
		`{"type":"auto","disable_parallel_tool_use":false}`:                     false,
		`{"type":"auto"}`: false,
		``:                false,
	} {
		if got := anthropicSerial(json.RawMessage(raw)); got != serial {
			t.Errorf("%s: serial = %v, want %v", raw, got, serial)
		}
	}
}

// The arm that takes a forced call where Sonnet 5.5 cannot, Claude Sonnet 5 on
// Anthropic's own key (universe enso catalog), costs no more than enso-pro and
// enso-ultra bill in any class; Opus 5, its other candidate, reads the cache above
// enso-ultra's price.
func TestTheForcedCallArmFitsUnderEnsoPrices(t *testing.T) {
	orFees(t)
	ensoServiceAt(t)
	rs, err := rungsOf([]byte(`{"providers":{"anthropic":{"base":"https://api.anthropic.com/v1","funding":"prepaid"}},"models":{
	  "sonnet":{"route":[{"provider":"anthropic","upstream":"claude-sonnet-5","ctx":1000000,"cost":{"in":"2.00","out":"10.00","cache_read":"0.20","cache_write":"2.50"}}]},
	  "opus":{"route":[{"provider":"anthropic","upstream":"claude-opus-5","ctx":1000000,"cost":{"in":"5.00","out":"25.00","cache_read":"0.50","cache_write":"6.25"}}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"enso-pro", "enso-ultra"} {
		m, ok := ensoFam.lookup(id)
		if !ok {
			t.Fatalf("%s not listed", id)
		}
		if why := misfit(m, rs["sonnet"]); why != "" {
			t.Errorf("%s: %s", id, why)
		}
	}
	ultra, _ := ensoFam.lookup("enso-ultra")
	if why := misfit(ultra, rs["opus"]); !strings.Contains(why, "cache") {
		t.Errorf("Opus 5 under enso-ultra: %q, want it over in the cache", why)
	}
}
