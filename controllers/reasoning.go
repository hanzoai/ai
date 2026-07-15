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
	"encoding/json"

	"github.com/hanzoai/ai/model"
)

// Relay-side wrappers over the shared reasoning normalizer (package model). They
// rewrite the wire JSON — an OpenAI SSE chunk (streaming) or a full completion
// body (non-streaming) — stripping a reasoning-inlining upstream's leading
// <think></think> block from content so it never reaches the client. Both are
// fail-safe: any parse error returns the input unchanged, so a malformed chunk
// can never corrupt the stream or drop a response.

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
			continue
		}
		content, ok := delta["content"].(string)
		if !ok || content == "" {
			continue
		}
		delta["content"] = s.Feed(content)
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
func stripReasoningBody(body []byte) []byte {
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
		if clean := model.StripLeadingReasoning(content); clean != content {
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
