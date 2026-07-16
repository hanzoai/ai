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
	ctx "context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
	"github.com/hanzoai/beego/context"
	iam "github.com/hanzoai/iam"
	"github.com/sashabaranov/go-openai"
)

// ── Anthropic Messages API types ────────────────────────────────────────────

// AnthropicRequest is the Anthropic Messages API request body.
type AnthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	System      json.RawMessage    `json:"system,omitempty"`
	Messages    []AnthropicMessage `json:"messages"`
	Tools       []AnthropicTool    `json:"tools,omitempty"`
	ToolChoice  json.RawMessage    `json:"tool_choice,omitempty"`
	Temperature float32            `json:"temperature,omitempty"`
	Stream      bool               `json:"stream"`
	// Thinking is Anthropic extended-thinking config: {"type":"enabled","budget_tokens":N}.
	// RawMessage so it forwards VERBATIM to a native Anthropic upstream (the native path
	// re-marshals this struct) AND is parseable by anthropicThinkingToReasoningEffort for
	// the Anthropic→OpenAI translation. One field, two consumers — the round trip that
	// used to be silently dropped on BOTH paths.
	Thinking json.RawMessage `json:"thinking,omitempty"`
}

// AnthropicTool is a tool definition in the Anthropic format.
type AnthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// SystemText returns the system prompt as a plain string.
// Handles both string format ("You are helpful") and array format
// ([{"type":"text","text":"You are helpful"}]) used by the Anthropic SDK.
func (r *AnthropicRequest) SystemText() string {
	return rawContentToText(r.System)
}

// AnthropicMessage is a single message in the Anthropic conversation.
// Content accepts both string ("hello") and array-of-blocks
// ([{"type":"text","text":"hello"}]) formats per the Anthropic Messages API.
type AnthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// ContentText returns the message content as a plain string.
// Handles both string format and array-of-content-blocks format.
func (m *AnthropicMessage) ContentText() string {
	return rawContentToText(m.Content)
}

// AnthropicContentBlock is a content block in the response.
type AnthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// rawContentToText converts a json.RawMessage that is either a JSON string
// or an array of AnthropicContentBlock into a plain Go string.
func rawContentToText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Fast path: try string first (most common for simple messages).
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Slow path: array of content blocks.
	var blocks []AnthropicContentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	// Fallback: return raw JSON as string (shouldn't happen in practice).
	return string(raw)
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
				"usage": map[string]interface{}{
					"input_tokens":  0,
					"output_tokens": 0,
				},
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
			"input_tokens":  promptTokens,
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

