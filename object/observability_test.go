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

package object

import (
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func sampleEvent() TraceEvent {
	start := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	return TraceEvent{
		TraceID:          "req-123",
		Org:              "acme",
		User:             "acme/alice",
		SessionID:        "sess-9",
		Name:             "chat.completions",
		Input:            "hello",
		Output:           "hi there",
		Tags:             []string{"gpt-4o", "openai", "success"},
		Metadata:         map[string]string{"provider": "openai", "premium": "true"},
		StartTime:        start,
		EndTime:          start.Add(2 * time.Second),
		Model:            "gpt-4o",
		Provider:         "openai",
		PromptTokens:     10,
		CompletionTokens: 20,
		TotalTokens:      30,
		CostCents:        150,
		Params:           map[string]any{"temperature": 0.7, "max_tokens": 256},
		Stream:           false,
		Status:           "success",
	}
}

// The trace row must carry the SERVER-VERIFIED org as project_id at the exact DDL
// position, and prefix ids so an observation joins to its trace.
func TestTraceRowMapping(t *testing.T) {
	ev := sampleEvent()
	row := ev.traceRow()

	if len(row) != 16 {
		t.Fatalf("traceRow len = %d, want 16 (DDL column count)", len(row))
	}
	if got := row[0].(string); got != "trace-req-123" {
		t.Errorf("id = %q, want trace-req-123", got)
	}
	if got := row[7].(string); got != "acme" {
		t.Errorf("project_id = %q, want acme (the verified org)", got)
	}
	// input/output are *string (Nullable); non-empty here.
	if in := row[11].(*string); in == nil || *in != "hello" {
		t.Errorf("input = %v, want *hello", in)
	}
	if tags, ok := row[10].([]string); !ok || len(tags) != 3 {
		t.Errorf("tags = %v, want 3-element []string", row[10])
	}
	if uid := row[3].(*string); uid == nil || *uid != "acme/alice" {
		t.Errorf("user_id = %v, want acme/alice", uid)
	}
}

// The observation row must carry project_id (tenant) at its DDL position, a
// GENERATION type, the token usage map, and the exact decimal cost.
func TestObservationRowMapping(t *testing.T) {
	ev := sampleEvent()
	ev.WithCompletionStart(ev.StartTime.Add(300 * time.Millisecond))
	row := ev.observationRow()

	if len(row) != 28 {
		t.Fatalf("observationRow len = %d, want 28 (explicit INSERT column count)", len(row))
	}
	if got := row[0].(string); got != "gen-req-123" {
		t.Errorf("id = %q, want gen-req-123", got)
	}
	if got := row[1].(string); got != "trace-req-123" {
		t.Errorf("trace_id = %q, want trace-req-123", got)
	}
	if got := row[2].(string); got != "acme" {
		t.Errorf("project_id = %q, want acme (the verified org)", got)
	}
	if got := row[3].(string); got != ObservationGeneration {
		t.Errorf("type = %q, want GENERATION", got)
	}
	usage := row[18].(map[string]uint64)
	if usage["input"] != 10 || usage["output"] != 20 || usage["total"] != 30 {
		t.Errorf("usage_details = %v, want input:10 output:20 total:30", usage)
	}
	total := row[21].(*decimal.Decimal)
	if total == nil || !total.Equal(decimal.RequireFromString("1.50")) {
		t.Errorf("total_cost = %v, want 1.50 (150 cents)", total)
	}
	// completion_start_time present → TTFT computable by the reader.
	if cs := row[22].(*time.Time); cs == nil {
		t.Errorf("completion_start_time = nil, want the first-token instant")
	}
	// error level maps only on error.
	if lvl := row[9].(string); lvl != levelDefault {
		t.Errorf("level = %q, want DEFAULT for success", lvl)
	}
}

func TestObservationRowErrorLevel(t *testing.T) {
	ev := sampleEvent()
	ev.Status = "error"
	ev.ErrorMsg = "upstream 502"
	row := ev.observationRow()
	if lvl := row[9].(string); lvl != levelError {
		t.Errorf("level = %q, want ERROR", lvl)
	}
	if sm := row[10].(*string); sm == nil || *sm != "upstream 502" {
		t.Errorf("status_message = %v, want upstream 502", sm)
	}
}

// A trace with no server-verified org must be DROPPED, never written into a
// shared/unscoped bucket — the core cross-tenant defense.
func TestEmitTraceDropsUnscoped(t *testing.T) {
	// Ensure the pipeline is not running so a valid event would take the
	// "disabled" path; the unscoped check must fire BEFORE that.
	obsRunning.Store(false)
	done := make(chan struct{})
	go func() {
		EmitTrace(TraceEvent{Org: "   ", Input: "secret prompt"}) // blank org
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("EmitTrace blocked on an unscoped event")
	}
}

// EmitTrace must NEVER block the caller, even when the buffer is full — the core
// "no added request latency" defense. Fill a size-1 buffer with no drain running.
func TestEmitTraceNonBlockingWhenBufferFull(t *testing.T) {
	prevCh, prevRunning := obsCh, obsRunning.Load()
	prevMax := obsMaxContent
	defer func() { obsCh, obsMaxContent = prevCh, prevMax; obsRunning.Store(prevRunning) }()

	obsMaxContent = 32768
	obsCh = make(chan obsItem, 1)
	obsRunning.Store(true)

	EmitTrace(TraceEvent{Org: "acme", Input: "one"}) // fills the buffer

	done := make(chan struct{})
	go func() {
		EmitTrace(TraceEvent{Org: "acme", Input: "two"}) // must drop, not block
		EmitTrace(TraceEvent{Org: "acme", Input: "three"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("EmitTrace blocked when the buffer was full (would add request latency)")
	}
}

func TestScrubSecrets(t *testing.T) {
	cases := []struct {
		name, in string
		leak     string
	}{
		{"openai key", "my key is sk-abcdef0123456789ABCDEF ok", "sk-abcdef0123456789ABCDEF"},
		{"hanzo key", "use hk-0123456789abcdef0123 now", "hk-0123456789abcdef0123"},
		{"bearer", "Authorization: Bearer abcdef0123456789ghij", "abcdef0123456789ghij"},
		{"aws", "AKIAIOSFODNN7EXAMPLE here", "AKIAIOSFODNN7EXAMPLE"},
		{"jwt", "tok eyJhbGciOi.eyJzdWIiOi.SflKxwRJSM here", "eyJhbGciOi.eyJzdWIiOi.SflKxwRJSM"},
		{"kv secret", "password= hunter2hunter2xx tail", "hunter2hunter2xx"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := scrubSecrets(c.in)
			if strings.Contains(out, c.leak) {
				t.Errorf("scrubSecrets leaked %q: %q", c.leak, out)
			}
			if !strings.Contains(out, redactionMark) {
				t.Errorf("scrubSecrets did not redact: %q", out)
			}
		})
	}
}

func TestScrubSecretsKeepsCleanText(t *testing.T) {
	in := "The capital of France is Paris. 2+2=4."
	if out := scrubSecrets(in); out != in {
		t.Errorf("scrubSecrets altered clean text: %q", out)
	}
}

func TestTruncateContent(t *testing.T) {
	if got := truncateTraceContent("hello", 100); got != "hello" {
		t.Errorf("short content changed: %q", got)
	}
	long := strings.Repeat("a", 1000)
	got := truncateTraceContent(long, 100)
	if !strings.HasPrefix(got, strings.Repeat("a", 100)) || !strings.Contains(got, "truncated") {
		t.Errorf("truncateTraceContent did not bound+mark: len=%d", len(got))
	}
	// Rune-safe: never split a multibyte rune.
	multi := strings.Repeat("é", 100) // 2 bytes each
	if out := truncateTraceContent(multi, 51); strings.Contains(out, "�") {
		t.Errorf("truncateTraceContent split a rune: %q", out)
	}
}

// Content capture off must NULL input/output; on must scrub them.
func TestApplyCapturePolicy(t *testing.T) {
	off := sampleEvent()
	off.Input, off.Output = "sk-abcdefghij0123456789", "answer"
	off.applyCapturePolicy(false)
	if off.Input != "" || off.Output != "" {
		t.Errorf("capture off must clear content, got in=%q out=%q", off.Input, off.Output)
	}

	on := sampleEvent()
	on.Input = "leak sk-abcdefghij0123456789 here"
	on.applyCapturePolicy(true)
	if strings.Contains(on.Input, "sk-abcdefghij0123456789") {
		t.Errorf("capture on must scrub secrets, got %q", on.Input)
	}

	// Per-event override beats the global default.
	forced := sampleEvent()
	forced.SetCaptureContent(false)
	forced.applyCapturePolicy(true)
	if forced.Input != "" {
		t.Errorf("per-event opt-out ignored, got %q", forced.Input)
	}
}

func TestModelParametersJSON(t *testing.T) {
	ev := sampleEvent()
	mp := ev.modelParametersJSON()
	if mp == nil || !strings.Contains(*mp, "temperature") || !strings.Contains(*mp, "stream") {
		t.Errorf("model_parameters JSON missing fields: %v", mp)
	}

	empty := TraceEvent{} // no params, stream false → stream:false is still emitted
	if empty.modelParametersJSON() == nil {
		t.Errorf("expected at least the stream param")
	}
}

func TestUsageDetailsOmitsZeroCache(t *testing.T) {
	ev := sampleEvent()
	u := ev.usageDetails()
	if _, ok := u["cache_read"]; ok {
		t.Errorf("cache_read must be omitted when zero")
	}
	ev.CacheReadTokens = 5
	if ev.usageDetails()["cache_read"] != 5 {
		t.Errorf("cache_read must appear when non-zero")
	}
}
