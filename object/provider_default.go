// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2025 The OpenAgent Authors. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package object

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/ai/i18n"
	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/dbx"
)

// GetProviderByProviderKey retrieves a provider using the Provider key
func GetProviderByProviderKey(providerKey string, lang string) (*Provider, error) {
	if providerKey == "" {
		return nil, fmt.Errorf("%s", i18n.Translate(lang, "object:empty provider key"))
	}
	provider := &Provider{}
	// Try to find in main database first
	existed, err := getOne(adapter.db, "provider", provider, dbx.HashExp{"provider_key": providerKey})
	if err != nil {
		return nil, err
	}
	// If not found in main database, try provider adapter
	if providerAdapter != nil && !existed {
		existed, err = getOne(providerAdapter.db, "provider", provider, dbx.HashExp{"provider_key": providerKey})
		if err != nil {
			return nil, err
		}
	}
	if existed {
		return provider, nil
	}
	return nil, nil
}

// GetModelProviderByProviderKey retrieves both the provider and its model provider by API key
func GetModelProviderByProviderKey(providerKey string, lang string) (model.ModelProvider, error) {
	provider, err := GetProviderByProviderKey(providerKey, lang)
	if err != nil {
		return nil, err
	}
	if provider == nil {
		return nil, fmt.Errorf("%s", i18n.Translate(lang, "object:The provider is not found"))
	}
	// Ensure it's a model provider
	if provider.Category != "Model" {
		return nil, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:The model provider: %s is not found"), provider.Name))
	}
	modelProvider, err := provider.GetModelProvider(lang)
	if err != nil {
		return nil, err
	}
	if modelProvider == nil {
		return nil, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:The model provider: %s is not found"), provider.Name))
	}
	return modelProvider, nil
}

func getFilteredProviders(providers []*Provider, needStorage bool) []*Provider {
	res := []*Provider{}
	for _, provider := range providers {
		if (needStorage && provider.Category == "Storage") || (!needStorage && provider.Category != "Storage") {
			res = append(res, provider)
		}
	}
	return res
}

func GetDefaultStorageProvider() (*Provider, error) {
	provider := Provider{Owner: "admin", Category: "Storage"}
	existed, err := getOne(adapter.db, "provider", &provider, pk2(provider.Owner, provider.Name))
	if err != nil {
		return &provider, err
	}
	if !existed {
		return nil, nil
	}
	return &provider, nil
}

func GetDefaultVideoProvider() (*Provider, error) {
	provider := Provider{Owner: "admin", Category: "Video"}
	existed, err := getOne(adapter.db, "provider", &provider, pk2(provider.Owner, provider.Name))
	if err != nil {
		return &provider, err
	}
	if !existed {
		return nil, nil
	}
	return &provider, nil
}

func GetDefaultModelProvider() (*Provider, error) {
	provider := Provider{Owner: "admin", Category: "Model", IsDefault: true}
	existed, err := getOne(adapter.db, "provider", &provider, dbx.HashExp{"is_default": true, "category": provider.Category})
	if err != nil {
		return &provider, err
	}
	if providerAdapter != nil && !existed {
		existed, err = getOne(providerAdapter.db, "provider", &provider, dbx.HashExp{"is_default": true, "category": provider.Category})
		if err != nil {
			return &provider, err
		}
	}
	if !existed {
		return nil, nil
	}
	return &provider, nil
}

func GetDefaultEmbeddingProvider() (*Provider, error) {
	provider := Provider{Owner: "admin", Category: "Embedding", IsDefault: true}
	existed, err := getOne(adapter.db, "provider", &provider, dbx.HashExp{"is_default": true, "category": provider.Category})
	if err != nil {
		return &provider, err
	}
	if providerAdapter != nil && !existed {
		existed, err = getOne(providerAdapter.db, "provider", &provider, dbx.HashExp{"is_default": true, "category": provider.Category})
		if err != nil {
			return &provider, err
		}
	}
	if !existed {
		return nil, nil
	}
	return &provider, nil
}

func GetDefaultBlockchainProvider() (*Provider, error) {
	provider := Provider{Owner: "admin", Category: "Blockchain", IsDefault: true}
	existed, err := getOne(adapter.db, "provider", &provider, dbx.HashExp{"is_default": true, "category": provider.Category})
	if err != nil {
		return &provider, err
	}
	if providerAdapter != nil && !existed {
		existed, err = getOne(providerAdapter.db, "provider", &provider, dbx.HashExp{"is_default": true, "category": provider.Category})
		if err != nil {
			return &provider, err
		}
	}
	if !existed {
		return nil, nil
	}
	return &provider, nil
}

