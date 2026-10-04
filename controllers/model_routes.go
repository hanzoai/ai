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
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
)

// modelRouteFallback is an alternate provider+upstream for failover.
type modelRouteFallback struct {
	providerName  string
	upstreamModel string
	contextWindow int // window THIS provider serves it at; 0 = same as primary
}

// modelRoute maps a user-facing model name to an upstream provider and model ID.
type modelRoute struct {
	providerName  string               // provider name: a seeded row ("do-ai", "speech", …) or a model family ("openrouter", "zen", "enso")
	upstreamModel string               // Model ID sent to upstream API
	fallbacks     []modelRouteFallback // Alternate providers tried on error
	premium       bool                 // Requires positive balance
	hidden        bool                 // If true, excluded from /api/models listing (still callable)
	ownedBy       string               // Override for owned_by in model listing (default: providerName)
	contextWindow int                  // Max context tokens as THIS provider serves it; 0 = undeclared (caller uses the floor)
	maxOutput     int                  // Max completion tokens (from the upstream catalog); 0 = undeclared
	vision        bool                 // Accepts image input (OpenAI image_url parts) — set only from a live probe
	tools         bool                 // Supports function/tool calling — set only from a live probe
	outputs       []string             // Kinds of answer the model produces (e.g. "decision"); nil = not advertised
	created       int64                // When the model was released (Unix seconds); 0 = not recorded
	description   string               // One plain sentence on what the model is, for a route of our own; "" = none stated
}

// releasedOr is the route's release time, or now when nothing records one.
func (r modelRoute) releasedOr(now int64) int64 {
	if r.created > 0 {
		return r.created
	}
	return now
}

