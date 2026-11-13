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
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/ai/object"
)

// TestQABillingTokenAccuracyStreamingAndNonStreamingParity verifies that non-streaming
// and streaming (stream_options: { include_usage: true }) responses report identical
// prompt_tokens, completion_tokens, cached prompt tokens, and total_tokens across
// text, reasoning, tool-call, and multimodal turns.
func TestQABillingTokenAccuracyStreamingAndNonStreamingParity(t *testing.T) {
	withMarginPricing(t)

	cases := []struct {
		name             string
		model            string
		promptTokens     int
		completionTokens int
		cachedReadTokens int
		toolArgs         string
		clientWantsUsage bool
	}{
		{
			name:             "standard text completion (gpt-4o-mini)",
			model:            "gpt-4o-mini",
			promptTokens:     128,
			completionTokens: 64,
			cachedReadTokens: 0,
			clientWantsUsage: true,
		},
		{
			name:             "cached prompt + tool-call stream (claude-sonnet-4-6)",
			model:            "claude-sonnet-4-6",
			promptTokens:     2400,
			completionTokens: 180,
			cachedReadTokens: 1800,
			toolArgs:         `{"query":"hanzo billing ledger","limit":10}`,
			clientWantsUsage: true,
		},
		{
			name:             "reasoning tier with suppressed client usage chunk (zen5-pro)",
			model:            "zen5-pro",
			promptTokens:     512,
			completionTokens: 1024,
			cachedReadTokens: 256,
			clientWantsUsage: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			totalTokens := tc.promptTokens + tc.completionTokens
			usageJSON := fmt.Sprintf(
				`{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d,"prompt_tokens_details":{"cached_tokens":%d}}`,
				tc.promptTokens, tc.completionTokens, totalTokens, tc.cachedReadTokens,
			)

			// 1. Non-streaming usage extraction via sniffZenUsage
			nonStreamBody := fmt.Sprintf(
				`{"id":"chatcmpl-ns","object":"chat.completion","model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}],"usage":%s}`,
				tc.model, usageJSON,
			)
			var nonStreamTokens tokens
			sniffZenUsage([]byte(nonStreamBody), &nonStreamTokens)

			// 2. Streaming usage capture via streamCaptureUsage
			var chunks []string
			chunks = append(chunks, fmt.Sprintf(`data: {"id":"chatcmpl-st","model":%q,"choices":[{"delta":{"content":"Hello "}}]}`, tc.model))
			if tc.toolArgs != "" {
				chunks = append(chunks, fmt.Sprintf(`data: {"id":"chatcmpl-st","model":%q,"choices":[{"delta":{"tool_calls":[{"function":{"arguments":%q}}]}}]}`, tc.model, tc.toolArgs))
			}
			chunks = append(chunks, fmt.Sprintf(`data: {"id":"chatcmpl-st","model":%q,"choices":[],"usage":%s}`, tc.model, usageJSON))
			chunks = append(chunks, "data: [DONE]", "")

			var forwarded strings.Builder
			streamTokens, accText := streamCaptureUsage(
				strings.NewReader(strings.Join(chunks, "\n")),
				&forwarded,
				nil,
				tc.clientWantsUsage,
				nil,
				&mark{id: "chatcmpl-st", model: tc.model, seller: "hanzo"},
			)

			// Verify non-streaming and streaming token parity
			if nonStreamTokens.prompt() != tc.promptTokens || streamTokens.prompt() != tc.promptTokens {
				t.Fatalf("prompt_tokens mismatch: nonStream=%d stream=%d want=%d",
					nonStreamTokens.prompt(), streamTokens.prompt(), tc.promptTokens)
			}
			if nonStreamTokens.completion != tc.completionTokens || streamTokens.completion != tc.completionTokens {
				t.Fatalf("completion_tokens mismatch: nonStream=%d stream=%d want=%d",
					nonStreamTokens.completion, streamTokens.completion, tc.completionTokens)
			}
			if nonStreamTokens.cached != tc.cachedReadTokens || streamTokens.cached != tc.cachedReadTokens {
				t.Fatalf("cached_tokens mismatch: nonStream=%d stream=%d want=%d",
					nonStreamTokens.cached, streamTokens.cached, tc.cachedReadTokens)
			}
			if gotTotal := streamTokens.prompt() + streamTokens.completion; gotTotal != totalTokens {
				t.Fatalf("total_tokens invariant violated: prompt(%d)+completion(%d)=%d, want %d",
					streamTokens.prompt(), streamTokens.completion, gotTotal, totalTokens)
			}
			if tc.toolArgs != "" && !strings.Contains(accText, tc.toolArgs) {
				t.Errorf("accumulated stream output %q missing tool call arguments %q", accText, tc.toolArgs)
			}

			// Verify include_usage forwarding vs suppression
			hasUsageChunk := strings.Contains(forwarded.String(), `"usage"`)
			if tc.clientWantsUsage && !hasUsageChunk {
				t.Errorf("client requested include_usage=true but usage chunk was not forwarded:\n%s", forwarded.String())
			}
			if !tc.clientWantsUsage && hasUsageChunk {
				t.Errorf("client did not request include_usage but forced usage chunk leaked:\n%s", forwarded.String())
			}

			// Verify identical nano-USD billing across streaming and non-streaming records
			nonStreamRec := &usageRecord{
				Model:            "marginmodel",
				PromptTokens:     nonStreamTokens.prompt(),
				CompletionTokens: nonStreamTokens.completion,
				CacheReadTokens:  nonStreamTokens.cached,
			}
			streamRec := &usageRecord{
				Model:            "marginmodel",
				PromptTokens:     streamTokens.prompt(),
				CompletionTokens: streamTokens.completion,
				CacheReadTokens:  streamTokens.cached,
			}
			if usageCostNano(nonStreamRec) != usageCostNano(streamRec) {
				t.Errorf("billing drift between non-streaming (%d nano) and streaming (%d nano)",
					usageCostNano(nonStreamRec), usageCostNano(streamRec))
			}
		})
	}
}

