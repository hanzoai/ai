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
	"strings"
	"testing"
)

// benchPolicy is a realistic task→model table (the shape the gateway folds).
var benchPolicy = Policy{
	Prefer: map[string][]string{
		"code":         {"zen5-coder", "qwen3-coder"},
		"math":         {"zen5", "gpt-4o"},
		"reasoning":    {"zen5-ultra"},
		"creative":     {"zen5"},
		"vision":       {"zen5-vl"},
		"long_context": {"zen5-1m"},
		"cheap_chat":   {"zen5-mini"},
		"default":      {"zen5", "gpt-4o"},
	},
}

// A representative prompt (~a paragraph) so Classify does real keyword+length work.
var benchText = strings.Repeat(
	"Write and optimize a Go function that reverses a linked list, then explain the "+
		"time complexity step by step and prove why it is optimal. ", 3,
)

// BenchmarkClassify measures ONLY the task classifier — the keyword/length/media
// pass Classify runs on every auto request.
func BenchmarkClassify(b *testing.B) {
	req := Request{Text: benchText, ApproxTokens: 120}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Classify(req)
	}
}

// BenchmarkRouteDecisionHeuristic measures the WHOLE heuristic routing decision
// the gateway adds to an `auto` request when no engine endpoint is configured
// (the default prod path): Classify + Policy.ForTask with a servability predicate.
// This is the honest "how much latency does routing add" number — it is pure Go,
// no network, no allocation of the request path.
func BenchmarkRouteDecisionHeuristic(b *testing.B) {
	c := Client{Policy: benchPolicy, Known: func(string) bool { return true }}
	req := Request{Text: benchText, ApproxTokens: 120}
	ctx := context.Background()
	var slo Slo
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d := c.RouteDecision(ctx, req, slo)
		if d.Model == "" {
			b.Fatal("empty decision")
		}
	}
}

// BenchmarkRouteDecisionColdKnown measures the decision with a servability
// predicate that misses on the first pick (forcing the second-choice fallback),
// the worst case of the heuristic path.
func BenchmarkRouteDecisionColdKnown(b *testing.B) {
	// Known admits only the SECOND model per task, so ForTask walks the list.
	c := Client{Policy: benchPolicy, Known: func(m string) bool {
		return m == "qwen3-coder" || m == "gpt-4o"
	}}
	req := Request{Text: benchText, ApproxTokens: 120}
	ctx := context.Background()
	var slo Slo
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.RouteDecision(ctx, req, slo)
	}
}
