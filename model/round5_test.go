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

import (
	"strings"
	"testing"
	"time"
)

func TestRound5CutReasoningIsDropped(t *testing.T) {
	for name, c := range map[string]struct {
		in    string
		eaten bool
	}{
		"eaten":  {"The user wants X. secret plan", true},
		"opener": {"<think>secret plan", false},
	} {
		if got := StripReasoning(c.in, c.eaten, true); got != "" {
			t.Errorf("%s buffered cut: %q, want nothing", name, got)
		}
		s := NewReasoningStripper("deepseek-x", c.eaten)
		out := s.Feed(c.in) + s.Finish("length")
		if out != "" {
			t.Errorf("%s stream cut: %q, want nothing", name, out)
		}
		s = NewReasoningStripper("deepseek-x", c.eaten)
		if out := s.Feed(c.in) + s.Flush(); out != "" {
			t.Errorf("%s stream with no finish: %q, want nothing", name, out)
		}
	}
	s := NewReasoningStripper("deepseek-x", true)
	if out := s.Feed("just an answer") + s.Finish("stop"); out != "just an answer" {
		t.Errorf("finished with no terminator: %q, want the whole", out)
	}
	if got := StripReasoning("just an answer", true, false); got != "just an answer" {
		t.Errorf("buffered finished with no terminator: %q", got)
	}
	// A partial opener held at a cut is text, not reasoning.
	s = NewReasoningStripper("deepseek-x", false)
	if out := s.Feed("<thi") + s.Flush(); out != "<thi" {
		t.Errorf("held partial opener at a cut: %q", out)
	}
}

func TestRound5FlaggedStripperIsLinear(t *testing.T) {
	s := NewReasoningStripper("deepseek-x", true)
	start := time.Now()
	for s.state != stripPass {
		s.Feed("abc ")
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Errorf("1 MiB in 4-byte deltas took %v", d)
	}
	// The terminator straddling deltas is still found.
	s = NewReasoningStripper("deepseek-x", true)
	var out strings.Builder
	for _, p := range []string{"think", "ing</", "thi", "nk", ">ans", "wer"} {
		out.WriteString(s.Feed(p))
	}
	if out.String() != "answer" {
		t.Errorf("split terminator: %q", out.String())
	}
}
