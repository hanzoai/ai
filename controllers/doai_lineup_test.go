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
	"errors"
	"testing"
)

// doaiLineupRoutes is the set of DigitalOcean models this package exposes that
// were added/corrected for the full do-ai lineup: user-facing name → the exact
// DO catalog upstream id it must resolve to. Every id here was verified live
// against the account's GET /v1/models catalog (real, not invented).
var doaiLineupRoutes = map[string]string{
	// Embeddings (POST /v1/embeddings passthrough).
	"bge-m3":                     "bge-m3",
	"e5-large-v2":                "e5-large-v2",
	"gte-large-en-v1.5":          "gte-large-en-v1.5",
	"all-mini-lm-l6-v2":          "all-mini-lm-l6-v2",
	"multi-qa-mpnet-base-dot-v1": "multi-qa-mpnet-base-dot-v1",
	// Image (sync OpenAI /images shape).
	"stable-diffusion-3.5-large": "stable-diffusion-3.5-large",
	// Inference Router (chat completions; auto-selects a foundation model).
	"router:general":                 "router:general",
	"router:knowledge-base-document": "router:knowledge-base-document",
	"router:software-engineering":    "router:software-engineering",
	"router:software-engineering-01": "router:software-engineering-01",
	"router:writing":                 "router:writing",
	// Latent upstream-id fixes (the id shipped in a prior version 404'd upstream).
	"gemma-4-31b":       "gemma-4-31B-it",             // was "gemma-4-31b" (not in catalog)
	"claude-sonnet-4-6": "anthropic-claude-4.6-sonnet", // real 4.6 upstream, now served (was routed to 4.5 while 4.6 403'd)
}

// TestDOAILineupStaticRoutes asserts the static routing table maps each
// user-facing name to the correct real DO catalog upstream on the do-ai
// provider. This is the fallback path used when the YAML config is not loaded.
func TestDOAILineupStaticRoutes(t *testing.T) {
	for name, wantUpstream := range doaiLineupRoutes {
		route, ok := modelRoutes[name]
		if !ok {
			t.Errorf("modelRoutes missing %q", name)
			continue
		}
		if route.providerName != "do-ai" {
			t.Errorf("%q provider = %q, want do-ai", name, route.providerName)
		}
		if route.upstreamModel != wantUpstream {
			t.Errorf("%q upstream = %q, want %q", name, route.upstreamModel, wantUpstream)
		}
	}
}

// TestDOAILineupInYAML loads the real conf/models.yaml (the catalog /v1/models
// renders in production) and asserts every lineup model is listed, resolves to
// its correct upstream, and — for the embeddings — is priced at DigitalOcean's
// published per-1M rate. This guards against route/pricing drift between the
// static map and the shipped YAML.
func TestDOAILineupInYAML(t *testing.T) {
	if err := InitModelConfig("../conf/models.yaml"); err != nil {
		t.Fatalf("load conf/models.yaml: %v", err)
	}
	cfg := GetModelConfig()
	if cfg == nil {
		t.Fatal("model config nil after init")
	}

	// Every lineup model resolves to its correct upstream on do-ai.
	for name, wantUpstream := range doaiLineupRoutes {
		r := cfg.ResolveRoute(name)
		if r == nil {
			t.Errorf("YAML route %q did not resolve", name)
			continue
		}
		if r.providerName != "do-ai" {
			t.Errorf("YAML %q provider = %q, want do-ai", name, r.providerName)
		}
		if r.upstreamModel != wantUpstream {
			t.Errorf("YAML %q upstream = %q, want %q", name, r.upstreamModel, wantUpstream)
		}
	}

	// The visible models appear in the /v1/models listing (not hidden).
	listed := map[string]bool{}
	for _, m := range cfg.ListModels() {
		listed[m.ID] = true
	}
	for _, id := range []string{
		"bge-m3", "e5-large-v2", "gte-large-en-v1.5", "all-mini-lm-l6-v2",
		"multi-qa-mpnet-base-dot-v1", "stable-diffusion-3.5-large",
		"router:general", "router:writing", "gemma-4-31b",
	} {
		if !listed[id] {
			t.Errorf("/v1/models listing missing %q", id)
		}
	}

	// Embeddings priced at DO's published per-1M input rate (output 0).
	embedInput := map[string]float64{
		"bge-m3":                     0.02,
		"e5-large-v2":                0.02,
		"gte-large-en-v1.5":          0.09,
		"all-mini-lm-l6-v2":          0.009,
		"multi-qa-mpnet-base-dot-v1": 0.009,
	}
	for name, in := range embedInput {
		p := cfg.GetPrice(name)
		if p.InputPerMillion != in || p.OutputPerMillion != 0 {
			t.Errorf("price %q = {%.3f,%.3f}, want {%.3f,0}", name, p.InputPerMillion, p.OutputPerMillion, in)
		}
	}
}