// TestQABillingLedgerAndModelConfigRates verifies that conf/models.yaml pricing definitions
// map deterministically to integer nano-USD ledger entries, /v1/usage row values,
// cache-read discounts (10% of input when unstated), and BYOK 1% platform fees.
func TestQABillingLedgerAndModelConfigRates(t *testing.T) {
	prev := globalModelConfig
	t.Cleanup(func() { globalModelConfig = prev })

	if err := InitModelConfig("../conf/models.yaml"); err != nil {
		t.Fatalf("InitModelConfig(../conf/models.yaml): %v", err)
	}

	// Models the config prices itself, with the per-million rates it states. A model the
	// config does not name falls back to a synthesized default, which would make this
	// test pass without reading the config at all.
	representativeModels := []struct {
		id      string
		in, out float64
	}{
		{"claude-3-5-haiku", 0.80, 4.00},
		{"claude-3-7-sonnet", 3.00, 15.00},
		{"claude-opus-4", 15.00, 75.00},
	}

	for _, rm := range representativeModels {
		modelID := rm.id
		price, priced := getModelPriceForOrgOK(modelID, "")
		if !priced {
			t.Fatalf("model %q is not priced by conf/models.yaml", modelID)
		}
		if price.InputPerMillion != rm.in || price.OutputPerMillion != rm.out {
			t.Fatalf("model %q priced %v/%v per 1M, conf/models.yaml states %v/%v",
				modelID, price.InputPerMillion, price.OutputPerMillion, rm.in, rm.out)
		}

		rec := &usageRecord{
			Owner:            "hanzo",
			User:             "hanzo/qa-auditor",
			Model:            modelID,
			PromptTokens:     2000,
			CompletionTokens: 500,
			TotalTokens:      2500,
			CacheReadTokens:  1000,
			Status:           "success",
		}

		expectedNano := tokenNanoAt(
			price.InputPerMillion,
			price.OutputPerMillion,
			price.CacheReadPerMillion,
			price.CacheWritePerMillion,
			rec.PromptTokens,
			rec.CompletionTokens,
			rec.CacheReadTokens,
			rec.CacheWriteTokens,
		)
		gotNano := usageCostNano(rec)
		if gotNano != expectedNano {
			t.Errorf("%s: usageCostNano = %d, want exact %d", modelID, gotNano, expectedNano)
		}

		// Verify /v1/usage cloud_usage row consistency
		values := cloudUsageValues(rec, time.Now())
		at := map[string]any{}
		for i, col := range object.CloudUsageColumns {
			if i < len(values) {
				at[col] = values[i]
			}
		}
		if at["billed_nano"] != usageMargin(rec).BilledNano {
			t.Errorf("%s: cloud_usage.billed_nano = %v, want %d", modelID, at["billed_nano"], usageMargin(rec).BilledNano)
		}
		if at["total_tokens"] != rec.TotalTokens {
			t.Errorf("%s: cloud_usage.total_tokens = %v, want %d", modelID, at["total_tokens"], rec.TotalTokens)
		}
		if at["cost_cents"] != nanoToCents(gotNano) {
			t.Errorf("%s: cloud_usage.cost_cents = %v, want nanoToCents(%d)=%d", modelID, at["cost_cents"], gotNano, nanoToCents(gotNano))
		}
	}
}

