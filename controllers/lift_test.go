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
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/ai/object"
)

const (
	liftProse = "3. Starting with the receipts practical test now."
	liftArgs  = `{"cmd":"cd ~/Code/x && npm test -- tests/a.test.js","max_output_tokens":5000}`
	liftBlock = "<｜DSML｜tool_calls>\n" +
		"<｜DSML｜invoke name=\"exec_command\">\n" +
		"<｜DSML｜parameter name=\"cmd\" string=\"true\">cd ~/Code/x && npm test -- tests/a.test.js</｜DSML｜parameter>\n" +
		"<｜DSML｜parameter name=\"max_output_tokens\" number=\"true\">5000</｜DSML｜parameter>\n" +
		"</｜DSML｜invoke>\n" +
		"</｜DSML｜tool_calls>"
	liftSample = liftProse + "\n\n" + liftBlock

	liftTools = `"tools":[{"type":"function","function":{"name":"exec_command","parameters":{"type":"object"}}}]`
)

// liftHeard is what a chat client reads from an answer: the text, the calls by index,
// and how it finished.
type liftHeard struct {
	text   string
	names  map[int]string
	args   map[int]string
	ids    map[int]string
	finish string
}

func liftHear(t *testing.T, body string) liftHeard {
	t.Helper()
	h := liftHeard{names: map[int]string{}, args: map[int]string{}, ids: map[int]string{}}
	take := func(payload string) {
		var env struct {
			Choices []struct {
				Message *struct {
					Content   *string `json:"content"`
					ToolCalls []struct {
						ID       string `json:"id"`
						Index    *int   `json:"index"`
						Function struct{ Name, Arguments string }
					} `json:"tool_calls"`
				} `json:"message"`
				Delta *struct {
					Content   *string `json:"content"`
					ToolCalls []struct {
						ID       string `json:"id"`
						Index    *int   `json:"index"`
						Function struct{ Name, Arguments string }
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &env); err != nil {
			t.Fatalf("not an answer: %v\n%s", err, payload)
		}
		for _, ch := range env.Choices {
			if ch.FinishReason != "" {
				h.finish = ch.FinishReason
			}
			part := ch.Message
			if ch.Delta != nil {
				part = (*struct {
					Content   *string `json:"content"`
					ToolCalls []struct {
						ID       string `json:"id"`
						Index    *int   `json:"index"`
						Function struct{ Name, Arguments string }
					} `json:"tool_calls"`
				})(ch.Delta)
			}
			if part == nil {
				continue
			}
			if part.Content != nil {
				h.text += *part.Content
			}
			for i, c := range part.ToolCalls {
				if c.Index != nil {
					i = *c.Index
				}
				h.names[i] += c.Function.Name
				h.args[i] += c.Function.Arguments
				if c.ID != "" {
					h.ids[i] = c.ID
				}
			}
		}
	}
	if !strings.Contains(body, "data: ") {
		take(body)
		return h
	}
	for _, line := range strings.Split(body, "\n") {
		if payload, ok := strings.CutPrefix(line, "data: "); ok && strings.HasPrefix(payload, "{") {
			take(payload)
		}
	}
	return h
}

func (h liftHeard) wantCall(t *testing.T, text, name, args string) {
	t.Helper()
	if h.text != text || len(h.names) != 1 || h.names[0] != name || h.args[0] != args || h.ids[0] == "" || h.finish != "tool_calls" {
		t.Fatalf("liftHeard %+v, want text %q and one call %s(%s) finishing tool_calls", h, text, name, args)
	}
}

// completesWith is a buffered chat answer whose content is the text, finishing stop.
func completesWith(content string) func(http.ResponseWriter, int) {
	return completes(content, 10, 0, 5)
}

func TestLiftBufferedAnswerBecomesToolCall(t *testing.T) {
	w := newLiftWorld(t, completesWith(liftSample))
	c := w.chat(`{"model":"relay-sku",` + liftTools + `,"messages":[{"role":"user","content":"go"}]}`)
	out := sent(c)
	if answered(c) != http.StatusOK || strings.Contains(out, "DSML") {
		t.Fatalf("status %d, markup reached the client:\n%s", answered(c), out)
	}
	liftHear(t, out).wantCall(t, liftProse, "exec_command", liftArgs)
}

func TestLiftStreamedAnswerBecomesToolCall(t *testing.T) {
	// Byte by byte is the hardest split: every prefix of the marker is seen.
	var parts []string
	for _, r := range liftSample {
		parts = append(parts, string(r))
	}
	w := newLiftWorld(t, streams(parts, 10, 0, 5))
	c := w.chat(`{"model":"relay-sku","stream":true,` + liftTools + `,"messages":[{"role":"user","content":"go"}]}`)
	out := sent(c)
	if strings.Contains(out, "DSML") || strings.Contains(out, "tool_calls>") {
		t.Fatalf("markup reached the client:\n%s", out)
	}
	liftHear(t, out).wantCall(t, liftProse, "exec_command", liftArgs)
	if !strings.HasSuffix(strings.TrimSpace(out), "data: [DONE]") {
		t.Errorf("the stream did not end in [DONE]:\n%s", out)
	}
	// The call is billed as what the model wrote, not as text we did not relay.
	if got := w.paid(t); got.Model != relaySku {
		t.Errorf("billed as %q", got.Model)
	}
}

func TestLiftResponsesBecomeFunctionCall(t *testing.T) {
	respTools := `"tools":[{"type":"function","name":"exec_command","parameters":{"type":"object"}}]`
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			serve := completesWith(liftSample)
			if stream {
				serve = streams([]string{liftProse + "\n\n<｜DSML｜tool", "_calls>" + liftBlock[len("<｜DSML｜tool_calls>"):]}, 10, 0, 5)
			}
			w := newLiftWorld(t, serve)
			c := w.responses(fmt.Sprintf(`{"model":"relay-sku","stream":%v,%s,"input":"go"}`, stream, respTools))
			out := sent(c)
			if strings.Contains(out, "DSML") {
				t.Fatalf("markup reached the client:\n%s", out)
			}
			if !strings.Contains(out, `"function_call"`) || !strings.Contains(out, `"name":"exec_command"`) ||
				!strings.Contains(out, `max_output_tokens`) || !strings.Contains(out, liftProse) {
				t.Fatalf("want a function_call item and the prose:\n%s", out)
			}
			if !stream && !strings.Contains(out, `"output":[`) {
				t.Fatalf("not a Responses object:\n%s", out)
			}
		})
	}
}

