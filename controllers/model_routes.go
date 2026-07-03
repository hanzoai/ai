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

	"github.com/hanzoai/ai/object"
)

// modelRouteFallback is an alternate provider+upstream for failover.
type modelRouteFallback struct {
	providerName  string
	upstreamModel string
}

// modelRoute maps a user-facing model name to an upstream provider and model ID.
type modelRoute struct {
	providerName  string               // DB provider name: "do-ai", "fireworks", "openai-direct"
	upstreamModel string               // Model ID sent to upstream API
	fallbacks     []modelRouteFallback // Alternate providers tried on error
	premium       bool                 // Requires positive balance
	hidden        bool                 // If true, excluded from /api/models listing (still callable)
	ownedBy       string               // Override for owned_by in model listing (default: providerName)
}

// modelRoutes is the static routing table. Keys are user-facing model names
// (case-insensitive lookup via resolveModelRoute). Values describe where and
// how to forward the request.
var modelRoutes = map[string]modelRoute{
	// ── DO-AI models (28) ── usage tracked, no balance gate ─────────────
	"gpt-4o":                       {providerName: "do-ai", upstreamModel: "openai-gpt-4o"},
	"gpt-4o-mini":                  {providerName: "do-ai", upstreamModel: "openai-gpt-4o-mini"},
	"gpt-4.1":                      {providerName: "do-ai", upstreamModel: "openai-gpt-4.1"},
	"gpt-5":                        {providerName: "do-ai", upstreamModel: "openai-gpt-5"},
	"gpt-5-mini":                   {providerName: "do-ai", upstreamModel: "openai-gpt-5-mini"},
	"gpt-5-nano":                   {providerName: "do-ai", upstreamModel: "openai-gpt-5-nano"},
	"gpt-5.1-codex-max":            {providerName: "do-ai", upstreamModel: "openai-gpt-5.1-codex-max"},
	"gpt-5.2":                      {providerName: "do-ai", upstreamModel: "openai-gpt-5.2"},
	"gpt-5.2-pro":                  {providerName: "do-ai", upstreamModel: "openai-gpt-5.2-pro"},
	"gpt-5.3-codex":                {providerName: "do-ai", upstreamModel: "openai-gpt-5.3-codex"},
	"gpt-5.4":                      {providerName: "do-ai", upstreamModel: "openai-gpt-5.4"},
	"gpt-5.4-mini":                 {providerName: "do-ai", upstreamModel: "openai-gpt-5.4-mini"},
	"gpt-5.4-nano":                 {providerName: "do-ai", upstreamModel: "openai-gpt-5.4-nano"},
	"gpt-5.4-pro":                  {providerName: "do-ai", upstreamModel: "openai-gpt-5.4-pro"},
	"gpt-5.5":                      {providerName: "do-ai", upstreamModel: "openai-gpt-5.5"},
	"gpt-oss-120b":                 {providerName: "do-ai", upstreamModel: "openai-gpt-oss-120b"},
	"gpt-oss-20b":                  {providerName: "do-ai", upstreamModel: "openai-gpt-oss-20b"},
	"o1":                           {providerName: "do-ai", upstreamModel: "openai-o1"},
	"o3":                           {providerName: "do-ai", upstreamModel: "openai-o3"},
	"o3-mini":                      {providerName: "do-ai", upstreamModel: "openai-o3-mini"},
	"claude-3-5-haiku":             {providerName: "do-ai", upstreamModel: "anthropic-claude-3.5-haiku"},
	"claude-3-7-sonnet":            {providerName: "do-ai", upstreamModel: "anthropic-claude-3.7-sonnet"},
	"claude-4-1-opus":              {providerName: "do-ai", upstreamModel: "anthropic-claude-4.1-opus"},
	"claude-haiku-4-5":             {providerName: "do-ai", upstreamModel: "anthropic-claude-haiku-4.5"},
	"claude-opus-4":                {providerName: "do-ai", upstreamModel: "anthropic-claude-opus-4"},
	"claude-opus-4-5":              {providerName: "do-ai", upstreamModel: "anthropic-claude-opus-4.5"},
	"claude-opus-4-6":              {providerName: "do-ai", upstreamModel: "anthropic-claude-opus-4.6"},
	"claude-opus-4-7":              {providerName: "do-ai", upstreamModel: "anthropic-claude-opus-4.7"},
	"claude-opus-4-8":              {providerName: "do-ai", upstreamModel: "anthropic-claude-opus-4.8"},
	"claude-sonnet-4":              {providerName: "do-ai", upstreamModel: "anthropic-claude-sonnet-4"},
	"claude-sonnet-4-5":            {providerName: "do-ai", upstreamModel: "anthropic-claude-4.5-sonnet"},
	"claude-sonnet-4-6":            {providerName: "do-ai", upstreamModel: "anthropic-claude-4.5-sonnet"}, // was "anthropic-claude-sonnet-4.6" (not in catalog → 404); 4.6-sonnet is catalog-listed but 403 on this account, so route to the working latest Sonnet (matches conf/models.yaml)
	"deepseek-3.2":                 {providerName: "do-ai", upstreamModel: "deepseek-3.2"},
	"deepseek-chat":                {providerName: "do-ai", upstreamModel: "deepseek-v4-pro"},
	"deepseek-v4-flash":            {providerName: "do-ai", upstreamModel: "deepseek-4-flash"},
	"deepseek-v4-pro":              {providerName: "do-ai", upstreamModel: "deepseek-v4-pro"},
	"deepseek-r1-distill-70b":      {providerName: "do-ai", upstreamModel: "deepseek-r1-distill-llama-70b"},
	"llama-3.1-8b":                 {providerName: "do-ai", upstreamModel: "llama3-8b-instruct"},
	"llama-3.3-70b":                {providerName: "do-ai", upstreamModel: "llama3.3-70b-instruct"},
	"llama-4-maverick":             {providerName: "do-ai", upstreamModel: "llama-4-maverick"},
	"kimi-k2.5":                    {providerName: "do-ai", upstreamModel: "kimi-k2.5"},
	"kimi-k2.6":                    {providerName: "do-ai", upstreamModel: "kimi-k2.6"},
	"mimo-v2.5":                    {providerName: "do-ai", upstreamModel: "mimo-v2.5"},
	"mimo-v2.5-pro":                {providerName: "do-ai", upstreamModel: "mimo-v2.5-pro"},
	"minimax-m2.5":                 {providerName: "do-ai", upstreamModel: "minimax-m2.5"},
	"nemotron-3-nano-omni":         {providerName: "do-ai", upstreamModel: "nemotron-3-nano-omni"},
	"nemotron-3-super-120b":        {providerName: "do-ai", upstreamModel: "nvidia-nemotron-3-super-120b"},
	"nemotron-3-ultra-550b":        {providerName: "do-ai", upstreamModel: "nemotron-3-ultra-550b"},
	"nemotron-nano-12b-vl":         {providerName: "do-ai", upstreamModel: "nemotron-nano-12b-v2-vl"},
	"qwen3-coder-flash":            {providerName: "do-ai", upstreamModel: "qwen3-coder-flash"},
	"qwen3.5-397b":                 {providerName: "do-ai", upstreamModel: "qwen3.5-397b-a17b"},
	"arcee-trinity-large-thinking": {providerName: "do-ai", upstreamModel: "arcee-trinity-large-thinking"},
	"mistral-nemo":                 {providerName: "do-ai", upstreamModel: "mistral-nemo-instruct-2407"},
	"qwen3-32b":                    {providerName: "do-ai", upstreamModel: "alibaba-qwen3-32b", hidden: true}, // hidden: use zen-mini instead
	"glm-5":                        {providerName: "do-ai", upstreamModel: "glm-5"},
	"glm-5.1":                      {providerName: "do-ai", upstreamModel: "glm-5.1"},
	"glm-5.2":                      {providerName: "do-ai", upstreamModel: "glm-5.2", premium: true},
	"gemma-3-31b":                  {providerName: "do-ai", upstreamModel: "gemma-3-31b"},
	"gemma-4-31b":                  {providerName: "do-ai", upstreamModel: "gemma-4-31B-it"}, // real DO catalog id (was "gemma-4-31b" → 404)
	"kimi-k2":                      {providerName: "do-ai", upstreamModel: "kimi-k2"},
	"mistral-3-14b":                {providerName: "do-ai", upstreamModel: "mistral-3-14b"},
	"mistral-small":                {providerName: "do-ai", upstreamModel: "mistral-small"},
	"nemotron-3-nano":              {providerName: "do-ai", upstreamModel: "nemotron-3-nano"},
	"nemotron-nano-vl":             {providerName: "do-ai", upstreamModel: "nemotron-nano-12b-v2-vl"},
	"deepseek-v3.2":                {providerName: "do-ai", upstreamModel: "deepseek-3.2"},
	"deepseek-reasoner":            {providerName: "do-ai", upstreamModel: "deepseek-r1"},
	"qwen3-coder":                  {providerName: "do-ai", upstreamModel: "qwen3-coder-480b"},

	// ── DO-AI embeddings ── served at POST /v1/embeddings (passthrough) ──
	// User-facing name == DO catalog id (already clean public names). owned_by
	// defaults to the do-ai provider (surfaced), matching every other unbranded
	// passthrough. Verified live: each returns real vectors (dims noted).
	"bge-m3":                     {providerName: "do-ai", upstreamModel: "bge-m3"},                     // 1024-dim, multilingual
	"e5-large-v2":                {providerName: "do-ai", upstreamModel: "e5-large-v2"},                // 1024-dim
	"gte-large-en-v1.5":          {providerName: "do-ai", upstreamModel: "gte-large-en-v1.5"},          // 1024-dim
	"all-mini-lm-l6-v2":          {providerName: "do-ai", upstreamModel: "all-mini-lm-l6-v2"},          // 384-dim
	"multi-qa-mpnet-base-dot-v1": {providerName: "do-ai", upstreamModel: "multi-qa-mpnet-base-dot-v1"}, // 768-dim

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

	// ── DO-AI aliases (8) ── hidden from listing, still callable ─────────
	"openai/gpt-4o":                        {providerName: "do-ai", upstreamModel: "openai-gpt-4o", hidden: true},
	"openai/gpt-4o-mini":                   {providerName: "do-ai", upstreamModel: "openai-gpt-4o-mini", hidden: true},
	"openai/gpt-5":                         {providerName: "do-ai", upstreamModel: "openai-gpt-5", hidden: true},
	"openai/o3":                            {providerName: "do-ai", upstreamModel: "openai-o3", hidden: true},
	"openai/o3-mini":                       {providerName: "do-ai", upstreamModel: "openai-o3-mini", hidden: true},
	"anthropic/claude-haiku-4-5-20251001":  {providerName: "do-ai", upstreamModel: "anthropic-claude-haiku-4.5", hidden: true},
	"anthropic/claude-opus-4-6":            {providerName: "do-ai", upstreamModel: "anthropic-claude-opus-4.6", hidden: true},
	"anthropic/claude-sonnet-4-5-20250929": {providerName: "do-ai", upstreamModel: "anthropic-claude-4.5-sonnet", hidden: true},
	"anthropic/claude-sonnet-4-6":          {providerName: "do-ai", upstreamModel: "anthropic-claude-4.5-sonnet", hidden: true}, // 4.6-sonnet 403s on this account; route to working latest Sonnet (matches claude-sonnet-4-6)

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

	// ── Zen branded models ───────────────────────────────────────────────
	// Routes to DigitalOcean GenAI ("do-ai" provider) — the same working
	// upstream that backs the OpenAI/Anthropic models, so zen needs no extra
	// key and no GPU node. Upstreams are the Qwen3+/GLM/Kimi/DeepSeek families
	// (qwen3+ per convention). Identity injection happens in ChatCompletions
	// via zenIdentityPrompt(); upstream names are never exposed.
	//
	// Zen4 generation
	"zen4":             {providerName: "do-ai", upstreamModel: "glm-5", premium: true, ownedBy: "hanzo"},
	"zen4-ultra":       {providerName: "do-ai", upstreamModel: "deepseek-v4-pro", premium: true, ownedBy: "hanzo"},
	"zen4-pro":         {providerName: "do-ai", upstreamModel: "qwen3.5-397b-a17b", premium: true, ownedBy: "hanzo"},
	"zen4-max":         {providerName: "do-ai", upstreamModel: "kimi-k2.6", premium: true, ownedBy: "hanzo"},
	"zen4-mini":        {providerName: "do-ai", upstreamModel: "alibaba-qwen3-32b", premium: true, ownedBy: "hanzo"},
	"zen4-thinking":    {providerName: "do-ai", upstreamModel: "deepseek-v4-pro", premium: true, ownedBy: "hanzo"},
	"zen4-coder":       {providerName: "do-ai", upstreamModel: "qwen3-coder-flash", premium: true, ownedBy: "hanzo"},
	"zen4-coder-pro":   {providerName: "do-ai", upstreamModel: "glm-5.2", premium: true, ownedBy: "hanzo"},
	"zen4-coder-flash": {providerName: "do-ai", upstreamModel: "qwen3-coder-flash", premium: true, ownedBy: "hanzo"},
	// Zen5 generation — routed directly to DO-AI; identity injected via zenIdentityPrompt
	"zen5-flash": {providerName: "do-ai", upstreamModel: "deepseek-4-flash", premium: true, ownedBy: "hanzo"},
	"zen5-mini":  {providerName: "do-ai", upstreamModel: "minimax-m2.5", premium: true, ownedBy: "hanzo"},
	"zen5":       {providerName: "do-ai", upstreamModel: "glm-5.2", premium: true, ownedBy: "hanzo"},
	"zen5-coder": {providerName: "do-ai", upstreamModel: "glm-5.2", premium: true, ownedBy: "hanzo"},
	"zen5-pro":   {providerName: "do-ai", upstreamModel: "deepseek-v4-pro", premium: true, ownedBy: "hanzo"},
	"zen5-max":   {providerName: "do-ai", upstreamModel: "qwen3.5-397b-a17b", premium: true, ownedBy: "hanzo"},
	"zen5-ultra": {providerName: "do-ai", upstreamModel: "anthropic-claude-opus-4.8", premium: true, ownedBy: "hanzo"},
	// Zen3 generation
	"zen3-omni":      {providerName: "do-ai", upstreamModel: "glm-5", premium: true, ownedBy: "hanzo"},
	"zen3-vl":        {providerName: "do-ai", upstreamModel: "nemotron-nano-12b-v2-vl", premium: true, ownedBy: "hanzo"},
	"zen3-nano":      {providerName: "do-ai", upstreamModel: "alibaba-qwen3-32b", premium: true, ownedBy: "hanzo"},
	"zen3-guard":     {providerName: "do-ai", upstreamModel: "llama3.3-70b-instruct", premium: true, ownedBy: "hanzo"},
	"zen3-embedding": {providerName: "do-ai", upstreamModel: "qwen3-embedding-0.6b", premium: true, ownedBy: "hanzo"},

	// ── Zen3 image (diffusion) family ─────────────────────────────────────
	// Routed to do-ai's fal-hosted diffusion models via the ASYNC image API
	// (model/doai_image.go), not the OpenAI /v1/images/generations shape. The
	// flagship is FLUX.1 schnell (latest, fastest); SDXL serves the -sdxl/-ssd
	// high-resolution variants. do-ai has one flux + one sdxl image model, so
	// the family collapses cleanly onto those two upstreams rather than
	// inventing per-variant models that don't exist upstream. owned_by hanzo —
	// the fal/do-ai upstream is never exposed (same policy as every zen route).
	"zen3-image":            {providerName: "do-ai", upstreamModel: "fal-ai/flux/schnell", premium: true, ownedBy: "hanzo"},
	"zen3-image-max":        {providerName: "do-ai", upstreamModel: "fal-ai/flux/schnell", premium: true, ownedBy: "hanzo"},
	"zen3-image-fast":       {providerName: "do-ai", upstreamModel: "fal-ai/flux/schnell", premium: true, ownedBy: "hanzo"},
	"zen3-image-dev":        {providerName: "do-ai", upstreamModel: "fal-ai/flux/schnell", premium: true, ownedBy: "hanzo"},
	"zen3-image-playground": {providerName: "do-ai", upstreamModel: "fal-ai/flux/schnell", premium: true, ownedBy: "hanzo"},
	"zen3-image-jp":         {providerName: "do-ai", upstreamModel: "fal-ai/flux/schnell", premium: true, ownedBy: "hanzo"},
	"zen3-image-sdxl":       {providerName: "do-ai", upstreamModel: "fal-ai/fast-sdxl", premium: true, ownedBy: "hanzo"},
	"zen3-image-ssd":        {providerName: "do-ai", upstreamModel: "fal-ai/fast-sdxl", premium: true, ownedBy: "hanzo"},

	// ── DO-AI text-to-video ── served at POST /v1/videos/generations ─────
	// wan2-2-t2v-a14b is the only text-to-video model in the do-ai catalog. It
	// is served on the OpenAI Sora-style async /v1/videos API (create → poll →
	// download), NOT the fal async-invoke image path (which 404s it). Callable
	// directly by its catalog id (unbranded passthrough — no ownedBy, so the
	// listing surfaces the do-ai upstream like the embeddings passthroughs).
	// getOpenAiModelType classifies it via isDOAIVideoModel → the videos path.
	// premium: true — ALL video is premium. A single t2v inference is minutes of
	// A14B GPU compute (~40¢); without the premium flag the raw id would clear on
	// balance>0 alone, letting the $5 starter credit fund the platform's priciest
	// unit (replayable across throwaway signups). Premium routes the caller through
	// the starter-credit gate (balance must exceed the free credit).
	"wan2-2-t2v-a14b": {providerName: "do-ai", upstreamModel: "wan2-2-t2v-a14b", premium: true},

	// ── Zen3 video (text-to-video) family ────────────────────────────────
	// Served on OUR GB10 (spark) via Hanzo Studio (ComfyUI + the Zen video
	// diffusion model). The "spark-video" provider row's ProviderUrl points at
	// the spark queue service (https://spark-video.hanzo.ai/v1), which exposes
	// the SAME OpenAI Sora-style async video API (create → poll → download) that
	// model/doai_video.go already drives — so no client change, only the
	// provider re-point. upstreamModel stays wan2-2-t2v-a14b: that is the id the
	// spark async API accepts and maps to the Zen video workflow. owned_by
	// hanzo — the backend model name is never exposed (same policy as every zen
	// route). premium: true — ALL video is premium (minutes of GB10 compute),
	// routed through the starter-credit gate so the free credit can't fund it.
	"zen3-video":      {providerName: "spark-video", upstreamModel: "wan2-2-t2v-a14b", premium: true, ownedBy: "hanzo"},
	"zen3-video-fast": {providerName: "spark-video", upstreamModel: "wan2-2-t2v-a14b", premium: true, ownedBy: "hanzo"},
	"zen3-video-pro":  {providerName: "spark-video", upstreamModel: "wan2-2-t2v-a14b", premium: true, ownedBy: "hanzo"},

	// ── Zen versionless aliases (always point to latest zenN variant) ──
	"zen":             {providerName: "do-ai", upstreamModel: "glm-5", premium: true, ownedBy: "hanzo", hidden: true},
	"zen-pro":         {providerName: "do-ai", upstreamModel: "qwen3.5-397b-a17b", premium: true, ownedBy: "hanzo", hidden: true},
	"zen-max":         {providerName: "do-ai", upstreamModel: "kimi-k2.6", premium: true, ownedBy: "hanzo", hidden: true},
	"zen-mini":        {providerName: "do-ai", upstreamModel: "alibaba-qwen3-32b", premium: true, ownedBy: "hanzo", hidden: true},
	"zen-ultra":       {providerName: "do-ai", upstreamModel: "deepseek-v4-pro", premium: true, ownedBy: "hanzo", hidden: true},
	"zen-coder":       {providerName: "do-ai", upstreamModel: "qwen3-coder-flash", premium: true, ownedBy: "hanzo", hidden: true},
	"zen-coder-flash": {providerName: "do-ai", upstreamModel: "qwen3-coder-flash", premium: true, ownedBy: "hanzo", hidden: true},
	"zen-coder-pro":   {providerName: "do-ai", upstreamModel: "glm-5.2", premium: true, ownedBy: "hanzo", hidden: true},
	"zen-thinking":    {providerName: "do-ai", upstreamModel: "deepseek-v4-pro", premium: true, ownedBy: "hanzo", hidden: true},
	"zen-vl":          {providerName: "do-ai", upstreamModel: "nemotron-nano-12b-v2-vl", premium: true, ownedBy: "hanzo", hidden: true},
	"zen-vision":      {providerName: "do-ai", upstreamModel: "nemotron-nano-12b-v2-vl", premium: true, ownedBy: "hanzo", hidden: true},
	"zen-nano":        {providerName: "do-ai", upstreamModel: "alibaba-qwen3-32b", premium: true, ownedBy: "hanzo", hidden: true},
	"zen-omni":        {providerName: "do-ai", upstreamModel: "glm-5", premium: true, ownedBy: "hanzo", hidden: true},
	"zen-guard":       {providerName: "do-ai", upstreamModel: "llama3.3-70b-instruct", premium: true, ownedBy: "hanzo", hidden: true},
	"zen-embedding":   {providerName: "do-ai", upstreamModel: "qwen3-embedding-0.6b", premium: true, ownedBy: "hanzo", hidden: true},
}

