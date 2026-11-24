// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package controllers

// This file implements the OpenAI Responses API as a total adapter around the
// existing Chat Completions controller. Keeping auth, routing, failover,
// metering and balance reservation in ChatCompletions avoids creating a second
// policy path while giving Responses-only clients (notably current Codex) the
// wire protocol they require.

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/go-openai"
	"github.com/klauspost/compress/zstd"
)

// ErrPreviousResponseNotFound is returned when a Responses request names a
// previous_response_id that is not stored in the ThreadStore.
var ErrPreviousResponseNotFound = errors.New("previous_response_id not found")

// OpenAIResponsesRequest is the subset of POST /v1/responses used by Codex and
// OpenAI SDKs. Unknown fields are deliberately accepted for forward
// compatibility; fields that Chat Completions cannot represent are ignored.
type OpenAIResponsesRequest struct {
	Model               string            `json:"model"`
	Instructions        string            `json:"instructions,omitempty"`
	Input               json.RawMessage   `json:"input"`
	Tools               []responsesTool   `json:"tools,omitempty"`
	ToolChoice          json.RawMessage   `json:"tool_choice,omitempty"`
	ParallelToolCalls   *bool             `json:"parallel_tool_calls,omitempty"`
	Stream              bool              `json:"stream,omitempty"`
	MaxOutputTokens     int               `json:"max_output_tokens,omitempty"`
	Temperature         *float32          `json:"temperature,omitempty"`
	TopP                *float32          `json:"top_p,omitempty"`
	Store               bool              `json:"store,omitempty"`
	PreviousResponseID  string            `json:"previous_response_id,omitempty"`
	ThreadID            string            `json:"thread_id,omitempty"`
	Branch              string            `json:"branch,omitempty"`
	ContextTargetTokens int               `json:"context_target_tokens,omitempty"`
	Metadata            map[string]string `json:"metadata,omitempty"`
	Reasoning           json.RawMessage   `json:"reasoning,omitempty"`
	Text                json.RawMessage   `json:"text,omitempty"`

	// spaces names, for each tool a namespace holds, its flat chat name's
	// namespace and own name, so a call coming back is answered as the client
	// declared it.
	spaces map[string]spaced
}

// spaced is a tool inside a namespace: Codex declares each MCP server's tools
// as one `namespace` and routes a call by its namespace and name.
type spaced struct{ Namespace, Name string }

// flatName is a namespaced tool's chat function name. Chat Completions has no
// namespaces, so the two are joined, within the 64 characters a function name
// may have; the same pair always gives the same name, so a call in the history
// and a call in the answer agree.
func flatName(namespace, name string) string {
	flat := namespace + "__" + strings.TrimLeft(name, "_")
	if len(flat) <= 64 {
		return flat
	}
	sum := sha256.Sum256([]byte(namespace + "\x00" + name))
	return flat[:55] + "_" + hex.EncodeToString(sum[:4])
}

// unflat answers a chat call by the namespace and name its client declared.
func (r *OpenAIResponsesRequest) unflat(call *responsesToolCallState) {
	if r == nil {
		return
	}
	if s, ok := r.spaces[call.Name]; ok {
		call.Namespace, call.Name = s.Namespace, s.Name
	}
}

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      bool            `json:"strict,omitempty"`
	Format      json.RawMessage `json:"format,omitempty"`
	// Tools are what a `namespace` holds.
	Tools []responsesTool `json:"tools,omitempty"`
}

