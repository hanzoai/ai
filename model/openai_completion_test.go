// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package model

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/ai/proxy"
)

// A completion (non-chat) model streams its answer to the writer. The loop used
// to assert its flush to the answer-only shape while holding the think one, so
// every completion model panicked on its first piece. A chunk with no choice
// (usage arrives on one) is skipped rather than indexed.
func TestACompletionModelStreamsItsAnswer(t *testing.T) {
	stream := []string{
		`{"id":"c","object":"text_completion","created":1,"model":"davinci","choices":[{"index":0,"text":"Hello","finish_reason":""}]}`,
		`{"id":"c","object":"text_completion","created":1,"model":"davinci","choices":[]}`,
		`{"id":"c","object":"text_completion","created":1,"model":"davinci","choices":[{"index":0,"text":" world","finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
		`[DONE]`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/completions") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, data := range stream {
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
	}))
	t.Cleanup(srv.Close)

	previous := proxy.ProxyHttpClient
	proxy.ProxyHttpClient = srv.Client()
	t.Cleanup(func() { proxy.ProxyHttpClient = previous })

	p, err := NewOpenAiModelProvider("davinci", "k", srv.URL, 0, 1, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := getOpenAiModelType("davinci"); got != "Completion" {
		t.Fatalf("davinci is a %q model; this test needs a completion one", got)
	}

	w := httptest.NewRecorder()
	result, err := func() (result *ModelResult, err error) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("QueryText panicked: %v", r)
			}
		}()
		return p.QueryText("say hello", w, nil, "", nil, nil, "en")
	}()
	if err != nil {
		t.Fatal(err)
	}

	body := w.Body.String()
	for _, piece := range []string{"event: message\ndata: Hello\n\n", "event: message\ndata:  world\n\n"} {
		if !strings.Contains(body, piece) {
			t.Errorf("stream %q does not carry %q", body, piece)
		}
	}
	if result.PromptTokenCount != 3 || result.ResponseTokenCount != 2 || result.TotalTokenCount != 5 {
		t.Errorf("counts = %d/%d/%d, want 3/2/5 from the upstream's usage",
			result.PromptTokenCount, result.ResponseTokenCount, result.TotalTokenCount)
	}
}