// modelRoutes is the static routing table. Keys are user-facing model names
// (case-insensitive lookup via resolveModelRoute). Values describe where and
// how to forward the request.
//
// Third-party CHAT models are not here. They are served by the OpenRouter family
// under their OpenRouter ids, and the short ids callers know them by are aliases
// the family resolves (openrouter_alias.go). What remains is what OpenRouter does
// not serve: our own speech service, the embedding, image and video models (the
// family relays chat only), DigitalOcean's inference routers, and the few chat
// models OpenRouter does not carry.
var modelRoutes = map[string]modelRoute{
	// ── Chat models with no OpenRouter equivalent ── served where they always were ──
	// OpenRouter lists no priced SKU of the same model at the same size for these.
	// Claude 3.5 Haiku, 3.7 Sonnet and Opus 4 are retired there; nemotron-3-nano-omni
	// is listed only as a free route, whose terms let the vendor keep the exchange;
	// the 12B Nemotron VL and Cogito are absent. gemma-3-31b and mistral-small name
	// no single model to point at: Gemma 3 has no 31B, and OpenRouter carries four
	// Mistral Small releases of which the id names none.
	"claude-3-5-haiku":      {providerName: "do-ai", upstreamModel: "anthropic-claude-3.5-haiku"},
	"claude-3-7-sonnet":     {providerName: "do-ai", upstreamModel: "anthropic-claude-3.7-sonnet"},
	"claude-opus-4":         {providerName: "do-ai", upstreamModel: "anthropic-claude-opus-4"},
	"gemma-3-31b":           {providerName: "do-ai", upstreamModel: "gemma-3-31b"},
	"mistral-small":         {providerName: "do-ai", upstreamModel: "mistral-small"},
	"nemotron-3-nano-omni":  {providerName: "do-ai", upstreamModel: "nemotron-3-nano-omni"},
	"nemotron-nano-12b-vl":  {providerName: "do-ai", upstreamModel: "nemotron-nano-12b-v2-vl"},
	"nemotron-nano-vl":      {providerName: "do-ai", upstreamModel: "nemotron-nano-12b-v2-vl"},
	"fireworks/cogito-671b": {providerName: "fireworks", upstreamModel: "accounts/cogito/models/cogito-671b-v2-p1", premium: true, hidden: true},

	// ── DO-AI embeddings ── served at POST /v1/embeddings (passthrough) ──
	// User-facing name == DO catalog id (already clean public names). owned_by
	// defaults to the do-ai provider (surfaced), matching every other unbranded
	// passthrough. Verified live: each returns real vectors (dims noted).
	// text-embedding-qwen3 is the Hanzo-native embedder the knowledge stores
	// default to (object/init.go defaultEmbedModel) — the route the catalog
	// comment always promised. Verified live: 1024-dim, matching KB_EMBED_DIMS.
	"text-embedding-qwen3":       {providerName: "do-ai", upstreamModel: "qwen3-embedding-0.6b"},       // 1024-dim
	"bge-m3":                     {providerName: "do-ai", upstreamModel: "bge-m3"},                     // 1024-dim, multilingual
	"e5-large-v2":                {providerName: "do-ai", upstreamModel: "e5-large-v2"},                // 1024-dim
	"gte-large-en-v1.5":          {providerName: "do-ai", upstreamModel: "gte-large-en-v1.5"},          // 1024-dim
	"all-mini-lm-l6-v2":          {providerName: "do-ai", upstreamModel: "all-mini-lm-l6-v2"},          // 384-dim
	"multi-qa-mpnet-base-dot-v1": {providerName: "do-ai", upstreamModel: "multi-qa-mpnet-base-dot-v1"}, // 768-dim

	// ── Hanzo Speech ── our own service (ghcr.io/hanzoai/speech) on CPU nodes ──
	// A VOICE speaks and a SCRIBE writes down what is said. The DIRECTION is not
	// in either name because the route already carries it — /v1/audio/speech
	// synthesizes and /v1/audio/transcriptions transcribes — so a `-tts` or
	// `-stt` suffix would state the same fact twice and leave two places to
	// disagree. Same reason the chat models are not called `zen5-chat`.
	//
	// These used to be published under their UPSTREAM ids with owned_by hanzo,
	// on the grounds that the name needed no translation. That is the one thing
	// the house rule forbids: it puts somebody else's model name on our catalog
	// AND claims it as ours. The embeddings it was reasoned from are not the same
	// case — they carry owned_by do-ai, so they are attributed rather than
	// claimed. Priced per second heard and per character spoken (sttNanoPerSecond,
	// ttsNanoPerChar); `outputs` keeps a chat picker from offering either.
	//
	// zen-scribe is Parakeet, which hears 25 European languages and hands any
	// other to Whisper inside the speech service, so one id covers every language.
	"zen-voice-mini": {providerName: "speech", upstreamModel: "kokoro", ownedBy: "hanzo", outputs: []string{"audio"}, description: "Hanzo's text-to-speech voice, served at /v1/audio/speech."},
	"zen-scribe":     {providerName: "speech", upstreamModel: "parakeet", ownedBy: "hanzo", outputs: []string{"transcript"}, description: "Hanzo's speech-to-text model, which writes down what is said in any language, served at /v1/audio/transcriptions."},

	// The upstream ids and the retired zen-scribe-mini stay CALLABLE and leave the
	// listing, the same shape every other upstream-named route here takes. A rename
	// that 404s the name people are already sending is a wire break; a rename that
	// stops advertising it is not. whisper-small names a model the service no longer
	// carries, so it reaches Whisper.
	"zen-scribe-mini": {providerName: "speech", upstreamModel: "parakeet", ownedBy: "hanzo", hidden: true, outputs: []string{"transcript"}},
	"parakeet":        {providerName: "speech", upstreamModel: "parakeet", ownedBy: "hanzo", hidden: true, outputs: []string{"transcript"}},
	"whisper":         {providerName: "speech", upstreamModel: "whisper", ownedBy: "hanzo", hidden: true, outputs: []string{"transcript"}},
	"whisper-small":   {providerName: "speech", upstreamModel: "whisper", ownedBy: "hanzo", hidden: true, outputs: []string{"transcript"}},
	"kokoro":          {providerName: "speech", upstreamModel: "kokoro", ownedBy: "hanzo", hidden: true, outputs: []string{"audio"}},

	// ── DO-AI image (diffusion) ── Stable Diffusion 3.5 Large ────────────
	// Unlike the fal FLUX/SDXL models (async-invoke), SD 3.5 Large is served on
	// the SYNCHRONOUS OpenAI /images/generations shape (isDOAIImageModel==false
	// → client.Images.Generate). getOpenAiModelType classifies it as an image
	// model via the "stable-diffusion" pattern. Verified live: HTTP 200, b64.
	"stable-diffusion-3.5-large": {providerName: "do-ai", upstreamModel: "stable-diffusion-3.5-large"},

	// ── DO-AI Inference Router ── auto-selects a foundation model per request ─
	// DO's model router: send a chat completion and it picks the best upstream
	// (returns the chosen model in the response). Callable as plain chat
	// (getOpenAiModelType defaults to "Chat"). Billed at the underlying model's
	// rate (router adds no cost — public preview). Verified live: HTTP 200.
	"router:general":                 {providerName: "do-ai", upstreamModel: "router:general"},
	"router:knowledge-base-document": {providerName: "do-ai", upstreamModel: "router:knowledge-base-document"},
	"router:software-engineering":    {providerName: "do-ai", upstreamModel: "router:software-engineering"},
	"router:software-engineering-01": {providerName: "do-ai", upstreamModel: "router:software-engineering-01"},
	"router:writing":                 {providerName: "do-ai", upstreamModel: "router:writing"},

	// ── DO-AI text-to-video ── served at POST /v1/videos/generations ─────
	// wan2-2-t2v-a14b is the only text-to-video model in the do-ai catalog. It
	// is served on the OpenAI Sora-style async /v1/videos API (create → poll →
	// download), NOT the fal async-invoke image path (which 404s it). Callable
	// directly by its catalog id (unbranded passthrough — no ownedBy, so the
	// listing surfaces the do-ai upstream like the embeddings passthroughs).
	// getOpenAiModelType classifies it via isDOAIVideoModel → the videos path.
	// premium: true — ALL video is premium; the flag tags the usage record as
	// premium. A single t2v inference is minutes of A14B GPU compute (~40¢), billed
	// per video via videoCostCents through the one metering path. Access is gated
	// purely on a positive prepaid balance (there is no free credit), so a $0 signup
	// is refused at the balance gate before ever reaching video.
	"wan2-2-t2v-a14b": {providerName: "do-ai", upstreamModel: "wan2-2-t2v-a14b", premium: true},

	// ── Hanzo Decision ── served at POST /v1/decisions by the decision service ──
	// Kai is Hanzo's decision model; the Jev ids are forwarded by the same service
	// to OpenRouter, so they stay callable and leave the listing. A decision answers
	// typed questions, not a chat turn, and `outputs` says so to every catalog.
	// Released: kai when api.hanzo.ai first served it (cloud f80ce7a0a, ai v1.833.242),
	// the Jev ids when OpenRouter listed them (its `created`).
	"kai":                  {providerName: object.KaiName, upstreamModel: "kai", ownedBy: "hanzo", outputs: []string{"decision"}, created: 1790550362, description: "Hanzo's decision model, which answers typed questions at /v1/decisions rather than a chat turn."},
	"typesafe/jev-1.13":    {providerName: object.KaiName, upstreamModel: "typesafe/jev-1.13", ownedBy: "typesafe", outputs: []string{"decision"}, hidden: true, created: 1789689684},
	"~typesafe/jev-latest": {providerName: object.KaiName, upstreamModel: "~typesafe/jev-latest", ownedBy: "typesafe", outputs: []string{"decision"}, hidden: true, created: 1789689685},
}

