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

package object

import (
	"fmt"
	"sync"
	"time"

	"github.com/hanzoai/cloud/i18n"
	"github.com/hanzoai/cloud/model"
)

// GetProviderByProviderKey retrieves a provider using the Provider key
func GetProviderByProviderKey(providerKey string, lang string) (*Provider, error) {
	if providerKey == "" {
		return nil, fmt.Errorf(i18n.Translate(lang, "object:empty provider key"))
	}

	provider := &Provider{}

	// Try to find in main database first
	existed, err := adapter.engine.Where("provider_key = ?", providerKey).Get(provider)
	if err != nil {
		return nil, err
	}

	// If not found in main database, try provider adapter
	if providerAdapter != nil && !existed {
		existed, err = providerAdapter.engine.Where("provider_key = ?", providerKey).Get(provider)
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
		return nil, fmt.Errorf(i18n.Translate(lang, "object:The provider is not found"))
	}

	// Ensure it's a model provider
	if provider.Category != "Model" {
		return nil, fmt.Errorf(i18n.Translate(lang, "object:The model provider: %s is not found"), provider.Name)
	}

	modelProvider, err := provider.GetModelProvider(lang)
	if err != nil {
		return nil, err
	}
	if modelProvider == nil {
		return nil, fmt.Errorf(i18n.Translate(lang, "object:The model provider: %s is not found"), provider.Name)
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
	existed, err := adapter.engine.Get(&provider)
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
	existed, err := adapter.engine.Get(&provider)
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
	existed, err := adapter.engine.UseBool("is_default").Get(&provider)
	if err != nil {
		return &provider, err
	}

	if providerAdapter != nil && !existed {
		existed, err = providerAdapter.engine.UseBool("is_default").Get(&provider)
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
	existed, err := adapter.engine.UseBool("is_default").Get(&provider)
	if err != nil {
		return &provider, err
	}

	if providerAdapter != nil && !existed {
		existed, err = providerAdapter.engine.UseBool("is_default").Get(&provider)
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
	existed, err := adapter.engine.UseBool("is_default").Get(&provider)
	if err != nil {
		return &provider, err
	}

	if providerAdapter != nil && !existed {
		existed, err = providerAdapter.engine.UseBool("is_default").Get(&provider)
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
	existed, err := adapter.engine.UseBool("is_default").Get(&provider)
	if err != nil {
		return &provider, err
	}

	if providerAdapter != nil && !existed {
		existed, err = providerAdapter.engine.UseBool("is_default").Get(&provider)
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
	existed, err := adapter.engine.UseBool("is_default").Get(&provider)
	if err != nil {
		return &provider, err
	}

	if providerAdapter != nil && !existed {
		existed, err = providerAdapter.engine.UseBool("is_default").Get(&provider)
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
	existed, err := adapter.engine.Get(&provider)
	if err != nil {
		return &provider, err
	}

	if providerAdapter != nil && !existed {
		existed, err = providerAdapter.engine.Get(&provider)
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

// GetModelProviderByType retrieves a model provider by its type (e.g. "OpenAI", "Claude", "Fireworks").
func GetModelProviderByType(providerType string) (*Provider, error) {
	provider := &Provider{}
	existed, err := adapter.engine.Where("category = ? AND type = ?", "Model", providerType).Get(provider)
	if err != nil {
		return nil, err
	}

	if providerAdapter != nil && !existed {
		existed, err = providerAdapter.engine.Where("category = ? AND type = ?", "Model", providerType).Get(provider)
		if err != nil {
			return nil, err
		}
	}

	if !existed {
		return nil, nil
	}

	return provider, nil
}
