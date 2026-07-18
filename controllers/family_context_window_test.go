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
	"time"
)

// The production families pin their flagship SKUs to the 1M served window, and
// enso-pro exists as a pinned SKU (it mirrors zen5-pro) so /v1/models lists and
// routes it before the family advertises it.
func TestFamilyFlagshipPins(t *testing.T) {
	want := map[*modelFamily]map[string]int{
		zenFam:  {"zen5": flagshipWindow, "zen5-coder": flagshipWindow, "zen5-pro": flagshipWindow},
		ensoFam: {"enso": flagshipWindow, "enso-pro": flagshipWindow, "enso-ultra": flagshipWindow},
	}
	for fam, pins := range want {
		got := map[string]int{}
		for _, p := range fam.pins {
			got[p.id] = p.contextWindow
		}
		for id, w := range pins {
			if got[id] != w {
				t.Errorf("%s pin %q = %d, want %d", fam.name, id, got[id], w)
			}
		}
	}
	// enso-pro is the NEW SKU; it must be a pinned enso SKU.
	found := false
	for _, p := range ensoFam.pins {
		if p.id == "enso-pro" {
			found = true
		}
	}
	if !found {
		t.Fatal("enso-pro must be a pinned enso SKU")
	}
}

// mergeModels stamps each SKU with its served context window: a pinned flagship
// reports 1M (overriding a smaller discovered window), an unpinned flash/mini keeps
// its real discovered window, and a pinned-but-undiscovered SKU (enso-pro) is
// synthesized at its guaranteed window — all only for a CONFIGURED family.
func TestMergeModelsContextWindow(t *testing.T) {
	t.Setenv("TEST_FAM_ENSO_URL", "http://enso.invalid") // makes enabled() true

	fam := &modelFamily{
		name: "enso", prefix: "enso", owner: "hanzo", urlKey: "TEST_FAM_ENSO_URL",
		pins: []pinnedSKU{
			{id: "enso", contextWindow: flagshipWindow},
			{id: "enso-pro", contextWindow: flagshipWindow},
			{id: "enso-ultra", contextWindow: flagshipWindow},
		},
	}
	// Discovery advertises enso (gated, at a smaller window) and enso-flash (its real
	// served window). enso-ultra and enso-pro are NOT advertised yet.
	fam.byID = map[string]zenModel{
		"enso":       {ID: "enso", MaxCtx: 262144, Access: "waitlist"},
		"enso-flash": {ID: "enso-flash", MaxCtx: 131072},
	}
	fam.ids = []string{"enso", "enso-flash"}
	fam.loaded = true
	fam.fetchedAt = time.Now() // fresh — snapshot() skips the network refresh

	byID := indexModels(fam.mergeModels(nil))

	// Flagship pin overrides discovery's smaller window.
	if m := byID["enso"]; m.ContextWindow != flagshipWindow {
		t.Errorf("enso context_window = %d, want %d (pin overrides discovery)", m.ContextWindow, flagshipWindow)
	}
	if a := byID["enso"].Access; a == nil || a.State != "waitlist" {
		t.Errorf("enso must keep its gated standing, got %+v", a)
	}
	// flash keeps its real served window (NOT bumped).
	if m := byID["enso-flash"]; m.ContextWindow != 131072 {
		t.Errorf("enso-flash context_window = %d, want 131072 (unpinned, keep discovered)", m.ContextWindow)
	}
	// enso-ultra pinned + advertised nowhere → synthesized at 1M.
	if m, ok := byID["enso-ultra"]; !ok || m.ContextWindow != flagshipWindow {
		t.Errorf("enso-ultra should be listed at %d, got %+v ok=%v", flagshipWindow, m, ok)
	}
	// enso-pro is the NEW SKU (mirrors zen5-pro): synthesized, 1M, premium, ungated.
	pro, ok := byID["enso-pro"]
	if !ok {
		t.Fatal("enso-pro must appear in /v1/models")
	}
	if pro.ContextWindow != flagshipWindow || !pro.Premium || pro.OwnedBy != "hanzo" || pro.Access != nil {
		t.Errorf("enso-pro = %+v, want {context_window:%d, premium:true, owned_by:hanzo, access:nil}", pro, flagshipWindow)
	}
}

// A bare (unconfigured) family synthesizes NO pinned SKU — it lists only what its
// snapshot holds — so the gated-annotation invariant (exactly one model) is intact.
func TestMergeModelsBareFamilyNoPinSynthesis(t *testing.T) {
	fam := &modelFamily{
		name: "enso", prefix: "enso", // no urlKey → enabled() is false
		pins: []pinnedSKU{
			{id: "enso", contextWindow: flagshipWindow},
			{id: "enso-pro", contextWindow: flagshipWindow},
			{id: "enso-ultra", contextWindow: flagshipWindow},
		},
	}
	fam.byID = map[string]zenModel{"enso": {ID: "enso", Access: "waitlist"}}
	fam.ids = []string{"enso"}
	fam.loaded = true

	out := fam.mergeModels(nil)
	if len(out) != 1 {
		t.Fatalf("bare family must synthesize no pins: got %d models, want 1", len(out))
	}
	// A bare family also reports no pinned window (enabled() is false).
	if out[0].ContextWindow != 0 {
		t.Errorf("bare family must not pin a window, got %d", out[0].ContextWindow)
	}
}
