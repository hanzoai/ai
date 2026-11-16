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

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/ai/markup"
)

// lifter lifts the tool calls of one request's answer. One per request, read and
// written on the goroutine serving it. A nil lifter lifts nothing.
type lifter struct {
	offered map[string]bool
	scan    map[int]*markup.Scanner // by choice: a stream's held text
	calls   map[int]int             // by choice: tool calls the stream has so far
	made    map[int]bool            // by choice: a call was lifted from its text
}

// liftFor is the lifter of a chat request body: one for each tool the request
// offered, and none when it offered none or forbade calling them.
func liftFor(body []byte) *lifter {
	var r struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
		Functions []struct {
			Name string `json:"name"`
		} `json:"functions"`
		ToolChoice json.RawMessage `json:"tool_choice"`
	}
	if json.Unmarshal(body, &r) != nil || string(r.ToolChoice) == `"none"` {
		return nil
	}
	offered := map[string]bool{}
	for _, t := range r.Tools {
		if t.Function.Name != "" {
			offered[t.Function.Name] = true
		}
	}
	for _, f := range r.Functions {
		if f.Name != "" {
			offered[f.Name] = true
		}
	}
	if len(offered) == 0 {
		return nil
	}
	return &lifter{offered: offered, scan: map[int]*markup.Scanner{}, calls: map[int]int{}, made: map[int]bool{}}
}

// choices lifts the calls out of each choice's message (a buffered answer) or delta
// (a chunk). Text a stream holds back leaves with the chunk that ends its choice;
// the calls leave in the chunk that completes them, and a choice that made a call
// finishes as tool_calls. A choice it cannot read is left alone.
func (l *lifter) choices(raw json.RawMessage) json.RawMessage {
	var all []map[string]json.RawMessage
	if l == nil || json.Unmarshal(raw, &all) != nil {
		return raw
	}
	changed := false
	for _, choice := range all {
		changed = l.choice(choice) || changed
	}
	if !changed {
		return raw
	}
	if out, err := encode(all); err == nil {
		return out
	}
	return raw
}

func (l *lifter) choice(choice map[string]json.RawMessage) bool {
	part, stream := "message", false
	if _, ok := choice["delta"]; ok {
		part, stream = "delta", true
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(choice[part], &body) != nil || body == nil {
		return false
	}
	var at int
	_ = json.Unmarshal(choice["index"], &at)
	var native []json.RawMessage
	_ = json.Unmarshal(body["tool_calls"], &native)
	if stream {
		for i, c := range native {
			var n struct {
				Index *int `json:"index"`
			}
			_ = json.Unmarshal(c, &n)
			if n.Index != nil {
				i = *n.Index
			}
			l.calls[at] = max(l.calls[at], i+1)
		}
	}
	var content string
	text := bytes.HasPrefix(body["content"], []byte(`"`)) && json.Unmarshal(body["content"], &content) == nil
	var finish string
	_ = json.Unmarshal(choice["finish_reason"], &finish)
	var sc *markup.Scanner
	if stream {
		if sc = l.scan[at]; sc == nil {
			sc = markup.NewScanner(l.offered)
			l.scan[at] = sc
		}
	}
	// Text with no "<" and no whitespace, with nothing held, cannot begin a block or
	// change what is held: it goes on as it is.
	if finish == "" && (!text || !strings.ContainsAny(content, "<\n\r\t ") && (sc == nil || !sc.Held())) {
		return false
	}

	var out string
	var lifted []markup.Call
	if stream {
		if text {
			out, lifted = sc.Feed(content)
		}
		if finish != "" {
			out += sc.Flush()
		}
	} else if text {
		out, lifted = markup.Lift(content, l.offered)
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
			out = append(out, map[string]any{"index": at, "delta": map[string]string{"content": sc.Flush()}, "finish_reason": nil})
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
	lifted := l.choices(env["choices"])
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
		return append(append([]byte("data: "), s.l.payload(data)...), '\n')
	}
	return line
}

func (s *liftedStream) tail() []byte {
	held := s.l.held()
	if held == nil {
		return nil
	}
	chunk, _ := encode(map[string]any{"object": "chat.completion.chunk", "choices": held})
	return append(append([]byte("data: "), chunk...), '\n', '\n')
}
