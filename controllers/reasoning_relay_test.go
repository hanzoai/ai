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
	"fmt"
	"strings"
	"testing"
)

// An answer from a reasoning-inlining upstream (its SKU routes to a deepseek id)
// reaches the caller whole on every surface the relay serves a chat stream through:
// the one the model wrote, not the one a template would have.

// thinkWorld is a relay world whose think SKU's upstream says frames.
func thinkWorld(t *testing.T, frames ...string) *relayWorld {
	t.Helper()
	w := newRelayWorld(t, sseOf(append(frames, usageFrame(10, 5), "[DONE]")...), never(t))
	thinkRoute(t)
	return w
}

// answer frames: the content as it streams, reasoning_content first when asked.
func answerFrames(reasoning bool, parts ...string) []string {
	frames := []string{chunkFrame(`{"role":"assistant","content":""}`, "")}
	if reasoning {
		frames = append(frames, chunkFrame(`{"reasoning_content":"Let me think. "}`, ""), chunkFrame(`{"reasoning_content":"Done."}`, ""))
	}
	for _, p := range parts {
		frames = append(frames, chunkFrame(fmt.Sprintf(`{"content":%q}`, p), ""))
	}
	return append(frames, chunkFrame(`{}`, "stop"))
}

// A request with tools is the one the Anthropic surface sends over chat.
const anthropicTool = `"tools":[{"name":"exec_command","description":"run","input_schema":{"type":"object"}}]`

const relayTool = `"tools":[{"type":"function","function":{"name":"exec_command","parameters":{"type":"object"}}}]`

// the three answers a reasoning-inlining upstream's stream may carry that hold no
// reasoning block: reasoning sent apart, no reasoning, and text that mentions the
// terminator.
var unthought = map[string]struct {
	reasoning bool
	parts     []string
	want      string
}{
	"reasoning_content":     {true, []string{"Hello ", "world, this is the answer."}, "Hello world, this is the answer."},
	"non-thinking":          {false, []string{"Hello ", "world, this is the answer."}, "Hello world, this is the answer."},
	"leading whitespace":    {false, []string{"\n", "Hello world"}, "\nHello world"},
	"a literal close later": {false, []string{"Close a block with </th", "ink> and go on."}, "Close a block with </think> and go on."},
	"a close and a prefix":  {false, []string{"Reasoning aloud.</think>The answer."}, "Reasoning aloud.</think>The answer."},
	"reasoned, then a tag":  {true, []string{"<think>", "kept</think>x"}, "<think>kept</think>x"},
}

func TestReasoningRelayChatStreamKeepsTheAnswer(t *testing.T) {
	for name, c := range unthought {
		for _, tools := range []string{"", "," + relayTool} {
			t.Run(fmt.Sprintf("%s tools=%v", name, tools != ""), func(t *testing.T) {
				w := thinkWorld(t, answerFrames(c.reasoning, c.parts...)...)
				out := sent(w.chat(`{"model":"` + thinkSku + `","stream":true,"messages":[{"role":"user","content":"hi"}]` + tools + `}`))
				h := liftHear(t, out)
				if h.text != c.want || h.finish != "stop" {
					t.Fatalf("text %q finish %q, want %q stop:\n%s", h.text, h.finish, c.want, out)
				}
			})
		}
	}
}

func TestReasoningRelayChatStreamStillStripsABlock(t *testing.T) {
	whole := "<think>plan the answer</think>\n\nThe answer is 42."
	for cut := 0; cut <= len(whole); cut++ {
		w := thinkWorld(t, answerFrames(false, whole[:cut], whole[cut:])...)
		h := liftHear(t, sent(w.chat(`{"model":"`+thinkSku+`","stream":true,"messages":[{"role":"user","content":"hi"}]}`)))
		if h.text != "The answer is 42." {
			t.Fatalf("split at %d: text %q", cut, h.text)
		}
	}
}

// A stream that ends with no finish_reason lets go what the stripper holds.
func TestReasoningRelayChatStreamEndsHoldingNothing(t *testing.T) {
	for _, tail := range []string{"[DONE]", ""} {
		frames := []string{chunkFrame(`{"role":"assistant","content":"<thi"}`, "")}
		if tail != "" {
			frames = append(frames, tail)
		}
		w := newRelayWorld(t, sseOf(frames...), never(t))
		thinkRoute(t)
		h := liftHear(t, sent(w.chat(`{"model":"`+thinkSku+`","stream":true,"messages":[{"role":"user","content":"hi"}]}`)))
		if h.text != "<thi" {
			t.Fatalf("tail %q: text %q, want the held bytes", tail, h.text)
		}
	}
}

