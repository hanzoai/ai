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

// A JSON value that repeats an object name is read one way by one parser and
// another by another, in every form that carries JSON, not only the Hermes one.
func TestRepeatedNamesAreNoCallInAnyForm(t *testing.T) {
	for name, c := range map[string]struct {
		rules Rules
		in    string
	}{
		"coder": {Rules{Offered: offered, Model: "qwen/qwen3-coder"},
			"go\n<tool_call>\n<function=exec_command>\n<parameter=cmd>\n{\"a\":\"safe\",\"a\":\"rm -rf /\"}\n</parameter>\n</function>\n</tool_call>"},
		"dsml non-string": {ds,
			"go\n<｜DSML｜tool_calls><｜DSML｜invoke name=\"exec_command\"><｜DSML｜parameter name=\"a\" string=\"false\">{\"a\":1,\"a\":2}</｜DSML｜parameter></｜DSML｜invoke></｜DSML｜tool_calls>"},
		"fenced": {ds,
			"go\n<｜tool▁call▁begin｜>exec_command<｜tool▁sep｜>{\"cmd\":\"a\",\"cmd\":\"b\"}<｜tool▁call▁end｜>"},
		"fenced in a wrapper": {ds,
			"go\n<｜tool▁calls▁begin｜><｜tool▁call▁begin｜>function<｜tool▁sep｜>exec_command\n```json\n{\"o\":{\"k\":1,\"k\":2}}\n```<｜tool▁call▁end｜><｜tool▁calls▁end｜>"},
	} {
		for _, n := range []int{1, 7, len(c.in)} {
			_, all, calls := feedBy(c.rules, c.in, n)
			if all != c.in || len(calls) != 0 {
				t.Errorf("%s (deltas of %d): text=%q calls=%+v", name, n, all, calls)
			}
		}
		if text, calls := Lift(c.in, c.rules); text != c.in || len(calls) != 0 {
			t.Errorf("%s: Lift text=%q calls=%+v", name, text, calls)
		}
	}
	// The same shapes with distinct names still lift.
	for name, c := range map[string]struct {
		rules Rules
		in    string
	}{
		"coder": {Rules{Offered: offered, Model: "qwen/qwen3-coder"},
			"go\n<tool_call>\n<function=exec_command>\n<parameter=cmd>\n{\"a\":\"x\",\"b\":{\"a\":1}}\n</parameter>\n</function>\n</tool_call>"},
		"fenced": {ds, "go\n<｜tool▁call▁begin｜>exec_command<｜tool▁sep｜>{\"cmd\":\"a\"}<｜tool▁call▁end｜>"},
	} {
		if _, calls := Lift(c.in, c.rules); len(calls) != 1 {
			t.Errorf("%s: %d calls, want 1", name, len(calls))
		}
	}
}

// A line inside a blockquote is quoted, whatever its indent: a call written there
// is shown, not made.
func TestBlockquoteIsNeverLifted(t *testing.T) {
	for name, in := range map[string]string{
		"indented in a quote": "Example:\n\n>     " + shellCall,
		"quoted":              "The model said:\n\n> " + shellCall,
		"nested quote":        "Said:\n\n> > " + shellCall,
		"quote then list":     "Said:\n\n> - " + shellCall,
	} {
		for _, n := range []int{1, 3, len(in)} {
			_, all, calls := feedBy(qwenRules(), in, n)
			if all != in || len(calls) != 0 {
				t.Errorf("%s (deltas of %d): text=%q calls=%d", name, n, all, len(calls))
			}
		}
	}
	// The quote ends with its line: a call on the next line is the answer's own.
	for _, n := range []int{1, 3, 1000} {
		if _, _, calls := feedBy(qwenRules(), "> quoted\n\n"+shellCall, n); len(calls) != 1 {
			t.Errorf("deltas of %d: %d calls after the quote ended, want 1", n, len(calls))
		}
	}
}

// Prose after an opener that merely looked like one is let go inside the window,
// not held to the end of the answer.
func TestProseAfterACallOpenerStreams(t *testing.T) {
	prose := strings.Repeat("The model writes its call and then the stack reads it, line by line. ", 300)
	for name, c := range map[string]struct {
		rules Rules
		in    string
	}{
		"coder":     {qwenRules(), "Qwen writes <tool_call>\n<function= for a call. " + prose},
		"coder tag": {qwenRules(), "Qwen writes <tool_call>\n<function=a b c> for a call. " + prose},
		"dsml":      {ds, "DeepSeek writes <｜DSML｜tool_calls>\n<｜DSML｜invoke for a call. " + prose},
		"calls":     {ds, "DeepSeek writes <｜tool▁calls▁begin｜><｜tool▁call▁begin｜> for a call. " + prose},
	} {
		if len(c.in) < 20000 {
			t.Fatalf("%s: only %d bytes", name, len(c.in))
		}
		for _, n := range []int{1, 16, 100} {
			early, all, calls := feedBy(c.rules, c.in, n)
			if all != c.in || len(calls) != 0 {
				t.Fatalf("%s: text lost or called", name)
			}
			if len(early) < len(c.in)-1024 {
				t.Errorf("%s (deltas of %d): %d of %d bytes before the end", name, n, len(early), len(c.in))
			}
		}
	}
}
