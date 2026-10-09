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
	"math"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
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

// TestPricingInfoPublishesWhatWasFound proves the never-fabricate guard and the
// fact it must not swallow: a price nobody stated yields no block, and a stated
// price of zero yields zero — which is the one a caller most needs, since it says
// the route is free.
func TestPricingInfoPublishesWhatWasFound(t *testing.T) {
	if pricingInfo(modelPrice{}, false) != nil {
		t.Error("absent pricing must project to nil")
	}
	free := pricingInfo(modelPrice{InputPerMillion: 0, OutputPerMillion: 0}, true)
	if free == nil || !reflect.DeepEqual(*free, modelPricingInfo{Prompt: "0", Completion: "0"}) {
		t.Errorf("a stated price of zero must project as zero, got %+v", free)
	}
	got := pricingInfo(modelPrice{InputPerMillion: 1.25, OutputPerMillion: 5.00}, true)
	want := modelPricingInfo{Prompt: "0.00000125", Completion: "0.000005", InputPerMillion: 1.25, OutputPerMillion: 5.00}
	if got == nil || !reflect.DeepEqual(*got, want) {
		t.Errorf("real pricing must project faithfully, got %+v, want %+v", got, want)
	}
	if pricingInfo(modelPrice{InputPerMillion: math.Inf(1)}, true) != nil {
		t.Error("a rate that is not a number states no price")
	}
	router := pricingInfo(modelPrice{InputPerMillion: 6, OutputPerMillion: 24, Variable: true}, true)
	if router == nil || !router.Variable || router.InputPerMillion != 6 || router.OutputPerMillion != 24 {
		t.Errorf("a variable SKU must list its ceiling marked variable, got %+v", router)
	}
}

// TestPricingKeysNameTheirUnit pins the wire keys. The standard keys are OpenRouter's,
// per token, as strings; the per-million figures sit only under keys that say so. A
// bare `input`/`output` is what an OpenRouter or Vercel reader takes to be per token,
// so its reappearance would reprice every model a million times over.
func TestPricingKeysNameTheirUnit(t *testing.T) {
	raw, err := json.Marshal(pricingInfo(modelPrice{InputPerMillion: 0.8, OutputPerMillion: 4}, true))
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"prompt":"0.0000008","completion":"0.000004","input_per_million":0.8,"output_per_million":4}`
	if string(raw) != want {
		t.Errorf("pricing block\n got %s\nwant %s", raw, want)
	}
}

// catalogYAML is a config with a bare id an OpenRouter alias serves (gpt-4o, priced
// here at a figure billing never reads), a branded row (kai, owned_by hanzo), an
// unbranded passthrough, and a branded row whose owner is a family prefix.
const catalogYAML = `version: 1
models:
  gpt-4o:
    provider: do-ai
    upstream: openai-gpt-4o
    pricing: {input: 2.50, output: 10.00}
  kai:
    provider: kai
    upstream: kai
    owned_by: hanzo
    pricing: {input: 0.021, output: 0}
  bge-m3:
    provider: do-ai
    upstream: bge-m3
    pricing: {input: 0.02, output: 0}
  zen-voice-mini:
    provider: speech
    upstream: kokoro
    owned_by: hanzo
    pricing: {input: 0, output: 0}
  auto-x:
    provider: do-ai
    upstream: auto-x
    owned_by: openrouter
    pricing: {input: 1, output: 1}
