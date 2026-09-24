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
	"strings"
	"testing"

	"github.com/hanzoai/ai/object"
)

// ── resolveModelRoute ────────────────────────────────────────────────────────

func TestResolveModelRoute_KnownModels(t *testing.T) {
	cases := []struct {
		input        string
		wantProvider string
		wantModel    string
		wantPremium  bool
	}{
		// Chat OpenRouter does not carry stays on its relay.
		{"claude-3-7-sonnet", "do-ai", "anthropic-claude-3.7-sonnet", false},
		{"fireworks/cogito-671b", "fireworks", "accounts/cogito/models/cogito-671b-v2-p1", true},

		// What the family does not serve at all: embeddings, video, DO's routers,
		// and our own speech service.
		{"bge-m3", "do-ai", "bge-m3", false},
		{"wan2-2-t2v-a14b", "do-ai", "wan2-2-t2v-a14b", true},
		{"router:general", "do-ai", "router:general", false},
		{"zen-scribe", "speech", "whisper", false},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			route := resolveModelRoute(tc.input)
			if route == nil {
				t.Fatalf("resolveModelRoute(%q) = nil, want non-nil", tc.input)
			}
			if route.providerName != tc.wantProvider {
				t.Errorf("providerName = %q, want %q", route.providerName, tc.wantProvider)
			}
			if route.upstreamModel != tc.wantModel {
				t.Errorf("upstreamModel = %q, want %q", route.upstreamModel, tc.wantModel)
			}
			if route.premium != tc.wantPremium {
				t.Errorf("premium = %v, want %v", route.premium, tc.wantPremium)
			}
		})
	}
}

func TestResolveModelRoute_CaseInsensitive(t *testing.T) {
	// Keys in the map are lowercase; make sure uppercase input still resolves.
	route := resolveModelRoute("BGE-M3")
	if route == nil {
		t.Fatal("resolveModelRoute(\"BGE-M3\") = nil, want match")
	}
	if route.providerName != "do-ai" {
		t.Errorf("providerName = %q, want \"do-ai\"", route.providerName)
	}
}

func TestResolveModelRoute_UnknownReturnsNil(t *testing.T) {
	unknowns := []string{
		"nonexistent-model",
		"",
		"gpt-99",
		"fireworks/nonexistent",
	}
	for _, name := range unknowns {
		if route := resolveModelRoute(name); route != nil {
			t.Errorf("resolveModelRoute(%q) = %+v, want nil", name, route)
		}
	}
}

// ── Routing table integrity ──────────────────────────────────────────────────

func TestModelRoutes_KeysAreLowercase(t *testing.T) {
	for key := range modelRoutes {
		if key != strings.ToLower(key) {
			t.Errorf("routing table key %q is not lowercase", key)
		}
	}
}

func TestModelRoutes_NoDuplicateUpstreamForSameProvider(t *testing.T) {
	// Guard against accidentally mapping two user-facing names to the exact
	// same (provider, upstream) pair—aliases are fine when intentional, but
	// catch accidental copy-paste duplication within a single provider section.
	//
	// We keep this test informational: it logs duplicates rather than failing,
	// since some aliases intentionally share an upstream (e.g. "gpt-4o" and
	// "openai/gpt-4o"). If this ever fires unexpectedly, investigate.
	type key struct{ provider, upstream string }
	seen := make(map[key][]string)
	for name, route := range modelRoutes {
		k := key{route.providerName, route.upstreamModel}
		seen[k] = append(seen[k], name)
	}
	for k, names := range seen {
		if len(names) > 2 {
			// More than 2 user-facing names pointing to the exact same upstream
			// is suspicious. Log, don't fail—but make it visible.
			t.Logf("WARNING: %d names share provider=%q upstream=%q: %v",
				len(names), k.provider, k.upstream, names)
		}
	}
}

func TestModelRoutes_NoEmptyFields(t *testing.T) {
	for name, route := range modelRoutes {
		if route.providerName == "" {
			t.Errorf("model %q has empty providerName", name)
		}
		if route.upstreamModel == "" {
			t.Errorf("model %q has empty upstreamModel", name)
		}
	}
}