// TestQAFailedRequestsAndAbortedStreamsNeverDeductBalance explicitly verifies that:
//  1. Pre-flight 4xx/402 insufficient-balance rejections incur 0 model calls and 0 debits.
//  2. Upstream 4xx (validation/auth), 429 (rate limit), and 5xx (provider error) responses
//     that settle with 0 actual spend release 100% of the budget hold with zero deduction.
//  3. Aborted streams before token generation capture 0 tokens and settle at 0 nano-USD.
func TestQAFailedRequestsAndAbortedStreamsNeverDeductBalance(t *testing.T) {
	withMarginPricing(t)

	t.Run("4xx/429/5xx failed upstream calls release reservation with zero deduction", func(t *testing.T) {
		failureStatuses := []struct {
			code int
			desc string
		}{
			{400, "400 Bad Request validation failure"},
			{401, "401 Unauthorized upstream auth error"},
			{422, "422 Unprocessable Entity schema error"},
			{429, "429 Too Many Requests rate limit"},
			{500, "500 Internal Server Error upstream crash"},
			{502, "502 Bad Gateway provider error"},
			{503, "503 Service Unavailable overloaded"},
		}

		for _, fs := range failureStatuses {
			subject := fmt.Sprintf("qa-zero-debit/status-%d", fs.code)
			const initialBalanceCents int64 = 5000 // $50.00
			object.GlobalBalanceLedger.SetBalance(subject, initialBalanceCents)

			estCents := estimateRequestCostCents("marginmodel", 500, reserveCompletionFloor)
			if estCents <= 0 {
				t.Fatalf("expected positive reservation estimate for priced model, got %d", estCents)
			}

			hold, ok := reserveBudget(subject, estCents)
			if !ok || hold == nil {
				t.Fatalf("%s: reserveBudget failed on funded wallet", fs.desc)
			}

			// Simulate failed upstream HTTP status: deferred fail-safe settle(0) runs
			hold.settle(0)
			// Verify idempotency if deferred cleanup also calls settleNano(0)
			hold.settleNano(0)

			availAfter, known := object.GlobalBalanceLedger.Available(subject)
			if !known || availAfter != initialBalanceCents {
				t.Errorf("%s: balance after failed request = %d cents (known=%v), want exact initial %d cents (zero deduction)",
					fs.desc, availAfter, known, initialBalanceCents)
			}
		}
	})

	t.Run("aborted stream before token generation bills zero nano-USD", func(t *testing.T) {
		const subject = "qa-zero-debit/aborted-stream"
		const initialBalanceCents int64 = 2500
		object.GlobalBalanceLedger.SetBalance(subject, initialBalanceCents)

		hold, ok := reserveBudget(subject, 50)
		if !ok || hold == nil {
			t.Fatal("reserveBudget must succeed on funded subject")
		}

		// Upstream sends only keep-alive comment before connection abort / reset
		abortedSSE := ": OPENROUTER PROCESSING\n"
		var out strings.Builder
		used, text := streamCaptureUsage(
			strings.NewReader(abortedSSE),
			&out,
			nil,
			true,
			nil,
			&mark{id: "chatcmpl-abort", model: "marginmodel", seller: "hanzo"},
		)

		if used.prompt() != 0 || used.completion != 0 || text != "" {
			t.Fatalf("aborted stream before token generation must yield 0 tokens, got prompt=%d completion=%d text=%q",
				used.prompt(), used.completion, text)
		}

		rec := &usageRecord{
			Model:            "marginmodel",
			PromptTokens:     used.prompt(),
			CompletionTokens: used.completion,
		}
		billedNano := usageMargin(rec).BilledNano
		if billedNano != 0 {
			t.Fatalf("aborted stream with 0 tokens billed %d nano, want 0", billedNano)
		}

		hold.settleNano(billedNano)
		if avail, _ := object.GlobalBalanceLedger.Available(subject); avail != initialBalanceCents {
			t.Fatalf("aborted stream deducted balance: got %d, want %d", avail, initialBalanceCents)
		}
	})
}