// modelCountByProvider returns, per provider name, how many user-facing model
// keys route to it. It counts from the ACTIVE routing table: the YAML config
// (conf/models.yaml, the runtime source of truth in prod) when loaded, else the
// static modelRoutes map. Counting is done programmatically from the route
// table's providerName field — never hardcoded — so adding/removing a model
// updates the count automatically.
//
// Note this counts by route.providerName, which is the DB provider whose key/URL
// serves the request. Branded families (every zen model, the OpenAI-owned
// embeddings) route THROUGH an infrastructure provider (do-ai), so their keys are
// attributed to that serving provider — a provider record like "zen" whose name
// no route targets will report 0. That is accurate: the "zen" provider record
// holds a key/URL but the zen MODELS are served via the do-ai route. DB-defined
// per-org routes (/v1/*-model-route) are not included in this static baseline;
// documented as an accepted baseline for the admin management view.
func modelCountByProvider() map[string]int {
	counts := map[string]int{}
	if cfg := GetModelConfig(); cfg != nil {
		cfg.mu.RLock()
		for _, route := range cfg.routes {
			counts[route.providerName]++
		}
		cfg.mu.RUnlock()
		return counts
	}
	for _, route := range modelRoutes {
		counts[route.providerName]++
	}
	return counts
}

// resolveModelRoute looks up a user-facing model name and returns its route.
// Lookup is case-insensitive. Checks DB routes (global "admin" owner) first,
// then falls back to YAML config, then static map.
// Returns nil if the model is not in the routing table.
func resolveModelRoute(model string) *modelRoute {
	return resolveModelRouteForOrg(model, "")
}

