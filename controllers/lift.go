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

// lift.go — a model's tool call written as text, made a tool call.
//
// A model writes its tool call in its own markup, and the stack serving it turns
// that into structured tool_calls. When that stack misses — the model wrote prose
// first and then the block — the markup arrives as assistant content, no tool call
// arrives, and an agent reads the turn as finished. A mark (envelope.go) carries a
// lifter for a request that offered tools, and the lifter reads each choice of a
// chat envelope the mark stamps, buffered or chunked, through package markup. So
// every relay that stamps — the forward relay, the family relay, an assembled
// answer — lifts the same way, and /v1/responses, which is served as a chat
// completion, with them.
//
// What is lifted is narrow, because a model quoting a call is not making one: only
// the markup of the family serving the routed model, only a block that ends the
// answer, never in code, never for a request that already carries an opener of that
// markup (a caller could be asking the model to echo it), never from a choice the
// upstream gave structured calls, and never for a strict request.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"maps"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/hanzoai/ai/markup"
)

// lifter lifts the tool calls of one request's answer. One per request, read and
// written on the goroutine serving it. A nil lifter lifts nothing.
type lifter struct {
	rules  markup.Rules
	scan   map[int]*markup.Scanner // by choice: a stream's held text
	calls  map[int]int             // by choice: tool calls the stream has so far
	made   map[int]bool            // by choice: a call was lifted from its text
	native map[int]bool            // by choice: the upstream sent structured calls
	shift  map[int]int             // by choice: lifted calls the upstream's own indices follow
}

// liftFor is the lifter of a chat request body served by the upstream model, none
// when markup.RulesFor says nothing may be lifted.
func liftFor(body []byte, upstream string, strict bool) *lifter {
	rules, ok := markup.RulesFor(body, upstream, strict)
	if !ok {
		return nil
	}
	return &lifter{rules: rules, scan: map[int]*markup.Scanner{}, calls: map[int]int{}, made: map[int]bool{},
		native: map[int]bool{}, shift: map[int]int{}}
}

// choices lifts the calls out of each choice's message (a buffered answer) or delta
// (a chunk). Text a stream holds back leaves with the chunk that ends its choice;
// the calls leave in the chunk that completes them, and a choice that made a call
// finishes as tool_calls. A choice it cannot read is left alone.
func (l *lifter) choices(raw json.RawMessage, chunk bool) json.RawMessage {
	var all []map[string]json.RawMessage
	if l == nil || json.Unmarshal(raw, &all) != nil {
		return raw
	}
	changed := false
	for _, choice := range all {
		changed = l.choice(choice, chunk) || changed
	}
	if !changed {
		return raw
	}
	if out, err := encode(all); err == nil {
		return out
	}
	return raw
}

