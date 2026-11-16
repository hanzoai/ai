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
	"io"

	"github.com/hanzoai/ai/model"
)

// Relay-side wrappers over the shared reasoning normalizer (package model). They
// rewrite the wire JSON — an OpenAI SSE chunk (streaming) or a full completion
// body (non-streaming) — stripping a reasoning-inlining upstream's leading
// <think></think> block from content so it never reaches the client. Both are
// fail-safe: any parse error returns the input unchanged, so a malformed chunk
// can never corrupt the stream or drop a response.

// strippedTail is the chunk that lets go what the stripper still holds when a
// stream ends with no finish_reason: nil when it holds nothing.
func strippedTail(s *model.ReasoningStripper) []byte {
	if s == nil {
		return nil
	}
	held := s.Flush()
	if held == "" {
		return nil
	}
	b, _ := json.Marshal(map[string]any{"object": "chat.completion.chunk",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": held}, "finish_reason": nil}}})
	return b
}

// applyReasoningStrip rewrites the content deltas of one OpenAI SSE data chunk
// (the JSON after "data: ") through the stripper. Billing is unaffected — the
// caller counts the original content before this runs.
func applyReasoningStrip(raw string, s *model.ReasoningStripper) string {
	var chunk map[string]any
	if json.Unmarshal([]byte(raw), &chunk) != nil {
		return raw
	}
	choices, ok := chunk["choices"].([]any)
	if !ok {
		return raw
	}
	changed := false
	for _, c := range choices {
		choice, ok := c.(map[string]any)
		if !ok {
			continue
		}
		delta, ok := choice["delta"].(map[string]any)
		if !ok {
			delta = map[string]any{}
		}
		content, _ := delta["content"].(string)
		out := ""
		// An upstream that sends its reasoning apart sends an answer in content.
		for _, k := range []string{"reasoning_content", "reasoning"} {
			if r, _ := delta[k].(string); r != "" {
				out = s.Reasoned()
			}
		}
		out += s.Feed(content)
		// The answer ending is the last chance to let go what is held.
		if fin, _ := choice["finish_reason"].(string); fin != "" {
			out += s.Flush()
		}
		if out == content {
			continue
		}
		delta["content"] = out
		choice["delta"] = delta
		changed = true
	}
	if !changed {
		return raw
	}
	out, err := json.Marshal(chunk)
	if err != nil {
		return raw
	}
	return string(out)
}

// stripReasoningBody rewrites message.content in a non-streaming OpenAI chat
// completion response, removing each choice's leading reasoning block.
func stripReasoningBody(body []byte, eaten bool) []byte {
	var resp map[string]any
	if json.Unmarshal(body, &resp) != nil {
		return body
	}
	choices, ok := resp["choices"].([]any)
	if !ok {
		return body
	}
	changed := false
	for _, c := range choices {
		choice, ok := c.(map[string]any)
		if !ok {
			continue
		}
		msg, ok := choice["message"].(map[string]any)
		if !ok {
			continue
		}
		content, ok := msg["content"].(string)
		if !ok || content == "" {
			continue
		}
		if clean := model.StripReasoning(content, eaten); clean != content {
			msg["content"] = clean
			changed = true
		}
	}
	if !changed {
		return body
	}
	if out, err := json.Marshal(resp); err == nil {
		return out
	}
	return body
}

// readAnswer reads an upstream chat SSE stream the way every relay that
// translates it must: the inline reasoning comes off first, and only what is left
// is read for tool calls. One order, here, so a path cannot lift from text the
// caller will never see (an unclosed fence in the reasoning desyncs the scanner).
func readAnswer(r io.Reader, strip *model.ReasoningStripper, l *lifter) io.Reader {
	if strip != nil {
		r = &reasonedStream{strip: strip, in: bufio.NewReader(r)}
	}
	return l.stream(r)
}

// readAnswerBody is readAnswer for a buffered chat completion.
func readAnswerBody(body []byte, eaten, inlines bool, l *lifter) []byte {
	if inlines {
		body = stripReasoningBody(body, eaten)
	}
	return l.payload(body)
}

// reasonedStream is an SSE stream with its inline reasoning removed, and what the
// stripper still holds let go before [DONE] or at the end.
type reasonedStream struct {
	strip *model.ReasoningStripper
	in    *bufio.Reader
	out   []byte
	done  bool
}

func (s *reasonedStream) Read(p []byte) (int, error) {
	for len(s.out) == 0 {
		if s.done {
			return 0, io.EOF
		}
		line, err := s.in.ReadBytes('\n')
		if err != nil {
			s.done = true
		}
		s.out = s.rewrite(line)
		if s.done {
			s.out = append(s.out, s.tail()...)
		}
	}
	n := copy(p, s.out)
	s.out = s.out[n:]
	return n, nil
}

func (s *reasonedStream) rewrite(line []byte) []byte {
	data, ok := bytes.CutPrefix(bytes.TrimRight(line, "\r\n"), []byte("data:"))
	data = bytes.TrimSpace(data)
	switch {
	case ok && string(data) == "[DONE]":
		return append(s.tail(), line...)
	case ok && len(data) > 0 && data[0] == '{':
		return append([]byte("data: "+applyReasoningStrip(string(data), s.strip)), '\n')
	}
	return line
}

func (s *reasonedStream) tail() []byte {
	held := strippedTail(s.strip)
	if held == nil {
		return nil
	}
	return append(append([]byte("data: "), held...), '\n', '\n')
}
