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
	"fmt"
	"math"
	"strconv"
	"strings"
)

// EXACT usage cost in nano-USD (1e-9), the anti-cents-flooring path. LLM prices are
// $/1M tokens, so the natural unit is integer nano-USD: a token's cost is
// tokens × (price/1e6 $) = tokens × (price × 1000 nano) with NO division and NO float
// rounding, so a 200-token $6.60/1M completion bills exactly 1,320,000 nano ($0.00132)
// instead of flooring to 0¢. The host parses the emitted decimal-USD string into
// atto-USD (1e-18) for the ledger — nano→atto is exact (×1e9), so nothing is skipped
// end to end. This is the money-of-record path; the cents helpers remain only for the
// analytics warehouse and the standalone HTTP billing queue.

// nanoPerToken converts a $/1M-token price to EXACT integer nano-USD per token:
// perMillion/1e6 dollars/token × 1e9 nano/dollar = perMillion × 1000. Model prices carry
// ≤3 decimals, so the product is integer and math.Round only clears float64's binary
// representation of the decimal literal — it is not an economic rounding.
func nanoPerToken(perMillion float64) int64 {
	if perMillion <= 0 {
		return 0
	}
	return int64(math.Round(perMillion * 1000))
}

// tokenNanoAt is the exact nano-USD token cost for the four per-million base rates,
// applying the shared cache-rate defaulting (cache-read = 10% of input when the model
// declares none, cache-write = input). It is the ONE place the token→nano arithmetic
// lives: both the billed-price path (tokenCostNano) and the provider-COGS path
// (tokenProviderCostNano) call it, so their cache math can never drift.
func tokenNanoAt(inPerM, outPerM, cacheReadPerM, cacheWritePerM float64, promptTokens, completionTokens, cacheReadTokens, cacheWriteTokens int) int64 {
	if cacheReadPerM == 0 && inPerM > 0 {
		cacheReadPerM = inPerM * 0.10
	}
	if cacheWritePerM == 0 {
		cacheWritePerM = inPerM
	}
	return int64(promptTokens)*nanoPerToken(inPerM) +
		int64(completionTokens)*nanoPerToken(outPerM) +
		int64(cacheReadTokens)*nanoPerToken(cacheReadPerM) +
		int64(cacheWriteTokens)*nanoPerToken(cacheWritePerM)
}

// tokenCostNano is the exact BILLED (customer-price) token cost in nano-USD, mirroring
// modelCostBreakdown's rate selection but in integer arithmetic so no sub-cent is lost.
func tokenCostNano(model string, promptTokens, completionTokens, cacheReadTokens, cacheWriteTokens int) int64 {
	price := getModelPrice(model)
	return tokenNanoAt(price.InputPerMillion, price.OutputPerMillion, price.CacheReadPerMillion, price.CacheWritePerMillion,
		promptTokens, completionTokens, cacheReadTokens, cacheWriteTokens)
}

// tokenProviderCostNano is the exact PROVIDER-COGS token cost in nano-USD: the same
// arithmetic as tokenCostNano at the model's COGS rates (which default to the price
// rates when no COGS is configured, giving cost == billed ⇒ margin 0). Cache COGS is
// not separately configured, so pass 0 and let tokenNanoAt default cache-read to 10%
// of the COGS input rate and cache-write to it — mirroring the price path exactly.
func tokenProviderCostNano(model string, promptTokens, completionTokens, cacheReadTokens, cacheWriteTokens int) int64 {
	price := getModelPrice(model)
	return tokenNanoAt(price.costInputPerMillion(), price.costOutputPerMillion(), 0, 0,
		promptTokens, completionTokens, cacheReadTokens, cacheWriteTokens)
}

// usageCostNano is the authoritative billable cost of a call in nano-USD — the exact twin
// of usageCostCents. Image/video bill per unit (their per-unit cents scaled to nano);
// everything else bills exactly per token.
func usageCostNano(record *usageRecord) int64 {
	switch {
	case record.VideoCount > 0:
		return videoCostCents(record.Model, record.VideoCount) * 10_000_000 // 1¢ = 1e7 nano
	case record.ImageCount > 0:
		return imageCostCents(record.Model, record.ImageCount) * 10_000_000
	default:
		return tokenCostNano(record.Model, record.PromptTokens, record.CompletionTokens,
			record.CacheReadTokens, record.CacheWriteTokens)
	}
}

