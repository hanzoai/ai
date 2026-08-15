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
	"sort"
	"strings"

	"github.com/hanzoai/ai/conf"
)

// familyProvider resolves a model family's provider. The ADMIN ROW IS THE ONE
// CONTROL: if an admin-owned row exists for this family, it decides — its State
// gates the family, and its ProviderUrl/ClientSecret address it, so an operator
// turns a family on or off, or repoints it, from admin.hanzo.ai like any other
// Model provider. Deployment configuration (a base-URL key + a service-key key)
// is the BOOTSTRAP default, used only when no row has been written yet.
//
// It used to read configuration and nothing else, on the reasoning that "a family
// is not a database row: its address is configuration, so this repository carries
// no routing detail". The second half still holds — a row is runtime data and
// never lands in the repo, so nothing is disclosed by reading one — but the first
// half cost us the admin surface. /v1/admin/providers listed `openrouter` and
// offered a toggle that flipped a row this function never read, so enabling a
// family from the console did nothing and reported success. A control that
// silently does nothing is worse than no control.
//
// Returns nil when the family is neither configured nor rowed, and nil when its
// row is disabled — the same shape as a missing provider, which is what every
// caller already handles. Zen, Enso and OpenRouter are the three instances.
func familyProvider(name, typ, urlKey, keyKey string) *Provider {
	base := strings.TrimRight(strings.TrimSpace(conf.GetConfigString(urlKey)), "/")
	// The credential resolves through the ONE precedence rule (resolveSecretName):
	// the embedded KMS store first, the environment second. It read the
	// environment alone, which is the one place a provider credential cannot be
	// governed — a value in the process environment is readable by anything that
	// can reach the process, is outside the per-secret policy KMS already keeps,
	// and leaves no record of who read it. Sealing the key in KMS now moves it out
	// of the environment without any further change here; leaving it in the
	// environment keeps working, because absence in the store falls through.
	key := strings.TrimSpace(resolveSecretName(keyKey))

	// The admin row wins where it speaks. getProvider is the direct store read,
	// NOT GetModelProviderByName — which resolves families through here and would
	// recurse. The store may not exist yet: this runs during boot and in tests, so
	// an absent adapter falls through to configuration rather than panicking.
	if adapter != nil && adapter.db != nil {
		if row, err := getProvider("admin", name); err == nil && row != nil && row.Name == name {
			if row.Category == "Model" && row.State != "" && row.State != "Active" {
				return nil // disabled from the console: the family serves nothing
			}
			if u := strings.TrimRight(strings.TrimSpace(row.ProviderUrl), "/"); u != "" {
				base = u
			}
			if s := strings.TrimSpace(row.ClientSecret); s != "" {
				key = s
			}
		}
	}

	if base == "" {
		return nil
	}
	p := &Provider{
		Owner:        "admin",
		Name:         name,
		Category:     "Model",
		Type:         typ,
		State:        "Active",
		ProviderUrl:  base,
		ClientSecret: key,
	}

	// An operator stores the key as a kms:// reference (secrets live in KMS, never
	// in a row), and the family relay sends ClientSecret STRAIGHT into an
	// Authorization header. Resolve here, in the one constructor, so no caller can
	// forget: an unresolved reference would authenticate as the literal string
	// "kms://…" — a guaranteed upstream 401 that also puts the reference on the
	// wire. A no-op for a plain key, which is what deployment config supplies.
	// Fails CLOSED: a family whose key cannot be resolved serves nothing rather
	// than leaking the reference.
	if err := ResolveProviderSecret(p); err != nil {
		return nil
	}
	if strings.HasPrefix(p.ClientSecret, "kms://") {
		return nil
	}
	return p
}

// familyProviderFns is the ONE list of model families known to provider
// resolution, keyed by the family's provider name. GetModelProviderByName reads
// it instead of hand-writing the names, so a family added here is resolvable
// everywhere by construction. Keep it in step with controllers.modelFamilies —
// TestFamilyProviderFnsCoverEveryFamily fails when it is not.
var familyProviderFns = map[string]func() *Provider{
	"zen":        ZenProvider,
	"enso":       EnsoProvider,
	"openrouter": OpenRouterProvider,
}

// FamilyProviderNames returns the family names provider resolution knows about.
// Exported so the controllers package — which owns the modelFamilies list and
// cannot be imported from here — can assert the two agree.
func FamilyProviderNames() []string {
	names := make([]string, 0, len(familyProviderFns))
	for n := range familyProviderFns {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
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
