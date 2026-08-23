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
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/upstream"
	"github.com/hanzoai/go-openai"
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

// requestHasMediaAnthropic reports whether any message carries a non-text content
// block (image, document). String content is text-only; an array with any block whose
// type is not "text" is multimodal and must be forwarded verbatim, not text-flattened.
func requestHasMediaAnthropic(req *AnthropicRequest) bool {
	for _, m := range req.Messages {
		s := strings.TrimSpace(string(m.Content))
		if s == "" || s[0] != '[' {
			continue
		}
		var blocks []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(m.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type != "" && b.Type != "text" {
				return true
			}
		}
	}
	return false
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
	*bufio.Writer
	Cleaner    Cleaner
	Buffer     []byte
	MessageBuf []byte
	RequestID  string
	Stream     bool
	StreamSent bool
	Model      string
	headerSent bool
}

// Flush satisfies http.Flusher.
//
// It has to be written out even though the embedded *bufio.Writer already has a
// Flush, because that one returns an error and http.Flusher requires a method
// returning nothing. The promoted method therefore made this type LOOK
// flushable while failing the interface assertion, and every streaming model
// adapter begins by asserting exactly that — so /v1/messages answered
// "writer does not implement http.Flusher" for every request, tools or not,
// streaming or not, while /v1/chat/completions beside it was fine.
func (w *AnthropicWriter) Flush() {
	if w.Writer != nil {
		_ = w.Writer.Flush()
	}
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
	return len(p), nil
}

// MessageString returns the full accumulated message text.
func (w *AnthropicWriter) MessageString() string {
	return string(w.MessageBuf)
}

// Reset discards what a failed attempt accumulated, so the next provider's
// answer is not served glued to the dead one's half-sentence. Same contract as
// OpenAIWriter.Reset, and for the same reason: Write appends, and one writer is
// shared across every failover attempt.
//
// StreamSent and headerSent are NOT cleared. Both record that bytes reached the
// CLIENT — a fact about the wire that cannot be undone, and the one that
// forbids the retry this prepares for.
func (w *AnthropicWriter) Reset() {
	w.Buffer = w.Buffer[:0]
	w.MessageBuf = w.MessageBuf[:0]
	w.Cleaner = *NewCleaner(w.Cleaner.bufferSize)
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

	return w.Writer.Flush()
}

// writeSSE writes a single SSE event with the given event name and JSON data.
//
// It is also the ONE place StreamSent is set, because it is the one place bytes
// reach the client and that is precisely what StreamSent means. Setting it at the
// end of Write instead meant message_start and content_block_start — 185 measured
// bytes — were on the wire while the flag still said the request was movable. The
// failover loop reads that flag to decide whether it may offer this request to
// another vendor, so the window was one where it would have: the client gets the
// opening of one vendor's answer followed by the whole of another's, which is
// indistinguishable from a model losing its mind and detectable nowhere.
//
// BYTES LEAVE AT THE FLUSH, NOT AT THE WRITE. The destination is buffered — in
// production it is the writer the stream callback hands in — so a Write that returns
// n > 0 has reached the buffer and nothing else. Read as delivery, that flag says an
// answer reached a client it never reached, which is the failover loop's cue to stop
// routing around a first-byte failure; and an unchecked Flush reports a connection
// that broke as a success, so the relay carries on writing into a socket nobody is
// reading. Both were true of this function the moment a bufio.Writer went in front.
//
// A partial write is still bytes delivered, so a flush that fails having moved SOME
// of the buffer still sets the flag: what is left buffered afterwards is what did not
// go out, which is how "some of it did" is known.
func (w *AnthropicWriter) writeSSE(event string, data interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if n, err := w.Writer.Write([]byte(fmt.Sprintf("event: %s\ndata: %s\n\n", event, jsonData))); err != nil {
		// A write that reaches past the buffer carries its own delivery.
		if n > 0 && w.Buffered() == 0 {
			w.StreamSent = true
		}
		return err
	}
	pending := w.Buffered()
	if err := w.Writer.Flush(); err != nil {
		if w.Buffered() < pending {
			w.StreamSent = true
		}
		return err
	}
	w.StreamSent = true
	return nil
}

