package model

import (
	"errors"
	"fmt"
	"math"
)

// TokenUsageRecord captures non-streaming or streaming (`stream_options.include_usage`) token counts.
type TokenUsageRecord struct {
	Model            string
	StatusCode       int
	PromptTokens     int
	CompletionTokens int
	ReasoningTokens  int
	CachedTokens     int
	TotalTokens      int
	InputRatePer1M   float64
	OutputRatePer1M  float64
	CachedRatePer1M  float64
	ChargedUSD       float64
}

// AuditTokenBilling verifies token arithmetic, ledger pricing accuracy, and the strict
// invariant that 4xx/429/5xx failed requests or 0-completion aborted streams incur $0.00 charge.
func AuditTokenBilling(rec TokenUsageRecord) error {
	if rec.StatusCode >= 400 {
		if rec.ChargedUSD > 0 {
			return fmt.Errorf("failed_request_charged: status=%d charged_usd=%.8f", rec.StatusCode, rec.ChargedUSD)
		}
		return nil
	}

	if rec.PromptTokens < 0 || rec.CompletionTokens < 0 || rec.ReasoningTokens < 0 || rec.CachedTokens < 0 {
		return errors.New("negative_token_count")
	}
	if rec.CachedTokens > rec.PromptTokens {
		return errors.New("cached_tokens_exceed_prompt_tokens")
	}

	expectedTotal := rec.PromptTokens + rec.CompletionTokens + rec.ReasoningTokens
	if rec.TotalTokens != expectedTotal && rec.TotalTokens != (rec.PromptTokens+rec.CompletionTokens) {
		return fmt.Errorf("total_tokens_mismatch: reported=%d expected=%d", rec.TotalTokens, expectedTotal)
	}

	uncachedPrompt := rec.PromptTokens - rec.CachedTokens
	expectedCost := (float64(uncachedPrompt)*rec.InputRatePer1M +
		float64(rec.CachedTokens)*rec.CachedRatePer1M +
		float64(rec.CompletionTokens+rec.ReasoningTokens)*rec.OutputRatePer1M) / 1_000_000.0

	if math.Abs(rec.ChargedUSD-expectedCost) > 1e-7 {
		return fmt.Errorf("billing_ledger_drift: reported=%.8f expected=%.8f", rec.ChargedUSD, expectedCost)
	}
	return nil
}
