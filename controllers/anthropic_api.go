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
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	iamsdk "github.com/hanzoid/go-sdk/casdoorsdk"
	"github.com/beego/beego/context"
	"github.com/hanzoai/cloud/model"
	"github.com/hanzoai/cloud/object"
	"github.com/hanzoai/cloud/util"
	"github.com/sashabaranov/go-openai"
)

// ── Anthropic Messages API types ────────────────────────────────────────────

// AnthropicRequest is the Anthropic Messages API request body.
type AnthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []AnthropicMessage `json:"messages"`
	Stream    bool               `json:"stream"`
}

// AnthropicMessage is a single message in the Anthropic conversation.
type AnthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// AnthropicContentBlock is a content block in the response.
type AnthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// AnthropicUsage tracks token counts.
type AnthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// AnthropicResponse is the non-streaming Messages API response.
type AnthropicResponse struct {
	ID         string                  `json:"id"`
	Type       string                  `json:"type"`
	Role       string                  `json:"role"`
	Content    []AnthropicContentBlock `json:"content"`
	Model      string                  `json:"model"`
	StopReason string                  `json:"stop_reason"`
	Usage      AnthropicUsage          `json:"usage"`
}

// AnthropicErrorBody is the Anthropic error response shape.
type AnthropicErrorBody struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// ── AnthropicWriter ─────────────────────────────────────────────────────────

// AnthropicWriter implements io.Writer, collecting output for non-streaming
// and emitting SSE events in Anthropic format for streaming.
type AnthropicWriter struct {
	context.Response
	Cleaner    Cleaner
	Buffer     []byte
	MessageBuf []byte
	RequestID  string
	Stream     bool
	StreamSent bool
	Model      string
	headerSent bool
}

// Write processes incoming data chunks from the model provider.
func (w *AnthropicWriter) Write(p []byte) (n int, err error) {
	var content string

	if bytes.HasPrefix(p, []byte("event: message\ndata: ")) {
		prefix := []byte("event: message\ndata: ")
		suffix := []byte("\n\n")
		content = string(bytes.TrimSuffix(bytes.TrimPrefix(p, prefix), suffix))
		w.MessageBuf = append(w.MessageBuf, []byte(content)...)
	} else if bytes.HasPrefix(p, []byte("event: reason\ndata: ")) {
		prefix := []byte("event: reason\ndata: ")
		suffix := []byte("\n\n")
		content = string(bytes.TrimSuffix(bytes.TrimPrefix(p, prefix), suffix))
	} else {
		content = w.Cleaner.CleanString(string(p))
		if content != "" {
			w.MessageBuf = append(w.MessageBuf, []byte(content)...)
		}
	}

	w.Buffer = append(w.Buffer, p...)

	if !w.Stream {
		return len(p), nil
	}

	if content == "" {
		return len(p), nil
	}

	// Emit header events on first content chunk.
	if !w.headerSent {
		w.headerSent = true

		// message_start
		msgStart := map[string]interface{}{
			"type": "message_start",
			"message": map[string]interface{}{
				"id":      "msg_" + w.RequestID,
				"type":    "message",
				"role":    "assistant",
				"content": []interface{}{},
				"model":   w.Model,
			},
		}
		if err := w.writeSSE("message_start", msgStart); err != nil {
			return 0, err
		}

		// content_block_start
		blockStart := map[string]interface{}{
			"type":  "content_block_start",
			"index": 0,
			"content_block": map[string]interface{}{
				"type": "text",
				"text": "",
			},
		}
		if err := w.writeSSE("content_block_start", blockStart); err != nil {
			return 0, err
		}
	}

	// content_block_delta
	delta := map[string]interface{}{
		"type":  "content_block_delta",
		"index": 0,
		"delta": map[string]interface{}{
			"type": "text_delta",
			"text": content,
		},
	}
	if err := w.writeSSE("content_block_delta", delta); err != nil {
		return 0, err
	}

	w.StreamSent = true
	return len(p), nil
}

// MessageString returns the full accumulated message text.
func (w *AnthropicWriter) MessageString() string {
	return string(w.MessageBuf)
}

// Close finalizes the streaming response with stop events.
func (w *AnthropicWriter) Close(promptTokens, completionTokens, totalTokens int) error {
	if !w.Stream {
		return nil
	}

	if !w.StreamSent {
		return nil
	}

	// content_block_stop
	blockStop := map[string]interface{}{
		"type":  "content_block_stop",
		"index": 0,
	}
	if err := w.writeSSE("content_block_stop", blockStop); err != nil {
		return err
	}

	// message_delta
	msgDelta := map[string]interface{}{
		"type": "message_delta",
		"delta": map[string]interface{}{
			"stop_reason": "end_turn",
		},
		"usage": map[string]interface{}{
			"output_tokens": completionTokens,
		},
	}
	if err := w.writeSSE("message_delta", msgDelta); err != nil {
		return err
	}

	// message_stop
	msgStop := map[string]interface{}{
		"type": "message_stop",
	}
	if err := w.writeSSE("message_stop", msgStop); err != nil {
		return err
	}

	w.Flush()
	return nil
}

