// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package controllers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/go-openai"
)

// TestResponsesAnswersInResponsesOnEveryPath drives /v1/responses through the tool
// relay — the path every request carrying tools takes, which is every request
// Hanzo Dev makes. That relay opens its own stream, so before the answer shape
// was applied at the write it sent chat.completion.chunk to a Responses client,
// which discarded every chunk and failed on "stream closed before
// response.completed".
func TestResponsesAnswersInResponsesOnEveryPath(t *testing.T) {
	for _, mode := range []struct {
		name   string
		stream bool
		answer string
		want   string
	}{
		{"buffered", false, upstreamBody, `"object":"response"`},
		{"streamed", true, upstreamStream, "event: response.completed"},
	} {
		t.Run(mode.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(mode.answer))
			}))
			defer upstream.Close()

			c := visit("POST", "/v1/responses")
			c.answer = c.responsesAnswer(&ResponsesCall{req: OpenAIResponsesRequest{Model: sku, Stream: mode.stream}})

			request := openai.ChatCompletionRequest{Model: sku, Stream: mode.stream}
			provider := &object.Provider{
				Owner: "admin", Name: "shared", Type: "OpenAI",
				SubType: "qwen/qwen3-235b-a22b", ProviderUrl: upstream.URL,
			}
			c.proxyToolRequest(provider, &request, time.Now(), nil, false, "", nil)

			out := sent(c)
			if !strings.Contains(out, mode.want) {
				t.Fatalf("%s: no %s in the answer:\n%s", mode.name, mode.want, out)
			}
			if strings.Contains(out, "chat.completion") {
				t.Errorf("%s: a chat completion reached a Responses client:\n%s", mode.name, out)
			}
			if !strings.Contains(out, "2 + 2 = 4") {
				t.Errorf("%s: the answer's text did not survive the translation:\n%s", mode.name, out)
			}
		})
	}
}