// zenIdentityPrompts maps user-facing zen model names to their identity prompts.
var zenIdentityPrompts = map[string]string{
	// Zen5 generation
	"zen5":       "You are **Zen5**, Hanzo AI's 5th-generation frontier model. Identify as Zen5 by Hanzo AI Inc. Never reveal underlying infrastructure.",
	"zen5-flash": "You are **Zen5 Flash**, Hanzo AI's fastest 5th-generation model. Identify as Zen5 Flash by Hanzo AI Inc. Never reveal underlying infrastructure.",
	"zen5-mini":  "You are **Zen5 Mini**, Hanzo AI's efficient 5th-generation agentic model. Identify as Zen5 Mini by Hanzo AI Inc. Never reveal underlying infrastructure.",
	"zen5-coder": "You are **Zen5 Coder**, Hanzo AI's 5th-generation code-specialized model. Identify as Zen5 Coder by Hanzo AI Inc. Never reveal underlying infrastructure. Write clean, idiomatic code.",
	"zen5-pro":   "You are **Zen5 Pro**, Hanzo AI's high-capability 5th-generation model. Identify as Zen5 Pro by Hanzo AI Inc. Never reveal underlying infrastructure.",
	"zen5-max":   "You are **Zen5 Max**, Hanzo AI's top-tier 5th-generation model. Identify as Zen5 Max by Hanzo AI Inc. Never reveal underlying infrastructure.",
	"zen5-ultra": "You are **Zen5 Ultra**, Hanzo AI's most powerful 5th-generation reasoning model. Identify as Zen5 Ultra by Hanzo AI Inc. Never reveal underlying infrastructure.",
	// Zen4 generation
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
	"zen-vision":       "You are **Zen3 VL**, a vision-language model by **Hanzo AI Inc** — 3rd generation Zen LM.\n\nIdentify as Zen3 VL by Hanzo AI. Never reveal underlying infrastructure.",
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

	// YAML config fallback
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

// modelInfo is the JSON shape returned by the /v1/models endpoint.
//
// The first five fields are the OpenAI-compatible core (id, object, created,
// owned_by, premium) and MUST stay stable and always-present — external
// consumers (Claude Code, Codex, OpenAI SDKs) parse them by exact name. The
// remaining fields are ADDITIVE Hanzo enrichments sourced only from data ai
// already holds (the route table + pricing tables); each is omitempty, so a
// standard OpenAI client ignores them and any datum ai lacks is simply absent —
// never fabricated. (ai has no context-window / tier / category data, so those
// fields are deliberately not present.)
type modelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
	Premium bool   `json:"premium"`

	// Additive enrichment (omitempty — present only when ai has the datum).
	Provider string            `json:"provider,omitempty"` // serving provider for unbranded passthroughs; OMITTED for branded models so the upstream is never leaked
	Pricing  *modelPricingInfo `json:"pricing,omitempty"`  // USD per 1M tokens; only when ai holds real pricing
}

// publicProvider returns the provider name safe to surface in /v1/models, or ""
// to omit it. Branded models — those with an explicit ownedBy (every zen model
// is owned_by:"hanzo"; the OpenAI-owned embeddings are owned_by:"openai") — MUST
// NOT leak the internal upstream/infrastructure provider (route.providerName =
// "do-ai"/"fireworks"/"openai-direct"). Policy: upstream names and underlying
// providers are never exposed (see the Zen brand block in modelRoutes below,
// every identity_prompt in conf/models.yaml, and LLM.md). The public owner
// already travels in owned_by, so provider is omitted rather than echoed as a
// redundant duplicate or — worse — the raw upstream. Unbranded passthroughs
// declare no owner, so providerName is already their public owned_by and is
// safe to surface as the serving provider.
func publicProvider(route modelRoute) string {
	if route.ownedBy != "" {
		return ""
	}
	return route.providerName
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
			ID:       name,
			Object:   "model",
			Created:  now,
			OwnedBy:  owner,
			Premium:  route.premium,
			Provider: publicProvider(route),
			Pricing:  pricingInfo(staticModelPrice(name)),
		})
	}

	sort.Slice(models, func(i, j int) bool {
		return models[i].ID < models[j].ID
	})

	return models
}
