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
	"os"
	"path/filepath"
	"testing"
)

// These tests pin the ADDITIVE enrichment of the /v1/models response shape
// (modelInfo): the OpenAI-compatible core stays byte-stable while provider +
// pricing are added omitempty, sourced only from data ai already holds. Both
// builders are covered — ListModels() (YAML/prod) and listAvailableModels()
// (static fallback) — so they emit the identical shape. A branded (owned_by)
// model surfaces its public owner in owned_by; the serving provider is omitted
// (publicProvider). These are the internal provider names that, for a branded
// model, must not appear as the public "provider" — see hip-00NN.
var upstreamProviders = map[string]bool{"do-ai": true, "fireworks": true, "openai-direct": true}

func indexModels(models []modelInfo) map[string]modelInfo {
	m := make(map[string]modelInfo, len(models))
	for _, mi := range models {
		m[mi.ID] = mi
	}
	return m
}

func TestModelListEnvelopeSupportsOpenAIAndCodex(t *testing.T) {
	raw, err := json.Marshal(modelListEnvelope([]modelInfo{{
		ID: "zen4-coder", Object: "model", Created: 1, OwnedBy: "hanzo", Premium: true,
	}}))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Object string            `json:"object"`
		Data   []modelInfo       `json:"data"`
		Models []json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Object != "list" || len(got.Data) != 1 || got.Data[0].ID != "zen4-coder" {
		t.Fatalf("OpenAI catalog changed: %#v", got)
	}
	if got.Models == nil || len(got.Models) != 0 {
		t.Fatalf("Codex fallback catalog must be a present empty array: %#v", got.Models)
	}
}

// jsonKeys marshals a modelInfo and returns the set of top-level JSON keys, so
// tests can assert presence/omission of omitempty fields on the real wire shape.
func jsonKeys(t *testing.T, mi modelInfo) map[string]bool {
	t.Helper()
	raw, err := json.Marshal(mi)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	keys := make(map[string]bool, len(m))
	for k := range m {
		keys[k] = true
	}
	return keys
}

// assertCoreUnchanged verifies the five OpenAI-compatible core keys are present
// and named exactly as external consumers (Claude Code, Codex, OpenAI SDK) parse
// them. A rename or drop here would break those clients.
func assertCoreUnchanged(t *testing.T, mi modelInfo) {
	t.Helper()
	keys := jsonKeys(t, mi)
	for _, k := range []string{"id", "object", "created", "owned_by", "premium"} {
		if !keys[k] {
			t.Errorf("core field %q missing from JSON for %q", k, mi.ID)
		}
	}
}

// TestModelInfoOmitEmpty proves the additive fields vanish when empty (so an
// existing consumer sees exactly the legacy core), while premium — part of the
// stable core — is always emitted even when false.
func TestModelInfoOmitEmpty(t *testing.T) {
	keys := jsonKeys(t, modelInfo{
		ID: "sample", Object: "model", Created: 1, OwnedBy: "do-ai", Premium: false,
	})
	for _, absent := range []string{"provider", "context_window", "pricing"} {
		if keys[absent] {
			t.Errorf("bare modelInfo must omit %q", absent)
		}
	}
	if !keys["premium"] {
		t.Error("premium must always be present (no omitempty)")
	}
}

