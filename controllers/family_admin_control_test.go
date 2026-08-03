// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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
	"testing"

	"github.com/hanzoai/ai/object"
)

// TestFamilyProviderFnsCoverEveryFamily is the guard for the bug that has now
// been written twice: a family added to modelFamilies but missing from a
// hand-written name ladder elsewhere. The first time it was
// familyForProviderType's switch, and OpenRouter's models were listed and served
// AROUND the funding gate. The second time it was GetModelProviderByName's
// `if name == "zen"` ladder, and `openrouter` resolved against the seed row
// instead of its family.
//
// Both ladders are now derived from a list. This asserts the two lists agree, so
// a third occurrence fails here instead of in production.
func TestFamilyProviderFnsCoverEveryFamily(t *testing.T) {
	known := map[string]bool{}
	for _, n := range object.FamilyProviderNames() {
		known[n] = true
	}

	for _, f := range modelFamilies {
		if !known[f.name] {
			t.Errorf("family %q is in modelFamilies but not in object.familyProviderFns — "+
				"GetModelProviderByName(%q) will resolve against the DB instead of the family",
				f.name, f.name)
		}
	}

	// And nothing resolves as a family that is not one.
	names := map[string]bool{}
	for _, f := range modelFamilies {
		names[f.name] = true
	}
	for n := range known {
		if !names[n] {
			t.Errorf("object.familyProviderFns names %q, which is not a modelFamilies entry", n)
		}
	}
}

// TestFamilyGateAgreesWithServingPath: whatever address serves a family must be
// the address that decides whether it is listed. They used to be read from two
// places — pipeToFamily through providerFn (admin row first), listing through
// conf directly — so a family disabled in the console stayed listed and served.
func TestFamilyGateAgreesWithServingPath(t *testing.T) {
	for _, f := range modelFamilies {
		if f.providerFn == nil {
			t.Errorf("family %q has no providerFn: its gate and its serving path cannot agree", f.name)
			continue
		}
		prov := f.providerFn()
		if prov == nil {
			if f.enabled() {
				t.Errorf("family %q reports enabled but resolves no provider — it would be listed and unservable", f.name)
			}
			continue
		}
		if !f.enabled() {
			t.Errorf("family %q resolves a provider (%s) but reports disabled — it would be servable and unlisted",
				f.name, prov.ProviderUrl)
		}
	}
}
