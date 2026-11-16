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

import (
	"strings"
	"testing"
)

func TestStripLeadingReasoning(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"matched think pair", "<think>let me reason…</think>The answer is 42.", "The answer is 42."},
		{"leading whitespace before open", " \n<think>r</think>\n\n  Answer here.", "Answer here."},
		{"bare close is text", "I am **Zen5 Flash**.</think>Rest", "I am **Zen5 Flash**.</think>Rest"},
		{"later close is text", "To end a block write </think> like so.", "To end a block write </think> like so."},
		{"unclosed block unchanged", "<think>never closed", "<think>never closed"},
		// Clean output (GLM / non-reasoning) is untouched.
		{"no think — passthrough", "I am Zen5, a frontier model.", "I am Zen5, a frontier model."},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := StripLeadingReasoning(c.in); got != c.want {
				t.Errorf("StripLeadingReasoning(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestInlinesReasoning(t *testing.T) {
	yes := []string{"deepseek-v4-pro", "deepseek-4-flash", "DeepSeek-Reasoner"}
	no := []string{"glm-5.2", "qwen3.5-397b-a17b", "minimax-m2.5", "gpt-4o", ""}
	for _, m := range yes {
		if !InlinesReasoning(m) {
			t.Errorf("InlinesReasoning(%q) = false, want true", m)
		}
	}
	for _, m := range no {
		if InlinesReasoning(m) {
			t.Errorf("InlinesReasoning(%q) = true, want false", m)
		}
	}
}

// feedAll runs a delta sequence through the stripper and returns the forwarded
// content, joined — what the client would actually see.
func feedAll(s *ReasoningStripper, deltas ...string) string {
	var out strings.Builder
	for _, d := range deltas {
		out.WriteString(s.Feed(d))
	}
	return out.String()
}

// every split of in into two deltas, each through a fresh stripper and Flush.
func splitsOf(t *testing.T, in, want string) {
	t.Helper()
	for i := 0; i <= len(in); i++ {
		s := &ReasoningStripper{}
		got := feedAll(s, in[:i], in[i:]) + s.Flush()
		if got != want {
			t.Fatalf("split at %d of %q: got %q, want %q", i, in, got, want)
		}
	}
	s := &ReasoningStripper{}
	var parts []string
	for _, r := range in {
		parts = append(parts, string(r))
	}
	if got := feedAll(s, parts...) + s.Flush(); got != want {
		t.Fatalf("byte-wise %q: got %q, want %q", in, got, want)
	}
}

func TestReasoningStripperStreaming(t *testing.T) {
	t.Run("a genuine block is stripped at every split", func(t *testing.T) {
		splitsOf(t, "<think>thinking…</think>Hello.", "Hello.")
		splitsOf(t, " \n<think>a</think>\n\nHello</think>x", "Hello</think>x")
	})

	t.Run("an answer is delivered whole, with or without a flush", func(t *testing.T) {
		for _, in := range []string{"Hello world", "  ", "<thi", "<think", "<b>bold</b>", "ok</think>no"} {
			splitsOf(t, in, in)
		}
		s := &ReasoningStripper{}
		if got := s.Feed("Hello world"); got != "Hello world" {
			t.Errorf("an answer streams as it comes, got %q", got)
		}
	})

	t.Run("a close later in the text is text", func(t *testing.T) {
		splitsOf(t, "Use </think> to end it.", "Use </think> to end it.")
	})

	t.Run("reasoning_content turns the strip off", func(t *testing.T) {
		s := &ReasoningStripper{}
		held := s.Feed("\n")
		if held != "" {
			t.Fatalf("whitespace is held, got %q", held)
		}
		if got := s.Reasoned(); got != "\n" {
			t.Errorf("Reasoned released %q", got)
		}
		if got := s.Feed("<think>x</think>y"); got != "<think>x</think>y" {
			t.Errorf("after reasoning_content content is the answer, got %q", got)
		}
	})

	t.Run("a block already open survives reasoning_content", func(t *testing.T) {
		s := &ReasoningStripper{}
		s.Feed("<think>a")
		if got := s.Reasoned(); got != "" {
			t.Errorf("got %q", got)
		}
		if got := s.Feed("</think>b"); got != "b" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("an unclosed block is let go at the end", func(t *testing.T) {
		s := &ReasoningStripper{}
		s.Feed("<think>cut off")
		if got := s.Flush(); got != "cut off" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("cap flushes rather than buffering unboundedly", func(t *testing.T) {
		s := &ReasoningStripper{}
		big := "<think>" + strings.Repeat("x", reasoningCap+10)
		if got := s.Feed(big); got != big[len(thinkOpen):] {
			t.Errorf("cap: want the held buffer flushed, got %d bytes", len(got))
		}
		if got := s.Feed("!"); got != "!" {
			t.Errorf("post-cap passthrough: got %q", got)
		}
	})
}