// ── Handler ─────────────────────────────────────────────────────────────────

// respondAnthropicError writes an Anthropic-shaped error JSON and stops.
// anthropicErrorBody is the error shape the API documents. Which connection it
// travels on is a separate question, answered by the two functions below.
func anthropicErrorBody(errType string, message string) ([]byte, error) {
	body := AnthropicErrorBody{Type: "error"}
	body.Error.Type = errType
	body.Error.Message = message
	return json.Marshal(body)
}

func (c *ApiController) respondAnthropicError(errType string, message string, status int) {
	jsonData, err := anthropicErrorBody(errType, message)
	if err != nil {
		c.Status(500)
		return
	}

	c.SetHeader("Content-Type", "application/json")
	c.Bytes(status, jsonData)
}

// streamAnthropicError sends the same body as an `error` event. Once a stream is
// being produced the reply is no longer the controller's to write — the request
// context has been released and the client is reading events — so the failure
// goes out on the stream's own writer.
func streamAnthropicError(w *bufio.Writer, errType string, message string) {
	jsonData, err := anthropicErrorBody(errType, message)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", jsonData)
	_ = w.Flush()
}

// anthropicErrorType maps an auth/routing/upstream error to its Anthropic wire
// type. ONE way to turn an error into a wire type: read its status (statusOf),
// fold through the single table (anthropicErrorTypeForStatus). Upstream HTTP
// failures are typed at the provider boundary (wrapUpstreamError), so a 429
// becomes rate_limit_error here — not a generic api_error.
func anthropicErrorType(err error) string {
	return anthropicErrorTypeForStatus(statusOf(err))
}

// respondAnthropicRefusal renders a typed refusal in the Anthropic dialect, reading
// the status and wire type off the ONE error rather than off a status a caller
// carried separately. It is the dialect twin of ResponseFailure, and it exists so
// that "who does this refusal belong to" is decided once, by relay, and merely
// rendered here.
func (c *ApiController) respondAnthropicRefusal(err error) {
	c.respondAnthropicError(anthropicErrorType(err), err.Error(), statusOf(err))
}

