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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
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
	// Thinking is Anthropic extended-thinking config, read by
	// anthropicThinkingToReasoningEffort for the Anthropic→OpenAI translation. A
	// native upstream is sent the caller's own bytes (nativeRequest), never this
	// struct re-marshalled.
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

// AnthropicUsage tracks token counts as the Messages API reports them: the two
// cache counts are beside InputTokens, not inside it. input_tokens is what was read
// fresh; the cache counts are what was read from the cache and written to it.
type AnthropicUsage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_input_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_creation_input_tokens,omitempty"`
	// CacheWrites splits CacheWriteTokens by how long the entry is kept, which
	// the vendor prices apart.
	CacheWrites *CacheWrites `json:"cache_creation,omitempty"`
	// Iterations is one usage per time the model ran for this answer. Compaction
	// runs it more than once, and the counts above then cover the last run only.
	Iterations []AnthropicUsage `json:"iterations,omitempty"`
}

// CacheWrites is cache_creation: the cache writes kept five minutes and one hour.
type CacheWrites struct {
	Minutes int `json:"ephemeral_5m_input_tokens"`
	Hour    int `json:"ephemeral_1h_input_tokens"`
}

// merge takes each count u reports and keeps what was already known for a count it
// does not. A stream reports usage twice — message_start opens with the input side,
// message_delta closes with the output side — and either may repeat or omit a count.
func (a *AnthropicUsage) merge(u AnthropicUsage) {
	if u.InputTokens > 0 {
		a.InputTokens = u.InputTokens
	}
	if u.OutputTokens > 0 {
		a.OutputTokens = u.OutputTokens
	}
	if u.CacheReadTokens > 0 {
		a.CacheReadTokens = u.CacheReadTokens
	}
	if u.CacheWriteTokens > 0 {
		a.CacheWriteTokens = u.CacheWriteTokens
	}
	if u.CacheWrites != nil {
		a.CacheWrites = u.CacheWrites
	}
	if len(u.Iterations) > 0 {
		a.Iterations = u.Iterations
	}
}

// tally is an answer's usage as it is priced: tokens read fresh, written, read
// from the cache, and cache writes in five-minute-write units.
type tally struct{ in, out, read, write int }

// prompt is every input token, as a usage record counts it: read fresh, read from
// the cache and written to it.
func (t tally) prompt() int { return t.in + t.read + t.write }

// billed is u as it is priced for model.
//
// Every iteration is summed, and no count is billed below what the top level
// reports, so the answer is priced whether or not the vendor listed its runs and
// whether or not each run lists its cache counts.
//
// The usage record carries one cache-write count, so a one-hour write is folded in
// as the five-minute writes that cost the same at model's rates. The token count
// on the row reads high by that premium; the money is right.
func (u AnthropicUsage) billed(model string) tally {
	top := u.flat(model)
	if len(u.Iterations) == 0 {
		return top
	}
	var sum tally
	for _, it := range u.Iterations {
		f := it.flat(model)
		sum.in += f.in
		sum.out += f.out
		sum.read += f.read
		sum.write += f.write
	}
	return tally{max(sum.in, top.in), max(sum.out, top.out), max(sum.read, top.read), max(sum.write, top.write)}
}

// flat is u's own counts, without its iterations.
func (u AnthropicUsage) flat(model string) tally {
	hour := 0
	if u.CacheWrites != nil {
		hour = min(u.CacheWrites.Hour, u.CacheWriteTokens)
	}
	return tally{u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens - hour + hourAsMinutes(model, hour)}
}

// hourAsMinutes is n one-hour cache writes as the number of five-minute writes that
// cost the same for model, rounded up.
func hourAsMinutes(model string, n int) int {
	if n == 0 {
		return 0
	}
	p := getModelPrice(model)
	minutes := cacheWriteRate(p.InputPerMillion, p.CacheWritePerMillion)
	if minutes <= 0 {
		return n
	}
	return int(math.Ceil(float64(n) * p.InputPerMillion * cacheWriteHourMultiple / minutes))
}

// answerUsage is the usage a whole native answer reports, and whether it reported
// one at all.
func answerUsage(body []byte) (AnthropicUsage, bool) {
	var answer struct {
		Usage *AnthropicUsage `json:"usage"`
	}
	if json.Unmarshal(body, &answer) != nil || answer.Usage == nil {
		return AnthropicUsage{}, false
	}
	return *answer.Usage, true
}

