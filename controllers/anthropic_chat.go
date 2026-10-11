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

// anthropic_chat.go — a chat completion asked of Anthropic's own Messages API.
//
// anthropic_translate.go carries a Messages request to a vendor that speaks chat
// completions, and its answer back. This is the same translation run the other
// way, for a Claude model asked of its maker (native.go):
//
//	request  : a chat completion body      → a Messages body   (chatAsMessages)
//	response : a Messages event stream     → chat completion chunks (claudeChat)
//
// A chat is always asked of the vendor as a stream: the pipe assembles a whole
// answer from one (assembleZenStream), and /v1/responses is a chat by the time it
// arrives here, so the one stream translation serves all three.
//
// What the Messages API has no word for is not guessed at. A request that carries
// such a thing — audio, a stored file, a JSON mode with no schema, an answer begun
// for the model — is errUntranslated, and is sent where it was sent before.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"time"
)

// chatTurn is one message of a chat completion request, as far as it translates.
type chatTurn struct {
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	ToolCalls []struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
	ToolCallID   string          `json:"tool_call_id"`
	FunctionCall json.RawMessage `json:"function_call"`
}

// chatPart is one part of a message's content.
type chatPart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url"`
	File *struct {
		Data string `json:"file_data"`
		ID   string `json:"file_id"`
	} `json:"file"`
	// CacheControl is the caller's own cache breakpoint, in the vendor's words.
	CacheControl json.RawMessage `json:"cache_control"`
}

// claudeImages are the image types the vendor reads.
var claudeImages = fieldSet("image/jpeg", "image/png", "image/gif", "image/webp")

// inline reads a data: URL into its media type and base64 payload.
func inline(u string) (media, data string, ok bool) {
	rest, ok := strings.CutPrefix(u, "data:")
	if !ok {
		return "", "", false
	}
	head, data, ok := strings.Cut(rest, ",")
	media, encoding, _ := strings.Cut(head, ";")
	if !ok || !strings.EqualFold(encoding, "base64") || data == "" {
		return "", "", false
	}
	return strings.ToLower(strings.TrimSpace(media)), data, true
}

// blocks are a message's content as the vendor's content blocks. A plain string
// is one text block; text that says nothing is no block, since the vendor refuses
// an empty one. only names the one kind of part the message may hold, "" for any.
func blocks(raw json.RawMessage, m claude, only string) ([]map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return nil, nil
		}
		return []map[string]any{{"type": "text", "text": s}}, nil
	}
	var parts []chatPart
	if json.Unmarshal(raw, &parts) != nil {
		return nil, errUntranslated
	}
	out := make([]map[string]any, 0, len(parts))
	for _, p := range parts {
		if only != "" && p.Type != only {
			return nil, errUntranslated
		}
		var b map[string]any
		switch p.Type {
		case "text":
			if p.Text == "" {
				continue
			}
			b = map[string]any{"type": "text", "text": p.Text}
		case "image_url":
			if p.ImageURL == nil || !m.Vision {
				return nil, errUntranslated
			}
			if media, data, ok := inline(p.ImageURL.URL); ok {
				if !claudeImages[media] {
					return nil, errUntranslated
				}
				b = map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": media, "data": data}}
			} else if strings.HasPrefix(p.ImageURL.URL, "https://") || strings.HasPrefix(p.ImageURL.URL, "http://") {
				b = map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": p.ImageURL.URL}}
			} else {
				return nil, errUntranslated
			}
		case "file":
			// A PDF sent whole. A file the caller stored elsewhere is not the
			// vendor's to find.
			if p.File == nil {
				return nil, errUntranslated
			}
			media, data, ok := inline(p.File.Data)
			if !ok || media != "application/pdf" {
				return nil, errUntranslated
			}
			b = map[string]any{"type": "document", "source": map[string]any{"type": "base64", "media_type": media, "data": data}}
		default:
			return nil, errUntranslated
		}
		if present(p.CacheControl) {
			b["cache_control"] = p.CacheControl
		}
		out = append(out, b)
	}
	return out, nil
}

// effortOf is the effort level a model is asked for when a chat asks for asked:
// that level where the model takes it, else the nearest it takes below, else the
// least it takes. "" for a model that takes none.
func effortOf(asked string, m claude) string {
	want := -1
	switch strings.ToLower(strings.TrimSpace(asked)) {
	case "none", "off", "minimal", "low":
		want = 0
	case "medium":
		want = 1
	case "high":
		want = 2
	case "xhigh":
		want = 3
	case "max":
		want = 4
	}
	if want < 0 {
		return ""
	}
	for i := want; i >= 0; i-- {
		if m.Efforts[efforts[i]] {
			return efforts[i]
		}
	}
	for _, e := range efforts[want:] {
		if m.Efforts[e] {
			return e
		}
	}
	return ""
}

