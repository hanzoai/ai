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

// GetModelProviderByName retrieves a Model-category provider by its Name field
// (e.g. "do-ai", "fireworks", "openai-direct"). Results are cached for 60 seconds.
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
		return GetModelProviderByName(name) // no override → global built-in
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
