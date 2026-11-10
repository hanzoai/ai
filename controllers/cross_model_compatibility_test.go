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

// TestCrossModelAPICompatibility_DeepIntegration performs rigorous integration testing
// of request handling, response body contracts, error envelopes, and SSE streaming across
// both OpenAI (/v1/chat/completions) and Anthropic (/v1/messages) endpoints.
func TestCrossModelAPICompatibility_DeepIntegration(t *testing.T) {

	t.Run("OpenAI_ChatCompletions_Contract_Validation", func(t *testing.T) {
		reqBody := `{
			"model": "test-model",
			"messages": [
				{"role": "user", "content": "Hello OpenAI"}
			],
			"stream": false,
			"response_format": {"type": "json_object"}
		}`

		c := visit(http.MethodPost, "/v1/chat/completions")
		c.Fiber().Request().SetBody([]byte(reqBody))
		c.Fiber().Request().Header.Set("Content-Type", "application/json")

		status := answering(t, c, c.ChatCompletions)
		body := sent(c)

		if status == http.StatusInternalServerError {
			t.Fatalf("unexpected 500 internal server error: %s", body)
		}

		var result map[string]interface{}
		if err := json.Unmarshal([]byte(body), &result); err != nil {
			t.Fatalf("response body is not valid JSON: %v, body: %s", err, body)
		}

		if status != http.StatusOK {
			errObj, ok := result["error"].(map[string]interface{})
			if !ok {
				t.Fatalf("non-200 response must contain structured 'error' envelope, got: %s", body)
			}
			if _, ok := errObj["message"].(string); !ok {
				t.Errorf("error envelope missing 'message' string field")
			}
		} else {
			if _, ok := result["id"].(string); !ok {
				t.Errorf("successful OpenAI response missing 'id' string field")
			}
			if obj, ok := result["object"].(string); !ok || obj != "chat.completion" {
				t.Errorf("unexpected or missing 'object' field in OpenAI response (want 'chat.completion'): %v", result["object"])
			}
			if _, ok := result["choices"].([]interface{}); !ok {
				t.Errorf("successful OpenAI response missing 'choices' array field")
			}
		}
	})

	t.Run("Anthropic_Messages_Contract_Validation", func(t *testing.T) {
		reqBody := `{
			"model": "claude-3-5-sonnet",
			"max_tokens": 256,
			"messages": [
				{"role": "user", "content": "Hello Anthropic"}
			],
			"stream": false
		}`

		c := visit(http.MethodPost, "/v1/messages")
		c.Fiber().Request().SetBody([]byte(reqBody))
		c.Fiber().Request().Header.Set("Content-Type", "application/json")
		c.Fiber().Request().Header.Set("anthropic-version", "2023-01-01")

		status := answering(t, c, c.AnthropicMessages)
		body := sent(c)

		if status == http.StatusInternalServerError {
			t.Fatalf("unexpected 500 internal server error from anthropic endpoint: %s", body)
		}

		var result map[string]interface{}
		if err := json.Unmarshal([]byte(body), &result); err != nil {
			t.Fatalf("anthropic response body is not valid JSON: %v, body: %s", err, body)
		}

		if status != http.StatusOK {
			errObj, ok := result["error"].(map[string]interface{})
			if ok {
				if _, ok := errObj["message"].(string); !ok {
					t.Errorf("anthropic error response missing 'message'")
				}
			}
		} else {
			if _, ok := result["id"].(string); !ok {
				t.Errorf("successful Anthropic response missing 'id'")
			}
			if _, ok := result["content"].([]interface{}); !ok {
				t.Errorf("successful Anthropic response missing 'content' array")
			}
		}
	})

	t.Run("Standardized_Error_Envelope_Structure", func(t *testing.T) {
		reqBody := `{"invalid_key": 12345}`

		c := visit(http.MethodPost, "/v1/chat/completions")
		c.Fiber().Request().SetBody([]byte(reqBody))
		c.Fiber().Request().Header.Set("Content-Type", "application/json")

		status := answering(t, c, c.ChatCompletions)
		body := sent(c)

		if status >= 500 {
			t.Fatalf("server faulted with status %d: %s", status, body)
		}

		var errResp struct {
			Error struct {
				Message string      `json:"message"`
				Type    string      `json:"type"`
				Code    interface{} `json:"code"`
			} `json:"error"`
		}

		if err := json.Unmarshal([]byte(body), &errResp); err != nil {
			t.Fatalf("error response body is not valid JSON: %v, body: %s", err, body)
		}

		if errResp.Error.Message == "" {
			t.Errorf("error envelope missing or empty 'message', body: %s", body)
		}
	})

	t.Run("SSE_Streaming_Response_Validation", func(t *testing.T) {
		reqBody := `{
			"model": "test-model",
			"messages": [{"role": "user", "content": "Stream validation"}],
			"stream": true
		}`

		c := visit(http.MethodPost, "/v1/chat/completions")
		c.Fiber().Request().SetBody([]byte(reqBody))
		c.Fiber().Request().Header.Set("Content-Type", "application/json")

		status := answering(t, c, c.ChatCompletions)
		body := sent(c)
		contentType := c.Fiber().Response().Header.Get("Content-Type")

		if status == http.StatusInternalServerError {
			t.Fatalf("streaming request caused internal server error")
		}

		if strings.TrimSpace(body) == "" {
			t.Errorf("streaming response body is empty")
		}

		if contentType != "" && strings.Contains(contentType, "text/event-stream") {
			if !strings.Contains(body, "data:") {
				t.Errorf("SSE stream body missing 'data:' prefix lines")
			}
			// Verify stream terminator or delta markers are present if stream is non-empty
			if !strings.Contains(body, "[DONE]") && !strings.Contains(body, "delta") {
				t.Errorf("SSE stream body missing '[DONE]' terminator or delta chunks")
			}
		}
	})
}