// routeForPrompt resolves a model's route and, when the prompt will not fit the
// provider that route names, upgrades it to a provider that can hold it.
//
// The same model is served at different sizes by different providers, so a
// prompt that is too large is not a property of the MODEL — it is a property of
// WHERE we were about to send it. DigitalOcean serves glm-5.2 at 262,144, so a
// 400K prompt to zen5 used to be refused outright:
//
//	the token count: [400000] exceeds the model: [glm-5.2]'s
//	maximum token count: [262144]
//
// but deepseek-v4-pro on the same platform holds 1M (measured). Refusing was
// never necessary; we simply were not looking. So the fallback chain, which
// already exists to survive a provider being DOWN, is also consulted for a
// provider being TOO SMALL. Failover and capability selection are the same
// question — "can this provider serve me?" — and so they are the same mechanism.
//
// The upgraded route keeps everything else about the request intact (premium
// gate, owner, identity prompt): only the provider and upstream move, and the
// caller cannot tell the difference except that the request now succeeds.
//
// promptTokens <= 0 means the size is unknown; the route is returned unchanged.
func routeForPrompt(model string, orgId string, promptTokens int) *modelRoute {
	route := resolveModelRouteForOrg(model, orgId)
	if route == nil || promptTokens <= 0 {
		return route
	}

	cfg := GetModelConfig()
	if cfg == nil {
		return route
	}

	provider, upstream, window, ok := cfg.RouteForContext(model, promptTokens)
	if !ok || provider == route.providerName && upstream == route.upstreamModel {
		return route // it already fits, or nothing better exists
	}

	log.Info("context routing: %s prompt is %d tokens, more than %s/%s can hold — routing to %s/%s (%d)",
		model, promptTokens, route.providerName, route.upstreamModel, provider, upstream, window)

	upgraded := *route // copy: never mutate the shared route
	upgraded.providerName = provider
	upgraded.upstreamModel = upstream
	upgraded.contextWindow = window
	return &upgraded
}