// TestBestVirtualModelCascade asserts the virtual `best` model (the "best
// available coding model" that `hanzo code` defaults to) is a plain route whose
// ordered fallback chain IS the quality ranking — resolving to glm-5.2 first and
// cascading down the ranked list on the retryable conditions the deployed
// failover loop (controllers/failover.go) advances on. This pins the DRY design:
// `best` reuses the ONE fallback mechanism, not a parallel router.
func TestBestVirtualModelCascade(t *testing.T) {
	if err := InitModelConfig("../conf/models.yaml"); err != nil {
		t.Fatalf("load conf/models.yaml: %v", err)
	}
	cfg := GetModelConfig()
	if cfg == nil {
		t.Fatal("model config nil after init")
	}

	r := cfg.ResolveRoute("best")
	if r == nil {
		t.Fatal("`best` did not resolve — the virtual model is missing from the catalog")
	}
	if r.providerName != "do-ai" || r.upstreamModel != "glm-5.2" {
		t.Errorf("best primary = %s/%s, want do-ai/glm-5.2", r.providerName, r.upstreamModel)
	}
	// The fallback chain, IN ORDER, is the quality ranking.
	wantChain := []struct{ provider, upstream string }{
		{"do-ai", "deepseek-v4-pro"},
		{"do-ai", "kimi-k2.6"},
		{"do-ai", "deepseek-4-flash"},
		{"do-ai", "qwen3.5-397b-a17b"},
	}
	if len(r.fallbacks) != len(wantChain) {
		t.Fatalf("best has %d fallbacks, want %d (%v)", len(r.fallbacks), len(wantChain), r.fallbacks)
	}
	for i, want := range wantChain {
		if r.fallbacks[i].providerName != want.provider || r.fallbacks[i].upstreamModel != want.upstream {
			t.Errorf("best fallback[%d] = %s/%s, want %s/%s",
				i, r.fallbacks[i].providerName, r.fallbacks[i].upstreamModel, want.provider, want.upstream)
		}
	}

	// `best` is the model `hanzo code claude` runs on by default, and glm-5.2 —
	// its primary — is served by DO at 262,144. Without a fallback that declares
	// a bigger window, a long session hits that ceiling and the request is
	// refused. deepseek-v4-pro is the one model DO serves at a true 1M
	// (measured), so it must carry its window here: that declaration is what
	// lets routeForPrompt send an oversized prompt to it instead of failing.
	if got := r.fallbacks[0].contextWindow; got != 1000000 {
		t.Errorf("best's deepseek-v4-pro fallback declares a %d window, want 1000000 — "+
			"without it, `hanzo code claude` is capped at glm-5.2's 262144 and long "+
			"sessions dead-end instead of routing to the model that can hold them", got)
	}
	// owned_by hanzo: it is a Hanzo policy model, so the upstream is never leaked
	// in the /v1/models listing (publicProvider omits it) and model-sync leaves it
	// alone (it manages only owned_by:"do-ai" passthroughs).
	if r.ownedBy != "hanzo" {
		t.Errorf("best owned_by = %q, want hanzo", r.ownedBy)
	}
	if publicProvider(*r) != "" {
		t.Errorf("best must not leak its upstream provider in listing, got %q", publicProvider(*r))
	}

	// `best` is listed (not hidden) so the CLI's catalog resolver finds it.
	listed := false
	for _, m := range cfg.ListModels() {
		if m.ID == "best" {
			listed = true
			if m.OwnedBy != "hanzo" {
				t.Errorf("listed best owned_by = %q, want hanzo", m.OwnedBy)
			}
		}
	}
	if !listed {
		t.Error("`best` missing from /v1/models listing — `hanzo code` catalog resolve would fail")
	}

	// The ranking only degrades gracefully because the failover loop treats
	// exactly these conditions as retryable (→ advance to the next rank). Pin the
	// trigger set that makes `best` fall through instead of hard-failing.
	for _, msg := range []string{
		"error, status code: 429, status: 429 Too Many Requests", // glm-5.2 rate-limited (observed live)
		"402 payment required",
		"403 this model is not available for your account",
		"insufficient_quota",
		"provider \"do-ai\" is unavailable (disabled or not configured)",
	} {
		if !isRetryableError(errors.New(msg)) {
			t.Errorf("best cascade would NOT fire on %q (isRetryableError=false)", msg)
		}
	}
}

// TestDOAIStableDiffusionImagePricing asserts SD 3.5 Large bills per image at
// DigitalOcean's published $0.08/image via the shared image-cost seam.
func TestDOAIStableDiffusionImagePricing(t *testing.T) {
	if got := imageCostCents("stable-diffusion-3.5-large", 1); got != 8 {
		t.Errorf("imageCostCents(stable-diffusion-3.5-large, 1) = %d, want 8", got)
	}
	if got := imageCostCents("stable-diffusion-3.5-large", 3); got != 24 {
		t.Errorf("imageCostCents(stable-diffusion-3.5-large, 3) = %d, want 24", got)
	}
}
