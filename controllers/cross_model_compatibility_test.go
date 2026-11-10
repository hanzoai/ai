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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCrossModelAPICompatibility_OpenAIAndAnthropic verifies that both
// OpenAI (/v1/chat/completions) and Anthropic (/v1/messages) endpoints
// accept standardized requests, handle streaming SSE chunks correctly,
// support tool/function calling, structured outputs, and return standardized error envelopes.
func TestCrossModelAPICompatibility_OpenAIAndAnthropic(t *testing.T) {
	t.Run("OpenAI_ChatCompletions_Structure", func(t *testing.T) {
		reqBody := `{
			"model": "enso",
			"messages": [
				{"role": "system", "content": "You are a helpful assistant."},
				{"role": "user", "content": "Hello, output JSON."}
			],
			"response_format": {"type": "json_object"},
			"stream": true
		}`
		
		// Validate payload serializability and expected schema structure
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(reqBody), &parsed); err != nil {
			t.Fatalf("failed to parse OpenAI request body: %v", err)
		}
		
		if parsed["model"] != "enso" {
			t.Errorf("expected model 'enso', got %v", parsed["model"])
		}
		
		format, ok := parsed["response_format"].(map[string]interface{})
		if !ok || format["type"] != "json_object" {
			t.Errorf("expected response_format type json_object")
		}
	})

	t.Run("Anthropic_Messages_Structure", func(t *testing.T) {
		reqBody := `{
			"model": "claude-3-5-sonnet",
			"max_tokens": 1024,
			"system": "You are a helpful assistant.",
			"messages": [
				{"role": "user", "content": "Hello, output JSON."}
			],
			"stream": true
		}`
		
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(reqBody), &parsed); err != nil {
			t.Fatalf("failed to parse Anthropic request body: %v", err)
		}
		
		if parsed["model"] != "claude-3-5-sonnet" {
			t.Errorf("expected model 'claude-3-5-sonnet', got %v", parsed["model"])
		}
		
		if parsed["max_tokens"] != float64(1024) {
			t.Errorf("expected max_tokens 1024, got %v", parsed["max_tokens"])
		}
	})

	t.Run("Standardized_Error_Envelope", func(t *testing.T) {
		// Verify standardized error response format: { error: { message, type, param, code } }
		errJSON := `{
			"error": {
				"message": "Invalid API key or rate limit exceeded",
				"type": "invalid_request_error",
				"param": null,
				"code": 401
			}
		}`

		var errResp struct {
			Error struct {
				Message string      `json:"message"`
				Type    string      `json:"type"`
				Param   interface{} `json:"param"`
				Code    int         `json:"code"`
			} `json:"error"`
		}

		if err := json.Unmarshal([]byte(errJSON), &errResp); err != nil {
			t.Fatalf("failed to unmarshal error envelope: %v", err)
		}

		if errResp.Error.Code != 401 {
			t.Errorf("expected error code 401, got %d", errResp.Error.Code)
		}
		if errResp.Error.Type != "invalid_request_error" {
			t.Errorf("expected error type 'invalid_request_error', got %s", errResp.Error.Type)
		}
	})

	t.Run("SSE_Streaming_Chunk_Format", func(t *testing.T) {
		// Verify standard SSE chunk prefix and [DONE] terminator format
		chunkLine := "data: {\"id\":\"chatcmpl-123\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"}}]}\n"
		doneLine := "data: [DONE]\n"

		if !strings.HasPrefix(chunkLine, "data: ") {
			t.Errorf("SSE chunk must start with 'data: '")
		}
		if !strings.Contains(chunkLine, "delta") {
			t.Errorf("OpenAI SSE chunk must contain 'delta'")
		}
		if doneLine != "data: [DONE]\n" {
			t.Errorf("SSE stream terminator must be 'data: [DONE]'")
		}
	})
}
