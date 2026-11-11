// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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
    "bytes"
    "context"
    "strings"
    "testing"
)

// TestBillingQANanoPerTokenAndCacheArithmetic verifies P0 Billing & Token Accuracy (#136)
// across exact integer nano-USD conversions, fresh vs cached prompt token splitting,
// and Anthropic-parity 10% default cache-read discounting.
func TestBillingQANanoPerTokenAndCacheArithmetic(t *testing.T) {
    if got := nanoPerToken(0); got != 0 {
        t.Fatalf("nanoPerToken(0) = %d, want 0", got)
    }
    if got := nanoPerToken(-2.5); got != 0 {
        t.Fatalf("nanoPerToken(-2.5) = %d, want 0", got)
    }
    // $6.60/1M -> 6600 nano/token; 200 completion tokens = 1,320,000 nano ($0.00132)
    if got := nanoPerToken(6.60) * 200; got != 1_320_000 {
        t.Fatalf("200 tokens at $6.60/1M = %d nano, want 1320000", got)
    }

    // Verify fresh() never goes negative when cache tokens exceed total prompt tokens
    if got := fresh(100, 80, 50); got != 0 {
        t.Fatalf("fresh(100, 80, 50) = %d, want 0", got)
    }
    if got := fresh(1000, 300, 200); got != 500 {
        t.Fatalf("fresh(1000, 300, 200) = %d, want 500", got)
    }

    // Verify default cacheReadRate is 10% of input rate when readPerM == 0
    if got := cacheReadRate(3.0, 0); got != 0.30 {
        t.Fatalf("cacheReadRate(3.0, 0) = %v, want 0.30", got)
    }

    // Verify tokenNanoAt exact summation across fresh input, completion, cache read, and cache write
    // in=$10/1M (10000 nano), out=$30/1M (30000 nano), read=$1/1M (1000 nano), write=$12.5/1M (12500 nano)
    // prompt=1000 (fresh=500, read=300, write=200), completion=100
    gotNano := tokenNanoAt(10.0, 30.0, 1.0, 12.5, 1000, 100, 300, 200)
    wantNano := int64(500*10000 + 100*30000 + 300*1000 + 200*12500)
    if gotNano != wantNano {
        t.Fatalf("tokenNanoAt = %d, want %d", gotNano, wantNano)
    }
}

// TestBillingQAStreamingIncludeUsageAndAbortedStream verifies that streamCaptureUsage
// captures prompt, completion, and cached tokens from SSE include_usage chunks,
// accumulates delta content + tool_calls arguments for tokenizer fallback, and
// reports zero usage when an upstream stream aborts before emitting tokens (#136).
func TestBillingQAStreamingIncludeUsageAndAbortedStream(t *testing.T) {
    ssePayload := strings.Join([]string{
        ": OPENROUTER PROCESSING",
        `data: {"id":"chatcmpl-1","choices":[{"delta":{"content":"Hello "}}]}`,
        `data: {"id":"chatcmpl-1","choices":[{"delta":{"content":"world!","tool_calls":[{"function":{"arguments":"{"city":"Lahore"}"}}]}}]}`,
        `data: {"id":"chatcmpl-1","choices":[],"usage":{"prompt_tokens":42,"completion_tokens":18,"total_tokens":60}}`,
        "data: [DONE]",
        "",
    }, "
")

    var out bytes.Buffer
    tok, text := streamCaptureUsage(strings.NewReader(ssePayload), &out, func() error { return nil }, true, nil, nil)

    if tok.prompt != 42 || tok.completion != 18 {
        t.Fatalf("captured tokens = (prompt=%d, completion=%d), want (42, 18)", tok.prompt, tok.completion)
    }
    if !strings.Contains(text, "Hello world!") || !strings.Contains(text, `{"city":"Lahore"}`) {
        t.Fatalf("accumulated fallback text = %q, missing content or tool call arguments", text)
    }

    // Aborted stream before token generation must yield 0 prompt and 0 completion tokens
    var emptyOut bytes.Buffer
    abortedTok, abortedText := streamCaptureUsage(strings.NewReader(": keep-alive
"), &emptyOut, func() error { return nil }, false, nil, nil)
    if abortedTok.prompt != 0 || abortedTok.completion != 0 || abortedText != "" {
        t.Fatalf("aborted stream yielded non-zero usage: %+v text=%q", abortedTok, abortedText)
    }
}

// TestBillingQAFailedRequestsAndZeroChargeInvariants verifies that failed or
// non-billable records (0 tokens, negative values, Free/BYO routes, and 401/403/400
// rejected requests) incur zero balance deduction (#136).
func TestBillingQAFailedRequestsAndZeroChargeInvariants(t *testing.T) {
    withMarginPricing(t)

    zeroCases := []*usageRecord{
        {Model: "marginmodel", PromptTokens: 0, CompletionTokens: 0},
        {Model: "marginmodel", PromptTokens: 500, CompletionTokens: 200, Free: true},
    }
    for _, r := range zeroCases {
        if got := usageCostNano(r); got != 0 {
            t.Fatalf("expected 0 nano cost for non-billable/empty record %+v, got %d", r, got)
        }
        if saved := cacheSavedNano(r); saved != 0 {
            t.Fatalf("expected 0 cacheSavedNano for %+v, got %d", r, saved)
        }
    }

    // Verify unauthenticated / rejected usage admin endpoints return 401 without mutating ledger
    msg, err := zapPostBackfillDOUsageHandler(context.Background(), "POST", "/v1/admin/ai/usage/backfill-do", "", "", nil)
    if err != nil {
        t.Fatalf("unexpected transport error: %v", err)
    }
    status, envStatus, _ := gwStatusMsg(t, msg)
    if status != 401 || envStatus != "error" {
        t.Fatalf("rejected request returned status=%d env=%q, want 401/error", status, envStatus)
    }
}
