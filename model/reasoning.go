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

package model

import "strings"

// Reasoning normalization across heterogeneous upstreams.
//
// Zen aliases front models that expose chain-of-thought differently: GLM (zen5,
// zen5-coder) returns it in the separate reasoning_content field, but DeepSeek
// (zen5-pro → deepseek-v4-pro, zen5-flash → deepseek-4-flash) inlines it into
// content as `<reasoning></think><answer>` — the opening <think> consumed by its
// chat template. Presented through one Anthropic/OpenAI-shaped surface with no
// normalization, DeepSeek's closing </think> and its reasoning text leak into the
// visible answer. These helpers strip that leading block so every upstream reads
// identically: clean content, no template artifacts. Lives in package model so
// both the relay (controllers) and the QueryText providers share one definition.

const thinkClose = "</think>"

// reasoningCap bounds how much leading content is held while waiting for the
// </think> terminator, so a stream gated as reasoning-inlining but somehow
// lacking the terminator flushes rather than buffering unboundedly.
const reasoningCap = 1 << 16 // 64 KiB

// InlinesReasoning reports whether an upstream model emits chain-of-thought
// inline in content (terminated by </think>) rather than in reasoning_content.
// Gating on it keeps every other upstream's bytes untouched — no buffering, no
// risk of a false strip.
func InlinesReasoning(upstreamModel string) bool {
	return strings.Contains(strings.ToLower(upstreamModel), "deepseek")
}

// StripLeadingReasoning removes a leading `<think>…</think>` (or bare `…</think>`,
// opener consumed by the template) block from a complete content string,
// returning the clean answer. Content with no </think> is returned unchanged —
// a no-op on already-clean (e.g. GLM) output.
func StripLeadingReasoning(content string) string {
	if i := strings.Index(content, thinkClose); i >= 0 {
		return strings.TrimLeft(content[i+len(thinkClose):], " \n")
	}
	return content
}

// ReasoningStripper strips the leading inline-reasoning block from a STREAMED
// content sequence. It holds content deltas until </think> arrives, then emits
// the answer remainder and passes every later delta straight through. Feed it
// only content deltas from a reasoning-inlining upstream (see InlinesReasoning);
// the terminator may split across deltas, which the running buffer handles.
type ReasoningStripper struct {
	done bool
	buf  strings.Builder
}

// Feed returns the content to forward for one upstream content delta: "" while
// still inside the reasoning block, the answer text once </think> is seen (or the
// held buffer if the cap is hit without a terminator), then passthrough.
func (s *ReasoningStripper) Feed(delta string) string {
	if s.done {
		return delta
	}
	s.buf.WriteString(delta)
	b := s.buf.String()
	if i := strings.Index(b, thinkClose); i >= 0 {
		s.done = true
		s.buf.Reset()
		return strings.TrimLeft(b[i+len(thinkClose):], " \n")
	}
	if s.buf.Len() >= reasoningCap {
		s.done = true
		s.buf.Reset()
		return b
	}
	return ""
}
