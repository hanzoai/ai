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

// Package crossmodel is the cross-model API compatibility suite for #138: one
// request shape, sent to every major model family over both the OpenAI-compatible
// /v1/chat/completions and the Anthropic-compatible /v1/messages surface, with the
// SSE chunk layout, tool-call serialization, structured output and error-envelope
// contracts asserted on each response.
//
// The live suite is env-gated (HANZO_API_KEY) so it skips in a gate that has no
// credential, and its contract checks run against an httptest server so CI still
// proves the checks themselves work. See qa/crossmodel/README.md.
package crossmodel

import (
	"os"
	"strings"
	"time"
)

// Family is one row of the compatibility matrix: a model family #138 names, and
// what the gateway actually serves for it.
type Family struct {
	Name  string // report label
	Model string // id exactly as GET /v1/models lists it
	Chat  bool   // false = nothing is served on /v1/chat/completions for this id
	Tools bool   // catalog says supports_tools
}

// Matrix pins one representative per family named in #138. The ids are taken from
// the live catalog (2026-11), so a rename surfaces as a 404 model_not_found in the
// suite rather than drifting silently out of coverage.
//
// Kai is listed with Chat=false on purpose: the catalog routes it to /v1/decisions,
// so an identical chat payload cannot reach it at all. That is a matrix finding, not
// a test bug — see README.md.
var Matrix = []Family{
	{Name: "Zen", Model: "hanzo/zen", Chat: true},
	{Name: "Enso", Model: "enso-flash", Chat: true},
	{Name: "Kai", Model: "hanzo/kai"},
	{Name: "Claude", Model: "anthropic/claude-haiku-4.5", Chat: true, Tools: true},
	{Name: "GPT", Model: "openai/gpt-4o-mini", Chat: true, Tools: true},
	{Name: "Gemini", Model: "google/gemini-2.5-flash-lite", Chat: true, Tools: true},
	{Name: "Qwen", Model: "qwen/qwen-plus", Chat: true, Tools: true},
	{Name: "DeepSeek", Model: "deepseek/deepseek-v4-flash", Chat: true, Tools: true},
	{Name: "Llama", Model: "meta-llama/llama-3.3-70b-instruct", Chat: true, Tools: true},
}

// DefaultBase is the production gateway. HANZO_API_URL may name the URL with or
// without the /v1 suffix (the docs print it without).
const DefaultBase = "https://api.hanzo.ai"

// BaseURLEnv and KeyEnv are the two inputs the live suite needs.
const (
	BaseURLEnv = "HANZO_API_URL"
	KeyEnv     = "HANZO_API_KEY"
	// ReportEnv names a path to write the matrix report to (JSON; the .md twin is
	// written beside it). Unset means no artifact.
	ReportEnv = "HANZO_QA_REPORT"
	// LivePublicEnv opts the key-free public-surface checks into real network calls.
	// They are off by default so a gate without egress stays green.
	LivePublicEnv = "HANZO_QA_LIVE"
)

// BaseURL resolves the gateway root, ending in /v1.
func BaseURL() string {
	base := strings.TrimRight(os.Getenv(BaseURLEnv), "/")
	if base == "" {
		base = DefaultBase
	}
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	return base
}

// RequestTimeout bounds one model round trip. Reasoning families stream for a while
// before they answer, so this is generous by design; the whole suite still fails fast
// because every scenario runs with its own context.
const RequestTimeout = 150 * time.Second

// AnthropicVersion is sent on /v1/messages. The gateway defaults it when absent, but
// a compatibility suite should not rely on a default it did not ask for.
const AnthropicVersion = "2023-06-01"