func TestLiftLeavesWhatItWasNotAskedToLift(t *testing.T) {
	cases := map[string]string{
		"no tools offered": `{"model":"relay-sku","messages":[{"role":"user","content":"go"}]}`,
		"tool_choice none": `{"model":"relay-sku",` + liftTools + `,"tool_choice":"none","messages":[{"role":"user","content":"go"}]}`,
		"another tool":     `{"model":"relay-sku","tools":[{"type":"function","function":{"name":"read_file"}}],"messages":[{"role":"user","content":"go"}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			w := newLiftWorld(t, completesWith(liftSample))
			h := liftHear(t, sent(w.chat(body)))
			if h.text != liftSample || len(h.names) != 0 || h.finish != "stop" {
				t.Fatalf("liftHeard %+v, want the text untouched", h)
			}
		})
		t.Run(name+" streamed", func(t *testing.T) {
			w := newLiftWorld(t, streams([]string{liftSample[:len(liftProse)+3], liftSample[len(liftProse)+3:]}, 10, 0, 5))
			h := liftHear(t, sent(w.chat(strings.Replace(body, `{"model"`, `{"stream":true,"model"`, 1))))
			if h.text != liftSample || len(h.names) != 0 || h.finish != "stop" {
				t.Fatalf("liftHeard %+v, want the text untouched", h)
			}
		})
	}
}

// A stream that ends inside a block lets the held text go: nothing is lost, and
// nothing is called.
func TestLiftStreamCutInsideABlockReleasesTheText(t *testing.T) {
	cut := liftSample[:strings.LastIndex(liftSample, "</｜DSML｜tool_calls>")]
	w := newLiftWorld(t, streams([]string{cut}, 10, 0, 5))
	c := w.chat(`{"model":"relay-sku","stream":true,` + liftTools + `,"messages":[{"role":"user","content":"go"}]}`)
	h := liftHear(t, sent(c))
	if h.text != cut || len(h.names) != 0 || h.finish != "stop" {
		t.Fatalf("liftHeard %+v, want the cut text released", h)
	}
}

// A choice the upstream gave structured calls is not read for more: its text stays
// text, so one call is never made twice.
func TestLiftKeepsNativeCallsAndLiftsNothingBesideThem(t *testing.T) {
	l := liftFor([]byte(`{`+liftTools+`}`), liftModel, false)
	in := `{"choices":[{"index":0,"message":{"role":"assistant","content":` + liftQuote(liftSample) +
		`,"tool_calls":[{"id":"call_n","type":"function","function":{"name":"exec_command","arguments":"{}"}}]},"finish_reason":"stop"}]}`
	h := liftHear(t, string(l.payload([]byte(in))))
	if len(h.names) != 1 || h.names[0] != "exec_command" || h.args[0] != "{}" || h.ids[0] != "call_n" || h.text != liftSample {
		t.Fatalf("liftHeard %+v", h)
	}
}

// liftModel is the upstream model id the lift tests route to: a family whose markup
// the sample is written in.
const liftModel = "deepseek/deepseek-v4"

// newLiftWorld is a relay world whose SKU routes to the upstream model liftModel.
func newLiftWorld(t *testing.T, serve func(http.ResponseWriter, int)) *relayWorld {
	return newLiftWorldFor(t, liftModel, serve)
}

func newLiftWorldFor(t *testing.T, upstream string, serve func(http.ResponseWriter, int)) *relayWorld {
	t.Helper()
	w := newRelayWorld(t, serve, never(t))
	route, err := object.GetModelRoute("built-in", relaySku)
	if err != nil || route == nil {
		t.Fatalf("route: %v", err)
	}
	route.Upstream = upstream
	if _, err := object.UpdateModelRoute("built-in", relaySku, route); err != nil {
		t.Fatal(err)
	}
	return w
}

func liftQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

// The upstream stream a translator reads (Anthropic /v1/messages over a chat
// upstream), split at every byte of every line boundary, lifts the same.
func TestLiftStreamReaderEverySplit(t *testing.T) {
	chunks := []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
		`{"choices":[{"index":0,"delta":{"content":` + liftQuote(liftProse+"\n\n<｜DSML｜tool_") + `}}]}`,
		`{"choices":[{"index":0,"delta":{"content":` + liftQuote(liftBlock[len("<｜DSML｜tool_"):]) + `}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2}}`,
	}
	var sse strings.Builder
	for _, c := range chunks {
		sse.WriteString("data: " + c + "\n\n")
	}
	sse.WriteString("data: [DONE]\n\n")
	for cut := 0; cut <= sse.Len(); cut++ {
		l := liftFor([]byte(`{`+liftTools+`}`), liftModel, false)
		r := l.stream(io.MultiReader(strings.NewReader(sse.String()[:cut]), strings.NewReader(sse.String()[cut:])))
		out, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(out, []byte("DSML")) {
			t.Fatalf("cut %d: markup in %s", cut, out)
		}
		liftHear(t, string(out)).wantCall(t, liftProse, "exec_command", liftArgs)
	}
}

func TestLiftAnthropicBufferedAnswer(t *testing.T) {
	body := `{"choices":[{"index":0,"message":{"role":"assistant","content":` + liftQuote(liftSample) + `},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4}}`
	l := liftFor([]byte(`{`+liftTools+`}`), liftModel, false)
	resp, _, _ := openAIResponseToAnthropic(l.payload([]byte(body)), "m", "m", "r")
	out, _ := json.Marshal(resp)
	if strings.Contains(string(out), "DSML") || resp["stop_reason"] != "tool_use" ||
		!strings.Contains(string(out), `"type":"tool_use"`) || !strings.Contains(string(out), `"name":"exec_command"`) {
		t.Fatalf("not a tool_use answer: %s", out)
	}
}

// Direction in: a history of assistant tool calls and their results goes upstream as
// structured tool_calls and tool messages, from every dialect that carries one.
func TestToolHistoryGoesUpstreamStructured(t *testing.T) {
	w := newLiftWorld(t, completesWith("done"))
	w.chat(`{"model":"relay-sku",` + liftTools + `,"messages":[{"role":"user","content":"go"},` +
		`{"role":"assistant","content":"running","tool_calls":[{"id":"call_1","type":"function","function":{"name":"exec_command","arguments":"{\"cmd\":\"ls\"}"}}]},` +
		`{"role":"tool","tool_call_id":"call_1","content":"a b"}]}`)
	sentBody := string(w.a.sent(t, 0))
	for _, want := range []string{`"tool_calls":[{"id":"call_1"`, `"role":"tool"`, `"tool_call_id":"call_1"`} {
		if !strings.Contains(sentBody, want) {
			t.Errorf("upstream was sent %s, want %s", sentBody, want)
		}
	}
	if strings.Contains(sentBody, "DSML") {
		t.Errorf("markup went upstream: %s", sentBody)
	}

	msgs, err := responsesInputToMessages("", json.RawMessage(`[{"type":"message","role":"user","content":"go"},`+
		`{"type":"function_call","call_id":"call_1","name":"exec_command","arguments":"{\"cmd\":\"ls\"}"},`+
		`{"type":"function_call_output","call_id":"call_1","output":"a b"}]`))
	if err != nil || len(msgs) != 3 || msgs[1].Role != "assistant" || len(msgs[1].ToolCalls) != 1 ||
		msgs[1].ToolCalls[0].ID != "call_1" || msgs[2].Role != "tool" || msgs[2].ToolCallID != "call_1" {
		t.Fatalf("responses history: %v %+v", err, msgs)
	}
}

// The sample split in two at every rune boundary, and in one-rune chunks, through
// the mark every relay stamps with: the same call and text each time, and no marker
// in anything the client is sent.
func TestLiftEverySplitThroughTheMark(t *testing.T) {
	chunk := func(content, finish string) []byte {
		fr := "null"
		if finish != "" {
			fr = liftQuote(finish)
		}
		return []byte(`{"id":"up","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":` +
			liftQuote(content) + `},"finish_reason":` + fr + `}]}`)
	}
	run := func(parts []string) {
		mk := &mark{id: "chatcmpl-x", model: "sku", lift: liftFor([]byte(`{`+liftTools+`}`), liftModel, false)}
		var wire strings.Builder
		for _, p := range parts {
			wire.WriteString("data: " + string(mk.stamp(chunk(p, ""))) + "\n\n")
		}
		wire.WriteString("data: " + string(mk.stamp(chunk("", "stop"))) + "\n\n")
		if rest := mk.rest(); rest != nil {
			t.Fatalf("held after the finish: %s", rest)
		}
		if strings.Contains(wire.String(), "DSML") {
			t.Fatalf("parts %q: markup reached the client:\n%s", parts, wire.String())
		}
		liftHear(t, wire.String()).wantCall(t, liftProse, "exec_command", liftArgs)
	}
	var runes []string
	for i, r := range liftSample {
		runes = append(runes, string(r))
		run([]string{liftSample[:i], liftSample[i:]})
	}
	run(runes)
}

// A stream that ends with no finish chunk lets the held text go through rest.
func TestLiftRestReleasesWhatIsHeld(t *testing.T) {
	mk := &mark{id: "chatcmpl-x", model: "sku", lift: liftFor([]byte(`{`+liftTools+`}`), liftModel, false)}
	out := mk.stamp([]byte(`{"choices":[{"index":0,"delta":{"content":"so far <｜DS"}}]}`))
	if strings.Contains(string(out), "DS") {
		t.Fatalf("a possible marker went out: %s", out)
	}
	rest := mk.rest()
	if h := liftHear(t, string(rest)); h.text != " <｜DS" && h.text != "<｜DS" {
		t.Fatalf("rest = %s", rest)
	}
	if mk.rest() != nil {
		t.Fatal("rest released twice")
	}
}
