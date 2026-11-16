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

package markup

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// shape reads the JSON body of a block as it arrives and says when the bytes can no
// longer be that body: one JSON object, then only whitespace and the block's
// closer. Prose after an opener that merely looked like one leaves the shape at its
// first byte, so the text is let go then and not held to the end of the answer.
// It accepts every prefix a JSON parser does and some it does not (numbers and raw
// control bytes in strings are read loosely): the parse of the whole block has the
// last word, this only decides when to stop waiting for it.
type shape struct {
	stack  []byte
	st     int
	key    bool
	esc    bool
	hex    int
	lit    string
	closer string
	tail   string
}

const (
	inValue = iota
	inKeyOrEnd
	inKey
	inColon
	inValueOrEnd
	inString
	inNumber
	inLiteral
	inAfter
)

const maxDepth = 64

func newShape(closer string) *shape { return &shape{closer: closer} }

// feed reads more of the body, and is false once it cannot be one.
func (j *shape) feed(b []byte) bool {
	for _, c := range b {
		if !j.step(c) {
			return false
		}
	}
	return true
}

func (j *shape) push(c byte) bool {
	j.stack = append(j.stack, c)
	return len(j.stack) <= maxDepth
}

func (j *shape) pop() { j.stack = j.stack[:len(j.stack)-1] }

func (j *shape) step(c byte) bool {
	switch j.st {
	case inString:
		switch {
		case j.hex > 0:
			j.hex--
			return strings.IndexByte("0123456789abcdefABCDEF", c) >= 0
		case j.esc:
			j.esc = false
			if c == 'u' {
				j.hex = 4
				return true
			}
			return strings.IndexByte(`"\/bfnrt`, c) >= 0
		case c == '\\':
			j.esc = true
		case c == '"':
			j.st = inAfter
			if j.key {
				j.st = inColon
			}
		}
		return true
	case inNumber:
		if c >= '0' && c <= '9' || strings.IndexByte("+-.eE", c) >= 0 {
			return true
		}
		j.st = inAfter
		return j.step(c)
	case inLiteral:
		if c != j.lit[0] {
			return false
		}
		if j.lit = j.lit[1:]; j.lit == "" {
			j.st = inAfter
		}
		return true
	case inValueOrEnd:
		if c == ']' && len(j.stack) > 0 {
			j.pop()
			j.st = inAfter
			return true
		}
		if isSpace(c) {
			return true
		}
		j.st = inValue
		return j.step(c)
	case inKeyOrEnd, inKey:
		switch {
		case isSpace(c):
		case c == '"':
			j.st, j.key = inString, true
		case c == '}' && j.st == inKeyOrEnd:
			j.pop()
			j.st = inAfter
		default:
			return false
		}
		return true
	case inColon:
		if isSpace(c) {
			return true
		}
		j.st = inValue
		return c == ':'
	case inAfter:
		if len(j.stack) == 0 {
			if j.tail == "" && isSpace(c) {
				return true
			}
			j.tail += string(c)
			return strings.HasPrefix(j.closer, j.tail)
		}
		top := j.stack[len(j.stack)-1]
		switch {
		case isSpace(c):
		case c == ',' && top == '{':
			j.st = inKey
		case c == ',':
			j.st = inValue
		case c == '}' && top == '{', c == ']' && top == '[':
			j.pop()
		default:
			return false
		}
		return true
	}
	// inValue
	switch {
	case isSpace(c):
	case c == '"':
		j.st, j.key = inString, false
	case c == '{':
		j.st = inKeyOrEnd
		return j.push('{')
	case c == '[':
		j.st = inValueOrEnd
		return j.push('[')
	case c == '-' || c >= '0' && c <= '9':
		j.st = inNumber
	case c == 't':
		j.st, j.lit = inLiteral, "rue"
	case c == 'f':
		j.st, j.lit = inLiteral, "alse"
	case c == 'n':
		j.st, j.lit = inLiteral, "ull"
	default:
		return false
	}
	return true
}

// object reads b as one JSON object whose names are all distinct, at every depth,
// and returns its members. A body that repeats a name, or has trailing bytes, is not
// one: a parser keeps one of two copies and a reader of another could keep the
// other.
func object(b []byte) (map[string]json.RawMessage, bool) {
	if !distinct(b) {
		return nil, false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil || m == nil {
		return nil, false
	}
	return m, true
}

// distinct is true when b is one JSON value in which no object repeats a name.
func distinct(b []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(b))
	if !walk(dec) {
		return false
	}
	_, err := dec.Token()
	return err == io.EOF
}

func walk(dec *json.Decoder) bool {
	tok, err := dec.Token()
	if err != nil {
		return false
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return true
	}
	seen := map[string]bool{}
	for dec.More() {
		if d == '{' {
			k, err := dec.Token()
			name, _ := k.(string)
			if err != nil || seen[name] {
				return false
			}
			seen[name] = true
		}
		if !walk(dec) {
			return false
		}
	}
	_, err = dec.Token()
	return err == nil
}