type responsesInputItem struct {
	Type      string          `json:"type"`
	ID        string          `json:"id,omitempty"`
	Role      string          `json:"role,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Namespace string          `json:"namespace,omitempty"`
	Arguments string          `json:"arguments,omitempty"`
	Input     string          `json:"input,omitempty"`
	Output    json.RawMessage `json:"output,omitempty"`
}

type responsesContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

// Responses implements POST /v1/responses. The converted request is completed by
// the chat path, which is handed a sink saying where the answer goes: a stream is
// translated as it is produced, a whole body is translated entire.
func (c *ApiController) Responses() {
	authHeader := c.Header("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		c.ResponseErrorWithStatus(http.StatusUnauthorized, "Invalid API key format. Expected 'Bearer API_KEY'")
		return
	}
	token := strings.TrimPrefix(authHeader, "Bearer ")
	if isPublishableKey(token) {
		c.ResponseErrorWithStatus(http.StatusForbidden, "Publishable keys cannot access /v1/responses. Use a secret key.")
		return
	}

	// A Responses request is served as a chat completion, which is a translation, so
	// one that asked to be served unchanged is refused — after its credential is read,
	// like every other refusal here.
	if c.strictAsked() {
		if authErr := c.authenticate(token); authErr != nil {
			c.ResponseAuthError(authErr)
			return
		}
		c.refuseStrict("translation", "/v1/responses is served as a chat completion; send the request to /v1/chat/completions")
		return
	}

	// Resolve verified caller identity when available so stateful thread lookup
	// (previous_response_id / thread_id) and storage (store: true) enforce ACLs
	// and attribute ownership accurately.
	var callerOrg, callerUser string
	if u := c.principalUser(); u != nil {
		callerOrg = strings.TrimSpace(c.GetOrg())
		callerUser = u.Name
	} else if !isJwtToken(token) {
		if prov, pErr := object.GetProviderByProviderKey(token, c.GetAcceptLanguage()); pErr == nil && prov != nil && strings.TrimSpace(prov.Owner) != "" {
			callerOrg = strings.TrimSpace(prov.Owner)
			callerUser = "provider:" + prov.Name
		}
	}

	// c.Body() is the body the server already decoded (zstd included, within its body
	// limit), so it is read as plain whatever Content-Encoding the caller sent.
	call, err := ReadResponsesForCaller(c.Body(), "", callerOrg, callerUser, strings.TrimSpace(c.Header("X-Hanzo-Thread")))
	if err != nil {
		// Authenticate before reporting: whatever the body says, a caller without
		// a valid key is told that first, and learns nothing about how we read it.
		if authErr := c.authenticate(token); authErr != nil {
			c.ResponseAuthError(authErr)
			return
		}
		// A body over the bound is 413, not 400: it is not malformed, it is too
		// big, and only one of those tells the caller to send less.
		if errors.Is(err, zstd.ErrDecoderSizeExceeded) {
			c.ResponseErrorWithStatus(http.StatusRequestEntityTooLarge, fmt.Sprintf(
				"request body exceeds the %d MiB limit", MaxDecoded>>20))
			return
		}
		if errors.Is(err, ErrPreviousResponseNotFound) || errors.Is(err, object.ErrThreadNotFound) || errors.Is(err, object.ErrBranchNotFound) {
			c.ResponseErrorWithStatus(http.StatusNotFound, err.Error())
			return
		}
		if errors.Is(err, object.ErrForbidden) {
			c.ResponseErrorWithStatus(http.StatusForbidden, err.Error())
			return
		}
		c.ResponseErrorWithStatus(http.StatusBadRequest, err.Error())
		return
	}

	// The chat request on the wire, because the completion reads its own body. This
	// is the request being made, not a place being patched.
	req := c.Fiber().Request()
	req.Header.Del("Content-Encoding")
	req.SetBody(call.Chat)
	req.Header.Del("Content-Length")
	if call.threadID != "" && strings.TrimSpace(c.Header("X-Hanzo-Thread")) == "" {
		req.Header.Set("X-Hanzo-Thread", call.threadID)
	}
	if call.responseID != "" {
		c.SetHeader("X-Hanzo-Response-Id", call.responseID)
	}

	// This endpoint IS a chat completion, read in a second dialect. So it asks for
	// one and says what shape the answer must take, rather than swapping the writer
	// out of the request context and reading whatever came back through it.
	c.chatCompletions(callerBearer, c.responsesAnswer(call))
}

// ResponsesCall is a /v1/responses request read as the chat completion it asks
// for. Chat is that completion's body; Stream and Whole write its answer back in
// the Responses dialect. It is the one translation: ai serves Chat itself, and a
// host that serves chat in-process (cloud's zen) serves it there and answers
// through the same two functions.
type ResponsesCall struct {
	Chat         []byte
	req          OpenAIResponsesRequest
	kinds        map[string]string
	responseID   string
	threadID     string
	branch       string
	callerOrg    string
	callerUser   string
	newInputMsgs []openai.ChatCompletionMessage
}

// ResponseID returns the response ID minted for this Responses turn.
func (r *ResponsesCall) ResponseID() string {
	if r == nil {
		return ""
	}
	return r.responseID
}

// ReadResponses reads a Responses body, zstd-compressed when encoding says so.
// Every error is the caller's; one for a body too large to expand wraps
// zstd.ErrDecoderSizeExceeded.
func ReadResponses(body []byte, encoding string) (*ResponsesCall, error) {
	return ReadResponsesForCaller(body, encoding, "", "", "")
}

// ReadResponsesForCaller reads a Responses body and resolves any stateful
// previous_response_id or thread_id continuation against ThreadStore, enforcing
// cross-account ACLs when callerOrg is known and projecting 10M+ histories
// through object.ProjectWindow.
func ReadResponsesForCaller(body []byte, encoding, callerOrg, callerUser, headerThreadID string) (*ResponsesCall, error) {
	if strings.EqualFold(strings.TrimSpace(encoding), "zstd") {
		decoded, err := decodeResponsesZstd(body)
		if err != nil {
			return nil, fmt.Errorf("Failed to decompress zstd request: %w", err)
		}
		body = decoded
	}
	call := &ResponsesCall{
		responseID: "resp_" + uuid.NewString(),
		callerOrg:  strings.TrimSpace(callerOrg),
		callerUser: strings.TrimSpace(callerUser),
	}
	if err := json.Unmarshal(body, &call.req); err != nil {
		return nil, fmt.Errorf("Failed to parse Responses request: %w", err)
	}
	if call.req.Model == "" {
		return nil, errors.New("model is required")
	}
	chat, kinds, err := responsesToChatRequest(&call.req)
	if err != nil {
		return nil, err
	}
	call.newInputMsgs = append([]openai.ChatCompletionMessage(nil), chat.Messages...)

	// Resolve stateful continuation from previous_response_id or thread_id.
	store := object.DefaultThreadStore()
	branch := strings.TrimSpace(call.req.Branch)
	if branch == "" {
		branch = object.DefaultBranchName
	}
	var historyChunks []object.Chunk
	switch {
	case strings.TrimSpace(call.req.PreviousResponseID) != "":
		prevID := strings.TrimSpace(call.req.PreviousResponseID)
		tid, br, cid, ok := store.LookupResponse(prevID)
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrPreviousResponseNotFound, prevID)
		}
		thr, gErr := store.GetThread(tid)
		if gErr != nil {
			return nil, gErr
		}
		if call.callerOrg != "" && !object.Can(thr, call.callerOrg, call.callerUser, object.ThreadScopeRead) {
			return nil, fmt.Errorf("%w: caller cannot read thread %s", object.ErrForbidden, tid)
		}
		historyChunks, err = store.GetBranchChunksUpTo(tid, br, cid)
		if err != nil {
			return nil, err
		}
		call.threadID = tid
		call.branch = br
	case strings.TrimSpace(call.req.ThreadID) != "" || headerThreadID != "":
		tid := strings.TrimSpace(call.req.ThreadID)
		if tid == "" {
			tid = headerThreadID
		}
		thr, gErr := store.GetThread(tid)
		if gErr != nil {
			// If thread_id was explicitly supplied in the JSON body, fail with 404.
			// If it only arrived as an advisory header, allow proceeding statelessly.
			if strings.TrimSpace(call.req.ThreadID) != "" {
				return nil, gErr
			}
		} else {
			if call.callerOrg != "" && !object.Can(thr, call.callerOrg, call.callerUser, object.ThreadScopeRead) {
				return nil, fmt.Errorf("%w: caller cannot read thread %s", object.ErrForbidden, tid)
			}
			historyChunks, err = store.GetBranchChunks(tid, branch)
			if err != nil {
				return nil, err
			}
			call.threadID = tid
			call.branch = branch
		}
	default:
		call.branch = branch
	}

	if len(historyChunks) > 0 {
		targetTokens := call.req.ContextTargetTokens
		if targetTokens <= 0 {
			targetTokens = 128_000
		}
		query := extractQueryFromMessages(call.newInputMsgs)
		projected := object.ProjectWindow(historyChunks, targetTokens, query)
		histMsgs := chunksToChatMessages(projected.Chunks)
		chat.Messages = mergeHistoryAndNewMessages(histMsgs, chat.Messages)
	}

	if call.Chat, err = json.Marshal(chat); err != nil {
		return nil, err
	}
	if call.Chat, err = carry(call.Chat, body); err != nil {
		return nil, err
	}
	call.kinds = kinds
	return call, nil
}

func extractQueryFromMessages(msgs []openai.ChatCompletionMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == openai.ChatMessageRoleUser && strings.TrimSpace(msgs[i].Content) != "" {
			return msgs[i].Content
		}
	}
	return ""
}

func chunksToChatMessages(chunks []object.Chunk) []openai.ChatCompletionMessage {
	msgs := make([]openai.ChatCompletionMessage, 0, len(chunks))
	for _, ch := range chunks {
		role := ch.Role
		if role == "" {
			role = openai.ChatMessageRoleUser
		}
		m := openai.ChatCompletionMessage{
			Role:       role,
			Content:    ch.Content,
			ToolCallID: ch.ToolCallID,
		}
		if len(ch.ToolCalls) > 0 {
			m.ToolCalls = make([]openai.ToolCall, 0, len(ch.ToolCalls))
			for _, tc := range ch.ToolCalls {
				tt := openai.ToolType(tc.Type)
				if tt == "" {
					tt = openai.ToolTypeFunction
				}
				m.ToolCalls = append(m.ToolCalls, openai.ToolCall{
					ID:   tc.ID,
					Type: tt,
					Function: openai.FunctionCall{
						Name:      tc.Name,
						Arguments: tc.Arguments,
					},
				})
			}
		}
		msgs = append(msgs, m)
	}
	return msgs
}

func mergeHistoryAndNewMessages(hist, next []openai.ChatCompletionMessage) []openai.ChatCompletionMessage {
	if len(hist) == 0 {
		return next
	}
	// Avoid duplicating an identical leading system prompt when instructions are resent.
	if len(next) > 0 && next[0].Role == openai.ChatMessageRoleSystem &&
		hist[0].Role == openai.ChatMessageRoleSystem && hist[0].Content == next[0].Content {
		next = next[1:]
	}
	out := make([]openai.ChatCompletionMessage, 0, len(hist)+len(next))
	out = append(out, hist...)
	out = append(out, next...)
	return out
}

func (r *ResponsesCall) persistTurn(responseID, assistantText string, toolCalls []object.ThreadToolCall) {
	if r == nil || (!r.req.Store && r.threadID == "") {
		return
	}
	store := object.DefaultThreadStore()
	org := r.callerOrg
	if org == "" {
		org = "default"
	}
	tid := r.threadID
	branch := r.branch
	if branch == "" {
		branch = object.DefaultBranchName
	}
	if tid == "" {
		thr, err := store.CreateThread(org, r.callerUser, "", r.req.Metadata)
		if err != nil {
			return
		}
		tid = thr.ID
		r.threadID = tid
	} else {
		// Verify append permission when writing to an existing thread.
		if thr, err := store.GetThread(tid); err == nil && r.callerOrg != "" {
			if !object.Can(thr, r.callerOrg, r.callerUser, object.ThreadScopeAppend) {
				return
			}
		}
	}

	existing, _ := store.GetBranchChunks(tid, branch)
	for i, m := range r.newInputMsgs {
		// Skip duplicating an identical leading system prompt already at head[0].
		if i == 0 && m.Role == openai.ChatMessageRoleSystem && len(existing) > 0 &&
			existing[0].Role == "system" && existing[0].Content == m.Content {
			continue
		}
		var tcs []object.ThreadToolCall
		for _, tc := range m.ToolCalls {
			tcs = append(tcs, object.ThreadToolCall{
				ID:        tc.ID,
				Type:      string(tc.Type),
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			})
		}
		_, _, _ = store.AppendChunk(tid, branch, nil, object.Chunk{
			Role:       m.Role,
			Content:    m.Content,
			ToolCalls:  tcs,
			ToolCallID: m.ToolCallID,
			Actor:      r.callerUser,
			Org:        org,
		})
	}
	_, _, _ = store.AppendChunk(tid, branch, nil, object.Chunk{
		Role:       openai.ChatMessageRoleAssistant,
		Content:    assistantText,
		ToolCalls:  toolCalls,
		ResponseID: responseID,
		Actor:      r.callerUser,
		Org:        org,
		Model:      r.req.Model,
	})
}

// carry puts into the chat body what go-openai's struct cannot hold, read from the
// Responses body as the caller wrote it: a temperature or top_p of zero (omitempty
// drops both, so a caller who asked for greedy sampling got the vendor's default),
// the prompt cache key (chat's own field of that name, which keeps a conversation's
// turns on the cache that holds its prefix), the structured-output format
// (text.format, chat's response_format) and the reasoning effort.
func carry(chat, body []byte) ([]byte, error) {
	var in struct {
		CacheKey    json.RawMessage `json:"prompt_cache_key"`
		Temperature json.RawMessage `json:"temperature"`
		TopP        json.RawMessage `json:"top_p"`
		Text        struct {
			Format json.RawMessage `json:"format"`
		} `json:"text"`
		Reasoning struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, fmt.Errorf("text and reasoning must be objects: %w", err)
	}
	if f := unpricedField(body); f != "" {
		return nil, fmt.Errorf("%q is not accepted: it would buy something this model's price does not cover", f)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(chat, &out); err != nil {
		return nil, err
	}
	for k, v := range map[string]json.RawMessage{"temperature": in.Temperature, "top_p": in.TopP, "prompt_cache_key": in.CacheKey} {
		if len(v) > 0 && string(v) != "null" {
			out[k] = v
		}
	}
	format, err := chatFormat(in.Text.Format)
	if err != nil {
		return nil, err
	}
	if format != nil {
		out["response_format"] = format
	}
	if in.Reasoning.Effort != "" {
		out["reasoning_effort"], _ = json.Marshal(in.Reasoning.Effort)
	}
	return json.Marshal(out)
}

// chatFormat is a Responses text.format in chat's response_format shape, or nil for
// plain text. The Responses form holds the schema's fields beside its type; chat's
// holds them under json_schema.
func chatFormat(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var f map[string]json.RawMessage
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("text.format: %w", err)
	}
	var kind string
	_ = json.Unmarshal(f["type"], &kind)
	switch kind {
	case "", "text":
		return nil, nil
	case "json_object":
		return json.RawMessage(`{"type":"json_object"}`), nil
	case "json_schema":
		delete(f, "type")
		schema, err := json.Marshal(f)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]json.RawMessage{"type": json.RawMessage(`"json_schema"`), "json_schema": schema})
	}
	return nil, fmt.Errorf("text.format type %q is not supported", kind)
}

// Stream writes a chat SSE stream to w as Responses events, as it is produced,
// while recording every frame in the resumable stream hub keyed by response_id.
func (r *ResponsesCall) Stream(w io.Writer) io.WriteCloser {
	b := newResponsesBridge(w, &r.req, r.kinds)
	b.call = r
	if r != nil && r.responseID != "" {
		b.responseID = r.responseID
		b.streamRec = DefaultResponseStreamHub().Register(r.responseID, r.callerOrg, r.callerUser, r.threadID)
	}
	return b
}

// Whole translates one finished chat completion into a Responses object and
// persists the turn to ThreadStore when store:true or a thread is active.
func (r *ResponsesCall) Whole(chat []byte) ([]byte, error) {
	return openAIChatResponseToResponsesWithCall(chat, &r.req, r.kinds, r)
}

// responsesAnswer is the Responses shape of a chat completion: a stream is
// decorated as it is produced, a whole body is translated entire.
func (c *ApiController) responsesAnswer(call *ResponsesCall) *sink {
	return &sink{
		wrap:  func(w io.Writer) io.Writer { return call.Stream(w) },
		whole: call.Whole,
	}
}

// decodeResponsesZstd expands a zstd-framed Responses body, bounded by what it
// expands to. It uses a pure-Go decoder because Cloud release binaries are built
// with CGO_ENABLED=0. Current Codex clients compress large requests with zstd.
//
// The bound is MaxDecoded, the same ceiling every other body gets, and it has
// to be stated: the decoder's own default is 64 GiB. Capping the COMPRESSED
// input caps nothing that costs memory here — zstd's ratio on repetitive input
// runs to thousands to one, so the 64 MiB that reaches this function is already
// enough to ask for more heap than any pod has. The decode also runs before the
// token is validated, so whatever it allocates, a caller holding nothing but a
// syntactically-valid Bearer string can make it allocate.
//
// Overflow returns zstd.ErrDecoderSizeExceeded, which callers answer 413 —
// naming the limit, rather than letting a half-expanded body fail later as an
// unintelligible parse error.
func decodeResponsesZstd(body []byte) ([]byte, error) {
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(uint64(MaxDecoded)))
	if err != nil {
		return nil, err
	}
	defer decoder.Close()
	return decoder.DecodeAll(body, nil)
}

func responsesToChatRequest(request *OpenAIResponsesRequest) (*openai.ChatCompletionRequest, map[string]string, error) {
	messages, err := responsesInputToMessages(request.Instructions, request.Input)
	if err != nil {
		return nil, nil, err
	}
	if len(messages) == 0 {
		return nil, nil, fmt.Errorf("input must contain at least one message")
	}

	tools := make([]openai.Tool, 0, len(request.Tools))
	toolKinds := make(map[string]string, len(request.Tools))
	// A namespace is not a function: each tool it holds is one, named flat.
	var add func(tool responsesTool, namespace string)
	add = func(tool responsesTool, namespace string) {
		if tool.Name == "" {
			return
		}
		kind := tool.Type
		if kind == "" {
			kind = "function"
		}
		if kind == "namespace" {
			for _, inner := range tool.Tools {
				add(inner, tool.Name)
			}
			return
		}
		name := tool.Name
		if namespace != "" {
			name = flatName(namespace, tool.Name)
			if request.spaces == nil {
				request.spaces = map[string]spaced{}
			}
			request.spaces[name] = spaced{Namespace: namespace, Name: tool.Name}
		}
		toolKinds[name] = kind
		params := any(json.RawMessage(tool.Parameters))
		if len(tool.Parameters) == 0 || string(tool.Parameters) == "null" {
			if kind == "custom" {
				params = map[string]any{
					"type": "object",
					"properties": map[string]any{
						"input": map[string]any{"type": "string"},
					},
					"required": []string{"input"},
				}
			} else {
				params = map[string]any{"type": "object", "properties": map[string]any{}}
			}
		}
		tools = append(tools, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name: name, Description: tool.Description, Parameters: params, Strict: tool.Strict,
			},
		})
	}
	for _, tool := range request.Tools {
		add(tool, "")
	}

	chat := &openai.ChatCompletionRequest{
		Model:               request.Model,
		Messages:            messages,
		Tools:               tools,
		ToolChoice:          responsesToolChoiceToChat(request.ToolChoice),
		Stream:              request.Stream,
		MaxCompletionTokens: request.MaxOutputTokens,
		Store:               request.Store,
		Metadata:            request.Metadata,
	}
	if request.Stream {
		// The existing ChatCompletions proxy only forwards its forced upstream
		// usage chunk when the downstream client opted in. The Responses bridge
		// is that client, and response.completed must contain real token counts.
		chat.StreamOptions = &openai.StreamOptions{IncludeUsage: true}
	}
	if request.MaxOutputTokens > 0 {
		// DO's OpenAI-compatible models currently honor max_tokens uniformly.
		chat.MaxTokens = request.MaxOutputTokens
		chat.MaxCompletionTokens = 0
	}
	if request.Temperature != nil {
		chat.Temperature = *request.Temperature
	}
	if request.TopP != nil {
		chat.TopP = *request.TopP
	}
	if request.ParallelToolCalls != nil {
		chat.ParallelToolCalls = *request.ParallelToolCalls
	}
	return chat, toolKinds, nil
}

func responsesInputToMessages(instructions string, raw json.RawMessage) ([]openai.ChatCompletionMessage, error) {
	messages := make([]openai.ChatCompletionMessage, 0, 8)
	if instructions != "" {
		messages = append(messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleSystem, Content: instructions})
	}
	if len(raw) == 0 || string(raw) == "null" {
		return messages, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return append(messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: text}), nil
	}

	var items []responsesInputItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("input must be a string or an array of Responses input items: %w", err)
	}

	var pendingCalls []openai.ToolCall
	flushCalls := func() {
		if len(pendingCalls) == 0 {
			return
		}
		messages = append(messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, ToolCalls: pendingCalls})
		pendingCalls = nil
	}

	for _, item := range items {
		switch item.Type {
		case "function_call", "custom_tool_call", "local_shell_call":
			callID := item.CallID
			if callID == "" {
				callID = item.ID
			}
			arguments := item.Arguments
			if item.Type == "custom_tool_call" && arguments == "" {
				encoded, _ := json.Marshal(map[string]string{"input": item.Input})
				arguments = string(encoded)
			}
			name := item.Name
			if item.Namespace != "" {
				name = flatName(item.Namespace, item.Name)
			}
			pendingCalls = append(pendingCalls, openai.ToolCall{
				ID: callID, Type: openai.ToolTypeFunction,
				Function: openai.FunctionCall{Name: name, Arguments: arguments},
			})
		case "function_call_output", "custom_tool_call_output", "local_shell_call_output":
			flushCalls()
			messages = append(messages, openai.ChatCompletionMessage{
				Role: openai.ChatMessageRoleTool, ToolCallID: item.CallID, Content: responsesOutputText(item.Output),
			})
		case "message", "":
			flushCalls()
			role := item.Role
			if role == "" {
				role = openai.ChatMessageRoleUser
			}
			msg := openai.ChatCompletionMessage{Role: role}
			parts := responsesContentParts(item.Content)
			for _, part := range parts {
				switch part.Type {
				case "input_image":
					msg.MultiContent = append(msg.MultiContent, openai.ChatMessagePart{
						Type:     openai.ChatMessagePartTypeImageURL,
						ImageURL: &openai.ChatMessageImageURL{URL: part.ImageURL},
					})
				default:
					if part.Text != "" {
						msg.Content += part.Text
						msg.MultiContent = append(msg.MultiContent, openai.ChatMessagePart{Type: openai.ChatMessagePartTypeText, Text: part.Text})
					}
				}
			}
			// go-openai marshals exactly one of Content and MultiContent. A
			// message with an image keeps the parts and drops the joined text;
			// text-only messages go out as a plain string.
			if len(msg.MultiContent) > 0 {
				hasImage := false
				for _, part := range msg.MultiContent {
					hasImage = hasImage || part.Type == openai.ChatMessagePartTypeImageURL
				}
				if hasImage {
					msg.Content = ""
				} else {
					msg.MultiContent = nil
				}
			}
			messages = append(messages, msg)
		case "reasoning":
			// Encrypted/reasoning items are provider-specific state. The serving
			// model receives the visible conversation and continues normally.
		default:
			// Forward-compatible: ignore server-executed Responses item types.
		}
	}
	flushCalls()
	return messages, nil
}

func responsesContentParts(raw json.RawMessage) []responsesContentPart {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []responsesContentPart{{Type: "input_text", Text: text}}
	}
	var parts []responsesContentPart
	if json.Unmarshal(raw, &parts) == nil {
		return parts
	}
	return []responsesContentPart{{Type: "input_text", Text: string(raw)}}
}

func responsesOutputText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	parts := responsesContentParts(raw)
	var out strings.Builder
	for _, part := range parts {
		out.WriteString(part.Text)
	}
	if out.Len() > 0 {
		return out.String()
	}
	return string(raw)
}

func responsesToolChoiceToChat(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var choice string
	if json.Unmarshal(raw, &choice) == nil {
		return choice
	}
	var choiceObj struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &choiceObj) == nil && choiceObj.Name != "" {
		return map[string]any{
			"type": "function", "function": map[string]string{"name": choiceObj.Name},
		}
	}
	return nil
}

// responsesBridge reads the chat completion this request was translated into and
// writes it back out in the Responses dialect.
//
// It is handed BYTES, and that is the whole shape of it. The chat side used to
// write through it as an http.ResponseWriter — status included — so it had to
// intercept WriteHeader, withhold a success it was not ready to send, keep a
// buffer of a body it meant to rewrite, and remember which of those it had done.
// Every one of those existed to separate a body from a status arriving on the same
// call. It now receives the two things it actually wants, separately: the stream's
// chunks as they are produced, and — through the sink in openai_api.go — the
// non-streaming body as one slice. The status is the handler's, and stays there.
type responsesBridge struct {
	out        io.Writer
	request    *OpenAIResponsesRequest
	toolKinds  map[string]string
	call       *ResponsesCall
	responseID string
	streamRec  *ResponseStream
	lineBuf    bytes.Buffer
	translator *responsesStreamTranslator
	mu         sync.Mutex
	closed     bool
}

func newResponsesBridge(out io.Writer, request *OpenAIResponsesRequest, toolKinds map[string]string) *responsesBridge {
	return &responsesBridge{out: out, request: request, toolKinds: toolKinds}
}

// Write takes the chat stream's SSE as it is produced and emits the Responses
// events for it. Chunks arrive on no particular boundary, so a partial last line
// is kept for the next call.
func (w *responsesBridge) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.translator == nil {
		w.translator = newResponsesStreamTranslator(w.emitLocked, w.request, w.toolKinds)
		if w.responseID != "" {
			w.translator.responseID = w.responseID
		} else {
			w.responseID = w.translator.responseID
		}
		if w.streamRec == nil {
			var org, user, tid string
			if w.call != nil {
				org, user, tid = w.call.callerOrg, w.call.callerUser, w.call.threadID
			}
			w.streamRec = DefaultResponseStreamHub().Register(w.responseID, org, user, tid)
		}
		w.translator.onComplete = func(respID, text string, calls []object.ThreadToolCall) {
			if w.call != nil {
				w.call.persistTurn(respID, text, calls)
			}
			if w.streamRec != nil {
				w.streamRec.Complete()
			}
		}
		if err := w.translator.start(); err != nil {
			return len(p), err
		}
	}
	_, _ = w.lineBuf.Write(p)
	for {
		line, err := w.lineBuf.ReadString('\n')
		if err != nil {
			// ReadString consumed the partial line; put it back until the next write.
			w.lineBuf.Reset()
			_, _ = w.lineBuf.WriteString(line)
			break
		}
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			if err := w.translator.finish(); err != nil {
				return len(p), err
			}
			continue
		}
		var chunk openaiStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if err := w.translator.handleChunk(&chunk); err != nil {
			return len(p), err
		}
	}
	return len(p), nil
}

// Flush pushes each event as it is produced, which is what makes this a stream.
// Two shapes, for the same reason OpenAIWriter takes both.
func (w *responsesBridge) Flush() {
	switch f := w.out.(type) {
	case interface{ Flush() error }:
		_ = f.Flush()
	case interface{ Flush() }:
		f.Flush()
	}
}

// Close ends the event stream. Idempotent: the chat side sends [DONE], which
// finishes the translator, and the handler closes again on the way out.
func (w *responsesBridge) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	if w.translator == nil {
		if w.streamRec != nil {
			w.streamRec.Complete()
		}
		return nil
	}
	err := w.translator.finish()
	if w.streamRec != nil {
		w.streamRec.Complete()
	}
	return err
}

func (w *responsesBridge) emitLocked(event string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if w.streamRec != nil {
		w.streamRec.Append(event, b)
	}
	if _, err = fmt.Fprintf(w.out, "event: %s\ndata: %s\n\n", event, b); err != nil {
		return err
	}
	w.Flush()
	return nil
}

type responsesToolCallState struct {
	Index       int
	OutputIndex int
	ID          string
	CallID      string
	Name        string
	Namespace   string
	Arguments   strings.Builder
	Kind        string
	Started     bool
}

type responsesStreamTranslator struct {
	emit             func(string, any) error
	onComplete       func(responseID, text string, calls []object.ThreadToolCall)
	request          *OpenAIResponsesRequest
	toolKinds        map[string]string
	responseID       string
	messageID        string
	createdAt        int64
	sequence         int
	started          bool
	finished         bool
	textStarted      bool
	textOutputIndex  int
	text             strings.Builder
	calls            map[int]*responsesToolCallState
	callOrder        []int
	nextOutput       int
	promptTokens     int
	cachedTokens     int
	writtenTokens    int
	completionTokens int
	totalTokens      int
	sawFinishReason  bool
	sawUsage         bool
}

func newResponsesStreamTranslator(emit func(string, any) error, request *OpenAIResponsesRequest, toolKinds map[string]string) *responsesStreamTranslator {
	return &responsesStreamTranslator{
		emit: emit, request: request, toolKinds: toolKinds,
		responseID: "resp_" + uuid.NewString(), messageID: "msg_" + uuid.NewString(),
		createdAt: time.Now().Unix(), calls: map[int]*responsesToolCallState{},
	}
}

func (t *responsesStreamTranslator) next() int { n := t.sequence; t.sequence++; return n }

func (t *responsesStreamTranslator) start() error {
	if t.started {
		return nil
	}
	t.started = true
	return t.emit("response.created", map[string]any{
		"type": "response.created", "sequence_number": t.next(),
		"response": t.resource("in_progress", []any{}),
	})
}

func (t *responsesStreamTranslator) handleChunk(chunk *openaiStreamChunk) error {
	// Nothing follows response.completed: an event after it is one the client
	// cannot place.
	if t.finished {
		return nil
	}
	if err := t.start(); err != nil {
		return err
	}
	if chunk.Usage != nil {
		t.count(chunk.Usage)
		t.sawUsage = true
		if t.sawFinishReason {
			return t.finish()
		}
	}
	for i := range chunk.Choices {
		choice := &chunk.Choices[i]
		if choice.Delta.Content != "" {
			if !t.textStarted {
				t.textStarted = true
				outputIndex := t.nextOutput
				t.textOutputIndex = outputIndex
				t.nextOutput++
				if err := t.emit("response.output_item.added", map[string]any{
					"type": "response.output_item.added", "sequence_number": t.next(), "output_index": outputIndex,
					"item": map[string]any{"id": t.messageID, "type": "message", "role": "assistant", "status": "in_progress", "content": []any{}},
				}); err != nil {
					return err
				}
				if err := t.emit("response.content_part.added", map[string]any{
					"type": "response.content_part.added", "sequence_number": t.next(), "output_index": outputIndex, "content_index": 0,
					"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
				}); err != nil {
					return err
				}
			}
			t.text.WriteString(choice.Delta.Content)
			if err := t.emit("response.output_text.delta", map[string]any{
				"type": "response.output_text.delta", "sequence_number": t.next(), "item_id": t.messageID,
				"output_index": t.textOutputIndex, "content_index": 0, "delta": choice.Delta.Content,
			}); err != nil {
				return err
			}
		}

		for j := range choice.Delta.ToolCalls {
			tc := &choice.Delta.ToolCalls[j]
			idx := j
			if tc.Index != nil {
				idx = *tc.Index
			}
			call := t.calls[idx]
			if call == nil {
				call = &responsesToolCallState{Index: idx, OutputIndex: t.nextOutput, Kind: "function"}
				t.nextOutput++
				t.calls[idx] = call
				t.callOrder = append(t.callOrder, idx)
			}
			if tc.ID != "" {
				call.ID, call.CallID = tc.ID, tc.ID
			}
			if tc.Function.Name != "" {
				call.Name = tc.Function.Name
				if kind := t.toolKinds[call.Name]; kind != "" {
					call.Kind = kind
				}
				t.request.unflat(call)
			}
			if !call.Started && (call.CallID != "" || call.Name != "") {
				call.Started = true
				if call.ID == "" {
					call.ID = "fc_" + uuid.NewString()
				}
				if call.CallID == "" {
					call.CallID = call.ID
				}
				if err := t.emit("response.output_item.added", map[string]any{
					"type": "response.output_item.added", "sequence_number": t.next(), "output_index": call.OutputIndex,
					"item": call.item("in_progress", ""),
				}); err != nil {
					return err
				}
			}
			if tc.Function.Arguments != "" {
				call.Arguments.WriteString(tc.Function.Arguments)
				event := "response.function_call_arguments.delta"
				if call.Kind == "custom" {
					event = "response.custom_tool_call_input.delta"
				}
				if err := t.emit(event, map[string]any{
					"type": event, "sequence_number": t.next(), "item_id": call.ID,
					"output_index": call.OutputIndex, "delta": tc.Function.Arguments,
				}); err != nil {
					return err
				}
			}
		}
		// The end of the answer, in whichever order the two signals arrive.
		//
		// finish_reason alone is not enough: providers commonly send the
		// usage-only chunk *after* the terminal choice chunk, and finishing on
		// finish_reason alone published response.completed with zero usage and
		// made that accounting chunk impossible to apply.
		//
		// Waiting only for [DONE] is not enough either, and that is what broke
		// clients: an OpenAI-compatible provider is free to end the stream
		// without one. When that happened the terminal events were never sent —
		// response.output_text.done, .content_part.done, .output_item.done and
		// .completed — so every delta arrived and the response never closed.
		// A client reading the Responses stream to spec waits forever, retries,
		// and reports the stream as truncated.
		//
		// So: whichever of the two arrives second finishes the response, and
		// the bridge Close stays the backstop for a stream that sends neither.
		if choice.FinishReason != "" {
			t.sawFinishReason = true
			if t.sawUsage {
				return t.finish()
			}
		}
	}
	return nil
}

func (c *responsesToolCallState) finalArguments() string {
	args := c.Arguments.String()
	if c.Kind != "custom" {
		return args
	}
	var wrapped struct {
		Input string `json:"input"`
	}
	if json.Unmarshal([]byte(args), &wrapped) == nil && wrapped.Input != "" {
		return wrapped.Input
	}
	return args
}

func (c *responsesToolCallState) item(status, arguments string) map[string]any {
	item := map[string]any{
		"id": c.ID, "type": "function_call", "call_id": c.CallID,
		"name": c.Name, "arguments": arguments, "status": status,
	}
	if c.Kind == "custom" {
		item = map[string]any{
			"id": c.ID, "type": "custom_tool_call", "call_id": c.CallID,
			"name": c.Name, "input": arguments, "status": status,
		}
	}
	if c.Namespace != "" {
		item["namespace"] = c.Namespace
	}
	return item
}

func (t *responsesStreamTranslator) finish() error {
	if t.finished {
		return nil
	}
	if err := t.start(); err != nil {
		return err
	}
	t.finished = true
	outputs := make([]any, 0, 1+len(t.calls))
	if t.textStarted {
		text := t.text.String()
		message := map[string]any{
			"id": t.messageID, "type": "message", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}},
		}
		if err := t.emit("response.output_text.done", map[string]any{
			"type": "response.output_text.done", "sequence_number": t.next(), "item_id": t.messageID,
			"output_index": t.textOutputIndex, "content_index": 0, "text": text,
		}); err != nil {
			return err
		}
		if err := t.emit("response.content_part.done", map[string]any{
			"type": "response.content_part.done", "sequence_number": t.next(), "output_index": t.textOutputIndex, "content_index": 0,
			"part": message["content"].([]any)[0],
		}); err != nil {
			return err
		}
		if err := t.emit("response.output_item.done", map[string]any{
			"type": "response.output_item.done", "sequence_number": t.next(), "output_index": t.textOutputIndex, "item": message,
		}); err != nil {
			return err
		}
		outputs = append(outputs, message)
	}
	for _, idx := range t.callOrder {
		call := t.calls[idx]
		args := call.finalArguments()
		if !call.Started {
			call.Started = true
			if call.ID == "" {
				call.ID = "fc_" + uuid.NewString()
			}
			if call.CallID == "" {
				call.CallID = call.ID
			}
		}
		if call.Kind != "custom" {
			if err := t.emit("response.function_call_arguments.done", map[string]any{
				"type": "response.function_call_arguments.done", "sequence_number": t.next(), "item_id": call.ID,
				"output_index": call.OutputIndex, "arguments": args,
			}); err != nil {
				return err
			}
		}
		item := call.item("completed", args)
		if err := t.emit("response.output_item.done", map[string]any{
			"type": "response.output_item.done", "sequence_number": t.next(), "output_index": call.OutputIndex, "item": item,
		}); err != nil {
			return err
		}
		outputs = append(outputs, item)
	}
	if err := t.emit("response.completed", map[string]any{
		"type": "response.completed", "sequence_number": t.next(),
		"response": t.resource("completed", outputs),
	}); err != nil {
		return err
	}
	if t.onComplete != nil {
		var savedCalls []object.ThreadToolCall
		for _, idx := range t.callOrder {
			call := t.calls[idx]
			savedCalls = append(savedCalls, object.ThreadToolCall{
				ID:        call.CallID,
				Type:      "function",
				Name:      call.Name,
				Arguments: call.finalArguments(),
			})
		}
		t.onComplete(t.responseID, t.text.String(), savedCalls)
	}
	return nil
}

// count takes the usage a chunk or a whole answer reports. A count it leaves out
// keeps what an earlier chunk said: a usage chunk need not repeat every part.
func (t *responsesStreamTranslator) count(u *chatUsage) {
	if u.PromptTokens > 0 {
		t.promptTokens = u.PromptTokens
	}
	if u.CompletionTokens > 0 {
		t.completionTokens = u.CompletionTokens
	}
	if u.TotalTokens > 0 {
		t.totalTokens = u.TotalTokens
	}
	if d := u.PromptDetails; d != nil {
		if d.Cached > 0 {
			t.cachedTokens = d.Cached
		}
		if d.Written > 0 {
			t.writtenTokens = d.Written
		}
	}
}

func (t *responsesStreamTranslator) resource(status string, output []any) responsesResource {
	total := t.totalTokens
	if total == 0 {
		total = t.promptTokens + t.completionTokens
	}
	// Both are parts of the prompt, and never more than it.
	cached := min(t.cachedTokens, t.promptTokens)
	written := min(t.writtenTokens, t.promptTokens-cached)
	var prevID any
	if t.request.PreviousResponseID != "" {
		prevID = t.request.PreviousResponseID
	}
	return responsesResource{
		ID: t.responseID, Object: "response", CreatedAt: t.createdAt, Status: status,
		Model: t.request.Model, Output: output,
		Instructions: t.request.Instructions, MaxOutputTokens: t.request.MaxOutputTokens,
		Metadata: t.request.Metadata, ParallelToolCalls: t.request.ParallelToolCalls,
		PreviousResponseID: prevID,
		Reasoning:          jsonOrNil(t.request.Reasoning),
		Store:     t.request.Store, Temperature: t.request.Temperature,
		Text: jsonOrNil(t.request.Text), ToolChoice: jsonOrNil(t.request.ToolChoice),
		Tools: t.request.Tools, TopP: t.request.TopP,
		Usage: responsesUsage{
			InputTokens:   t.promptTokens,
			InputDetails:  responsesInputDetails{CachedTokens: cached, CacheWriteTokens: written},
			OutputTokens:  t.completionTokens,
			OutputDetails: responsesOutputDetails{},
			TotalTokens:   total,
		},
	}
}

// responsesResource is one answer from /v1/responses.
//
// Error, IncompleteDetails and PreviousResponseID are `any` and carry no
// omitempty because the OpenAI Responses API sends them as an explicit null
// rather than omitting them, which is what the map this replaced did by setting
// the key to nil.
//
// Output is the one field with no shape to state: it is a union — a message, a
// tool call, a reasoning item — and each element says which it is in its own
// `type`. An empty schema is the honest answer for a union; a guessed one would
// send every client down the wrong branch.
type responsesResource struct {
	ID                 string            `json:"id"`
	Object             string            `json:"object"`
	CreatedAt          int64             `json:"created_at"`
	Status             string            `json:"status"`
	Model              string            `json:"model"`
	Output             []any             `json:"output"`
	Error              any               `json:"error"`
	IncompleteDetails  any               `json:"incomplete_details"`
	Instructions       string            `json:"instructions"`
	MaxOutputTokens    int               `json:"max_output_tokens"`
	Metadata           map[string]string `json:"metadata"`
	ParallelToolCalls  *bool             `json:"parallel_tool_calls"`
	PreviousResponseID any               `json:"previous_response_id"`
	Reasoning          any               `json:"reasoning"`
	Store              bool              `json:"store"`
	Temperature        *float32          `json:"temperature"`
	Text               any               `json:"text"`
	ToolChoice         any               `json:"tool_choice"`
	Tools              []responsesTool   `json:"tools"`
	TopP               *float32          `json:"top_p"`
	Usage              responsesUsage    `json:"usage"`
}

// responsesUsage is what the answer cost.
type responsesUsage struct {
	InputTokens   int                    `json:"input_tokens"`
	InputDetails  responsesInputDetails  `json:"input_tokens_details"`
	OutputTokens  int                    `json:"output_tokens"`
	OutputDetails responsesOutputDetails `json:"output_tokens_details"`
	TotalTokens   int                    `json:"total_tokens"`
}

// responsesInputDetails is the part of input_tokens the upstream's prompt cache
// served (cached_tokens) and the part this call wrote to it (cache_write_tokens,
// which Codex reads beside OpenAI's field).
type responsesInputDetails struct {
	CachedTokens     int `json:"cached_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

// chatUsage is a chat completion's usage. prompt_tokens is the whole prompt, and
// prompt_tokens_details says how much of it the upstream's cache served
// (cached_tokens) and, on OpenRouter, how much this call wrote to it
// (cache_write_tokens).
type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	PromptDetails    *struct {
		Cached  int `json:"cached_tokens"`
		Written int `json:"cache_write_tokens"`
	} `json:"prompt_tokens_details"`
}

type responsesOutputDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

func jsonOrNil(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return value
}

func openAIChatResponseToResponses(body []byte, request *OpenAIResponsesRequest, toolKinds map[string]string) ([]byte, error) {
	return openAIChatResponseToResponsesWithCall(body, request, toolKinds, nil)
}

func openAIChatResponseToResponsesWithCall(body []byte, request *OpenAIResponsesRequest, toolKinds map[string]string, call *ResponsesCall) ([]byte, error) {
	var response struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string                           `json:"id"`
					Function struct{ Name, Arguments string } `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage chatUsage `json:"usage"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("invalid upstream chat response: %w", err)
	}
	translator := newResponsesStreamTranslator(func(string, any) error { return nil }, request, toolKinds)
	if call != nil && call.responseID != "" {
		translator.responseID = call.responseID
	}
	translator.count(&response.Usage)
	outputs := []any{}
	var savedText string
	var savedCalls []object.ThreadToolCall
	if len(response.Choices) > 0 {
		message := response.Choices[0].Message
		if message.Content != "" {
			savedText = message.Content
			outputs = append(outputs, map[string]any{
				"id": translator.messageID, "type": "message", "role": "assistant", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": message.Content, "annotations": []any{}}},
			})
		}
		for _, tc := range message.ToolCalls {
			tcState := &responsesToolCallState{ID: tc.ID, CallID: tc.ID, Name: tc.Function.Name, Kind: toolKinds[tc.Function.Name]}
			if tcState.Kind == "" {
				tcState.Kind = "function"
			}
			request.unflat(tcState)
			tcState.Arguments.WriteString(tc.Function.Arguments)
			finalArgs := tcState.finalArguments()
			outputs = append(outputs, tcState.item("completed", finalArgs))
			savedCalls = append(savedCalls, object.ThreadToolCall{
				ID:        tcState.CallID,
				Type:      "function",
				Name:      tcState.Name,
				Arguments: finalArgs,
			})
		}
	}
	if call != nil {
		call.persistTurn(translator.responseID, savedText, savedCalls)
	}
	resource := translator.resource("completed", outputs)
	return json.Marshal(resource)
}

