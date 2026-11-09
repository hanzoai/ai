// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package model

import (
	"math"
	"testing"
)

// A thousand tokens asked and three thousand answered: the prompt is priced at
// the input rate and the answer at the output rate. These five priced the TOTAL
// at both rates, so every call was charged for its whole length twice over.
func TestPromptAndAnswerArePricedAtTheirOwnRates(t *testing.T) {
	for _, c := range []struct {
		name     string
		price    func(*ModelResult, string) error
		in, out  float64 // the table's per-thousand rates for that model
		currency string
	}{
		{"deepseek-chat", (&DeepSeekProvider{subType: "deepseek-chat"}).calculatePrice, 0.002, 0.003, "CNY"},
		{"mistral-large-latest", (&MistralModelProvider{modelName: "mistral-large-latest"}).calculatePrice, 0.002, 0.006, "USD"},
		{"deepseek-ai/DeepSeek-R1", (&SiliconFlowProvider{subType: "deepseek-ai/DeepSeek-R1"}).calculatePrice, 0.004, 0.016, "CNY"},
		{"Baichuan4-Air", (&BaichuanModelProvider{subType: "Baichuan4-Air"}).calculatePrice, 0.00098, 0.00098, "CNY"},
		{"ernie-4.0-8k", (&BaiduCloudModelProvider{subType: "ernie-4.0-8k"}).calculatePrice, 0.03, 0.09, "CNY"},
	} {
		result := &ModelResult{PromptTokenCount: 1000, ResponseTokenCount: 3000, TotalTokenCount: 4000}
		if err := c.price(result, "en"); err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		want := 1*c.in + 3*c.out
		if math.Abs(result.TotalPrice-want) > 1e-9 {
			t.Errorf("%s: 1k in + 3k out = %v, want %v", c.name, result.TotalPrice, want)
		}
		if result.Currency != c.currency {
			t.Errorf("%s: currency %q, want %q", c.name, result.Currency, c.currency)
		}
	}
}

// gpt-4.5 is billed at the rate the pricing table publishes for it. Its case sat
// after gpt-4's, which matches "gpt-4.5" too, so it was never reached and
// gpt-4.5 was charged as gpt-4. The neighbours keep their own rates.
func TestGpt45IsBilledAtItsOwnRate(t *testing.T) {
	for _, c := range []struct {
		model   string
		in, out float64
	}{
		{"gpt-4.5-preview", 0.002, 0.008},
		{"gpt-4.5-mini", 0.0004, 0.0016},
		{"gpt-4.5-nano", 0.0001, 0.0004},
		{"gpt-4", 0.03, 0.06},
		{"gpt-4-turbo", 0.01, 0.03},
		{"gpt-4o", 0.0025, 0.0075},
		{"gpt-4.1", 0.002, 0.008},
	} {
		result := &ModelResult{PromptTokenCount: 1000, ResponseTokenCount: 1000, TotalTokenCount: 2000}
		if err := CalculateOpenAIModelPrice(c.model, result, "en"); err != nil {
			t.Fatalf("%s: %v", c.model, err)
		}
		if want := c.in + c.out; math.Abs(result.TotalPrice-want) > 1e-9 {
			t.Errorf("%s: 1k in + 1k out = %v, want %v", c.model, result.TotalPrice, want)
		}
	}
}
