// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package model

import "testing"

// TestUnknownModelIsUnpricedNotAnError: the table lists what OpenAI retails, so a
// model served from our own hardware or a vendor's free tier is never in it. That
// used to fail the request AFTER the call had already succeeded upstream.
func TestUnknownModelIsUnpricedNotAnError(t *testing.T) {
	for _, m := range []string{
		"default",                          // the local engine's own id
		"meta-llama/Llama-3.3-70B-Instruct", // a Hugging Face route
		"z-ai/glm-5.2:free",                 // an OpenRouter free SKU
	} {
		r := &ModelResult{PromptTokenCount: 10, ResponseTokenCount: 5}
		if err := CalculateOpenAIModelPrice(m, r, "en"); err != nil {
			t.Fatalf("%s: %v — an unpriceable model must not fail the request", m, err)
		}
		if !r.Unpriced {
			t.Fatalf("%s: Unpriced = false; it would bill as costing nothing", m)
		}
		if r.TotalPrice != 0 {
			t.Fatalf("%s: TotalPrice = %v, want 0", m, r.TotalPrice)
		}
	}
}

// TestKnownModelStaysPriced is the regression guard. Unpriced defaults to false so
// that every client which never sets it keeps billing exactly as before; a known
// model must never come back flagged.
func TestKnownModelStaysPriced(t *testing.T) {
	for _, m := range []string{"gpt-4o", "gpt-5", "o3-mini"} {
		r := &ModelResult{PromptTokenCount: 1000, ResponseTokenCount: 1000}
		if err := CalculateOpenAIModelPrice(m, r, "en"); err != nil {
			t.Fatalf("%s: %v", m, err)
		}
		if r.Unpriced {
			t.Fatalf("%s: flagged unpriced — billing would stop for a retail model", m)
		}
		if r.TotalPrice <= 0 {
			t.Fatalf("%s: TotalPrice = %v, want > 0", m, r.TotalPrice)
		}
		if r.Currency != "USD" {
			t.Fatalf("%s: Currency = %q, want USD", m, r.Currency)
		}
	}
}
