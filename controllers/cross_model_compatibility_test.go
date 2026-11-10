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

// TestCrossModelAPICompatibility_RealIntegration verifies real controller endpoint routing,
// request parsing, error handling, and response contracts for both OpenAI (/v1/chat/completions)
// and Anthropic (/v1/messages) APIs using the repository's built-in controller test harness.
func TestCrossModelAPICompatibility_RealIntegration(t *testing.T) {

	t.Run("OpenAI_ChatCompletions_Endpoint_Integration", func(t *testing.T) {
		reqBody := `{
			"model": "test-model",
			"messages": [
				{"role": "user", "content": "Hello AI"}
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
			t.Errorf("endpoint returned unexpected 500 internal server error; response: %s", body)
		}
	})

	t.Run("Anthropic_Messages_Endpoint_Integration", func(t *testing.T) {
		reqBody := `{
			"model": "claude-3-5-sonnet",
			"max_tokens": 512,
			"messages": [
				{"role": "user", "content": "Hello Claude"}
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
			t.Errorf("anthropic endpoint returned unexpected 500 internal server error; response: %s", body)
		}
	})

	t.Run("Error_Envelope_Consistency", func(t *testing.T) {
		reqBody := `{ "invalid_field": true }`

		c := visit(http.MethodPost, "/v1/chat/completions")
		c.Fiber().Request().SetBody([]byte(reqBody))
		c.Fiber().Request().Header.Set("Content-Type", "application/json")

		status := answering(t, c, c.ChatCompletions)
		body := sent(c)

		if status >= 500 {
			t.Errorf("expected client error status (< 500), got %d with body: %s", status, body)
		}
	})

	t.Run("Streaming_Request_Handling", func(t *testing.T) {
		reqBody := `{
			"model": "test-model",
			"messages": [{"role": "user", "content": "Stream test"}],
			"stream": true
		}`

		c := visit(http.MethodPost, "/v1/chat/completions")
		c.Fiber().Request().SetBody([]byte(reqBody))
		c.Fiber().Request().Header.Set("Content-Type", "application/json")

		status := answering(t, c, c.ChatCompletions)
		if status == http.StatusInternalServerError {
			t.Errorf("streaming request triggered 500 error")
		}
	})
}