// writeSSE writes a single SSE event with the given event name and JSON data.
func (w *AnthropicWriter) writeSSE(event string, data interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = w.ResponseWriter.Write([]byte(fmt.Sprintf("event: %s\ndata: %s\n\n", event, jsonData)))
	if err != nil {
		return err
	}
	w.Flush()
	return nil
}

// ── Handler ─────────────────────────────────────────────────────────────────

// respondAnthropicError writes an Anthropic-shaped error JSON and stops.
func (c *ApiController) respondAnthropicError(errType string, message string, status int) {
	body := AnthropicErrorBody{Type: "error"}
	body.Error.Type = errType
	body.Error.Message = message

	jsonData, err := json.Marshal(body)
	if err != nil {
		c.Ctx.ResponseWriter.WriteHeader(500)
		return
	}

	c.Ctx.Output.Header("Content-Type", "application/json")
	c.Ctx.ResponseWriter.WriteHeader(status)
	c.Ctx.Output.Body(jsonData)
	c.EnableRender = false
}

// AnthropicMessages implements the Anthropic Messages API.
// @Title AnthropicMessages
// @Tag Anthropic Compatible API
// @Description Anthropic compatible messages API. Accepts:
//   - IAM API key (hk-...)  via x-api-key or Authorization header
//   - hanzo.id JWT token    via Authorization header
//   - Provider API key      via Authorization header
//
// @Param   body    body    AnthropicRequest  true    "The Anthropic messages request"
// @Success 200 {object} AnthropicResponse
// @router /api/messages [post]
func (c *ApiController) AnthropicMessages() {
	// Extract token: prefer x-api-key, fall back to Authorization: Bearer
	token := c.Ctx.Request.Header.Get("x-api-key")
	if token == "" {
		authHeader := c.Ctx.Request.Header.Get("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ") {
			token = strings.TrimPrefix(authHeader, "Bearer ")
		}
	}

	if token == "" {
		c.respondAnthropicError("authentication_error", "Missing API key. Provide x-api-key header or Authorization: Bearer header.", 401)
		return
	}

	// Parse request body.
	var request AnthropicRequest
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, &request); err != nil {
		c.respondAnthropicError("invalid_request_error", fmt.Sprintf("Failed to parse request: %s", err.Error()), 400)
		return
	}

	if request.Model == "" {
		c.respondAnthropicError("invalid_request_error", "model is required", 400)
		return
	}

	if request.MaxTokens <= 0 {
		c.respondAnthropicError("invalid_request_error", "max_tokens is required and must be > 0", 400)
		return
	}

	if len(request.Messages) == 0 {
		c.respondAnthropicError("invalid_request_error", "messages must contain at least one message", 400)
		return
	}

	// ── Auth ────────────────────────────────────────────────────────────
	var provider *object.Provider
	var authUser *iamsdk.User
	var upstreamModel string
	var isPremium bool
	var err error

	if isIAMApiKey(token) {
		provider, authUser, upstreamModel, err = resolveProviderFromIAMKey(token, request.Model, c.GetAcceptLanguage())
		if err != nil {
			c.respondAnthropicError("authentication_error", fmt.Sprintf("Authentication failed: %s", err.Error()), 401)
			return
		}
		if authUser != nil {
			c.Ctx.Input.SetParam("recordUserId", authUser.Owner+"/"+authUser.Name)
		}
		if route := resolveModelRoute(request.Model); route != nil {
			isPremium = route.premium
		}
	} else if isJwtToken(token) {
		provider, authUser, upstreamModel, err = resolveProviderFromJwt(token, request.Model, c.GetAcceptLanguage())
		if err != nil {
			c.respondAnthropicError("authentication_error", fmt.Sprintf("Authentication failed: %s", err.Error()), 401)
			return
		}
		if authUser != nil {
			c.Ctx.Input.SetParam("recordUserId", authUser.Owner+"/"+authUser.Name)
		}
		if route := resolveModelRoute(request.Model); route != nil {
			isPremium = route.premium
		}
	} else {
		provider, err = object.GetProviderByProviderKey(token, c.GetAcceptLanguage())
		if err != nil {
			c.respondAnthropicError("authentication_error", fmt.Sprintf("Authentication failed: %s", err.Error()), 401)
			return
		}
		if provider == nil {
			c.respondAnthropicError("authentication_error", "Invalid API key", 401)
			return
		}
		if route := resolveModelRoute(request.Model); route != nil {
			upstreamModel = route.upstreamModel
			isPremium = route.premium
		}
	}

	if provider.Category != "Model" {
		c.respondAnthropicError("invalid_request_error", fmt.Sprintf("Provider %s is not a model provider", provider.Name), 400)
		return
	}

	// Set upstream model on the provider.
	if upstreamModel != "" {
		provider.SubType = upstreamModel
	} else if request.Model != "" {
		provider.SubType = request.Model
	}

	modelProvider, err := provider.GetModelProvider(c.GetAcceptLanguage())
	if err != nil {
		c.respondAnthropicError("api_error", fmt.Sprintf("Failed to get model provider: %s", err.Error()), 500)
		return
	}

	// ── Convert Anthropic messages to internal format ────────────────────
	// Build OpenAI-style messages for zen identity injection, then extract
	// question/history the same way the OpenAI endpoint does.
	oaiMessages := make([]openai.ChatCompletionMessage, 0, len(request.Messages)+1)

	// Anthropic system prompt is a top-level field, not a message.
	if request.System != "" {
		oaiMessages = append(oaiMessages, openai.ChatCompletionMessage{
			Role:    "system",
			Content: request.System,
		})
	}

	for _, msg := range request.Messages {
		oaiMessages = append(oaiMessages, openai.ChatCompletionMessage{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}

	// Inject Zen identity prompt.
	if zenPrompt := zenIdentityPrompt(request.Model); zenPrompt != "" {
		hasSystem := len(oaiMessages) > 0 && oaiMessages[0].Role == "system"
		if hasSystem {
			oaiMessages[0].Content = zenPrompt + "\n\n" + oaiMessages[0].Content
		} else {
			oaiMessages = append([]openai.ChatCompletionMessage{{
				Role:    "system",
				Content: zenPrompt,
			}}, oaiMessages...)
		}
	}

	// Extract question, system, history — mirrors OpenAI endpoint logic.
	var question string
	var systemPrompt string
	history := []*model.RawMessage{}

	for _, msg := range oaiMessages {
		switch msg.Role {
		case "system":
			systemPrompt = msg.Content
		case "user":
			question = msg.Content
		case "assistant":
			history = append(history, &model.RawMessage{
				Author: "AI",
				Text:   msg.Content,
			})
		}
	}

	if question == "" {
		c.respondAnthropicError("invalid_request_error", "No user message found in the request", 400)
		return
	}

	if systemPrompt != "" {
		question = fmt.Sprintf("System: %s\n\nUser: %s", systemPrompt, question)
	}

	// ── Call model provider ─────────────────────────────────────────────
	requestId := util.GenerateUUID()

	if request.Stream {
		c.Ctx.ResponseWriter.Header().Set("Content-Type", "text/event-stream")
		c.Ctx.ResponseWriter.Header().Set("Cache-Control", "no-cache")
		c.Ctx.ResponseWriter.Header().Set("Connection", "keep-alive")
	}

	writer := &AnthropicWriter{
		Response:  *c.Ctx.ResponseWriter,
		Buffer:    []byte{},
		RequestID: requestId,
		Stream:    request.Stream,
		Cleaner:   *NewCleaner(6),
		Model:     request.Model,
	}

	knowledge := []*model.RawMessage{}

	modelResult, err := modelProvider.QueryText(question, writer, history, "", knowledge, nil, c.GetAcceptLanguage())
	if err != nil {
		if authUser != nil {
			recordUsage(&usageRecord{
				Owner:     authUser.Owner,
				User:      authUser.Owner + "/" + authUser.Name,
				Model:     request.Model,
				Provider:  provider.Name,
				Premium:   isPremium,
				Stream:    request.Stream,
				Status:    "error",
				ErrorMsg:  err.Error(),
				ClientIP:  c.Ctx.Request.RemoteAddr,
				RequestID: requestId,
			})
		}
		c.respondAnthropicError("api_error", err.Error(), 500)
		return
	}

	// Record successful usage.
	if authUser != nil {
		recordUsage(&usageRecord{
			Owner:            authUser.Owner,
			User:             authUser.Owner + "/" + authUser.Name,
			Organization:     authUser.Owner,
			Model:            request.Model,
			Provider:         provider.Name,
			PromptTokens:     modelResult.PromptTokenCount,
			CompletionTokens: modelResult.ResponseTokenCount,
			TotalTokens:      modelResult.TotalTokenCount,
			Currency:         "USD",
			Premium:          isPremium,
			Stream:           request.Stream,
			Status:           "success",
			ClientIP:         c.Ctx.Request.RemoteAddr,
			RequestID:        requestId,
		})
	}

	// ── Build response ──────────────────────────────────────────────────
	if !request.Stream {
		answer := writer.MessageString()

		response := AnthropicResponse{
			ID:   "msg_" + requestId,
			Type: "message",
			Role: "assistant",
			Content: []AnthropicContentBlock{
				{Type: "text", Text: answer},
			},
			Model:      request.Model,
			StopReason: "end_turn",
			Usage: AnthropicUsage{
				InputTokens:  modelResult.PromptTokenCount,
				OutputTokens: modelResult.ResponseTokenCount,
			},
		}

		jsonResponse, err := json.Marshal(response)
		if err != nil {
			c.respondAnthropicError("api_error", err.Error(), 500)
			return
		}

		c.Ctx.Output.Header("Content-Type", "application/json")
		c.Ctx.Output.Body(jsonResponse)
	} else {
		if err := writer.Close(
			modelResult.PromptTokenCount,
			modelResult.ResponseTokenCount,
			modelResult.TotalTokenCount,
		); err != nil {
			c.respondAnthropicError("api_error", err.Error(), 500)
			return
		}
	}

	c.EnableRender = false
}
