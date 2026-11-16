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
// Zen SKUs front models that expose chain-of-thought two different ways. Some
// return it in the separate reasoning_content field; others INLINE it at the head
// of content as `<think>reasoning</think>answer`. Presented through one
// Anthropic/OpenAI-shaped surface with no normalization, the inlined block lands
// in the visible answer.
//
// These helpers strip a block only when content STARTS with <think> (after any
// whitespace). Content that starts otherwise is an answer, and a </think> inside
// it is text. Which SKU behaves which way is a property of
// the catalog (HIP-0039), read at run time — never a list here. Lives in package
// model so both the relay (controllers) and the QueryText providers share one
// definition.

const (
	thinkOpen  = "<think>"
	thinkClose = "</think>"
)

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

// StripLeadingReasoning removes a leading `<think>…</think>` block from a complete
// content string, returning the answer. Content that does not start with <think>,
// or whose block never closes, is returned unchanged.
func StripLeadingReasoning(content string) string {
	rest := strings.TrimLeft(content, " \t\r\n")
	if !strings.HasPrefix(rest, thinkOpen) {
		return content
	}
	if _, after, ok := strings.Cut(rest[len(thinkOpen):], thinkClose); ok {
		return strings.TrimLeft(after, " \n")
	}
	return content
}

// ReasoningStripper strips the leading inline-reasoning block from a STREAMED
// content sequence. It holds content only while it could still be that block —
// whitespace, a prefix of <think>, or inside <think> awaiting </think> — and
// passes everything else straight through. Feed it content deltas of a
// reasoning-inlining upstream (see InlinesReasoning); Flush at the end of the
// stream returns whatever is still held.
type ReasoningStripper struct {
	state int // 0 deciding, 1 inside the block, 2 pass through
	buf   strings.Builder
}

const (
	stripDeciding = iota
	stripInside
	stripPass
	stripTrim // just past the block: blank lines before the answer are dropped
)

// Feed returns the content to forward for one upstream content delta.
func (s *ReasoningStripper) Feed(delta string) string {
	switch s.state {
	case stripPass:
		return delta
	case stripTrim:
		if delta = strings.TrimLeft(delta, " \n"); delta != "" {
			s.state = stripPass
		}
		return delta
	case stripDeciding:
		s.buf.WriteString(delta)
		b := s.buf.String()
		rest := strings.TrimLeft(b, " \t\r\n")
		switch {
		case rest == "" || (len(rest) < len(thinkOpen) && strings.HasPrefix(thinkOpen, rest)):
			return ""
		case strings.HasPrefix(rest, thinkOpen):
			s.state = stripInside
			s.buf.Reset()
			s.buf.WriteString(rest[len(thinkOpen):])
		default:
			s.state = stripPass
			s.buf.Reset()
			return b
		}
	default:
		s.buf.WriteString(delta)
	}
	b := s.buf.String()
	if _, after, ok := strings.Cut(b, thinkClose); ok {
		s.state = stripTrim
		s.buf.Reset()
		if after = strings.TrimLeft(after, " \n"); after != "" {
			s.state = stripPass
		}
		return after
	}
	if s.buf.Len() >= reasoningCap {
		s.state = stripPass
		s.buf.Reset()
		return b
	}
	return ""
}

// Flush ends the stream: it returns what is still held and passes everything after.
func (s *ReasoningStripper) Flush() string {
	held := s.buf.String()
	s.buf.Reset()
	s.state = stripPass
	return held
}

// Reasoned notes that the upstream sent its reasoning in reasoning_content, so
// content is the answer and is not read for a block. It returns what was held
// while deciding (whitespace, a partial opener), to go out ahead of the delta in
// hand; a block already open is still stripped.
func (s *ReasoningStripper) Reasoned() string {
	if s.state != stripDeciding {
		return ""
	}
	return s.Flush()
}
