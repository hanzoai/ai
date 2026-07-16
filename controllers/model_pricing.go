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
	"math"
	"strings"

	"github.com/hanzoai/ai/object"
)

// StarterCreditDollars is the amount granted to new users as free credit.
// Premium models require a balance above this threshold to ensure the user
// has added real funds beyond the starter credit.
const StarterCreditDollars = 5.00

// modelPrice defines per-model economics in dollars per 1M tokens. Input/Output/Cache*
// are the CUSTOMER PRICE (what the org is billed); CostIn/CostOut are the PROVIDER COGS
// (what it costs Hanzo to serve the call). Cost defaults to price when unset (0), so a
// model that declares only a price bills identically and books a zero margin — a real
// margin appears only where a genuinely lower COGS is configured.
type modelPrice struct {
	InputPerMillion      float64 // $ per 1M input tokens (customer price)
	OutputPerMillion     float64 // $ per 1M output tokens (customer price)
	CacheReadPerMillion  float64 // $ per 1M cache-read tokens (0 = use InputPerMillion)
	CacheWritePerMillion float64 // $ per 1M cache-write tokens (0 = use InputPerMillion)
	CostInPerMillion     float64 // $ per 1M input tokens (provider COGS; 0 = use InputPerMillion)
	CostOutPerMillion    float64 // $ per 1M output tokens (provider COGS; 0 = use OutputPerMillion)
}

// costInputPerMillion is the effective provider COGS for input tokens: the explicit
// CostInPerMillion when set, else the customer price. The default keeps a model that
// configures no COGS byte-identical to before COGS existed (cost == price ⇒ margin 0).
func (p modelPrice) costInputPerMillion() float64 {
	if p.CostInPerMillion > 0 {
		return p.CostInPerMillion
	}
	return p.InputPerMillion
}

// costOutputPerMillion is the effective provider COGS for output tokens: CostOutPerMillion
// when set, else the customer price (same zero-margin default).
func (p modelPrice) costOutputPerMillion() float64 {
	if p.CostOutPerMillion > 0 {
		return p.CostOutPerMillion
	}
	return p.OutputPerMillion
}

// modelPricingInfo is the public, omitempty pricing block embedded in the
// /v1/models response (modelInfo.Pricing). It is expressed in US dollars per
// one million tokens and is emitted ONLY when ai holds real pricing for the
// model — never synthesized from defaults.
type modelPricingInfo struct {
	Input  float64 `json:"input"`  // USD per 1M input tokens
	Output float64 `json:"output"` // USD per 1M output tokens
}

// pricingInfo projects an internal modelPrice into the public pricing block, or
// returns nil (→ dropped by omitempty) when there is no genuine pricing to
// expose. ok reports whether a real per-model pricing entry was found; a
// degenerate all-zero entry is treated as "no pricing" so the listing never
// emits a meaningless {"input":0,"output":0}.
func pricingInfo(p modelPrice, ok bool) *modelPricingInfo {
	if !ok || (p.InputPerMillion <= 0 && p.OutputPerMillion <= 0) {
		return nil
	}
	return &modelPricingInfo{Input: p.InputPerMillion, Output: p.OutputPerMillion}
}

// staticModelPrice looks up real per-model pricing from the static tables only.
// Unlike getModelPrice it neither queries the DB nor falls back to a default
// price: ok is false when ai holds no genuine pricing, letting the /v1/models
// listing OMIT pricing rather than fabricate it.
func staticModelPrice(model string) (modelPrice, bool) {
	// Zen family: the discovered retail price is the source of truth (hip-00NN).
	if p, ok := familyModelPrice(model); ok {
		return p, true
	}

	m := strings.ToLower(model)
	if price, ok := modelPricing[m]; ok {
		return price, true
	}
	if base, ok := aliasPricing[m]; ok {
		if price, ok := modelPricing[base]; ok {
			return price, true
		}
	}
	return modelPrice{}, false
}