// AnthropicMessages implements the Anthropic Messages API.
// @Title AnthropicMessages
// @Tag Anthropic Compatible API
// @Description Anthropic compatible messages API. Accepts:
//   - IAM API key (sk-...)  via x-api-key or Authorization header
//   - hanzo.id JWT token    via Authorization header
//   - Provider API key      via Authorization header
//
// @Param   body    body    AnthropicRequest  true    "The Anthropic messages request"
// @Success 200 {object} AnthropicResponse
// @router /messages [post]
func (c *ApiController) AnthropicMessages() {
	// Extract token: prefer x-api-key, fall back to Authorization: Bearer
	token := c.Header("x-api-key")
	if token == "" {
		authHeader := c.Header("Authorization")
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
		c.respondAnthropicError("auth_error", "Publishable keys (pk-) can only access read-only endpoints (/v1/models, /v1/embeddings, /health). Use a secret key (sk-) for messages.", 403)
		return
	}

	// Parse + validate the request body. Authenticate BEFORE reporting any client
	// error so an invalid credential is 401 regardless of body validity — a
	// malformed/incomplete body from an unauthenticated caller must not return a
	// probe-able 400. A valid credential with a bad body gets the precise 400.
	var request AnthropicRequest
	badReq := ""
	if err := json.Unmarshal(c.Body(), &request); err != nil {
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
	provider, authUser, upstreamModel, isPremium, err := c.authResolveProvider(token, request.Model, orgId)
	if err != nil {
		c.respondAnthropicError(anthropicErrorType(err), err.Error(), statusOf(err))
		return
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
		ledger := c.billingOrg(authUser)
		subject := authUser.PayerSubject(ledger)
		est := estimateRequestCostCents(request.Model, len(request.Messages)*500, request.MaxTokens)
		var ok bool
		if hold, ok = reserveBudget(subject, est); !ok {
			c.respondAnthropicError("billing_error", object.InsufficientBalance(c.Host(), ledger, "request cost").Message, http.StatusPaymentRequired)
			return
		}
	}
	defer hold.settle(0)

	// One request id for the whole request — the response id, the usage-ledger
	// key, and the id every refusal along the way is filed under, so a failover
	// reads back as one story rather than as unrelated rows.
	requestId := uuid.NewString()

	// ── Model families (Zen, Enso) ─────────────────────
	// A family model is served by its family service, which owns identity, reasoning,
	// the 1M ladder, vision, the fan-out, and the upstream. ai forwards verbatim and
	// meters the result; it holds no family routing of its own (hip-00NN).
	//
	// A family is one provider among several: when it refuses for a reason of
	// its own it writes nothing and hands back the reason, and the request
	// carries on to the route's declared alternates below.
	var familyRefused []attempt
	if fam := familyForProviderType(provider.Type); fam != nil {
		familyRefused = c.pipeToFamily(fam, "messages", "anthropic", request.Model, c.Body(), request.Stream, orgId, authUser, isPremium, hold, requestStartTime)
		if familyRefused == nil {
			return
		}
		recordRefusals(c.takeSnapshot(authUser), request.Model, familyRefused, authUser, isPremium, request.Stream, requestId, requestStartTime)
	}

	// ── Tool-calling proxy ────────────────────────────────────────────────
	// When the request carries tools (Claude Code, agents, etc.) the QueryText
	// pipeline cannot handle structured tool_use blocks. Proxy the raw Anthropic
	// request directly to the upstream and stream/return the raw response.
	//
	// A tool request the family refused stops here: the pipeline below is
	// text-only, and answering a tool call with prose is worse than an honest
	// refusal that names the vendor and the reason.
	if len(request.Tools) > 0 {
		if familyRefused != nil {
			err := exhausted(request.Model, familyRefused)
			c.respondAnthropicError("api_error", err.Error(), statusOf(err))
			return
		}
		c.proxyAnthropicToolRequest(provider, &request, requestStartTime, authUser, isPremium, hold)
		return
	}

	// Multimodal (vision): the QueryText path below is text-only and would drop image
	// blocks. Forward multimodal requests verbatim to the upstream (same path as tools),
	// so vision-capable models receive the images. Symmetric with the OpenAI endpoint.
	if requestHasMediaAnthropic(&request) {
		// Same stop as tools: cascading a request whose images the pipeline
		// would discard produces an answer about nothing.
		if familyRefused != nil {
			err := exhausted(request.Model, familyRefused)
			c.respondAnthropicError("api_error", err.Error(), statusOf(err))
			return
		}
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

	if request.Stream {
		c.SetHeader("Content-Type", "text/event-stream")
		c.SetHeader("Cache-Control", "no-cache")
		c.SetHeader("Connection", "keep-alive")
	}

	// The answer is produced once and delivered two ways. Streaming, zip holds the
	// connection and hands the writer in; otherwise the writer only accumulates and
	// the reply goes out whole at the end, so the sink it was given is never
	// written to.
	//
	// Which way it goes decides what run may touch. Streamed, fasthttp drains w
	// from its own goroutine once this handler has returned and the request context
	// is gone — so run reads the snapshot rather than the controller, and a failure
	// travels out through fail(), which knows which connection is still open.
	snap := c.takeSnapshot(authUser)
	run := func(w *bufio.Writer) {
		fail := func(errType string, message string, status int) {
			if request.Stream {
				streamAnthropicError(w, errType, message)
				return
			}
			c.respondAnthropicError(errType, message, status)
		}
		writer := &AnthropicWriter{
			Writer:    w,
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
		var actualProvider served
		var tried []attempt

		if route != nil {
			// ONE execute path. ask.serve rides out a transient upstream refusal
			// (429 / 5xx) with the shared retry policy, then cascades through the
			// route's alternates. A model with no alternate still gets the retry,
			// the demotion, and the honest exhausted error — the cascade is just the
			// identity case — so there is no second, retry-less path that silently
			// turns a 429 into a hard client 500.
			modelResult, actualProvider, tried, err = ask{
				ctx:       snap.ctx,
				route:     route,
				org:       snap.org,
				model:     request.Model,
				primary:   provider,
				question:  question,
				history:   history,
				knowledge: knowledge,
				lang:      snap.lang,
				writer:    writer,
				sent:      func() bool { return writer.StreamSent },
				prior:     familyRefused,
			}.serve()
		} else {
			// Model not in the route table: call the resolved provider directly, on
			// the SAME retry policy failover uses, typing the error at the boundary.
			var modelProvider model.ModelProvider
			modelProvider, err = provider.GetModelProvider(snap.lang)
			if err != nil {
				fail("api_error", fmt.Sprintf("Failed to get model provider: %s", err.Error()), 500)
				return
			}
			err = retryTransient(snap.ctx, currentRetryPolicy(), func() error {
				if writer.StreamSent {
					return errPartiallyWritten
				}
				writer.Reset()
				res, e := modelProvider.QueryText(question, writer, history, "", knowledge, nil, snap.lang)
				if e != nil {
					return wrapUpstreamError(e)
				}
				modelResult = res
				return nil
			})
			actualProvider = served{provider.Name, provider.Origin(), provider}
		}

		// Every vendor that refused goes in the ledger, whether or not one of them
		// eventually served. The family's own refusal is already recorded above.
		if n := len(familyRefused); len(tried) > n {
			recordRefusals(snap, request.Model, tried[n:], authUser, isPremium, request.Stream, requestId, requestStartTime)
		}

		if err != nil {
			if authUser != nil {
				errRecord := &usageRecord{
					Owner:     snap.org,
					Model:     request.Model,
					Provider:  actualProvider.name,
					Origin:    actualProvider.origin,
					Premium:   isPremium,
					Stream:    request.Stream,
					Status:    "error",
					ErrorMsg:  err.Error(),
					ClientIP:  snap.ip,
					RequestID: requestId,
				}
				errRecord.bind(snap.ctx, authUser)
				errRecord.BYO, errRecord.Account = providerBYO(provider, authUser)
				recordUsage(errRecord)
				recordTrace(snap.ctx, errRecord, requestStartTime)
			}
			// Surface the real upstream status: a 429 stays a 429 (rate_limit_error)
			// so the client retries with backoff instead of treating it as a fatal
			// 500 and stopping. Status typed at the provider boundary.
			st := statusForModelError(err)
			fail(anthropicErrorTypeForStatus(st), err.Error(), st)
			return
		}

		// Record successful usage (actualProvider reflects which provider served the request).
		if authUser != nil {
			successRecord := &usageRecord{
				Owner:            snap.org,
				Organization:     authUser.Owner,
				Model:            request.Model,
				Provider:         actualProvider.name,
				Origin:           actualProvider.origin,
				PromptTokens:     modelResult.PromptTokenCount,
				CacheReadTokens:  modelResult.CacheReadTokenCount,
				CacheWriteTokens: modelResult.CacheWriteTokenCount,
				CompletionTokens: modelResult.ResponseTokenCount,
				TotalTokens:      modelResult.TotalTokenCount,
				Currency:         "USD",
				Premium:          isPremium,
				Stream:           request.Stream,
				Status:           "success",
				ClientIP:         snap.ip,
				RequestID:        requestId,
			}
			successRecord.bind(snap.ctx, authUser)
			// The row that SPENT a credential decides whether this was the customer's
			// own key — not the row auth resolved before failover moved the request.
			successRecord.BYO, successRecord.Account = providerBYO(actualProvider.row, authUser)
			recordUsage(successRecord)
			recordTrace(snap.ctx, successRecord, requestStartTime)
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
				fail("api_error", err.Error(), 500)
				return
			}

			c.SetHeader("Content-Type", "application/json")
			c.Bytes(http.StatusOK, jsonResponse)
		} else {
			if err := writer.Close(
				modelResult.PromptTokenCount,
				modelResult.ResponseTokenCount,
				modelResult.TotalTokenCount,
			); err != nil {
				fail("api_error", err.Error(), 500)
				return
			}
		}

	}
	if request.Stream {
		_ = c.SendStreamWriter(run)
	} else {
		run(bufio.NewWriter(io.Discard))
	}
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
	upstream.Authorize(req, provider)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		c.respondAnthropicError("api_error", "Upstream request failed: "+err.Error(), 502)
		return
	}
	// Closed by whoever reads it: this function, or the stream callback that outlives
	// it and takes the body with it.
	defer func() {
		if resp != nil {
			resp.Body.Close()
		}
	}()

	// ONE status decision, ahead of the stream/buffered split and ahead of the
	// billing below, for the reason proxyToolRequest states: both branches write a
	// status and both then bill, and neither question has a different answer for a
	// stream than for a buffered response.
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		c.respondAnthropicRefusal(relay(request.Model, provider.Name, resp.StatusCode, b))
		return
	}

	for k, vals := range resp.Header {
		for _, v := range vals {
			c.Fiber().Response().Header.Add(k, v)
		}
	}

	requestId := uuid.NewString()

	if request.Stream {
		c.SetHeader("Content-Type", "text/event-stream")
		c.SetHeader("Cache-Control", "no-cache")
		c.SetHeader("Connection", "keep-alive")
		c.Status(http.StatusOK)
		// Native Anthropic SSE passes through verbatim; capture usage from the
		// Anthropic events (message_start/message_delta), not the OpenAI shape.
		//
		// THE CAPTURE AND THE BILLING BOTH RUN INSIDE THE STREAM. fasthttp drains this
		// writer while it serialises the response, so the callback has not run when
		// SendStreamWriter returns: the upstream body has to travel in (the defer above
		// would otherwise close it before a byte was read) and the counts have to be
		// used in here, or every streamed answer is billed as zero.
		upstream := resp.Body
		resp = nil
		snap := c.takeSnapshot(authUser)
		_ = c.SendStreamWriter(func(w *bufio.Writer) {
			defer upstream.Close()
			prompt, completion := streamCaptureAnthropicUsage(
				upstream, w, func() { _ = w.Flush() },
			)
			if authUser == nil {
				return
			}
			rec := &usageRecord{
				Owner: snap.org, Organization: authUser.Owner, Model: request.Model, Provider: provider.Name,
				Origin:       provider.Origin(),
				PromptTokens: prompt, CompletionTokens: completion,
				TotalTokens: prompt + completion, Currency: "USD",
				Premium: isPremium, Stream: true, Status: "success",
				ClientIP: snap.ip, RequestID: requestId,
			}
			rec.bind(snap.ctx, authUser)
			rec.BYO, rec.Account = providerBYO(provider, authUser)
			recordUsage(rec)
			recordTrace(snap.ctx, rec, requestStartTime)
			hold.settle(calculateCostCentsWithCache(request.Model, prompt, completion, 0, 0))
		})
	} else {
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
				Owner: c.billingOrg(authUser), Organization: authUser.Owner, Model: request.Model, Provider: provider.Name,
				Origin:       provider.Origin(),
				PromptTokens: prompt, CompletionTokens: completion,
				TotalTokens: prompt + completion, Currency: "USD",
				Premium: isPremium, Stream: false, Status: "success",
				ClientIP: c.Fiber().IP(), RequestID: requestId,
			}
			rec.bind(c.Context(), authUser)
			rec.BYO, rec.Account = providerBYO(provider, authUser)
			recordUsage(rec)
			recordTrace(c.Context(), rec, requestStartTime)
			hold.settle(calculateCostCentsWithCache(request.Model, prompt, completion, 0, 0))
		}
		c.Bytes(http.StatusOK, respBody)
	}
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
	requestId := uuid.NewString()

	// Force a final usage chunk on the streaming path so tool calls bill for real
	// token counts (a funded key must never get free premium inference via
	// stream:true — the same guard proxyToolRequest applies).
	if oaiReq.Stream {
		oaiReq.StreamOptions = &openai.StreamOptions{IncludeUsage: true}
	}

	upstreamURL := upstream.Endpoint(provider, "chat/completions")
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
	upstream.Authorize(req, provider)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		if authUser != nil {
			errRecord := &usageRecord{
				Owner: c.billingOrg(authUser), Model: request.Model, Provider: provider.Name, Premium: isPremium,
				Origin: provider.Origin(),
				Stream: request.Stream, Status: "error", ErrorMsg: err.Error(),
				ClientIP: c.Fiber().IP(), RequestID: requestId,
			}
			errRecord.bind(c.Context(), authUser)
			errRecord.BYO, errRecord.Account = providerBYO(provider, authUser)
			recordUsage(errRecord)
			recordTrace(c.Context(), errRecord, requestStartTime)
		}
		c.respondAnthropicError("api_error", "Upstream request failed: "+err.Error(), 502)
		return
	}
	// Closed by whoever reads it: this function, or the stream callback that outlives
	// it and takes the body with it.
	defer func() {
		if resp != nil {
			resp.Body.Close()
		}
	}()

	if request.Stream {
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			c.respondAnthropicRefusal(relay(request.Model, provider.Name, resp.StatusCode, respBody))
			return
		}
		c.SetHeader("Content-Type", "text/event-stream")
		c.SetHeader("Cache-Control", "no-cache")
		c.SetHeader("Connection", "keep-alive")
		c.Status(http.StatusOK)

		// The translation and the billing both run inside the stream, for the reason
		// the verbatim relay above states: the callback has not run when
		// SendStreamWriter returns, so the body must travel in and the counts must be
		// used in here.
		upstream := resp.Body
		resp = nil
		snap := c.takeSnapshot(authUser)
		_ = c.SendStreamWriter(func(w *bufio.Writer) {
			defer upstream.Close()
			emit := func(event string, data interface{}) error {
				jsonData, mErr := json.Marshal(data)
				if mErr != nil {
					return mErr
				}
				if _, wErr := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, jsonData); wErr != nil {
					return wErr
				}
				_ = w.Flush()
				return nil
			}
			prompt, completion, _ := translateOpenAIStream(upstream, emit, request.Model, requestId)
			// Tokenizer fallback so a successful streamed tool call is never billed $0.
			if prompt == 0 {
				if pt, e := model.OpenaiNumTokensFromMessages(oaiReq.Messages, request.Model); e == nil {
					prompt = pt
				}
			}
			recordAnthropicToolUsage(snap, request, provider, authUser, isPremium, true, requestId, prompt, completion, requestStartTime, hold)
		})
		return
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		c.respondAnthropicError("api_error", "Failed to read upstream response: "+err.Error(), 502)
		return
	}
	if resp.StatusCode != http.StatusOK {
		c.respondAnthropicRefusal(relay(request.Model, provider.Name, resp.StatusCode, respBody))
		return
	}
	antResp, prompt, completion := openAIResponseToAnthropic(respBody, request.Model, requestId)
	out, err := json.Marshal(antResp)
	if err != nil {
		c.respondAnthropicError("api_error", err.Error(), 500)
		return
	}
	c.SetHeader("Content-Type", "application/json")
	c.Bytes(http.StatusOK, out)
	recordAnthropicToolUsage(c.takeSnapshot(authUser), request, provider, authUser, isPremium, false, requestId, prompt, completion, requestStartTime, hold)
}

