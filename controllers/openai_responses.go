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
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/DataDog/zstd"
	"github.com/hanzoai/ai/util"
	"github.com/sashabaranov/go-openai"
)

// OpenAIResponsesRequest is the subset of POST /v1/responses used by Codex and
// OpenAI SDKs. Unknown fields are deliberately accepted for forward
// compatibility; fields that Chat Completions cannot represent are ignored.
type OpenAIResponsesRequest struct {
	Model             string            `json:"model"`
	Instructions      string            `json:"instructions,omitempty"`
	Input             json.RawMessage   `json:"input"`
	Tools             []responsesTool   `json:"tools,omitempty"`
	ToolChoice        json.RawMessage   `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool             `json:"parallel_tool_calls,omitempty"`
	Stream            bool              `json:"stream,omitempty"`
	MaxOutputTokens   int               `json:"max_output_tokens,omitempty"`
	Temperature       *float32          `json:"temperature,omitempty"`
	TopP              *float32          `json:"top_p,omitempty"`
	Store             bool              `json:"store,omitempty"`
	Metadata          map[string]string `json:"metadata,omitempty"`
	Reasoning         json.RawMessage   `json:"reasoning,omitempty"`
	Text              json.RawMessage   `json:"text,omitempty"`
}

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      bool            `json:"strict,omitempty"`
	Format      json.RawMessage `json:"format,omitempty"`
}

type responsesInputItem struct {
	Type      string          `json:"type"`
	ID        string          `json:"id,omitempty"`
	Role      string          `json:"role,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments string          `json:"arguments,omitempty"`
	Input     string          `json:"input,omitempty"`
	Output    json.RawMessage `json:"output,omitempty"`
}

type responsesContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