// modelPricing maps upstream model identifiers to their pricing.
// Keyed by user-facing model name (lowercase). Pricing reflects actual
// upstream costs with Hanzo margin applied.
//
// Sources:
//   - DO-AI: DigitalOcean GenAI Platform pricing (Feb 2026)
//   - Fireworks: Fireworks AI pricing (Feb 2026)
//   - OpenAI Direct: OpenAI API pricing (Feb 2026)
var modelPricing = map[string]modelPrice{
	// ── DO-AI models (non-premium, included in free credit) ──────────

	// OpenAI via DO-AI
	"gpt-4o":            {InputPerMillion: 2.50, OutputPerMillion: 10.00},
	"gpt-4o-mini":       {InputPerMillion: 0.15, OutputPerMillion: 0.60},
	"gpt-4.1":           {InputPerMillion: 2.00, OutputPerMillion: 8.00},
	"gpt-5":             {InputPerMillion: 5.00, OutputPerMillion: 15.00},
	"gpt-5-mini":        {InputPerMillion: 1.25, OutputPerMillion: 5.00},
	"gpt-5-nano":        {InputPerMillion: 0.30, OutputPerMillion: 1.20},
	"gpt-5.1-codex-max": {InputPerMillion: 3.00, OutputPerMillion: 12.00},
	"gpt-5.2":           {InputPerMillion: 5.00, OutputPerMillion: 15.00},
	"gpt-5.2-pro":       {InputPerMillion: 10.00, OutputPerMillion: 30.00},
	"gpt-oss-120b":      {InputPerMillion: 0.90, OutputPerMillion: 0.90},
	"gpt-oss-20b":       {InputPerMillion: 0.20, OutputPerMillion: 0.20},
	"o1":                {InputPerMillion: 15.00, OutputPerMillion: 60.00},
	"o3":                {InputPerMillion: 10.00, OutputPerMillion: 40.00},
	"o3-mini":           {InputPerMillion: 1.10, OutputPerMillion: 4.40},

	// Anthropic via DO-AI
	"claude-3-5-haiku":  {InputPerMillion: 0.80, OutputPerMillion: 4.00},
	"claude-3-7-sonnet": {InputPerMillion: 3.00, OutputPerMillion: 15.00},
	"claude-4-1-opus":   {InputPerMillion: 15.00, OutputPerMillion: 75.00},
	"claude-haiku-4-5":  {InputPerMillion: 1.00, OutputPerMillion: 5.00},
	"claude-opus-4":     {InputPerMillion: 15.00, OutputPerMillion: 75.00},
	"claude-opus-4-5":   {InputPerMillion: 15.00, OutputPerMillion: 75.00},
	"claude-opus-4-6":   {InputPerMillion: 15.00, OutputPerMillion: 75.00},
	"claude-sonnet-4":   {InputPerMillion: 3.00, OutputPerMillion: 15.00},
	"claude-sonnet-4-5": {InputPerMillion: 3.00, OutputPerMillion: 15.00},

	// Open source via DO-AI
	"deepseek-r1-distill-70b": {InputPerMillion: 0.35, OutputPerMillion: 1.20},
	"llama-3.1-8b":            {InputPerMillion: 0.10, OutputPerMillion: 0.10},
	"llama-3.3-70b":           {InputPerMillion: 0.59, OutputPerMillion: 0.79},
	"mistral-nemo":            {InputPerMillion: 0.15, OutputPerMillion: 0.15},
	"qwen3-32b":               {InputPerMillion: 0.20, OutputPerMillion: 0.60},
	"gemma-4-31b":             {InputPerMillion: 0.18, OutputPerMillion: 0.50}, // DO published rate

	// DO-AI embeddings (POST /v1/embeddings). Output cost is 0 — embeddings
	// bill on input tokens only. Rates are DigitalOcean's published per-1M
	// embedding prices (real, not defaulted).
	"bge-m3":                     {InputPerMillion: 0.02},
	"e5-large-v2":                {InputPerMillion: 0.02},
	"gte-large-en-v1.5":          {InputPerMillion: 0.09},
	"all-mini-lm-l6-v2":          {InputPerMillion: 0.009},
	"multi-qa-mpnet-base-dot-v1": {InputPerMillion: 0.009},

	// DO-AI Inference Router. The router itself adds no cost (public preview);
	// the request bills at the underlying foundation model it selects. There is
	// no fixed per-router token price upstream, so this is a conservative
	// in-pattern estimate matching the gpt-oss-120b class the routers select
	// (DEFAULTED — not a published per-router rate).
	"router:general":                 {InputPerMillion: 0.90, OutputPerMillion: 0.90},
	"router:knowledge-base-document": {InputPerMillion: 0.90, OutputPerMillion: 0.90},
	"router:software-engineering":    {InputPerMillion: 0.90, OutputPerMillion: 0.90},
	"router:software-engineering-01": {InputPerMillion: 0.90, OutputPerMillion: 0.90},
	"router:writing":                 {InputPerMillion: 0.90, OutputPerMillion: 0.90},

	// ── Fireworks premium models ────────────────────────────────────

	"fireworks/cogito-671b":                 {InputPerMillion: 3.00, OutputPerMillion: 3.00},
	"fireworks/deepseek-r1":                 {InputPerMillion: 3.00, OutputPerMillion: 8.00},
	"fireworks/deepseek-v3":                 {InputPerMillion: 0.90, OutputPerMillion: 0.90},
	"fireworks/deepseek-v3p1":               {InputPerMillion: 0.90, OutputPerMillion: 0.90},
	"fireworks/deepseek-v3p2":               {InputPerMillion: 0.90, OutputPerMillion: 0.90},
	"fireworks/glm-4p7":                     {InputPerMillion: 1.80, OutputPerMillion: 6.60},
	"fireworks/glm-5":                       {InputPerMillion: 3.00, OutputPerMillion: 9.60},
	"fireworks/glm-5-thinking":              {InputPerMillion: 3.00, OutputPerMillion: 9.60},
	"fireworks/gpt-oss-120b":                {InputPerMillion: 0.90, OutputPerMillion: 0.90},
	"fireworks/gpt-oss-20b":                 {InputPerMillion: 0.20, OutputPerMillion: 0.20},
	"fireworks/kimi-k2":                     {InputPerMillion: 2.00, OutputPerMillion: 8.00},
	"fireworks/kimi-k2-thinking":            {InputPerMillion: 2.00, OutputPerMillion: 8.00},
	"fireworks/kimi-k2p5":                   {InputPerMillion: 2.50, OutputPerMillion: 10.00},
	"fireworks/minimax-m2p5":                {InputPerMillion: 1.00, OutputPerMillion: 4.00},
	"fireworks/mixtral-8x22b":               {InputPerMillion: 0.90, OutputPerMillion: 0.90},
	"fireworks/qwen3-4b":                    {InputPerMillion: 0.30, OutputPerMillion: 0.30},
	"fireworks/qwen3-8b":                    {InputPerMillion: 0.60, OutputPerMillion: 0.60},
	"fireworks/qwen3-235b-a22b":             {InputPerMillion: 3.60, OutputPerMillion: 3.60},
	"fireworks/qwen3-coder-30b-a3b":         {InputPerMillion: 1.50, OutputPerMillion: 1.50},
	"fireworks/qwen3-coder-480b":            {InputPerMillion: 3.60, OutputPerMillion: 3.60},
	"fireworks/qwen3-coder-480b-bf16":       {InputPerMillion: 4.50, OutputPerMillion: 4.50},
	"fireworks/qwen3-next-80b-a3b":          {InputPerMillion: 2.70, OutputPerMillion: 2.70},
	"fireworks/qwen3-next-80b-a3b-thinking": {InputPerMillion: 2.70, OutputPerMillion: 2.70},
	"fireworks/qwen3-vl-30b-a3b":            {InputPerMillion: 0.45, OutputPerMillion: 1.80},
	"fireworks/qwen3-vl-235b":               {InputPerMillion: 1.20, OutputPerMillion: 1.20},

	// ── OpenAI Direct premium models ────────────────────────────────

	"openai-direct/gpt-4o":      {InputPerMillion: 2.50, OutputPerMillion: 10.00},
	"openai-direct/gpt-4o-mini": {InputPerMillion: 0.15, OutputPerMillion: 0.60},
	"openai-direct/gpt-5":       {InputPerMillion: 5.00, OutputPerMillion: 15.00},
	"openai-direct/o3":          {InputPerMillion: 10.00, OutputPerMillion: 40.00},
	"openai-direct/o3-mini":     {InputPerMillion: 1.10, OutputPerMillion: 4.40},
}

