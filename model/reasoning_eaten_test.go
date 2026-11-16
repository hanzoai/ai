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

package model

import "testing"

// On a route whose template eats the opener, reasoning runs to the first </think>.
func TestEatenStripperEverySplit(t *testing.T) {
	for name, c := range map[string]struct{ in, want string }{
		"no opener":    {"The user wants X. I should be brief.</think>\n\nHello!", "Hello!"},
		"with opener":  {"<think>The user wants X. I should be brief.</think>\n\nHello!", "Hello!"},
		"empty block":  {"</think>Hello!", "Hello!"},
		"later close":  {"a</think>b</think>c", "b</think>c"},
		"no close":     {"Hello, a whole answer.", "Hello, a whole answer."},
		"opener alone": {"<think>never closed", "<think>never closed"},
		"blank lead":   {"\n\nHello", "\n\nHello"},
	} {
		for cut := 0; cut <= len(c.in); cut++ {
			s := NewReasoningStripper("anything", true)
			if got := feedAll(s, c.in[:cut], c.in[cut:]) + s.Flush(); got != c.want {
				t.Errorf("%s split at %d: %q, want %q", name, cut, got, c.want)
			}
		}
		for n := 1; n <= 3; n++ {
			s := NewReasoningStripper("anything", true)
			var deltas []string
			for i := 0; i < len(c.in); i += n {
				deltas = append(deltas, c.in[i:min(i+n, len(c.in))])
			}
			if got := feedAll(s, deltas...) + s.Flush(); got != c.want {
				t.Errorf("%s deltas of %d: %q, want %q", name, n, got, c.want)
			}
		}
		if got := StripReasoning(c.in, true); got != c.want {
			t.Errorf("%s buffered: %q, want %q", name, got, c.want)
		}
	}
}

func TestEatenStripperReasonedContentDisablesIt(t *testing.T) {
	s := NewReasoningStripper("x", true)
	if held := s.Reasoned(); held != "" {
		t.Fatalf("held %q", held)
	}
	if got := s.Feed("a </think> in the answer"); got != "a </think> in the answer" {
		t.Fatalf("got %q", got)
	}
}

func TestStripperGate(t *testing.T) {
	if NewReasoningStripper("glm-5.2", false) != nil {
		t.Error("a route that inlines nothing gets no stripper")
	}
	if NewReasoningStripper("deepseek-r1", false) == nil || NewReasoningStripper("glm-5.2", true) == nil {
		t.Error("an inlining or opener-eating route gets one")
	}
	// strict rule unchanged
	if StripReasoning("Reasoning aloud.</think>The answer.", false) != "Reasoning aloud.</think>The answer." {
		t.Error("a bare close is text off a flagged route")
	}
}
