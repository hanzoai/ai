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

package controllers

import (
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

func attrMap(attrs []attribute.KeyValue) map[string]attribute.Value {
	m := make(map[string]attribute.Value, len(attrs))
	for _, a := range attrs {
		m[string(a.Key)] = a.Value
	}
	return m
}

// TestBuildGenAISpanFields_ChatSuccess pins the OTel GenAI contract with o11y:
// span name and every gen_ai.*/attribution/cost/token attribute for a successful
// token-billed chat call, including the cache tokens, per-component cost detail,
// session grouping, and served-by/route attribution.
func TestBuildGenAISpanFields_ChatSuccess(t *testing.T) {
	rec := &usageRecord{
		Owner: "acme", Organization: "acme", User: "acme/alice",
		Model: "zen5", Provider: "digitalocean",
		PromptTokens: 120, CompletionTokens: 45, TotalTokens: 165,
		CacheReadTokens: 30, CacheWriteTokens: 10,
		Status: "success", RequestID: "req-abc",
		Session: "conv-42", ServedBy: "byo-gpu", ClusterID: "cl-1", RoutePolicy: "enso:cheap",
	}
	br := &costBreakdown{Input: 0.0012, Output: 0.0009, CacheRead: 0.00003, CacheWrite: 0.0001, Total: 0.00223}

	f := buildGenAISpanFields(rec, 0.0022, br, false)

	if f.name != "chat zen5" {
		t.Fatalf("span name = %q, want %q", f.name, "chat zen5")
	}
	if f.statusCode != codes.Ok {
		t.Errorf("status = %v, want Ok", f.statusCode)
	}

	m := attrMap(f.attrs)
	wantStr := map[string]string{
		"gen_ai.system":            "hanzo",
		"gen_ai.operation.name":    "chat",
		"gen_ai.request.model":     "zen5",
		"gen_ai.response.model":    "zen5",
		"user.id":                  "acme/alice",
		"gen_ai.hanzo.org_id":      "acme",
		"gen_ai.hanzo.provider":    "digitalocean",
		"gen_ai.hanzo.request_id":  "req-abc",
		"session.id":               "conv-42",
		"gen_ai.conversation.id":   "conv-42",
		"gen_ai.hanzo.served_by":   "byo-gpu",
		"gen_ai.hanzo.cluster_id":  "cl-1",
		"gen_ai.hanzo.route_policy": "enso:cheap",
	}
	for k, want := range wantStr {
		got, ok := m[k]
		if !ok {
			t.Errorf("missing attribute %q", k)
			continue
		}
		if got.AsString() != want {
			t.Errorf("attr %q = %q, want %q", k, got.AsString(), want)
		}
	}
	wantInt := map[string]int64{
		"gen_ai.usage.input_tokens":                  120,
		"gen_ai.usage.output_tokens":                 45,
		"gen_ai.usage.total_tokens":                  165,
		"gen_ai.usage.cache_read.input_tokens":       30,
		"gen_ai.usage.cache_creation.input_tokens":   10,
	}
	for k, want := range wantInt {
		if got := m[k].AsInt64(); got != want {
			t.Errorf("attr %q = %d, want %d", k, got, want)
		}
	}
	wantFloat := map[string]float64{
		"_o11y.gen_ai.total_cost":       0.0022,
		"_o11y.gen_ai.cost_input":       0.0012,
		"_o11y.gen_ai.cost_output":      0.0009,
		"_o11y.gen_ai.cost_cache_read":  0.00003,
		"_o11y.gen_ai.cost_cache_write": 0.0001,
	}
	for k, want := range wantFloat {
		got, ok := m[k]
		if !ok {
			t.Errorf("missing cost attribute %q", k)
			continue
		}
		if got.AsFloat64() != want {
			t.Errorf("attr %q = %v, want %v", k, got.AsFloat64(), want)
		}
	}
}

// TestBuildGenAISpanFields_ErrorAndFallbacks covers the error status, the
// org=Owner fallback when Organization is empty, the "unknown" model fallback, the
// served_by default (hanzo), and omission of every empty optional attribute
// (provider, request_id, session, cluster, route, cache tokens, cost breakdown).
func TestBuildGenAISpanFields_ErrorAndFallbacks(t *testing.T) {
	rec := &usageRecord{
		Owner: "acme", User: "acme/bob",
		Model: "", Status: "error", ErrorMsg: "upstream 500",
	}

	f := buildGenAISpanFields(rec, 0, nil, false)

	if f.name != "chat unknown" {
		t.Fatalf("span name = %q, want %q", f.name, "chat unknown")
	}
	if f.statusCode != codes.Error || f.statusMsg != "upstream 500" {
		t.Errorf("status = %v %q, want Error \"upstream 500\"", f.statusCode, f.statusMsg)
	}

	m := attrMap(f.attrs)
	if got := m["gen_ai.hanzo.org_id"].AsString(); got != "acme" {
		t.Errorf("gen_ai.hanzo.org_id = %q, want \"acme\" (Owner fallback)", got)
	}
	// served_by is always present, defaulting to hanzo cloud.
	if got := m["gen_ai.hanzo.served_by"].AsString(); got != "hanzo" {
		t.Errorf("gen_ai.hanzo.served_by = %q, want \"hanzo\" (default)", got)
	}
	// total_cost is always present (the column must exist), 0 here.
	if got, ok := m["_o11y.gen_ai.total_cost"]; !ok || got.AsFloat64() != 0 {
		t.Errorf("_o11y.gen_ai.total_cost = %v (present=%v), want 0 present", got.AsFloat64(), ok)
	}
	for _, k := range []string{
		"gen_ai.hanzo.provider", "gen_ai.hanzo.request_id",
		"session.id", "gen_ai.conversation.id",
		"gen_ai.hanzo.cluster_id", "gen_ai.hanzo.route_policy",
		"gen_ai.usage.cache_read.input_tokens", "gen_ai.usage.cache_creation.input_tokens",
		"_o11y.gen_ai.cost_input", "_o11y.gen_ai.cost_output",
		"gen_ai.input.messages", "gen_ai.output.messages",
	} {
		if _, ok := m[k]; ok {
			t.Errorf("attribute %q must be absent when unset", k)
		}
	}
}

// TestBuildGenAISpanFields_MessageRedaction proves prompt/completion bodies are
// emitted ONLY when the caller opts in — the privacy gate is fail-secure.
func TestBuildGenAISpanFields_MessageRedaction(t *testing.T) {
	rec := &usageRecord{
		Owner: "acme", Model: "zen5", Status: "success",
		InputMessages: `[{"role":"user","content":"secret"}]`, OutputMessages: `[{"role":"assistant","content":"reply"}]`,
	}

	// Gate off: bodies must NOT appear.
	off := attrMap(buildGenAISpanFields(rec, 0, nil, false).attrs)
	if _, ok := off["gen_ai.input.messages"]; ok {
		t.Error("gen_ai.input.messages leaked with capture disabled")
	}
	if _, ok := off["gen_ai.output.messages"]; ok {
		t.Error("gen_ai.output.messages leaked with capture disabled")
	}

	// Gate on: bodies appear verbatim.
	on := attrMap(buildGenAISpanFields(rec, 0, nil, true).attrs)
	if got := on["gen_ai.input.messages"].AsString(); got != rec.InputMessages {
		t.Errorf("gen_ai.input.messages = %q, want %q", got, rec.InputMessages)
	}
	if got := on["gen_ai.output.messages"].AsString(); got != rec.OutputMessages {
		t.Errorf("gen_ai.output.messages = %q, want %q", got, rec.OutputMessages)
	}
}

// TestGenAICaptureMessages verifies the env gate is fail-secure: only explicit
// truthy values enable message capture.
func TestGenAICaptureMessages(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want bool
	}{
		{"", false}, {"false", false}, {"0", false}, {"no", false}, {"off", false},
		{"true", true}, {"1", true}, {"TRUE", true}, {"Yes", true}, {"on", true},
	} {
		t.Setenv("O11Y_GENAI_CAPTURE_MESSAGES", tc.val)
		if got := genAICaptureMessages(); got != tc.want {
			t.Errorf("genAICaptureMessages(%q) = %v, want %v", tc.val, got, tc.want)
		}
	}
}

// TestUsageCostCents_MatchesSpanTotal proves the ledger cost and the span total are
// the ONE value — usageCostCents drives both recordUsage and emitGenAISpan.
func TestUsageCostCents_MatchesSpanTotal(t *testing.T) {
	rec := &usageRecord{Model: "zen5", PromptTokens: 1000, CompletionTokens: 500}
	cents := usageCostCents(rec)
	spanTotalUSD := float64(cents) / 100.0
	m := attrMap(buildGenAISpanFields(rec, spanTotalUSD, nil, false).attrs)
	if got := m["_o11y.gen_ai.total_cost"].AsFloat64(); got != spanTotalUSD {
		t.Errorf("span total_cost = %v, want %v (== usageCostCents/100)", got, spanTotalUSD)
	}
}