// TestModelInfoContextWindowSurfaced proves the additive context_window field is
// emitted (and honestly valued) once ai holds the datum — so an OpenAI-compatible
// client can size a 1M-token enso context instead of guessing a smaller default.
func TestModelInfoContextWindowSurfaced(t *testing.T) {
	raw, err := json.Marshal(modelInfo{
		ID: "enso", Object: "model", Created: 1, OwnedBy: "hanzo", Premium: true,
		ContextWindow: 1_000_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got modelInfo
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.ContextWindow != 1_000_000 {
		t.Errorf("enso context_window: want 1000000, got %d", got.ContextWindow)
	}
	if !jsonKeys(t, modelInfo{ID: "enso", ContextWindow: 1_000_000})["context_window"] {
		t.Error("context_window must be present when set")
	}
}

// TestPricingInfoZeroOmitted proves the never-fabricate guard: a present but
// all-zero price yields no pricing block.
func TestPricingInfoZeroOmitted(t *testing.T) {
	if pricingInfo(modelPrice{}, false) != nil {
		t.Error("absent pricing must project to nil")
	}
	if pricingInfo(modelPrice{InputPerMillion: 0, OutputPerMillion: 0}, true) != nil {
		t.Error("all-zero pricing must project to nil (no meaningless {0,0})")
	}
	got := pricingInfo(modelPrice{InputPerMillion: 1.25, OutputPerMillion: 5.00}, true)
	if got == nil || got.Input != 1.25 || got.Output != 5.00 {
		t.Errorf("real pricing must project faithfully, got %+v", got)
	}
}

// TestPublicProviderBrandGate is the unit-level guard for the leak fix: branded
// models (ownedBy set) never expose the upstream provider; unbranded ones do.
func TestPublicProviderBrandGate(t *testing.T) {
	cases := []struct {
		name  string
		route modelRoute
		want  string
	}{
		{"unbranded passthrough", modelRoute{providerName: "do-ai"}, "do-ai"},
		{"zen brand (hanzo)", modelRoute{providerName: "do-ai", ownedBy: "hanzo"}, ""},
		{"fireworks brand", modelRoute{providerName: "fireworks", ownedBy: "hanzo"}, ""},
		{"openai embedding", modelRoute{providerName: "openai-direct", ownedBy: "openai"}, ""},
	}
	for _, c := range cases {
		if got := publicProvider(c.route); got != c.want {
			t.Errorf("%s: publicProvider = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestListModelsRichConfigPath covers the prod path: ModelConfig.ListModels()
// built from YAML. It exercises both an unbranded model (real provider exposed)
// and two branded classes (provider OMITTED, never the upstream), plus pricing
// present/omitted.
func TestListModelsRichConfigPath(t *testing.T) {
	const richYAML = `
version: 1
features:
  live_mode: false
models:
  gpt-4o:
    provider: do-ai
    upstream: openai-gpt-4o
    pricing: { input: 2.50, output: 10.00 }
  llama-free:
    provider: do-ai
    upstream: some-llama
  zen4:
    provider: do-ai
    upstream: glm-5
    premium: true
    owned_by: hanzo
    pricing: { input: 3.00, output: 9.60 }
  text-embedding-3-small:
    provider: openai-direct
    upstream: text-embedding-3-small
    owned_by: openai
`
	dir := t.TempDir()
	path := filepath.Join(dir, "models.yaml")
	if err := os.WriteFile(path, []byte(richYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	mc := &ModelConfig{
		routes:  make(map[string]modelRoute),
		pricing: make(map[string]modelPrice),
		stopCh:  make(chan struct{}),
	}
	if err := mc.loadFromFile(path); err != nil {
		t.Fatal(err)
	}
	byID := indexModels(mc.ListModels())

	// (a) OpenAI core fields unchanged for a sample (unbranded) model, and the
	// real serving provider IS exposed for unbranded passthroughs.
	gpt, ok := byID["gpt-4o"]
	if !ok {
		t.Fatal("gpt-4o missing from listing")
	}
	if gpt.Object != "model" || gpt.OwnedBy != "do-ai" || gpt.Created == 0 {
		t.Errorf("gpt-4o core wrong: %+v", gpt)
	}
	if gpt.Provider != "do-ai" {
		t.Errorf("unbranded gpt-4o should expose provider do-ai, got %q", gpt.Provider)
	}
	if gpt.Pricing == nil || gpt.Pricing.Input != 2.50 || gpt.Pricing.Output != 10.00 {
		t.Errorf("gpt-4o pricing want {2.50,10.00}, got %+v", gpt.Pricing)
	}
	assertCoreUnchanged(t, gpt)

	// (b) unbranded, no pricing → provider present, pricing OMITTED.
	free := byID["llama-free"]
	if free.Provider != "do-ai" {
		t.Errorf("llama-free provider want do-ai, got %q", free.Provider)
	}
	if free.Pricing != nil {
		t.Errorf("llama-free pricing must be omitted, got %+v", *free.Pricing)
	}
	if jsonKeys(t, free)["pricing"] {
		t.Error("llama-free JSON must not contain pricing")
	}

	// (c) BRANDED zen (owned_by hanzo): provider OMITTED (upstream do-ai never
	// leaks), owner still hanzo, pricing still real.
	zen, ok := byID["zen4"]
	if !ok {
		t.Fatal("zen4 missing from listing")
	}
	if zen.Provider != "" {
		t.Errorf("zen4 must NOT expose a provider (leak), got %q", zen.Provider)
	}
	if zen.OwnedBy != "hanzo" {
		t.Errorf("zen4 owned_by want hanzo, got %q", zen.OwnedBy)
	}
	if zen.Pricing == nil || zen.Pricing.Input != 3.00 || zen.Pricing.Output != 9.60 {
		t.Errorf("zen4 pricing want {3.00,9.60}, got %+v", zen.Pricing)
	}
	zenKeys := jsonKeys(t, zen)
	if zenKeys["provider"] {
		t.Error("zen4 JSON must NOT contain provider")
	}
	if !zenKeys["pricing"] {
		t.Error("zen4 JSON should still contain pricing")
	}

	// (c) BRANDED embedding (owned_by openai): provider OMITTED (openai-direct
	// never leaks), owner openai.
	emb, ok := byID["text-embedding-3-small"]
	if !ok {
		t.Fatal("text-embedding-3-small missing from listing")
	}
	if emb.Provider != "" {
		t.Errorf("embedding must NOT expose provider (leak openai-direct), got %q", emb.Provider)
	}
	if emb.OwnedBy != "openai" {
		t.Errorf("embedding owned_by want openai, got %q", emb.OwnedBy)
	}
	if jsonKeys(t, emb)["provider"] {
		t.Error("embedding JSON must NOT contain provider")
	}
}

// TestListModelsRichStaticPath covers the fallback path: listAvailableModels()
// reading the real static route + pricing tables. gpt-4o is unbranded (provider
// exposed + priced); glm-5 is unbranded with no static price; zen4 is branded
// (provider omitted).
func TestListModelsRichStaticPath(t *testing.T) {
	if GetModelConfig() != nil {
		t.Skip("global model config set by another test; static path not exercised")
	}
	byID := indexModels(listAvailableModels())

	gpt, ok := byID["gpt-4o"]
	if !ok {
		t.Fatal("gpt-4o missing from static listing")
	}
	if gpt.Object != "model" || gpt.OwnedBy != "do-ai" {
		t.Errorf("gpt-4o core wrong: %+v", gpt)
	}
	if gpt.Provider != "do-ai" {
		t.Errorf("unbranded gpt-4o should expose provider do-ai, got %q", gpt.Provider)
	}
	if gpt.Pricing == nil || gpt.Pricing.Input != 2.50 || gpt.Pricing.Output != 10.00 {
		t.Errorf("gpt-4o static pricing want {2.50,10.00}, got %+v", gpt.Pricing)
	}

	glm, ok := byID["glm-5"]
	if !ok {
		t.Fatal("glm-5 missing from static listing")
	}
	if glm.Provider != "do-ai" {
		t.Errorf("unbranded glm-5 provider want do-ai, got %q", glm.Provider)
	}
	if glm.Pricing != nil {
		t.Errorf("glm-5 has no static pricing; must be omitted, got %+v", *glm.Pricing)
	}

	zen, ok := byID["zen4"]
	if !ok {
		t.Fatal("zen4 missing from static listing")
	}
	if zen.Provider != "" {
		t.Errorf("branded zen4 must NOT expose provider, got %q", zen.Provider)
	}
	if zen.OwnedBy != "hanzo" {
		t.Errorf("zen4 owned_by want hanzo, got %q", zen.OwnedBy)
	}
}

// TestNoStaticBrandedModels is the invariant after the Zen family moved to the
// zen service: ai's static table holds NO branded (owned_by) model. The Zen
// family — the only branded models — is discovered from zen's /v1/models and
// served through the zen provider; zen's discovery surface carries no upstream,
// so that is where the brand-leak guard now lives. A branded model reappearing
// here means a zen SKU was re-hardcoded into ai, the exact drift this removed.
func TestNoStaticBrandedModels(t *testing.T) {
	if GetModelConfig() != nil {
		t.Skip("global model config set by another test; static path not exercised")
	}
	for _, m := range listAvailableModels() {
		if route := modelRoutes[m.ID]; route.ownedBy != "" {
			t.Errorf("static table has branded model %q (owned_by=%s) — zen SKUs must be dynamic, not hardcoded", m.ID, route.ownedBy)
		}
	}
}