// budgetShare is the part of an answer's ceiling a model that thinks within a
// budget is given for each effort a chat can ask.
var budgetShare = map[string]float64{"minimal": 0.1, "low": 0.2, "medium": 0.5, "high": 0.8, "xhigh": 0.95, "max": 0.95}

// leastBudget is the smallest thinking budget the vendor takes.
const leastBudget = 1024

// chatAsMessages is a chat completion body as the Messages body that asks the
// same of m, always as a stream. hide says the caller asked for the model's
// reasoning not to be written out.
//
// The fields are the ones a family is sent (familyFields): what shapes the
// answer. Each has the vendor's word or is left behind where the vendor has none
// and the answer does not depend on it (seed, penalties, logit_bias, logprobs).
// A sampling parameter and a way of thinking are sent only to a model whose
// listing says it takes them.
func chatAsMessages(body []byte, m claude) (out []byte, hide bool, err error) {
	var in struct {
		Messages    []chatTurn      `json:"messages"`
		MaxTokens   int             `json:"max_tokens"`
		Temperature *float64        `json:"temperature"`
		TopP        *float64        `json:"top_p"`
		TopK        *int            `json:"top_k"`
		Stop        json.RawMessage `json:"stop"`
		Tools       []struct {
			Type     string `json:"type"`
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
				Strict      bool            `json:"strict"`
			} `json:"function"`
			CacheControl json.RawMessage `json:"cache_control"`
		} `json:"tools"`
		ToolChoice    json.RawMessage `json:"tool_choice"`
		Parallel      *bool           `json:"parallel_tool_calls"`
		Format        json.RawMessage `json:"response_format"`
		Effort        string          `json:"reasoning_effort"`
		CacheControl  json.RawMessage `json:"cache_control"`
		ReasoningWish *struct {
			Effort    string `json:"effort"`
			MaxTokens int    `json:"max_tokens"`
			Exclude   bool   `json:"exclude"`
			Enabled   *bool  `json:"enabled"`
		} `json:"reasoning"`
	}
	if json.Unmarshal(body, &in) != nil {
		return nil, false, errUntranslated
	}

	// ── the conversation ──
	// Every system turn is the system prompt, in order. Turns of one role that
	// follow each other are one turn: the vendor reads a tool's results, and what
	// the user said after them, as a single message.
	type turn struct {
		role   string
		blocks []map[string]any
	}
	var system []map[string]any
	var turns []turn
	push := func(role string, bs []map[string]any) {
		if len(bs) == 0 {
			return
		}
		if n := len(turns); n > 0 && turns[n-1].role == role {
			turns[n-1].blocks = append(turns[n-1].blocks, bs...)
			return
		}
		turns = append(turns, turn{role, bs})
	}
	looping := false // the model's last turn called tools and nothing was said since
	for _, msg := range in.Messages {
		switch msg.Role {
		case "system", "developer":
			bs, err := blocks(msg.Content, m, "text")
			if err != nil {
				return nil, false, err
			}
			system = append(system, bs...)
		case "user":
			bs, err := blocks(msg.Content, m, "")
			if err != nil {
				return nil, false, err
			}
			push("user", bs)
			looping = false
		case "assistant":
			if present(msg.FunctionCall) {
				return nil, false, errUntranslated
			}
			bs, err := blocks(msg.Content, m, "text")
			if err != nil {
				return nil, false, err
			}
			for _, tc := range msg.ToolCalls {
				if (tc.Type != "" && tc.Type != "function") || tc.ID == "" || tc.Function.Name == "" {
					return nil, false, errUntranslated
				}
				// The vendor takes a call's arguments as an object.
				input := json.RawMessage(tc.Function.Arguments)
				var obj map[string]json.RawMessage
				if json.Unmarshal(input, &obj) != nil || obj == nil {
					input = json.RawMessage("{}")
				}
				bs = append(bs, map[string]any{"type": "tool_use", "id": tc.ID, "name": tc.Function.Name, "input": input})
			}
			push("assistant", bs)
			looping = len(msg.ToolCalls) > 0
		case "tool":
			if msg.ToolCallID == "" {
				return nil, false, errUntranslated
			}
			result := map[string]any{"type": "tool_result", "tool_use_id": msg.ToolCallID}
			var text string
			if json.Unmarshal(msg.Content, &text) == nil {
				result["content"] = text
			} else {
				bs, err := blocks(msg.Content, m, "")
				if err != nil {
					return nil, false, err
				}
				result["content"] = bs
			}
			push("user", []map[string]any{result})
		default:
			return nil, false, errUntranslated
		}
	}
	// The vendor takes a conversation that opens with the user and waits on the
	// model. An answer begun for the model is one the newer models refuse.
	if len(turns) == 0 || turns[0].role != "user" || turns[len(turns)-1].role != "user" {
		return nil, false, errUntranslated
	}
	messages := make([]map[string]any, 0, len(turns))
	for _, t := range turns {
		messages = append(messages, map[string]any{"role": t.role, "content": t.blocks})
	}

	ceiling := in.MaxTokens
	if ceiling <= 0 {
		ceiling = reserveCompletionFloor
	}
	if m.Out > 0 && ceiling > m.Out {
		ceiling = m.Out
	}
	req := map[string]any{"model": m.ID, "messages": messages, "max_tokens": ceiling, "stream": true}
	if len(system) > 0 {
		req["system"] = system
	}

	// ── tools ──
	forced := false
	if len(in.Tools) > 0 {
		tools := make([]map[string]any, 0, len(in.Tools))
		for _, t := range in.Tools {
			if t.Type != "function" || t.Function.Name == "" {
				return nil, false, errUntranslated
			}
			tool := map[string]any{"name": t.Function.Name, "input_schema": json.RawMessage(`{"type":"object","properties":{}}`)}
			if t.Function.Description != "" {
				tool["description"] = t.Function.Description
			}
			if present(t.Function.Parameters) {
				tool["input_schema"] = t.Function.Parameters
			}
			if t.Function.Strict && m.Schema {
				tool["strict"] = true
			}
			if present(t.CacheControl) {
				tool["cache_control"] = t.CacheControl
			}
			tools = append(tools, tool)
		}
		req["tools"] = tools

		choice := map[string]any{}
		var word string
		var named struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		switch {
		case !present(in.ToolChoice):
		case json.Unmarshal(in.ToolChoice, &word) == nil:
			switch word {
			case "auto":
				choice["type"] = "auto"
			case "none":
				choice["type"] = "none"
			case "required":
				choice["type"], forced = "any", true
			default:
				return nil, false, errUntranslated
			}
		case json.Unmarshal(in.ToolChoice, &named) == nil && named.Type == "function" && named.Function.Name != "":
			choice["type"], choice["name"], forced = "tool", named.Function.Name, true
		default:
			return nil, false, errUntranslated
		}
		if in.Parallel != nil && !*in.Parallel && choice["type"] != "none" {
			if choice["type"] == nil {
				choice["type"] = "auto"
			}
			choice["disable_parallel_tool_use"] = true
		}
		if len(choice) > 0 {
			req["tool_choice"] = choice
		}
	}

	// ── the answer's shape ──
	config := map[string]any{}
	var format struct {
		Type   string `json:"type"`
		Schema struct {
			Schema json.RawMessage `json:"schema"`
		} `json:"json_schema"`
	}
	if present(in.Format) {
		if json.Unmarshal(in.Format, &format) != nil {
			return nil, false, errUntranslated
		}
		switch format.Type {
		case "", "text":
		case "json_schema":
			if !m.Schema || !present(format.Schema.Schema) {
				return nil, false, errUntranslated
			}
			config["format"] = map[string]any{"type": "json_schema", "schema": format.Schema.Schema}
		default:
			// A JSON mode with no schema is one the vendor has no word for, and an
			// answer that skipped it would read as one that followed it.
			return nil, false, errUntranslated
		}
	}

	// ── thinking ──
	// A chat asks for reasoning by effort, by a budget of tokens, or not at all.
	// A model that thinks adaptively is given the effort; one that thinks within a
	// budget is given the budget, but never in the middle of a tool loop. Neither
	// is asked to think beside a forced tool call, which the vendor refuses.
	effort, budget, off := in.Effort, 0, false
	if w := in.ReasoningWish; w != nil {
		if w.Effort != "" {
			effort = w.Effort
		}
		budget, hide = w.MaxTokens, w.Exclude
		off = w.Enabled != nil && !*w.Enabled
	}
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "none", "off":
		off = true
	}
	asked := !off && (effort != "" || budget > 0 || (in.ReasoningWish != nil && in.ReasoningWish.Enabled != nil))
	switch {
	case m.Adaptive:
		if asked && !forced {
			req["thinking"] = map[string]any{"type": "adaptive"}
		}
		if e := effortOf(effort, m); e != "" {
			config["effort"] = e
		}
	case m.Budget && asked && !looping && !forced:
		if budget == 0 {
			budget = int(budgetShare[strings.ToLower(strings.TrimSpace(effort))] * float64(ceiling))
		}
		if budget = min(max(budget, leastBudget), ceiling-1); budget >= leastBudget {
			req["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
		}
	}
	if len(config) > 0 {
		req["output_config"] = config
	}

	// ── sampling ──
	// The models that think only adaptively take no sampling parameter, and none
	// does while it thinks. The vendor's temperature runs to 1, and it takes a
	// temperature or a top_p, not both.
	if _, thinks := req["thinking"]; m.Budget && !thinks {
		switch {
		case in.Temperature != nil:
			req["temperature"] = min(max(*in.Temperature, 0), 1)
		case in.TopP != nil:
			req["top_p"] = *in.TopP
		}
		if in.TopK != nil {
			req["top_k"] = *in.TopK
		}
	}
	if present(in.Stop) {
		var one string
		var many []string
		if json.Unmarshal(in.Stop, &one) == nil {
			many = []string{one}
		} else if json.Unmarshal(in.Stop, &many) != nil {
			return nil, false, errUntranslated
		}
		var stops []string
		for _, s := range many {
			if strings.TrimSpace(s) != "" {
				stops = append(stops, s)
			}
		}
		if len(stops) > 0 {
			req["stop_sequences"] = stops
		}
	}

	// ── the prompt cache ──
	// The vendor caches only a prompt the request marks. A caller's own marks
	// travel as written; a request with none is asked to cache up to its last
	// block (claudeCache), so the next turn reads that prefix at the cache rate.
	switch {
	case present(in.CacheControl):
		req["cache_control"] = in.CacheControl
	case !bytes.Contains(body, []byte(`"cache_control"`)):
		req["cache_control"] = ephemeral
	}

	if out, err = json.Marshal(req); err != nil {
		return nil, false, errUntranslated
	}
	return out, hide, nil
}

