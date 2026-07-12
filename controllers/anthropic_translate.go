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
	"encoding/json"
	"fmt"
	"io"
	"strings"

	openai "github.com/sashabaranov/go-openai"
)

// ── Anthropic ⇄ OpenAI translation ───────────────────────────────────────────
//
// The Anthropic-compatible surface (/v1/messages) accepts requests carrying
// tools, tool_result blocks, images and thinking, and must return Anthropic SSE
// events — never raw OpenAI chunks. This file is the single, total translation
// between the two shapes, in BOTH directions:
//
//   request  : Anthropic messages/tools  → OpenAI ChatCompletionRequest
//   response : OpenAI (stream or JSON)    → Anthropic SSE events / JSON
//
// It is pure (no controller/HTTP state): the streaming translator writes to an
// injected io.Writer. Every OpenAI chunk is converted — there is no passthrough.

// anthropicBlock is a fully-typed Anthropic content block. Unlike the display-
// only AnthropicContentBlock (text only), this captures every field we must
// translate: tool_use (id/name/input), tool_result (tool_use_id/content),
// image (source), and thinking (thinking/signature).
type anthropicBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`

	// tool_use (assistant → OpenAI assistant.tool_calls)
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result (user → OpenAI role:"tool")
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`

	// image (→ OpenAI image_url)
	Source *anthropicImageSource `json:"source,omitempty"`

	// thinking (extended thinking) — dropped when translating history to OpenAI
	Thinking string `json:"thinking,omitempty"`
}

// anthropicImageSource is an Anthropic image block source: base64 or url.
type anthropicImageSource struct {
	Type      string `json:"type"`       // "base64" | "url"
	MediaType string `json:"media_type"` // e.g. "image/png"
	Data      string `json:"data"`       // base64 payload (base64 source)
	URL       string `json:"url"`        // url source
}

// parseAnthropicBlocks decodes a message's raw content into blocks. The Anthropic
// Messages API accepts content as either a plain string ("hi") or an array of
// blocks. Returns (blocks, isPlainString); a plain string yields one text block.
func parseAnthropicBlocks(raw json.RawMessage) ([]anthropicBlock, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []anthropicBlock{{Type: "text", Text: s}}, true
	}
	var blocks []anthropicBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		return blocks, false
	}
	return nil, false
}

// anthropicImageToDataURL renders an Anthropic image source as an OpenAI
// image_url value (a data: URI for base64, or the URL verbatim).
func anthropicImageToDataURL(s *anthropicImageSource) string {
	if s == nil {
		return ""
	}
	switch s.Type {
	case "base64":
		if s.Data == "" {
			return ""
		}
		mt := s.MediaType
		if mt == "" {
			mt = "image/png"
		}
		return "data:" + mt + ";base64," + s.Data
	case "url":
		return s.URL
	}
	return ""
}

// toolResultText flattens an Anthropic tool_result content (string, or array of
// text/image blocks) into the plain string an OpenAI role:"tool" message wants.
func toolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	blocks, _ := parseAnthropicBlocks(raw)
	var parts []string
	for _, b := range blocks {
		if b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	// Unknown/structured content — pass through as JSON so the model sees it.
	return string(raw)
}

// anthropicToOpenAIMessages converts a full Anthropic request (system + messages
// with text/image/tool_use/tool_result/thinking) into OpenAI chat messages. A
// user turn carrying tool_result blocks expands into one OpenAI role:"tool"
// message per result (kept ahead of any user text, per the OpenAI contract).
func anthropicToOpenAIMessages(req *AnthropicRequest) []openai.ChatCompletionMessage {
	out := make([]openai.ChatCompletionMessage, 0, len(req.Messages)+1)
	if sys := req.SystemText(); sys != "" {
		out = append(out, openai.ChatCompletionMessage{Role: "system", Content: sys})
	}
	for _, m := range req.Messages {
		blocks, isStr := parseAnthropicBlocks(m.Content)
		if isStr {
			out = append(out, openai.ChatCompletionMessage{Role: m.Role, Content: blocks[0].Text})
			continue
		}
		if m.Role == "assistant" {
			out = append(out, assistantBlocksToOpenAI(blocks))
			continue
		}
		out = append(out, userBlocksToOpenAI(blocks)...)
	}
	return out
}

