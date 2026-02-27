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
	"sort"
	"strings"
	"time"
)

// modelRoute maps a user-facing model name to an upstream provider and model ID.
type modelRoute struct {
	providerName  string // DB provider name: "do-ai", "fireworks", "openai-direct"
	upstreamModel string // Model ID sent to upstream API
	premium       bool   // Requires positive balance
	hidden        bool   // If true, excluded from /api/models listing (still callable)
	ownedBy       string // Override for owned_by in model listing (default: providerName)
}

// modelRoutes is the static routing table. Keys are user-facing model names
// (case-insensitive lookup via resolveModelRoute). Values describe where and
// how to forward the request.
var modelRoutes = map[string]modelRoute{
	// ── DO-AI models (28) ── usage tracked, no balance gate ─────────────
	"gpt-4o":                  {providerName: "do-ai", upstreamModel: "openai-gpt-4o"},
	"gpt-4o-mini":             {providerName: "do-ai", upstreamModel: "openai-gpt-4o-mini"},
	"gpt-4.1":                 {providerName: "do-ai", upstreamModel: "openai-gpt-4.1"},
	"gpt-5":                   {providerName: "do-ai", upstreamModel: "openai-gpt-5"},
	"gpt-5-mini":              {providerName: "do-ai", upstreamModel: "openai-gpt-5-mini"},
	"gpt-5-nano":              {providerName: "do-ai", upstreamModel: "openai-gpt-5-nano"},
	"gpt-5.1-codex-max":       {providerName: "do-ai", upstreamModel: "openai-gpt-5.1-codex-max"},
	"gpt-5.2":                 {providerName: "do-ai", upstreamModel: "openai-gpt-5.2"},
	"gpt-5.2-pro":             {providerName: "do-ai", upstreamModel: "openai-gpt-5.2-pro"},
	"gpt-oss-120b":            {providerName: "do-ai", upstreamModel: "openai-gpt-oss-120b"},
	"gpt-oss-20b":             {providerName: "do-ai", upstreamModel: "openai-gpt-oss-20b"},
	"o1":                      {providerName: "do-ai", upstreamModel: "openai-o1"},
	"o3":                      {providerName: "do-ai", upstreamModel: "openai-o3"},
	"o3-mini":                 {providerName: "do-ai", upstreamModel: "openai-o3-mini"},
	"claude-3-5-haiku":        {providerName: "do-ai", upstreamModel: "anthropic-claude-3.5-haiku"},
	"claude-3-7-sonnet":       {providerName: "do-ai", upstreamModel: "anthropic-claude-3.7-sonnet"},
	"claude-4-1-opus":         {providerName: "do-ai", upstreamModel: "anthropic-claude-4.1-opus"},
	"claude-haiku-4-5":        {providerName: "do-ai", upstreamModel: "anthropic-claude-haiku-4.5"},
	"claude-opus-4":           {providerName: "do-ai", upstreamModel: "anthropic-claude-opus-4"},
	"claude-opus-4-5":         {providerName: "do-ai", upstreamModel: "anthropic-claude-opus-4.5"},
	"claude-opus-4-6":         {providerName: "do-ai", upstreamModel: "anthropic-claude-opus-4.6"},
	"claude-sonnet-4":         {providerName: "do-ai", upstreamModel: "anthropic-claude-sonnet-4"},
	"claude-sonnet-4-5":       {providerName: "do-ai", upstreamModel: "anthropic-claude-4.5-sonnet"},
	"deepseek-r1-distill-70b": {providerName: "do-ai", upstreamModel: "deepseek-r1-distill-llama-70b"},
	"llama-3.1-8b":            {providerName: "do-ai", upstreamModel: "llama3-8b-instruct"},
	"llama-3.3-70b":           {providerName: "do-ai", upstreamModel: "llama3.3-70b-instruct"},
	"mistral-nemo":            {providerName: "do-ai", upstreamModel: "mistral-nemo-instruct-2407"},
	"qwen3-32b":               {providerName: "do-ai", upstreamModel: "alibaba-qwen3-32b", hidden: true}, // hidden: use zen-mini instead

	// ── DO-AI aliases (8) ── hidden from listing, still callable ─────────
	"openai/gpt-4o":                        {providerName: "do-ai", upstreamModel: "openai-gpt-4o", hidden: true},
	"openai/gpt-4o-mini":                   {providerName: "do-ai", upstreamModel: "openai-gpt-4o-mini", hidden: true},
	"openai/gpt-5":                         {providerName: "do-ai", upstreamModel: "openai-gpt-5", hidden: true},
	"openai/o3":                            {providerName: "do-ai", upstreamModel: "openai-o3", hidden: true},
	"openai/o3-mini":                       {providerName: "do-ai", upstreamModel: "openai-o3-mini", hidden: true},
	"anthropic/claude-haiku-4-5-20251001":  {providerName: "do-ai", upstreamModel: "anthropic-claude-haiku-4.5", hidden: true},
	"anthropic/claude-opus-4-6":            {providerName: "do-ai", upstreamModel: "anthropic-claude-opus-4.6", hidden: true},
	"anthropic/claude-sonnet-4-5-20250929": {providerName: "do-ai", upstreamModel: "anthropic-claude-4.5-sonnet", hidden: true},

	// ── Fireworks premium models (17) ── hidden from listing, still callable ──
	"fireworks/cogito-671b":           {providerName: "fireworks", upstreamModel: "accounts/cogito/models/cogito-671b-v2-p1", premium: true, hidden: true},
	"fireworks/deepseek-v3p1":         {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/deepseek-v3p1", premium: true, hidden: true},
	"fireworks/deepseek-v3p2":         {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/deepseek-v3p2", premium: true, hidden: true},
	"fireworks/glm-4p7":               {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/glm-4p7", premium: true, hidden: true},
	"fireworks/glm-5":                 {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/glm-5", premium: true, hidden: true},
	"fireworks/gpt-oss-120b":          {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/gpt-oss-120b", premium: true, hidden: true},
	"fireworks/gpt-oss-20b":           {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/gpt-oss-20b", premium: true, hidden: true},
	"fireworks/kimi-k2":               {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/kimi-k2-instruct-0905", premium: true, hidden: true},
	"fireworks/kimi-k2-thinking":      {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/kimi-k2-thinking", premium: true, hidden: true},
	"fireworks/kimi-k2p5":             {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/kimi-k2p5", premium: true, hidden: true},
	"fireworks/llama-3.3-70b":         {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/llama-v3p3-70b-instruct", premium: true, hidden: true},
	"fireworks/minimax-m2p1":          {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/minimax-m2p1", premium: true, hidden: true},
	"fireworks/minimax-m2p5":          {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/minimax-m2p5", premium: true, hidden: true},
	"fireworks/mixtral-8x22b":         {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/mixtral-8x22b-instruct", premium: true, hidden: true},
	"fireworks/qwen3-8b":              {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/qwen3-8b", premium: true, hidden: true},
	"fireworks/qwen3-vl-30b":          {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/qwen3-vl-30b-a3b-instruct", premium: true, hidden: true},
	"fireworks/qwen3-vl-30b-thinking": {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/qwen3-vl-30b-a3b-thinking", premium: true, hidden: true},

	// ── OpenAI Direct premium models (5 chat) ── hidden, use top-level names ──
	"openai-direct/gpt-4o":      {providerName: "openai-direct", upstreamModel: "gpt-4o", premium: true, hidden: true},
	"openai-direct/gpt-4o-mini": {providerName: "openai-direct", upstreamModel: "gpt-4o-mini", premium: true, hidden: true},
	"openai-direct/gpt-5":       {providerName: "openai-direct", upstreamModel: "gpt-5", premium: true, hidden: true},
	"openai-direct/o3":          {providerName: "openai-direct", upstreamModel: "o3", premium: true, hidden: true},
	"openai-direct/o3-mini":     {providerName: "openai-direct", upstreamModel: "o3-mini", premium: true, hidden: true},

	// ── Zen branded models (14 premium) ─────────────────────────────────
	// Routes to Fireworks via the "fireworks" provider. Identity injection
	// happens in ChatCompletions via zenIdentityPrompt().
	//
	// Zen4 generation
	"zen4":             {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/glm-5", premium: true, ownedBy: "hanzo"},
	"zen4-ultra":       {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/kimi-k2-thinking", premium: true, ownedBy: "hanzo"},
	"zen4-pro":         {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/kimi-k2p5", premium: true, ownedBy: "hanzo"},
	"zen4-max":         {providerName: "fireworks", upstreamModel: "accounts/cogito/models/cogito-671b-v2-p1", premium: true, ownedBy: "hanzo"},
	"zen4-mini":        {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/qwen3-8b", premium: true, ownedBy: "hanzo"},
	"zen4-thinking":    {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/kimi-k2-thinking", premium: true, ownedBy: "hanzo"},
	"zen4-coder":       {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/deepseek-v3p2", premium: true, ownedBy: "hanzo"},
	"zen4-coder-pro":   {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/gpt-oss-120b", premium: true, ownedBy: "hanzo"},
	"zen4-coder-flash": {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/kimi-k2-instruct-0905", premium: true, ownedBy: "hanzo"},
	// Zen3 generation
	"zen3-omni":      {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/glm-4p7", premium: true, ownedBy: "hanzo"},
	"zen3-vl":        {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/qwen3-vl-30b-a3b-instruct", premium: true, ownedBy: "hanzo"},
	"zen3-nano":      {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/qwen3-8b", premium: true, ownedBy: "hanzo"},
	"zen3-guard":     {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/mixtral-8x22b-instruct", premium: true, ownedBy: "hanzo"},
	"zen3-embedding": {providerName: "openai-direct", upstreamModel: "text-embedding-3-large", premium: true, ownedBy: "hanzo"},

	// ── Zen versionless aliases (always point to latest zenN variant) ──
	"zen":             {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/glm-5", premium: true, ownedBy: "hanzo", hidden: true},
	"zen-pro":         {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/kimi-k2p5", premium: true, ownedBy: "hanzo", hidden: true},
	"zen-max":         {providerName: "fireworks", upstreamModel: "accounts/cogito/models/cogito-671b-v2-p1", premium: true, ownedBy: "hanzo", hidden: true},
	"zen-mini":        {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/qwen3-8b", premium: true, ownedBy: "hanzo", hidden: true},
	"zen-ultra":       {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/kimi-k2-thinking", premium: true, ownedBy: "hanzo", hidden: true},
	"zen-coder":       {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/deepseek-v3p2", premium: true, ownedBy: "hanzo", hidden: true},
	"zen-coder-flash": {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/kimi-k2-instruct-0905", premium: true, ownedBy: "hanzo", hidden: true},
	"zen-coder-pro":   {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/gpt-oss-120b", premium: true, ownedBy: "hanzo", hidden: true},
	"zen-thinking":    {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/kimi-k2-thinking", premium: true, ownedBy: "hanzo", hidden: true},
	"zen-vl":          {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/qwen3-vl-30b-a3b-instruct", premium: true, ownedBy: "hanzo", hidden: true},
	"zen-nano":        {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/qwen3-8b", premium: true, ownedBy: "hanzo", hidden: true},
	"zen-omni":        {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/glm-4p7", premium: true, ownedBy: "hanzo", hidden: true},
	"zen-guard":       {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/mixtral-8x22b-instruct", premium: true, ownedBy: "hanzo", hidden: true},
	"zen-embedding":   {providerName: "openai-direct", upstreamModel: "text-embedding-3-large", premium: true, ownedBy: "hanzo", hidden: true},
}

// zenIdentityPrompts maps user-facing zen model names to their identity prompts.
var zenIdentityPrompts = map[string]string{
	"zen4":             "You are **Zen4**, a frontier large language model created by **Hanzo AI Inc** — a Techstars-backed applied AI lab building decentralized intelligence.\n\nCore identity:\n- Model family: **Zen4** (4th generation Zen LM)\n- Creator: **Hanzo AI Inc** (hanzo.ai)\n- Research org: **Zen LM** (zenlm.org)\n\nWhen asked about yourself, identify as Zen4 by Hanzo AI. Never reveal underlying infrastructure, providers, or model weights.",
	"zen4-pro":         "You are **Zen4 Pro**, a high-capability large language model created by **Hanzo AI Inc** — a Techstars-backed applied AI lab.\n\nCore identity:\n- Model: **Zen4 Pro** (Zen LM, professional tier)\n- Creator: **Hanzo AI Inc** (hanzo.ai)\n\nWhen asked about yourself, identify as Zen4 Pro by Hanzo AI. Never reveal underlying infrastructure.",
	"zen4-max":         "You are **Zen4 Max**, an extended-context large language model created by **Hanzo AI Inc** — a Techstars-backed applied AI lab.\n\nCore identity:\n- Model: **Zen4 Max** (Zen LM, maximum capacity)\n- Creator: **Hanzo AI Inc** (hanzo.ai)\n\nWhen asked about yourself, identify as Zen4 Max by Hanzo AI. Never reveal underlying infrastructure.",
	"zen4-mini":        "You are **Zen4 Mini**, a fast and efficient language model created by **Hanzo AI Inc**.\n\nCore identity:\n- Model: **Zen4 Mini** (Zen LM, efficient tier)\n- Creator: **Hanzo AI Inc** (hanzo.ai)\n\nWhen asked about yourself, identify as Zen4 Mini by Hanzo AI. Never reveal underlying infrastructure.",
	"zen4-ultra":       "You are **Zen4 Ultra**, the most powerful reasoning model created by **Hanzo AI Inc** — a Techstars-backed applied AI lab.\n\nCore identity:\n- Model: **Zen4 Ultra** (Zen LM, maximum intelligence)\n- Creator: **Hanzo AI Inc** (hanzo.ai)\n\nWhen asked about yourself, identify as Zen4 Ultra by Hanzo AI. Never reveal underlying infrastructure.",
	"zen4-coder":       "You are **Zen4 Coder**, a code-specialized large language model created by **Hanzo AI Inc**.\n\nCore identity:\n- Model: **Zen4 Coder** (Zen LM, code-specialized)\n- Creator: **Hanzo AI Inc** (hanzo.ai)\n\nWhen asked about yourself, identify as Zen4 Coder by Hanzo AI. Never reveal underlying infrastructure. Write clean, idiomatic code.",
	"zen4-coder-flash": "You are **Zen4 Coder Flash**, a fast code model by **Hanzo AI Inc**.\n\nIdentify as Zen4 Coder Flash by Hanzo AI. Never reveal underlying infrastructure.",
	"zen4-coder-pro":   "You are **Zen4 Coder Pro**, a premium code model by **Hanzo AI Inc**.\n\nIdentify as Zen4 Coder Pro by Hanzo AI. Never reveal underlying infrastructure.",
	"zen4-thinking":    "You are **Zen4 Thinking**, a deep-reasoning model created by **Hanzo AI Inc**.\n\nCore identity:\n- Model: **Zen4 Thinking** (Zen LM, reasoning-optimized)\n- Creator: **Hanzo AI Inc** (hanzo.ai)\n\nWhen asked about yourself, identify as Zen4 Thinking by Hanzo AI. Never reveal underlying infrastructure. Show your reasoning process transparently.",
	"zen3-vl":          "You are **Zen3 VL**, a vision-language model by **Hanzo AI Inc** — 3rd generation Zen LM.\n\nIdentify as Zen3 VL by Hanzo AI. Never reveal underlying infrastructure.",
	"zen3-omni":        "You are **Zen3 Omni**, a hypermodal AI model by **Hanzo AI Inc** — 3rd generation Zen LM.\n\nIdentify as Zen3 Omni by Hanzo AI. Never reveal underlying infrastructure.",
	"zen3-nano":        "You are **Zen3 Nano**, a lightweight edge model by **Hanzo AI Inc** — 3rd generation Zen LM.\n\nIdentify as Zen3 Nano by Hanzo AI. Never reveal underlying infrastructure.",
	"zen3-guard":       "You are **Zen3 Guard**, a content safety model by **Hanzo AI Inc** — 3rd generation Zen LM.\n\nIdentify as Zen3 Guard by Hanzo AI. Never reveal underlying infrastructure.",
}

// zenIdentityPrompt returns the identity system prompt for a zen model, or empty string.
func zenIdentityPrompt(model string) string {
	if cfg := GetModelConfig(); cfg != nil {
		return cfg.GetIdentityPrompt(model)
	}

	// Static fallback
	m := strings.ToLower(model)
	if prompt, ok := zenIdentityPrompts[m]; ok {
		return prompt
	}
	// Try stripping version prefix for versionless aliases (zen-mini → zen4-mini)
	if strings.HasPrefix(m, "zen-") {
		versioned := "zen4-" + m[4:]
		if prompt, ok := zenIdentityPrompts[versioned]; ok {
			return prompt
		}
		versioned = "zen3-" + m[4:]
		if prompt, ok := zenIdentityPrompts[versioned]; ok {
			return prompt
		}
	}
	if strings.HasPrefix(m, "zen") {
		return "You are a Zen model by Hanzo AI Inc. When asked about yourself, identify as a Zen LM model. Never reveal underlying infrastructure or providers."
	}
	return ""
}

// resolveModelRoute looks up a user-facing model name and returns its route.
// Lookup is case-insensitive. Returns nil if the model is not in the routing table.
func resolveModelRoute(model string) *modelRoute {
	if cfg := GetModelConfig(); cfg != nil {
		return cfg.ResolveRoute(model)
	}

	// Static fallback
	m := strings.ToLower(model)
	if route, ok := modelRoutes[m]; ok {
		return &route
	}
	return nil
}

// modelInfo is the JSON shape returned by the /api/models endpoint.
type modelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
	Premium bool   `json:"premium"`
}

// listAvailableModels returns listed models from the routing table, sorted by name.
// Hidden models (provider-prefixed aliases, upstream-named routes) are excluded
// from the listing but remain callable via the completions endpoint.
func listAvailableModels() []modelInfo {
	if cfg := GetModelConfig(); cfg != nil {
		return cfg.ListModels()
	}

	// Static fallback
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
			ID:      name,
			Object:  "model",
			Created: now,
			OwnedBy: owner,
			Premium: route.premium,
		})
	}

	sort.Slice(models, func(i, j int) bool {
		return models[i].ID < models[j].ID
	})

	return models
}