// SendStreamWriter is zip's, with this request's answer shape applied to every
// stream it writes.
//
// The Responses bridge used to be installed by ONE path — the native text
// completion — while the family relay, the tool-call proxy and the vision proxy
// each opened their own stream and wrote upstream chat SSE straight to the
// client. So /v1/responses answered `chat.completion.chunk` for every model a
// family or a proxy serves, which is nearly all of them, and any request carrying
// tools: a Responses client (Hanzo Dev, the OpenAI SDK) saw no response.* event,
// dropped every chunk, and failed on "stream closed before response.completed".
//
// Here the bridge wraps the writer each path is handed, so the translation is a
// property of the request rather than of which path happened to serve it. A path
// flushes its own buffered writer after each chunk; the bridge emits and flushes
// each Responses event as the chunk arrives, so the answer still streams.
//
// On the paid lane the stream is paced by its seat (seat.paced): each chunk the
// client takes keeps the seat's hold, and a stream nobody reads gives it back.
func (c *ApiController) SendStreamWriter(fn func(*bufio.Writer)) error {
	s := seatOf(c.Context())
	to := c.answer
	if to == nil || to.wrap == nil {
		return c.Ctx.SendStreamWriter(s.paced(fn))
	}
	return c.Ctx.SendStreamWriter(s.paced(func(bw *bufio.Writer) {
		out := to.wrap(bw)
		in := bufio.NewWriter(out)
		fn(in)
		_ = in.Flush()
		// An upstream that ends without [DONE] still gets its response.completed;
		// finish is idempotent, so one that sent it is unaffected. This runs inside
		// the callback because zip runs the callback after the handler returns: a
		// close placed after chatCompletions ran before any byte was written.
		//
		// Logged, not answered: the events are already on the wire, so there is no
		// status left to change.
		if cl, ok := out.(io.Closer); ok {
			if err := cl.Close(); err != nil {
				log.Error(fmt.Sprintf("Responses: closing the stream failed: %s", err.Error()))
			}
		}
	}))
}

// answerBody writes one whole successful answer in the shape this request asked
// for: the chat completion as is, or translated by the sink. It is the
// non-streaming half of SendStreamWriter's rule, for the same reason.
func (c *ApiController) answerBody(chat []byte) {
	out, err := c.answer.shape(chat)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.SetHeader("Content-Type", "application/json")
	c.Bytes(http.StatusOK, out)
}

// sendWhole writes ONE whole answer over time, paced by the request's seat as a
// stream is: the spaces that keep its connection open while it is assembled, then
// the answer. It is never decorated as a stream. Through SendStreamWriter a
// /v1/responses request asked whole had its assembled chat completion read by the
// Responses event bridge, which found no event in it and answered
// response.created and response.completed with nothing in between, under
// application/json. The writer shapes the answer itself (sink.shape).
func (c *ApiController) sendWhole(fn func(*bufio.Writer)) error {
	return c.Ctx.SendStreamWriter(seatOf(c.Context()).paced(fn))
}
