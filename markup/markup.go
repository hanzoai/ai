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
// form, Qwen's and Hermes's <tool_call> JSON or XML. The stack serving the model
// turns that markup into structured tool_calls, and when it misses (the model wrote
// prose first, then the block) the markup arrives as content and the caller never
// sees a call. Lift and Scanner do that parse here.
//
// A call is lifted only when its name is one the request offered, and a block with
// any invalid invoke is left as the text it arrived as: this never invents a call.
// Only the markup of the family serving the model is read, only blocks that end the
// answer are lifted, and nothing inside a code fence or inline code is: a model
// quoting a block is not calling it.
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

// Rules says what a scanner may lift.
type Rules struct {
	Offered map[string]bool
	Model   string // the upstream model id: its family picks the markup read
	Only    string // when set, the one tool that may be lifted
	One     bool   // at most one call, for a request that forbade parallel calls
}

type verdict int

const (
	wait   verdict = iota // more bytes decide it
	accept                // the block can be what its opener says
	reject                // it cannot: the opener is text
)

// form is one markup family member: the text that opens a block, the text that
// closes it, the check of what must follow the opener, and the parse of a whole
// block into calls.
type form struct {
	open, close string
	follow      func(after string) verdict
	parse       func(block string) ([]rawCall, bool)
}

type rawCall struct{ name, args string }

var (
	deepseek = []*form{
		{"<｜DSML｜tool_calls>", "</｜DSML｜tool_calls>", followedBy(invoke), dsml},
		{"<｜DSML｜function_calls>", "</｜DSML｜function_calls>", followedBy(invoke), dsml},
		{"<｜tool▁calls▁begin｜>", "<｜tool▁calls▁end｜>", followedBy(begin), fenced},
		{"<｜tool▁call▁begin｜>", "<｜tool▁call▁end｜>", func(string) verdict { return accept }, fenced},
	}
	hermesForms = []*form{
		{"<tool_call>", "</tool_call>", hermesFollow, hermes},
	}
)

// formsOf is the markup the family serving model writes, none for a model whose
// family is not known: nothing is lifted from an answer whose markup is a guess.
func formsOf(model string) []*form {
	m := strings.ToLower(model)
	base := m[strings.LastIndex(m, "/")+1:]
	switch {
	case strings.Contains(m, "deepseek"):
		return deepseek
	case strings.Contains(m, "qwen"), strings.Contains(m, "hermes"), strings.HasPrefix(base, "zen"):
		return hermesForms
	}
	return nil
}

// Reads reports whether the family serving model has markup that is lifted.
func Reads(model string) bool { return len(formsOf(model)) > 0 }

// Quotes reports whether text holds an opener of the family serving model: a
// request that already carries one could be asking the model to echo a call.
func Quotes(model, text string) bool {
	for _, f := range formsOf(model) {
		if strings.Contains(text, f.open) {
			return true
		}
	}
	return false
}

// followedBy accepts an opener followed, past whitespace, by want.
func followedBy(want string) func(string) verdict {
	return func(after string) verdict {
		return starts(after, want)
	}
}

// starts is accept when s, past leading whitespace, begins with want, wait while it
// is a prefix of want or only whitespace so far, and reject otherwise. Whitespace
// longer than the window is no block.
func starts(s, want string) verdict {
	const window = 1024
	t := strings.TrimLeft(s[:min(len(s), window)], " \t\r\n")
	switch {
	case strings.HasPrefix(t, want):
		return accept
	case t == "" && len(s) >= window:
		return reject
	case strings.HasPrefix(want, t):
		return wait
	}
	return reject
}

// hermesFollow accepts a <tool_call> followed by a JSON object or a <function=.
func hermesFollow(after string) verdict {
	if starts(after, "{") == accept {
		return accept
	}
	return starts(after, "<function=")
}

// maxHeld bounds what a scanner keeps while it waits to decide. A block longer than
// this is not a tool call anyone sent, and is released as text.
const maxHeld = 256 << 10

