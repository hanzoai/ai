// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A family's chat stream becomes one chat completion: content, reasoning, tool
// calls merged by index, finish reason and usage, with a space written every beat
// until it is whole.
func TestAStreamIsAssembledIntoOneAnswer(t *testing.T) {
	pr, pw := io.Pipe()
	go func() {
		frames := []string{
			`{"id":"gen-1","created":7,"model":"arm","choices":[{"delta":{"role":"assistant","reasoning_content":"think "}}]}`,
			`{"id":"gen-1","choices":[{"delta":{"content":"Hel"}}]}`,
			`{"id":"gen-1","choices":[{"delta":{"content":"lo","tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"shell","arguments":"{\"command\":"}}]}}]}`,
			`{"id":"gen-1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"[\"ls\"]}"}}]},"finish_reason":"tool_calls"}]}`,
			`{"id":"gen-1","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":3}}`,
		}
		for i, f := range frames {
			if i == 1 {
				time.Sleep(60 * time.Millisecond)
			}
			_, _ = pw.Write([]byte("data: " + f + "\n\n"))
		}
		_, _ = pw.Write([]byte("data: [DONE]\n\n"))
		_ = pw.Close()
	}()
	var out bytes.Buffer
	w := bufio.NewWriter(&out)
	tk, _, _, _ := assembleZenStream(w, pr, nil, 20*time.Millisecond)
	raw := out.String()
	if !strings.HasPrefix(raw, " ") {
		t.Errorf("no heartbeat before the answer: %q", raw[:min(20, len(raw))])
	}
	var got struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				ToolCalls        []struct {
					ID       string `json:"id"`
					Function struct{ Name, Arguments string }
				} `json:"tool_calls"`
			} `json:"message"`
			Finish string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &got); err != nil {
		t.Fatalf("not one JSON answer: %v\n%s", err, raw)
	}
	m := got.Choices[0].Message
	if got.Object != "chat.completion" || m.Content != "Hello" || m.ReasoningContent != "think " || got.Choices[0].Finish != "tool_calls" {
		t.Fatalf("assembled %+v", got)
	}
	if len(m.ToolCalls) != 1 || m.ToolCalls[0].ID != "c1" || m.ToolCalls[0].Function.Arguments != `{"command":["ls"]}` {
		t.Fatalf("tool calls %+v", m.ToolCalls)
	}
	if tk.fresh+tk.cached != 5 || tk.completion != 3 {
		t.Fatalf("usage %+v", tk)
	}
}

// A caller who asked for a whole answer is answered whole, from a family asked for
// a stream.
func TestAWholeAnswerIsAskedOfTheFamilyAsAStream(t *testing.T) {
	var asked []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"g\",\"choices\":[{\"delta\":{\"content\":\"ready\"}}]}\n\ndata: {\"id\":\"g\",\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	fam := otherFamily(t, srv.URL)
	body := []byte(`{"model":"enso","messages":[{"role":"user","content":"hi"}]}`)
	c := visit(http.MethodPost, "/v1/chat/completions")
	c.Fiber().Request().SetBody(body)
	if out := c.pipeToFamily(fam, "chat/completions", "openai", "enso", body, false, 0, "acme", nil, false, nil, time.Now()); out != nil {
		t.Fatalf("attempts %+v", out)
	}
	if !bytes.Contains(asked, []byte(`"stream":true`)) {
		t.Fatalf("the family was not asked for a stream: %s", asked)
	}
	var s stream
	s.w = bufio.NewWriter(&s.buf)
	if err := c.Fiber().Response().BodyWriteTo(s.w); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(bytes.TrimSpace([]byte(s.String())), &got); err != nil || got["object"] != "chat.completion" {
		t.Fatalf("answer %v %s", err, s.String())
	}
}