func GetDefaultAgentProvider() (*Provider, error) {
	provider := Provider{Owner: "admin", Category: "Agent", IsDefault: true}
	existed, err := getOne(adapter.db, "provider", &provider, dbx.HashExp{"is_default": true, "category": provider.Category})
	if err != nil {
		return &provider, err
	}
	if providerAdapter != nil && !existed {
		existed, err = getOne(providerAdapter.db, "provider", &provider, dbx.HashExp{"is_default": true, "category": provider.Category})
		if err != nil {
			return &provider, err
		}
	}
	if !existed {
		return nil, nil
	}
	return &provider, nil
}

func GetDefaultTextToSpeechProvider() (*Provider, error) {
	provider := Provider{Owner: "admin", Category: "Text-to-Speech", IsDefault: true}
	existed, err := getOne(adapter.db, "provider", &provider, dbx.HashExp{"is_default": true, "category": provider.Category})
	if err != nil {
		return &provider, err
	}
	if providerAdapter != nil && !existed {
		existed, err = getOne(providerAdapter.db, "provider", &provider, dbx.HashExp{"is_default": true, "category": provider.Category})
		if err != nil {
			return &provider, err
		}
	}
	if !existed {
		return nil, nil
	}
	return &provider, nil
}

func GetDefaultSpeechToTextProvider() (*Provider, error) {
	provider := Provider{Owner: "admin", Category: "Speech-to-Text"}
	existed, err := getOne(adapter.db, "provider", &provider, pk2(provider.Owner, provider.Name))
	if err != nil {
		return &provider, err
	}
	if providerAdapter != nil && !existed {
		existed, err = getOne(providerAdapter.db, "provider", &provider, pk2(provider.Owner, provider.Name))
		if err != nil {
			return &provider, err
		}
	}
	if !existed {
		return nil, nil
	}
	return &provider, nil
}

// providerByNameEntry caches a provider lookup by name to avoid per-request DB queries.
type providerByNameEntry struct {
	provider  *Provider
	fetchedAt time.Time
}

var (
	providerByNameCache    = make(map[string]*providerByNameEntry)
	providerByNameCacheMu  sync.RWMutex
	providerByNameCacheTTL = 60 * time.Second

	// Per-org BYOK override cache, keyed "org/name" (shares the global TTL). A nil
	// provider entry negative-caches "this org has no override → use global".
	orgProviderCache   = make(map[string]*providerByNameEntry)
	orgProviderCacheMu sync.RWMutex
)

// InvalidateProviderNameCache evicts a provider (by admin name) from the hot-path
// resolution caches so a mutation — e.g. an admin enable/disable or set-primary
// via /v1/admin/providers — takes effect IMMEDIATELY instead of after the 60s
// TTL. The global (admin) entry is keyed by name; per-org BYOK entries are keyed
// "org/name", so all org overrides of that provider name are dropped too. Passing
// an empty name flushes everything (used when a bulk change touched many records).
func InvalidateProviderNameCache(name string) {
	providerByNameCacheMu.Lock()
	if name == "" {
		providerByNameCache = make(map[string]*providerByNameEntry)
	} else {
		delete(providerByNameCache, name)
	}
	providerByNameCacheMu.Unlock()

	orgProviderCacheMu.Lock()
	if name == "" {
		orgProviderCache = make(map[string]*providerByNameEntry)
	} else {
		suffix := "/" + name
		for k := range orgProviderCache {
			if strings.HasSuffix(k, suffix) {
				delete(orgProviderCache, k)
			}
		}
	}
	orgProviderCacheMu.Unlock()
}

// ModelProviderUsable reports whether a Model-category provider is currently
// eligible to serve traffic. A Model provider is usable only when its admin
// toggle (State) is "Active" — a "Disabled"/paused provider is treated as
// unavailable so the /v1/admin/providers toggle actually takes effect on the
// hot path. Non-Model providers (Storage, Embedding config records, etc.) are
// governed by their own callers and are not affected by this gate.
//
// This is the ONE place the "is this provider allowed to route?" policy lives:
// GetModelProviderByName (the completion/embeddings/image chokepoint) and
// GetModelProviderByNameForOrg (the per-org BYOK seam) both consult it, so a
// disabled provider is uniformly unavailable to every caller (chat, anthropic,
// embeddings, message_answer, widget, failover) without duplicating the check.
func ModelProviderUsable(p *Provider) bool {
	if p == nil {
		return false
	}
	if p.Category == "Model" && p.State != "Active" {
		return false
	}
	return true
}

