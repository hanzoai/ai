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
		// Exact shapes observed live from the DeepSeek upstreams.
		{"zen5-flash bare close", "</think>I am **Zen5 Flash** by **Hanzo AI**.", "I am **Zen5 Flash** by **Hanzo AI**."},
		{"zen5-pro reasoning then close", " Zebra stripe.</think>I am Zen5 Pro, a high-capability model.", "I am Zen5 Pro, a high-capability model."},
		{"matched think pair", "<think>let me reason…</think>The answer is 42.", "The answer is 42."},
		{"leading whitespace after close", "reasoning</think>\n\n  Answer here.", "Answer here."},
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

func TestReasoningStripperStreaming(t *testing.T) {
	t.Run("reasoning then answer in one delta", func(t *testing.T) {
		s := &ReasoningStripper{}
		if got := feedAll(s, "thinking…</think>Hello."); got != "Hello." {
			t.Errorf("got %q, want %q", got, "Hello.")
		}
	})

	t.Run("terminator split across deltas", func(t *testing.T) {
		s := &ReasoningStripper{}
		// </think> arrives as "</th" + "ink>" — the classic chunk boundary.
		got := feedAll(s, "some ", "reasoning", "</th", "ink>", "The ", "answer.")
		if got != "The answer." {
			t.Errorf("got %q, want %q", got, "The answer.")
		}
	})

	t.Run("answer streams through after close", func(t *testing.T) {
		s := &ReasoningStripper{}
		got := feedAll(s, "r</think>", "Hel", "lo ", "world")
		if got != "Hello world" {
			t.Errorf("got %q, want %q", got, "Hello world")
		}
	})

	t.Run("no terminator under cap holds (upstream should be gated out)", func(t *testing.T) {
		s := &ReasoningStripper{}
		if got := feedAll(s, "just an answer"); got != "" {
			t.Errorf("got %q, want empty (held)", got)
		}
	})

	t.Run("cap flushes rather than buffering unboundedly", func(t *testing.T) {
		s := &ReasoningStripper{}
		big := strings.Repeat("x", reasoningCap+10)
		if got := s.Feed(big); got != big {
			t.Errorf("cap: want the held buffer flushed, got %d bytes", len(got))
		}
		if got := s.Feed("!"); got != "!" {
			t.Errorf("post-cap passthrough: got %q", got)
		}
	})
}