// resolveModelRouteForOrg looks up a model route with per-org override support.
// Resolution order: DB org-specific -> DB global ("admin") -> YAML config -> static map.
func resolveModelRouteForOrg(model string, orgId string) *modelRoute {
	// Check DB routes first (org-specific -> global)
	dbRoute, err := object.ResolveModelRouteFromDB(strings.ToLower(model), orgId)
	if err == nil && dbRoute != nil {
		r := &modelRoute{
			providerName:  dbRoute.Provider,
			upstreamModel: dbRoute.Upstream,
			premium:       dbRoute.Premium,
			hidden:        dbRoute.Hidden,
			ownedBy:       dbRoute.OwnedBy,
		}
		if dbRoute.Fallback1 != "" {
			r.fallbacks = append(r.fallbacks, modelRouteFallback{
				providerName:  dbRoute.Fallback1,
				upstreamModel: dbRoute.Fallback1Up,
			})
		}
		if dbRoute.Fallback2 != "" {
			r.fallbacks = append(r.fallbacks, modelRouteFallback{
				providerName:  dbRoute.Fallback2,
				upstreamModel: dbRoute.Fallback2Up,
			})
		}
		return r
	}

	// YAML config (the runtime source of truth) — or, when none is loaded, the
	// static map. Either resolves everything ai serves directly.
	if cfg := GetModelConfig(); cfg != nil {
		if route := cfg.ResolveRoute(model); route != nil {
			return route
		}
	} else if route, ok := modelRoutes[strings.ToLower(model)]; ok {
		return &route
	}

	// A canonical slug is refused, not routed. It names a listed row but is not an id,
	// and a family would otherwise take it by prefix ("zenlm/zen5" starts with "zen")
	// and bill it at a price nothing states. A SKU a family lists under that very name
	// still routes.
	if listedSlug(model) {
		if _, known := familyLookup(model); !known {
			return nil
		}
	}

	// Model families (Zen, Enso, OpenRouter): any family SKU routes to its family
	// service, which owns the SKU→upstream mapping, identity, and reasoning. ai holds no
	// such route of its own (hip-00NN). An alias a family resolves (openrouter_alias.go)
	// is reached here, so a DB row or a config entry for the same id still wins over it.
	//
	// Per-tier gate (Seam B): when the caller's commerce tier is CONFIDENTLY below the
	// SKU's advertised min_tier, drop the route (nil) so the auto-router's `known`
	// predicate degrades to the next servable — a free user's `auto` falls enso→
	// enso-flash. Fail-safe: familyTierAllowed admits on any uncertainty, so the direct
	// call path (which resolves its authoritative route with orgId "" ⇒ empty subject ⇒
	// admit) is never degraded here; it reaches the family pipe and the 403 access gate
	// (Seam A). Only the auto path, which carries the real orgId, degrades.
	// The funding floor joins the tier floor here, but ONLY for a caller we can name.
	// This is route SELECTION, not authorization: an empty subject means the direct path
	// resolving its authoritative route, where dropping to nil would leave the request
	// with no route at all rather than deferring to the access gate. Refusing to SPEND is
	// the serve gate's job (familyAccessAllowed, which fails closed on exactly this
	// uncertainty); refusing to PREFER is this one's. With a real subject both floors
	// degrade `auto` away from a SKU the serve path would refuse anyway.
	if subject := subjectFromOrg(orgId); subject != "" {
		if _, ok := familyServing(model); ok &&
			(!familyTierAllowed(subject, model) || !familyFundingAllowed(subject, model)) {
			return nil
		}
	}
	return familyPassthroughRoute(model)
}

// modelInfo is the JSON shape returned by the /v1/models endpoint.
//
// The first five fields are the OpenAI-compatible core (id, object, created,
// owned_by, premium) and MUST stay stable and always-present — external
// consumers (Claude Code, Codex, OpenAI SDKs) parse them by exact name. The
// remaining fields are ADDITIVE Hanzo enrichments sourced only from data ai
// already holds (the route table + pricing tables + family discovery); each is
// omitempty, so a standard OpenAI client ignores them and any datum ai lacks is
// simply absent — never fabricated. The context window is present for the family
// SKUs (Zen, Enso), whose served window ai discovers and pins; models with no
// window datum omit it.
type modelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
	Premium bool   `json:"premium"`

	// Additive enrichment (omitempty — present only when ai has the datum).
	CanonicalSlug     string            `json:"canonical_slug,omitempty"`     // the id qualified by its maker, OpenRouter's field and form ("hanzo/kai", "anthropic/claude-sonnet-4"); see canonicalSlug
	Class             string            `json:"class,omitempty"`              // premium | ours | free: ClassOf, the class the usage policy is asked about; every listed row carries it
	Family            string            `json:"family,omitempty"`             // the Hanzo family the model belongs to (enso, zen, kai, jev, zoo); absent for a third-party model. See lineage
	Name              string            `json:"name,omitempty"`               // display name where the source states one (OpenRouter's, without its "<Vendor>: " lead — owned_by carries the vendor)
	Description       string            `json:"description,omitempty"`        // what the source says the model is: OpenRouter's description, the family's, or one sentence on a route of our own
	Provider          string            `json:"provider,omitempty"`           // serving provider, surfaced for unbranded passthroughs; omitted for branded models (owned_by already carries the public owner — see hip-00NN)
	ContextWindow     int               `json:"context_window,omitempty"`     // max tokens the SKU is served at; surfaced from family discovery and pinned for the flagship SKUs (enso/zen5 = 1,000,000) so clients (Codex, Claude Code) size context honestly
	MaxOutputTokens   int               `json:"max_output_tokens,omitempty"`  // max completion tokens (upstream catalog); lets clients cap output honestly
	Inputs            []string          `json:"inputs,omitempty"`             // kinds of input the model takes, in OpenRouter's words ("text", "image", "audio", "file", "video"); absent ⇒ not advertised
	Outputs           []string          `json:"outputs,omitempty"`            // kinds of answer the model produces ("text", "audio", "image", "embeddings", "rerank"); absent ⇒ not advertised. A caller choosing a model for a chat turn needs it: the free lineup carries music models and a classifier beside the chat models, and a price alone cannot tell them apart
	SupportsVision    bool              `json:"supports_vision,omitempty"`    // model accepts image input, as its family or a live probe states; absent ⇒ not advertised, never a fabricated yes
	SupportsTools     bool              `json:"supports_tools,omitempty"`     // model supports function/tool calling, as its catalog or a live probe states
	SupportsReasoning bool              `json:"supports_reasoning,omitempty"` // model takes a reasoning request, as its catalog states
	Pricing           *modelPricingInfo `json:"pricing,omitempty"`            // per-token and per-1M USD, each key naming its unit; only when ai holds real pricing
	Access            *modelAccessInfo  `json:"access,omitempty"`             // present only for a gated (limited-preview) SKU; carries the caller's standing (waitlist|requested|granted)
}

