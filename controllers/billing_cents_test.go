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

// TestNanoToCents pins the rounding, because it is money and the boundary is where
// the old path went wrong: it raised anything that rounded to nothing up to a whole
// cent, which on a $0.0002 call is fifty times the price.
func TestNanoToCents(t *testing.T) {
	for _, c := range []struct {
		nano int64
		want int64
		why  string
	}{
		{0, 0, "nothing costs nothing"},
		{1, 0, "a tenth of a millionth of a cent is not a cent"},
		{4_999_999, 0, "just under half a cent rounds down"},
		{5_000_000, 1, "half a cent rounds up"},
		{10_000_000, 1, "a cent is a cent"},
		{14_999_999, 1, ""},
		{15_000_000, 2, ""},
		{1_320_000, 0, "the $0.00132 completion the nano ledger exists for"},
		{34_694_637_345, 3469, "ten days of measured spend, to the cent"},
		{-1, 0, "a negative amount has no meaning as a cost"},
	} {
		if got := nanoToCents(c.nano); got != c.want {
			t.Errorf("nanoToCents(%d) = %d, want %d — %s", c.nano, got, c.want, c.why)
		}
	}
}

// TestCentsProjectExactCost: the cents column and the nano column describe the same
// call, so they must be the same money. Two computations over the same inputs could
// disagree, and did — by $8.03 on $34.69 across ten days of the live ledger, because
// one of them invented a cent whenever the exact price rounded to nothing.
func TestCentsProjectExactCost(t *testing.T) {
	withMarginPricing(t)

	for _, r := range []*usageRecord{
		{Model: "marginmodel", PromptTokens: 1, CompletionTokens: 1},
		{Model: "marginmodel", PromptTokens: 1000, CompletionTokens: 500},
		{Model: "marginmodel", PromptTokens: 1_000_000, CompletionTokens: 250_000},
		{Model: "zeromargin", PromptTokens: 700, CacheReadTokens: 3000},
		{Model: "gpt-image-1", ImageCount: 2},
		{Model: "wan2-2-t2v-a14b", VideoCount: 1},
	} {
		if got, want := usageCostCents(r), nanoToCents(usageCostNano(r)); got != want {
			t.Errorf("%s: cents %d, exact %d nano ⇒ %d — the row disagrees with itself",
				r.Model, got, usageCostNano(r), want)
		}
	}

	// The per-unit paths must survive the projection intact: image and video prices
	// are whole cents, so rounding them through nano returns them unchanged. This is
	// the regression the ledger-cost authority was introduced to stop.
	img := &usageRecord{Model: "gpt-image-1", ImageCount: 2}
	if got, want := usageCostCents(img), imageCostCents(img.Model, 2); got != want || got <= 0 {
		t.Errorf("image cost %d, want %d (>0)", got, want)
	}
}
