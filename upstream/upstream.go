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

// Package upstream is the one place that knows where a provider lives and how a
// call proves it may reach one. It is a leaf on purpose: it depends on object and
// the standard library and nothing else, so a second door — hanzoai/egress, which
// exists so that callers never hold a vendor key — reaches the same addresses and
// the same credential scheme by importing them rather than restating them.
//
// A copy would not stay a copy. Provider addresses and auth schemes are facts
// about vendors, and two files holding them drift in the direction that is hardest
// to notice: the door that is wrong sends a credential to the wrong host.
package upstream

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/hanzoai/ai/object"
)

// Reaching an upstream asks two questions, and they are separate questions.
// Where does the call go, and how does this deployment prove it may make it.
// One function answers each. The address is a value anybody may hold; the
// credential is applied to a request and never handed back, so the number of
// places that can mishandle it is one.

// Endpoint returns the upstream URL for a provider and an OpenAI-style API path
// ("chat/completions", "embeddings", "rerank"). It is the single place that knows
// each provider's address, so every OpenAI-compatible surface is built by varying
// path alone and no per-endpoint copy of provider routing exists.
//
// A provider whose address is unknown yields "": the relay refuses rather than
// guessing a host, and every caller checks for it before sending anything.
//
// ProviderUrl is honoured for OpenAI, Azure, Local/Ollama/DigitalOcean and any
// other type; it is deliberately IGNORED for Fireworks, Grok, OpenRouter,
// Moonshot, Gemini, Jina and Cohere, whose single public address is stated here
// rather than taken from a row. Changing that would move traffic — and a
// credential — to a different host, so it stays as it is.
func Endpoint(provider *object.Provider, path string) string {
	switch provider.Type {
	case "OpenAI":
		base := provider.ProviderUrl
		if base == "" {
			base = "https://api.openai.com/v1"
		}
		base = strings.TrimRight(base, "/")
		if !strings.HasSuffix(base, "/v1") {
			base += "/v1"
		}
		return base + "/" + path

	case "Fireworks":
		return "https://api.fireworks.ai/inference/v1/" + path

	case "Grok":
		return "https://api.x.ai/v1/" + path

	case "OpenRouter":
		return "https://openrouter.ai/api/v1/" + path

	case "Moonshot":
		return "https://api.moonshot.cn/v1/" + path

	case "Gemini":
		// Gemini exposes an OpenAI-compatible surface under /v1beta/openai.
		return "https://generativelanguage.googleapis.com/v1beta/openai/" + path

	case "Jina":
		// Jina AI: OpenAI-compatible /v1/embeddings and a native /v1/rerank.
		return "https://api.jina.ai/v1/" + path

	case "Cohere":
		// Cohere v1 exposes /v1/embeddings and /v1/rerank.
		return "https://api.cohere.com/v1/" + path

	case "Azure":
		base := strings.TrimRight(provider.ProviderUrl, "/")
		version := provider.ApiVersion
		if version == "" {
			version = "2024-02-01"
		}
		return fmt.Sprintf("%s/openai/deployments/%s/%s?api-version=%s",
			base, provider.SubType, path, version)

	case "Local", "Ollama", "DigitalOcean":
		// Local/compatible providers with custom URLs.
		base := strings.TrimRight(provider.ProviderUrl, "/")
		if base == "" {
			return ""
		}
		if strings.HasSuffix(base, "/v1") {
			return base + "/" + path
		}
		return base + "/v1/" + path

	default:
		// Any other OpenAI-compatible provider with a custom URL.
		if provider.ProviderUrl == "" {
			return ""
		}
		return strings.TrimRight(provider.ProviderUrl, "/") + "/" + path
	}
}

// Authorize applies a provider's credential to an outbound request and returns
// nothing. It is the one place on the relay that reads a provider secret, and the
// header it writes is the only form that secret takes: there is no value for a
// caller to log, echo into an error, or forward somewhere it does not belong.
//
// The scheme belongs to the provider, not to the surface calling it. Azure names
// its key in the Authorization value, Anthropic sends it in a header of its own,
// and everything else carries a bearer token — so any door that reaches an
// Anthropic upstream sends x-api-key without having to remember to.
//
// A nil provider means the call carries no credential (a session already opened
// with one), which is a fact about the call, not a mistake to guard against.
func Authorize(req *http.Request, provider *object.Provider) {
	if provider == nil {
		return
	}
	switch provider.Type {
	case "Azure":
		req.Header.Set("Authorization", "api-key "+provider.ClientSecret)
	case "Claude", "Anthropic":
		req.Header.Set("x-api-key", provider.ClientSecret)
	default:
		if provider.ClientSecret != "" {
			req.Header.Set("Authorization", "Bearer "+provider.ClientSecret)
		}
	}
}
