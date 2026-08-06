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

// TestRecordUnpriced pins the honest-marking policy: a token-billed call for a model
// with no configured price is flagged unpriced (it billed at the default), a configured
// model is not, and image/video calls (own per-unit pricing) are never flagged.
func TestRecordUnpriced(t *testing.T) {
	withMarginPricing(t) // "marginmodel" priced; unknown ids fall to the default.

	cases := []struct {
		name string
		rec  usageRecord
		want bool
	}{
		{"unknown token model ⇒ flagged", usageRecord{Model: "ghost-model", PromptTokens: 100, CompletionTokens: 10}, true},
		{"configured model ⇒ not flagged", usageRecord{Model: "marginmodel", PromptTokens: 100, CompletionTokens: 10}, false},
		{"unknown image model ⇒ never flagged", usageRecord{Model: "ghost-image", ImageCount: 1}, false},
		{"unknown video model ⇒ never flagged", usageRecord{Model: "ghost-video", VideoCount: 1}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := recordUnpriced(&tc.rec); got != tc.want {
				t.Errorf("recordUnpriced = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestUnpricedBillingUnchanged proves flagging did NOT change what an unknown model
// bills: getModelPrice still returns the conservative $1/$4 default (the exact
// pre-existing behavior), so the debit is identical — only the row is now honest.
func TestUnpricedBillingUnchanged(t *testing.T) {
	withMarginPricing(t)

	p, ok := getModelPriceForOrgOK("ghost-model", "")
	if ok {
		t.Fatal("unknown model reported ok=true — should be flagged unpriced")
	}
	if p.InputPerMillion != 1.00 || p.OutputPerMillion != 4.00 {
		t.Errorf("unknown model price = %v, want the unchanged default {1.00, 4.00}", p)
	}
	// getModelPrice (the billing entry point) returns the SAME default, silently ok.
	if bp := getModelPrice("ghost-model"); bp != p {
		t.Errorf("getModelPrice = %v, want %v (identical billed default)", bp, p)
	}
}

// TestUnpricedSpanAttr proves the span carries gen_ai.hanzo.priced=false ONLY when the
// row is flagged unpriced, and omits it otherwise (priced assumed true).
func TestUnpricedSpanAttr(t *testing.T) {
	flagged := &usageRecord{Owner: "acme", Model: "ghost-model", Status: "success", Unpriced: true}
	m := attrMap(buildGenAISpanFields(flagged, 0, 0, 0, usdPtr(0), nil, false).attrs)
	got, ok := m["gen_ai.hanzo.priced"]
	if !ok {
		t.Fatal("gen_ai.hanzo.priced missing on an unpriced row")
	}
	if got.AsBool() {
		t.Errorf("gen_ai.hanzo.priced = true, want false")
	}

	priced := &usageRecord{Owner: "acme", Model: "marginmodel", Status: "success"}
	m = attrMap(buildGenAISpanFields(priced, 0, 0, 0, usdPtr(0), nil, false).attrs)
	if _, ok := m["gen_ai.hanzo.priced"]; ok {
		t.Error("gen_ai.hanzo.priced must be absent for a normally-priced row")
	}
}