// assistantBlocksToOpenAI folds an assistant turn's text + tool_use blocks into
// one OpenAI assistant message (thinking blocks are dropped — OpenAI has no
// equivalent, and echoing them to the upstream is meaningless).
func assistantBlocksToOpenAI(blocks []anthropicBlock) openai.ChatCompletionMessage {
	msg := openai.ChatCompletionMessage{Role: "assistant"}
	var text strings.Builder
	for _, b := range blocks {
		switch b.Type {
		case "text":
			text.WriteString(b.Text)
		case "tool_use":
			args := strings.TrimSpace(string(b.Input))
			if args == "" || args == "null" {
				args = "{}"
			}
			msg.ToolCalls = append(msg.ToolCalls, openai.ToolCall{
				ID:   b.ID,
				Type: openai.ToolTypeFunction,
				Function: openai.FunctionCall{
					Name:      b.Name,
					Arguments: args,
				},
			})
		}
	}
	msg.Content = text.String()
	return msg
}

// userBlocksToOpenAI expands a user turn. tool_result blocks become standalone
// OpenAI role:"tool" messages (emitted first, so they immediately follow the
// prior assistant tool_calls); text/image blocks become a single user message
// (MultiContent when any image is present, plain Content otherwise).
func userBlocksToOpenAI(blocks []anthropicBlock) []openai.ChatCompletionMessage {
	var toolMsgs []openai.ChatCompletionMessage
	var parts []openai.ChatMessagePart
	var text strings.Builder
	hasImage := false

	for _, b := range blocks {
		switch b.Type {
		case "tool_result":
			toolMsgs = append(toolMsgs, openai.ChatCompletionMessage{
				Role:       "tool",
				ToolCallID: b.ToolUseID,
				Content:    toolResultText(b.Content),
			})
		case "text":
			text.WriteString(b.Text)
			parts = append(parts, openai.ChatMessagePart{
				Type: openai.ChatMessagePartTypeText,
				Text: b.Text,
			})
		case "image":
			if url := anthropicImageToDataURL(b.Source); url != "" {
				hasImage = true
				parts = append(parts, openai.ChatMessagePart{
					Type:     openai.ChatMessagePartTypeImageURL,
					ImageURL: &openai.ChatMessageImageURL{URL: url},
				})
			}
		}
	}

	out := toolMsgs
	switch {
	case hasImage:
		out = append(out, openai.ChatCompletionMessage{Role: "user", MultiContent: parts})
	case text.Len() > 0:
		out = append(out, openai.ChatCompletionMessage{Role: "user", Content: text.String()})
	}
	return out
}

// anthropicToolsToOpenAI converts Anthropic tool definitions to OpenAI function
// tools. An absent/empty input_schema becomes an empty object schema.
func anthropicToolsToOpenAI(tools []AnthropicTool) []openai.Tool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]openai.Tool, 0, len(tools))
	for _, t := range tools {
		var params interface{}
		if len(t.InputSchema) > 0 {
			_ = json.Unmarshal(t.InputSchema, &params)
		}
		if params == nil {
			params = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
		}
		out = append(out, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			},
		})
	}
	return out
}

// anthropicToolChoiceToOpenAI maps an Anthropic tool_choice to the OpenAI form:
// auto→"auto", any→"required", none→"none", tool→{type:function,function:{name}}.
func anthropicToolChoiceToOpenAI(raw json.RawMessage) interface{} {
	if len(raw) == 0 {
		return nil
	}
	var tc struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &tc); err != nil {
		return nil
	}
	switch tc.Type {
	case "auto":
		return "auto"
	case "any":
		return "required"
	case "none":
		return "none"
	case "tool":
		if tc.Name != "" {
			return openai.ToolChoice{
				Type:     openai.ToolTypeFunction,
				Function: openai.ToolFunction{Name: tc.Name},
			}
		}
	}
	return nil
}

