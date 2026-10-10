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
	tk, _, _, _ := assembleZenStream(w, pr, nil, 20*time.Millisecond, nil, nil)
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

// A /v1/responses request asked whole, served by a family asked for a stream, is
// answered with ONE Responses object holding the text and the usage, under
// application/json — never the event stream. Through the stream's decoration the
// assembled completion was read as events, none were found, and the caller got
// response.created and response.completed with an empty output.
func TestAWholeResponsesAnswerFromAFamilyStreamIsOneResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"g\",\"choices\":[{\"delta\":{\"content\":\"ready\"}}]}\n\ndata: {\"id\":\"g\",\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	fam := otherFamily(t, srv.URL)
	call, err := ReadResponses([]byte(`{"model":"enso","input":"hi"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	c := visit(http.MethodPost, "/v1/responses")
	c.answer = c.responsesAnswer(call)
	c.Fiber().Request().SetBody(call.Chat)
	if out := c.pipeToFamily(fam, "chat/completions", "openai", "enso", call.Chat, false, 0, "acme", nil, false, nil, time.Now()); out != nil {
		t.Fatalf("attempts %+v", out)
	}
	if ct := string(c.Fiber().Response().Header.ContentType()); ct != "application/json" {
		t.Errorf("content-type %q, want application/json", ct)
	}
	var s stream
	s.w = bufio.NewWriter(&s.buf)
	if err := c.Fiber().Response().BodyWriteTo(s.w); err != nil {
		t.Fatal(err)
	}
	raw := s.String()
	if strings.Contains(raw, "event:") || strings.Contains(raw, "chat.completion") {
		t.Fatalf("a whole Responses answer came as events or as chat:\n%s", raw)
	}
	var got struct {
		Object string `json:"object"`
		Status string `json:"status"`
		Output []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(bytes.TrimSpace([]byte(raw)), &got); err != nil {
		t.Fatalf("not one JSON object: %v\n%s", err, raw)
	}
	if got.Object != "response" || got.Status != "completed" || len(got.Output) != 1 || len(got.Output[0].Content) != 1 || got.Output[0].Content[0].Text != "ready" {
		t.Fatalf("answer %+v\n%s", got, raw)
	}
	if got.Usage.InputTokens != 2 || got.Usage.OutputTokens != 1 {
		t.Errorf("usage %+v, want 2 in and 1 out", got.Usage)
	}
}

// A stream asked whole that breaks off before it states its usage — cut, or out of
// its time — bills what it said, as the relay of a stream does: its text, its
// reasoning and its tool arguments.
func TestAnAssembledStreamCutBeforeItsUsageBillsWhatItSaid(t *testing.T) {
	body := "data: " + `{"id":"gen-1","choices":[{"delta":{"content":"0123456789abcdef"}}]}` + "\n\n" +
		"data: " + `{"id":"gen-1","choices":[{"delta":{"reasoning":"01234567"}}]}` + "\n\n" +
		"data: " + `{"id":"gen-1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"shell","arguments":"{\"cmd\":1}"}}]}}]}` + "\n\n"
	var out bytes.Buffer
	w := bufio.NewWriter(&out)
	tk, _, _, _ := assembleZenStream(w, io.NopCloser(strings.NewReader(body)), nil, time.Hour, nil, nil)
	said := 16 + 8 + len(`{"cmd":1}`)
	if tk.completion != said/4+1 || tk.reported {
		t.Errorf("tokens = %+v, want the %d characters said billed as %d", tk, said, said/4+1)
	}
	// One that states its usage is billed as it states it.
	tk, _, _, _ = assembleZenStream(bufio.NewWriter(io.Discard), io.NopCloser(strings.NewReader(body+"data: "+`{"id":"gen-1","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":3}}`+"\n\n")), nil, time.Hour, nil, nil)
	if tk.completion != 3 {
		t.Errorf("a stated usage was replaced: %+v", tk)
	}
}
