// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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

// openrouterAliases names, for each short model id a caller already sends, the
// OpenRouter SKU that serves it. Every third-party chat model is served through the
// OpenRouter family, and an alias is how the id a caller knows reaches it: the family
// resolves `gpt-4o` to the discovered `openai/gpt-4o` SKU, so the alias is routed,
// priced, gated, metered and sent upstream under that SKU's terms, and the answer
// still names `gpt-4o` because the envelope wears the id that was asked for.
//
// An alias names a model and never a stand-in for one. Each target is the same model
// at the same size as the id it serves, confirmed against OpenRouter's /v1/models
// listing; an id whose model OpenRouter does not carry has no entry here and keeps
// its own route. A target the live listing no longer carries resolves to nothing,
// so a retired model answers "not available" rather than being served by another.
//
// Keys and targets are lowercase, and no key is itself an OpenRouter id: those are
// served by the family directly and need no entry. Aliases are not listed in
// /v1/models, because the SKU they name already is.
var openrouterAliases = map[string]string{
	// ── OpenAI ──────────────────────────────────────────────────────────────
	"gpt-4o":            "openai/gpt-4o",
	"gpt-4o-mini":       "openai/gpt-4o-mini",
	"gpt-4.1":           "openai/gpt-4.1",
	"gpt-5":             "openai/gpt-5",
	"gpt-5-mini":        "openai/gpt-5-mini",
	"gpt-5-nano":        "openai/gpt-5-nano",
	"gpt-5.1-codex-max": "openai/gpt-5.1-codex-max",
	"gpt-5.2":           "openai/gpt-5.2",
	"gpt-5.2-pro":       "openai/gpt-5.2-pro",
	"gpt-5.3-codex":     "openai/gpt-5.3-codex",
	"gpt-5.4":           "openai/gpt-5.4",
	"gpt-5.4-mini":      "openai/gpt-5.4-mini",
	"gpt-5.4-nano":      "openai/gpt-5.4-nano",
	"gpt-5.4-pro":       "openai/gpt-5.4-pro",
	"gpt-5.5":           "openai/gpt-5.5",
	"gpt-5.6-luna":      "openai/gpt-5.6-luna",
	"gpt-5.6-sol":       "openai/gpt-5.6-sol",
	"gpt-5.6-terra":     "openai/gpt-5.6-terra",
	"gpt-oss-120b":      "openai/gpt-oss-120b",
	"gpt-oss-20b":       "openai/gpt-oss-20b",
	"o1":                "openai/o1",
	"o3":                "openai/o3",
	"o3-mini":           "openai/o3-mini",

	// ── Anthropic ───────────────────────────────────────────────────────────
	// Both spellings are in use: the dashed one Anthropic's own ids take, and the
	// dotted one the catalog published. The dated ids are what Anthropic's SDKs
	// send by default.
	"claude-4-1-opus":                      "anthropic/claude-opus-4.1",
	"claude-haiku-4-5":                     "anthropic/claude-haiku-4.5",
	"claude-haiku-4.5":                     "anthropic/claude-haiku-4.5",
	"claude-opus-4-5":                      "anthropic/claude-opus-4.5",
	"claude-opus-4.5":                      "anthropic/claude-opus-4.5",
	"claude-opus-4-6":                      "anthropic/claude-opus-4.6",
	"claude-opus-4-7":                      "anthropic/claude-opus-4.7",
	"claude-opus-4.7":                      "anthropic/claude-opus-4.7",
	"claude-opus-4-8":                      "anthropic/claude-opus-4.8",
	"claude-opus-4.8":                      "anthropic/claude-opus-4.8",
	"claude-sonnet-4":                      "anthropic/claude-sonnet-4",
	"claude-sonnet-4-5":                    "anthropic/claude-sonnet-4.5",
	"claude-4.5-sonnet":                    "anthropic/claude-sonnet-4.5",
	"claude-sonnet-4-6":                    "anthropic/claude-sonnet-4.6",
	"claude-4.6-sonnet":                    "anthropic/claude-sonnet-4.6",
	"claude-sonnet-5":                      "anthropic/claude-sonnet-5",
	"claude-5-sonnet":                      "anthropic/claude-sonnet-5",
	"claude-fable-5":                       "anthropic/claude-fable-5",
	"anthropic/claude-haiku-4-5":           "anthropic/claude-haiku-4.5",
	"anthropic/claude-haiku-4-5-20251001":  "anthropic/claude-haiku-4.5",
	"anthropic/claude-opus-4-6":            "anthropic/claude-opus-4.6",
	"anthropic/claude-sonnet-4-5-20250929": "anthropic/claude-sonnet-4.5",
	"anthropic/claude-sonnet-4-6":          "anthropic/claude-sonnet-4.6",

	// ── DeepSeek ────────────────────────────────────────────────────────────
	// deepseek-chat and deepseek-reasoner are DeepSeek's floating API names; they
	// name the model they were already served by.
	"deepseek-3.2":                  "deepseek/deepseek-v3.2",
	"deepseek-v3.2":                 "deepseek/deepseek-v3.2",
	"deepseek-chat":                 "deepseek/deepseek-v4-pro",
	"deepseek-reasoner":             "deepseek/deepseek-v4-pro",
	"deepseek-v4-pro":               "deepseek/deepseek-v4-pro",
	"deepseek-v4-pro-0813":          "deepseek/deepseek-v4-pro-0813",
	"deepseek-v4-flash":             "deepseek/deepseek-v4-flash",
	"deepseek-v4-flash-0731":        "deepseek/deepseek-v4-flash-0731",
	"deepseek-r1-distill-70b":       "deepseek/deepseek-r1-distill-llama-70b",
	"deepseek-r1-distill-llama-70b": "deepseek/deepseek-r1-distill-llama-70b",

	// ── Google ──────────────────────────────────────────────────────────────
	"gemma-4-31b":    "google/gemma-4-31b-it",
	"gemma-4-31b-it": "google/gemma-4-31b-it",

	// ── Meta ────────────────────────────────────────────────────────────────
	"llama-3.1-8b":     "meta-llama/llama-3.1-8b-instruct",
	"llama-3.3-70b":    "meta-llama/llama-3.3-70b-instruct",
	"llama-4-maverick": "meta-llama/llama-4-maverick",

	// ── MiniMax, Mistral, Moonshot ──────────────────────────────────────────
	// mistral-3-14b is Ministral 3 14B, the 14B member of the Mistral 3 release.
	"minimax-m2.5":  "minimax/minimax-m2.5",
	"mistral-nemo":  "mistralai/mistral-nemo",
	"mistral-3-14b": "mistralai/ministral-14b-2512",
	"kimi-k2":       "moonshotai/kimi-k2",
	"kimi-k2.5":     "moonshotai/kimi-k2.5",
	"kimi-k2.6":     "moonshotai/kimi-k2.6",
	"kimi-k3":       "moonshotai/kimi-k3",

	// ── NVIDIA ──────────────────────────────────────────────────────────────
	"nemotron-3-nano":       "nvidia/nemotron-3-nano-30b-a3b",
	"nemotron-3-super-120b": "nvidia/nemotron-3-super-120b-a12b",
	"nemotron-3-ultra-550b": "nvidia/nemotron-3-ultra-550b-a55b",

	// ── Qwen ────────────────────────────────────────────────────────────────
	// qwen3-coder is the 480B-A35B coder; qwen3.8-max is served by its current
	// snapshot, not by the separately priced Prime SKU.
	"qwen3-32b":         "qwen/qwen3-32b",
	"qwen3-coder":       "qwen/qwen3-coder",
	"qwen3-coder-flash": "qwen/qwen3-coder-flash",
	"qwen3.5-397b":      "qwen/qwen3.5-397b-a17b",
	"qwen3.5-397b-a17b": "qwen/qwen3.5-397b-a17b",
	"qwen3.8-max":       "qwen/qwen3.8-max-0902",

	// ── Xiaomi, Arcee, Z.ai ─────────────────────────────────────────────────
	"mimo-v2.5":              "xiaomi/mimo-v2.5",
	"mimo-v2.5-pro":          "xiaomi/mimo-v2.5-pro",
	"trinity-large-thinking": "arcee-ai/trinity-large-thinking",
	"glm-5":                  "z-ai/glm-5",
	"glm-5.1":                "z-ai/glm-5.1",
	"glm-5.2":                "z-ai/glm-5.2",
}
