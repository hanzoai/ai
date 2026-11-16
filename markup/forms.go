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
	"strings"
)

const (
	invoke = "<｜DSML｜invoke"
	param  = "<｜DSML｜parameter"
	sep    = "<｜tool▁sep｜>"
	begin  = "<｜tool▁call▁begin｜>"
	end    = "<｜tool▁call▁end｜>"
)

// dsml reads DeepSeek's DSML block: invokes holding typed parameters. A parameter
// marked string="true" is its text verbatim; any other is read as JSON and, when it
// is not JSON, kept as a string. A block repeating a parameter name is no call.
func dsml(block string) ([]rawCall, bool) {
	body := block[strings.Index(block, ">")+1 : strings.LastIndex(block, "</")]
	var calls []rawCall
	for {
		body = strings.TrimSpace(body)
		if body == "" {
			return calls, true
		}
		tag, rest, ok := tagged(body, invoke)
		if !ok {
			return nil, false
		}
		name, ok := attr(tag, "name")
		if !ok || name == "" {
			return nil, false
		}
		inner, after, ok := strings.Cut(rest, "</｜DSML｜invoke>")
		if !ok {
			return nil, false
		}
		args, ok := parameters(inner)
		if !ok {
			return nil, false
		}
		calls = append(calls, rawCall{name, args})
		body = after
	}
}

// parameters encodes the parameters of one invoke as a JSON object, in the order
// the model wrote them.
func parameters(inner string) (string, bool) {
	var b strings.Builder
	b.WriteByte('{')
	seen := map[string]bool{}
	for n := 0; ; n++ {
		inner = strings.TrimSpace(inner)
		if inner == "" {
			b.WriteByte('}')
			return b.String(), true
		}
		tag, rest, ok := tagged(inner, param)
		if !ok {
			return "", false
		}
		name, ok := attr(tag, "name")
		if !ok || seen[name] {
			return "", false
		}
		seen[name] = true
		value, after, ok := strings.Cut(rest, "</｜DSML｜parameter>")
		if !ok {
			return "", false
		}
		if kind, _ := attr(tag, "string"); kind != "true" {
			if v, ok := compact(value); ok {
				value = v
			} else {
				value = quote(value)
			}
		} else {
			value = quote(value)
		}
		if n > 0 {
			b.WriteByte(',')
		}
		b.WriteString(quote(name))
		b.WriteByte(':')
		b.WriteString(value)
		inner = after
	}
}

// tagged splits s, which must start with the opening tag open, into the tag's
// attributes and what follows its closing bracket.
func tagged(s, open string) (tag, rest string, ok bool) {
	if !strings.HasPrefix(s, open) {
		return "", "", false
	}
	i := strings.Index(s, ">")
	if i < 0 {
		return "", "", false
	}
	return s[len(open):i], s[i+1:], true
}

// attr reads key="value" out of a tag's attributes.
func attr(tag, key string) (string, bool) {
	_, after, ok := strings.Cut(tag, " "+key+`="`)
	if !ok {
		return "", false
	}
	v, _, ok := strings.Cut(after, `"`)
	return v, ok
}

// quote is s as a JSON string, with <, > and & left as written.
func quote(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

// fenced reads DeepSeek's fenced form: calls between begin and end, alone or inside
// a calls wrapper, as "function<sep>name" with a fenced JSON body, or "name<sep>json".
func fenced(block string) ([]rawCall, bool) {
	s := strings.TrimSpace(block)
	s = strings.TrimSuffix(strings.TrimPrefix(s, "<｜tool▁calls▁begin｜>"), "<｜tool▁calls▁end｜>")
	var calls []rawCall
	for {
		s = strings.TrimSpace(s)
		if s == "" {
			return calls, true
		}
		body, ok := strings.CutPrefix(s, begin)
		if !ok {
			return nil, false
		}
		body, s, ok = strings.Cut(body, end)
		if !ok {
			return nil, false
		}
		left, right, ok := strings.Cut(body, sep)
		if !ok {
			return nil, false
		}
		name, right := strings.TrimSpace(left), strings.TrimSpace(right)
		if name == "function" {
			name, right, _ = strings.Cut(right, "\n")
			name = strings.TrimSpace(name)
		}
		right = strings.TrimSpace(right)
		if rest, ok := strings.CutPrefix(right, "```"); ok {
			_, rest, _ = strings.Cut(rest, "\n")
			right = strings.TrimSuffix(strings.TrimSpace(rest), "```")
		}
		args, ok := compact(right)
		if !ok || name == "" {
			return nil, false
		}
		calls = append(calls, rawCall{name, args})
	}
}

// hermes reads the Qwen and Hermes block: one JSON object naming the tool, its
// arguments an object or a JSON string holding one; or Qwen3-Coder's XML form.
func hermes(block string) ([]rawCall, bool) {
	body := strings.TrimSuffix(strings.TrimPrefix(block, "<tool_call>"), "</tool_call>")
	if strings.HasPrefix(strings.TrimSpace(body), "<function=") {
		return coder(body)
	}
	var c struct {
		Name       string          `json:"name"`
		Arguments  json.RawMessage `json:"arguments"`
		Parameters json.RawMessage `json:"parameters"`
	}
	if json.Unmarshal([]byte(body), &c) != nil || c.Name == "" {
		return nil, false
	}
	raw := c.Arguments
	if len(raw) == 0 {
		raw = c.Parameters
	}
	if len(raw) == 0 {
		return []rawCall{{c.Name, "{}"}}, true
	}
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		raw = []byte(encoded)
	}
	args, ok := compact(string(raw))
	if !ok || args[0] != '{' {
		return nil, false
	}
	return []rawCall{{c.Name, args}}, true
}

// coder reads Qwen3-Coder's block: <function=name> holding <parameter=key> values.
// A value is its text between the tag's own line breaks; one that is a JSON object
// or array is that JSON. A repeated parameter name makes the block no call.
func coder(body string) ([]rawCall, bool) {
	rest, _ := strings.CutPrefix(strings.TrimSpace(body), "<function=")
	name, rest, ok := strings.Cut(rest, ">")
	inner, tail, found := strings.Cut(rest, "</function>")
	if !ok || !found || name == "" || strings.TrimSpace(tail) != "" {
		return nil, false
	}
	var b strings.Builder
	b.WriteByte('{')
	seen := map[string]bool{}
	for {
		inner = strings.TrimSpace(inner)
		if inner == "" {
			b.WriteByte('}')
			return []rawCall{{name, b.String()}}, true
		}
		rest, ok := strings.CutPrefix(inner, "<parameter=")
		key, rest, ok2 := strings.Cut(rest, ">")
		value, after, ok3 := strings.Cut(rest, "</parameter>")
		if !ok || !ok2 || !ok3 || seen[key] {
			return nil, false
		}
		seen[key] = true
		value = strings.TrimSuffix(strings.TrimPrefix(value, "\n"), "\n")
		if t := strings.TrimSpace(value); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
			if v, ok := compact(t); ok {
				value = v
			} else {
				value = quote(value)
			}
		} else {
			value = quote(value)
		}
		if b.Len() > 1 {
			b.WriteByte(',')
		}
		b.WriteString(quote(key))
		b.WriteByte(':')
		b.WriteString(value)
		inner = after
	}
}