// imagePricePerImageCents maps an image model (user-facing name OR upstream
// fal/OpenAI id, lowercase) to its price per generated image, in cents. Image
// generation is billed per image, NOT per token — the token-based
// calculateCostCents math yields 0 for a 0-token image call, so image billing
// is this separate, explicit seam. Keyed by both the user-facing zen3-image
// name and the upstream id so a lookup succeeds whichever the caller records.
var imagePricePerImageCents = map[string]int64{
	// Upstream ids (the do-ai fal image models) — same price, so billing is
	// correct whether the record carries the user-facing or upstream name.
	"fal-ai/flux/schnell": 5,
	"fal-ai/fast-sdxl":    6,
	// Stable Diffusion 3.5 Large — synchronous OpenAI /images shape (not fal
	// async). DO published rate is $0.08/image. Keyed by both the user-facing
	// name and the identical upstream id so billing is correct either way.
	"stable-diffusion-3.5-large": 8,
	// OpenAI-family image models, if ever routed directly ($0.08/image).
	"gpt-image-1": 8,
	"dall-e-3":    8,
	"dall-e-2":    2,
}

// imageCostCents returns the total cost in cents for generating n images with
// the given model. It matches the user-facing name or the upstream id, falling
// back to a conservative per-image floor for an unknown image model so an image
// call is never silently free. n <= 0 costs nothing.
func imageCostCents(model string, n int) int64 {
	if n <= 0 {
		return 0
	}
	m := strings.ToLower(model)
	per, ok := imagePricePerImageCents[m]
	if !ok {
		// Unknown image model: charge a conservative floor rather than $0.
		per = 5
	}
	return per * int64(n)
}