// Responses implements POST /v1/responses. The converted request is passed to
// ChatCompletions and an installed ResponseWriter converts its OpenAI chat JSON
// or SSE back into Responses JSON/SSE on the fly.
func (c *ApiController) Responses() {
	authHeader := c.Ctx.Request.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		c.ResponseErrorWithStatus(http.StatusUnauthorized, "Invalid API key format. Expected 'Bearer API_KEY'")
		return
	}
	token := strings.TrimPrefix(authHeader, "Bearer ")
	if isPublishableKey(token) {
		c.ResponseErrorWithStatus(http.StatusForbidden, "Publishable keys cannot access /v1/responses. Use a secret key.")
		return
	}

	body := c.Ctx.Input.RequestBody
	if strings.EqualFold(strings.TrimSpace(c.Ctx.Request.Header.Get("Content-Encoding")), "zstd") {
		decoded, err := zstd.Decompress(nil, body)
		if err != nil {
			if authErr := c.authenticate(token); authErr != nil {
				c.ResponseAuthError(authErr)
				return
			}
			c.ResponseErrorWithStatus(http.StatusBadRequest, "Failed to decompress zstd request: "+err.Error())
			return
		}
		body = decoded
		c.Ctx.Request.Header.Del("Content-Encoding")
	}

	var request OpenAIResponsesRequest
	if err := json.Unmarshal(body, &request); err != nil {
		if authErr := c.authenticate(token); authErr != nil {
			c.ResponseAuthError(authErr)
			return
		}
		c.ResponseErrorWithStatus(http.StatusBadRequest, "Failed to parse Responses request: "+err.Error())
		return
	}
	if request.Model == "" {
		c.ResponseErrorWithStatus(http.StatusBadRequest, "model is required")
		return
	}

	chatRequest, toolKinds, err := responsesToChatRequest(&request)
	if err != nil {
		c.ResponseErrorWithStatus(http.StatusBadRequest, err.Error())
		return
	}
	chatBody, err := json.Marshal(chatRequest)
	if err != nil {
		c.ResponseErrorWithStatus(http.StatusInternalServerError, err.Error())
		return
	}

	original := c.Ctx.ResponseWriter.ResponseWriter
	bridge := newResponsesBridgeWriter(original, &request, toolKinds)
	c.Ctx.ResponseWriter.ResponseWriter = bridge
	c.Ctx.Input.RequestBody = chatBody
	c.Ctx.Request.Header.Del("Content-Length")

	c.ChatCompletions()
	if err := bridge.Close(); err != nil && !c.Ctx.ResponseWriter.Started {
		c.ResponseErrorWithStatus(http.StatusBadGateway, err.Error())
	}
	c.EnableRender = false
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
	for _, tool := range request.Tools {
		if tool.Name == "" {
			continue
		}
		kind := tool.Type
		if kind == "" {
			kind = "function"
		}
		toolKinds[tool.Name] = kind
		params := any(json.RawMessage(tool.Parameters))
		if len(tool.Parameters) == 0 || string(tool.Parameters) == "null" {
			if kind == "custom" {
				params = map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"input": map[string]interface{}{"type": "string"},
					},
					"required": []string{"input"},
				}
			} else {
				params = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
			}
		}
		tools = append(tools, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name: tool.Name, Description: tool.Description, Parameters: params, Strict: tool.Strict,
			},
		})
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
			pendingCalls = append(pendingCalls, openai.ToolCall{
				ID: callID, Type: openai.ToolTypeFunction,
				Function: openai.FunctionCall{Name: item.Name, Arguments: arguments},
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
			if len(msg.MultiContent) > 0 {
				hasImage := false
				for _, part := range msg.MultiContent {
					hasImage = hasImage || part.Type == openai.ChatMessagePartTypeImageURL
				}
				if !hasImage {
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

func responsesToolChoiceToChat(raw json.RawMessage) interface{} {
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
		return map[string]interface{}{
			"type": "function", "function": map[string]string{"name": choiceObj.Name},
		}
	}
	return nil
}

type responsesBridgeWriter struct {
	dst        http.ResponseWriter
	request    *OpenAIResponsesRequest
	toolKinds  map[string]string
	status     int
	stream     bool
	buffer     bytes.Buffer
	lineBuf    bytes.Buffer
	translator *responsesStreamTranslator
	mu         sync.Mutex
	closed     bool
}

func newResponsesBridgeWriter(dst http.ResponseWriter, request *OpenAIResponsesRequest, toolKinds map[string]string) *responsesBridgeWriter {
	return &responsesBridgeWriter{dst: dst, request: request, toolKinds: toolKinds, stream: request.Stream}
}

func (w *responsesBridgeWriter) Header() http.Header { return w.dst.Header() }

func (w *responsesBridgeWriter) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status != 0 {
		return
	}
	w.status = status
	if status >= 200 && status < 300 && !w.stream {
		// Delay the successful status and headers until Close translates the
		// buffered Chat Completions JSON. In particular, do not send the chat
		// body's Content-Length on the Responses body.
		return
	}
	if status >= 200 && status < 300 && w.stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Del("Content-Length")
		w.dst.WriteHeader(status)
		w.translator = newResponsesStreamTranslator(w.emitLocked, w.request, w.toolKinds)
		_ = w.translator.start()
		return
	}
	w.dst.WriteHeader(status)
}

func (w *responsesBridgeWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = http.StatusOK
		if !w.stream {
			_, _ = w.buffer.Write(p)
			return len(p), nil
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Del("Content-Length")
		w.dst.WriteHeader(w.status)
	}
	if w.status < 200 || w.status >= 300 {
		_, err := w.dst.Write(p)
		return len(p), err
	}
	if !w.stream {
		_, _ = w.buffer.Write(p)
		return len(p), nil
	}
	if w.translator == nil {
		w.translator = newResponsesStreamTranslator(w.emitLocked, w.request, w.toolKinds)
		_ = w.translator.start()
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

func (w *responsesBridgeWriter) Flush() {
	if flusher, ok := w.dst.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *responsesBridgeWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	if w.status < 200 || w.status >= 300 {
		return nil
	}
	if w.stream {
		if w.translator != nil {
			return w.translator.finish()
		}
		return nil
	}
	out, err := openAIChatResponseToResponses(w.buffer.Bytes(), w.request, w.toolKinds)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(out)))
	w.dst.WriteHeader(w.status)
	_, err = w.dst.Write(out)
	return err
}

func (w *responsesBridgeWriter) emitLocked(event string, data interface{}) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(w.dst, "event: %s\ndata: %s\n\n", event, b); err != nil {
		return err
	}
	if flusher, ok := w.dst.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

type responsesToolCallState struct {
	Index       int
	OutputIndex int
	ID          string
	CallID      string
	Name        string
	Arguments   strings.Builder
	Kind        string
	Started     bool
}

type responsesStreamTranslator struct {
	emit             func(string, interface{}) error
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
	completionTokens int
	totalTokens      int
}

func newResponsesStreamTranslator(emit func(string, interface{}) error, request *OpenAIResponsesRequest, toolKinds map[string]string) *responsesStreamTranslator {
	return &responsesStreamTranslator{
		emit: emit, request: request, toolKinds: toolKinds,
		responseID: "resp_" + util.GenerateUUID(), messageID: "msg_" + util.GenerateUUID(),
		createdAt: time.Now().Unix(), calls: map[int]*responsesToolCallState{},
	}
}

func (t *responsesStreamTranslator) next() int { n := t.sequence; t.sequence++; return n }

func (t *responsesStreamTranslator) start() error {
	if t.started {
		return nil
	}
	t.started = true
	return t.emit("response.created", map[string]interface{}{
		"type": "response.created", "sequence_number": t.next(),
		"response": t.resource("in_progress", []interface{}{}),
	})
}

func (t *responsesStreamTranslator) handleChunk(chunk *openaiStreamChunk) error {
	if err := t.start(); err != nil {
		return err
	}
	if chunk.Usage != nil {
		if chunk.Usage.PromptTokens > 0 {
			t.promptTokens = chunk.Usage.PromptTokens
		}
		if chunk.Usage.CompletionTokens > 0 {
			t.completionTokens = chunk.Usage.CompletionTokens
		}
		if chunk.Usage.TotalTokens > 0 {
			t.totalTokens = chunk.Usage.TotalTokens
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
				if err := t.emit("response.output_item.added", map[string]interface{}{
					"type": "response.output_item.added", "sequence_number": t.next(), "output_index": outputIndex,
					"item": map[string]interface{}{"id": t.messageID, "type": "message", "role": "assistant", "status": "in_progress", "content": []interface{}{}},
				}); err != nil {
					return err
				}
				if err := t.emit("response.content_part.added", map[string]interface{}{
					"type": "response.content_part.added", "sequence_number": t.next(), "output_index": outputIndex, "content_index": 0,
					"part": map[string]interface{}{"type": "output_text", "text": "", "annotations": []interface{}{}},
				}); err != nil {
					return err
				}
			}
			t.text.WriteString(choice.Delta.Content)
			if err := t.emit("response.output_text.delta", map[string]interface{}{
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
			}
			if !call.Started && (call.CallID != "" || call.Name != "") {
				call.Started = true
				if call.ID == "" {
					call.ID = "fc_" + util.GenerateUUID()
				}
				if call.CallID == "" {
					call.CallID = call.ID
				}
				if err := t.emit("response.output_item.added", map[string]interface{}{
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
				if err := t.emit(event, map[string]interface{}{
					"type": event, "sequence_number": t.next(), "item_id": call.ID,
					"output_index": call.OutputIndex, "delta": tc.Function.Arguments,
				}); err != nil {
					return err
				}
			}
		}
		if choice.FinishReason != "" {
			return t.finish()
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

func (c *responsesToolCallState) item(status, arguments string) map[string]interface{} {
	if c.Kind == "custom" {
		return map[string]interface{}{
			"id": c.ID, "type": "custom_tool_call", "call_id": c.CallID,
			"name": c.Name, "input": arguments, "status": status,
		}
	}
	return map[string]interface{}{
		"id": c.ID, "type": "function_call", "call_id": c.CallID,
		"name": c.Name, "arguments": arguments, "status": status,
	}
}

func (t *responsesStreamTranslator) finish() error {
	if t.finished {
		return nil
	}
	if err := t.start(); err != nil {
		return err
	}
	t.finished = true
	outputs := make([]interface{}, 0, 1+len(t.calls))
	if t.textStarted {
		text := t.text.String()
		message := map[string]interface{}{
			"id": t.messageID, "type": "message", "role": "assistant", "status": "completed",
			"content": []interface{}{map[string]interface{}{"type": "output_text", "text": text, "annotations": []interface{}{}}},
		}
		if err := t.emit("response.output_text.done", map[string]interface{}{
			"type": "response.output_text.done", "sequence_number": t.next(), "item_id": t.messageID,
			"output_index": t.textOutputIndex, "content_index": 0, "text": text,
		}); err != nil {
			return err
		}
		if err := t.emit("response.content_part.done", map[string]interface{}{
			"type": "response.content_part.done", "sequence_number": t.next(), "output_index": t.textOutputIndex, "content_index": 0,
			"part": message["content"].([]interface{})[0],
		}); err != nil {
			return err
		}
		if err := t.emit("response.output_item.done", map[string]interface{}{
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
				call.ID = "fc_" + util.GenerateUUID()
			}
			if call.CallID == "" {
				call.CallID = call.ID
			}
		}
		if call.Kind != "custom" {
			if err := t.emit("response.function_call_arguments.done", map[string]interface{}{
				"type": "response.function_call_arguments.done", "sequence_number": t.next(), "item_id": call.ID,
				"output_index": call.OutputIndex, "arguments": args,
			}); err != nil {
				return err
			}
		}
		item := call.item("completed", args)
		if err := t.emit("response.output_item.done", map[string]interface{}{
			"type": "response.output_item.done", "sequence_number": t.next(), "output_index": call.OutputIndex, "item": item,
		}); err != nil {
			return err
		}
		outputs = append(outputs, item)
	}
	return t.emit("response.completed", map[string]interface{}{
		"type": "response.completed", "sequence_number": t.next(),
		"response": t.resource("completed", outputs),
	})
}

func (t *responsesStreamTranslator) resource(status string, output []interface{}) map[string]interface{} {
	total := t.totalTokens
	if total == 0 {
		total = t.promptTokens + t.completionTokens
	}
	return map[string]interface{}{
		"id": t.responseID, "object": "response", "created_at": t.createdAt, "status": status,
		"model": t.request.Model, "output": output, "error": nil, "incomplete_details": nil,
		"instructions": t.request.Instructions, "max_output_tokens": t.request.MaxOutputTokens,
		"metadata": t.request.Metadata, "parallel_tool_calls": t.request.ParallelToolCalls,
		"previous_response_id": nil, "reasoning": jsonOrNil(t.request.Reasoning),
		"store": t.request.Store, "temperature": t.request.Temperature,
		"text": jsonOrNil(t.request.Text), "tool_choice": jsonOrNil(t.request.ToolChoice),
		"tools": t.request.Tools, "top_p": t.request.TopP,
		"usage": map[string]interface{}{
			"input_tokens": t.promptTokens, "input_tokens_details": map[string]int{"cached_tokens": 0},
			"output_tokens": t.completionTokens, "output_tokens_details": map[string]int{"reasoning_tokens": 0},
			"total_tokens": total,
		},
	}
}

func jsonOrNil(raw json.RawMessage) interface{} {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var value interface{}
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return value
}

func openAIChatResponseToResponses(body []byte, request *OpenAIResponsesRequest, toolKinds map[string]string) ([]byte, error) {
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
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("invalid upstream chat response: %w", err)
	}
	translator := newResponsesStreamTranslator(func(string, interface{}) error { return nil }, request, toolKinds)
	translator.promptTokens = response.Usage.PromptTokens
	translator.completionTokens = response.Usage.CompletionTokens
	translator.totalTokens = response.Usage.TotalTokens
	outputs := []interface{}{}
	if len(response.Choices) > 0 {
		message := response.Choices[0].Message
		if message.Content != "" {
			outputs = append(outputs, map[string]interface{}{
				"id": translator.messageID, "type": "message", "role": "assistant", "status": "completed",
				"content": []interface{}{map[string]interface{}{"type": "output_text", "text": message.Content, "annotations": []interface{}{}}},
			})
		}
		for _, tc := range message.ToolCalls {
			call := &responsesToolCallState{ID: tc.ID, CallID: tc.ID, Name: tc.Function.Name, Kind: toolKinds[tc.Function.Name]}
			if call.Kind == "" {
				call.Kind = "function"
			}
			call.Arguments.WriteString(tc.Function.Arguments)
			outputs = append(outputs, call.item("completed", call.finalArguments()))
		}
	}
	resource := translator.resource("completed", outputs)
	return json.Marshal(resource)
}
