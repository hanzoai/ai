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
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// A route whose chat template writes the <think> opener itself says so
// (opener_eaten), and its content is `reasoning</think>answer`. Everything up to the
// first </think> is reasoning, on every surface; the opener may or may not be there.

// eatenRoute declares the think SKU's route (relay-a, deepseek-r1) as one that eats
// the opener, the way models.yaml does.
func eatenRoute(t *testing.T) {
	t.Helper()
	if globalModelConfig == nil {
		globalModelConfig = &ModelConfig{routes: map[string]modelRoute{}, pricing: map[string]modelPrice{}, aliases: map[string]string{}}
		t.Cleanup(func() { globalModelConfig = nil })
	}
	mc := globalModelConfig
	mc.mu.Lock()
	mc.routes["eaten-test"] = modelRoute{providerName: "relay-a", upstreamModel: "deepseek-r1", openerEaten: true}
	mc.mu.Unlock()
	t.Cleanup(func() {
		mc.mu.Lock()
		delete(mc.routes, "eaten-test")
		mc.mu.Unlock()
	})
}

func eatenWorld(t *testing.T, frames ...string) *relayWorld {
	t.Helper()
	w := thinkWorld(t, frames...)
	eatenRoute(t)
	return w
}

// the forms a flagged route's content takes, and what the caller must read.
var eatenForms = map[string]string{
	"no opener":   "The user wants X. I should be brief.</think>\n\nHello!",
	"with opener": "<think>The user wants X. I should be brief.</think>\n\nHello!",
}

func TestOpenerEatenParsesFromModelsYAML(t *testing.T) {
	var file ModelConfigFile
	doc := `
models:
  deepseek-v4-pro: {provider: do-ai, upstream: deepseek-v4-pro, opener_eaten: true}
  deepseek-v4-flash: {provider: do-ai, upstream: deepseek-4-flash, opener_eaten: true}
  deepseek-3.2: {provider: do-ai, upstream: deepseek-3.2}
  fb:
    provider: x
    upstream: y
    fallbacks: [{provider: do-ai, upstream: deepseek-r1-distill-llama-70b, opener_eaten: true}]
`
	if err := yaml.Unmarshal([]byte(doc), &file); err != nil {
		t.Fatal(err)
	}
	mc := &ModelConfig{routes: map[string]modelRoute{}, pricing: map[string]modelPrice{}, aliases: map[string]string{}}
	if err := mc.applyConfig(&file); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		provider, upstream string
		want               bool
	}{
		{"do-ai", "deepseek-v4-pro", true},
		{"do-ai", "DeepSeek-4-Flash", true},
		{"do-ai", "deepseek-r1-distill-llama-70b", true},
		{"", "deepseek-v4-pro", true},
		{"fireworks", "deepseek-v4-pro", false},
		{"do-ai", "deepseek-3.2", false},
		{"do-ai", "", false},
	} {
		if got := mc.openerEaten(c.provider, c.upstream); got != c.want {
			t.Errorf("openerEaten(%q, %q) = %v, want %v", c.provider, c.upstream, got, c.want)
		}
	}
	var none *ModelConfig
	if none.openerEaten("do-ai", "deepseek-v4-pro") {
		t.Error("no config says nothing")
	}
}