// videoPricePerVideoCents maps a text-to-video model (user-facing name OR
// upstream do-ai id, lowercase) to its price per generated video, in cents. Like
// images, video is billed per unit, NOT per token (the token-based math yields 0
// for a 0-token video call), so video billing is this separate, explicit seam.
// Keyed by both the user-facing zen3-video name and the upstream id so a lookup
// succeeds whichever the caller records.
//
// PRICING NOTE: DigitalOcean publishes NO per-video rate for wan2-2-t2v-a14b (the
// catalog record carries only token fields, which are meaningless for video). 40
// cents/video is a real, defensible estimate: Wan 2.2 T2V on comparable fal /
// serverless GPU markets runs ~$0.20-0.40 for a short clip; 40¢ sits at the top
// of that band with Hanzo margin, and is intentionally an order of magnitude
// above the image rate (5-8¢) since a t2v inference is minutes of A14B compute.
// FLAGGED as best-estimate — refine when DO publishes a per-video price.
var videoPricePerVideoCents = map[string]int64{
	// The do-ai text-to-video upstream, callable by its raw id. The zen video
	// family (zen-video) is served by the zen service and priced from its catalog.
	"wan2-2-t2v-a14b": 40,
}

// videoDefaultCostCents is the conservative per-video floor for an unknown video
// model, so a video call is never silently free. Set to the known real rate.
const videoDefaultCostCents int64 = 40

// videoCostCents returns the total cost in cents for generating n videos with
// the given model, matching the user-facing name or the upstream id and falling
// back to a conservative per-video floor for an unknown video model. n <= 0
// costs nothing.
func videoCostCents(model string, n int) int64 {
	if n <= 0 {
		return 0
	}
	m := strings.ToLower(model)
	per, ok := videoPricePerVideoCents[m]
	if !ok {
		per = videoDefaultCostCents
	}
	return per * int64(n)
}

// DO-AI alias pricing (same as their base model)
var aliasPricing = map[string]string{
	"openai/gpt-4o":                        "gpt-4o",
	"openai/gpt-4o-mini":                   "gpt-4o-mini",
	"openai/gpt-5":                         "gpt-5",
	"openai/o3":                            "o3",
	"openai/o3-mini":                       "o3-mini",
	"anthropic/claude-haiku-4-5-20251001":  "claude-haiku-4-5",
	"anthropic/claude-opus-4-6":            "claude-opus-4-6",
	"anthropic/claude-sonnet-4-5-20250929": "claude-sonnet-4-5",
}

// getModelPrice looks up pricing for a user-facing model name.
// Returns a default price if the model isn't in the pricing table.
func getModelPrice(model string) modelPrice {
	return getModelPriceForOrg(model, "")
}

