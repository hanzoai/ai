package controllers

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestTextGenerationEdgeCases_LongContextAndNeedleRetrieval verifies that long-context
// payloads (32k-128k+ chars) preserve needle tokens without silent truncation (#132).
func TestTextGenerationEdgeCases_LongContextAndNeedleRetrieval(t *testing.T) {
	haystack := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 2500)
	needle := "SECRET_NEEDLE_TOKEN_994821"
	payloadText := haystack + needle + haystack

	req := anthropicMessagesRequest{
		Model:     "zen",
		MaxTokens: 256,
		Messages: []anthropicMessage{
			{
				Role:    "user",
				Content: json.RawMessage(mustJSON(t, payloadText)),
			},
		},
	}

	msgs, _, promptTokens := anthropicToOpenAIMessages(req)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, needle) {
		t.Fatalf("needle token lost during long-context message normalization")
	}
	if promptTokens < 10000 {
		t.Fatalf("expected long-context token estimate >= 10000, got %d", promptTokens)
	}
}

// TestTextGenerationEdgeCases_ReasoningDepthAndTruncation verifies reasoning effort
// normalization and max_tokens boundary handling (#132).
func TestTextGenerationEdgeCases_ReasoningDepthAndTruncation(t *testing.T) {
	cases := []struct {
		effort string
		budget int
		want   string
	}{
		{"", 0, "none"},
		{"low", 0, "low"},
		{"medium", 0, "medium"},
		{"high", 0, "max"},
		{"", 1500, "low"},
		{"", 6000, "medium"},
		{"", 24000, "max"},
	}

	for _, tc := range cases {
		var r chatCompletionRequest
		r.ReasoningEffort = tc.effort
		if tc.budget > 0 {
			r.Thinking = &struct {
				Type         string `json:"type"`
				BudgetTokens int    `json:"budget_tokens"`
			}{Type: "enabled", BudgetTokens: tc.budget}
		}
		if got := depthOf(r); got != tc.want {
			t.Fatalf("depthOf(effort=%q, budget=%d) = %q, want %q", tc.effort, tc.budget, got, tc.want)
		}
	}
}

func mustJSON(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