// lost is what an answer is billed no less than: nothing when the vendor's final
// count arrived, and floor — the most it could have cost — when it did not. An
// answer cut off by the upstream, by an idle deadline, or by an error event mid-way
// was still generated, and invoiced, up to the point it stopped; the count of how
// far that was is exactly what did not arrive.
func lost(final bool, floor AnthropicUsage) *AnthropicUsage {
	if final {
		return nil
	}
	return &floor
}

// reservation is the most a /v1/messages call can use: its body read as tokens at
// 3.5 bytes each, rounded up — every byte counted as fresh input — and the
// completion ceiling it is held to.
func reservation(body []byte, completion int) AnthropicUsage {
	return AnthropicUsage{InputTokens: (2*len(body) + 6) / 7, OutputTokens: completion}
}

// holdCents is what reservation u holds against the caller's balance: its price at
// model's rates, or nothing for a route that costs nothing.
func holdCents(model string, u AnthropicUsage) int64 {
	if costsNothing(model, "") {
		return 0
	}
	return calculateCostCents(model, u.InputTokens, u.OutputTokens)
}

// completionCeiling is the most output a /v1/messages request can be answered with:
// the max_tokens it is sent with wherever it is proxied, and the QueryText
// pipeline's own cap where that pipeline serves it — a text-only request to an
// upstream that does not speak this API.
func completionCeiling(provider *object.Provider, request *AnthropicRequest) int {
	if model.Upstream(provider.Type) != model.Anthropic && len(request.Tools) == 0 && !requestHasMediaAnthropic(request) {
		return reserveCompletionTokens(request.MaxTokens)
	}
	return request.MaxTokens
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
		msgStart := map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id":      "msg_" + w.RequestID,
				"type":    "message",
				"role":    "assistant",
				"content": []any{},
				"model":   w.Model,
				"usage": map[string]any{
					"input_tokens":  0,
					"output_tokens": 0,
				},
			},
		}
		if err := w.writeSSE("message_start", msgStart); err != nil {
			return 0, err
		}

		// content_block_start
		blockStart := map[string]any{
			"type":  "content_block_start",
			"index": 0,
			"content_block": map[string]any{
				"type": "text",
				"text": "",
			},
		}
		if err := w.writeSSE("content_block_start", blockStart); err != nil {
			return 0, err
		}
	}

	// content_block_delta
	delta := map[string]any{
		"type":  "content_block_delta",
		"index": 0,
		"delta": map[string]any{
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
	blockStop := map[string]any{
		"type":  "content_block_stop",
		"index": 0,
	}
	if err := w.writeSSE("content_block_stop", blockStop); err != nil {
		return err
	}

	// message_delta
	msgDelta := map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason": "end_turn",
		},
		"usage": map[string]any{
			"input_tokens":  promptTokens,
			"output_tokens": completionTokens,
		},
	}
	if err := w.writeSSE("message_delta", msgDelta); err != nil {
		return err
	}

	// message_stop
	msgStop := map[string]any{
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
func (w *AnthropicWriter) writeSSE(event string, data any) error {
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
		if after, ok := strings.CutPrefix(authHeader, "Bearer "); ok {
			token = after
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
		badReq = fmt.Sprintf("Failed to parse request: %s", parseProblem(err))
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

	// ── Balance reservation (shared by the proxies and the QueryText path) ──
	// On the paid lane the ceiling is never above the one its seat was sized for.
	request.MaxTokens = laneTokens(c.Context(), clampMaxTokens(request.MaxTokens))
	var hold *budgetHold
	if authUser != nil {
		ledger := c.billingOrg(authUser)
		subject := authUser.PayerSubject(ledger)
		est := holdCents(request.Model, reservation(c.Body(), completionCeiling(provider, &request)))
		var ok bool
		if hold, ok = reserveFor(c.Context(), subject, est); !ok {
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
		// A family answers in its own pipeline, so a strict request cannot be proven
		// to reach a vendor as sent there (strict.go).
		if c.strictAsked() {
			c.refuseStrict("translation", "this model is served through a model family, not sent to its vendor as is; name a native Anthropic model")
			return
		}
		familyRefused = c.pipeToFamily(fam, "messages", "anthropic", request.Model, c.Body(), request.Stream, request.MaxTokens, orgId, authUser, isPremium, hold, requestStartTime)
		if familyRefused == nil {
			return
		}
		recordRefusals(c.takeSnapshot(authUser), request.Model, familyRefused, authUser, isPremium, request.Stream, requestId, requestStartTime)
	}

	// ── Proxy ─────────────────────────────────────────────────────────────
	// A native Anthropic upstream is always sent the caller's own request: the
	// QueryText pipeline below keeps one user turn, flattens every block to text
	// and drops every field it has no slot for, so it has nothing to offer a
	// vendor that speaks this API.
	//
	// Otherwise the proxy takes what the pipeline cannot carry: tools (it emits no
	// tool_use blocks) and media (it is text-only). A tool or media request the
	// family refused stops here: answering a tool call with prose, or images with an
	// answer about nothing, is worse than an honest refusal naming the reason.
	if model.Upstream(provider.Type) == model.Anthropic || len(request.Tools) > 0 || requestHasMediaAnthropic(&request) {
		if familyRefused != nil {
			err := exhausted(request.Model, familyRefused)
			c.respondAnthropicError("api_error", err.Error(), statusOf(err))
			return
		}
		if shut(c.Context(), provider) {
			err := laneOff(c.Context(), request.Model)
			c.respondAnthropicError("api_error", err.Error(), statusOf(err))
			return
		}
		c.proxyAnthropic(provider, &request, requestId, requestStartTime, authUser, isPremium, hold)
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
		defer hold.settle(0)
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
			hold.settle(calculateCostCentsWithCache(request.Model, modelResult.PromptTokenCount, modelResult.ResponseTokenCount, modelResult.CacheReadTokenCount, modelResult.CacheWriteTokenCount))
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
		// run reads hold by reference, so from here it settles the carried one.
		hold = hand(hold)
		_ = c.SendStreamWriter(run)
	} else {
		run(bufio.NewWriter(io.Discard))
	}
}

// carry moves a reservation into a stream writer and returns the hold the writer
// settles; the writer also defers settle(0), as the handler does, for the answers
// that end without a price.
//
// zip runs the writer on its own goroutine, and the handler's deferred settle(0)
// runs when the handler returns — before the stream has produced anything worth
// pricing. A hold settles once, so that settle(0) won: the reservation was released
// before the first byte streamed, and the answer's real cost never reached the
// ledger, so neither bound the spend of a caller with several streams open.
// Carried, the handler's hold is spent and its settle does nothing; the reservation
// stands until the writer settles it. fasthttp starts the writer when the stream is
// set, and a writer whose client has gone still runs to its end, so the
// reservation is always given back.
func hand(h *budgetHold) *budgetHold {
	if h == nil || !h.settled.CompareAndSwap(false, true) {
		return nil
	}
	return &budgetHold{subject: h.subject, est: h.est}
}

// proxyAnthropic forwards a /v1/messages request the QueryText pipeline cannot
// carry. A native Anthropic upstream is sent the caller's own request
// (nativeRequest) and its answer is relayed as it came; any other upstream is sent
// a full Anthropic→OpenAI translation and its answer translated back.
func (c *ApiController) proxyAnthropic(
	provider *object.Provider,
	request *AnthropicRequest,
	requestId string,
	requestStartTime time.Time,
	authUser *iam.User,
	isPremium bool,
	hold *budgetHold,
) {
	// Our id for this answer, and the only header of ours or the vendor's that goes
	// back with it besides its type: it is the key the usage row is filed under.
	c.SetHeader("X-Request-Id", requestId)

	// What the answer is billed at when it ends without the vendor's own count: the
	// most it could have cost. Read here, while the body is still this handler's.
	floor := reservation(c.Body(), request.MaxTokens)

	// For non-native Anthropic upstreams (DO-AI, OpenAI-compat, Local, etc.):
	// fully translate the request Anthropic→OpenAI (messages incl. tool_use /
	// tool_result / images, tools, tool_choice) and translate the response
	// OpenAI→Anthropic (SSE events or JSON) — never a raw OpenAI passthrough.
	if model.Upstream(provider.Type) != model.Anthropic {
		if c.strictAsked() {
			c.refuseStrict("translation", "this model's vendor speaks another API, so the request would be translated; name a native Anthropic model")
			return
		}
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
		c.proxyAnthropicViaOpenAI(provider, oaiReq, request, requestId, floor, requestStartTime, authUser, isPremium, hold)
		return
	}

	// Native Anthropic upstream: the caller's own request, addressed to the route's
	// upstream id. Built here, while the request buffer is still this handler's.
	byo, _ := providerBYO(provider, authUser)
	version, betas := c.anthropicHeaders()
	set := map[string]any{
		"model":      provider.SubType,
		"max_tokens": request.MaxTokens,
	}
	if c.strictAsked() && !c.provenNative(set) {
		return
	}
	req, err := nativeRequest(context.Background(), provider, c.Body(), set, byo, version, betas)
	if err != nil {
		c.respondAnthropicRefusal(err)
		return
	}

	resp, err := sendIdle(req)
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
	// billing below: both branches write a status and both then bill, and neither
	// question has a different answer for a stream than for a buffered response.
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		c.respondAnthropicRefusal(relay(request.Model, provider.Name, resp.StatusCode, b))
		return
	}

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
		held := hand(hold)
		_ = c.SendStreamWriter(func(w *bufio.Writer) {
			defer held.settle(0)
			defer upstream.Close()
			used, final := streamCaptureAnthropicUsage(upstream, w, func() { _ = w.Flush() })
			recordAnthropicToolUsage(snap, request, provider, authUser, isPremium, true, requestId, used, lost(final, floor), requestStartTime, held)
		})
		return
	}

	// A body that could not be read is not a success. Handing back what arrived
	// gives the caller truncated JSON under a 200, and the counts parsed from it
	// are whatever survived — which settles the hold at whatever that came to,
	// for an answer the upstream did charge us for.
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		c.respondAnthropicRefusal(relay(request.Model, provider.Name, http.StatusBadGateway, nil))
		return
	}
	used, final := answerUsage(respBody)
	recordAnthropicToolUsage(c.takeSnapshot(authUser), request, provider, authUser, isPremium, false, requestId, used, lost(final, floor), requestStartTime, hold)
	// A whole Messages answer is JSON. The type is stated rather than copied: a
	// connected account's URL is the customer's to set, and whatever it answers is
	// served from this origin.
	c.SetHeader("Content-Type", "application/json")
	c.Bytes(http.StatusOK, respBody)
}

