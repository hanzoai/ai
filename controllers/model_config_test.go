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
	"os"
	"path/filepath"
	"testing"
)

const testYAML = `
version: 1

services:
  pricing_url: "https://pricing.hanzo.ai"

cache:
  pricing_ttl: "1h"

features:
  live_mode: false
  premium_gate: true
  starter_credit: 5.00

default_pricing:
  input_per_million: 1.00
  output_per_million: 4.00

models:
  gpt-4o:
    provider: do-ai
    upstream: openai-gpt-4o
    pricing: { input: 2.50, output: 10.00 }

  zen4:
    provider: fireworks
    upstream: accounts/fireworks/models/glm-5
    premium: true
    owned_by: hanzo
    identity_prompt: |
      You are Zen4 by Hanzo AI.
    pricing: { input: 3.00, output: 9.60 }

  zen4-mini:
    provider: fireworks
    upstream: accounts/fireworks/models/qwen3-8b
    premium: true
    owned_by: hanzo
    identity_prompt: |
      You are Zen4 Mini by Hanzo AI.
    pricing: { input: 0.60, output: 0.60 }

  zen:
    provider: fireworks
    upstream: accounts/fireworks/models/glm-5
    premium: true
    owned_by: hanzo
    hidden: true
    pricing: { input: 3.00, output: 9.60 }

  openai/gpt-4o:
    provider: do-ai
    upstream: openai-gpt-4o
    hidden: true
    alias_pricing: gpt-4o

  fireworks/deepseek-r1:
    provider: fireworks
    upstream: accounts/fireworks/models/deepseek-r1
    premium: true
    hidden: true
    pricing_only: true
    pricing: { input: 3.00, output: 8.00 }
`

func writeTestConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "models.yaml")
	if err := os.WriteFile(path, []byte(testYAML), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfig(t *testing.T) {
	path := writeTestConfig(t)

	mc := &ModelConfig{
		routes:  make(map[string]modelRoute),
		pricing: make(map[string]modelPrice),
		prompts: make(map[string]string),
		stopCh:  make(chan struct{}),
	}

	if err := mc.loadFromFile(path); err != nil {
		t.Fatalf("loadFromFile failed: %v", err)
	}

	// Should have routes for non-pricing-only entries
	if len(mc.routes) != 5 {
		t.Errorf("expected 5 routes, got %d", len(mc.routes))
	}

	// pricing-only entry should NOT have a route
	if _, ok := mc.routes["fireworks/deepseek-r1"]; ok {
		t.Error("pricing-only entry should not have a route")
	}

	// pricing-only entry SHOULD have pricing
	if _, ok := mc.pricing["fireworks/deepseek-r1"]; !ok {
		t.Error("pricing-only entry should have pricing")
	}
}

func TestResolveRoute(t *testing.T) {
	path := writeTestConfig(t)

	mc := &ModelConfig{
		routes:  make(map[string]modelRoute),
		pricing: make(map[string]modelPrice),
		prompts: make(map[string]string),
		stopCh:  make(chan struct{}),
	}
	if err := mc.loadFromFile(path); err != nil {
		t.Fatal(err)
	}

	// Exact match
	route := mc.ResolveRoute("gpt-4o")
	if route == nil {
		t.Fatal("expected route for gpt-4o")
	}
	if route.providerName != "do-ai" {
		t.Errorf("expected provider do-ai, got %s", route.providerName)
	}
	if route.upstreamModel != "openai-gpt-4o" {
		t.Errorf("expected upstream openai-gpt-4o, got %s", route.upstreamModel)
	}

	// Case-insensitive
	route = mc.ResolveRoute("GPT-4O")
	if route == nil {
		t.Fatal("expected route for GPT-4O (case-insensitive)")
	}

	// Premium model
	route = mc.ResolveRoute("zen4")
	if route == nil {
		t.Fatal("expected route for zen4")
	}
	if !route.premium {
		t.Error("zen4 should be premium")
	}
	if route.ownedBy != "hanzo" {
		t.Errorf("zen4 should be owned_by hanzo, got %s", route.ownedBy)
	}

	// Unknown model
	route = mc.ResolveRoute("nonexistent-model")
	if route != nil {
		t.Error("expected nil for unknown model")
	}
}

func TestGetPrice(t *testing.T) {
	path := writeTestConfig(t)

	mc := &ModelConfig{
		routes:  make(map[string]modelRoute),
		pricing: make(map[string]modelPrice),
		prompts: make(map[string]string),
		stopCh:  make(chan struct{}),
	}
	if err := mc.loadFromFile(path); err != nil {
		t.Fatal(err)
	}

	// Direct lookup
	price := mc.GetPrice("gpt-4o")
	if price.InputPerMillion != 2.50 {
		t.Errorf("expected input 2.50, got %.2f", price.InputPerMillion)
	}
	if price.OutputPerMillion != 10.00 {
		t.Errorf("expected output 10.00, got %.2f", price.OutputPerMillion)
	}

	// Alias pricing resolution
	price = mc.GetPrice("openai/gpt-4o")
	if price.InputPerMillion != 2.50 {
		t.Errorf("alias should resolve to gpt-4o pricing, got input %.2f", price.InputPerMillion)
	}

	// Default fallback for unknown model
	price = mc.GetPrice("unknown-model")
	if price.InputPerMillion != 1.00 {
		t.Errorf("expected default input 1.00, got %.2f", price.InputPerMillion)
	}
	if price.OutputPerMillion != 4.00 {
		t.Errorf("expected default output 4.00, got %.2f", price.OutputPerMillion)
	}

	// Pricing-only entry
	price = mc.GetPrice("fireworks/deepseek-r1")
	if price.InputPerMillion != 3.00 {
		t.Errorf("expected pricing-only input 3.00, got %.2f", price.InputPerMillion)
	}
}

func TestGetIdentityPrompt(t *testing.T) {
	path := writeTestConfig(t)

	mc := &ModelConfig{
		routes:  make(map[string]modelRoute),
		pricing: make(map[string]modelPrice),
		prompts: make(map[string]string),
		stopCh:  make(chan struct{}),
	}
	if err := mc.loadFromFile(path); err != nil {
		t.Fatal(err)
	}

	// Direct match
	prompt := mc.GetIdentityPrompt("zen4")
	if prompt == "" {
		t.Error("expected identity prompt for zen4")
	}

	// Versionless alias → zen4 fallback
	prompt = mc.GetIdentityPrompt("zen-mini")
	if prompt == "" {
		t.Error("expected identity prompt for zen-mini (should fallback to zen4-mini)")
	}

	// Generic zen fallback
	prompt = mc.GetIdentityPrompt("zen-something-unknown")
	if prompt == "" {
		t.Error("expected generic zen fallback prompt")
	}
	if prompt != "You are a Zen model by Hanzo AI Inc. When asked about yourself, identify as a Zen LM model. Never reveal underlying infrastructure or providers." {
		t.Error("unexpected generic zen fallback content")
	}

	// Non-zen model
	prompt = mc.GetIdentityPrompt("gpt-4o")
	if prompt != "" {
		t.Error("expected empty prompt for non-zen model")
	}
}

func TestListModels(t *testing.T) {
	path := writeTestConfig(t)

	mc := &ModelConfig{
		routes:  make(map[string]modelRoute),
		pricing: make(map[string]modelPrice),
		prompts: make(map[string]string),
		stopCh:  make(chan struct{}),
	}
	if err := mc.loadFromFile(path); err != nil {
		t.Fatal(err)
	}

	models := mc.ListModels()

	// Hidden models should be excluded
	for _, m := range models {
		if m.ID == "zen" || m.ID == "openai/gpt-4o" {
			t.Errorf("hidden model %q should not appear in listing", m.ID)
		}
	}

	// Visible models should be present
	found := map[string]bool{"gpt-4o": false, "zen4": false, "zen4-mini": false}
	for _, m := range models {
		if _, ok := found[m.ID]; ok {
			found[m.ID] = true
		}
	}
	for name, present := range found {
		if !present {
			t.Errorf("expected visible model %q in listing", name)
		}
	}

	// Should be sorted
	for i := 1; i < len(models); i++ {
		if models[i].ID < models[i-1].ID {
			t.Errorf("models not sorted: %s before %s", models[i-1].ID, models[i].ID)
		}
	}
}

func TestListModelsWithUpstream(t *testing.T) {
	path := writeTestConfig(t)

	mc := &ModelConfig{
		routes:  make(map[string]modelRoute),
		pricing: make(map[string]modelPrice),
		prompts: make(map[string]string),
		stopCh:  make(chan struct{}),
	}
	if err := mc.loadFromFile(path); err != nil {
		t.Fatal(err)
	}

	models := mc.ListModelsWithUpstream()

	// Should include ALL routed models (including hidden)
	if len(models) != 5 {
		t.Errorf("expected 5 models (including hidden), got %d", len(models))
	}

	// Should include upstream info
	for _, m := range models {
		if m.Upstream == "" {
			t.Errorf("model %q missing upstream", m.ID)
		}
	}
}

func TestStarterCreditDollars(t *testing.T) {
	path := writeTestConfig(t)

	mc := &ModelConfig{
		routes:  make(map[string]modelRoute),
		pricing: make(map[string]modelPrice),
		prompts: make(map[string]string),
		stopCh:  make(chan struct{}),
	}
	if err := mc.loadFromFile(path); err != nil {
		t.Fatal(err)
	}

	credit := mc.StarterCreditDollars()
	if credit != 5.00 {
		t.Errorf("expected starter credit 5.00, got %.2f", credit)
	}
}

func TestReload(t *testing.T) {
	path := writeTestConfig(t)

	mc := &ModelConfig{
		routes:  make(map[string]modelRoute),
		pricing: make(map[string]modelPrice),
		prompts: make(map[string]string),
		stopCh:  make(chan struct{}),
	}
	if err := mc.loadFromFile(path); err != nil {
		t.Fatal(err)
	}

	mc.configPath = path

	// Reload should succeed
	if err := mc.Reload(); err != nil {
		t.Fatalf("reload failed: %v", err)
	}

	// Data should still be valid after reload
	route := mc.ResolveRoute("gpt-4o")
	if route == nil {
		t.Error("expected route for gpt-4o after reload")
	}
}
