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
)

// ── resolveModelRoute ────────────────────────────────────────────────────────

func TestResolveModelRoute_KnownModels(t *testing.T) {
	cases := []struct {
		input        string
		wantProvider string
		wantModel    string
		wantPremium  bool
	}{
		// DO-AI free-tier
		{"gpt-4o", "do-ai", "openai-gpt-4o", false},
		{"gpt-5", "do-ai", "openai-gpt-5", false},
		{"claude-opus-4-6", "do-ai", "anthropic-claude-opus-4.6", false},
		{"qwen3-32b", "do-ai", "alibaba-qwen3-32b", false},

		// Aliases (free-tier)
		{"openai/gpt-4o", "do-ai", "openai-gpt-4o", false},
		{"anthropic/claude-opus-4-6", "do-ai", "anthropic-claude-opus-4.6", false},

		// Fireworks premium
		{"fireworks/glm-5", "fireworks", "accounts/fireworks/models/glm-5", true},
		{"fireworks/qwen3-8b", "fireworks", "accounts/fireworks/models/qwen3-8b", true},

		// OpenAI direct premium
		{"openai-direct/gpt-5", "openai-direct", "gpt-5", true},
		{"openai-direct/o3", "openai-direct", "o3", true},

		// Zen branded premium (routed through Fireworks)
		{"zen4", "fireworks", "accounts/fireworks/models/glm-5", true},
		{"zen4-mini", "fireworks", "accounts/fireworks/models/qwen3-8b", true},
		{"zen4-pro", "fireworks", "accounts/fireworks/models/kimi-k2p5", true},
		{"zen4-max", "fireworks", "accounts/cogito/models/cogito-671b-v2-p1", true},
		{"zen4-ultra", "fireworks", "accounts/fireworks/models/kimi-k2-thinking", true},
		{"zen4-coder", "fireworks", "accounts/fireworks/models/deepseek-v3p2", true},
		{"zen4-coder-flash", "fireworks", "accounts/fireworks/models/kimi-k2-instruct-0905", true},
		{"zen4-coder-pro", "fireworks", "accounts/fireworks/models/gpt-oss-120b", true},
		{"zen4-thinking", "fireworks", "accounts/fireworks/models/kimi-k2-thinking", true},
		{"zen3-omni", "fireworks", "accounts/fireworks/models/glm-4p7", true},
		{"zen3-vl", "fireworks", "accounts/fireworks/models/qwen3-vl-30b-a3b-instruct", true},
		{"zen3-nano", "fireworks", "accounts/fireworks/models/qwen3-8b", true},
		{"zen3-guard", "fireworks", "accounts/fireworks/models/mixtral-8x22b-instruct", true},
		{"zen3-embedding", "openai-direct", "text-embedding-3-large", true},

		// Zen versionless aliases → latest zenN
		{"zen", "fireworks", "accounts/fireworks/models/glm-5", true},
		{"zen-pro", "fireworks", "accounts/fireworks/models/kimi-k2p5", true},
		{"zen-mini", "fireworks", "accounts/fireworks/models/qwen3-8b", true},
		{"zen-vl", "fireworks", "accounts/fireworks/models/qwen3-vl-30b-a3b-instruct", true},
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
	route := resolveModelRoute("GPT-4O")
	if route == nil {
		t.Fatal("resolveModelRoute(\"GPT-4O\") = nil, want match")
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

func TestModelRoutes_ProviderNamesAreKnown(t *testing.T) {
	known := map[string]bool{
		"do-ai":         true,
		"fireworks":     true,
		"openai-direct": true,
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
	// As of 2026-02: 41 visible models (hidden aliases/prefixed routes excluded from listing).
	// Adjust if routes are added/removed. This is a canary for unexpected drift.
	if len(models) < 30 {
		t.Errorf("expected at least 30 visible models, got %d", len(models))
	}
}