// usageBilledNano is what Hanzo DEBITS in nano-USD: the full cost for a Hanzo-served
// call, or the 1% platform fee for a BYO call (ceil to the nano, so a fee is never
// rounded down to zero).
func usageBilledNano(record *usageRecord, costNano int64) int64 {
	if record.BYO {
		if costNano <= 0 {
			return 0
		}
		return (costNano + 99) / 100 // ceil(cost / 100) = 1% platform fee
	}
	return costNano
}

// costMargin is the money-of-record margin view of one call, all in integer nano-USD:
// BilledNano (what Hanzo debits the org), CostNano (Hanzo's provider COGS to serve it),
// and MarginNano = BilledNano − CostNano. Computed once (usageMargin) so the ledger, the
// cloud_usage row, and the o11y span read identical figures and can never drift.
type costMargin struct {
	CostNano   int64 // provider COGS to Hanzo (0 for BYO — the customer paid the upstream)
	BilledNano int64 // what Hanzo debits the org (full for served, 1% platform fee for BYO)
	MarginNano int64 // BilledNano − CostNano
}

// providerCostNano is Hanzo's cost of goods sold for a call, in nano-USD. A BYO call is
// 0 — the customer paid the upstream with their own key, so Hanzo carries no provider
// cost and the platform fee is pure margin. A Hanzo-served call is the per-unit cost for
// image/video (whose COGS equals their price today ⇒ zero margin) or the token cost at
// COGS rates otherwise (which default to price ⇒ zero margin unless a real COGS is set).
func providerCostNano(record *usageRecord) int64 {
	if record.BYO {
		return 0
	}
	switch {
	case record.VideoCount > 0:
		return videoCostCents(record.Model, record.VideoCount) * 10_000_000 // 1¢ = 1e7 nano
	case record.ImageCount > 0:
		return imageCostCents(record.Model, record.ImageCount) * 10_000_000
	default:
		return tokenProviderCostNano(record.Model, record.PromptTokens, record.CompletionTokens,
			record.CacheReadTokens, record.CacheWriteTokens)
	}
}

// usageMargin computes the billed/cost/margin trio for a call in nano-USD from the ONE
// billed path (usageBilledNano ∘ usageCostNano) and the provider COGS (providerCostNano).
// MarginNano is Hanzo's gross margin on the call: for a Hanzo-served call it is
// price − COGS; for a BYO call CostNano is 0 so the margin is exactly the platform fee
// (the billed amount itself is unchanged — still the 1% fee).
func usageMargin(record *usageRecord) costMargin {
	billed := usageBilledNano(record, usageCostNano(record))
	cost := providerCostNano(record)
	// A self-billing subsystem (zen's commerce Meter) knows its EXACT billed
	// amount + upstream COGS per served tier; prefer those over the table
	// recompute so the warehouse margin is true, not approximated.
	if record.BilledNanoExact > 0 {
		billed = record.BilledNanoExact
	}
	if record.CostNanoExact > 0 {
		cost = record.CostNanoExact
	}
	return costMargin{CostNano: cost, BilledNano: billed, MarginNano: billed - cost}
}

// nanoToUSD renders a signed nano-USD amount as an EXACT decimal USD string ("0.00132"),
// the value carried across the billing seam.
func nanoToUSD(nano int64) string {
	if nano == 0 {
		return "0"
	}
	neg := nano < 0
	if neg {
		nano = -nano
	}
	s := strconv.FormatInt(nano/1_000_000_000, 10)
	if frac := nano % 1_000_000_000; frac > 0 {
		s += "." + strings.TrimRight(fmt.Sprintf("%09d", frac), "0")
	}
	if neg {
		s = "-" + s
	}
	return s
}

// usageBilledUSD is the exact decimal-USD amount for the native finance debit.
func usageBilledUSD(record *usageRecord) string {
	return nanoToUSD(usageBilledNano(record, usageCostNano(record)))
}