// GetModelProviderByName retrieves a Model-category provider by its Name field
// (e.g. "do-ai", "fireworks", "openai-direct"). Results are cached for 60 seconds.
//
// A provider whose admin toggle is disabled (State != "Active") resolves to
// (nil, nil) — the SAME shape as a missing provider — so every completion path
// (chat, anthropic, embeddings, images, message_answer, widget) sees a disabled
// provider as "not configured" and fails/falls-back uniformly. See
// ModelProviderUsable for the single-point policy.
func GetModelProviderByName(name string) (*Provider, error) {
	providerByNameCacheMu.RLock()
	entry, ok := providerByNameCache[name]
	providerByNameCacheMu.RUnlock()
	if ok && time.Since(entry.fetchedAt) < providerByNameCacheTTL {
		if entry.provider == nil {
			return nil, nil
		}
		// Return a shallow copy so callers can mutate fields (e.g. SubType)
		// without corrupting the cached value.
		cp := *entry.provider
		return &cp, nil
	}
	provider, err := getProvider("admin", name)
	if err != nil {
		return nil, err
	}
	// A disabled Model provider is unavailable — cache and return it as nil so
	// the toggle takes effect on the hot path (and the negative result is cached
	// for the TTL, same as a genuinely-missing provider).
	if !ModelProviderUsable(provider) {
		provider = nil
	}
	if provider != nil {
		// Resolve KMS-backed secrets (e.g. "kms://DO_AI_API_KEY" → actual key).
		if err := ResolveProviderSecret(provider); err != nil {
			return nil, err
		}
	}
	providerByNameCacheMu.Lock()
	providerByNameCache[name] = &providerByNameEntry{provider: provider, fetchedAt: time.Now()}
	providerByNameCacheMu.Unlock()
	if provider == nil {
		return nil, nil
	}
	cp := *provider
	return &cp, nil
}

// GetModelProviderByNameForOrg resolves the provider an org's request should use:
// the org's OWN custom provider (BYOK) if it has configured one under its slug,
// otherwise the global built-in provider on api.hanzo.ai (the universal router).
// This is the per-tenant override seam — "let people add their own providers to
// route through" — with the global default inherited until they do. An empty or
// "admin" org has no override and takes the global path directly.
func GetModelProviderByNameForOrg(orgId, name string) (*Provider, error) {
	if orgId == "" || orgId == "admin" {
		return GetModelProviderByName(name)
	}

	cacheKey := orgId + "/" + name
	orgProviderCacheMu.RLock()
	entry, ok := orgProviderCache[cacheKey]
	orgProviderCacheMu.RUnlock()
	if ok && time.Since(entry.fetchedAt) < providerByNameCacheTTL {
		if entry.provider == nil {
			return GetModelProviderByName(name) // negative-cached: no override → global
		}
		cp := *entry.provider
		return &cp, nil
	}

	// The org's own provider record is keyed (Owner=orgId, Name=name).
	provider, err := getProvider(orgId, name)
	if err != nil {
		return nil, err
	}
	// A disabled org BYOK override reverts the org to the global built-in — the
	// same behavior as "no override configured". This makes toggling an org's own
	// provider off a clean fall-back to platform defaults rather than a hard
	// failure, and keeps the disabled-provider policy in one predicate.
	if !ModelProviderUsable(provider) {
		provider = nil
	}
	if provider != nil {
		// BYOK key is stored as a kms:// ref (never plaintext); resolve it.
		if err := ResolveProviderSecret(provider); err != nil {
			return nil, err
		}
	}
	orgProviderCacheMu.Lock()
	orgProviderCache[cacheKey] = &providerByNameEntry{provider: provider, fetchedAt: time.Now()}
	orgProviderCacheMu.Unlock()

	if provider == nil {
		return GetModelProviderByName(name) // no override (or disabled) → global built-in
	}
	cp := *provider
	return &cp, nil
}

// GetModelProviderByType retrieves a model provider by its type (e.g. "OpenAI", "Claude", "Fireworks").
func GetModelProviderByType(providerType string) (*Provider, error) {
	provider := &Provider{}
	existed, err := getOne(adapter.db, "provider", provider, dbx.HashExp{"category": "Model", "type": providerType})
	if err != nil {
		return nil, err
	}
	if providerAdapter != nil && !existed {
		existed, err = getOne(providerAdapter.db, "provider", provider, dbx.HashExp{"category": "Model", "type": providerType})
		if err != nil {
			return nil, err
		}
	}
	if !existed {
		return nil, nil
	}
	return provider, nil
}
