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
}

// modelRoutes is the static routing table. Keys are user-facing model names
// (case-insensitive lookup via resolveModelRoute). Values describe where and
// how to forward the request.
var modelRoutes = map[string]modelRoute{
	// ── DO-AI free-tier models (28) ──────────────────────────────────────
	"gpt-4o":                   {providerName: "do-ai", upstreamModel: "openai-gpt-4o"},
	"gpt-4o-mini":              {providerName: "do-ai", upstreamModel: "openai-gpt-4o-mini"},
	"gpt-4.1":                  {providerName: "do-ai", upstreamModel: "openai-gpt-4.1"},
	"gpt-5":                    {providerName: "do-ai", upstreamModel: "openai-gpt-5"},
	"gpt-5-mini":               {providerName: "do-ai", upstreamModel: "openai-gpt-5-mini"},
	"gpt-5-nano":               {providerName: "do-ai", upstreamModel: "openai-gpt-5-nano"},
	"gpt-5.1-codex-max":        {providerName: "do-ai", upstreamModel: "openai-gpt-5.1-codex-max"},
	"gpt-5.2":                  {providerName: "do-ai", upstreamModel: "openai-gpt-5.2"},
	"gpt-5.2-pro":              {providerName: "do-ai", upstreamModel: "openai-gpt-5.2-pro"},
	"gpt-oss-120b":             {providerName: "do-ai", upstreamModel: "openai-gpt-oss-120b"},
	"gpt-oss-20b":              {providerName: "do-ai", upstreamModel: "openai-gpt-oss-20b"},
	"o1":                       {providerName: "do-ai", upstreamModel: "openai-o1"},
	"o3":                       {providerName: "do-ai", upstreamModel: "openai-o3"},
	"o3-mini":                  {providerName: "do-ai", upstreamModel: "openai-o3-mini"},
	"claude-3-5-haiku":         {providerName: "do-ai", upstreamModel: "anthropic-claude-3.5-haiku"},
	"claude-3-7-sonnet":        {providerName: "do-ai", upstreamModel: "anthropic-claude-3.7-sonnet"},
	"claude-4-1-opus":          {providerName: "do-ai", upstreamModel: "anthropic-claude-4.1-opus"},
	"claude-haiku-4-5":         {providerName: "do-ai", upstreamModel: "anthropic-claude-haiku-4.5"},
	"claude-opus-4":            {providerName: "do-ai", upstreamModel: "anthropic-claude-opus-4"},
	"claude-opus-4-5":          {providerName: "do-ai", upstreamModel: "anthropic-claude-opus-4.5"},
	"claude-opus-4-6":          {providerName: "do-ai", upstreamModel: "anthropic-claude-opus-4.6"},
	"claude-sonnet-4":          {providerName: "do-ai", upstreamModel: "anthropic-claude-sonnet-4"},
	"claude-sonnet-4-5":        {providerName: "do-ai", upstreamModel: "anthropic-claude-4.5-sonnet"},
	"deepseek-r1-distill-70b":  {providerName: "do-ai", upstreamModel: "deepseek-r1-distill-llama-70b"},
	"llama-3.1-8b":             {providerName: "do-ai", upstreamModel: "llama3-8b-instruct"},
	"llama-3.3-70b":            {providerName: "do-ai", upstreamModel: "llama3.3-70b-instruct"},
	"mistral-nemo":             {providerName: "do-ai", upstreamModel: "mistral-nemo-instruct-2407"},
	"qwen3-32b":                {providerName: "do-ai", upstreamModel: "alibaba-qwen3-32b"},

	// ── DO-AI aliases (8 free-tier) ──────────────────────────────────────
	"openai/gpt-4o":                          {providerName: "do-ai", upstreamModel: "openai-gpt-4o"},
	"openai/gpt-4o-mini":                     {providerName: "do-ai", upstreamModel: "openai-gpt-4o-mini"},
	"openai/gpt-5":                           {providerName: "do-ai", upstreamModel: "openai-gpt-5"},
	"openai/o3":                              {providerName: "do-ai", upstreamModel: "openai-o3"},
	"openai/o3-mini":                         {providerName: "do-ai", upstreamModel: "openai-o3-mini"},
	"anthropic/claude-haiku-4-5-20251001":    {providerName: "do-ai", upstreamModel: "anthropic-claude-haiku-4.5"},
	"anthropic/claude-opus-4-6":              {providerName: "do-ai", upstreamModel: "anthropic-claude-opus-4.6"},
	"anthropic/claude-sonnet-4-5-20250929":   {providerName: "do-ai", upstreamModel: "anthropic-claude-4.5-sonnet"},

	// ── Fireworks premium models (17) ────────────────────────────────────
	"fireworks/cogito-671b":        {providerName: "fireworks", upstreamModel: "accounts/cogito/models/cogito-671b-v2-p1", premium: true},
	"fireworks/deepseek-r1":        {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/deepseek-r1-0528", premium: true},
	"fireworks/deepseek-v3":        {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/deepseek-v3-0324", premium: true},
	"fireworks/deepseek-v3p1":      {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/deepseek-v3p1", premium: true},
	"fireworks/deepseek-v3p2":      {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/deepseek-v3p2", premium: true},
	"fireworks/glm-4p7":            {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/glm-4p7", premium: true},
	"fireworks/glm-5":              {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/glm-5", premium: true},
	"fireworks/gpt-oss-120b":       {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/gpt-oss-120b", premium: true},
	"fireworks/gpt-oss-20b":        {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/gpt-oss-20b", premium: true},
	"fireworks/kimi-k2":            {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/kimi-k2-instruct-0905", premium: true},
	"fireworks/kimi-k2-thinking":   {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/kimi-k2-thinking", premium: true},
	"fireworks/kimi-k2p5":          {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/kimi-k2p5", premium: true},
	"fireworks/minimax-m2p5":       {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/minimax-m2p5", premium: true},
	"fireworks/mixtral-8x22b":      {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/mixtral-8x22b-instruct", premium: true},
	"fireworks/qwen3-235b-a22b":    {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/qwen3-235b-a22b", premium: true},
	"fireworks/qwen3-coder-480b":   {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/qwen3-coder-480b-a35b-instruct", premium: true},
	"fireworks/qwen3-vl-235b":      {providerName: "fireworks", upstreamModel: "accounts/fireworks/models/qwen3-vl-235b-a22b-instruct", premium: true},

	// ── OpenAI Direct premium models (5 chat) ───────────────────────────
	"openai-direct/gpt-4o":      {providerName: "openai-direct", upstreamModel: "gpt-4o", premium: true},
	"openai-direct/gpt-4o-mini": {providerName: "openai-direct", upstreamModel: "gpt-4o-mini", premium: true},
	"openai-direct/gpt-5":       {providerName: "openai-direct", upstreamModel: "gpt-5", premium: true},
	"openai-direct/o3":          {providerName: "openai-direct", upstreamModel: "o3", premium: true},
	"openai-direct/o3-mini":     {providerName: "openai-direct", upstreamModel: "o3-mini", premium: true},

	// ── Zen branded models (premium, 3X pricing) ────────────────────────
	// Routed through the internal Zen gateway (zen.hanzo.ai) which injects
	// Zen identity system prompts, tracks usage via Langfuse, and forwards
	// to upstream providers. DB provider "zen" must have base URL pointing
	// to http://zen-gateway.hanzo.svc.cluster.local:4000 (or zen.hanzo.ai).
	"zen4":             {providerName: "zen", upstreamModel: "zen4", premium: true},
	"zen4-pro":         {providerName: "zen", upstreamModel: "zen4-pro", premium: true},
	"zen4-max":         {providerName: "zen", upstreamModel: "zen4-max", premium: true},
	"zen4-mini":        {providerName: "zen", upstreamModel: "zen4-mini", premium: true},
	"zen4-ultra":       {providerName: "zen", upstreamModel: "zen4-ultra", premium: true},
	"zen4-coder":       {providerName: "zen", upstreamModel: "zen4-coder", premium: true},
	"zen4-coder-flash": {providerName: "zen", upstreamModel: "zen4-coder-flash", premium: true},
	"zen4-coder-pro":   {providerName: "zen", upstreamModel: "zen4-coder-pro", premium: true},
	"zen4-thinking":    {providerName: "zen", upstreamModel: "zen4-thinking", premium: true},
	"zen3-vl":          {providerName: "zen", upstreamModel: "zen3-vl", premium: true},
	"zen3-nano":        {providerName: "zen", upstreamModel: "zen3-nano", premium: true},
	"zen3-omni":        {providerName: "zen", upstreamModel: "zen3-omni", premium: true},
	"zen3-guard":       {providerName: "zen", upstreamModel: "zen3-guard", premium: true},
	"zen3-embedding":   {providerName: "zen", upstreamModel: "zen3-embedding", premium: true},

	// ── Zen versionless aliases (always point to latest zenN variant) ──
	"zen":             {providerName: "zen", upstreamModel: "zen4", premium: true},
	"zen-pro":         {providerName: "zen", upstreamModel: "zen4-pro", premium: true},
	"zen-max":         {providerName: "zen", upstreamModel: "zen4-max", premium: true},
	"zen-mini":        {providerName: "zen", upstreamModel: "zen4-mini", premium: true},
	"zen-ultra":       {providerName: "zen", upstreamModel: "zen4-ultra", premium: true},
	"zen-coder":       {providerName: "zen", upstreamModel: "zen4-coder", premium: true},
	"zen-coder-flash": {providerName: "zen", upstreamModel: "zen4-coder-flash", premium: true},
	"zen-coder-pro":   {providerName: "zen", upstreamModel: "zen4-coder-pro", premium: true},
	"zen-thinking":    {providerName: "zen", upstreamModel: "zen4-thinking", premium: true},
	"zen-vl":          {providerName: "zen", upstreamModel: "zen3-vl", premium: true},
	"zen-nano":        {providerName: "zen", upstreamModel: "zen3-nano", premium: true},
	"zen-omni":        {providerName: "zen", upstreamModel: "zen3-omni", premium: true},
	"zen-guard":       {providerName: "zen", upstreamModel: "zen3-guard", premium: true},
	"zen-embedding":   {providerName: "zen", upstreamModel: "zen3-embedding", premium: true},
}

// resolveModelRoute looks up a user-facing model name and returns its route.
// Lookup is case-insensitive. Returns nil if the model is not in the routing table.
func resolveModelRoute(model string) *modelRoute {
	m := strings.ToLower(model)
	if route, ok := modelRoutes[m]; ok {
		return &route
	}
	return nil
}

// modelInfo is the JSON shape returned by the /api/models endpoint.
type modelInfo struct {
	ID       string `json:"id"`
	Object   string `json:"object"`
	Created  int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
	Premium  bool   `json:"premium"`
}

// listAvailableModels returns all models from the routing table, sorted by name.
func listAvailableModels() []modelInfo {
	now := time.Now().Unix()
	models := make([]modelInfo, 0, len(modelRoutes))

	for name, route := range modelRoutes {
		models = append(models, modelInfo{
			ID:       name,
			Object:   "model",
			Created:  now,
			OwnedBy: route.providerName,
			Premium:  route.premium,
		})
	}

	sort.Slice(models, func(i, j int) bool {
		return models[i].ID < models[j].ID
	})

	return models
}