// publicProvider returns the provider name to surface in /v1/models, or "" to
// omit it. A branded model declares its public owner in ownedBy (every zen model
// is owned_by:"hanzo"; the OpenAI-owned embeddings are owned_by:"openai"), and
// the public owner already travels in owned_by — so the serving provider is
// omitted rather than echoed as a redundant duplicate. An unbranded passthrough
// declares no owner, so providerName is its public owned_by and is surfaced as
// the serving provider. See hip-00NN for the catalog/ownership contract.
func publicProvider(route modelRoute) string {
	if route.ownedBy != "" {
		return ""
	}
	return route.providerName
}

// listRouteProviders returns the distinct providers actually serving the LISTED
// catalog, sorted. It takes the SAME branch as listAvailableModels — config when
// one is loaded, the static table otherwise, plus the configured families — so
// the provider set and the model list are two projections of one fact and cannot
// disagree. That is the whole point: the previous endpoint answered from the
// provider DB instead, which is a second list, and it drifted (it named a family
// whose models were not listed, and named none of the families' own).
//
// Secret-free by construction: it projects only names. It does NOT say which
// provider serves which model — publicProvider deliberately withholds that, and
// this returns a set, not a mapping.
func listRouteProviders() []string {
	seen := map[string]struct{}{}

	if cfg := GetModelConfig(); cfg != nil {
		for _, p := range cfg.ListRouteProviders() {
			seen[p] = struct{}{}
		}
	} else {
		for _, route := range modelRoutes {
			if route.hidden || route.providerName == "" {
				continue
			}
			seen[route.providerName] = struct{}{}
		}
	}

	// A configured family serves models of its own (discovered, so absent from
	// both tables above). An unconfigured one serves nothing and must not appear.
	for _, f := range modelFamilies {
		if f.enabled() {
			seen[f.name] = struct{}{}
		}
	}

	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ── the listed catalogue ─────────────────────────────────────────────────────
//
// The catalogue is a function of two things: the loaded config, and what each family
// last discovered. Both move on a five-minute scale and the endpoint is asked
// thousands of times in between, so it is built when they move and held.
//
// Held, rather than rebuilt per caller, because a build is not cheap: it merges and
// sorts every family's lineup into the routing table, marshals ~100 KB, and resolves
// each SKU's serving address to report the window it is served at — one admin-row
// read per listed model, measured at 531 for a 534-model catalogue. A request that
// finds the inputs unmoved does one read per family and no work at all.

// discovery is where a family is served from and when it last discovered its
// lineup. Together with the loaded config it is everything the catalogue is built
// from, so equal discoveries mean an equal catalogue.
type discovery struct {
	url string
	at  time.Time
}

// discoveries reports every family's discovery, refreshing any snapshot past its
// TTL. The refresh stays on the read path so a family keeps its own schedule and an
// operator repointing one at admin.hanzo.ai is seen on the next request.
func discoveries() []discovery {
	out := make([]discovery, len(modelFamilies))
	for i, f := range modelFamilies {
		url := f.fresh()
		f.mu.RLock()
		out[i] = discovery{url: url, at: f.fetchedAt}
		f.mu.RUnlock()
	}
	return out
}

// modelCatalog holds one build: the models, the public body, and the inputs they
// came from.
type modelCatalog struct {
	mu     sync.Mutex
	cfg    *ModelConfig
	cfgAt  time.Time
	fam    []discovery
	models []modelInfo
	body   []byte
	err    error
}

// listing is the held catalogue every endpoint answers from.
var listing modelCatalog

// get returns the held models, the public body, and the error from marshalling it.
// All three come from one build, which runs when the config or a family's discovery
// has moved since the last one. The build happens under the lock, so a burst of
// callers arriving on a moved input does one build between them, not one each.
func (c *modelCatalog) get() ([]modelInfo, []byte, error) {
	cfg := GetModelConfig()
	var cfgAt time.Time
	if cfg != nil {
		cfgAt = cfg.ChangedAt()
	}
	fam := discoveries()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.models == nil || c.cfg != cfg || c.cfgAt != cfgAt || !slices.Equal(c.fam, fam) {
		c.models = c.build(cfg)
		c.body, c.err = json.Marshal(modelListEnvelope(c.models))
		c.cfg, c.cfgAt, c.fam = cfg, cfgAt, fam
	}
	return c.models, c.body, c.err
}

// build renders the catalogue from the config's routing table, or from the static
// table when no config is loaded, overlaid in the config case with every family's
// discovered lineup and sorted by name. Hidden models (provider-prefixed aliases,
// upstream-named routes) are excluded from the listing but remain callable via the
// completions endpoint.
//
// Each row is priced by getModelPriceForOrgOK, the lookup billing charges from, so a
// listed rate is the billed rate by construction: a bare id an OpenRouter alias serves
// lists the SKU's retail, not a config figure billing never reads. And each row's
// canonical slug is stamped only where the slug routes nowhere else; the published
// set is kept in slugs, which resolveModelRouteForOrg refuses.
func (c *modelCatalog) build(cfg *ModelConfig) []modelInfo {
	var models []modelInfo
	if cfg != nil {
		models = mergeFamilyModels(cfg.ListModels())
	} else {
		models = staticModels()
	}
	ids := make(map[string]bool, len(models))
	for _, m := range models {
		ids[strings.ToLower(m.ID)] = true
	}
	published := make(map[string]string, len(models))
	for i := range models {
		m := &models[i]
		m.Pricing = pricingInfo(getModelPriceForOrgOK(m.ID, ""))
		slug := canonicalSlug(*m)
		key := strings.ToLower(slug)
		switch {
		case slug == m.ID:
			m.CanonicalSlug = slug
		case slug != "" && !ids[key] && !routes(key):
			m.CanonicalSlug = slug
			published[key] = m.ID
		}
	}
	slugs.Store(&published)

	// Class and family resolve each row's route, and resolving an id no config row
	// names asks listedSlug, which builds the catalogue when no slugs are stored. So
	// they come after the store above: before it, the first build on a replica would
	// wait on its own lock.
	for i := range models {
		m := &models[i]
		m.Class = ClassOf(m.ID)
		m.Family = lineage(m.ID, m.OwnedBy)
	}
	return models
}

// slugs is every published canonical slug that is not its row's id, lowercased, from
// the last catalogue build.
var slugs atomic.Pointer[map[string]string]

// routes reports that id names a route of its own — config, static table, a family
// SKU or alias, or a global route row — which a slug spelled the same must not shadow.
// It matches exactly: a family's prefix is not a claim on a name.
func routes(id string) bool {
	if cfg := GetModelConfig(); cfg != nil {
		if cfg.ResolveRoute(id) != nil {
			return true
		}
	} else if _, ok := modelRoutes[id]; ok {
		return true
	}
	if _, ok := familyLookup(id); ok {
		return true
	}
	r, err := object.ResolveModelRouteFromDB(id, "")
	return err == nil && r != nil
}

// listedSlug reports that model is a published canonical slug, which names a listed
// row without being its id. The first call on a replica builds the catalogue; the
// build resolves routes only after it has stored the slugs, so it cannot come back
// here holding the lock.
func listedSlug(model string) bool {
	m := slugs.Load()
	if m == nil {
		listing.get()
		if m = slugs.Load(); m == nil {
			return false
		}
	}
	_, ok := (*m)[strings.ToLower(strings.TrimSpace(model))]
	return ok
}

// canonicalSlug is the row's id qualified by the maker `owned_by` names, the form
// OpenRouter lists ids in: "hanzo/kai", "openai/text-embedding-3-small". An id that
// already carries a vendor segment ("anthropic/claude-sonnet-4") is its own slug.
//
// An unbranded passthrough gets none. Its owned_by is the provider that SERVES it
// (the row says so in `provider`), so qualifying by it would name DigitalOcean as the
// maker of Claude; ai holds no maker for it and publishes none.
//
// The slug identifies; `id` is what a caller sends, and two rows may share a slug when
// two routes serve one model. The id itself stays bare because every entry path bills,
// gates and meters on the raw id string: a second spelling is only safe once those
// paths canonicalize it first.
func canonicalSlug(m modelInfo) string {
	switch {
	case strings.Contains(m.ID, "/"):
		return m.ID
	case m.Provider != "" || m.OwnedBy == "":
		return ""
	}
	return strings.ToLower(m.OwnedBy) + "/" + m.ID
}

// staticModels lists the static routing table, for when no config is loaded.
func staticModels() []modelInfo {
	now := time.Now().Unix()
	models := make([]modelInfo, 0, len(modelRoutes))

	for name, route := range modelRoutes {
		if route.hidden {
			continue
		}
		owner := route.ownedBy
		if owner == "" {
			owner = route.providerName
		}
		models = append(models, modelInfo{
			ID:              name,
			Object:          "model",
			Created:         route.releasedOr(now),
			OwnedBy:         owner,
			Premium:         route.premium,
			Provider:        publicProvider(route),
			ContextWindow:   route.contextWindow,
			MaxOutputTokens: route.maxOutput,
			SupportsVision:  route.vision,
			SupportsTools:   route.tools,
			Outputs:         route.outputs,
			Description:     route.description,
		})
	}

	sort.Slice(models, func(i, j int) bool {
		return models[i].ID < models[j].ID
	})

	return models
}

// listAvailableModels returns the listed catalogue, sorted by name.
//
// It is a COPY, Access included — that field is the one thing a listing says about
// its reader, and annotateModelAccess writes it. Handing out the held slice would
// let one caller's standing be read by the next.
func listAvailableModels() []modelInfo {
	models, _, _ := listing.get()
	out := slices.Clone(models)
	for i := range out {
		if a := out[i].Access; a != nil {
			standing := *a
			out[i].Access = &standing
		}
	}
	return out
}

// modelListing is the /v1/models answer for a caller, as it goes on the wire.
//
// Without a verified principal that is the public body, marshalled once per build
// and handed to everyone. With one it is a copy annotated with that caller's own
// standing on the gated SKUs, which is theirs alone and so is marshalled for them.
func modelListing(user *iam.User) ([]byte, error) {
	if user == nil {
		_, body, err := listing.get()
		return body, err
	}
	models := listAvailableModels()
	annotateModelAccess(models, user)
	return json.Marshal(modelListEnvelope(models))
}

// modelListEnvelope preserves the standard OpenAI model-list response while
// adding Codex's optional private catalog envelope. Current Codex releases GET
// the provider's /v1/models even when remote_models is disabled; an empty
// `models` list tells Codex to retain its safe built-in fallback metadata
// instead of failing to decode the otherwise-valid OpenAI `data` list.
func modelListEnvelope(models []modelInfo) modelList {
	return modelList{Object: "list", Data: models, Models: []modelInfo{}}
}

// modelList is the catalogue as /v1/models answers it.
//
// It is a struct rather than the map it used to be so the shape is READABLE:
// this is the most-called address on the surface, and a map literal has no type
// for the published document to reflect, so every generated client handed the
// caller an untyped bag where the catalogue should be.
//
// Models is the empty array a long-retired client read the list from. It stays
// empty and it stays present — dropping it changes the wire — and it is []T
// rather than nil so it marshals as [] the way it always has.
type modelList struct {
	Object string      `json:"object"`
	Data   []modelInfo `json:"data"`
	Models []modelInfo `json:"models"`
}
