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
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanzoai/ai/object"
)

// ── adminProviderView projection ────────────────────────────────────────────

// TestToAdminProviderView_FieldsAndFlags verifies the projection maps State →
// enabled, IsDefault → primary, resolves keyPresent as a boolean, and carries the
// model count from the supplied map.
func TestToAdminProviderView_FieldsAndFlags(t *testing.T) {
	// KMS disabled + no env key → a kms:// ref is fail-closed absent.
	t.Setenv("KMS_CLIENT_ID", "")
	t.Setenv("KMS_SERVICE_TOKEN", "")
	t.Setenv("HANZO_API_KEY", "")

	p := &object.Provider{
		Name:         "do-ai",
		DisplayName:  "DigitalOcean AI (GenAI)",
		Category:     "Model",
		Type:         "DigitalOcean",
		ProviderUrl:  "https://inference.do-ai.run/v1",
		ClientSecret: "kms://DO_AI_API_KEY_MISSING_FOR_TEST",
		State:        "Active",
		IsDefault:    true,
	}
	counts := map[string]int{"do-ai": 122}

	v := toAdminProviderView(p, counts)

	if v.Name != "do-ai" {
		t.Errorf("Name = %q, want do-ai", v.Name)
	}
	if v.DisplayName != "DigitalOcean AI (GenAI)" {
		t.Errorf("DisplayName = %q", v.DisplayName)
	}
	if v.Type != "DigitalOcean" {
		t.Errorf("Type = %q, want DigitalOcean", v.Type)
	}
	if !v.Enabled {
		t.Error("Enabled = false for State=Active, want true")
	}
	if !v.Primary {
		t.Error("Primary = false for IsDefault=true, want true")
	}
	if v.KeyPresent {
		t.Error("KeyPresent = true for an unresolved kms:// ref (KMS off), want false")
	}
	if v.ModelCount != 122 {
		t.Errorf("ModelCount = %d, want 122", v.ModelCount)
	}
	if v.ProviderURL != "https://inference.do-ai.run/v1" {
		t.Errorf("ProviderURL = %q", v.ProviderURL)
	}
}

func TestToAdminProviderView_DisabledAndKeyFromEnv(t *testing.T) {
	// Env-first resolution → keyPresent true; State != Active → enabled false;
	// IsDefault false → primary false.
	t.Setenv("FIREWORKS_API_KEY_TEST_PRESENT", "fw-key")
	p := &object.Provider{
		Name:         "fireworks",
		Category:     "Model",
		Type:         "Fireworks",
		ClientSecret: "kms://FIREWORKS_API_KEY_TEST_PRESENT",
		State:        "Disabled",
		IsDefault:    false,
	}
	v := toAdminProviderView(p, map[string]int{})
	if v.Enabled {
		t.Error("Enabled = true for State=Disabled, want false")
	}
	if v.Primary {
		t.Error("Primary = true for IsDefault=false, want false")
	}
	if !v.KeyPresent {
		t.Error("KeyPresent = false for a kms:// ref resolvable via env, want true")
	}
	if v.ModelCount != 0 {
		t.Errorf("ModelCount = %d for a provider absent from the count map, want 0", v.ModelCount)
	}
}

// TestAdminProviderView_NeverSerializesSecrets is the security regression guard:
// the JSON of the management view must NOT contain any secret-bearing key. If a
// future edit adds ClientSecret/ProviderKey to the struct, this fails.
func TestAdminProviderView_NeverSerializesSecrets(t *testing.T) {
	t.Setenv("SUPER_SECRET_TEST_KEY", "sk-must-not-leak-123")
	p := &object.Provider{
		Name:         "do-ai",
		Category:     "Model",
		ClientSecret: "kms://SUPER_SECRET_TEST_KEY",
		ProviderKey:  "sk-provider-key-must-not-leak",
		UserKey:      "user-key-must-not-leak",
		SignKey:      "sign-key-must-not-leak",
		State:        "Active",
	}
	v := toAdminProviderView(p, map[string]int{"do-ai": 1})
	// Sanity: env-first means keyPresent should be true here.
	if !v.KeyPresent {
		t.Fatal("precondition: expected keyPresent true via env resolution")
	}
	blob, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(blob)
	for _, forbidden := range []string{
		"sk-must-not-leak-123",           // resolved kms value
		"kms://SUPER_SECRET_TEST_KEY",    // the raw ref
		"sk-provider-key-must-not-leak",  // providerKey
		"user-key-must-not-leak",         // userKey
		"sign-key-must-not-leak",         // signKey
		"clientSecret", "providerKey",    // secret-bearing field names
		"userKey", "signKey", "configText",
	} {
		if strings.Contains(s, forbidden) {
			t.Errorf("admin provider view JSON leaked %q: %s", forbidden, s)
		}
	}
	// Positive: the boolean signal IS present.
	if !strings.Contains(s, `"keyPresent":true`) {
		t.Errorf("expected keyPresent:true in JSON, got: %s", s)
	}
}

// ── exactly-one-primary ─────────────────────────────────────────────────────