// chatFinish is the finish_reason a chat is told for the vendor's stop_reason.
func chatFinish(stop string) string {
	switch stop {
	case "tool_use":
		return "tool_calls"
	case "max_tokens", "model_context_window_exceeded":
		return "length"
	case "refusal":
		return "content_filter"
	}
	return "stop"
}

// claudeChat reads a Messages event stream and is read as the chat completion
// stream that says the same: a role, then text, reasoning and tool calls as they
// arrive, the finish reason, the usage, and [DONE]. The usage counts the prompt
// whole, with what the cache served and what was written to it stated inside it,
// as every chat usage here does (sniffZenUsage).
//
// It is pulled, not pushed: a Read translates as many events as it takes to have
// something to hand back, so a reader that stops reading stops the vendor, and
// Close hangs up on it.
type claudeChat struct {
	src     io.ReadCloser
	br      *bufio.Reader
	out     bytes.Buffer
	model   string
	created int64
	hide    bool
	calls   map[int]int // content block index → the tool call's index
	used    AnthropicUsage
	final   bool
	stop    string
	wrote   int
	done    bool
	// end is told what the answer used once, when the stream ends however it ends:
	// at its last event, when the vendor breaks off, or when its reader hangs up.
	end func(used AnthropicUsage, final bool, wrote int)
	// mu holds what a Read translated against a Close from another goroutine.
	mu sync.Mutex
}