func TestEatenRouteChatStream(t *testing.T) {
	for name, whole := range eatenForms {
		for cut := 0; cut <= len(whole); cut++ {
			w := eatenWorld(t, answerFrames(false, whole[:cut], whole[cut:])...)
			out := sent(w.chat(`{"model":"` + thinkSku + `","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
			if h := liftHear(t, out); h.text != "Hello!" || strings.Contains(out, "</think>") || strings.Contains(out, "brief") {
				t.Fatalf("%s split at %d: text %q:\n%s", name, cut, h.text, out)
			}
		}
	}
}

func TestEatenRouteChatBuffered(t *testing.T) {
	for name, whole := range eatenForms {
		w := newRelayWorld(t, completes(whole, 5, 0, 3), never(t))
		thinkRoute(t)
		eatenRoute(t)
		out := sent(w.chat(`{"model":"` + thinkSku + `","messages":[{"role":"user","content":"hi"}]}`))
		if h := liftHear(t, out); h.text != "Hello!" || strings.Contains(out, "brief") {
			t.Errorf("%s: text %q:\n%s", name, h.text, out)
		}
	}
}

func TestEatenRouteResponsesStream(t *testing.T) {
	for name, whole := range eatenForms {
		for cut := 0; cut <= len(whole); cut++ {
			w := eatenWorld(t, answerFrames(false, whole[:cut], whole[cut:])...)
			out := sent(w.responses(`{"model":"` + thinkSku + `","stream":true,"input":"hi"}`))
			var deltas []string
			for _, e := range chunks(t, out) {
				if string(e["type"]) == `"response.output_text.delta"` {
					deltas = append(deltas, field(t, e, "delta"))
				}
			}
			if got := strings.Join(deltas, ""); got != "Hello!" || strings.Contains(out, "brief") {
				t.Fatalf("%s split at %d: deltas %q:\n%s", name, cut, got, out)
			}
		}
	}
}

func TestEatenRouteMessagesStream(t *testing.T) {
	for name, whole := range eatenForms {
		for cut := 0; cut <= len(whole); cut++ {
			if got := anthropicText(t, eatenWorld(t, answerFrames(false, whole[:cut], whole[cut:])...)); got != "Hello!" {
				t.Fatalf("%s split at %d: text %q", name, cut, got)
			}
		}
	}
}

func TestEatenRouteMessagesBuffered(t *testing.T) {
	for name, whole := range eatenForms {
		w := newRelayWorld(t, completes(whole, 5, 0, 3), never(t))
		thinkRoute(t)
		eatenRoute(t)
		c := as(visit("POST", "/v1/messages"), w.cred)
		c.Fiber().Request().SetBody([]byte(`{"model":"` + thinkSku + `","max_tokens":256,` + anthropicTool + `,"messages":[{"role":"user","content":"hi"}]}`))
		c.AnthropicMessages()
		if out := sent(c); !strings.Contains(out, `"text":"Hello!"`) || strings.Contains(out, "brief") {
			t.Errorf("%s: %s", name, out)
		}
	}
}

// An answer with no </think> on a flagged route is the answer: delivered whole, at
// every split, on every surface — held until the stream ends, never dropped.
func TestEatenRouteAnswerWithNoCloseIsDeliveredWhole(t *testing.T) {
	const answer = "Hello, this is the whole answer."
	for cut := 0; cut <= len(answer); cut++ {
		frames := answerFrames(false, answer[:cut], answer[cut:])
		w := eatenWorld(t, frames...)
		if h := liftHear(t, sent(w.chat(`{"model":"`+thinkSku+`","stream":true,"messages":[{"role":"user","content":"hi"}]}`))); h.text != answer || h.finish != "stop" {
			t.Fatalf("chat split at %d: text %q finish %q", cut, h.text, h.finish)
		}
		if got := anthropicText(t, eatenWorld(t, frames...)); got != answer {
			t.Fatalf("messages split at %d: text %q", cut, got)
		}
	}
	// the stream ends with no finish_reason and no [DONE]
	for _, tail := range []string{"[DONE]", ""} {
		frames := []string{chunkFrame(`{"role":"assistant","content":"Hello, no ending"}`, "")}
		if tail != "" {
			frames = append(frames, tail)
		}
		w := newRelayWorld(t, sseOf(frames...), never(t))
		thinkRoute(t)
		eatenRoute(t)
		if h := liftHear(t, sent(w.chat(`{"model":"`+thinkSku+`","stream":true,"messages":[{"role":"user","content":"hi"}]}`))); h.text != "Hello, no ending" {
			t.Fatalf("tail %q: text %q, want the held bytes", tail, h.text)
		}
	}
	w := newRelayWorld(t, completes(answer, 5, 0, 3), never(t))
	thinkRoute(t)
	eatenRoute(t)
	if h := liftHear(t, sent(w.chat(`{"model":"`+thinkSku+`","messages":[{"role":"user","content":"hi"}]}`))); h.text != answer {
		t.Fatalf("buffered: text %q", h.text)
	}
}

// reasoning_content still says content is the answer, on a flagged route too.
func TestEatenRouteReasoningContentDisablesIt(t *testing.T) {
	w := eatenWorld(t, answerFrames(true, "Hello ", "world, a </think> in the answer.")...)
	if h := liftHear(t, sent(w.chat(`{"model":"`+thinkSku+`","stream":true,"messages":[{"role":"user","content":"hi"}]}`))); h.text != "Hello world, a </think> in the answer." {
		t.Fatalf("text %q", h.text)
	}
}

// An unflagged route keeps the strict rule: only a leading <think> block is
// reasoning, and a bare </think> is text.
func TestUnflaggedRouteKeepsTheStrictOpener(t *testing.T) {
	w := thinkWorld(t, answerFrames(false, "Reasoning aloud.</think>", "The answer.")...)
	if h := liftHear(t, sent(w.chat(`{"model":"`+thinkSku+`","stream":true,"messages":[{"role":"user","content":"hi"}]}`))); h.text != "Reasoning aloud.</think>The answer." {
		t.Fatalf("text %q", h.text)
	}
}

// What /v1/messages lifts is read after the reasoning is stripped, as the chat path
// reads it: a fence the reasoning opened and never closed does not make the answer's
// own fenced block a call.
func TestMessagesStripsBeforeItLifts(t *testing.T) {
	answer := "<think>Sketch:\n```\nfoo\n</think>Shown as code:\n```\n" + liftBlock
	for cut := 0; cut <= len(answer); cut += 7 {
		for !utf8.RuneStart(answer[min(cut, len(answer)-1)]) {
			cut++
		}
		frames := []string{chunkFrame(`{"role":"assistant","content":""}`, ""),
			chunkFrame(`{"content":`+mustJSON(answer[:cut])+`}`, ""),
			chunkFrame(`{"content":`+mustJSON(answer[cut:])+`}`, ""), chunkFrame(`{}`, "stop")}
		w := thinkWorld(t, frames...)
		c := as(visit("POST", "/v1/messages"), w.cred)
		c.Fiber().Request().SetBody([]byte(`{"model":"` + thinkSku + `","max_tokens":256,"stream":true,` + anthropicTool + `,"messages":[{"role":"user","content":"hi"}]}`))
		c.AnthropicMessages()
		out := sent(c)
		if strings.Contains(out, "tool_use") || !strings.Contains(out, `"stop_reason":"end_turn"`) {
			t.Fatalf("split at %d: a fenced block was lifted:\n%s", cut, out)
		}
	}
	w := newRelayWorld(t, completes(answer, 5, 0, 3), never(t))
	thinkRoute(t)
	c := as(visit("POST", "/v1/messages"), w.cred)
	c.Fiber().Request().SetBody([]byte(`{"model":"` + thinkSku + `","max_tokens":256,` + anthropicTool + `,"messages":[{"role":"user","content":"hi"}]}`))
	c.AnthropicMessages()
	if out := sent(c); strings.Contains(out, "tool_use") || !strings.Contains(out, `"stop_reason":"end_turn"`) {
		t.Fatalf("buffered: a fenced block was lifted:\n%s", out)
	}
}

// A cut upstream is not a finished turn: the Anthropic stream ends in an error
// event, never message_stop with end_turn.
func TestMessagesStreamCutIsAnErrorNotEndTurn(t *testing.T) {
	var events []sseEvent
	emit := func(event string, data any) error {
		b, _ := json.Marshal(data)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		events = append(events, sseEvent{Event: event, Data: m})
		return nil
	}
	cut := `data: {"choices":[{"delta":{"content":"half an ans"},"finish_reason":null}]}` + "\n"
	translateOpenAIStream(strings.NewReader(cut), emit, "m", "r")
	last := events[len(events)-1]
	if last.Event != "error" {
		t.Fatalf("a cut stream ended with %q, want an error event: %+v", last.Event, events)
	}
	for _, e := range events {
		if e.Event == "message_stop" || e.Event == "message_delta" {
			t.Fatalf("a cut stream carries %s: %+v", e.Event, events)
		}
	}
	// A finished one is unchanged.
	events = nil
	done := cut + `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n" + "data: [DONE]\n"
	translateOpenAIStream(strings.NewReader(done), emit, "m", "r")
	if events[len(events)-1].Event != "message_stop" {
		t.Fatalf("a finished stream ended with %q", events[len(events)-1].Event)
	}
}

// A strict request over the ZAP gateway is refused for a vendor that would have it
// translated, as the HTTP twin refuses it; the version and betas a frame carries
// reach a native vendor as the HTTP request's do.
func TestZapGatewayStrictMessages(t *testing.T) {
	w := thinkWorld(t, answerFrames(false, "ok")...)
	body := `{"model":"` + thinkSku + `","max_tokens":256,` + anthropicTool + `,"messages":[{"role":"user","content":"hi"}]}`
	for _, c := range []struct {
		headers map[string]string
		strict  bool
	}{
		{map[string]string{"Authorization": w.cred, "X-Hanzo-Strict": "1"}, true},
		{map[string]string{"Authorization": w.cred}, false},
	} {
		msg, err := gateway(nil)(context.Background(), "", gatewayCall(t, "/v1/messages", c.headers, body))
		if err != nil {
			t.Fatal(err)
		}
		status, out := antDecodeGateway(t, msg)
		if c.strict && (status != http.StatusConflict || !strings.Contains(string(out), "strict: translation")) {
			t.Errorf("strict: status %d: %s", status, out)
		}
		if !c.strict && status == http.StatusConflict {
			t.Errorf("not strict: refused: %s", out)
		}
	}
	// the HTTP twin refuses the same request the same way
	c := as(visit("POST", "/v1/messages"), w.cred)
	c.Fiber().Request().Header.Set(strictHeader, "1")
	c.Fiber().Request().SetBody([]byte(body))
	c.AnthropicMessages()
	if answered(c) != http.StatusConflict {
		t.Errorf("http twin status %d", answered(c))
	}
}

func TestZapGatewayCarriesVersionAndBetas(t *testing.T) {
	url, got := nativeUpstream(t, "application/json", nativeReply)
	p := nativePayer(t, "acme", url)
	body := strings.Replace(maximal, `"stream": false`, `"stream": true`, 1)
	msg, err := gateway(nil)(context.Background(), "", gatewayCall(t, "/v1/messages",
		map[string]string{"Authorization": p.credential, "anthropic-version": "2023-01-01", "anthropic-beta": "files-api-2025-04-14"}, body))
	if err != nil {
		t.Fatal(err)
	}
	if status, out := antDecodeGateway(t, msg); status != http.StatusOK {
		t.Fatalf("status %d: %s", status, out)
	}
	_, _, _, header := got.get()
	if v := header.Get("Anthropic-Version"); v != "2023-01-01" {
		t.Errorf("anthropic-version = %q, want the frame's", v)
	}
	if v := header.Values("Anthropic-Beta"); len(v) != 1 || v[0] != "files-api-2025-04-14" {
		t.Errorf("anthropic-beta = %q, want the frame's", v)
	}
}
