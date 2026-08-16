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
	// A route the vendor charges nothing for costs nothing, whatever a price table
	// would guess for a model id it has never seen. It is stated HERE because every
	// reader of the money — the ledger debit, the warehouse row and the gen_ai span —
	// asks this one function, which is what keeps them agreeing.
	case record.Free:
		return 0
	case record.VideoCount > 0:
		return videoCostCents(record.Model, record.VideoCount) * 10_000_000 // 1¢ = 1e7 nano
	case record.ImageCount > 0:
		return imageCostCents(record.Model, record.ImageCount) * 10_000_000
	case recordIsAudio(record):
		return audioCostCents(record) * 10_000_000
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
	// MarginNano is BilledNano − CostNano, and it is NIL when that subtraction has
	// no meaning. An unpriced model has no configured rate, so the COGS leg is the
	// conservative default rather than a cost anyone paid; subtracting an invented
	// number from a real billed amount yields a figure that reads as a heavy loss
	// and is not one. Better to report no margin than a fabricated one.
	MarginNano *int64
}

// providerCostNano is Hanzo's cost of goods sold for a call, in nano-USD. A BYO call is
// 0 — the customer paid the upstream with their own key, so Hanzo carries no provider
// cost and the platform fee is pure margin. A Hanzo-served call is the per-unit cost for
// image/video (whose COGS equals their price today ⇒ zero margin) or the token cost at
// COGS rates otherwise (which default to price ⇒ zero margin unless a real COGS is set).
func providerCostNano(record *usageRecord) int64 {
	if record.BYO || record.Free {
		return 0
	}
	switch {
	case record.VideoCount > 0:
		return videoCostCents(record.Model, record.VideoCount) * 10_000_000 // 1¢ = 1e7 nano
	case record.ImageCount > 0:
		return imageCostCents(record.Model, record.ImageCount) * 10_000_000
	case recordIsAudio(record):
		// Speech runs on hardware we already own, so there is no upstream invoice
		// to pass through: COGS is 0 and the margin on an audio call is the whole
		// price. Stated explicitly rather than reached by falling through to token
		// math that happens to yield 0 on a record with no tokens — the two agree
		// today by accident, and only one of them is the reason.
		return 0
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
	// amount + upstream COGS per served tier; those ARE the numbers, so the table
	// recompute is not consulted. Presence, not positivity: a turn that billed
	// exactly nothing said so, and must not be handed a price it never charged.
	if record.BilledNanoExact != nil {
		billed = *record.BilledNanoExact
	}
	if record.CostNanoExact != nil {
		cost = *record.CostNanoExact
	}
	m := costMargin{CostNano: cost, BilledNano: billed}
	// A caller that STATED its COGS has a real one, unpriced model or not.
	if record.CostNanoExact != nil || !recordUnpriced(record) {
		margin := billed - cost
		m.MarginNano = &margin
	}
	return m
}

// nanoToCents rounds a nano-USD amount to the nearest cent (1¢ = 1e7 nano). It is how
// the cent-precision surfaces — the analytics ledger, the span's total, the standalone
// billing queue — read the exact money, so none of them computes a cost of its own.
// Half a cent rounds up; a negative amount has no meaning as a cost and reads as zero.
func nanoToCents(nano int64) int64 {
	if nano <= 0 {
		return 0
	}
	return (nano + 5_000_000) / 10_000_000
}

// usdToNano converts a decimal-USD amount to integer nano-USD — the inverse of
// nanoToUSD, for a caller that already knows the exact dollars it billed and needs
// to report them on the nano money ledger.
func usdToNano(usd float64) int64 {
	return int64(math.Round(usd * 1_000_000_000))
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

// usageBilledUSD is the exact decimal-USD amount for the native finance debit. It
// reads usageMargin — the ONE billed path — rather than recomputing the table leg of
// it, so a caller that knows its exact billed amount is debited THAT and not a
// rate-table guess, and the invoice, the warehouse row and the span all carry one
// number.
func usageBilledUSD(record *usageRecord) string {
	return nanoToUSD(usageMargin(record).BilledNano)
}

// usageFree reports whether a call was served at a price of ZERO — the class a wallet
// has nothing to refuse, and therefore the class the plan allowance counts.
//
// "THE NUMBER CAME OUT ZERO" IS A DIFFERENT QUESTION AND GETS A DIFFERENT ANSWER, so
// three things have to be true:
//
//   - It billed nothing. The wallet's question, read from the same margin
//     usageBilledUSD renders, so free and $0 are one answer and not two that drift.
//
//   - The zero is a PRICE and not a missing one. A model whose rate is not set yet —
//     transcription and synthesis are exactly that, deliberately (see
//     sttPricePerMinuteCents) — computes zero and is UNPRICED, not free. Counting it
//     would spend a customer's free calls on traffic we have simply not billed yet,
//     and empty their day on the work they are paying for.
//
//   - The route is free by DECLARATION or by the TABLE. costsNothing is the same
//     question the balance gate asks before the call, so the gate and the counter
//     bound the same set of calls; record.Free is a route the vendor charges nothing
//     for — the free pool and the spare routes — which states its own zero and
//     appears in no table under the id that served it.
//
// The third is what keeps a vendor's failure from being read as a gift: an upstream
// that answers an error carries no tokens, so a PRICED model can compute zero, and
// only asking what the route costs tells the two apart.
func usageFree(record *usageRecord) bool {
	return usageMargin(record).BilledNano == 0 &&
		!recordUnpriced(record) &&
		(record.Free || costsNothing(record.Model, record.Owner))
}