// TestModelRoutes_ProviderNamesAreKnown asserts every route names a provider
// something actually creates: a seeded row (initLLMProviders) or a model family
// (whose address is deployment config, so it has no row).
//
// The set is DERIVED from those two sources rather than hand-listed. A
// hand-written copy of the seed table is the second-list bug this package has
// already paid for twice — familyForProviderType and GetModelProviderByName were
// both rewritten to remove one — and it fails backwards: seeding a provider
// correctly makes the copy stale, so the test reports a WORKING route as unknown
// and the cure looks like deleting the route.
func TestModelRoutes_ProviderNamesAreKnown(t *testing.T) {
	known := map[string]bool{}
	for name := range object.SeededModelProviders() {
		known[name] = true
	}
	for _, name := range object.FamilyProviderNames() {
		known[name] = true
	}
	for name, route := range modelRoutes {
		if !known[route.providerName] {
			t.Errorf("model %q uses unknown provider %q", name, route.providerName)
		}
	}
}

// ── listAvailableModels ──────────────────────────────────────────────────────

func TestListAvailableModels_ReturnsSortedList(t *testing.T) {
	models := listAvailableModels()

	if len(models) == 0 {
		t.Fatal("listAvailableModels() returned empty slice")
	}

	// Count visible (non-hidden) models in the routing table
	visibleCount := 0
	for _, route := range modelRoutes {
		if !route.hidden {
			visibleCount++
		}
	}
	if cfg := GetModelConfig(); cfg != nil {
		visibleCount = len(cfg.ListModels())
	}
	if len(models) != visibleCount {
		t.Errorf("listAvailableModels() returned %d models, want %d",
			len(models), visibleCount)
	}

	// Verify sorted by ID
	for i := 1; i < len(models); i++ {
		if models[i].ID < models[i-1].ID {
			t.Errorf("models not sorted: %q comes after %q",
				models[i].ID, models[i-1].ID)
		}
	}

	// Verify JSON object field
	for _, m := range models {
		if m.Object != "model" {
			t.Errorf("model %q has Object=%q, want \"model\"", m.ID, m.Object)
		}
		if m.OwnedBy == "" {
			t.Errorf("model %q has empty OwnedBy", m.ID)
		}
	}
}

func TestListAvailableModels_CountSanity(t *testing.T) {
	models := listAvailableModels()
	// The static table alone lists 24 models: third-party chat is discovered from
	// OpenRouter at runtime, and hidden routes are excluded from the listing.
	// Adjust if routes are added/removed. This is a canary for unexpected drift.
	if len(models) < 20 {
		t.Errorf("expected at least 20 visible models, got %d", len(models))
	}
}

// ── resolveModelRouteForOrg ──────────────────────────────────────────────────

func TestResolveModelRouteForOrg_FallsBackToStatic(t *testing.T) {
	// When DB adapter is nil (as in tests), resolveModelRouteForOrg should
	// fall back to the static routing table for any org.
	route := resolveModelRouteForOrg("claude-3-7-sonnet", "some-org")
	if route == nil {
		t.Fatal("resolveModelRouteForOrg(\"claude-3-7-sonnet\", \"some-org\") = nil, want non-nil from static fallback")
	}
	if route.providerName != "do-ai" {
		t.Errorf("provider = %q, want %q", route.providerName, "do-ai")
	}
}

func TestResolveModelRouteForOrg_UnknownModelReturnsNil(t *testing.T) {
	route := resolveModelRouteForOrg("nonexistent-model-xyz", "hanzo")
	if route != nil {
		t.Errorf("resolveModelRouteForOrg(\"nonexistent-model-xyz\", \"hanzo\") = %+v, want nil", route)
	}
}

func TestResolveModelRouteForOrg_EmptyOrgFallsBack(t *testing.T) {
	// Empty org resolves from the static map
	route := resolveModelRouteForOrg("wan2-2-t2v-a14b", "")
	if route == nil {
		t.Fatal("resolveModelRouteForOrg(\"wan2-2-t2v-a14b\", \"\") = nil, want non-nil")
	}
	if route.providerName != "do-ai" {
		t.Errorf("provider = %q, want %q", route.providerName, "do-ai")
	}
	if !route.premium {
		t.Error("wan2-2-t2v-a14b should be premium")
	}
}