// ── Native Anthropic passthrough ────────────────────────────────────────────

// anthropicVersion is the API version a native upstream is asked for when the
// caller names none; the upstream refuses a request that carries no version.
const anthropicVersion = "2023-06-01"

// anthropicHeaders is the caller's anthropic-version and every anthropic-beta,
// copied out of the request buffer. They are the caller's choice of API dialect and
// opt-in features, and the upstream reads the body by them, so the body is not the
// caller's request without them.
func (c *ApiController) anthropicHeaders() (version string, betas []string) {
	for _, v := range c.Fiber().Request().Header.PeekAll("anthropic-beta") {
		betas = append(betas, string(v))
	}
	return strings.Clone(strings.TrimSpace(c.Header("anthropic-version"))), betas
}

// nativeRequest is what a native Anthropic upstream receives for a /v1/messages
// call: the caller's body byte for byte except the fields in set, the caller's
// anthropic-version (anthropicVersion when there is none) and anthropic-beta, and
// the provider's credential. Nothing else of the caller's crosses — least of all
// the caller's own credential.
//
// set always carries `model`, the route's upstream id, and `max_tokens`, the
// ceiling the hold was reserved for. A max_tokens that passed validation is a
// plain decimal integer, so for a caller who asked for no more than that ceiling
// its bytes are the ones they sent.
//
// On the shared account anything not priced in tokens, or reaching state the
// account holds, is refused before anything is sent (shared); the caller's own
// connected account (byo) is sent whatever the caller wrote.
func nativeRequest(ctx context.Context, provider *object.Provider, body []byte, set map[string]any, byo bool, version string, betas []string) (*http.Request, error) {
	out, err := splice(body, set)
	if err != nil {
		return nil, err
	}
	if !byo {
		if err := shared(body, betas); err != nil {
			return nil, err
		}
	}
	base := strings.TrimRight(provider.ProviderUrl, "/")
	if base == "" {
		base = "https://api.anthropic.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/messages", bytes.NewReader(out))
	if err != nil {
		return nil, serverError("Failed to build upstream request: %s", err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	if version == "" {
		version = anthropicVersion
	}
	req.Header.Set("anthropic-version", version)
	for _, b := range betas {
		req.Header.Add("anthropic-beta", b)
	}
	upstream.Authorize(req, provider)
	return req, nil
}

// spelled are the top-level fields this process reads a request by: to route it,
// reserve for it, price it, choose how the answer is carried back, and decide
// whether it may be sent at all (shared).
var spelled = func() map[string]string {
	m := map[string]string{}
	for _, name := range []string{"model", "max_tokens", "messages", "system", "tools",
		"tool_choice", "temperature", "stream", "thinking", "speed", "inference_geo", "fallbacks"} {
		m[fold(name)] = name
	}
	return m
}()

// provenNative holds a strict /v1/messages request to the body the vendor would be
// sent — the caller's bytes with set spliced in — and either refuses it naming what
// would change or stamps the two digests that prove nothing did (strict.go).
func (c *ApiController) provenNative(set map[string]any) bool {
	upstream, err := splice(c.Body(), set)
	if err != nil {
		c.refuseStrict("not_canonical", err.Error())
		return false
	}
	invariant, field, sent, relayed, err := unchanged(c.Body(), upstream)
	if err != nil {
		c.refuseStrict("not_canonical", err.Error())
		return false
	}
	switch {
	case invariant == "":
	case field == "max_tokens":
		c.refuseStrict("ceiling_lowered", "max_tokens is above what this request may spend; ask for less")
		return false
	default:
		c.refuseStrict(invariant, fmt.Sprintf("%q would not reach the vendor as sent", field))
		return false
	}
	c.SetHeader(strictHeader, "1")
	c.SetHeader(requestSha, sent)
	c.SetHeader(upstreamSha, relayed)
	return true
}

// splice is body with each top-level field named in set given that value, and every
// other byte as the caller sent it: key order, whitespace, escapes and number
// spellings included, since any of them can be what a prompt-cache prefix or a
// signature was computed over.
//
// It refuses a body whose top-level fields this process and the upstream could read
// differently. encoding/json keeps the LAST of two equal keys and matches a key to
// a field ignoring case; another parser need do neither. The request is routed and
// priced on this process's reading and answered on the upstream's, so
// `"stream":false,"stream":true`, or a lone "Stream", is an answer streamed to a
// relay that expected one JSON body, found no usage in it, and billed nothing. A
// body is sent only when both readings are the same one.
func splice(body []byte, set map[string]any) ([]byte, error) {
	out := make([]byte, 0, len(body)+32)
	seen := map[string]bool{}
	last := 0
	err := members(body, func(key string, value json.RawMessage, end int) error {
		folded := fold(key)
		if seen[folded] {
			return modelError("the field %q appears more than once", key)
		}
		seen[folded] = true
		if name, ok := spelled[folded]; ok && name != key {
			return modelError("the field %q must be spelled %q", key, name)
		}
		v, ok := set[key]
		if !ok {
			return nil
		}
		start := end - len(value)
		if start < last || !bytes.Equal(body[start:end], value) {
			return serverError("the request could not be addressed to its upstream")
		}
		b, err := json.Marshal(v)
		if err != nil {
			return serverError("the request could not be addressed to its upstream: %s", err.Error())
		}
		out = append(append(out, body[last:start]...), b...)
		last = end
		return nil
	})
	if err != nil {
		return nil, err
	}
	for key := range set {
		if !seen[fold(key)] {
			return nil, modelError("%s is required", key)
		}
	}
	return append(out, body[last:]...), nil
}

// members calls fn with each member of the JSON object obj in order, duplicates
// included, and the offset in obj just past the member's value. obj must be one
// object and nothing after it but whitespace.
func members(obj []byte, fn func(key string, value json.RawMessage, end int) error) error {
	dec := json.NewDecoder(bytes.NewReader(obj))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return modelError("the request body must be a JSON object")
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return modelError("Failed to parse request: %s", parseProblem(err))
		}
		key, _ := t.(string)
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return modelError("Failed to parse request: %s", parseProblem(err))
		}
		if err := fn(key, value, int(dec.InputOffset())); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil {
		return modelError("Failed to parse request: %s", parseProblem(err))
	}
	if len(bytes.TrimSpace(obj[dec.InputOffset():])) > 0 {
		return modelError("the request body continues after its JSON object")
	}
	return nil
}

