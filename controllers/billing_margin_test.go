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

import "testing"

// withMarginPricing pins globalModelConfig to a hand-built table so usageMargin
// resolves deterministic prices/COGS (no live DB, no YAML). "marginmodel" prices at
// 10/30 $/1M with a genuine 2/6 COGS; "zeromargin" configures no COGS so cost defaults
// to price. Restores the previous config on cleanup.
func withMarginPricing(t *testing.T) {
	t.Helper()
	mc := &ModelConfig{
		routes: map[string]modelRoute{},
		pricing: map[string]modelPrice{
			"marginmodel": {InputPerMillion: 10, OutputPerMillion: 30, CostInPerMillion: 2, CostOutPerMillion: 6},
			"zeromargin":  {InputPerMillion: 10, OutputPerMillion: 30},
		},
		defaults: modelPrice{InputPerMillion: 1, OutputPerMillion: 4},
		stopCh:   make(chan struct{}),
	}
	prev := globalModelConfig
	globalModelConfig = mc
	t.Cleanup(func() { globalModelConfig = prev })
}

// TestUsageMargin pins the money-of-record margin math (all nano-USD): billed = what the
// org is debited, cost = provider COGS, margin = billed − cost. It covers the three
// load-bearing cases — price≠cost ⇒ margin>0, unset cost ⇒ margin 0, and the BYO fee
// path (billed still the 1% fee, COGS 0, so margin == the fee) — plus cache-token COGS
// defaulting. nanoPerToken(perM)=perM×1000, so a $10/1M input rate is 10000 nano/token.
func TestUsageMargin(t *testing.T) {
	withMarginPricing(t)

	cases := []struct {
		name                             string
		rec                              usageRecord
		wantCost, wantBilled, wantMargin int64
	}{
		{
			// billed 1000·10000 + 500·30000 = 25,000,000 ; cost 1000·2000 + 500·6000 = 5,000,000.
			name:       "served, price>cost ⇒ margin>0",
			rec:        usageRecord{Model: "marginmodel", PromptTokens: 1000, CompletionTokens: 500},
			wantCost:   5_000_000,
			wantBilled: 25_000_000,
			wantMargin: 20_000_000,
		},
		{
			// COGS unset ⇒ cost accessor defaults to price ⇒ cost == billed ⇒ margin 0.
			name:       "served, cost unset ⇒ margin 0",
			rec:        usageRecord{Model: "zeromargin", PromptTokens: 1000, CompletionTokens: 500},
			wantCost:   25_000_000,
			wantBilled: 25_000_000,
			wantMargin: 0,
		},
		{
			// BYO: billed = ceil(25,000,000/100) = 250,000 (unchanged 1% fee); COGS 0 (the
			// customer paid the upstream), so the whole fee is margin.
			name:       "BYO, fee path unchanged ⇒ margin == fee",
			rec:        usageRecord{Model: "marginmodel", PromptTokens: 1000, CompletionTokens: 500, BYO: true},
			wantCost:   0,
			wantBilled: 250_000,
			wantMargin: 250_000,
		},
		{
			// BYO margin is the fee regardless of whether a COGS is configured.
			name:       "BYO, no COGS configured ⇒ margin == fee",
			rec:        usageRecord{Model: "zeromargin", PromptTokens: 1000, CompletionTokens: 500, BYO: true},
			wantCost:   0,
			wantBilled: 250_000,
			wantMargin: 250_000,
		},
		{
			// Cache COGS is not separately configured: cache-read defaults to 10% of the
			// COGS input rate (2·0.10=0.2 ⇒ 200 nano/tok), cache-write to it (2000).
			// billed 25,000,000 + 2000·1000 + 100·10000 = 28,000,000 ;
			// cost   5,000,000  + 2000·200  + 100·2000  = 5,600,000.
			name:       "served, cache tokens ⇒ COGS cache defaulting",
			rec:        usageRecord{Model: "marginmodel", PromptTokens: 1000, CompletionTokens: 500, CacheReadTokens: 2000, CacheWriteTokens: 100},
			wantCost:   5_600_000,
			wantBilled: 28_000_000,
			wantMargin: 22_400_000,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := usageMargin(&tc.rec)
			if m.CostNano != tc.wantCost {
				t.Errorf("CostNano = %d, want %d", m.CostNano, tc.wantCost)
			}
			if m.BilledNano != tc.wantBilled {
				t.Errorf("BilledNano = %d, want %d", m.BilledNano, tc.wantBilled)
			}
			if m.MarginNano == nil || *m.MarginNano != tc.wantMargin {
				t.Errorf("MarginNano = %d, want %d", m.MarginNano, tc.wantMargin)
			}
			// Invariant: margin is always billed − cost, by construction.
			if m.MarginNano == nil || *m.MarginNano != m.BilledNano-m.CostNano {
				t.Errorf("MarginNano %d != BilledNano %d − CostNano %d", m.MarginNano, m.BilledNano, m.CostNano)
			}
			// BYO never charges COGS; the billed amount stays the fee path's output.
			if tc.rec.BYO && m.CostNano != 0 {
				t.Errorf("BYO CostNano = %d, want 0 (customer paid the upstream)", m.CostNano)
			}
		})
	}
}

// TestUsageMargin_BilledUnchangedFromBaseline proves the margin work did NOT alter the
// billed amount: BilledNano equals the pre-existing usageBilledNano(usageCostNano)
// exactly, for both a served and a BYO call. Margin is additive telemetry, never a
// change to what the customer pays.
func TestUsageMargin_BilledUnchangedFromBaseline(t *testing.T) {
	withMarginPricing(t)

	for _, rec := range []usageRecord{
		{Model: "marginmodel", PromptTokens: 1234, CompletionTokens: 567},
		{Model: "marginmodel", PromptTokens: 1234, CompletionTokens: 567, BYO: true},
	} {
		want := usageBilledNano(&rec, usageCostNano(&rec))
		if got := usageMargin(&rec).BilledNano; got != want {
			t.Errorf("BilledNano = %d, want %d (unchanged billed path); BYO=%v", got, want, rec.BYO)
		}
	}
}
