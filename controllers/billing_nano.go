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

// tokenCostNano is the exact token cost in nano-USD, mirroring modelCostBreakdown's rate
// selection (cache-read defaults to 10% of input, cache-write to input) but in integer
// arithmetic so no sub-cent is ever lost.
func tokenCostNano(model string, promptTokens, completionTokens, cacheReadTokens, cacheWriteTokens int) int64 {
	price := getModelPrice(model)
	cacheReadRate := price.CacheReadPerMillion
	if cacheReadRate == 0 && price.InputPerMillion > 0 {
		cacheReadRate = price.InputPerMillion * 0.10
	}
	cacheWriteRate := price.CacheWritePerMillion
	if cacheWriteRate == 0 {
		cacheWriteRate = price.InputPerMillion
	}
	return int64(promptTokens)*nanoPerToken(price.InputPerMillion) +
		int64(completionTokens)*nanoPerToken(price.OutputPerMillion) +
		int64(cacheReadTokens)*nanoPerToken(cacheReadRate) +
		int64(cacheWriteTokens)*nanoPerToken(cacheWriteRate)
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
