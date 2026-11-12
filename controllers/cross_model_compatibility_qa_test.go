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

package controllers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hanzoai/ai/model"
)

// TestCrossModelCompatibility_AnthropicToOpenAITranslationParity verifies that
// identical semantic conversations submitted in Anthropic (/v1/messages) format
// translate cleanly to OpenAI (/v1/chat/completions) messages and yield consistent
// positive token counts across model families (#138).
func TestCrossModelCompatibility_AnthropicToOpenAITranslationParity(t *testing.T) {
	families := []string{
		"zen-4",
		"enso-pro",
		"claude-3-5-sonnet",
		"gpt-4o",
		"gemini-2.5-pro",
		"qwen-max",
		"deepseek-r1",
	}

	for _, famModel := range families {
		t.Run(famModel, func(t *testing.T) {
			req := &AnthropicRequest{
				Model:     famModel,
				MaxTokens: 256,
				System:    json.RawMessage(`"You are a concise assistant."`),
				Messages: []AnthropicMessage{
					{Role: "user", Content: json.RawMessage(`"Explain deterministic JSON schema validation in one sentence."`)},
					{Role: "assistant", Content: json.RawMessage(`"Deterministic JSON schema validation enforces structural and type invariants on every output payload."`)},
					{Role: "user", Content: json.RawMessage(`[{"type":"text","text":"Now format that as a key-value summary."}]`)},
				},
			}
			msgs := anthropicToOpenAIMessages(req)
			if len(msgs) < 3 {
				t.Fatalf("model %s: expected at least 3 translated messages, got %d", famModel, len(msgs))
			}
			n, err := model.OpenaiNumTokensFromMessages(msgs, famModel)
			if err == nil && n <= 0 {
				t.Errorf("model %s: expected positive token count, got %d", famModel, n)
			}
		})
	}
}

// TestCrossModelCompatibility_ReasoningDepthDialectNormalization verifies that
// OpenAI reasoning_effort, nested reasoning.effort, and Anthropic thinking.budget_tokens
// normalize to the exact same internal depth tiers across all model families (#138).
func TestCrossModelCompatibility_ReasoningDepthDialectNormalization(t *testing.T) {
	pairs := []struct {
		name         string
		openaiJSON   string
		anthropicJSON string
		wantTier     string
	}{
		{
			"disabled_or_minimal",
			`{"model":"zen-4","reasoning_effort":"minimal"}`,
			`{"model":"claude-3-7-sonnet","thinking":{"type":"disabled"}}`,
			"none",
		},
		{
			"low_effort",
			`{"model":"enso-flash","reasoning_effort":"low"}`,
			`{"model":"claude-3-7-sonnet","thinking":{"type":"enabled","budget_tokens":1024}}`,
			"low",
		},
		{
			"medium_effort",
			`{"model":"enso-pro","reasoning":{"effort":"medium"}}`,
			`{"model":"claude-3-7-sonnet","thinking":{"type":"enabled","budget_tokens":8192}}`,
			"medium",
		},
		{
			"max_effort",
			`{"model":"enso-ultra","reasoning_effort":"xhigh"}`,
			`{"model":"claude-3-7-sonnet","thinking":{"type":"enabled","budget_tokens":32000}}`,
			"max",
		},
	}

	for _, p := range pairs {
		t.Run(p.name, func(t *testing.T) {
			gotOpenAI := depthOf([]byte(p.openaiJSON))
			gotAnthropic := depthOf([]byte(p.anthropicJSON))
			if gotOpenAI != p.wantTier || gotAnthropic != p.wantTier {
				t.Errorf("dialect mismatch: OpenAI=%q, Anthropic=%q, want both=%q", gotOpenAI, gotAnthropic, p.wantTier)
			}
		})
	}
}

// TestCrossModelCompatibility_ErrorEnvelopeConsistency verifies that authentication
// and permission errors return deterministic error envelopes across both Gateway
// and Cloud ZAP transports (#138).
func TestCrossModelCompatibility_ErrorEnvelopeConsistency(t *testing.T) {
	gwMsg, err := zapAnthropicMessagesGateway(context.Background(), "", []byte(`{"model":"enso-pro"}`))
	if err != nil {
		t.Fatalf("gateway error: %v", err)
	}
	gwStatus, gwBody := antDecodeGateway(t, gwMsg)
	if gwStatus != 401 {
		t.Errorf("gateway status = %d, want 401", gwStatus)
	}
	assertAnthropicError(t, gwBody, "authentication_error")

	cloudMsg, err := zapAnthropicMessagesCloud(context.Background(), "", []byte(`{"model":"enso-pro"}`))
	if err != nil {
		t.Fatalf("cloud error: %v", err)
	}
	clStatus, clBody, _ := antDecodeCloud(t, cloudMsg)
	if clStatus != 401 {
		t.Errorf("cloud status = %d, want 401", clStatus)
	}
	assertAnthropicError(t, clBody, "authentication_error")
}
