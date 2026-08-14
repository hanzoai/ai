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

import "testing"

// loadRealConfig loads the shipped conf/models.yaml into a LOCAL ModelConfig
// without touching the global singleton, so it never perturbs the static-path
// tests that skip when GetModelConfig() != nil.
func loadRealConfig(t *testing.T) *ModelConfig {
	t.Helper()
	mc := &ModelConfig{
		routes:  make(map[string]modelRoute),
		pricing: make(map[string]modelPrice),
		stopCh:  make(chan struct{}),
	}
	if err := mc.loadFromFile("../conf/models.yaml"); err != nil {
		t.Fatalf("load conf/models.yaml: %v", err)
	}
	return mc
}

// doaiEnabledModels is the customer-facing chat lineup enabled on the do-ai
// provider (2026-07-18), each verified live against inference.do-ai.run:
// user-facing SKU → {real catalog upstream, vision, tools}. vision/tools reflect
// the LIVE probe, so a "no" is a measured rejection, not an omission — gpt-5.6
// rejects tool calls (HTTP 400), and claude-sonnet-5 / deepseek-v4-pro / glm-5.2 /
// kimi-k2.6 / qwen3.5 reject image input, so those flags are intentionally false.
var doaiEnabledModels = []struct {
	sku      string
	upstream string
	vision   bool
	tools    bool
}{
	{"claude-opus-4-8", "anthropic-claude-opus-4.8", true, true},
	{"claude-opus-4-7", "anthropic-claude-opus-4.7", true, true},
	{"claude-sonnet-5", "anthropic-claude-5-sonnet", false, true},
	{"claude-sonnet-4-6", "anthropic-claude-4.6-sonnet", true, true},
	{"claude-haiku-4-5", "anthropic-claude-haiku-4.5", true, true},
	{"gpt-5.6-luna", "openai-gpt-5.6-luna", true, false},
	{"gpt-5.6-sol", "openai-gpt-5.6-sol", true, false},
	{"gpt-5.6-terra", "openai-gpt-5.6-terra", true, false},
	{"gpt-5.5", "openai-gpt-5.5", true, true},
	{"gpt-5.4", "openai-gpt-5.4", true, true},
	{"gpt-4o", "openai-gpt-4o", true, true},
	{"gpt-4o-mini", "openai-gpt-4o-mini", true, true},
	{"o3", "openai-o3", true, true},
	{"deepseek-v4-pro", "deepseek-v4-pro", false, true},
	{"llama-4-maverick", "llama-4-maverick", true, true},
	{"qwen3.5-397b", "qwen3.5-397b-a17b", false, true},
	{"glm-5.2", "glm-5.2", false, true},
	{"kimi-k2.6", "kimi-k2.6", false, true},
}

// TestDOAIEnabledModelsVisibleAndCapable pins that every customer-facing chat
// model enabled on do-ai (a) resolves to its real catalog upstream on the do-ai
// provider (so it meters under do-ai), (b) is VISIBLE in /v1/models (not hidden),
// and (c) surfaces its verified capabilities — context window, max output, and
// the vision/tools flags exactly as the live probe found them.
func TestDOAIEnabledModelsVisibleAndCapable(t *testing.T) {
	mc := loadRealConfig(t)

	listed := map[string]modelInfo{}
	for _, m := range mc.ListModels() {
		listed[m.ID] = m
	}

	for _, want := range doaiEnabledModels {
		r := mc.ResolveRoute(want.sku)
		if r == nil {
			t.Errorf("%q did not resolve", want.sku)
			continue
		}
		if r.providerName != "do-ai" {
			t.Errorf("%q provider = %q, want do-ai (must meter under do-ai)", want.sku, r.providerName)
		}
		if r.upstreamModel != want.upstream {
			t.Errorf("%q upstream = %q, want %q", want.sku, r.upstreamModel, want.upstream)
		}
		mi, ok := listed[want.sku]
		if !ok {
			t.Errorf("%q missing from /v1/models listing (still hidden?)", want.sku)
			continue
		}
		if mi.ContextWindow <= 0 {
			t.Errorf("%q listing has no context_window (clients cannot size context)", want.sku)
		}
		if mi.MaxOutputTokens <= 0 {
			t.Errorf("%q listing has no max_output_tokens", want.sku)
		}
		if mi.SupportsVision != want.vision {
			t.Errorf("%q supports_vision = %v, want %v (live probe)", want.sku, mi.SupportsVision, want.vision)
		}
		if mi.SupportsTools != want.tools {
			t.Errorf("%q supports_tools = %v, want %v (live probe)", want.sku, mi.SupportsTools, want.tools)
		}
	}
}

// TestModelInfoCapabilityFieldsOmitEmpty proves the new capability enrichments
// are additive: present on the wire only when true/nonzero (so a legacy consumer
// is unaffected), and a "no" is expressed by ABSENCE, never a fabricated false.
func TestModelInfoCapabilityFieldsOmitEmpty(t *testing.T) {
	bare := jsonKeys(t, modelInfo{ID: "x", Object: "model", Created: 1, OwnedBy: "do-ai"})
	for _, k := range []string{"max_output_tokens", "supports_vision", "supports_tools"} {
		if bare[k] {
			t.Errorf("bare modelInfo must omit %q", k)
		}
	}
	full := jsonKeys(t, modelInfo{
		ID: "x", Object: "model", Created: 1, OwnedBy: "do-ai",
		MaxOutputTokens: 128000, SupportsVision: true, SupportsTools: true,
	})
	for _, k := range []string{"max_output_tokens", "supports_vision", "supports_tools"} {
		if !full[k] {
			t.Errorf("modelInfo must surface %q when set", k)
		}
	}
}
