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
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/decimal"
)

// The production families guarantee their flagship SKUs at the 1M served window.
// enso-pro is deliberately NOT among them: the enso service serves enso, enso-flash
// and enso-ultra and answers 404 for enso-pro, and ai must never guarantee a window
// for a SKU no family serves.
func TestFamilyFlagshipWindows(t *testing.T) {
	want := map[*modelFamily]map[string]int{
		zenFam:  {"zen5": flagshipWindow, "zen5-coder": flagshipWindow, "zen5-pro": flagshipWindow},
		ensoFam: {"enso": flagshipWindow, "enso-ultra": flagshipWindow},
	}
	for fam, windows := range want {
		for id, w := range windows {
			if fam.windows[id] != w {
				t.Errorf("%s window %q = %d, want %d", fam.name, id, fam.windows[id], w)
			}
		}
	}
	if _, ok := ensoFam.windows["enso-pro"]; ok {
		t.Error("enso-pro must not be a guaranteed enso window: the family answers 404 for it")
	}
}

// mergeModels stamps each DISCOVERED SKU with its served context window: a flagship
// reports its guaranteed 1M (overriding a smaller discovered window) and an unpinned
// flash keeps its real discovered window. A SKU that discovery does not advertise is
// NOT listed, however it is configured — a listing that 404s upstream is worse than
// an absent one.
func TestMergeModelsListsOnlyDiscovered(t *testing.T) {
	t.Setenv("TEST_FAM_ENSO_URL", "http://enso.invalid") // makes enabled() true

	fam := &modelFamily{
		name: "enso", prefix: "enso", owner: "hanzo", urlKey: "TEST_FAM_ENSO_URL",
		windows: map[string]int{
			"enso":       flagshipWindow,
			"enso-ultra": flagshipWindow,
			"enso-pro":   flagshipWindow, // configured but never discovered
		},
	}
	// Discovery advertises enso (gated, at a smaller window) and enso-flash (its real
	// served window). enso-ultra and enso-pro are NOT advertised.
	fam.byID = map[string]zenModel{
		"enso":       {ID: "enso", MaxCtx: 262144, Access: "waitlist"},
		"enso-flash": {ID: "enso-flash", MaxCtx: 131072},
	}
	fam.ids = []string{"enso", "enso-flash"}
	fam.loaded = true
	fam.fetchedAt = time.Now() // fresh — snapshot() skips the network refresh

	out := fam.mergeModels(nil)
	byID := indexModels(out)

	// Exactly what discovery advertised — nothing synthesized.
	if len(out) != 2 {
		t.Fatalf("mergeModels listed %d models, want 2 (only what discovery advertised): %+v", len(out), out)
	}
	// Flagship window guarantee overrides discovery's smaller window.
	if m := byID["enso"]; m.ContextWindow != flagshipWindow {
		t.Errorf("enso context_window = %d, want %d (guarantee overrides discovery)", m.ContextWindow, flagshipWindow)
	}
	if a := byID["enso"].Access; a == nil || a.State != "waitlist" {
		t.Errorf("enso must keep its gated standing, got %+v", a)
	}
	// flash keeps its real served window (NOT bumped).
	if m := byID["enso-flash"]; m.ContextWindow != 131072 {
		t.Errorf("enso-flash context_window = %d, want 131072 (no guarantee, keep discovered)", m.ContextWindow)
	}
	// A configured-but-undiscovered SKU must not be listed, at any window.
	if _, ok := byID["enso-ultra"]; ok {
		t.Error("enso-ultra was not discovered and must not be listed")
	}
	if _, ok := byID["enso-pro"]; ok {
		t.Error("enso-pro was not discovered and must not be listed — that is the phantom SKU bug")
	}
}

// A bare (unconfigured) family lists only what its snapshot holds and guarantees no
// window, so the gated-annotation invariant (exactly one model) is intact.
func TestMergeModelsBareFamilyNoWindow(t *testing.T) {
	fam := &modelFamily{
		name: "enso", prefix: "enso", // no urlKey -> enabled() is false
		windows: map[string]int{
			"enso":       flagshipWindow,
			"enso-ultra": flagshipWindow,
		},
	}
	fam.byID = map[string]zenModel{"enso": {ID: "enso", Access: "waitlist"}}
	fam.ids = []string{"enso"}
	fam.loaded = true

	out := fam.mergeModels(nil)
	if len(out) != 1 {
		t.Fatalf("bare family must list only its snapshot: got %d models, want 1", len(out))
	}
	// A bare family also guarantees no window (enabled() is false).
	if out[0].ContextWindow != 0 {
		t.Errorf("bare family must not guarantee a window, got %d", out[0].ContextWindow)
	}
}

// Premium is a claim about money, so the listing takes it from the price. A route
// the family charges nothing for reported as premium reads to a client as a SKU
// their plan cannot afford — the opposite of what is true, and the reason a free
// route would never be offered by a picker that filters on it.
func TestMergeModelsReadsPremiumFromThePrice(t *testing.T) {
	fam := &modelFamily{name: "openrouter", prefix: "openrouter/", owner: "openrouter"}
	fam.byID = map[string]zenModel{
		"vendor/paid": {ID: "vendor/paid", MaxCtx: 128000, Base: zenTier{In: decimal.New(3, 0), Out: decimal.New(15, 0)}},
		"vendor/free": {ID: "vendor/free", MaxCtx: 128000},
	}
	fam.ids = []string{"vendor/paid", "vendor/free"}
	fam.loaded = true
	fam.fetchedAt = time.Now()

	byID := indexModels(fam.mergeModels(nil))
	if !byID["vendor/paid"].Premium {
		t.Error("a SKU the family charges for is not premium")
	}
	if byID["vendor/free"].Premium {
		t.Error("a SKU the family charges nothing for is premium")
	}
}

// TestMergeModelsListsOneRowPerID is the invariant /v1/models is keyed by: an id
// already in the base list is REPLACED, never appended beside itself.
//
// The empty-lineup path is where it broke. A family with nothing discovered still
// publishes its free id, and that append had no dedupe — harmless while the free
// pool was empty, because freeInfo returns nothing then and nothing was appended,
// and three duplicate rows the moment a route existed. Enabling our own compute
// (ENGINE_URL) is exactly what makes one exist, so the bug surfaced as "turning on
// the engine duplicates the free SKUs" rather than as anything about listing.
func TestMergeModelsListsOneRowPerID(t *testing.T) {
	t.Setenv("TEST_FAM_ENGINE_URL", "http://engine.invalid") // a route exists, so freeInfo speaks
	prev := engineFam
	engineFam = &modelFamily{
		name: "engine", prefix: "engine/", owner: "hanzo", urlKey: "TEST_FAM_ENGINE_URL",
	}
	t.Cleanup(func() { engineFam = prev })

	fam := &modelFamily{
		name: "zen", prefix: "zen", owner: "zenlm", urlKey: "TEST_FAM_ZEN_URL",
		freeName: "zen-free",
	}
	fam.loaded = true
	fam.fetchedAt = time.Now() // fresh, and byID is empty — the empty-lineup path

	// The base already carries the id, as it does when an earlier family or the
	// priced catalog listed it.
	base := []modelInfo{{ID: "zen-free", Object: "model", OwnedBy: "hanzo"}}
	out := fam.mergeModels(base)

	seen := map[string]int{}
	for _, m := range out {
		seen[strings.ToLower(m.ID)]++
	}
	for id, n := range seen {
		if n > 1 {
			t.Errorf("%q listed %d times — /v1/models is keyed by id, so a second row is a second answer", id, n)
		}
	}
}
