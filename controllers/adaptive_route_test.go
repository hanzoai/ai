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
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// flashDefaultConfig loads the production Flash-first router.prefer table
// (matching universe charts/app/values/hanzo/cloud.yaml).
func flashDefaultConfig(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models.yaml")
	const doc = `
version: 1
router:
  enabled: true
  depth:
    enso-auto: &enso
      default: enso-flash
      instant: enso-flash
      "off": enso-flash
      none: enso-flash
      minimal: enso-flash
      low: enso-flash
      medium: enso-pro
      high: enso-max
      xhigh: enso-ultra
      max: enso-ultra
  prefer:
    cheap_chat: [enso-flash]
    creative: [enso-flash]
    default: [enso-flash]
    vision: [enso-flash]
    audio: [enso-flash]
    code: [enso-flash]
    long_context: [enso-flash]
    reasoning: [enso-pro]
    math: [enso-pro]
models: {}
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := globalModelConfig
	mc := &ModelConfig{}
	if err := mc.loadFromFile(path); err != nil {
		t.Fatal(err)
	}
	globalModelConfig = mc
	t.Cleanup(func() { globalModelConfig = prev })
}

func TestAdaptiveRouting_ShiftDownOnCleanToolContinuationAndFollowUp(t *testing.T) {
	depthConfig(t) // prefer.code = enso-pro, prefer.long_context = enso-pro
	seedEnso(t)
	noKai(t)
	resetAdaptiveThreads()

	// 1. Clean tool result continuation in a coding thread shifts down to enso-flash
	// even when prefer.code is enso-pro.
	toolBody := `{
		"model": "enso-auto",
		"thread_id": "thr_shift_1",
		"messages": [
			{"role": "user", "content": "Fix the bug in foo.go and run tests"},
			{"role": "assistant", "content": "Reading foo.go"},
			{"role": "tool", "content": "package foo\nfunc Add(a, b int) int { return a + b }\nCommand exited with code 0"}
		]
	}`
	d, ok := RouteDepth(context.Background(), "enso-auto", []byte(toolBody), "acme", "acme/alice", "r1")
	if !ok || d.Tier() != "enso-flash" || d.p.by != "adaptive-shiftdown" {
		t.Fatalf("clean tool continuation -> (%q by %q, %v), want enso-flash by adaptive-shiftdown", d.Tier(), d.p.by, ok)
	}

	// 2. Anthropic tool_result block (clean) also shifts down to enso-flash.
	anthropicToolBody := `{
		"model": "enso-auto",
		"thread_id": "thr_shift_2",
		"messages": [
			{"role": "user", "content": "Refactor this function and compile"},
			{"role": "assistant", "content": "Running build"},
			{"role": "user", "content": [{"type": "tool_result", "content": "ok github.com/acme/pkg 0.42s"}]}
		]
	}`
	d, ok = RouteDepth(context.Background(), "enso-auto", []byte(anthropicToolBody), "acme", "acme/alice", "r2")
	if !ok || d.Tier() != "enso-flash" || d.p.by != "adaptive-shiftdown" {
		t.Fatalf("clean anthropic tool_result -> (%q by %q, %v), want enso-flash by adaptive-shiftdown", d.Tier(), d.p.by, ok)
	}

	// 3. Calm multi-turn follow-up after an initial code turn shifts down to enso-flash.
	followUp := `{
		"model": "enso-auto",
		"thread_id": "thr_shift_3",
		"messages": [
			{"role": "user", "content": "Refactor the billing function to handle zero amounts"},
			{"role": "assistant", "content": "Updated billing.go and added tests."},
			{"role": "user", "content": "looks good, now update the code comment and commit"}
		]
	}`
	d, ok = RouteDepth(context.Background(), "enso-auto", []byte(followUp), "acme", "acme/alice", "r3")
	if !ok || d.Tier() != "enso-flash" || d.p.by != "adaptive-shiftdown" {
		t.Fatalf("calm follow-up -> (%q by %q, %v), want enso-flash by adaptive-shiftdown", d.Tier(), d.p.by, ok)
	}
}

func TestAdaptiveRouting_FrustrationEscalatesProgressivelyAndThenShiftsBackDown(t *testing.T) {
	flashDefaultConfig(t)
	seedEnso(t)
	noKai(t)
	resetAdaptiveThreads()

	// Turn 1: simple coding prompt starts cheap on enso-flash.
	turn1 := `{
		"model": "enso-auto",
		"thread_id": "thr_frust_1",
		"messages": [
			{"role": "user", "content": "Add a helper function to parse dates"}
		]
	}`
	d, _ := RouteDepth(context.Background(), "enso-auto", []byte(turn1), "acme", "acme/alice", "r1")
	if d.Tier() != "enso-flash" {
		t.Fatalf("turn 1 simple code -> %q, want enso-flash", d.Tier())
	}

	// Turn 2: mild pushback ("didn't work") escalates to enso-pro.
	turn2 := `{
		"model": "enso-auto",
		"thread_id": "thr_frust_1",
		"messages": [
			{"role": "user", "content": "Add a helper function to parse dates"},
			{"role": "assistant", "content": "Here is the helper."},
			{"role": "user", "content": "That didn't work, it fails on RFC3339 timestamps. Try again."}
		]
	}`
	d, _ = RouteDepth(context.Background(), "enso-auto", []byte(turn2), "acme", "acme/alice", "r2")
	if d.Tier() != "enso-pro" || d.p.by != "adaptive-escalate" {
		t.Fatalf("turn 2 pushback -> %q by %q, want enso-pro by adaptive-escalate", d.Tier(), d.p.by)
	}
	if !strings.Contains(strings.ToLower(d.Routing("").Reason), "frustration") {
		t.Errorf("routing reason should explain frustration escalation, got %q", d.Routing("").Reason)
	}

	// Turn 3: repeated frustration in same thread escalates further to enso-max or enso-ultra.
	turn3 := `{
		"model": "enso-auto",
		"thread_id": "thr_frust_1",
		"messages": [
			{"role": "user", "content": "Add a helper function to parse dates"},
			{"role": "assistant", "content": "Here is the helper."},
			{"role": "user", "content": "That didn't work, it fails on RFC3339 timestamps. Try again."},
			{"role": "assistant", "content": "Updated."},
			{"role": "user", "content": "wtf it's still broken!! think harder and find the root cause"}
		]
	}`
	d, _ = RouteDepth(context.Background(), "enso-auto", []byte(turn3), "acme", "acme/alice", "r3")
	if d.Tier() != "enso-ultra" || d.p.by != "adaptive-escalate" {
		t.Fatalf("turn 3 severe repeated frustration -> %q by %q, want enso-ultra by adaptive-escalate", d.Tier(), d.p.by)
	}

	// Turn 4: clean tool output after the fix shifts right back down to enso-flash.
	turn4 := `{
		"model": "enso-auto",
		"thread_id": "thr_frust_1",
		"messages": [
			{"role": "user", "content": "Add a helper function to parse dates"},
			{"role": "assistant", "content": "Fixed timezone handling."},
			{"role": "tool", "content": "PASS\nok github.com/acme/dates 0.11s"}
		]
	}`
	d, _ = RouteDepth(context.Background(), "enso-auto", []byte(turn4), "acme", "acme/alice", "r4")
	if d.Tier() != "enso-flash" {
		t.Fatalf("turn 4 clean tool result -> %q, want enso-flash", d.Tier())
	}
}

func TestAdaptiveRouting_ConsecutiveToolFailuresEscalate(t *testing.T) {
	flashDefaultConfig(t)
	seedEnso(t)
	noKai(t)
	resetAdaptiveThreads()

	// 1 single tool failure does NOT over-escalate (normal red TDD step stays on Flash).
	oneFail := `{
		"model": "enso-auto",
		"thread_id": "thr_err_1",
		"messages": [
			{"role": "user", "content": "Fix the test"},
			{"role": "assistant", "content": "Running go test"},
			{"role": "tool", "content": "--- FAIL: TestParse (0.01s)\nexit status 1"}
		]
	}`
	d, _ := RouteDepth(context.Background(), "enso-auto", []byte(oneFail), "acme", "acme/alice", "r1")
	if d.Tier() != "enso-flash" {
		t.Fatalf("single tool failure -> %q, want enso-flash", d.Tier())
	}

	// 2 consecutive failing tool steps -> escalate to enso-pro.
	twoFails := `{
		"model": "enso-auto",
		"thread_id": "thr_err_2",
		"messages": [
			{"role": "user", "content": "Fix the test"},
			{"role": "assistant", "content": "Running go test"},
			{"role": "tool", "content": "--- FAIL: TestParse (0.01s)\nexit status 1"},
			{"role": "assistant", "content": "Retrying edit"},
			{"role": "tool", "content": "--- FAIL: TestParse (0.01s)\nexit status 1"}
		]
	}`
	d, _ = RouteDepth(context.Background(), "enso-auto", []byte(twoFails), "acme", "acme/alice", "r2")
	if d.Tier() != "enso-pro" || d.p.by != "adaptive-escalate" {
		t.Fatalf("2 consecutive tool failures -> %q by %q, want enso-pro by adaptive-escalate", d.Tier(), d.p.by)
	}

	// 3 consecutive failing tool steps -> escalate to enso-max.
	threeFails := `{
		"model": "enso-auto",
		"thread_id": "thr_err_3",
		"messages": [
			{"role": "user", "content": "Fix the test"},
			{"role": "assistant", "content": "Attempt 1"},
			{"role": "tool", "content": "panic: nil pointer dereference"},
			{"role": "assistant", "content": "Attempt 2"},
			{"role": "tool", "content": "panic: nil pointer dereference"},
			{"role": "assistant", "content": "Attempt 3"},
			{"role": "tool", "content": "panic: nil pointer dereference"}
		]
	}`
	d, _ = RouteDepth(context.Background(), "enso-auto", []byte(threeFails), "acme", "acme/alice", "r3")
	if d.Tier() != "enso-max" || d.p.by != "adaptive-escalate" {
		t.Fatalf("3 consecutive tool failures -> %q by %q, want enso-max by adaptive-escalate", d.Tier(), d.p.by)
	}
}

func TestAdaptiveRouting_SmartInitialTurnThenShiftDown(t *testing.T) {
	flashDefaultConfig(t)
	seedEnso(t)
	noKai(t)
	resetAdaptiveThreads()

	// Turn 1: explicit architecture/migration plan gets enso-pro start.
	turn1 := `{
		"model": "enso-auto",
		"thread_id": "thr_arch_1",
		"messages": [
			{"role": "user", "content": "Create a detailed migration plan and architecture for splitting our ledger service."}
		]
	}`
	d, _ := RouteDepth(context.Background(), "enso-auto", []byte(turn1), "acme", "acme/alice", "r1")
	if d.Tier() != "enso-pro" || d.p.by != "adaptive-initial" {
		t.Fatalf("deep initial prompt -> %q by %q, want enso-pro by adaptive-initial", d.Tier(), d.p.by)
	}

	// Turn 2: routine follow-up in the same thread shifts down to enso-flash.
	turn2 := `{
		"model": "enso-auto",
		"thread_id": "thr_arch_1",
		"messages": [
			{"role": "user", "content": "Create a detailed migration plan and architecture for splitting our ledger service."},
			{"role": "assistant", "content": "Here is the 4-phase migration plan..."},
			{"role": "user", "content": "sounds good, start with phase 1"}
		]
	}`
	d, _ = RouteDepth(context.Background(), "enso-auto", []byte(turn2), "acme", "acme/alice", "r2")
	if d.Tier() != "enso-flash" {
		t.Fatalf("routine turn 2 after deep initial -> %q, want enso-flash", d.Tier())
	}
}

func TestAdaptiveRouting_ExplicitEffortNeverOverridden(t *testing.T) {
	flashDefaultConfig(t)
	seedEnso(t)
	noKai(t)
	resetAdaptiveThreads()

	// Caller explicitly requesting low effort stays on enso-flash even with frustration cues.
	body := `{
		"model": "enso-auto",
		"reasoning_effort": "low",
		"messages": [
			{"role": "user", "content": "wtf still broken try again"}
		]
	}`
	d, _ := RouteDepth(context.Background(), "enso-auto", []byte(body), "acme", "acme/alice", "r1")
	if d.Tier() != "enso-flash" || d.p.by != "effort" {
		t.Fatalf("explicit low effort -> %q by %q, want enso-flash by effort", d.Tier(), d.p.by)
	}
}
