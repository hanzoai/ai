// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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

package controllers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanzoai/ai/model"
)

// chunkContent parses the content field out of a rewritten OpenAI SSE chunk.
func chunkContent(t *testing.T, raw string) string {
	t.Helper()
	var c struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatalf("rewritten chunk is not valid JSON: %v\n%s", err, raw)
	}
	var b strings.Builder
	for _, ch := range c.Choices {
		b.WriteString(ch.Delta.Content)
	}
	return b.String()
}

func TestApplyReasoningStripStreaming(t *testing.T) {
	s := &model.ReasoningStripper{}
	// A DeepSeek stream: reasoning, then </think>, then the answer — across chunks.
	deltas := []string{" Zebra", " stripe.", "</think>I am", " Zen5 Pro."}
	var out strings.Builder
	for _, d := range deltas {
		raw := `{"choices":[{"delta":{"content":` + mustJSON(d) + `}}]}`
		out.WriteString(chunkContent(t, applyReasoningStrip(raw, s)))
	}
	if got := out.String(); got != "I am Zen5 Pro." {
		t.Errorf("streamed content = %q, want %q", got, "I am Zen5 Pro.")
	}
}

func TestApplyReasoningStripFailSafe(t *testing.T) {
	s := &model.ReasoningStripper{}
	// Non-JSON and a role-only chunk pass through untouched.
	if got := applyReasoningStrip("not json", s); got != "not json" {
		t.Errorf("malformed chunk must pass through, got %q", got)
	}
	role := `{"choices":[{"delta":{"role":"assistant"}}]}`
	if got := applyReasoningStrip(role, s); got != role {
		t.Errorf("no-content chunk must pass through unchanged, got %q", got)
	}
}

func TestStripReasoningBodyNonStream(t *testing.T) {
	// The exact non-stream leak shape observed from zen5-pro.
	body := []byte(`{"choices":[{"message":{"role":"assistant","content":"</think>OK"}}],"usage":{"total_tokens":8}}`)
	out := stripReasoningBody(body)
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("rewritten body invalid: %v", err)
	}
	if resp.Choices[0].Message.Content != "OK" {
		t.Errorf("content = %q, want %q", resp.Choices[0].Message.Content, "OK")
	}
	if resp.Usage.TotalTokens != 8 {
		t.Errorf("usage must be preserved, got %d", resp.Usage.TotalTokens)
	}
	// Clean body is returned byte-identical.
	clean := []byte(`{"choices":[{"message":{"content":"hi"}}]}`)
	if got := stripReasoningBody(clean); string(got) != string(clean) {
		t.Errorf("clean body must be unchanged, got %s", got)
	}
}

func mustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestStreamCaptureUsageStripsReasoning is the end-to-end proof for the streaming
// tool path: a realistic DeepSeek SSE stream (reasoning inline in content, closed
// by </think>) is forwarded with clean content while billing still captures the
// ORIGINAL token text — exactly what Claude Code receives.
func TestStreamCaptureUsageStripsReasoning(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"choices":[{"delta":{"role":"assistant"}}]}`,
		``,
		`data: {"choices":[{"delta":{"content":" Zebra"}}]}`,
		``,
		`data: {"choices":[{"delta":{"content":" stripe.</think>I am"}}]}`,
		``,
		`data: {"choices":[{"delta":{"content":" Zen5 Pro."}}]}`,
		``,
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		``,
		`data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":8,"total_tokens":18}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	var out strings.Builder
	_, completion, total, completionText := streamCaptureUsage(
		strings.NewReader(sse), &out, nil, true, "req", "zen5-pro", &model.ReasoningStripper{},
	)

	// Billing sees the ORIGINAL content (reasoning + answer) and the usage chunk.
	if completion != 8 || total != 18 {
		t.Errorf("billing: completion=%d total=%d, want 8/18", completion, total)
	}
	if !strings.Contains(completionText, "</think>") {
		t.Errorf("billing text must retain the original (untouched) content, got %q", completionText)
	}

	// The client sees CLEAN content — no reasoning, no </think>.
	var forwarded strings.Builder
	for _, line := range strings.Split(out.String(), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		raw := strings.TrimPrefix(line, "data: ")
		if strings.TrimSpace(raw) == "[DONE]" {
			continue
		}
		forwarded.WriteString(chunkContent(t, raw))
	}
	if got := forwarded.String(); got != "I am Zen5 Pro." {
		t.Errorf("forwarded content = %q, want %q (clean, no </think>)", got, "I am Zen5 Pro.")
	}
	if strings.Contains(out.String(), "</think>") {
		t.Errorf("forwarded stream still contains </think>:\n%s", out.String())
	}
}