func (l *lifter) choice(choice map[string]json.RawMessage, chunk bool) bool {
	var at int
	_ = json.Unmarshal(choice["index"], &at)
	// A choice of a chunk is streamed with or without a delta: the one that carries
	// only its finish_reason ends the choice, and what is held must leave with it.
	_, delta := choice["delta"]
	_, whole := choice["message"]
	part, stream := "message", false
	if delta || chunk && !whole || !whole && l.scan[at] != nil {
		part, stream = "delta", true
	}
	var body map[string]json.RawMessage
	if _, ok := choice[part]; !ok && stream {
		body = map[string]json.RawMessage{}
	} else if json.Unmarshal(choice[part], &body) != nil || body == nil {
		return false
	}
	var native []json.RawMessage
	_ = json.Unmarshal(body["tool_calls"], &native)
	changed := false
	if stream && len(native) > 0 {
		// An upstream call arriving after lifted ones is numbered after them.
		l.native[at] = true
		for k, c := range native {
			var n map[string]json.RawMessage
			if json.Unmarshal(c, &n) != nil {
				continue
			}
			i := k
			if raw, ok := n["index"]; ok {
				_ = json.Unmarshal(raw, &i)
			}
			i += l.shift[at]
			l.calls[at] = max(l.calls[at], i+1)
			if l.shift[at] > 0 {
				n["index"], _ = json.Marshal(i)
				native[k], _ = encode(n)
				changed = true
			}
		}
		if changed {
			body["tool_calls"], _ = encode(native)
		}
	}
	var content string
	text := bytes.HasPrefix(body["content"], []byte(`"`)) && json.Unmarshal(body["content"], &content) == nil
	var finish string
	_ = json.Unmarshal(choice["finish_reason"], &finish)
	var sc *markup.Scanner
	if stream {
		if sc = l.scan[at]; sc == nil {
			sc = markup.NewScanner(l.rules)
			l.scan[at] = sc
		}
	}
	// A choice the upstream gave structured calls is not read for more: what the
	// scanner holds goes out as the text it is.
	if l.native[at] || !stream && len(native) > 0 {
		if sc != nil && sc.Held() {
			body["content"], _ = json.Marshal(sc.Release() + content)
			choice[part], _ = encode(body)
			return true
		}
		if changed {
			choice[part], _ = encode(body)
		}
		return changed
	}
	// A streamed choice is always read: the scanner follows every byte, the code
	// marks and line breaks among them, so no chunk may skip it.
	if finish == "" && !(stream && text) {
		return false
	}

	var out string
	var lifted []markup.Call
	switch {
	case stream:
		if text {
			out = sc.Feed(content)
		}
		if finish == "stop" {
			var rest string
			rest, lifted = sc.Flush()
			out += rest
		} else if finish != "" {
			out += sc.Release()
		}
	case text && finish == "stop":
		out, lifted = markup.Lift(content, l.rules)
	default:
		out = content
	}
	if len(lifted) > 0 {
		l.made[at] = true
	}
	finishing := l.made[at] && finish == "stop"
	if out == content && len(lifted) == 0 && !finishing {
		return false
	}

	if text || out != "" {
		body["content"], _ = json.Marshal(out)
	}
	if len(lifted) > 0 {
		base := len(native)
		if stream {
			base, native = l.calls[at], nil
		}
		for i, c := range lifted {
			call := map[string]any{
				"id":       c.ID,
				"type":     "function",
				"function": map[string]string{"name": c.Name, "arguments": c.Arguments},
			}
			if stream {
				call["index"] = base + i
			}
			encoded, _ := encode(call)
			native = append(native, encoded)
		}
		l.calls[at] = base + len(lifted)
		l.shift[at] += len(lifted)
		body["tool_calls"], _ = encode(native)
		if !stream && out == "" {
			body["content"] = json.RawMessage("null")
		}
	}
	if finishing {
		choice["finish_reason"] = json.RawMessage(`"tool_calls"`)
	}
	choice[part], _ = encode(body)
	return true
}

// held lets go of the text each stream choice still holds, as chunk choices.
func (l *lifter) held() []map[string]any {
	var out []map[string]any
	for _, at := range slices.Sorted(maps.Keys(l.scan)) {
		if sc := l.scan[at]; sc.Held() {
			out = append(out, map[string]any{"index": at, "delta": map[string]string{"content": sc.Release()}, "finish_reason": nil})
		}
	}
	return out
}

// rest is the text a stream still holds when it ends: one chunk, stamped, carrying
// it, or nil when nothing is held.
func (m *mark) rest() []byte {
	if m == nil || m.lift == nil {
		return nil
	}
	held := m.lift.held()
	if held == nil {
		return nil
	}
	if m.created == nil {
		m.created, _ = json.Marshal(time.Now().Unix())
	}
	return wire(map[string]any{
		"id": m.id, "object": "chat.completion.chunk", "created": m.created,
		"model": m.model, "provider": m.seller, "choices": held,
	})
}

