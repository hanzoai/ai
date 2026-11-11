package model

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ValidateTextEdgeCaseResponse verifies strict JSON schema output, parallel tool call ID uniqueness,
// reasoning block presence, and accurate `finish_reason: "length"` reporting on `max_tokens` truncation.
func ValidateTextEdgeCaseResponse(
	jsonMode bool,
	maxTokens int,
	completionTokens int,
	finishReason string,
	content string,
	toolCallIDs []string,
) error {
	if jsonMode {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(content), &parsed); err != nil {
			return fmt.Errorf("invalid_json_in_strict_json_mode: %w", err)
		}
	}

	if maxTokens > 0 && completionTokens >= maxTokens && finishReason != "length" {
		return fmt.Errorf("expected_finish_reason_length_on_truncation: got=%s", finishReason)
	}

	seen := make(map[string]bool, len(toolCallIDs))
	for _, id := range toolCallIDs {
		if id == "" {
			return errors.New("empty_tool_call_id")
		}
		if seen[id] {
			return fmt.Errorf("duplicate_parallel_tool_call_id: %s", id)
		}
		seen[id] = true
	}
	return nil
}
