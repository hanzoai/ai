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

const sample = "3. Starting with the receipts practical test now.\n\n" +
	"<｜DSML｜tool_calls>\n" +
	"<｜DSML｜invoke name=\"exec_command\">\n" +
	"<｜DSML｜parameter name=\"cmd\" string=\"true\">cd ~/Code/x && npm test -- tests/a.test.js</｜DSML｜parameter>\n" +
	"<｜DSML｜parameter name=\"max_output_tokens\" number=\"true\">5000</｜DSML｜parameter>\n" +
	"</｜DSML｜invoke>\n" +
	"</｜DSML｜tool_calls>"

const (
	sampleText = "3. Starting with the receipts practical test now."
	sampleArgs = `{"cmd":"cd ~/Code/x && npm test -- tests/a.test.js","max_output_tokens":5000}`
)

var offered = map[string]bool{"exec_command": true, "read_file": true}

var (
	ds = Rules{Offered: offered, Model: "deepseek/deepseek-v4"}
	qw = Rules{Offered: offered, Model: "qwen/qwen3-coder"}
)

func TestLiftDSML(t *testing.T) {
	text, calls := Lift(sample, ds)
	if text != sampleText {
		t.Fatalf("text = %q", text)
	}
	if len(calls) != 1 || calls[0].Name != "exec_command" || calls[0].Arguments != sampleArgs || calls[0].ID == "" {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestLiftDSMLTypes(t *testing.T) {
	in := `<｜DSML｜tool_calls><｜DSML｜invoke name="read_file">` +
		`<｜DSML｜parameter name="a" string="true">123</｜DSML｜parameter>` +
		`<｜DSML｜parameter name="b" string="false">{"k": [1, 2]}</｜DSML｜parameter>` +
		`<｜DSML｜parameter name="c" boolean="true">true</｜DSML｜parameter>` +
		`<｜DSML｜parameter name="d" string="false">not json</｜DSML｜parameter>` +
		`<｜DSML｜parameter name="e" string="true">line1
"line2"</｜DSML｜parameter>` +
		`</｜DSML｜invoke></｜DSML｜tool_calls>`
	text, calls := Lift(in, ds)
	want := `{"a":"123","b":{"k":[1,2]},"c":true,"d":"not json","e":"line1\n\"line2\""}`
	if text != "" || len(calls) != 1 || calls[0].Arguments != want {
		t.Fatalf("text=%q calls=%+v", text, calls)
	}
}

func TestLiftMultipleInvokes(t *testing.T) {
	in := `<｜DSML｜tool_calls>` +
		`<｜DSML｜invoke name="read_file"><｜DSML｜parameter name="p" string="true">a</｜DSML｜parameter></｜DSML｜invoke>` +
		`<｜DSML｜invoke name="exec_command"></｜DSML｜invoke>` +
		`</｜DSML｜tool_calls>`
	_, calls := Lift(in, ds)
	if len(calls) != 2 || calls[0].Name != "read_file" || calls[0].Arguments != `{"p":"a"}` ||
		calls[1].Name != "exec_command" || calls[1].Arguments != `{}` || calls[0].ID == calls[1].ID {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestLiftUnknownToolStaysText(t *testing.T) {
	in := "ok\n\n" + strings.ReplaceAll(sample, "exec_command", "rm_rf")
	in = strings.Replace(in, sampleText, "ok", 1)
	text, calls := Lift(in, ds)
	if text != in || len(calls) != 0 {
		t.Fatalf("text=%q calls=%+v", text, calls)
	}
	// One unknown invoke keeps the whole block: nothing is half lifted.
	mixed := `<｜DSML｜tool_calls><｜DSML｜invoke name="read_file"></｜DSML｜invoke><｜DSML｜invoke name="nope"></｜DSML｜invoke></｜DSML｜tool_calls>`
	if text, calls := Lift(mixed, ds); text != mixed || len(calls) != 0 {
		t.Fatalf("mixed: text=%q calls=%+v", text, calls)
	}
}

func TestLiftTruncatedStaysText(t *testing.T) {
	in := sample[:len(sample)-len("</｜DSML｜tool_calls>")]
	text, calls := Lift(in, ds)
	if text != in || len(calls) != 0 {
		t.Fatalf("text=%q calls=%+v", text, calls)
	}
}

func TestLiftMalformedStaysText(t *testing.T) {
	in := `<｜DSML｜tool_calls><｜DSML｜invoke><｜DSML｜parameter name="p">x</｜DSML｜parameter></｜DSML｜invoke></｜DSML｜tool_calls>`
	if text, calls := Lift(in, ds); text != in || len(calls) != 0 {
		t.Fatalf("text=%q calls=%+v", text, calls)
	}
}

func TestLiftPlainTextUntouched(t *testing.T) {
	for _, in := range []string{"", "  hello  \n", "a < b and <div> and <tool", "x <｜ y"} {
		if text, calls := Lift(in, ds); text != in || len(calls) != 0 {
			t.Fatalf("%q -> %q %+v", in, text, calls)
		}
	}
}

func TestLiftNoToolsOffered(t *testing.T) {
	if text, calls := Lift(sample, Rules{Model: ds.Model}); text != sample || len(calls) != 0 {
		t.Fatalf("text=%q calls=%+v", text, calls)
	}
}

func TestLiftQwen(t *testing.T) {
	in := "Checking.\n<tool_call>\n{\"name\": \"read_file\", \"arguments\": {\"path\": \"/a b\"}}\n</tool_call>\n" +
		"<tool_call>{\"name\":\"exec_command\",\"arguments\":\"{\\\"cmd\\\":\\\"ls\\\"}\"}</tool_call>"
	text, calls := Lift(in, qw)
	if text != "Checking." || len(calls) != 2 ||
		calls[0].Name != "read_file" || calls[0].Arguments != `{"path":"/a b"}` ||
		calls[1].Name != "exec_command" || calls[1].Arguments != `{"cmd":"ls"}` {
		t.Fatalf("text=%q calls=%+v", text, calls)
	}
}

func TestLiftFenced(t *testing.T) {
	in := "Sure.<｜tool▁calls▁begin｜><｜tool▁call▁begin｜>function<｜tool▁sep｜>read_file\n```json\n{\"path\": \"x\"}\n```<｜tool▁call▁end｜>" +
		"<｜tool▁call▁begin｜>exec_command<｜tool▁sep｜>{\"cmd\":\"ls\"}<｜tool▁call▁end｜><｜tool▁calls▁end｜>"
	text, calls := Lift(in, ds)
	if text != "Sure." || len(calls) != 2 ||
		calls[0].Name != "read_file" || calls[0].Arguments != `{"path":"x"}` ||
		calls[1].Name != "exec_command" || calls[1].Arguments != `{"cmd":"ls"}` {
		t.Fatalf("text=%q calls=%+v", text, calls)
	}
}

// Every split of the input in two, and the input one byte at a time, lift the same
// calls and the same text as the whole, and the text never carries a marker byte.
func TestScannerEverySplit(t *testing.T) {
	inputs := map[string]Rules{
		sample:                               ds,
		"before\n" + sample + "\nafter text": ds,
		"a <b> c " + sample + " tail <｜ not markup": ds,
		"a <b> c " + sample + " \n":                 ds,
		"<tool_call>{\"name\":\"read_file\",\"arguments\":{}}</tool_call> then <tool_call>{\"name\":\"nope\"}</tool_call> end": qw,
		"unterminated " + sample[:len(sample)-5]: ds,
	}
	for in, rules := range inputs {
		wantText, wantCalls := Lift(in, rules)
		check := func(parts []string) {
			s := NewScanner(rules)
			var text strings.Builder
			for _, p := range parts {
				x := s.Feed(p)
				text.WriteString(x)
				if len(wantCalls) > 0 && strings.Contains(x, "｜DSML｜tool_calls>") {
					t.Fatalf("markup leaked in %q", x)
				}
			}
			rest, calls := s.Flush()
			text.WriteString(rest)
			if text.String() != wantText || len(calls) != len(wantCalls) {
				t.Fatalf("parts %q: text=%q want %q calls=%d want %d", parts, text.String(), wantText, len(calls), len(wantCalls))
			}
			for i := range calls {
				if calls[i].Name != wantCalls[i].Name || calls[i].Arguments != wantCalls[i].Arguments {
					t.Fatalf("call %d = %+v want %+v", i, calls[i], wantCalls[i])
				}
			}
			if strings.Contains(text.String(), "｜DSML｜") != strings.Contains(wantText, "｜DSML｜") {
				t.Fatalf("markup in text: %q", text.String())
			}
		}
		for i := 0; i <= len(in); i++ {
			check([]string{in[:i], in[i:]})
		}
		var bytes []string
		for i := 0; i < len(in); i++ {
			bytes = append(bytes, in[i:i+1])
		}
		check(bytes)
	}
}

// Text before a block streams out at once; only a possible marker is held.
func TestScannerPassesTextThrough(t *testing.T) {
	s := NewScanner(ds)
	text := s.Feed("hello world <｜DS")
	if text != "hello world" || !s.Held() {
		t.Fatalf("text=%q held=%v", text, s.Held())
	}
	text = s.Feed("x")
	if text != " <｜DSx" || s.Held() {
		t.Fatalf("text=%q", text)
	}
}
