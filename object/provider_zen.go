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

package object

import (
	"strings"

	"github.com/hanzoai/ai/conf"
)

// familyProvider synthesizes a virtual model-family provider from deployment
// configuration (a base-URL key + a service-key key), or nil when the family is not
// configured. A family is not a database row: its address is configuration, so this
// repository carries no routing detail and reads as open source. Because it resolves
// the same way a real Model provider does — a name, a URL, a key — every path that
// looks a provider up by name serves the family without a special case beyond the one
// lookup in GetModelProviderByName. Zen and Enso are two instances. See hip-00NN.
func familyProvider(name, typ, urlKey, keyKey string) *Provider {
	base := strings.TrimRight(strings.TrimSpace(conf.GetConfigString(urlKey)), "/")
	if base == "" {
		return nil
	}
	return &Provider{
		Owner:        "admin",
		Name:         name,
		Category:     "Model",
		Type:         typ,
		State:        "Active",
		ProviderUrl:  base,
		ClientSecret: strings.TrimSpace(conf.GetConfigString(keyKey)),
	}
}

// ZenProvider is the open Zen family's virtual provider (ZEN_URL / ZEN_API_KEY).
func ZenProvider() *Provider { return familyProvider("zen", "Zen", "ZEN_URL", "ZEN_API_KEY") }

// EnsoProvider is the proprietary Enso family's virtual provider (ENSO_URL /
// ENSO_API_KEY) — the SAME zen serving binary run with ZEN_FAMILY=enso.
func EnsoProvider() *Provider { return familyProvider("enso", "Enso", "ENSO_URL", "ENSO_API_KEY") }

// OpenRouterProvider is the OpenRouter catalog's provider (OPENROUTER_URL /
// OPENROUTER_API_KEY). Type "OpenRouter" already resolves to the OpenAI-compatible
// upstream in resolveEndpointForPath, so serving needs nothing new — the catalog is a
// discovered family, and the relay that carries it is the one ai already had.
func OpenRouterProvider() *Provider {
	return familyProvider("openrouter", "OpenRouter", "OPENROUTER_URL", "OPENROUTER_API_KEY")
}