// payload lifts one chat answer, buffered or one chunk, that is not stamped: the
// answer an upstream gave a translator that speaks another dialect.
func (l *lifter) payload(p []byte) []byte {
	var env map[string]json.RawMessage
	if l == nil || json.Unmarshal(p, &env) != nil || env["choices"] == nil {
		return p
	}
	lifted := l.choices(env["choices"], string(env["object"]) == `"chat.completion.chunk"`)
	if bytes.Equal(lifted, env["choices"]) {
		return p
	}
	env["choices"] = lifted
	if out, err := encode(env); err == nil {
		return out
	}
	return p
}

// stream reads an upstream chat SSE stream with its tool calls lifted, and the
// text it held at the end let go.
func (l *lifter) stream(r io.Reader) io.Reader {
	if l == nil {
		return r
	}
	return &liftedStream{l: l, in: bufio.NewReader(r)}
}

type liftedStream struct {
	l    *lifter
	in   *bufio.Reader
	out  []byte
	done bool
}

func (s *liftedStream) Read(p []byte) (int, error) {
	for len(s.out) == 0 {
		if s.done {
			return 0, io.EOF
		}
		line, err := s.in.ReadBytes('\n')
		if err != nil {
			s.done = true
		}
		s.out = s.rewrite(line)
		if s.done {
			s.out = append(s.out, s.tail()...)
		}
	}
	n := copy(p, s.out)
	s.out = s.out[n:]
	return n, nil
}

// rewrite is one SSE line, lifted. The held tail goes out before [DONE], or at the
// stream's end when it has none.
func (s *liftedStream) rewrite(line []byte) []byte {
	data, ok := bytes.CutPrefix(bytes.TrimRight(line, "\r\n"), []byte("data:"))
	data = bytes.TrimSpace(data)
	switch {
	case ok && string(data) == "[DONE]":
		return append(s.tail(), line...)
	case ok && len(data) > 0 && data[0] == '{':
		return framed(s.l.payload(data), "\n")
	}
	return line
}

func (s *liftedStream) tail() []byte {
	held := s.l.held()
	if held == nil {
		return nil
	}
	chunk, _ := encode(map[string]any{"object": "chat.completion.chunk", "choices": held})
	return framed(chunk, "\n\n")
}

// maxFrame bounds the text one SSE line carries: a reader that scans a stream by
// lines has a limit, and text let go from a hold can be as long as the hold.
const maxFrame = 64 << 10

// framed is a chunk as SSE data lines ending in end. Text in a chunk's delta longer
// than maxFrame goes first, as chunks of its own, so no line is longer than a few
// times maxFrame whatever the text held.
func framed(chunk []byte, end string) []byte {
	var env map[string]json.RawMessage
	var choices []map[string]json.RawMessage
	var out []byte
	if json.Unmarshal(chunk, &env) == nil && json.Unmarshal(env["choices"], &choices) == nil {
		for more := true; more; {
			more = false
			var lead []map[string]any
			for _, ch := range choices {
				var delta map[string]json.RawMessage
				var content string
				if json.Unmarshal(ch["delta"], &delta) != nil || json.Unmarshal(delta["content"], &content) != nil || len(content) <= maxFrame {
					continue
				}
				cut := maxFrame
				for cut > 0 && !utf8.RuneStart(content[cut]) {
					cut--
				}
				delta["content"], _ = json.Marshal(content[cut:])
				ch["delta"], _ = encode(delta)
				lead = append(lead, map[string]any{"index": ch["index"], "delta": map[string]string{"content": content[:cut]}, "finish_reason": nil})
				more = more || len(content[cut:]) > maxFrame
			}
			if len(lead) > 0 {
				piece := maps.Clone(env)
				delete(piece, "usage")
				piece["choices"], _ = encode(lead)
				encoded, _ := encode(piece)
				out = append(append(append(out, "data: "...), encoded...), "\n\n"...)
			}
		}
		env["choices"], _ = encode(choices)
		chunk, _ = encode(env)
	}
	return append(append(append(out, "data: "...), chunk...), end...)
}