// recordAnthropicToolUsage settles the budget hold and records usage + trace for a
// translated Anthropic tool request. Shared by the streaming and non-streaming
// paths so billing lives in exactly one place.
// recordAnthropicToolUsage bills a tool call. One of its two callers runs inside
// a stream writer, where the request context is already released, so it reads the
// request's outliving parts from a snapshot rather than from a controller.
func recordAnthropicToolUsage(
	snap snapshot,
	request *AnthropicRequest, provider *object.Provider, authUser *iam.User,
	isPremium, stream bool, requestId string, prompt, completion int,
	requestStartTime time.Time, hold *budgetHold,
) {
	actualCents := calculateCostCentsWithCache(request.Model, prompt, completion, 0, 0)
	if authUser != nil {
		rec := &usageRecord{
			Owner: snap.org, Organization: authUser.Owner, Model: request.Model, Provider: provider.Name,
			Origin:       provider.Origin(),
			PromptTokens: prompt, CompletionTokens: completion, TotalTokens: prompt + completion,
			Currency: "USD", Premium: isPremium, Stream: stream, Status: "success",
			ClientIP: snap.ip, RequestID: requestId,
		}
		rec.bind(snap.ctx, authUser)
		rec.BYO, rec.Account = providerBYO(provider, authUser)
		recordUsage(rec)
		recordTrace(snap.ctx, rec, requestStartTime)
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
	case http.StatusServiceUnavailable:
		// What this service answers when OUR supply cannot serve the request —
		// every provider refused, or our own cash breaker is holding. Anthropic's
		// own word for "retry shortly"; api_error reads as "we are broken" and
		// sends the caller to file a bug instead of trying again.
		return "overloaded_error"
	default:
		return "api_error"
	}
}

