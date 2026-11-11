package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ModelFamilies lists the canonical model families verified for cross-model API compatibility.
var ModelFamilies = []string{
	"zen",
	"enso",
	"kai",
	"claude",
	"gpt",
	"gemini",
	"qwen",
	"deepseek",
	"llama",
}

// StandardErrorEnvelope represents the normalized OpenAI/Hanzo API error schema.
type StandardErrorEnvelope struct {
	Error struct {
		Message string  `json:"message"`
		Type    string  `json:"type"`
		Param   *string `json:"param"`
		Code    string  `json:"code"`
	} `json:"error"`
}

// ValidateSSEStream verifies that a sequence of raw SSE lines adheres strictly to
// `data: {...}` JSON chunks with `delta.content` or `delta.tool_calls`, a terminal
// `finish_reason`, and a closing `data: [DONE]` sentinel.
func ValidateSSEStream(lines []string) error {
	if len(lines) == 0 {
		return errors.New("empty_sse_stream")
	}
	sawDone := false
	sawFinishReason := false
	sawDelta := false

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			return fmt.Errorf("non_standard_sse_prefix: %s", line)
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if payload == "[DONE]" {
			sawDone = true
			break
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return fmt.Errorf("invalid_sse_json_chunk: %w", err)
		}
		choices, ok := chunk["choices"].([]any)
		if !ok || len(choices) == 0 {
			return errors.New("sse_chunk_missing_choices")
		}
		firstChoice, _ := choices[0].(map[string]any)
		if fr, ok := firstChoice["finish_reason"].(string); ok && fr != "" {
			sawFinishReason = true
		}
		if delta, ok := firstChoice["delta"].(map[string]any); ok {
			if _, hasContent := delta["content"]; hasContent {
				sawDelta = true
			}
			if _, hasTools := delta["tool_calls"]; hasTools {
				sawDelta = true
			}
		}
	}

	if !sawDelta {
		return errors.New("sse_stream_missing_delta_content_or_tool_calls")
	}
	if !sawFinishReason {
		return errors.New("sse_stream_missing_finish_reason")
	}
	if !sawDone {
		return errors.New("sse_stream_missing_done_sentinel")
	}
	return nil
}

// ValidateStandardErrorEnvelope checks HTTP status code consistency (400, 401, 404, 422, 429, 500)
// and verifies `{ error: { message, type, param, code } }` structure.
func ValidateStandardErrorEnvelope(statusCode int, body []byte) error {
	validCodes := map[int]bool{400: true, 401: true, 404: true, 422: true, 429: true, 500: true}
	if !validCodes[statusCode] {
		return fmt.Errorf("non_standard_error_http_status: %d", statusCode)
	}
	var env StandardErrorEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("invalid_error_envelope_json: %w", err)
	}
	if strings.TrimSpace(env.Error.Message) == "" || strings.TrimSpace(env.Error.Type) == "" || strings.TrimSpace(env.Error.Code) == "" {
		return errors.New("incomplete_error_envelope_fields")
	}
	return nil
}
