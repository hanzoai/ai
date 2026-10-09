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
	"context"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

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
// half cost us the admin surface. /v1/admin/ai/providers listed `openrouter` and
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
	key := strings.TrimSpace(resolveKey(keyKey))

	// The admin row wins where it speaks.
	if row := familyRow(name); row != nil {
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

// familyRow is the admin-owned row for a family, or nil when there is none.
// getProvider is the direct store read, NOT GetModelProviderByName — which
// resolves families through familyProvider and would recurse. The store may not
// exist yet (boot, tests), and then there is no row.
func familyRow(name string) *Provider {
	if adapter == nil || adapter.db == nil {
		return nil
	}
	row, err := getProvider("admin", name)
	if err != nil || row == nil || row.Name != name {
		return nil
	}
	return row
}

// OpenRouterKeys names the OpenRouter credentials in the order a request tries
// them: OPENROUTER_API_KEY, the funded account, then OPENROUTER_API_KEY_2 through
// _8. Each is a separate account. A name with no value is skipped (FamilyKeys) and
// its absence is cached, so another account joins the pool by writing its key under
// the next name at the provider keys path (PROVIDER_KEYS_PATH, hanzo/prod:/ai).
var OpenRouterKeys = keySeries("OPENROUTER_API_KEY", 8)

// keySeries is name, then name_2 through name_n.
func keySeries(name string, n int) []string {
	out := []string{name}
	for i := 2; i <= n; i++ {
		out = append(out, name+"_"+strconv.Itoa(i))
	}
	return out
}

// FamilyKeys returns the credentials a family's requests try, in order, each
// resolved the way familyProvider resolves its one key (resolveKey).
// A name with no value is skipped and a value repeated under a second name is
// tried once. It returns nil when the family's admin row supplies the key:
// that key is then the only one, carried on the provider as before.
func FamilyKeys(family string, names []string) []string {
	if row := familyRow(family); row != nil && strings.TrimSpace(row.ClientSecret) != "" {
		return nil
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		v := strings.TrimSpace(resolveKey(n))
		if v == "" || slices.Contains(out, v) {
			continue
		}
		out = append(out, v)
	}
	return out
}

// AccountOf is the name among names whose key is key — the account a request was
// sent on, said without its key — or "" when none resolves to it.
func AccountOf(names []string, key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	for _, n := range names {
		if strings.TrimSpace(resolveKey(n)) == key {
			return n
		}
	}
	return ""
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
// upstream in endpoint, so serving needs nothing new — the catalog is a
// discovered family, and the relay that carries it is the one ai already had.
func OpenRouterProvider() *Provider {
	return familyProvider("openrouter", "OpenRouter", "OPENROUTER_URL", "OPENROUTER_API_KEY")
}

// KaiName is the decision service's provider name — the one the decision routes
// (conf/models.yaml, provider kai) name.
const KaiName = "kai"

// KaiProvider is the decision service (KAI_URL / KAI_API_KEY): Kai, Hanzo's
// decision model, and the decision models it forwards. It is addressed the way a
// family is — deployment config, overridden by an admin row of its name — but it
// is not one: it answers POST /v1/decisions and nothing else, so nothing
// discovers a catalog from it or pipes a chat turn to it. In-cluster it takes no
// credential, so KAI_API_KEY is unset and no Authorization header is sent.
func KaiProvider() *Provider { return familyProvider(KaiName, "Kai", "KAI_URL", "KAI_API_KEY") }

// Capabilities reads an org's published capabilities as the decision service
// reads them on each of the org's decisions (X-Org-Capabilities): a JSON array,
// "[]" or "" for none.
type Capabilities func(ctx context.Context, org string) (string, error)

var capabilities atomic.Pointer[Capabilities]

// SetCapabilities binds the read the host holds: the org's published capabilities
// live in the host's train store, so ai asks whoever mounted it. nil unbinds it.
func SetCapabilities(f Capabilities) {
	if f == nil {
		capabilities.Store(nil)
		return
	}
	capabilities.Store(&f)
}

// OrgCapabilities is org's published capabilities, "" when the host bound no read.
func OrgCapabilities(ctx context.Context, org string) (string, error) {
	f := capabilities.Load()
	if f == nil {
		return "", nil
	}
	return (*f)(ctx, org)
}