// sharedFields are the top-level fields a request may carry on the shared account,
// each with the values it may take there (nil: any). They are the fields the vendor
// prices in tokens and that touch nothing held in the account but the answer
// itself, so the route's token price covers them. Anything else — a new field, a
// faster lane, a pinned region, a fallback model, a container, a remote MCP server,
// cache diagnostics naming another message — is served on the caller's own
// connected account, where the vendor bills the caller and holds only the caller's
// state.
var sharedFields = map[string][]string{
	"model": nil, "max_tokens": nil, "messages": nil, "system": nil,
	"stop_sequences": nil, "stream": nil, "temperature": nil, "top_p": nil, "top_k": nil,
	"tools": nil, "tool_choice": nil, "thinking": nil, "metadata": nil,
	"output_config": nil, "context_management": nil, "cache_control": nil,
	"service_tier":  {"auto", "standard_only"},
	"speed":         {"standard"},
	"inference_geo": {"global"},
}

// sharedTools are the tool types a request may declare on the shared account: its
// own tools, and the vendor's that the caller runs and that are priced in tokens
// alone. Search, code execution, the advisor and remote MCP toolsets are billed per
// use or run elsewhere; web fetch and tool search run in the vendor's own loop, which
// reads the whole prompt again on every pass, so what one request costs is not
// bounded by what it sends (quote.go). None of them is on it.
var sharedTools = []string{"bash_", "text_editor_", "computer_", "memory_"}