// anthropicErrorType maps an auth/routing/upstream error to its Anthropic wire
// type. ONE way to turn an error into a wire type: read its status (statusOf),
// fold through the single table (anthropicErrorTypeForStatus). Upstream HTTP
// failures are typed at the provider boundary (wrapUpstreamError), so a 429
// becomes rate_limit_error here — not a generic api_error.
func anthropicErrorType(err error) string {
	return anthropicErrorTypeForStatus(statusOf(err))
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
// @router /messages [post]
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

	// Publishable keys (pk-) cannot access messages — reject early
	if isPublishableKey(token) {
		c.respondAnthropicError("auth_error", "Publishable keys (pk-) can only access read-only endpoints (/api/models, /health). Use a secret key (sk-) for messages.", 403)
		return
	}

	// Parse + validate the request body. Authenticate BEFORE reporting any client
	// error so an invalid credential is 401 regardless of body validity — a
	// malformed/incomplete body from an unauthenticated caller must not return a
	// probe-able 400. A valid credential with a bad body gets the precise 400.
	var request AnthropicRequest
	badReq := ""
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, &request); err != nil {
		badReq = fmt.Sprintf("Failed to parse request: %s", err.Error())
	} else if request.Model == "" {
		badReq = "model is required"
	} else if request.MaxTokens <= 0 {
		badReq = "max_tokens is required and must be > 0"
	} else if len(request.Messages) == 0 {
		badReq = "messages must contain at least one message"
	}
	if badReq != "" {
		if authErr := c.authenticate(token); authErr != nil {
			c.respondAnthropicError("authentication_error", authErr.Error(), 401)
			return
		}
		c.respondAnthropicError("invalid_request_error", badReq, 400)
		return
	}

	// Track timing for observability.
	requestStartTime := time.Now().UTC()

	// Resolve org context for per-org model routing and pricing.
	orgId := c.GetOrg()

	// Share the exact auth + model-routing policy used by /v1/chat/completions.
	provider, authUser, upstreamModel, isPremium, isWidget, err := c.authResolveProvider(token, request.Model, orgId)
	if err != nil {
		c.respondAnthropicError(anthropicErrorType(err), err.Error(), statusOf(err))
		return
	}
	if isWidget {
		// Cap max_tokens for anonymous widget requests.
		if request.MaxTokens == 0 || request.MaxTokens > widgetMaxTokens {
			request.MaxTokens = widgetMaxTokens
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

	// ── Balance reservation (shared by tool-proxy and QueryText paths) ────
	request.MaxTokens = clampMaxTokens(request.MaxTokens)
	var hold *budgetHold
	if authUser != nil {
		subject := object.BillingSubject(authUser.Owner, authUser.Name)
		est := estimateRequestCostCents(request.Model, len(request.Messages)*500, request.MaxTokens)
		var ok bool
		if hold, ok = reserveBudget(subject, est); !ok {
			c.respondAnthropicError("billing_error", "Insufficient balance for the estimated request cost. add credits to your wallet at https://pay.hanzo.ai", http.StatusPaymentRequired)
			return
		}
	}
	defer hold.settle(0)

	// ── Model families (Zen, Enso) ─────────────────────
	// A family model is served by its family service, which owns identity, reasoning,
	// the 1M ladder, vision, the fan-out, and the upstream. ai forwards verbatim and
	// meters the result; it holds no family routing of its own (hip-00NN).
	if fam := familyForProviderType(provider.Type); fam != nil {
		c.pipeToFamily(fam, "messages", "anthropic", request.Model, c.Ctx.Input.RequestBody, request.Stream, orgId, authUser, isPremium, hold, requestStartTime)
		return
	}

	// ── Tool-calling proxy ────────────────────────────────────────────────
	// When the request carries tools (Claude Code, agents, etc.) the QueryText
	// pipeline cannot handle structured tool_use blocks. Proxy the raw Anthropic
	// request directly to the upstream and stream/return the raw response.
	if len(request.Tools) > 0 {
		c.proxyAnthropicToolRequest(provider, &request, requestStartTime, authUser, isPremium, hold)
		return
	}

	// ── Convert Anthropic messages to internal format ────────────────────
	// Build OpenAI-style messages for zen identity injection, then extract
	// question/history the same way the OpenAI endpoint does.
	oaiMessages := make([]openai.ChatCompletionMessage, 0, len(request.Messages)+1)

	// Anthropic system prompt is a top-level field, not a message.
	if sysText := request.SystemText(); sysText != "" {
		oaiMessages = append(oaiMessages, openai.ChatCompletionMessage{
			Role:    "system",
			Content: sysText,
		})
	}

	for _, msg := range request.Messages {
		oaiMessages = append(oaiMessages, openai.ChatCompletionMessage{
			Role:    msg.Role,
			Content: msg.ContentText(),
		})
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

	// Resolve the route for failover (may have fallback providers)
	route := resolveModelRouteForOrg(request.Model, orgId)

	var modelResult *model.ModelResult
	var actualProvider string

	if route != nil {
		// ONE execute path. failoverQueryText rides out a transient upstream
		// refusal (429 / 5xx) with the shared retry policy, then cascades through
		// the route's fallbacks if any. A model with no fallbacks still gets the
		// retry — the cascade is just the identity case — so there is no longer a
		// second, retry-less path that silently turns a 429 into a hard client
		// 500 (the failure behind "it stops in ours").
		modelResult, actualProvider, err = failoverQueryText(
			route, question, writer, history, knowledge,
			c.GetAcceptLanguage(),
			func() bool { return writer.StreamSent },
		)
	} else {
		// Model not in the route table: call the resolved provider directly, on
		// the SAME retry policy failover uses, typing the error at the boundary.
		var modelProvider model.ModelProvider
		modelProvider, err = provider.GetModelProvider(c.GetAcceptLanguage())
		if err != nil {
			c.respondAnthropicError("api_error", fmt.Sprintf("Failed to get model provider: %s", err.Error()), 500)
			return
		}
		err = retryTransient(ctx.Background(), currentRetryPolicy(), func() error {
			if writer.StreamSent {
				return errPartiallyWritten
			}
			res, e := modelProvider.QueryText(question, writer, history, "", knowledge, nil, c.GetAcceptLanguage())
			if e != nil {
				return wrapUpstreamError(e)
			}
			modelResult = res
			return nil
		})
		actualProvider = provider.Name
	}

	if err != nil {
		if authUser != nil {
			errRecord := &usageRecord{
				Owner:     authUser.Owner,
				User:      authUser.Owner + "/" + authUser.Name,
				Model:     request.Model,
				Provider:  actualProvider,
				Premium:   isPremium,
				Stream:    request.Stream,
				Status:    "error",
				ErrorMsg:  err.Error(),
				ClientIP:  c.Ctx.Request.RemoteAddr,
				RequestID: requestId,
			}
			errRecord.BYO, errRecord.Account = providerBYO(provider, authUser)
			recordUsage(errRecord)
			recordTrace(c.Ctx.Request.Context(), errRecord, requestStartTime)
		}
		// Surface the real upstream status: a 429 stays a 429 (rate_limit_error)
		// so the client retries with backoff instead of treating it as a fatal
		// 500 and stopping. Status typed at the provider boundary.
		st := statusForModelError(err)
		c.respondAnthropicError(anthropicErrorTypeForStatus(st), err.Error(), st)
		return
	}

	// Record successful usage (actualProvider reflects which provider served the request).
	if authUser != nil {
		successRecord := &usageRecord{
			Owner:            authUser.Owner,
			User:             authUser.Owner + "/" + authUser.Name,
			Organization:     authUser.Owner,
			Model:            request.Model,
			Provider:         actualProvider,
			PromptTokens:     modelResult.PromptTokenCount,
			CompletionTokens: modelResult.ResponseTokenCount,
			TotalTokens:      modelResult.TotalTokenCount,
			Currency:         "USD",
			Premium:          isPremium,
			Stream:           request.Stream,
			Status:           "success",
			ClientIP:         c.Ctx.Request.RemoteAddr,
			RequestID:        requestId,
		}
		successRecord.BYO, successRecord.Account = providerBYO(provider, authUser)
		recordUsage(successRecord)
		recordTrace(c.Ctx.Request.Context(), successRecord, requestStartTime)
		hold.settle(calculateCostCentsWithCache(request.Model, modelResult.PromptTokenCount, modelResult.ResponseTokenCount, 0, 0))
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

// proxyAnthropicToolRequest forwards a /v1/messages request that contains tools
// directly to the upstream, bypassing the QueryText pipeline which cannot emit
// tool_use blocks. For DO-AI / OpenAI-compat upstreams it delegates to the
// proxyToolRequest OpenAI path; for native Anthropic upstreams it forwards verbatim.
func (c *ApiController) proxyAnthropicToolRequest(
	provider *object.Provider,
	request *AnthropicRequest,
	requestStartTime time.Time,
	authUser *iam.User,
	isPremium bool,
	hold *budgetHold,
) {
	// For non-native Anthropic upstreams (DO-AI, OpenAI-compat, Local, etc.):
	// fully translate the request Anthropic→OpenAI (messages incl. tool_use /
	// tool_result / images, tools, tool_choice) and translate the response
	// OpenAI→Anthropic (SSE events or JSON) — never a raw OpenAI passthrough.
	if provider.Type != "Claude" && provider.Type != "Anthropic" {
		oaiReq := &openai.ChatCompletionRequest{
			Model:      provider.SubType,
			Messages:   anthropicToOpenAIMessages(request),
			Tools:      anthropicToolsToOpenAI(request.Tools),
			ToolChoice: anthropicToolChoiceToOpenAI(request.ToolChoice),
			MaxTokens:  request.MaxTokens,
			Stream:     request.Stream,
		}
		if request.Temperature > 0 {
			oaiReq.Temperature = request.Temperature
		}
		// Forward extended thinking: Anthropic budget_tokens → upstream reasoning_effort,
		// in the vocabulary THIS upstream model accepts (glm "max"|"high" for
		// GLM-5.*/DeepSeek V4, openai "low"|"medium"|"high", "" for qwen/kimi which
		// use a native thinking param). "" leaves the upstream at its native default.
		vocab := thinkingVocabularyForUpstream(provider.SubType)
		if re := anthropicThinkingToReasoningEffort(request.Thinking, vocab); re != "" {
			oaiReq.ReasoningEffort = re
		}
		c.proxyAnthropicViaOpenAI(provider, oaiReq, request, requestStartTime, authUser, isPremium, hold)
		return
	}

	// Native Anthropic upstream: forward the raw request body verbatim.
	apiKey := provider.ClientSecret
	baseURL := strings.TrimRight(provider.ProviderUrl, "/")
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}

	body, err := json.Marshal(request)
	if err != nil {
		c.respondAnthropicError("api_error", "Failed to marshal request: "+err.Error(), 500)
		return
	}

	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		c.respondAnthropicError("api_error", "Failed to build upstream request: "+err.Error(), 500)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		c.respondAnthropicError("api_error", "Upstream request failed: "+err.Error(), 502)
		return
	}
	defer resp.Body.Close()

	for k, vals := range resp.Header {
		for _, v := range vals {
			c.Ctx.ResponseWriter.Header().Add(k, v)
		}
	}

	requestId := util.GenerateUUID()

	if request.Stream {
		c.Ctx.ResponseWriter.Header().Set("Content-Type", "text/event-stream")
		c.Ctx.ResponseWriter.Header().Set("Cache-Control", "no-cache")
		c.Ctx.ResponseWriter.Header().Set("Connection", "keep-alive")
		c.Ctx.ResponseWriter.WriteHeader(resp.StatusCode)
		// Native Anthropic SSE passes through verbatim; capture usage from the
		// Anthropic events (message_start/message_delta), not the OpenAI shape.
		capPrompt, capCompletion := streamCaptureAnthropicUsage(
			resp.Body, c.Ctx.ResponseWriter, c.Ctx.ResponseWriter.Flush,
		)
		if authUser != nil {
			rec := &usageRecord{
				Owner: authUser.Owner, User: authUser.Owner + "/" + authUser.Name,
				Organization: authUser.Owner, Model: request.Model, Provider: provider.Name,
				PromptTokens: capPrompt, CompletionTokens: capCompletion,
				TotalTokens: capPrompt + capCompletion, Currency: "USD",
				Premium: isPremium, Stream: true, Status: "success",
				ClientIP: c.Ctx.Request.RemoteAddr, RequestID: requestId,
			}
			rec.BYO, rec.Account = providerBYO(provider, authUser)
			recordUsage(rec)
			recordTrace(c.Ctx.Request.Context(), rec, requestStartTime)
			hold.settle(calculateCostCentsWithCache(request.Model, capPrompt, capCompletion, 0, 0))
		}
	} else {
		c.Ctx.ResponseWriter.WriteHeader(resp.StatusCode)
		respBody, _ := io.ReadAll(resp.Body)
		var usage struct {
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		_ = json.Unmarshal(respBody, &usage)
		prompt, completion := usage.Usage.InputTokens, usage.Usage.OutputTokens
		if authUser != nil {
			rec := &usageRecord{
				Owner: authUser.Owner, User: authUser.Owner + "/" + authUser.Name,
				Organization: authUser.Owner, Model: request.Model, Provider: provider.Name,
				PromptTokens: prompt, CompletionTokens: completion,
				TotalTokens: prompt + completion, Currency: "USD",
				Premium: isPremium, Stream: false, Status: "success",
				ClientIP: c.Ctx.Request.RemoteAddr, RequestID: requestId,
			}
			rec.BYO, rec.Account = providerBYO(provider, authUser)
			recordUsage(rec)
			recordTrace(c.Ctx.Request.Context(), rec, requestStartTime)
			hold.settle(calculateCostCentsWithCache(request.Model, prompt, completion, 0, 0))
		}
		c.Ctx.Output.Body(respBody)
	}
	c.EnableRender = false
}

// proxyAnthropicViaOpenAI sends a translated OpenAI request to an OpenAI-compatible
// upstream and converts the response back into Anthropic Messages format — proper
// Anthropic SSE events on the streaming path, a content-block JSON body otherwise.
// This is the total translation that replaces the old raw-OpenAI passthrough.
func (c *ApiController) proxyAnthropicViaOpenAI(
	provider *object.Provider,
	oaiReq *openai.ChatCompletionRequest,
	request *AnthropicRequest,
	requestStartTime time.Time,
	authUser *iam.User,
	isPremium bool,
	hold *budgetHold,
) {
	requestId := util.GenerateUUID()

	// Force a final usage chunk on the streaming path so tool calls bill for real
	// token counts (a funded key must never get free premium inference via
	// stream:true — the same guard proxyToolRequest applies).
	if oaiReq.Stream {
		oaiReq.StreamOptions = &openai.StreamOptions{IncludeUsage: true}
	}

	upstreamURL, apiKey, authHeader := resolveUpstreamEndpoint(provider)
	if upstreamURL == "" {
		c.respondAnthropicError("api_error", "No upstream endpoint configured for provider: "+provider.Name, 500)
		return
	}

	body, err := json.Marshal(oaiReq)
	if err != nil {
		c.respondAnthropicError("api_error", "Failed to marshal request: "+err.Error(), 500)
		return
	}

	req, err := http.NewRequest(http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		c.respondAnthropicError("api_error", "Failed to build upstream request: "+err.Error(), 500)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	} else if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		if authUser != nil {
			errRecord := &usageRecord{
				Owner: authUser.Owner, User: authUser.Owner + "/" + authUser.Name,
				Model: request.Model, Provider: provider.Name, Premium: isPremium,
				Stream: request.Stream, Status: "error", ErrorMsg: err.Error(),
				ClientIP: c.Ctx.Request.RemoteAddr, RequestID: requestId,
			}
			errRecord.BYO, errRecord.Account = providerBYO(provider, authUser)
			recordUsage(errRecord)
			recordTrace(c.Ctx.Request.Context(), errRecord, requestStartTime)
		}
		c.respondAnthropicError("api_error", "Upstream request failed: "+err.Error(), 502)
		return
	}
	defer resp.Body.Close()

	if request.Stream {
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			c.respondAnthropicError(anthropicErrorTypeForStatus(resp.StatusCode), upstreamErrorMessage(respBody), resp.StatusCode)
			return
		}
		c.Ctx.ResponseWriter.Header().Set("Content-Type", "text/event-stream")
		c.Ctx.ResponseWriter.Header().Set("Cache-Control", "no-cache")
		c.Ctx.ResponseWriter.Header().Set("Connection", "keep-alive")
		c.Ctx.ResponseWriter.WriteHeader(http.StatusOK)

		emit := func(event string, data interface{}) error {
			jsonData, mErr := json.Marshal(data)
			if mErr != nil {
				return mErr
			}
			if _, wErr := fmt.Fprintf(c.Ctx.ResponseWriter, "event: %s\ndata: %s\n\n", event, jsonData); wErr != nil {
				return wErr
			}
			c.Ctx.ResponseWriter.Flush()
			return nil
		}
		prompt, completion, _ := translateOpenAIStream(resp.Body, emit, request.Model, requestId)

		// Tokenizer fallback so a successful streamed tool call is never billed $0.
		if prompt == 0 {
			if pt, e := model.OpenaiNumTokensFromMessages(oaiReq.Messages, request.Model); e == nil {
				prompt = pt
			}
		}
		c.recordAnthropicToolUsage(request, provider, authUser, isPremium, true, requestId, prompt, completion, requestStartTime, hold)
		c.EnableRender = false
		return
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		c.respondAnthropicError("api_error", "Failed to read upstream response: "+err.Error(), 502)
		return
	}
	if resp.StatusCode != http.StatusOK {
		c.respondAnthropicError(anthropicErrorTypeForStatus(resp.StatusCode), upstreamErrorMessage(respBody), resp.StatusCode)
		return
	}
	antResp, prompt, completion := openAIResponseToAnthropic(respBody, request.Model, requestId)
	out, err := json.Marshal(antResp)
	if err != nil {
		c.respondAnthropicError("api_error", err.Error(), 500)
		return
	}
	c.Ctx.Output.Header("Content-Type", "application/json")
	c.Ctx.Output.Body(out)
	c.recordAnthropicToolUsage(request, provider, authUser, isPremium, false, requestId, prompt, completion, requestStartTime, hold)
	c.EnableRender = false
}

// recordAnthropicToolUsage settles the budget hold and records usage + trace for a
// translated Anthropic tool request. Shared by the streaming and non-streaming
// paths so billing lives in exactly one place.
func (c *ApiController) recordAnthropicToolUsage(
	request *AnthropicRequest, provider *object.Provider, authUser *iam.User,
	isPremium, stream bool, requestId string, prompt, completion int,
	requestStartTime time.Time, hold *budgetHold,
) {
	actualCents := calculateCostCentsWithCache(request.Model, prompt, completion, 0, 0)
	if authUser != nil {
		rec := &usageRecord{
			Owner: authUser.Owner, User: authUser.Owner + "/" + authUser.Name,
			Organization: authUser.Owner, Model: request.Model, Provider: provider.Name,
			PromptTokens: prompt, CompletionTokens: completion, TotalTokens: prompt + completion,
			Currency: "USD", Premium: isPremium, Stream: stream, Status: "success",
			ClientIP: c.Ctx.Request.RemoteAddr, RequestID: requestId,
		}
		rec.BYO, rec.Account = providerBYO(provider, authUser)
		recordUsage(rec)
		recordTrace(c.Ctx.Request.Context(), rec, requestStartTime)
	}
	hold.settle(actualCents)
}

// anthropicErrorTypeForStatus maps an upstream HTTP status to an Anthropic error type.
func anthropicErrorTypeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	default:
		return "api_error"
	}
}

