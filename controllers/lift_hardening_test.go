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

// finishOnly is the chunk a vendor sends to end a choice with no delta at all.
const finishOnly = `{"id":"up-1","object":"chat.completion.chunk","created":1,"model":"a-up","choices":[{"index":0,"finish_reason":"stop"}]}`

func contentFrame(s string) string {
	return fmt.Sprintf(`{"id":"up-1","object":"chat.completion.chunk","created":1,"model":"a-up","choices":[{"index":0,"delta":{"content":%s}}]}`, liftQuote(s))
}

// Code marks are read from every byte of the text, in whatever chunks it comes: a
// chunk with no "<" or space in it is still part of a run of backticks.
func TestLiftFenceRunSpansChunks(t *testing.T) {
	in := "```\n``x`\n" + liftBlock
	parts := [][]string{{in}, {"```\n``", "x", "`\n" + liftBlock}, {"```\n", "``", "x", "`", "\n", liftBlock}}
	w := newLiftWorld(t, completesWith(in))
	b := liftHear(t, sent(w.chat(`{"model":"relay-sku",`+liftTools+`,"messages":[{"role":"user","content":"go"}]}`)))
	if b.text != in || len(b.names) != 0 {
		t.Fatalf("buffered: %+v, want the text and no call", b)
	}
	for _, p := range parts {
		w := newLiftWorld(t, streams(p, 10, 0, 5))
		h := liftHear(t, sent(w.chat(`{"model":"relay-sku","stream":true,`+liftTools+`,"messages":[{"role":"user","content":"go"}]}`)))
		if h.text != in || len(h.names) != 0 || h.finish != "stop" {
			t.Fatalf("streamed %q: %+v, want the text and no call", p, h)
		}
	}
}

// A choice ended by a chunk with a finish_reason and no delta lifts, and lets its
// held text go, before that finish: nothing follows it.
func TestLiftFinishChunkWithoutDelta(t *testing.T) {
	respTools := `"tools":[{"type":"function","name":"exec_command","parameters":{"type":"object"}}]`
	cut := liftSample[:strings.LastIndex(liftSample, "</｜DSML｜tool_calls>")]
	frames := func(text string) func() *relayWorld {
		return func() *relayWorld {
			return newLiftWorld(t, sseOf(chunkFrame(`{"role":"assistant","content":""}`, ""), contentFrame(text), finishOnly, usageFrame(10, 5), "[DONE]"))
		}
	}

	t.Run("a held call is lifted", func(t *testing.T) {
		out := sent(frames(liftSample)().chat(`{"model":"relay-sku","stream":true,` + liftTools + `,"messages":[{"role":"user","content":"go"}]}`))
		liftHear(t, out).wantCall(t, liftProse, "exec_command", liftArgs)
	})

	t.Run("a held cut block is let go before the finish", func(t *testing.T) {
		out := sent(frames(cut)().chat(`{"model":"relay-sku","stream":true,` + liftTools + `,"messages":[{"role":"user","content":"go"}]}`))
		finished := false
		var text strings.Builder
		for _, e := range chunks(t, out) {
			var ch struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
					FinishReason string `json:"finish_reason"`
				} `json:"choices"`
			}
			_ = json.Unmarshal(marshal(t, e), &ch)
			for _, c := range ch.Choices {
				if c.Delta.Content != "" && finished {
					t.Fatalf("text %q after the finish:\n%s", c.Delta.Content, out)
				}
				text.WriteString(c.Delta.Content)
				finished = finished || c.FinishReason != ""
			}
		}
		if text.String() != cut || !finished {
			t.Fatalf("text %q finished=%v, want %q", text.String(), finished, cut)
		}
	})

	for name, text := range map[string]string{"call": liftSample, "cut block": cut} {
		t.Run("responses "+name, func(t *testing.T) {
			out := sent(frames(text)().responses(`{"model":"relay-sku","stream":true,` + respTools + `,"input":"go"}`))
			done := strings.Index(out, "event: response.completed")
			if done < 0 {
				t.Fatalf("no response.completed:\n%s", out)
			}
			if after := out[done:]; strings.Contains(after, "response.output_text.delta") || strings.Contains(after, "output_item.added") {
				t.Fatalf("an event after response.completed:\n%s", after)
			}
			if name == "call" && !strings.Contains(out, `"function_call"`) {
				t.Fatalf("no function_call:\n%s", out)
			}
			if name == "cut block" && !strings.Contains(out, "Starting with the receipts") {
				t.Fatalf("the held text was lost:\n%s", out)
			}
		})
	}
}

// tool_choice allowed_tools narrows what may be lifted to the allowed set.
func TestLiftHonoursAllowedTools(t *testing.T) {
	two := `"tools":[{"type":"function","function":{"name":"exec_command"}},{"type":"function","function":{"name":"read_file"}}]`
	readBlock := strings.ReplaceAll(liftBlock, "exec_command", "read_file")
	for name, choice := range map[string]string{
		"chat":      `{"type":"allowed_tools","allowed_tools":{"mode":"auto","tools":[{"type":"function","function":{"name":"read_file"}}]}}`,
		"responses": `{"type":"allowed_tools","mode":"auto","tools":[{"type":"function","name":"read_file"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			body := []byte(`{` + two + `,"tool_choice":` + choice + `}`)
			one := func(block string) liftHeard {
				l := liftFor(body, liftModel, false)
				in := `{"choices":[{"index":0,"message":{"role":"assistant","content":` + liftQuote("go\n"+block) + `},"finish_reason":"stop"}]}`
				return liftHear(t, string(l.payload([]byte(in))))
			}
			if h := one(liftBlock); len(h.names) != 0 || h.text != "go\n"+liftBlock {
				t.Fatalf("a tool outside the allowed set was lifted: %+v", h)
			}
			if h := one(readBlock); len(h.names) != 1 || h.names[0] != "read_file" {
				t.Fatalf("an allowed tool was not lifted: %+v", h)
			}
		})
	}
	// An allowed set that names nothing offered lifts nothing.
	body := []byte(`{` + two + `,"tool_choice":{"type":"allowed_tools","allowed_tools":{"tools":[{"type":"function","function":{"name":"other"}}]}}}`)
	if liftFor(body, liftModel, false) != nil {
		t.Fatal("a lifter for an allowed set that names no offered tool")
	}
}