// Lift returns content with its trailing tool-call blocks removed and the calls
// they held. Content with no liftable block is returned as it came.
func Lift(content string, r Rules) (string, []Call) {
	s := NewScanner(r)
	text := s.Feed(content)
	rest, calls := s.Flush()
	return text + rest, calls
}

// Scanner is Lift over a stream of content deltas. Text passes through as soon as
// it cannot be the start of a block; from the first byte that could begin one the
// scanner holds, until the block is known to be text or the stream ends. At the end
// it lifts the held blocks if they closed and only whitespace follows, and otherwise
// lets them go as text. Whitespace between the text and a lifted block is dropped,
// so the text a stream yields equals the text Lift returns, whatever the split.
type Scanner struct {
	rules Rules
	forms []*form

	// Whether the text let go so far ends inside a code fence or inline code.
	fence   byte // the fence character while inside a fence
	tick    bool // inside `inline code`
	run     int  // length of the run of fence characters being read
	runChar byte

	pend []byte // whitespace not yet known to be interior

	buf     []byte // held: from the first byte that could begin a block
	holding bool
	done    int // end of the last closed block in buf
	gap     int // whitespace after done is read up to here
	cur     *form
	start   int // where the block being read opens
	scanned int // the closer is searched from here
	calls   []Call
}

// NewScanner returns a scanner lifting the calls r allows.
func NewScanner(r Rules) *Scanner { return &Scanner{rules: r, forms: formsOf(r.Model)} }

// Feed takes the next delta and returns the text now known to be text. The calls
// come at Flush: a block is lifted only once nothing but whitespace follows it.
func (s *Scanner) Feed(delta string) string {
	if len(s.forms) == 0 {
		return delta
	}
	var out strings.Builder
	s.buf = append(s.buf, delta...)
	for {
		if !s.holding {
			i := s.candidate(s.buf)
			if i < 0 {
				s.emit(&out, s.buf)
				s.buf = s.buf[:0]
				break
			}
			s.emit(&out, s.buf[:i])
			s.buf = s.buf[i:]
			s.holding, s.done, s.gap, s.cur = true, 0, 0, nil
			s.calls = s.calls[:0]
		}
		n := s.hold()
		if n < 0 {
			break
		}
		s.release(&out, s.buf[:n])
		s.buf = s.buf[n:]
		s.holding = false
	}
	return out.String()
}

// hold decides what the held text is, as far as it can be decided now: the number
// of bytes at the front of buf to let go as text, or -1 to keep holding.
func (s *Scanner) hold() int {
	for {
		if s.cur == nil {
			j := max(s.done, s.gap)
			for j < len(s.buf) && isSpace(s.buf[j]) {
				j++
			}
			s.gap = j
			r := s.buf[j:]
			var f *form
			for _, c := range s.forms {
				if bytes.HasPrefix(r, []byte(c.open)) {
					f = c
				}
			}
			if f == nil {
				if len(r) == 0 || s.starts(r) {
					return s.cap()
				}
				// Not an opener: what follows a closed block is text, so the blocks are
				// not trailing; a lone byte that looked like an opener is text.
				return max(s.done, 1)
			}
			after := string(r[len(f.open):min(len(r), len(f.open)+1024)])
			switch f.follow(after) {
			case wait:
				return s.cap()
			case reject:
				if s.done > 0 {
					return s.done
				}
				return len(f.open)
			}
			s.cur, s.start, s.scanned = f, j, j+len(f.open)
		}
		from := max(s.scanned-len(s.cur.close)+1, s.start+len(s.cur.open))
		i := bytes.Index(s.buf[from:], []byte(s.cur.close))
		s.scanned = len(s.buf)
		if i < 0 {
			return s.cap()
		}
		end := from + i + len(s.cur.close)
		raws, ok := s.cur.parse(string(s.buf[s.start:end]))
		calls, ok2 := s.lift(raws)
		if !ok || !ok2 {
			return end
		}
		s.calls = append(s.calls, calls...)
		if s.rules.One && len(s.calls) > 1 {
			return end
		}
		s.done, s.cur = end, nil
	}
}