func newClaudeChat(src io.ReadCloser, model string, hide bool, end func(AnthropicUsage, bool, int)) *claudeChat {
	return &claudeChat{
		src: src, br: bufio.NewReaderSize(src, 64<<10), model: model, created: time.Now().Unix(),
		hide: hide, calls: map[int]int{}, end: end,
	}
}

// Close hangs up on the vendor and, for a stream that had not ended, says what it
// used as far as it got: the vendor bills what it wrote either way.
func (s *claudeChat) Close() error {
	err := s.src.Close()
	s.mu.Lock()
	s.finish()
	s.mu.Unlock()
	return err
}

func (s *claudeChat) Read(p []byte) (int, error) {
	for {
		s.mu.Lock()
		if s.out.Len() > 0 {
			n, err := s.out.Read(p)
			s.mu.Unlock()
			return n, err
		}
		if s.done {
			s.mu.Unlock()
			return 0, io.EOF
		}
		s.mu.Unlock()
		// Not under mu: this is the read a Close has to be able to break.
		line, err := s.br.ReadBytes('\n')
		s.mu.Lock()
		for !s.done {
			s.event(strings.TrimRight(string(line), "\r\n"))
			if err != nil {
				// The vendor broke off before it said the answer was over.
				s.broken("the answer was cut off upstream")
				break
			}
			// Every line the vendor has already sent is translated now, so what the
			// stream knows it wrote never waits on how fast it is read: a reader
			// that hangs up is billed for what had arrived, not for what it took.
			if !s.arrived() {
				break
			}
			line, err = s.br.ReadBytes('\n')
		}
		s.mu.Unlock()
	}
}