// upstreamErrorMessage is the readable reason inside an upstream's error body, in
// the shapes upstreams actually write.
//
// It NEVER returns the body. Every one of its callers is answering a customer —
// zenError, respondAnthropicError, and the attempt that exhausted() ends up
// quoting — so falling back to the raw body published whatever the vendor happened
// to put in it. Measured on the way out that way: `provider`, `cost` and
// `upstream_inference_cost`, which is exactly the disclosure the envelope removes
// from every SUCCESSFUL answer. A refusal is not a hole in that.
//
// It also never repeats a sentence that NAMES an upstream. A served answer does
// not say which one produced it, and a refusal carries the same obligation: the
// sentence a vendor writes for its own billing says who they are and links their
// console, which is a remedy the caller has no access to perform. It is dropped
// WHOLE rather than edited, because a partial redaction leaves the shape of the
// name behind. A complaint about the REQUEST keeps its words — those are the
// caller's to act on, and dropping them would make every upstream failure opaque.
//
// A body in none of these shapes is one we have not read, and "upstream error" is
// the honest thing to say about it. It is deliberately not logged either: an error
// body is where a vendor echoes the request that provoked it, so it is the last
// place a prompt should be copied to.
func upstreamErrorMessage(body []byte) string {
	// error.message — OpenAI, Anthropic, and everyone who copied them.
	var nested struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &nested) == nil {
		if said := sayable(nested.Error.Message); said != "" {
			return said
		}
	}
	// The flatter shapes: {"error":"..."} , {"message":"..."} , {"detail":"..."}.
	var flat struct {
		Error   string `json:"error"`
		Message string `json:"message"`
		Detail  string `json:"detail"`
	}
	if json.Unmarshal(body, &flat) == nil {
		for _, raw := range []string{flat.Error, flat.Message, flat.Detail} {
			if said := sayable(raw); said != "" {
				return said
			}
		}
	}
	return "upstream error"
}