func TestProvidersToRepointPrimary_ExactlyOne(t *testing.T) {
	providers := []*object.Provider{
		{Name: "do-ai", Category: "Model", IsDefault: true},
		{Name: "fireworks", Category: "Model", IsDefault: false},
		{Name: "openai-direct", Category: "Model", IsDefault: false},
		{Name: "zen", Category: "Model", IsDefault: false},
		{Name: "openrouter", Category: "Model", IsDefault: false},
		// A non-Model provider must never be touched.
		{Name: "provider-storage-built-in", Category: "Storage", IsDefault: true},
	}

	changed := providersToRepointPrimary(providers, "fireworks")

	// Only do-ai (true→false) and fireworks (false→true) change. do-ai currently
	// primary must be demoted; fireworks promoted. The others already match.
	changedNames := map[string]bool{}
	for _, p := range changed {
		changedNames[p.Name] = true
	}
	if !changedNames["do-ai"] || !changedNames["fireworks"] {
		t.Errorf("expected do-ai and fireworks to change, got %v", changedNames)
	}
	if changedNames["provider-storage-built-in"] {
		t.Error("non-Model storage provider must not be repointed")
	}

	// After applying, EXACTLY ONE Model provider is primary and it is the target.
	primaryCount := 0
	for _, p := range providers {
		if p.Category == "Model" && p.IsDefault {
			primaryCount++
			if p.Name != "fireworks" {
				t.Errorf("primary Model provider = %q, want fireworks", p.Name)
			}
		}
	}
	if primaryCount != 1 {
		t.Errorf("primary Model provider count = %d, want exactly 1", primaryCount)
	}

	// The Storage provider's IsDefault is untouched (still true, its own concern).
	for _, p := range providers {
		if p.Name == "provider-storage-built-in" && !p.IsDefault {
			t.Error("storage provider IsDefault was incorrectly cleared")
		}
	}
}

func TestProvidersToRepointPrimary_NoOpWhenAlreadyPrimary(t *testing.T) {
	providers := []*object.Provider{
		{Name: "do-ai", Category: "Model", IsDefault: true},
		{Name: "fireworks", Category: "Model", IsDefault: false},
	}
	changed := providersToRepointPrimary(providers, "do-ai")
	if len(changed) != 0 {
		t.Errorf("expected no changes when target is already the sole primary, got %d", len(changed))
	}
}

// ── stateForEnabled ─────────────────────────────────────────────────────────

func TestStateForEnabled(t *testing.T) {
	if got := stateForEnabled(true); got != "Active" {
		t.Errorf("stateForEnabled(true) = %q, want Active", got)
	}
	if got := stateForEnabled(false); got != "Disabled" {
		t.Errorf("stateForEnabled(false) = %q, want Disabled", got)
	}
}

// ── modelCountByProvider ────────────────────────────────────────────────────

// TestModelCountByProvider_MatchesStaticMap verifies the count helper agrees
// with an independent count over the static map — proving it is computed from the
// route table, not hardcoded. (In the unit env no YAML config is loaded, so the
// helper uses the static modelRoutes map.)
func TestModelCountByProvider_MatchesStaticMap(t *testing.T) {
	if GetModelConfig() != nil {
		t.Skip("YAML model config loaded; static-map parity check not applicable")
	}
	got := modelCountByProvider()

	// Independent recount over the same source.
	want := map[string]int{}
	for _, route := range modelRoutes {
		want[route.providerName]++
	}

	if len(got) != len(want) {
		t.Errorf("provider count keys = %d, want %d (got=%v want=%v)", len(got), len(want), got, want)
	}
	for prov, n := range want {
		if got[prov] != n {
			t.Errorf("modelCountByProvider()[%q] = %d, want %d", prov, got[prov], n)
		}
	}

	// Every count must equal the number of routes whose providerName == prov —
	// asserted here for the known providers so drift in the table is visible.
	for _, prov := range []string{"do-ai", "fireworks", "openai-direct"} {
		n := 0
		for _, route := range modelRoutes {
			if route.providerName == prov {
				n++
			}
		}
		if got[prov] != n {
			t.Errorf("count for %q = %d, recount = %d", prov, got[prov], n)
		}
		if n == 0 {
			t.Errorf("expected at least one route for provider %q", prov)
		}
	}
}

// TestModelCountByProvider_ZenAttributedToServingProvider documents that branded
// zen models are counted under their SERVING provider (do-ai), so a "zen"
// provider record (whose name no route targets) reports 0 — accurate, since the
// zen MODELS route via do-ai. This locks in the documented behavior.
func TestModelCountByProvider_ZenAttributedToServingProvider(t *testing.T) {
	if GetModelConfig() != nil {
		t.Skip("YAML model config loaded; static-map behavior check not applicable")
	}
	counts := modelCountByProvider()
	if _, ok := counts["zen"]; ok {
		t.Errorf("did not expect any route with providerName=\"zen\" (zen models route via do-ai); got count=%d", counts["zen"])
	}
	// zen models exist and route through do-ai — confirm at least one zen key
	// resolves to the do-ai provider so the attribution rationale holds.
	if r := resolveModelRoute("zen4"); r == nil || r.providerName != "do-ai" {
		t.Errorf("zen4 route = %+v, want providerName=do-ai", r)
	}
}
