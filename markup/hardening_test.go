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
	"strings"
	"testing"
)

// feedBy runs in through a fresh scanner in deltas of n bytes and returns the text
// released before Flush, the text with Flush, and the calls.
func feedBy(rules Rules, in string, n int) (early, all string, calls []Call) {
	s := NewScanner(rules)
	var b strings.Builder
	for i := 0; i < len(in); i += n {
		b.WriteString(s.Feed(in[i:min(i+n, len(in))]))
	}
	early = b.String()
	rest, calls := s.Flush()
	return early, early + rest, calls
}

// Fences are CommonMark's: a closer is at least as long as its opener, the same
// character, at the start of a line; an unclosed fence is code to the end.
func TestFencesFollowCommonMark(t *testing.T) {
	for name, in := range map[string]string{
		"longer opener, shorter closer": "Example:\n````\n```\n" + shellCall + "\n",
		"closer mid-line":               "```\nx ``` y\n" + shellCall,
		"unclosed":                      "```\ncode\n" + shellCall,
		"other character":               "~~~\n```\n" + shellCall,
		"closer with a word after it":   "```\n``` not a closer\n" + shellCall,
		"closer indented four":          "```\n    ```\n" + shellCall,
		"in a list item":                "- item\n  ```\n  " + shellCall + "\n",
	} {
		for _, n := range []int{1, 3, len(in)} {
			_, all, calls := feedBy(qwenRules(), in, n)
			if all != in || len(calls) != 0 {
				t.Errorf("%s (deltas of %d): text=%q calls=%d, want the text and no call", name, n, all, len(calls))
			}
		}
		if text, calls := Lift(in, qwenRules()); text != in || len(calls) != 0 {
			t.Errorf("%s: Lift text=%q calls=%d", name, text, len(calls))
		}
	}
	for name, in := range map[string]string{
		"closed":              "```\ncode\n```\n" + shellCall,
		"longer closer":       "```\ncode\n`````\n" + shellCall,
		"closer then blanks":  "~~~\ncode\n~~~  \n" + shellCall,
		"three-space closer":  "```\ncode\n   ```\n" + shellCall,
		"inline span, a line": "use ```x``` here\n" + shellCall,
		"three-space block":   "Run:\n   " + shellCall,
	} {
		for _, n := range []int{1, 3, len(in)} {
			if _, _, calls := feedBy(qwenRules(), in, n); len(calls) != 1 {
				t.Errorf("%s (deltas of %d): %d calls, want 1", name, n, len(calls))
			}
		}
	}
}

func TestIndentedCodeIsNotLifted(t *testing.T) {
	for _, in := range []string{"Run:\n\n    " + shellCall, "Run:\n\n\t" + shellCall, "Run:\n\n      " + shellCall + "\n"} {
		for _, n := range []int{1, 2, len(in)} {
			_, all, calls := feedBy(qwenRules(), in, n)
			if all != in || len(calls) != 0 {
				t.Errorf("%q (deltas of %d): text=%q calls=%d", in, n, all, len(calls))
			}
		}
	}
}

// A prose mention of an opener that has the opener's first byte after it is text,
// and the answer after it streams: it is not held to the end of the stream.
func TestProseAfterAnOpenerStreams(t *testing.T) {
	prose := strings.Repeat("The model writes its call and then the stack reads it, line by line. ", 400)
	for name, c := range map[string]struct {
		rules Rules
		in    string
	}{
		"json opener":   {qwenRules(), "Qwen writes <tool_call>{ for a call. " + prose},
		"json then key": {qwenRules(), "Qwen writes <tool_call>{\"name\": \"shell\"} and stops. " + prose},
		"fenced opener": {Rules{Offered: offered, Model: ds.Model}, "DeepSeek writes <｜tool▁call▁begin｜> for a call. " + prose},
	} {
		if len(c.in) < 25000 {
			t.Fatalf("%s: only %d bytes", name, len(c.in))
		}
		for _, n := range []int{1, 16, 100} {
			early, all, calls := feedBy(c.rules, c.in, n)
			if all != c.in || len(calls) != 0 {
				t.Fatalf("%s: text lost or called", name)
			}
			if len(early) < len(c.in)-512 {
				t.Errorf("%s (deltas of %d): %d of %d bytes before the end", name, n, len(early), len(c.in))
			}
		}
	}
}

// A real call, pretty-printed or not, still lifts at every split after the shape
// check is on.
func TestShapedCallsLiftAtEverySplit(t *testing.T) {
	for _, in := range []string{
		"x\n" + shellCall,
		"x\n<tool_call>\n{\n  \"name\": \"shell\",\n  \"arguments\": {\"cmd\": \"a\\n\\\"b\\\" \\u00e9\", \"n\": [1, -2.5e3, true, null, {}]}\n}\n</tool_call>\n",
		"x <tool_call>{\"name\":\"shell\",\"arguments\":\"{\\\"cmd\\\":\\\"ls\\\"}\"}</tool_call>",
	} {
		_, want := Lift(in, qwenRules())
		if len(want) != 1 {
			t.Fatalf("%q lifts %d", in, len(want))
		}
		for i := 0; i <= len(in); i++ {
			s := NewScanner(qwenRules())
			s.Feed(in[:i])
			s.Feed(in[i:])
			if _, calls := s.Flush(); len(calls) != 1 || calls[0].Arguments != want[0].Arguments {
				t.Fatalf("%q split at %d: calls=%+v", in, i, calls)
			}
		}
	}
}

// A body that names a member twice, or spells name or arguments in another case, is
// read one way by one parser and another by another: it is no call.
func TestAmbiguousJSONIsNotACall(t *testing.T) {
	for name, body := range map[string]string{
		"duplicate name":            `{"name":"nope","name":"shell","arguments":{}}`,
		"duplicate name, last real": `{"name":"shell","name":"nope","arguments":{}}`,
		"case-variant name":         `{"Name":"nope","name":"shell","arguments":{}}`,
		"only a case-variant name":  `{"NAME":"shell","arguments":{}}`,
		"duplicate arguments":       `{"name":"shell","arguments":{"cmd":"a"},"arguments":{"cmd":"b"}}`,
		"case-variant arguments":    `{"name":"shell","Arguments":{"cmd":"a"},"arguments":{"cmd":"b"}}`,
		"duplicate argument":        `{"name":"shell","arguments":{"cmd":"a","cmd":"b"}}`,
		"duplicate nested":          `{"name":"shell","arguments":{"o":{"k":1,"k":2}}}`,
		"duplicate in a string":     `{"name":"shell","arguments":"{\"cmd\":\"a\",\"cmd\":\"b\"}"}`,
		"trailing value":            `{"name":"shell","arguments":{}} {}`,
	} {
		in := "go\n<tool_call>" + body + "</tool_call>"
		if text, calls := Lift(in, qwenRules()); text != in || len(calls) != 0 {
			t.Errorf("%s: text=%q calls=%+v", name, text, calls)
		}
	}
	in := `<tool_call>{"name":"shell","arguments":{"cmd":"a","other":{"cmd":"b"}}}</tool_call>`
	if _, calls := Lift(in, qwenRules()); len(calls) != 1 || calls[0].Arguments != `{"cmd":"a","other":{"cmd":"b"}}` {
		t.Fatalf("a name used at two depths is not a duplicate: %+v", calls)
	}
}