// cap is -1 while the held text is within its bound, and all of it once it is not.
func (s *Scanner) cap() int {
	if len(s.buf) > maxHeld {
		return len(s.buf)
	}
	return -1
}

// release lets held bytes go as text.
func (s *Scanner) release(out *strings.Builder, text []byte) {
	for _, b := range text {
		s.track(b)
	}
	s.emit(out, text)
}

// Flush ends the stream. When what is held is one or more closed blocks with only
// whitespace after them, it returns their calls; otherwise it returns what was
// held as text.
func (s *Scanner) Flush() (string, []Call) {
	if s.holding && s.cur == nil && s.done > 0 && len(bytes.TrimSpace(s.buf[s.done:])) == 0 && len(s.calls) > 0 {
		calls := s.calls
		s.reset()
		return "", calls
	}
	return s.Release(), nil
}

// Release ends the stream and returns whatever was held as text, lifting nothing.
func (s *Scanner) Release() string {
	out := string(s.pend) + string(s.buf)
	s.reset()
	return out
}

func (s *Scanner) reset() {
	s.pend, s.buf, s.calls = nil, nil, nil
	s.holding, s.cur = false, nil
}

// Held reports whether the scanner is keeping text back.
func (s *Scanner) Held() bool { return len(s.pend) > 0 || len(s.buf) > 0 }

// emit writes text to out, keeping trailing whitespace back until something follows
// it.
func (s *Scanner) emit(out *strings.Builder, text []byte) {
	k := len(bytes.TrimRight(text, " \t\r\n"))
	if k == 0 {
		s.pend = append(s.pend, text...)
		return
	}
	out.Write(s.pend)
	out.Write(text[:k])
	s.pend = append(s.pend[:0], text[k:]...)
}

// lift checks the calls of one block against the rules, all or nothing.
func (s *Scanner) lift(raws []rawCall) ([]Call, bool) {
	if len(raws) == 0 {
		return nil, false
	}
	calls := make([]Call, 0, len(raws))
	for _, r := range raws {
		if !s.rules.Offered[r.name] || s.rules.Only != "" && r.name != s.rules.Only {
			return nil, false
		}
		calls = append(calls, Call{ID: id(), Name: r.name, Arguments: r.args})
	}
	return calls, true
}

// candidate is the index in text of the first byte that could begin a block outside
// code: the start of an opener, or a tail that is a proper prefix of one. -1 when
// none. It reads the code state through text up to there.
func (s *Scanner) candidate(text []byte) int {
	for i, b := range text {
		if b == '<' {
			s.settle()
			if s.fence == 0 && !s.tick && s.starts(text[i:]) {
				return i
			}
		}
		s.track(b)
	}
	return -1
}

func (s *Scanner) starts(t []byte) bool {
	for _, f := range s.forms {
		if bytes.HasPrefix(t, []byte(f.open)) || len(t) < len(f.open) && strings.HasPrefix(f.open, string(t)) {
			return true
		}
	}
	return false
}

// track reads one byte of let-go text into the code state: a run of three or more
// backticks or tildes opens and closes a fence, a lone backtick an inline span that
// ends with its line.
func (s *Scanner) track(b byte) {
	if b == '`' || b == '~' {
		if s.runChar != b {
			s.settle()
			s.runChar = b
		}
		s.run++
		return
	}
	s.settle()
	if b == '\n' {
		s.tick = false
	}
}

func (s *Scanner) settle() {
	switch {
	case s.run >= 3 && s.fence == 0:
		s.fence, s.tick = s.runChar, false
	case s.run >= 3 && s.fence == s.runChar:
		s.fence = 0
	case s.run > 0 && s.runChar == '`' && s.fence == 0:
		s.tick = !s.tick
	}
	s.run, s.runChar = 0, 0
}

func isSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\r' || b == '\n' }

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
