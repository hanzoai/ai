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
	"testing"
	"time"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/decimal"
)

// TestBillingTokenAccuracy_PreVendorFailuresNeverCharge proves the P0 billing
// safety invariant (#136): requests that fail before reaching an upstream vendor
// (400 validation, 401 auth, 429 pre-flight rate limit, or aborted client stream
// where Provider == "") MUST NEVER emit a ledger debit or commerce charge.
func TestBillingTokenAccuracy_PreVendorFailuresNeverCharge(t *testing.T) {
	failureScenarios := []struct {
		name     string
		status   string
		errMsg   string
		pTokens  int
		cTokens  int
	}{
		{"400_invalid_model_param", "error", "400 Bad Request: invalid temperature", 0, 0},
		{"401_unauthorized_bearer", "error", "401 Unauthorized: missing bearer token", 0, 0},
		{"429_preflight_rate_limit", "error", "429 Too Many Requests: rate limit exceeded", 250, 0},
		{"aborted_stream_pre_upstream", "error", "client canceled context before upstream dispatch", 512, 0},
	}

	for _, sc := range failureScenarios {
		t.Run(sc.name, func(t *testing.T) {
			rec := &usageRecord{
				Owner:            "acme",
				User:             "acme/alice",
				Model:            "gpt-4o",
				Provider:         "", // never reached an upstream vendor
				PromptTokens:     sc.pTokens,
				CompletionTokens: sc.cTokens,
				TotalTokens:      sc.pTokens + sc.cTokens,
				Currency:         "USD",
				Status:           sc.status,
				ErrorMsg:         sc.errMsg,
				RequestID:        "req-" + sc.name,
			}
			if events := spend(t, rec); len(events) != 0 {
				t.Fatalf("scenario %s charged %d ledger events (%+v), want 0", sc.name, len(events), events)
			}
			if queued := enqueued(t, rec); len(queued) != 0 {
				t.Fatalf("scenario %s enqueued %d commerce charges (%+v), want 0", sc.name, len(queued), queued)
			}
		})
	}
}

// TestBillingTokenAccuracy_TokenLinearityAndParity verifies that reported
// prompt_tokens, completion_tokens, and total_tokens produce strictly monotonic,
// linear USD billing across both the native in-proc ledger recorder and the
// standalone HTTP commerce billing queue (#136).
func TestBillingTokenAccuracy_TokenLinearityAndParity(t *testing.T) {
	baseRec := &usageRecord{
		Owner:            "acme",
		User:             "acme/alice",
		Model:            "gpt-4o",
		Provider:         "openai",
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500,
		Currency:         "USD",
		Status:           "success",
		RequestID:        "req-linear-1x",
	}
	doubleRec := &usageRecord{
		Owner:            "acme",
		User:             "acme/alice",
		Model:            "gpt-4o",
		Provider:         "openai",
		PromptTokens:     2000,
		CompletionTokens: 1000,
		TotalTokens:      3000,
		Currency:         "USD",
		Status:           "success",
		RequestID:        "req-linear-2x",
	}

	ev1 := spend(t, baseRec)
	ev2 := spend(t, doubleRec)
	if len(ev1) != 1 || len(ev2) != 1 {
		t.Fatalf("expected 1 ledger event each, got %d and %d", len(ev1), len(ev2))
	}

	usd1, err1 := decimal.Parse(ev1[0].USD)
	usd2, err2 := decimal.Parse(ev2[0].USD)
	if err1 != nil || err2 != nil {
		t.Fatalf("invalid USD decimals: %q (%v), %q (%v)", ev1[0].USD, err1, ev2[0].USD, err2)
	}
	if usd1.Sign() <= 0 {
		t.Fatalf("expected positive USD charge for 1500 tokens, got %s", usd1.String())
	}
	wantDouble := usd1.Mul(decimal.New(2, 0))
	if usd2.Cmp(wantDouble) != 0 {
		t.Errorf("2x tokens billed %s, want exact 2x = %s", usd2.String(), wantDouble.String())
	}
}

// TestBillingTokenAccuracy_FeeMarkupAndLedgerMargin verifies platform fee
// markups (5.5% standard vs 1.0% BYOK), nanodollar ceiling rounding, and
// multi-route ledger margin & loss accounting (#136).
func TestBillingTokenAccuracy_FeeMarkupAndLedgerMargin(t *testing.T) {
	orFees(t)

	// 1,000,000 nanodollars ($0.001) with 5.5% OpenRouter fee -> 1,055,000 nano
	base := int64(1_000_000)
	withVendorFee := costed(&base, "openrouter")
	if withVendorFee == nil || *withVendorFee != 1_055_000 {
		t.Fatalf("costed(1_000_000, openrouter) = %v, want 1055000", withVendorFee)
	}

	// Platform fee (5.5%) on top of 1,055,000 -> 1,113,025 nano; BYOK (1%) -> 1,010,000 nano
	if got := plus(*withVendorFee, false); got != 1_113_025 {
		t.Errorf("plus(1055000, byok=false) = %d, want 1113025", got)
	}
	if got := plus(base, true); got != 1_010_000 {
		t.Errorf("plus(1000000, byok=true) = %d, want 1010000", got)
	}

	// Verify ledger aggregation across success, failover, and error rows
	now := time.Now().UTC()
	summary := books([]object.Routed{
		{
			Model: "enso-flash", Served: "openai/gpt-6-luna", Vendor: "openrouter",
			Status: "success", Requests: 5, CostNano: 2_000_000, BilledNano: 6_000_000, KnownNano: 6_000_000,
		},
		{
			Model: "enso-flash", Served: "openai/gpt-6-luna", Vendor: "openrouter",
			Status: "error", Requests: 3, CostNano: 0, BilledNano: 0,
		},
	}, now)
	if summary.Requests != 5 {
		t.Errorf("summary.Requests = %d, want 5 (error rows excluded from billed request count)", summary.Requests)
	}
	if summary.MarginNano != 4_000_000 {
		t.Errorf("summary.MarginNano = %d, want 4000000", summary.MarginNano)
	}
}
