package controllers

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

const (
	answerTools = `{"model":"zen4","tools":[{"type":"function","function":{"name":"exec_command","parameters":{"type":"object"}}}],"messages":[{"role":"user","content":"hi"}]}`
	trailing    = "Running it.\n<tool_call>{\"name\":\"exec_command\",\"arguments\":{\"cmd\":\"ls\"}}</tool_call>"
)

func sse(content, fin string) string {
	c := strings.NewReplacer(`"`, `\"`, "\n", `\n`).Replace(content)
	return `data: {"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"` + c + `"},"finish_reason":null}]}` + "\n\n" +
		`data: {"object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"` + fin + `"}]}` + "\n\n" + "data: [DONE]\n\n"
}

func answerStreamed(t *testing.T, body, in string) string {
	t.Helper()
	var out bytes.Buffer
	w := ReadAnswer([]byte(body), "zen4", false).Stream(&out)
	for i := 0; i < len(in); i += 7 { // split across writes
		if _, err := w.Write([]byte(in[i:min(i+7, len(in))])); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestAnsweringStreamLiftsTrailingBlock(t *testing.T) {
	out := answerStreamed(t, answerTools, sse(trailing, "stop"))
	if !strings.Contains(out, `"tool_calls"`) || !strings.Contains(out, "exec_command") || strings.Contains(out, "<tool_call>") {
		t.Fatalf("not lifted: %s", out)
	}
	if !strings.Contains(out, "Running it.") {
		t.Fatalf("text lost: %s", out)
	}
}

func TestAnsweringStreamKeepsQuotedAndPlain(t *testing.T) {
	for name, content := range map[string]string{
		"fenced": "Example:\n```\n" + trailing + "\n```",
		"plain":  "Just an answer.",
	} {
		in := sse(content, "stop")
		out := answerStreamed(t, answerTools, in)
		if strings.Contains(out, `"tool_calls"`) {
			t.Errorf("%s lifted: %s", name, out)
		}
		if got, want := heardText(out), content; got != want {
			t.Errorf("%s text %q != %q", name, got, want)
		}
	}
}

func TestAnsweringWholeAndGates(t *testing.T) {
	whole := `{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":` + jsonString(trailing) + `},"finish_reason":"stop"}]}`
	if out := string(ReadAnswer([]byte(answerTools), "zen4", false).Whole([]byte(whole))); !strings.Contains(out, `"tool_calls"`) || strings.Contains(out, "<tool_call>") {
		t.Errorf("whole not lifted: %s", out)
	}
	plain := `{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`
	if out := string(ReadAnswer([]byte(answerTools), "zen4", false).Whole([]byte(plain))); out != plain {
		t.Errorf("plain changed: %s", out)
	}
	if ReadAnswer([]byte(answerTools), "zen4", true) != nil {
		t.Error("strict lifted")
	}
	if ReadAnswer([]byte(`{"model":"zen4","messages":[]}`), "zen4", false) != nil {
		t.Error("no tools lifted")
	}
	quoted := strings.Replace(answerTools, `"content":"hi"`, `"content":"echo <tool_call>"`, 1)
	if ReadAnswer([]byte(quoted), "zen4", false) != nil {
		t.Error("request carrying an opener lifted")
	}
}

func jsonString(s string) string { b, _ := json.Marshal(s); return string(b) }

// heardText is the content a chat SSE stream's deltas carry, joined.
func heardText(stream string) string {
	var out strings.Builder
	for _, line := range strings.Split(stream, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var c struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &c) == nil {
			for _, ch := range c.Choices {
				out.WriteString(ch.Delta.Content)
			}
		}
	}
	return out.String()
}
