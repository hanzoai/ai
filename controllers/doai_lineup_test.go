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
	"gemma-4-31b":       "gemma-4-31B-it",              // was "gemma-4-31b" (not in catalog)
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
	useCatalog(t, "../conf/models.yaml")
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

// TestBestIsRetired is a ratchet. `best` was a virtual policy model whose
// ordered fallback chain was its quality ranking, and it is gone: a superlative
// names no product — it says which model is best without saying what it IS —
// and it was a second answer to a question the enso family already answers,
// since enso is the tier that routes. Nothing points at it now (cloud's
// FallbackModel takes `enso`, and `hanzo code`'s DEFAULT_MODEL was already
// `enso`, which is why the comment here claiming it defaulted to `best` was
// stale), and production carried no request naming it in 24h.
//
// Asserted ABSENT rather than merely deleted, because this catalog is
// hand-edited in two places that have to agree — this file and the prod
// ConfigMap (universe/infra/k8s/cloud/models.yaml) — so a re-add would
// republish a retired SKU to /v1/models with nothing to catch it.
func TestBestIsRetired(t *testing.T) {
	useCatalog(t, "../conf/models.yaml")
	cfg := GetModelConfig()
	if cfg == nil {
		t.Fatal("model config nil after init")
	}
	if r := cfg.ResolveRoute("best"); r != nil {
		t.Errorf("`best` is retired but still resolves to %s/%s", r.providerName, r.upstreamModel)
	}
	for _, m := range cfg.ListModels() {
		if m.ID == "best" {
			t.Error("`best` is retired but is still published in the /v1/models listing")
		}
	}
}

// TestFailoverAdvancesOnProviderFaults pins the trigger set the failover loop
// (controllers/failover.go) treats as retryable — the conditions on which a
// route advances to its next rank instead of hard-failing. It outlived `best`,
// which is the point: this is the ONE fallback mechanism every route with a
// chain uses, and it had only ever been demonstrated THROUGH that model.
func TestFailoverAdvancesOnProviderFaults(t *testing.T) {
	for _, err := range []error{
		errors.New("error, status code: 429, status: 429 Too Many Requests"), // rate-limited (observed live)
		errors.New("402 payment required"),
		errors.New("403 this model is not available for your account"),
		errors.New("insufficient_quota"),
		unavailable("do-ai"), // the value the loop actually raises for a switched-off row
	} {
		if faultOf(err) != faultProvider {
			t.Errorf("a cascade would NOT fire on %q (faultOf said request, not provider)", err)
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
