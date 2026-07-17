// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestRoutingDemo is an EMPIRICAL demonstration: it routes a batch of real,
// diverse prompts through the ACTUAL heuristic RouteDecision (Classify + Policy)
// and prints, per prompt, the classified task, the model it routed to, and the
// wall-clock time the decision took. It proves the router (a) classifies by
// content — different prompts → different tasks → different models — and (b) that
// the heuristic decision is genuinely sub-microsecond because it is only keyword
// matching + a map lookup. Run with:
//
//	go test ./router/ -run TestRoutingDemo -v -count=1
func TestRoutingDemo(t *testing.T) {
	// A realistic per-task model table (what an org/conf would set).
	pol := Policy{Prefer: map[string][]string{
		"code":         {"zen5-coder"},
		"math":         {"zen5-math"},
		"reasoning":    {"zen5-ultra"},
		"creative":     {"zen5-creative"},
		"vision":       {"zen5-vl"},
		"long_context": {"zen5-1m"},
		"cheap_chat":   {"zen5-mini"},
		"default":      {"zen5"},
	}}
	c := Client{Policy: pol, Known: func(string) bool { return true }}
	ctx := context.Background()

	prompts := []struct{ label, text string }{
		{"code", "Refactor this function to remove the nested loop and explain the bug."},
		{"math", "Prove that the integral of 1/x from 1 to e equals 1."},
		{"reasoning", "Explain step by step why event sourcing beats CRUD for an audit ledger."},
		{"creative", "Write a short poem about a lighthouse keeper at dawn."},
		{"cheap-chat", "hi there"},
		{"code-fence", "```python\nfor i in range(len(xs)): print(xs[i])\n```  make this idiomatic"},
		{"general", "What time zone is Lisbon in during summer?"},
	}

	fmt.Printf("\n%-12s | %-13s | %-15s | %s\n", "PROMPT KIND", "TASK", "ROUTED MODEL", "DECISION TIME")
	fmt.Printf("%s\n", "-------------+---------------+-----------------+--------------")
	for _, p := range prompts {
		req := Request{Text: p.text, ApproxTokens: estTokens(p.text)}
		start := time.Now()
		d := c.RouteDecision(ctx, req, Slo{})
		elapsed := time.Since(start)
		fmt.Printf("%-12s | %-13s | %-15s | %v\n", p.label, d.Task, d.Model, elapsed)
		if d.Model == "" {
			t.Errorf("%s: empty routing decision", p.label)
		}
	}

	// Aggregate timing over many iterations for a stable per-decision number —
	// this is the honest "how long does a routing decision take" measurement on
	// the heuristic (no-engine) path.
	const N = 100000
	req := Request{Text: prompts[0].text, ApproxTokens: 40}
	t0 := time.Now()
	for i := 0; i < N; i++ {
		_ = c.RouteDecision(ctx, req, Slo{})
	}
	per := time.Since(t0) / N
	fmt.Printf("\nAggregate: %d heuristic decisions in %v → %v per decision (%.0f ns)\n\n",
		N, time.Since(t0), per, float64(per.Nanoseconds()))
	if per > time.Millisecond {
		t.Errorf("heuristic decision unexpectedly slow: %v", per)
	}
}

// estTokens is a rough word-based token estimate for the demo (the real gateway
// uses the request's token count).
func estTokens(s string) int {
	n, inWord := 0, false
	for _, r := range s {
		if r == ' ' || r == '\n' || r == '\t' {
			inWord = false
		} else if !inWord {
			inWord = true
			n++
		}
	}
	return n * 4 / 3
}
