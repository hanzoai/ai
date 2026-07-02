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
// span name and every gen_ai.*/attribution attribute for a successful chat call.
func TestBuildGenAISpanFields_ChatSuccess(t *testing.T) {
	rec := &usageRecord{
		Owner: "acme", Organization: "acme", User: "acme/alice",
		Model: "zen5", Provider: "digitalocean",
		PromptTokens: 120, CompletionTokens: 45, TotalTokens: 165,
		Status: "success", RequestID: "req-abc",
	}

	f := buildGenAISpanFields(rec)

	if f.name != "chat zen5" {
		t.Fatalf("span name = %q, want %q", f.name, "chat zen5")
	}
	if f.statusCode != codes.Ok {
		t.Errorf("status = %v, want Ok", f.statusCode)
	}

	m := attrMap(f.attrs)
	wantStr := map[string]string{
		"gen_ai.system":           "hanzo",
		"gen_ai.operation.name":   "chat",
		"gen_ai.request.model":    "zen5",
		"gen_ai.response.model":   "zen5",
		"user.id":                 "acme/alice",
		"gen_ai.hanzo.org_id":     "acme",
		"gen_ai.hanzo.provider":   "digitalocean",
		"gen_ai.hanzo.request_id": "req-abc",
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
	if got := m["gen_ai.usage.input_tokens"].AsInt64(); got != 120 {
		t.Errorf("gen_ai.usage.input_tokens = %d, want 120", got)
	}
	if got := m["gen_ai.usage.output_tokens"].AsInt64(); got != 45 {
		t.Errorf("gen_ai.usage.output_tokens = %d, want 45", got)
	}
}

// TestBuildGenAISpanFields_ErrorAndFallbacks covers the error status, the
// org=Owner fallback when Organization is empty, the "unknown" model fallback,
// and omission of empty optional attributes.
func TestBuildGenAISpanFields_ErrorAndFallbacks(t *testing.T) {
	rec := &usageRecord{
		Owner: "acme", User: "acme/bob",
		Model: "", Status: "error", ErrorMsg: "upstream 500",
	}

	f := buildGenAISpanFields(rec)

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
	if _, ok := m["gen_ai.hanzo.provider"]; ok {
		t.Error("gen_ai.hanzo.provider must be absent when Provider is empty")
	}
	if _, ok := m["gen_ai.hanzo.request_id"]; ok {
		t.Error("gen_ai.hanzo.request_id must be absent when RequestID is empty")
	}
}