// sharedBetas are the anthropic-beta features a request may turn on on the shared
// account: the ones priced in tokens whose request is read once, with the one-hour
// cache TTL priced apart (hourAsMinutes). Compaction and web fetch run the model again
// over the prompt, and are not on it.
var sharedBetas = fieldSet(
	"prompt-caching-2024-07-31", "extended-cache-ttl-2025-04-11",
	"interleaved-thinking-2025-05-14", "fine-grained-tool-streaming-2025-05-14",
	"token-efficient-tools-2025-02-19", "output-128k-2025-02-19",
	"context-management-2025-06-27",
	"structured-outputs-2025-11-13", "claude-code-20250219",
	"thinking-display-updates-2026-08-18", "mid-conversation-output-config-2026-07-01",
	"mid-conversation-system-clear-at-2026-08-21",
	"computer-use-2025-01-24", "computer-use-2025-11-24",
)

// shared refuses what the shared account does not serve: a field, value, tool or
// beta off the lists above, a context edit that compacts (the model run again over
// the prompt), or a reference to a file held in the account. Every
// answer on it is priced from the tokens it reports at the rate of the model the
// caller named, and the account is one workspace for every tenant, so what is not
// priced in tokens is served at our cost and what reaches the account's stored
// state reaches every tenant's.
//
// splice has already refused a top-level field spelled any way but one, so each is
// read as the upstream reads it. A tool object has no such guarantee, so every key
// in it that folds to "type" is read, not only the one encoding/json would keep.
func shared(body []byte, betas []string) error {
	refuse := func(format string, a ...any) error {
		return forbiddenError("%s is not served on the shared account; it is served on your organization's own connected account", fmt.Sprintf(format, a...))
	}
	var tools, edits []json.RawMessage
	err := members(body, func(key string, value json.RawMessage, _ int) error {
		allowed, ok := sharedFields[key]
		if !ok {
			return refuse("the field %q", key)
		}
		if key == "tools" {
			if err := json.Unmarshal(value, &tools); err != nil {
				return modelError("tools: %s", err.Error())
			}
		}
		if key == "context_management" && string(value) != "null" {
			if err := members(value, func(k string, v json.RawMessage, _ int) error {
				var some []json.RawMessage
				if fold(k) != fold("edits") || string(v) == "null" {
					return nil
				}
				if err := json.Unmarshal(v, &some); err != nil {
					return modelError("context_management: %s", err.Error())
				}
				edits = append(edits, some...)
				return nil
			}); err != nil {
				return err
			}
		}
		if allowed == nil || string(value) == "null" {
			return nil
		}
		var v string
		if json.Unmarshal(value, &v) != nil || !slices.Contains(allowed, v) {
			return refuse("%s %s", key, value)
		}
		return nil
	})
	if err != nil {
		return err
	}
	typ := fold("type")
	for _, edit := range edits {
		err := members(edit, func(key string, value json.RawMessage, _ int) error {
			var name string
			if fold(key) != typ {
				return nil
			}
			if json.Unmarshal(value, &name) != nil || strings.HasPrefix(strings.ToLower(name), "compact") {
				return refuse("the context edit %s", value)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	for _, tool := range tools {
		err := members(tool, func(key string, value json.RawMessage, _ int) error {
			if fold(key) != typ {
				return nil
			}
			var name string
			if json.Unmarshal(value, &name) != nil {
				return refuse("the tool type %s", value)
			}
			if name == "custom" || slices.ContainsFunc(sharedTools, func(p string) bool { return strings.HasPrefix(name, p) }) {
				return nil
			}
			return refuse("the %s tool", name)
		})
		if err != nil {
			return err
		}
	}
	for _, line := range betas {
		for b := range strings.SplitSeq(line, ",") {
			if b = strings.TrimSpace(b); b != "" && !sharedBetas[b] {
				return refuse("the %s beta", b)
			}
		}
	}
	if held, err := heldFile(body); err != nil {
		return modelError("Failed to parse request: %s", parseProblem(err))
	} else if held {
		return refuse("a file_id")
	}
	return nil
}

// heldFile reports whether any object in body names a file the account holds: a
// file source or a container upload, anywhere the body can carry one. One pass over
// the tokens, so its cost is the body's length whatever its depth. Every key that
// folds to "type" is read, as in shared.
func heldFile(body []byte) (bool, error) {
	type frame struct {
		obj, key, id, file bool
		last               string
	}
	typ, id := fold("type"), fold("file_id")
	var stack []*frame
	dec := json.NewDecoder(bytes.NewReader(body))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if d, ok := tok.(json.Delim); ok {
			if d == '{' || d == '[' {
				stack = append(stack, &frame{obj: d == '{', key: d == '{'})
				continue
			}
			f := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if f.id && f.file {
				return true, nil
			}
			if n := len(stack); n > 0 && stack[n-1].obj {
				stack[n-1].key = true
			}
			continue
		}
		if len(stack) == 0 {
			continue
		}
		top := stack[len(stack)-1]
		if top.obj && top.key {
			top.last, top.key = fold(tok.(string)), false
			top.id = top.id || top.last == id
			continue
		}
		if s, ok := tok.(string); ok && top.obj && top.last == typ && (s == "file" || s == "container_upload") {
			top.file = true
		}
		if top.obj {
			top.key = true
		}
	}
}

// upstreamIdle is how long a proxied upstream may send nothing — before its
// response starts, or between two reads of its body — before the request is
// abandoned. It bounds silence, not length: an answer that keeps arriving is never
// cut however long it runs, and one that stalls is cut wherever it stopped.
var upstreamIdle = 120 * time.Second

// sendIdle sends req with the idle deadline upstreamIdle in place of a deadline on
// the whole exchange, which cut every stream still running at two minutes.
func sendIdle(req *http.Request) (*http.Response, error) {
	idle := upstreamIdle
	ctx, cancel := context.WithCancel(req.Context())
	timer := time.AfterFunc(idle, cancel)
	resp, err := (&http.Client{}).Do(req.WithContext(ctx))
	if err != nil {
		timer.Stop()
		cancel()
		return nil, err
	}
	timer.Reset(idle)
	resp.Body = &idleBody{ReadCloser: resp.Body, timer: timer, idle: idle, cancel: cancel}
	return resp, nil
}

// idleBody is a response body whose every read that returns bytes restarts the
// idle deadline.
type idleBody struct {
	io.ReadCloser
	timer  *time.Timer
	idle   time.Duration
	cancel context.CancelFunc
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.timer.Reset(b.idle)
	}
	return n, err
}

func (b *idleBody) Close() error {
	b.timer.Stop()
	b.cancel()
	return b.ReadCloser.Close()
}

// proxyAnthropicViaOpenAI sends a translated OpenAI request to an OpenAI-compatible
// upstream and converts the response back into Anthropic Messages format — proper
// Anthropic SSE events on the streaming path, a content-block JSON body otherwise.
// This is the total translation that replaces the old raw-OpenAI passthrough.
func (c *ApiController) proxyAnthropicViaOpenAI(
	provider *object.Provider,
	oaiReq *openai.ChatCompletionRequest,
	request *AnthropicRequest,
	requestId string,
	floor AnthropicUsage,
	requestStartTime time.Time,
	authUser *iam.User,
	isPremium bool,
	hold *budgetHold,
) {
	// Force a final usage chunk on the streaming path so tool calls bill for real
	// token counts: a funded key must never get free premium inference by asking
	// for stream:true.
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

	resp, err := sendIdle(req)
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
		held := hand(hold)
		_ = c.SendStreamWriter(func(w *bufio.Writer) {
			defer held.settle(0)
			defer upstream.Close()
			emit := func(event string, data any) error {
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
			prompt, completion, total := translateOpenAIStream(upstream, emit, request.Model, requestId)
			// Tokenizer fallback so a successful streamed tool call is never billed $0.
			if prompt == 0 {
				if pt, e := model.OpenaiNumTokensFromMessages(oaiReq.Messages, request.Model); e == nil {
					prompt = pt
				}
			}
			// The usage chunk is the stream's last; a stream that ended without one
			// (the upstream cut off, the client left and the relay stopped reading)
			// is billed at what it could have cost.
			recordAnthropicToolUsage(snap, request, provider, authUser, isPremium, true, requestId,
				AnthropicUsage{InputTokens: prompt, OutputTokens: completion}, lost(total > 0 || completion > 0, floor), requestStartTime, held)
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
	recordAnthropicToolUsage(c.takeSnapshot(authUser), request, provider, authUser, isPremium, false, requestId,
		AnthropicUsage{InputTokens: prompt, OutputTokens: completion}, lost(prompt > 0 || completion > 0, floor), requestStartTime, hold)
}

// recordAnthropicToolUsage settles the budget hold and records usage + trace for a
// proxied Anthropic request, native or translated, streamed or not, so billing
// lives in exactly one place. Some callers run inside a stream writer, where the
// request context is already released, so it reads the request's outliving parts
// from a snapshot rather than from a controller.
//
// The price is request.Model's — the model the caller asked for and the route
// is priced at — never the upstream id the request was addressed to.
//
// floor is non-nil when the answer ended without the vendor's final count, and
// then no count is billed below it (lost).
func recordAnthropicToolUsage(
	snap snapshot,
	request *AnthropicRequest, provider *object.Provider, authUser *iam.User,
	isPremium, stream bool, requestId string, used AnthropicUsage, floor *AnthropicUsage,
	requestStartTime time.Time, hold *budgetHold,
) {
	b := used.billed(request.Model)
	if floor != nil {
		b.in = max(b.in, floor.InputTokens)
		b.out = max(b.out, floor.OutputTokens)
	}
	actualCents := calculateCostCentsWithCache(request.Model, b.prompt(), b.out, b.read, b.write)
	if authUser != nil {
		rec := &usageRecord{
			Owner: snap.org, Organization: authUser.Owner, Model: request.Model, Provider: provider.Name,
			Origin:       provider.Origin(),
			PromptTokens: b.prompt(), CompletionTokens: b.out, TotalTokens: b.prompt() + b.out,
			CacheReadTokens: b.read, CacheWriteTokens: b.write,
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
		if after, ok := strings.CutPrefix(authHeader, "Bearer "); ok {
			token = after
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
		c.respondAnthropicError("invalid_request_error", "Failed to parse request: "+parseProblem(parseErr), 400)
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

	out, _ := json.Marshal(tokenCount{Input: n})
	c.SetHeader("Content-Type", "application/json")
	c.Bytes(http.StatusOK, out)
}

// tokenCount is what /v1/messages/count_tokens answers: what the prompt would
// cost before it is sent.
type tokenCount struct {
	Input int `json:"input_tokens"`
}