`

// orCatalog adds, beside orBody's two SKUs, the one an alias names (openai/gpt-4o, at
// an OpenRouter price unlike the config's) and a router OpenRouter prices at -1.
const orCatalog = `{"data":[
 {"id":"anthropic/claude-sonnet-4","context_length":200000,"pricing":{"prompt":"0.000003","completion":"0.000015"}},
 {"id":"meta/muse-spark-1.1","context_length":1048576,"pricing":{"prompt":"0.00000125","completion":"0.00000425"}},
 {"id":"openai/gpt-4o","context_length":128000,"pricing":{"prompt":"0.000005","completion":"0.00002"}},
 {"id":"openrouter/auto","context_length":2000000,"pricing":{"prompt":"-1","completion":"-1"}}
]}`

func withListing(t *testing.T) map[string]modelInfo {
	t.Helper()
	fee(t, "20")
	path := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(path, []byte(catalogYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	useCatalog(t, path)
	withOpenRouter(t, orCatalog)
	return indexModels(listAvailableModels())
}

// TestListedPriceIsBilledPrice holds /v1/models to the one price authority: every
// row lists exactly what getModelPriceForOrgOK, the lookup billing charges from,
// returns for its id — and no price where billing has none to state.
func TestListedPriceIsBilledPrice(t *testing.T) {
	byID := withListing(t)
	if len(byID) < 8 {
		t.Fatalf("listed %d rows, want the config and the OpenRouter lineup", len(byID))
	}
	for id, m := range byID {
		want := pricingInfo(getModelPriceForOrgOK(id, ""))
		if (want == nil) != (m.Pricing == nil) || want != nil && !reflect.DeepEqual(*want, *m.Pricing) {
			t.Errorf("%s lists %+v, billing charges %+v", id, m.Pricing, want)
		}
	}

	// The bare id an alias serves bills at the SKU's retail ($5/M × 1.20), so that is
	// what it lists — not the $2.50 its config row states.
	gpt := byID["gpt-4o"]
	if gpt.Pricing == nil || gpt.Pricing.InputPerMillion != 6 || gpt.Pricing.Prompt != "0.000006" {
		t.Errorf("gpt-4o lists %+v, want OpenRouter retail $6/M", gpt.Pricing)
	}
	haiku := pricingInfo(modelPrice{InputPerMillion: 0.80, OutputPerMillion: 4}, true)
	if haiku.Prompt != "0.0000008" || haiku.Completion != "0.000004" {
		t.Errorf("$0.80/$4 per 1M projects as %+v", haiku)
	}
}

// A router OpenRouter prices at -1 routes each call to some SKU of its choosing. It is
// premium and paid-floored, its hold reserves the dearest rate in the catalog, it lists
// that ceiling marked variable, and a call bills at the cost its answer states times
// the margin.
func TestVariableRouterBillsStatedCost(t *testing.T) {
	byID := withListing(t)
	router, ok := byID["openrouter/auto"]
	if !ok {
		t.Fatal("openrouter/auto is not listed")
	}
	want := modelPricingInfo{Prompt: "0.000006", Completion: "0.000024", InputPerMillion: 6, OutputPerMillion: 24, Variable: true}
	if !router.Premium || router.Pricing == nil || !reflect.DeepEqual(*router.Pricing, want) {
		t.Errorf("openrouter/auto lists premium=%v pricing=%+v, want premium and the ceiling %+v marked variable", router.Premium, router.Pricing, want)
	}

	zm, ok := familyLookup("openrouter/auto")
	if !ok || !zm.variable() || zm.MinTier != "paid" || zm.Funding != "prepaid" {
		t.Fatalf("openrouter/auto discovered as %+v", zm)
	}
	// The dearest SKU on both sides is openai/gpt-4o, $5/$20 per 1M, at 1.20 retail.
	if zm.Base.In.String() != "6" || zm.Base.Out.String() != "24" {
		t.Errorf("router ceiling = %s/%s per 1M, want 6/24", zm.Base.In, zm.Base.Out)
	}
	if p := getModelPrice("openrouter/auto"); p.InputPerMillion != 6 || p.OutputPerMillion != 24 || !p.Variable {
		t.Errorf("the hold prices openrouter/auto at %+v, want the 6/24 ceiling", p)
	}
	// 100k prompt tokens at the $6/M input ceiling alone is 60 cents.
	if got := estimateRequestCostCents("openrouter/auto", 100_000, 10_000); got < 60 {
		t.Errorf("the hold for a 100k-token call reserves %d cents, under the ceiling's 60 for input alone", got)
	}

	// The hold and the ledger settle the same exact dollars, and both are the stated
	// cost times the margin: never the ceiling's rate for tokens a cheaper SKU served,
	// and never below what the call cost us.
	debits := captureDebits(t)
	c := visit(http.MethodPost, "/v1/x")
	w := whence{ledger: "acme", ip: c.Fiber().IP(), ctx: c.Context()}
	alice := &iam.User{Owner: "acme", Name: "alice"}
	use := tokens{fresh: 1000, completion: 1000}
	for _, tc := range []struct {
		name string
		cost *int64 // nano-USD the answer says the call cost us
		nano int64
		usd  string
	}{
		{"a $0.50 call", new(int64(500_000_000)), 600_000_000, "0.6"},
		{"a $0.001 call", new(int64(1_000_000)), 1_200_000, "0.0012"},
		{"a call that states no cost", nil, zm.retailNano(tokens{fresh: 1000, cached: 0, completion: 1000}), "0.03"},
	} {
		*debits = (*debits)[:0]
		if nano := recordFamilyUsage(w, openrouterFam, "openrouter/auto", "", nil, &mark{cost: tc.cost}, alice, true, false, "r", use, serving{}, time.Now(), nil, "success", ""); nano != tc.nano {
			t.Errorf("%s settled its hold at %d nano, want %d", tc.name, nano, tc.nano)
		}
		if len(*debits) != 1 || (*debits)[0].usd != tc.usd {
			t.Errorf("%s debited %+v, want one debit of $%s", tc.name, *debits, tc.usd)
		}
	}

	// A catalog with nothing priced cannot bound a router, so it is left out.
	only, err := openrouterCatalog([]byte(`{"data":[{"id":"openrouter/auto","pricing":{"prompt":"-1","completion":"-1"}}]}`))
	if err != nil || len(only) != 0 {
		t.Errorf("an unbounded router decoded as %+v (%v)", only, err)
	}
}

// TestIdsRouteAndSlugsAreRefused: every listed id routes, a bare id and the
// vendor-qualified SKU it aliases route to the same place, and a canonical slug that
// is not an id is refused rather than taken by a family's prefix and billed at a price
// nothing states. A slug another row's id already spells is not published.
func TestIdsRouteAndSlugsAreRefused(t *testing.T) {
	byID := withListing(t)

	bare, qualified := resolveModelRoute("claude-sonnet-4"), resolveModelRoute("anthropic/claude-sonnet-4")
	if bare == nil || qualified == nil {
		t.Fatalf("bare %+v, qualified %+v: both spellings must route", bare, qualified)
	}
	if bare.providerName != qualified.providerName || bare.upstreamModel != qualified.upstreamModel {
		t.Errorf("bare routes to %s/%s, qualified to %s/%s", bare.providerName, bare.upstreamModel, qualified.providerName, qualified.upstreamModel)
	}

	for id, slug := range map[string]string{
		"anthropic/claude-sonnet-4": "anthropic/claude-sonnet-4", // already qualified
		"kai":                       "hanzo/kai",                 // branded: owned_by is the maker
		"zen-voice-mini":            "hanzo/zen-voice-mini",
		"auto-x":                    "openrouter/auto-x", // under a family's prefix
		"bge-m3":                    "",                  // passthrough: owned_by is the server, not the maker
	} {
		if got := byID[id].CanonicalSlug; got != slug {
			t.Errorf("%s canonical_slug = %q, want %q", id, got, slug)
		}
	}
	for id, m := range byID {
		if resolveModelRoute(id) == nil {
			t.Errorf("listed id %s does not route", id)
		}
		if m.CanonicalSlug != "" && m.CanonicalSlug != id && resolveModelRoute(m.CanonicalSlug) != nil {
			t.Errorf("slug %s of %s routes; it must be refused", m.CanonicalSlug, id)
		}
	}

	// Without the refusal the OpenRouter family would take this by its prefix.
	if !openrouterFam.serves("openrouter/auto-x") {
		t.Fatal("the prefix no longer claims openrouter/auto-x, so this test proves nothing")
	}

	// kai's slug is an id the moment OpenRouter lists hanzo/kai, so it is withheld.
	withOpenRouter(t, `{"data":[{"id":"hanzo/kai","context_length":131072,"pricing":{"prompt":"0.00000004","completion":"0"}}]}`)
	byID = indexModels(listAvailableModels())
	if got := byID["kai"].CanonicalSlug; got != "" {
		t.Errorf("kai publishes %q, which is another row's id", got)
	}
	if r := resolveModelRoute("hanzo/kai"); r == nil || r.providerName != "openrouter" {
		t.Errorf("hanzo/kai, an OpenRouter SKU, routes to %+v", r)
	}
}

// TestCatalogDecodesUnderCodex decodes the real /v1/models body the way Codex does
// (codex-rs/codex-api/src/endpoint/models.rs → protocol ModelsResponse): the one field
// it reads is `models`, required, an array of ModelInfo whose every entry must carry
// Codex's required keys. It reads nothing in `data`, which is why the pricing keys and
// the slug are free to live there.
func TestCatalogDecodesUnderCodex(t *testing.T) {
	useCatalog(t, "../conf/models.yaml")
	withOpenRouter(t, orBody)

	body, err := modelListing(nil)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		t.Fatal(err)
	}
	raw, ok := top["models"]
	if !ok {
		t.Fatal("Codex requires `models`; serde fails on a missing field")
	}
	var models []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &models); err != nil || models == nil {
		t.Fatalf("`models` must be a JSON array (serde refuses null): %s", raw)
	}
	for i, m := range models {
		for _, k := range []string{"slug", "display_name", "supported_reasoning_levels", "shell_type", "visibility",
			"supported_in_api", "priority", "support_verbosity", "truncation_policy", "experimental_supported_tools"} {
			if _, ok := m[k]; !ok {
				t.Errorf("models[%d] lacks Codex's required %q", i, k)
			}
		}
	}

	var list modelList
	if err := json.Unmarshal(body, &list); err != nil || len(list.Data) == 0 {
		t.Fatalf("OpenAI `data` list: %v, %d rows", err, len(list.Data))
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

// TestListModelsRichConfigPath covers the prod path: the catalogue built from a
// YAML config. It exercises both an unbranded model (real provider exposed)
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
	useCatalog(t, path)
	byID := indexModels(listAvailableModels())

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
	if gpt.Pricing == nil || gpt.Pricing.InputPerMillion != 2.50 || gpt.Pricing.OutputPerMillion != 10.00 {
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
	if zen.Pricing == nil || zen.Pricing.InputPerMillion != 3.00 || zen.Pricing.OutputPerMillion != 9.60 {
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
// reading the real static route + pricing tables. bge-m3 is unbranded (provider
// exposed + priced); text-embedding-qwen3 is unbranded with no static price;
// zen-scribe is branded (provider omitted). Third-party chat is not in the static
// table at all — it is the OpenRouter family's, discovered at runtime.
//
// It used to name zen4 for the branded case and to skip itself whenever a catalog
// was loaded. zen4 left the static table when the Zen chat family moved to the zen
// service, so on the one path where this ran it could only fail — and it did not
// run, because the load that set the catalog came first in file order and the skip
// took it every time.
func TestListModelsRichStaticPath(t *testing.T) {
	byID := indexModels(listAvailableModels())

	if _, ok := byID["gpt-4o"]; ok {
		t.Error("gpt-4o is listed from the static table — third-party chat belongs to the OpenRouter family")
	}

	bge, ok := byID["bge-m3"]
	if !ok {
		t.Fatal("bge-m3 missing from static listing")
	}
	if bge.Object != "model" || bge.OwnedBy != "do-ai" {
		t.Errorf("bge-m3 core wrong: %+v", bge)
	}
	if bge.Provider != "do-ai" {
		t.Errorf("unbranded bge-m3 should expose provider do-ai, got %q", bge.Provider)
	}
	if bge.Pricing == nil || bge.Pricing.InputPerMillion != 0.02 || bge.Pricing.OutputPerMillion != 0 {
		t.Errorf("bge-m3 static pricing want {0.02,0}, got %+v", bge.Pricing)
	}

	qwen, ok := byID["text-embedding-qwen3"]
	if !ok {
		t.Fatal("text-embedding-qwen3 missing from static listing")
	}
	if qwen.Provider != "do-ai" {
		t.Errorf("unbranded text-embedding-qwen3 provider want do-ai, got %q", qwen.Provider)
	}
	if qwen.Pricing != nil {
		t.Errorf("text-embedding-qwen3 has no static pricing; must be omitted, got %+v", *qwen.Pricing)
	}

	zen, ok := byID["zen-scribe"]
	if !ok {
		t.Fatal("zen-scribe missing from static listing")
	}
	if zen.Provider != "" {
		t.Errorf("branded zen-scribe must NOT expose provider, got %q", zen.Provider)
	}
	if zen.OwnedBy != "hanzo" {
		t.Errorf("zen-scribe owned_by want hanzo, got %q", zen.OwnedBy)
	}
}

// A NAME WE CLAIM IS A MODEL WE SERVE.
//
// owned_by is a claim of authorship, so putting one on a SKU somebody else runs
// says we wrote a model we are reselling. The static table may therefore brand only
// what comes out of our own service — hanzoai/speech, the CPU deployment behind
// zen-voice-mini and the zen-scribe pair. Everything else it names is attributed to
// the provider that serves it.
//
// The rule used to read "no branded model here at all", from when the Zen family
// was entirely chat and moved wholesale to the zen service — which is still true of
// the chat SKUs, and is what a branded route through any OTHER provider would mean:
// one re-hardcoded here instead of discovered. Speech never moved. It is served
// from a deployment we operate, so its names are ours in the way owned_by means,
// and the honest form of the rule is about WHO SERVES a branded model rather than
// about there being none. Kai is the same case: served by the decision service, a
// deployment we operate.
func TestNoStaticBrandedModels(t *testing.T) {
	for _, m := range listAvailableModels() {
		route := modelRoutes[m.ID]
		if route.ownedBy != "" && route.providerName != "speech" && route.providerName != object.KaiName {
			t.Errorf("static table brands %q (owned_by=%s) on provider %q — a SKU we do not serve is attributed, not claimed, and a zen SKU is discovered rather than hardcoded",
				m.ID, route.ownedBy, route.providerName)
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