// upstreamErrorMessage extracts a readable message from an OpenAI-shaped error
// body ({"error":{"message":...}}), falling back to the raw body.
func upstreamErrorMessage(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	if len(body) > 0 {
		return string(body)
	}
	return "upstream error"
}

// AnthropicCountTokens implements POST /v1/messages/count_tokens. Claude Code
// calls it before a request; it returns {"input_tokens": N} for the given
// model + messages + tools.
// @Title AnthropicCountTokens
// @Tag Anthropic Compatible API
// @Description Anthropic-compatible token counting.
// @router /messages/count_tokens [post]
func (c *ApiController) AnthropicCountTokens() {
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
	if isPublishableKey(token) {
		c.respondAnthropicError("auth_error", "Publishable keys (pk-) can only access read-only endpoints. Use a secret key (sk-) for messages.", 403)
		return
	}

	var request AnthropicRequest
	parseErr := json.Unmarshal(c.Ctx.Input.RequestBody, &request)
	if authErr := c.authenticate(token); authErr != nil {
		c.respondAnthropicError("authentication_error", authErr.Error(), 401)
		return
	}
	if parseErr != nil {
		c.respondAnthropicError("invalid_request_error", "Failed to parse request: "+parseErr.Error(), 400)
		return
	}
	if request.Model == "" {
		c.respondAnthropicError("invalid_request_error", "model is required", 400)
		return
	}

	msgs := anthropicToOpenAIMessages(&request)
	n, err := model.OpenaiNumTokensFromMessages(msgs, request.Model)
	if err != nil || n <= 0 {
		// Coarse character-based fallback when the tokenizer can't handle the model.
		chars := 0
		for _, m := range msgs {
			chars += len(m.Content)
		}
		n = chars / 4
	}
	// Tool schemas contribute input tokens too.
	if len(request.Tools) > 0 {
		if tb, e := json.Marshal(anthropicToolsToOpenAI(request.Tools)); e == nil {
			if tn, e2 := model.GetTokenSize(request.Model, string(tb)); e2 == nil {
				n += tn
			}
		}
	}

	out, _ := json.Marshal(map[string]interface{}{"input_tokens": n})
	c.Ctx.Output.Header("Content-Type", "application/json")
	c.Ctx.Output.Body(out)
	c.EnableRender = false
}