// arrived reports whether a whole line is already buffered, so reading it does
// not wait on the vendor.
func (s *claudeChat) arrived() bool {
	n := s.br.Buffered()
	if n == 0 {
		return false
	}
	b, _ := s.br.Peek(n)
	return bytes.IndexByte(b, '\n') >= 0
}

// broken ends an answer the vendor did not finish. What was written is the
// caller's, marked as cut short (`length`) so that neither a stream's reader nor
// the whole answer assembled from it takes it for one that finished, and then the
// reason is said. mu is held.
func (s *claudeChat) broken(why string) {
	if s.wrote > 0 {
		s.chunk(map[string]any{}, "length")
	}
	s.frame(map[string]any{"error": map[string]any{"message": why, "type": "upstream_error"}})
	s.finish()
}

// finish ends the stream once and says what it used. mu is held.
func (s *claudeChat) finish() {
	if s.done {
		return
	}
	s.done = true
	s.end(s.used, s.final, s.wrote)
}

// chunk writes one chat completion chunk: a delta, or the finish reason.
func (s *claudeChat) chunk(delta map[string]any, finish any) {
	s.frame(map[string]any{
		"object": "chat.completion.chunk", "created": s.created, "model": s.model,
		"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}},
	})
}

func (s *claudeChat) frame(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	s.out.WriteString("data: ")
	s.out.Write(b)
	s.out.WriteString("\n\n")
}

// event translates one line of the vendor's stream.
func (s *claudeChat) event(line string) {
	if strings.HasPrefix(line, ":") {
		s.out.WriteString(":\n\n") // the vendor keeping the connection open
		return
	}
	var ev messagesEvent
	if !ev.read(line, &s.used, &s.final) {
		return
	}
	switch ev.Type {
	case "message_start":
		s.chunk(map[string]any{"role": "assistant", "content": ""}, nil)
	case "ping":
		s.out.WriteString(":\n\n")
	case "content_block_start":
		switch ev.ContentBlock.Type {
		case "tool_use":
			i := len(s.calls)
			s.calls[ev.Index] = i
			s.chunk(map[string]any{"tool_calls": []map[string]any{{
				"index": i, "id": ev.ContentBlock.ID, "type": "function",
				"function": map[string]any{"name": ev.ContentBlock.Name, "arguments": ""},
			}}}, nil)
		case "text":
			if ev.ContentBlock.Text != "" {
				s.wrote += len(ev.ContentBlock.Text)
				s.chunk(map[string]any{"content": ev.ContentBlock.Text}, nil)
			}
		}
	case "content_block_delta":
		switch ev.Delta.Type {
		case "text_delta":
			s.wrote += len(ev.Delta.Text)
			s.chunk(map[string]any{"content": ev.Delta.Text}, nil)
		case "thinking_delta":
			s.wrote += len(ev.Delta.Thinking)
			if !s.hide && ev.Delta.Thinking != "" {
				s.chunk(map[string]any{"reasoning_content": ev.Delta.Thinking}, nil)
			}
		case "input_json_delta":
			s.wrote += len(ev.Delta.PartialJSON)
			if i, ok := s.calls[ev.Index]; ok {
				s.chunk(map[string]any{"tool_calls": []map[string]any{{
					"index": i, "function": map[string]any{"arguments": ev.Delta.PartialJSON},
				}}}, nil)
			}
		}
	case "message_delta":
		if ev.Delta.StopReason != "" {
			s.stop = ev.Delta.StopReason
		}
	case "message_stop":
		s.chunk(map[string]any{}, chatFinish(s.stop))
		t := s.used.billed(s.model)
		s.frame(map[string]any{
			"object": "chat.completion.chunk", "created": s.created, "model": s.model, "choices": []any{},
			"usage": map[string]any{
				"prompt_tokens": t.prompt(), "completion_tokens": t.out, "total_tokens": t.prompt() + t.out,
				"prompt_tokens_details": map[string]any{"cached_tokens": t.read, "cache_write_tokens": t.write},
			},
		})
		s.out.WriteString("data: [DONE]\n\n")
		s.finish()
	case "error":
		// An answer that broke off after it began: the caller holds what was
		// written, and is told why there is no more.
		said := sayable(ev.Error.Message)
		if said == "" {
			said = "the answer was cut off upstream"
		}
		s.broken(said)
	}
}
