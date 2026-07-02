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
	"strings"
	"testing"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

// The trace org is the SERVER-VERIFIED principal's org (Organization, then Owner)
// — never a client value. This is the cross-tenant defense at the emission seam.
func TestResolveTraceOrg(t *testing.T) {
	if got := resolveTraceOrg(&usageRecord{Organization: "acme", Owner: "acme"}); got != "acme" {
		t.Errorf("org = %q, want acme", got)
	}
	// Falls back to Owner when Organization is unset (both are server-derived).
	if got := resolveTraceOrg(&usageRecord{Owner: "acme"}); got != "acme" {
		t.Errorf("org = %q, want acme (from Owner)", got)
	}
	// No verified principal → empty → EmitTrace drops it (no unscoped row).
	if got := resolveTraceOrg(&usageRecord{}); got != "" {
		t.Errorf("org = %q, want empty (fail-secure drop)", got)
	}
}

// The trace metadata is an allowlist: it must NEVER carry the raw client IP (PII)
// or any credential. Red will grep persisted traces for the caller IP.
func TestTraceMetadataExcludesPII(t *testing.T) {
	rec := &usageRecord{
		Provider:  "openai",
		RequestID: "req-1",
		ClientIP:  "203.0.113.7:44321",
		Premium:   true,
		Stream:    true,
	}
	md := traceMetadata(rec)
	for k, v := range md {
		if strings.Contains(v, "203.0.113.7") {
			t.Errorf("metadata[%q]=%q leaked the client IP", k, v)
		}
	}
	if _, ok := md["clientIp"]; ok {
		t.Errorf("metadata must not carry a clientIp key")
	}
	if md["provider"] != "openai" || md["premium"] != "true" || md["stream"] != "true" {
		t.Errorf("metadata missing expected allowlisted fields: %v", md)
	}
}

func TestTraceTags(t *testing.T) {
	rec := &usageRecord{Model: "zen-coder", Provider: "do-ai", Premium: true}
	tags := traceTags(rec, "success")
	want := map[string]bool{"zen-coder": true, "do-ai": true, "success": true, "premium": true}
	for _, tag := range tags {
		if !want[tag] {
			t.Errorf("unexpected tag %q", tag)
		}
		delete(want, tag)
	}
	if len(want) != 0 {
		t.Errorf("missing tags: %v", want)
	}
}

func TestMessagesToText(t *testing.T) {
	msgs := []openai.ChatCompletionMessage{
		{Role: "system", Content: "be brief"},
		{Role: "user", Content: "hi"},
	}
	got := messagesToText(msgs)
	if !strings.Contains(got, "system: be brief") || !strings.Contains(got, "user: hi") {
		t.Errorf("messagesToText = %q", got)
	}
}

func TestChatParamsOmitsZero(t *testing.T) {
	p := chatParams(&openai.ChatCompletionRequest{Temperature: 0.7})
	if p["temperature"] != float32(0.7) {
		t.Errorf("temperature = %v, want 0.7", p["temperature"])
	}
	if _, ok := p["top_p"]; ok {
		t.Errorf("top_p must be omitted when zero")
	}
}

// emitAIObservability must be safe (non-blocking, no panic) even with a fully
// empty record — the pipeline is not running in unit tests, so this exercises the
// build-and-drop path without a datastore.
func TestEmitAIObservabilitySafeWhenDisabled(t *testing.T) {
	done := make(chan struct{})
	go func() {
		emitAIObservability(&usageRecord{
			Organization: "acme", User: "acme/al", Model: "gpt-4o", Provider: "openai",
			PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3, Status: "success",
			traceInput: "hi", traceOutput: "hello", traceEnd: time.Now().UTC(),
		}, time.Now().Add(-time.Second))
		emitAIObservability(nil, time.Now()) // nil is a no-op, never panics
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("emitAIObservability blocked or hung")
	}
}
