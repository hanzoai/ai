package model

import "testing"

func TestAuditTokenBillingEnforcesZeroChargeOnErrorsAndExactLedgerMath(t *testing.T) {
	// 1. Valid streaming/non-streaming usage with cached & reasoning tokens
	valid := TokenUsageRecord{
		Model:            "zen-1.5-pro",
		StatusCode:       200,
		PromptTokens:     1000,
		CachedTokens:     400,
		CompletionTokens: 500,
		ReasoningTokens:  100,
		TotalTokens:      1600,
		InputRatePer1M:   2.0,
		CachedRatePer1M:  0.5,
		OutputRatePer1M:  8.0,
		ChargedUSD:       (600*2.0 + 400*0.5 + 600*8.0) / 1_000_000.0,
	}
	if err := AuditTokenBilling(valid); err != nil {
		t.Fatalf("expected valid billing record, got: %v", err)
	}

	// 2. Failed HTTP 429 / 500 must never charge user balance
	failedCharged := valid
	failedCharged.StatusCode = 429
	if err := AuditTokenBilling(failedCharged); err == nil {
		t.Fatal("expected error when HTTP 429 request was charged")
	}
}
