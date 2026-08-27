// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controllers

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// collect drives the writer with SSE frames and returns what a streaming client
// would assemble from the deltas: the answer, and the reasoning beside it.
func collect(t *testing.T, frames ...string) (content, reasoning string) {
	t.Helper()
	var out bytes.Buffer
	w := &OpenAIWriter{out: &out, RequestID: "req", Model: "glm-5.1", Stream: true}
	for _, f := range frames {
		if _, err := w.Write([]byte(f)); err != nil {
			t.Fatalf("write %q: %v", f, err)
		}
	}
	for _, line := range strings.Split(out.String(), "\n") {
		raw, ok := strings.CutPrefix(line, "data: ")
		if !ok || strings.TrimSpace(raw) == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(raw), &chunk) != nil {
			continue
		}
		for _, c := range chunk.Choices {
			content += c.Delta.Content
			reasoning += c.Delta.ReasoningContent
		}
	}
	return content, reasoning
}

// TestReasoningStreamsAsAFieldNotAsTheAnswer is the bug, exactly as measured
// against GLM-5.1: the upstream separates content from reasoning perfectly, and
// a caller reading the model NON-streaming got "ok" while the same model
// STREAMING got the whole chain-of-thought followed by "ok". Reason frames fell
// through into the content delta.
func TestReasoningStreamsAsAFieldNotAsTheAnswer(t *testing.T) {
	content, reasoning := collect(t,
		"event: reason\ndata: 1. Analyze the request.\n\n",
		"event: reason\ndata:  2. It wants exactly \"ok\".\n\n",
		"event: message\ndata: ok\n\n",
	)
	if content != "ok" {
		t.Fatalf("content = %q, want %q — reasoning leaked into the answer", content, "ok")
	}
	if !strings.Contains(reasoning, "Analyze the request") {
		t.Fatalf("reasoning = %q; it must still reach a client that renders it", reasoning)
	}
}

// TestMessageStringExcludesReasoning pins the half that was already right, so a
// fix to the stream cannot quietly break the buffered answer.
func TestMessageStringExcludesReasoning(t *testing.T) {
	var out bytes.Buffer
	w := &OpenAIWriter{out: &out, RequestID: "req", Model: "glm-5.1"}
	_, _ = w.Write([]byte("event: reason\ndata: thinking out loud\n\n"))
	_, _ = w.Write([]byte("event: message\ndata: ok\n\n"))
	if got := w.MessageString(); got != "ok" {
		t.Fatalf("MessageString = %q, want %q", got, "ok")
	}
}