func getModelPriceForOrg(model string, orgId string) modelPrice {
	// Zen family: the discovered retail price is the source of truth (hip-00NN).
	if p, ok := familyModelPrice(model); ok {
		return p
	}

	// Check DB route pricing first (org-specific -> global)
	dbRoute, err := object.ResolveModelRouteFromDB(strings.ToLower(model), orgId)
	if err == nil && dbRoute != nil && (dbRoute.InputPrice > 0 || dbRoute.OutputPrice > 0) {
		return modelPrice{
			InputPerMillion:   dbRoute.InputPrice,
			OutputPerMillion:  dbRoute.OutputPrice,
			CostInPerMillion:  dbRoute.CostInPerMillion,
			CostOutPerMillion: dbRoute.CostOutPerMillion,
		}
	}

	// YAML config pricing
	if cfg := GetModelConfig(); cfg != nil {
		return cfg.GetPrice(model)
	}

	// Static fallback
	m := strings.ToLower(model)

	// Direct lookup
	if price, ok := modelPricing[m]; ok {
		return price
	}

	// Check aliases
	if base, ok := aliasPricing[m]; ok {
		if price, ok := modelPricing[base]; ok {
			return price
		}
	}

	// Default: conservative pricing for unknown models
	return modelPrice{InputPerMillion: 1.00, OutputPerMillion: 4.00}
}

// calculateCostCents computes the cost in cents for a model call.
func calculateCostCents(model string, promptTokens, completionTokens int) int64 {
	return calculateCostCentsWithCache(model, promptTokens, completionTokens, 0, 0)
}

// costBreakdown is the per-component dollar cost of a token-billed LLM call. It is
// the ONE place the input/output/cache component split is computed, so the billing
// total (calculateCostCentsWithCache) and the o11y gen_ai span cost-detail
// (_o11y.gen_ai.cost_*) read the same numbers and can never drift.
type costBreakdown struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
	Total      float64
}

// modelCostBreakdown computes the per-component dollar cost of a token-billed call.
// Cache-read defaults to 10% of input price (Anthropic parity) and cache-write to the
// input price when the model declares no explicit cache rate. Pure: a pricing lookup
// and arithmetic, no I/O, no globals mutated — safe to call from the span emit path.
func modelCostBreakdown(model string, promptTokens, completionTokens, cacheReadTokens, cacheWriteTokens int) costBreakdown {
	price := getModelPrice(model)

	// Cache-read price: use explicit CacheReadPerMillion if set, else 10% of input.
	cacheReadRate := price.CacheReadPerMillion
	if cacheReadRate == 0 && price.InputPerMillion > 0 {
		cacheReadRate = price.InputPerMillion * 0.10
	}
	// Cache-write price: use explicit CacheWritePerMillion if set, else same as input.
	cacheWriteRate := price.CacheWritePerMillion
	if cacheWriteRate == 0 {
		cacheWriteRate = price.InputPerMillion
	}

	b := costBreakdown{
		Input:      float64(promptTokens) * price.InputPerMillion / 1_000_000.0,
		Output:     float64(completionTokens) * price.OutputPerMillion / 1_000_000.0,
		CacheRead:  float64(cacheReadTokens) * cacheReadRate / 1_000_000.0,
		CacheWrite: float64(cacheWriteTokens) * cacheWriteRate / 1_000_000.0,
	}
	b.Total = b.Input + b.Output + b.CacheRead + b.CacheWrite
	return b
}

// calculateCostCentsWithCache computes cost in cents including cache token pricing.
// Cache-read tokens are billed at 10% of input price (matching Anthropic).
// Cache-write tokens are billed at the same rate as input tokens.
func calculateCostCentsWithCache(model string, promptTokens, completionTokens, cacheReadTokens, cacheWriteTokens int) int64 {
	b := modelCostBreakdown(model, promptTokens, completionTokens, cacheReadTokens, cacheWriteTokens)
	costCents := int64(math.Round(b.Total * 100))

	// Minimum 1 cent for any non-zero usage
	if costCents <= 0 && (promptTokens > 0 || completionTokens > 0 || cacheReadTokens > 0 || cacheWriteTokens > 0) {
		costCents = 1
	}

	return costCents
}
