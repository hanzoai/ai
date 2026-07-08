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

package router

import "testing"

// TestClassify mirrors the cases exercised by hanzo-router's Rust Heuristic so
// the Go port stays behaviorally identical to engine/hanzo-router/src/classify.rs.
func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		req  Request
		want Task
	}{
		{"task hint wins", Request{Text: "write a poem", TaskHint: TaskMath}, TaskMath},
		{"media implies vision", Request{Text: "what is this", HasMedia: true}, TaskVision},
		{"long context by tokens", Request{Text: "summarize", ApproxTokens: 40000}, TaskLongContext},
		{"code fence", Request{Text: "```go\nfmt.Println()\n```", ApproxTokens: 50}, TaskCode},
		{"code keyword", Request{Text: "please refactor this function", ApproxTokens: 50}, TaskCode},
		{"code bug keyword", Request{Text: "there is a bug in the compile step", ApproxTokens: 50}, TaskCode},
		{"math keyword", Request{Text: "prove this theorem about the integral", ApproxTokens: 50}, TaskMath},
		{"math solve for", Request{Text: "solve for x in this equation", ApproxTokens: 50}, TaskMath},
		{"reasoning keyword", Request{Text: "explain how this works step by step", ApproxTokens: 50}, TaskReasoning},
		{"reasoning why", Request{Text: "why does the trade-off matter", ApproxTokens: 50}, TaskReasoning},
		{"creative keyword", Request{Text: "write a story about a dragon", ApproxTokens: 50}, TaskCreative},
		{"creative brainstorm", Request{Text: "brainstorm some names", ApproxTokens: 50}, TaskCreative},
		{"cheap chat short", Request{Text: "hi", ApproxTokens: 2}, TaskCheapChat},
		{"general fallback", Request{Text: "tell me about the weather today please", ApproxTokens: 100}, TaskGeneral},
		// Ordering: code is checked before math, so a message with both
		// classifies as code (matches the Rust switch order).
		{"code before math", Request{Text: "there is a bug, prove the theorem", ApproxTokens: 50}, TaskCode},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.req); got != tc.want {
				t.Errorf("Classify(%q) = %q, want %q", tc.req.Text, got, tc.want)
			}
		})
	}
}

func TestPolicyForTask(t *testing.T) {
	p := Policy{Prefer: map[string][]string{
		"code":    {"zen4-coder", "gpt-5.3-codex"},
		"default": {"zen4", "gpt-4o"},
	}}

	if got := p.ForTask(TaskCode, nil); got != "zen4-coder" {
		t.Errorf("ForTask(code, nil) = %q, want zen4-coder", got)
	}
	// Unknown task falls to the default list.
	if got := p.ForTask(TaskCreative, nil); got != "zen4" {
		t.Errorf("ForTask(creative, nil) = %q, want zen4 (default)", got)
	}
	// known predicate skips the first (unservable) code model.
	known := func(id string) bool { return id != "zen4-coder" }
	if got := p.ForTask(TaskCode, known); got != "gpt-5.3-codex" {
		t.Errorf("ForTask(code, known) = %q, want gpt-5.3-codex", got)
	}
	// Empty policy yields no model.
	if got := (Policy{}).ForTask(TaskCode, nil); got != "" {
		t.Errorf("empty policy ForTask = %q, want empty", got)
	}
}