// repeatable reports whether a sentence is one we may hand a caller: non-empty,
// and naming no provider we buy from — by name, or by a link into their console.
func sayable(msg string) string {
	s := strings.ToLower(strings.TrimSpace(msg))
	if s == "" {
		return ""
	}
	if strings.Contains(s, "http://") || strings.Contains(s, "https://") {
		return ""
	}
	for _, vendor := range []string{"openrouter", "openai", "anthropic", "together",
		"fireworks", "groq", "deepseek", "digitalocean"} {
		if strings.Contains(s, vendor) {
			return ""
		}
	}
	// Naming an upstream and disclosing its credential are different harms, so
	// the vendor check above does not cover this one: a refusal that quotes the
	// key back carries no vendor name and no URL, and used to be repeated
	// verbatim. Returning the scrubbed message rather than a yes/no is what makes
	// the unscrubbed form unavailable to a caller.
	return object.RedactKeys(strings.TrimSpace(msg))
}

// AnthropicCountTokens implements POST /v1/messages/count_tokens. Claude Code
// calls it before a request; it returns {"input_tokens": N} for the given
// model + messages + tools.
// @Title AnthropicCountTokens
// @Tag Anthropic Compatible API
// @Description Anthropic-compatible token counting.
// @router /messages/count_tokens [post]
func (c *ApiController) AnthropicCountTokens() {
	token := c.Header("x-api-key")
	if token == "" {
		authHeader := c.Header("Authorization")
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
	parseErr := json.Unmarshal(c.Body(), &request)
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
	c.SetHeader("Content-Type", "application/json")
	c.Bytes(http.StatusOK, out)
}
