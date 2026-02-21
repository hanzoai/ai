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
)

// StarterCreditDollars is the amount granted to new users as free credit.
// Premium models require a balance above this threshold to ensure the user
// has added real funds beyond the starter credit.
const StarterCreditDollars = 5.00

// modelPrice defines per-model pricing in dollars per 1M tokens.
type modelPrice struct {
	InputPerMillion  float64 // $ per 1M input tokens
	OutputPerMillion float64 // $ per 1M output tokens
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
	"gpt-4o":             {InputPerMillion: 2.50, OutputPerMillion: 10.00},
	"gpt-4o-mini":        {InputPerMillion: 0.15, OutputPerMillion: 0.60},
	"gpt-4.1":            {InputPerMillion: 2.00, OutputPerMillion: 8.00},
	"gpt-5":              {InputPerMillion: 5.00, OutputPerMillion: 15.00},
	"gpt-5-mini":         {InputPerMillion: 1.25, OutputPerMillion: 5.00},
	"gpt-5-nano":         {InputPerMillion: 0.30, OutputPerMillion: 1.20},
	"gpt-5.1-codex-max":  {InputPerMillion: 3.00, OutputPerMillion: 12.00},
	"gpt-5.2":            {InputPerMillion: 5.00, OutputPerMillion: 15.00},
	"gpt-5.2-pro":        {InputPerMillion: 10.00, OutputPerMillion: 30.00},
	"gpt-oss-120b":       {InputPerMillion: 0.90, OutputPerMillion: 0.90},
	"gpt-oss-20b":        {InputPerMillion: 0.20, OutputPerMillion: 0.20},
	"o1":                 {InputPerMillion: 15.00, OutputPerMillion: 60.00},
	"o3":                 {InputPerMillion: 10.00, OutputPerMillion: 40.00},
	"o3-mini":            {InputPerMillion: 1.10, OutputPerMillion: 4.40},

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

	// ── Fireworks premium models ────────────────────────────────────

	"fireworks/cogito-671b":                {InputPerMillion: 3.00, OutputPerMillion: 3.00},
	"fireworks/deepseek-r1":                {InputPerMillion: 3.00, OutputPerMillion: 8.00},
	"fireworks/deepseek-v3":                {InputPerMillion: 0.90, OutputPerMillion: 0.90},
	"fireworks/deepseek-v3p1":              {InputPerMillion: 0.90, OutputPerMillion: 0.90},
	"fireworks/deepseek-v3p2":              {InputPerMillion: 0.90, OutputPerMillion: 0.90},
	"fireworks/glm-4p7":                    {InputPerMillion: 1.80, OutputPerMillion: 6.60},
	"fireworks/glm-5":                      {InputPerMillion: 3.00, OutputPerMillion: 9.60},
	"fireworks/glm-5-thinking":             {InputPerMillion: 3.00, OutputPerMillion: 9.60},
	"fireworks/gpt-oss-120b":               {InputPerMillion: 0.90, OutputPerMillion: 0.90},
	"fireworks/gpt-oss-20b":                {InputPerMillion: 0.20, OutputPerMillion: 0.20},
	"fireworks/kimi-k2":                    {InputPerMillion: 2.00, OutputPerMillion: 8.00},
	"fireworks/kimi-k2-thinking":           {InputPerMillion: 2.00, OutputPerMillion: 8.00},
	"fireworks/kimi-k2p5":                  {InputPerMillion: 2.50, OutputPerMillion: 10.00},
	"fireworks/minimax-m2p5":               {InputPerMillion: 1.00, OutputPerMillion: 4.00},
	"fireworks/mixtral-8x22b":              {InputPerMillion: 0.90, OutputPerMillion: 0.90},
	"fireworks/qwen3-4b":                   {InputPerMillion: 0.30, OutputPerMillion: 0.30},
	"fireworks/qwen3-8b":                   {InputPerMillion: 0.60, OutputPerMillion: 0.60},
	"fireworks/qwen3-235b-a22b":            {InputPerMillion: 3.60, OutputPerMillion: 3.60},
	"fireworks/qwen3-coder-30b-a3b":        {InputPerMillion: 1.50, OutputPerMillion: 1.50},
	"fireworks/qwen3-coder-480b":           {InputPerMillion: 3.60, OutputPerMillion: 3.60},
	"fireworks/qwen3-coder-480b-bf16":      {InputPerMillion: 4.50, OutputPerMillion: 4.50},
	"fireworks/qwen3-next-80b-a3b":         {InputPerMillion: 2.70, OutputPerMillion: 2.70},
	"fireworks/qwen3-next-80b-a3b-thinking": {InputPerMillion: 2.70, OutputPerMillion: 2.70},
	"fireworks/qwen3-vl-30b-a3b":           {InputPerMillion: 0.45, OutputPerMillion: 1.80},
	"fireworks/qwen3-vl-235b":              {InputPerMillion: 1.20, OutputPerMillion: 1.20},

	// ── OpenAI Direct premium models ────────────────────────────────

	"openai-direct/gpt-4o":      {InputPerMillion: 2.50, OutputPerMillion: 10.00},
	"openai-direct/gpt-4o-mini": {InputPerMillion: 0.15, OutputPerMillion: 0.60},
	"openai-direct/gpt-5":       {InputPerMillion: 5.00, OutputPerMillion: 15.00},
	"openai-direct/o3":          {InputPerMillion: 10.00, OutputPerMillion: 40.00},
	"openai-direct/o3-mini":     {InputPerMillion: 1.10, OutputPerMillion: 4.40},

	// ── Zen branded models (use Fireworks pricing via upstream) ──────

	// Zen4 models
	"zen4":             {InputPerMillion: 3.00, OutputPerMillion: 9.60},   // GLM-5
	"zen4-ultra":       {InputPerMillion: 3.00, OutputPerMillion: 9.60},   // GLM-5 thinking
	"zen4-pro":         {InputPerMillion: 2.70, OutputPerMillion: 2.70},   // Qwen3-Next-80B-A3B
	"zen4-max":         {InputPerMillion: 3.60, OutputPerMillion: 3.60},   // Qwen3-235B-A22B
	"zen4-mini":        {InputPerMillion: 0.60, OutputPerMillion: 0.60},   // Qwen3-8B
	"zen4-thinking":    {InputPerMillion: 2.70, OutputPerMillion: 2.70},   // Qwen3-Next-80B-A3B thinking
	"zen4-coder":       {InputPerMillion: 3.60, OutputPerMillion: 3.60},   // Qwen3-Coder-480B-A35B
	"zen4-coder-pro":   {InputPerMillion: 4.50, OutputPerMillion: 4.50},   // Qwen3-Coder-480B BF16
	"zen4-coder-flash": {InputPerMillion: 1.50, OutputPerMillion: 1.50},   // Qwen3-Coder-30B-A3B
	"zen3-omni":        {InputPerMillion: 1.80, OutputPerMillion: 6.60},   // GLM-4.7
	"zen3-vl":          {InputPerMillion: 0.45, OutputPerMillion: 1.80},   // Qwen3-VL-30B-A3B
	"zen3-nano":        {InputPerMillion: 0.30, OutputPerMillion: 0.30},   // Qwen3-4B
	"zen3-guard":       {InputPerMillion: 0.30, OutputPerMillion: 0.30},   // Qwen3-4B
	"zen3-embedding":   {InputPerMillion: 0.39, OutputPerMillion: 0.39},   // text-embedding-3-large

	// Versionless aliases (same pricing as zen4/zen3 variants)
	"zen":             {InputPerMillion: 3.00, OutputPerMillion: 9.60},
	"zen-pro":         {InputPerMillion: 2.70, OutputPerMillion: 2.70},
	"zen-max":         {InputPerMillion: 3.60, OutputPerMillion: 3.60},
	"zen-mini":        {InputPerMillion: 0.60, OutputPerMillion: 0.60},
	"zen-ultra":       {InputPerMillion: 3.00, OutputPerMillion: 9.60},
	"zen-coder":       {InputPerMillion: 3.60, OutputPerMillion: 3.60},
	"zen-coder-flash": {InputPerMillion: 1.50, OutputPerMillion: 1.50},
	"zen-coder-pro":   {InputPerMillion: 4.50, OutputPerMillion: 4.50},
	"zen-thinking":    {InputPerMillion: 2.70, OutputPerMillion: 2.70},
	"zen-vl":          {InputPerMillion: 0.45, OutputPerMillion: 1.80},
	"zen-nano":        {InputPerMillion: 0.30, OutputPerMillion: 0.30},
	"zen-omni":        {InputPerMillion: 1.80, OutputPerMillion: 6.60},
	"zen-guard":       {InputPerMillion: 0.30, OutputPerMillion: 0.30},
	"zen-embedding":   {InputPerMillion: 0.39, OutputPerMillion: 0.39},
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
	price := getModelPrice(model)

	inputCost := float64(promptTokens) * price.InputPerMillion / 1_000_000.0
	outputCost := float64(completionTokens) * price.OutputPerMillion / 1_000_000.0
	totalDollars := inputCost + outputCost
	costCents := int64(math.Round(totalDollars * 100))

	// Minimum 1 cent for any non-zero usage
	if costCents <= 0 && (promptTokens > 0 || completionTokens > 0) {
		costCents = 1
	}

	return costCents
}