func TestReasoningRelayBufferedKeepsALaterClose(t *testing.T) {
	for name, content := range map[string]string{
		"later close": "Close a block with </think> and go on.",
		"bare close":  "Reasoning aloud.</think>The answer.",
	} {
		w := newRelayWorld(t, completes(content, 5, 0, 3), never(t))
		thinkRoute(t)
		h := liftHear(t, sent(w.chat(`{"model":"`+thinkSku+`","messages":[{"role":"user","content":"hi"}]}`)))
		if h.text != content {
			t.Errorf("%s: text %q, want %q", name, h.text, content)
		}
	}
}

func TestReasoningRelayResponsesStreamKeepsTheAnswer(t *testing.T) {
	for name, c := range unthought {
		t.Run(name, func(t *testing.T) {
			w := thinkWorld(t, answerFrames(c.reasoning, c.parts...)...)
			out := sent(w.responses(`{"model":"` + thinkSku + `","stream":true,"input":"hi"}`))
			var deltas []string
			for _, e := range chunks(t, out) {
				if string(e["type"]) == `"response.output_text.delta"` {
					deltas = append(deltas, field(t, e, "delta"))
				}
			}
			if got := strings.Join(deltas, ""); got != c.want {
				t.Fatalf("deltas %q, want %q:\n%s", got, c.want, out)
			}
			completed := strings.Index(out, "event: response.completed")
			if completed < 0 || strings.LastIndex(out, "response.output_text.delta") > completed {
				t.Fatalf("a delta after response.completed, or none:\n%s", out)
			}
		})
	}
}

// anthropicText posts a streamed /v1/messages for the think SKU and returns the text
// the caller reads.
func anthropicText(t *testing.T, w *relayWorld) string {
	t.Helper()
	c := as(visit("POST", "/v1/messages"), w.cred)
	c.Fiber().Request().SetBody([]byte(`{"model":"` + thinkSku + `","max_tokens":256,"stream":true,` + anthropicTool + `,"messages":[{"role":"user","content":"hi"}]}`))
	c.AnthropicMessages()
	var text strings.Builder
	for _, e := range chunks(t, sent(c)) {
		if string(e["type"]) == `"content_block_delta"` {
			text.WriteString(textOf(e["delta"]))
		}
	}
	return text.String()
}

func TestReasoningRelayAnthropicStreamKeepsTheAnswer(t *testing.T) {
	for name, c := range unthought {
		t.Run(name, func(t *testing.T) {
			if got := anthropicText(t, thinkWorld(t, answerFrames(c.reasoning, c.parts...)...)); got != c.want {
				t.Fatalf("text %q, want %q", got, c.want)
			}
		})
	}
	whole := "<think>plan</think>The answer."
	for cut := 0; cut <= len(whole); cut++ {
		if got := anthropicText(t, thinkWorld(t, answerFrames(false, whole[:cut], whole[cut:])...)); got != "The answer." {
			t.Fatalf("split at %d: text %q", cut, got)
		}
	}
}

// What strips is the model that answered, not the name the caller used for it.
func TestReasoningRelayAnthropicKeysOnTheUpstream(t *testing.T) {
	w := newRelayWorld(t, sseOf(append(answerFrames(false, "Hello ", "there"), usageFrame(5, 2), "[DONE]")...), never(t))
	// relaySku routes to a-up, which does not inline: its text is not read for a block.
	c := as(visit("POST", "/v1/messages"), w.cred)
	c.Fiber().Request().SetBody([]byte(`{"model":"` + relaySku + `","max_tokens":256,"stream":true,` + anthropicTool + `,"messages":[{"role":"user","content":"deepseek"}]}`))
	c.AnthropicMessages()
	if !strings.Contains(sent(c), "Hello ") {
		t.Fatalf("answer lost:\n%s", sent(c))
	}
}

func textOf(delta json.RawMessage) string {
	var d struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(delta, &d)
	return d.Text
}
