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

package controllers

import (
	"strings"
	"testing"

	"github.com/hanzoai/ai/object"
)

// TestStreamCaptureUsageCapturesTokens is the #1 P0 assertion: a streaming
// tool-call response that includes a usage chunk yields REAL token counts to
// bill — the streamed path no longer bills $0.
func TestStreamCaptureUsageCapturesTokens(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"id":"c1","model":"gpt-4o-mini","choices":[{"delta":{"content":"Hel"}}]}`,
		`data: {"id":"c1","model":"gpt-4o-mini","choices":[{"delta":{"content":"lo"}}]}`,
		`data: {"id":"c1","model":"gpt-4o-mini","choices":[{"delta":{"tool_calls":[{"function":{"arguments":"{\"x\":1}"}}]}}]}`,
		`data: {"id":"c1","model":"gpt-4o-mini","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":5,"total_tokens":17}}`,
		"data: [DONE]",
		"",
	}, "\n")

	var out strings.Builder
	prompt, completion, total, text := streamCaptureUsage(strings.NewReader(sse), &out, nil, false, nil, &mark{id: "chatcmpl-req1", model: "gpt-4o-mini", seller: "hanzo"})

	if prompt != 12 || completion != 5 || total != 17 {
		t.Fatalf("captured usage = (%d,%d,%d), want (12,5,17) — streamed tool call MUST capture real tokens", prompt, completion, total)
	}
	if !strings.Contains(text, "Hello") || !strings.Contains(text, `{"x":1}`) {
		t.Errorf("accumulated output = %q, want it to contain content + tool args", text)
	}
	// Client did NOT request usage => the forced usage-only chunk must be suppressed.
	if strings.Contains(out.String(), `"usage"`) {
		t.Errorf("usage-only chunk leaked to client that did not request it:\n%s", out.String())
	}
	// Content chunks ARE forwarded.
	if !strings.Contains(out.String(), `"content":"Hel"`) {
		t.Errorf("content chunks must be forwarded, got:\n%s", out.String())
	}
}

// TestStreamCaptureUsageForwardsUsageWhenRequested: when the client asked for
// usage, the usage chunk is forwarded (with a fixed envelope).
func TestStreamCaptureUsageForwardsUsageWhenRequested(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"id":"c2","model":"m","choices":[{"delta":{"content":"hi"}}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
		"data: [DONE]",
		"",
	}, "\n")
	var out strings.Builder
	_, _, total, _ := streamCaptureUsage(strings.NewReader(sse), &out, nil, true, nil, &mark{id: "chatcmpl-c2", model: "m", seller: "hanzo"})
	if total != 5 {
		t.Fatalf("total=%d, want 5", total)
	}
	if !strings.Contains(out.String(), `"usage"`) {
		t.Errorf("usage chunk must be forwarded when requested, got:\n%s", out.String())
	}
	// The forced bare-usage chunk got an envelope (id/object) for SDK clients.
	if !strings.Contains(out.String(), `"object":"chat.completion.chunk"`) {
		t.Errorf("forwarded usage chunk must carry a fixed envelope, got:\n%s", out.String())
	}
}

// TestReserveSettleDebitsLedger proves the controller reserve/settle path holds
// then debits the SAME ledger the gate reads — the per-org debit for a request.
func TestReserveSettleDebitsLedger(t *testing.T) {
	const subject = "billtest/u1"
	object.GlobalBalanceLedger.SetBalance(subject, 1000)

	hold, ok := reserveBudget(subject, 100)
	if !ok || hold == nil {
		t.Fatal("reserve against a funded subject must succeed")
	}
	if avail, _ := object.GlobalBalanceLedger.Available(subject); avail != 900 {
		t.Fatalf("available after reserve = %d, want 900 (100 held)", avail)
	}
	hold.settle(40) // actual cost 40
	if avail, _ := object.GlobalBalanceLedger.Available(subject); avail != 960 {
		t.Fatalf("available after settle = %d, want 960 (debited 40, hold released)", avail)
	}
	// Double-settle is a no-op (idempotent).
	hold.settle(40)
	if avail, _ := object.GlobalBalanceLedger.Available(subject); avail != 960 {
		t.Fatalf("double settle changed balance: %d, want 960", avail)
	}
}

// TestReserveBudgetGatesInsufficient: a known subject without funds for the
// estimate is gated (ok=false).
func TestReserveBudgetGatesInsufficient(t *testing.T) {
	const subject = "billtest/u2"
	object.GlobalBalanceLedger.SetBalance(subject, 50)
	if _, ok := reserveBudget(subject, 100); ok {
		t.Error("reserve must fail when the estimate exceeds the balance")
	}
}

// TestReserveBudgetSkipsUnknownAndAnon: a cold subject (no known balance) and an
// empty subject (widget/anon) are NOT gated — reservation only augments the
// existing balance gate, it never invents a new fail-closed path.
func TestReserveBudgetSkipsUnknownAndAnon(t *testing.T) {
	if h, ok := reserveBudget("billtest/cold-unknown", 100); !ok || h == nil || h.subject != "" {
		t.Error("cold/unknown subject must be skipped (ok, no real hold)")
	}
	if h, ok := reserveBudget("", 100); !ok || h == nil || h.subject != "" {
		t.Error("empty subject (widget/anon) must be skipped")
	}
}

// TestEstimateRequestCostMonotonic: more max_tokens never costs less (upper bound),
// and the estimate is non-negative.
func TestEstimateRequestCostMonotonic(t *testing.T) {
	small := estimateRequestCostCents("gpt-4o-mini", 100, reserveCompletionFloor)
	big := estimateRequestCostCents("gpt-4o-mini", 100, 8192)
	if small < 0 || big < 0 {
		t.Fatalf("estimates must be non-negative: small=%d big=%d", small, big)
	}
	if big < small {
		t.Errorf("more max_tokens must not cost less: small=%d big=%d", small, big)
	}
}

// TestClampMaxTokens locks the upstream completion ceiling: uncapped/absurd →
// reserveCompletionFloor; a normal client cap is preserved unchanged (R1b).
func TestClampMaxTokens(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, reserveCompletionFloor},                              // uncapped
		{-5, reserveCompletionFloor},                             // negative
		{500, 500},                                               // normal cap preserved
		{reserveCompletionFloor, reserveCompletionFloor},         // at floor
		{maxReserveCompletionTokens, maxReserveCompletionTokens}, // at max boundary
		{maxReserveCompletionTokens + 1, reserveCompletionFloor}, // absurd => floor
	}
	for _, c := range cases {
		if got := clampMaxTokens(c.in); got != c.want {
			t.Errorf("clampMaxTokens(%d)=%d, want %d", c.in, got, c.want)
		}
	}
}

// TestReserveCoversModelLayerCeiling: the QueryText pipeline caps completion at
// reserveCompletionFloor regardless of a low client cap, so the reservation MUST
// cover that ceiling even when the client capped lower.
func TestReserveCoversModelLayerCeiling(t *testing.T) {
	if got := reserveCompletionTokens(100); got < reserveCompletionFloor {
		t.Fatalf("reserveCompletionTokens(100)=%d must be >= floor %d (QueryText emits up to the floor)", got, reserveCompletionFloor)
	}
	estLowCap := estimateRequestCostCents("gpt-4o-mini", 100, 100)
	estFloor := calculateCostCents("gpt-4o-mini", 100, reserveCompletionFloor)
	if estLowCap < estFloor {
		t.Fatalf("a low client cap must still reserve >= the model-layer ceiling cost: got %d, floor cost %d", estLowCap, estFloor)
	}
}

// TestUncappedRequestCannotOverdraft is the R1b assertion: an UNCAPPED request,
// after the max_tokens clamp at the call site, can never settle MORE than it
// reserved — so a single request cannot drive the balance negative. The prior
// 1024-token estimate under-reserved and the actual (larger) settle went negative.
func TestUncappedRequestCannotOverdraft(t *testing.T) {
	const (
		subject = "billtest/uncapped"
		model   = "gpt-4o-mini"
	)
	promptTokens := 100

	// Controller flow: clamp the uncapped max_tokens, then reserve the estimate.
	clamped := clampMaxTokens(0) // uncapped
	if clamped != reserveCompletionFloor {
		t.Fatalf("uncapped clamp=%d, want reserveCompletionFloor=%d", clamped, reserveCompletionFloor)
	}
	est := estimateRequestCostCents(model, promptTokens, clamped)

	// Fund EXACTLY to the reservation — the tightest no-overdraft case.
	object.GlobalBalanceLedger.SetBalance(subject, est)
	hold, ok := reserveBudget(subject, est)
	if !ok || hold == nil {
		t.Fatalf("reserve must succeed when balance == estimate (est=%d)", est)
	}

	// Worst-case ACTUAL: a completion at the clamped ceiling. Because the upstream
	// is clamped to `clamped` tokens, the model can emit no more — actual <= reserve.
	worstActual := calculateCostCents(model, promptTokens, clamped)
	if worstActual > est {
		t.Fatalf("worst-case actual %d exceeds reservation %d — overdraft possible", worstActual, est)
	}
	hold.settle(worstActual)

	if avail, _ := object.GlobalBalanceLedger.Available(subject); avail < 0 {
		t.Fatalf("balance went NEGATIVE within one request: %d (worstActual=%d est=%d)", avail, worstActual, est)
	}
}