// mapFinishReason maps an OpenAI finish_reason to an Anthropic stop_reason.
// tool_calls → tool_use is load-bearing: Claude Code reads it to know it must
// run a tool and continue the loop.
func mapFinishReason(fr string) string {
	switch fr {
	case "tool_calls", "function_call":
		return "tool_use"
	case "length":
		return "max_tokens"
	case "content_filter":
		return "end_turn"
	case "stop":
		return "end_turn"
	default:
		return "end_turn"
	}
}

// ── OpenAI streaming chunk (wire subset) ─────────────────────────────────────

type openaiStreamChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content          string                 `json:"content"`
			ReasoningContent string                 `json:"reasoning_content"`
			ToolCalls        []openaiStreamToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type openaiStreamToolCall struct {
	Index    *int   `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// ── Anthropic SSE stream translator ──────────────────────────────────────────

// anthropicStreamTranslator converts an upstream OpenAI SSE stream into Anthropic
// Messages API SSE events, tracking a single content-block index space shared by
// interleaved thinking, text, and tool_use blocks. It also captures token usage
// (from the forced include_usage chunk) for billing.
type anthropicStreamTranslator struct {
	emit  func(event string, data interface{}) error
	model string
	reqID string

	started   bool   // message_start emitted
	blockOpen bool   // a content block is currently open
	blockKind string // "thinking" | "text" | "tool"
	nextIndex int    // next content-block index to assign
	curIndex  int    // index of the currently-open block

	stopReason string

	prompt, completion, total int
}

func newAnthropicStreamTranslator(emit func(string, interface{}) error, model, reqID string) *anthropicStreamTranslator {
	return &anthropicStreamTranslator{emit: emit, model: model, reqID: reqID, stopReason: "end_turn"}
}

// ensureStarted emits message_start exactly once.
func (t *anthropicStreamTranslator) ensureStarted() error {
	if t.started {
		return nil
	}
	t.started = true
	return t.emit("message_start", map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id":            "msg_" + t.reqID,
			"type":          "message",
			"role":          "assistant",
			"content":       []interface{}{},
			"model":         t.model,
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         map[string]interface{}{"input_tokens": 0, "output_tokens": 0},
		},
	})
}

// closeBlock emits content_block_stop for the open block, if any.
func (t *anthropicStreamTranslator) closeBlock() error {
	if !t.blockOpen {
		return nil
	}
	t.blockOpen = false
	kind := t.blockKind
	t.blockKind = ""
	_ = kind
	return t.emit("content_block_stop", map[string]interface{}{
		"type":  "content_block_stop",
		"index": t.curIndex,
	})
}

// openBlock closes any open block, then opens a new one of the given kind at the
// next index and emits its content_block_start.
func (t *anthropicStreamTranslator) openBlock(kind string, contentBlock map[string]interface{}) error {
	if err := t.closeBlock(); err != nil {
		return err
	}
	t.curIndex = t.nextIndex
	t.nextIndex++
	t.blockOpen = true
	t.blockKind = kind
	return t.emit("content_block_start", map[string]interface{}{
		"type":          "content_block_start",
		"index":         t.curIndex,
		"content_block": contentBlock,
	})
}

// ensureKind makes sure a block of the given kind (thinking or text) is open,
// opening one if the current block is a different kind or none is open.
func (t *anthropicStreamTranslator) ensureKind(kind string) error {
	if t.blockOpen && t.blockKind == kind {
		return nil
	}
	switch kind {
	case "thinking":
		return t.openBlock("thinking", map[string]interface{}{"type": "thinking", "thinking": ""})
	default: // text
		return t.openBlock("text", map[string]interface{}{"type": "text", "text": ""})
	}
}

// handleChunk translates one OpenAI chunk into Anthropic SSE events.
func (t *anthropicStreamTranslator) handleChunk(c *openaiStreamChunk) error {
	if err := t.ensureStarted(); err != nil {
		return err
	}
	if c.Usage != nil {
		if c.Usage.PromptTokens > 0 {
			t.prompt = c.Usage.PromptTokens
		}
		if c.Usage.CompletionTokens > 0 {
			t.completion = c.Usage.CompletionTokens
		}
		if c.Usage.TotalTokens > 0 {
			t.total = c.Usage.TotalTokens
		}
	}
	for i := range c.Choices {
		ch := &c.Choices[i]

		// reasoning_content → thinking block (never leak the raw OpenAI field).
		if ch.Delta.ReasoningContent != "" {
			if err := t.ensureKind("thinking"); err != nil {
				return err
			}
			if err := t.emit("content_block_delta", map[string]interface{}{
				"type":  "content_block_delta",
				"index": t.curIndex,
				"delta": map[string]interface{}{"type": "thinking_delta", "thinking": ch.Delta.ReasoningContent},
			}); err != nil {
				return err
			}
		}

		// content → text block.
		if ch.Delta.Content != "" {
			if err := t.ensureKind("text"); err != nil {
				return err
			}
			if err := t.emit("content_block_delta", map[string]interface{}{
				"type":  "content_block_delta",
				"index": t.curIndex,
				"delta": map[string]interface{}{"type": "text_delta", "text": ch.Delta.Content},
			}); err != nil {
				return err
			}
		}

		// tool_calls → tool_use blocks + input_json_delta.
		for j := range ch.Delta.ToolCalls {
			tc := &ch.Delta.ToolCalls[j]
			// A new tool call is signalled by an id and/or a function name.
			if tc.ID != "" || tc.Function.Name != "" {
				if err := t.openBlock("tool", map[string]interface{}{
					"type":  "tool_use",
					"id":    tc.ID,
					"name":  tc.Function.Name,
					"input": map[string]interface{}{},
				}); err != nil {
					return err
				}
			}
			if tc.Function.Arguments != "" {
				// Arguments stream as a JSON fragment; if no tool block is open
				// (defensive), open an unnamed one so we never drop input.
				if !t.blockOpen || t.blockKind != "tool" {
					if err := t.openBlock("tool", map[string]interface{}{
						"type": "tool_use", "id": tc.ID, "name": tc.Function.Name, "input": map[string]interface{}{},
					}); err != nil {
						return err
					}
				}
				if err := t.emit("content_block_delta", map[string]interface{}{
					"type":  "content_block_delta",
					"index": t.curIndex,
					"delta": map[string]interface{}{"type": "input_json_delta", "partial_json": tc.Function.Arguments},
				}); err != nil {
					return err
				}
			}
		}

		if ch.FinishReason != "" {
			t.stopReason = mapFinishReason(ch.FinishReason)
		}
	}
	return nil
}

// finish closes the open block and emits message_delta + message_stop.
func (t *anthropicStreamTranslator) finish() error {
	if err := t.ensureStarted(); err != nil {
		return err
	}
	if err := t.closeBlock(); err != nil {
		return err
	}
	if err := t.emit("message_delta", map[string]interface{}{
		"type":  "message_delta",
		"delta": map[string]interface{}{"stop_reason": t.stopReason, "stop_sequence": nil},
		"usage": map[string]interface{}{"input_tokens": t.prompt, "output_tokens": t.completion},
	}); err != nil {
		return err
	}
	return t.emit("message_stop", map[string]interface{}{"type": "message_stop"})
}

// streamCaptureAnthropicUsage copies a NATIVE Anthropic SSE stream verbatim to w
// while capturing token usage from message_start (input_tokens) and message_delta
// (output_tokens). The native upstream already emits correct Anthropic SSE, so the
// bytes pass through unchanged — only the usage capture (previously done with an
// OpenAI-shaped parser that always returned 0, billing native tool streams at $0)
// is fixed here.
func streamCaptureAnthropicUsage(r io.Reader, w io.Writer, flush func()) (prompt, completion int) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			raw := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if raw != "" && raw != "[DONE]" {
				var ev struct {
					Message struct {
						Usage struct {
							InputTokens  int `json:"input_tokens"`
							OutputTokens int `json:"output_tokens"`
						} `json:"usage"`
					} `json:"message"`
					Usage struct {
						InputTokens  int `json:"input_tokens"`
						OutputTokens int `json:"output_tokens"`
					} `json:"usage"`
				}
				if json.Unmarshal([]byte(raw), &ev) == nil {
					if ev.Message.Usage.InputTokens > 0 {
						prompt = ev.Message.Usage.InputTokens
					}
					if ev.Message.Usage.OutputTokens > 0 {
						completion = ev.Message.Usage.OutputTokens
					}
					if ev.Usage.InputTokens > 0 {
						prompt = ev.Usage.InputTokens
					}
					if ev.Usage.OutputTokens > 0 {
						completion = ev.Usage.OutputTokens
					}
				}
			}
		}
		_, _ = fmt.Fprintf(w, "%s\n", line)
		if flush != nil {
			flush()
		}
	}
	return prompt, completion
}

// translateOpenAIStream reads an upstream OpenAI SSE stream and drives the
// translator, returning captured usage. It never writes raw OpenAI frames.
func translateOpenAIStream(r io.Reader, emit func(string, interface{}) error, model, reqID string) (prompt, completion, total int) {
	t := newAnthropicStreamTranslator(emit, model, reqID)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if raw == "" || raw == "[DONE]" {
			continue
		}
		var chunk openaiStreamChunk
		if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
			continue
		}
		if err := t.handleChunk(&chunk); err != nil {
			break
		}
	}
	_ = t.finish()
	return t.prompt, t.completion, t.total
}

// ── OpenAI non-streaming response → Anthropic JSON ───────────────────────────

// openAIResponseToAnthropic converts a non-streaming OpenAI chat completion into
// an Anthropic Messages response (content blocks + stop_reason + usage).
func openAIResponseToAnthropic(body []byte, model, reqID string) (resp map[string]interface{}, prompt, completion int) {
	var oai struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				ToolCalls        []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(body, &oai)

	content := []interface{}{}
	stopReason := "end_turn"
	if len(oai.Choices) > 0 {
		ch := oai.Choices[0]
		if ch.Message.ReasoningContent != "" {
			content = append(content, map[string]interface{}{"type": "thinking", "thinking": ch.Message.ReasoningContent})
		}
		if ch.Message.Content != "" {
			content = append(content, map[string]interface{}{"type": "text", "text": ch.Message.Content})
		}
		for _, tc := range ch.Message.ToolCalls {
			var input interface{}
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &input); err != nil || input == nil {
				input = map[string]interface{}{}
			}
			content = append(content, map[string]interface{}{
				"type":  "tool_use",
				"id":    tc.ID,
				"name":  tc.Function.Name,
				"input": input,
			})
		}
		stopReason = mapFinishReason(ch.FinishReason)
	}
	// A tool_use content block always means stop_reason tool_use, even when the
	// upstream omitted finish_reason.
	for _, b := range content {
		if m, ok := b.(map[string]interface{}); ok && m["type"] == "tool_use" {
			stopReason = "tool_use"
		}
	}

	resp = map[string]interface{}{
		"id":            "msg_" + reqID,
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       content,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage": map[string]interface{}{
			"input_tokens":  oai.Usage.PromptTokens,
			"output_tokens": oai.Usage.CompletionTokens,
		},
	}
	return resp, oai.Usage.PromptTokens, oai.Usage.CompletionTokens
}
