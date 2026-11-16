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

// Package markup lifts a model's tool calls out of assistant text.
//
// A model writes a tool call in its own markup — DeepSeek's DSML or its fenced
// form, Qwen's and Hermes's <tool_call> JSON. The stack serving the model turns
// that markup into structured tool_calls, and when it misses (the model wrote
// prose first, then the block) the markup arrives as content and the caller never
// sees a call. Lift and Scanner do that parse here.
//
// A call is lifted only when its name is one the request offered, and a block with
// any invalid invoke is left as the text it arrived as: this never invents a call.
package markup

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// Call is one lifted tool call. Arguments is a JSON object, encoded.
type Call struct {
	ID        string
	Name      string
	Arguments string
}

// form is one markup family: the text that opens a block, the text that closes
// it, and the parse of a whole block into calls.
type form struct {
	open, close string
	parse       func(block string) ([]rawCall, bool)
}

type rawCall struct{ name, args string }

var forms = []form{
	{"<｜DSML｜tool_calls>", "</｜DSML｜tool_calls>", dsml},
	{"<｜DSML｜function_calls>", "</｜DSML｜function_calls>", dsml},
	{"<｜tool▁calls▁begin｜>", "<｜tool▁calls▁end｜>", fenced},
	{"<｜tool▁call▁begin｜>", "<｜tool▁call▁end｜>", fenced},
	{"<tool_call>", "</tool_call>", hermes},
}

// maxHeld bounds what a scanner keeps while it waits for a block to close. A block
// longer than this is not a tool call anyone sent, and is released as text.
const maxHeld = 4 << 20

// Lift returns content with its tool-call blocks removed and the calls they held.
// Content with no liftable block is returned as it came.
func Lift(content string, offered map[string]bool) (string, []Call) {
	s := NewScanner(offered)
	text, calls := s.Feed(content)
	return text + s.Flush(), calls
}

// Scanner is Lift over a stream of content deltas. Text passes through as soon as
// it cannot be the start of a block; from the first byte that could begin one, the
// scanner holds until it can decide. Whitespace beside a lifted block is dropped,
// so the text a stream yields equals the text Lift returns, whatever the split.
type Scanner struct {
	offered map[string]bool
	buf     string // held: from the first byte that could begin a block
	pend    string // whitespace not yet known to be interior
	skip    bool   // just after a lifted block: leading whitespace goes
	n       int
}

// NewScanner returns a scanner lifting calls named in offered.
func NewScanner(offered map[string]bool) *Scanner { return &Scanner{offered: offered} }

// Feed takes the next delta and returns the text now known to be text, and the
// calls completed by it.
func (s *Scanner) Feed(delta string) (string, []Call) {
	s.buf += delta
	var out strings.Builder
	var calls []Call
	for {
		i := first(s.buf)
		if i < 0 {
			s.emit(&out, s.buf)
			s.buf = ""
			break
		}
		s.emit(&out, s.buf[:i])
		s.buf = s.buf[i:]
		f := opened(s.buf)
		if f == nil {
			break // a prefix of an opener: wait for more
		}
		end := strings.Index(s.buf, f.close)
		if end < 0 {
			if len(s.buf) > maxHeld {
				s.emit(&out, s.buf)
				s.buf = ""
			}
			break
		}
		end += len(f.close)
		block := s.buf[:end]
		s.buf = s.buf[end:]
		if lifted, ok := s.lift(f, block); ok {
			calls = append(calls, lifted...)
			s.pend, s.skip = "", true
		} else {
			s.emit(&out, block)
		}
	}
	return out.String(), calls
}

// Flush ends the stream and returns whatever was held that never became a call.
func (s *Scanner) Flush() string {
	out := s.pend + s.buf
	s.pend, s.buf = "", ""
	return out
}

// Held reports whether the scanner is keeping text back.
func (s *Scanner) Held() bool { return s.pend != "" || s.buf != "" }

// emit writes text to out, keeping trailing whitespace back until something
// follows it, and dropping leading whitespace right after a lifted block.
func (s *Scanner) emit(out *strings.Builder, text string) {
	if s.skip {
		text = strings.TrimLeft(text, " \t\r\n")
		if text == "" {
			return
		}
		s.skip = false
	}
	text = s.pend + text
	body := strings.TrimRight(text, " \t\r\n")
	if body == "" {
		s.pend = text
		return
	}
	out.WriteString(body)
	s.pend = text[len(body):]
}

// lift parses one whole block, all or nothing: a block with an invoke that is
// malformed or names a tool the request did not offer is not a call.
func (s *Scanner) lift(f *form, block string) ([]Call, bool) {
	raws, ok := f.parse(block)
	if !ok || len(raws) == 0 {
		return nil, false
	}
	calls := make([]Call, 0, len(raws))
	for _, r := range raws {
		if !s.offered[r.name] {
			return nil, false
		}
		calls = append(calls, Call{ID: id(), Name: r.name, Arguments: r.args})
	}
	return calls, true
}

// first is the index of the earliest byte of s that could begin a block: the start
// of an opener, or a tail of s that is a proper prefix of one. -1 when none.
func first(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] != '<' {
			continue
		}
		for j := range forms {
			o, t := forms[j].open, s[i:]
			if strings.HasPrefix(t, o) || strings.HasPrefix(o, t) {
				return i
			}
		}
	}
	return -1
}

// opened is the form whose opener s starts with, or nil when s is only a prefix.
func opened(s string) *form {
	for j := range forms {
		if strings.HasPrefix(s, forms[j].open) {
			return &forms[j]
		}
	}
	return nil
}

func id() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "call_" + hex.EncodeToString(b[:])
}

// compact is v as compact JSON, or false when v is not JSON.
func compact(v string) (string, bool) {
	v = strings.TrimSpace(v)
	var b bytes.Buffer
	if v == "" || json.Compact(&b, []byte(v)) != nil {
		return "", false
	}
	return b.String(), true
}
