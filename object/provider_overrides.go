// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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

// Per-org and per-user provider API key overrides (OPENROUTER_API_KEY, NVIDIA_API_KEY)
// managed from admin.hanzo.ai and sealed in KMS under tenant-scoped refs:
//
//	orgs/{org}/users/{user}/ai-keys/{provider}   (user override — highest priority)
//	orgs/{org}/ai-keys/{provider}                (org override)
//
// Values never touch SQLite or logs and are cached with a short TTL so changes in
// admin.hanzo.ai take effect within seconds across every inference process.
package object

import (
	"context"
	"strings"
	"sync"
	"time"
)

const (
	// ProviderOverrideOpenRouter is the canonical slug for OPENROUTER_API_KEY overrides.
	ProviderOverrideOpenRouter = "openrouter"
	// ProviderOverrideNvidia is the canonical slug for NVIDIA_API_KEY overrides.
	ProviderOverrideNvidia = "nvidia"

	// tenantOverrideTTL bounds how long a key override change in admin.hanzo.ai
	// takes to reach the inference process. Short by design so rotating or removing
	// an override takes effect almost immediately without hammering KMS on every turn.
	tenantOverrideTTL = 15 * time.Second
)

type overrideCacheEntry struct {
	value     string
	fetchedAt time.Time
}

var (
	overrideCacheMu sync.RWMutex
	overrideCache   = map[string]overrideCacheEntry{}
)

// NormalizeOverrideProvider canonicalizes a supported provider override slug
// ("openrouter" or "nvidia"), returning ("", false) for anything else.
func NormalizeOverrideProvider(provider string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case ProviderOverrideOpenRouter, "openrouter_api_key":
		return ProviderOverrideOpenRouter, true
	case ProviderOverrideNvidia, "nvidia_api_key":
		return ProviderOverrideNvidia, true
	default:
		return "", false
	}
}

func cleanOverrideSegment(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, "/\\@ \t\r\n") || s == "." || s == ".." {
		return ""
	}
	return s
}

// cleanOverrideUser strips a leading "<org>/" prefix if present and validates the bare
// username segment so both "alice" and "acme/alice" map to the same KMS ref.
func cleanOverrideUser(org, user string) string {
	u := strings.TrimSpace(user)
	if u == "" {
		return ""
	}
	if prefix, rest, ok := strings.Cut(u, "/"); ok {
		if org == "" || strings.EqualFold(prefix, org) {
			u = rest
		} else {
			u = rest
		}
	}
	return cleanOverrideSegment(u)
}

// OrgProviderKeyRef returns the canonical KMS secret ref for an organization's
// provider API key override, or "" when org or provider is invalid.
func OrgProviderKeyRef(org, provider string) string {
	o := cleanOverrideSegment(org)
	p, ok := NormalizeOverrideProvider(provider)
	if o == "" || !ok {
		return ""
	}
	return "orgs/" + o + "/ai-keys/" + p
}

// UserProviderKeyRef returns the canonical KMS secret ref for a user's provider API
// key override inside org, or "" when org, user, or provider is invalid.
func UserProviderKeyRef(org, user, provider string) string {
	o := cleanOverrideSegment(org)
	u := cleanOverrideUser(o, user)
	p, ok := NormalizeOverrideProvider(provider)
	if o == "" || u == "" || !ok {
		return ""
	}
	return "orgs/" + o + "/users/" + u + "/ai-keys/" + p
}

func readOverrideRef(ref string) string {
	if ref == "" {
		return ""
	}
	store := secretStore()
	if store == nil {
		return ""
	}
	overrideCacheMu.RLock()
	if entry, ok := overrideCache[ref]; ok && time.Since(entry.fetchedAt) < tenantOverrideTTL {
		overrideCacheMu.RUnlock()
		return entry.value
	}
	overrideCacheMu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b, err := store.GetSecret(ctx, ref)
	if err != nil {
		if isNotFound(err) {
			overrideCacheMu.Lock()
			overrideCache[ref] = overrideCacheEntry{value: "", fetchedAt: time.Now()}
			overrideCacheMu.Unlock()
		}
		return ""
	}
	val := strings.TrimSpace(string(b))
	overrideCacheMu.Lock()
	overrideCache[ref] = overrideCacheEntry{value: val, fetchedAt: time.Now()}
	overrideCacheMu.Unlock()
	return val
}

// ResetProviderKeyOverrideCache clears the in-process tenant override cache (for tests
// and immediate local invalidation).
func ResetProviderKeyOverrideCache() {
	overrideCacheMu.Lock()
	overrideCache = map[string]overrideCacheEntry{}
	overrideCacheMu.Unlock()
}

// ResolveTenantProviderKey returns the highest-priority provider API key override for
// (org, userOrg, user, provider) sealed in KMS:
//
//  1. Per-user override in the active org: orgs/{org}/users/{user}/ai-keys/{provider}
//  2. Per-user override in the user's home org (when acting cross-org): orgs/{userOrg}/users/{user}/ai-keys/{provider}
//  3. Per-org override in the active org: orgs/{org}/ai-keys/{provider}
//
// Returns ("", "") when no override is configured, in which case callers fall through
// to the platform key pool.
func ResolveTenantProviderKey(org, userOrg, user, provider string) (key string, scope string) {
	if _, ok := NormalizeOverrideProvider(provider); !ok {
		return "", ""
	}
	if ref := UserProviderKeyRef(org, user, provider); ref != "" {
		if v := readOverrideRef(ref); v != "" {
			return v, "user"
		}
	}
	if userOrg != "" && userOrg != org {
		if ref := UserProviderKeyRef(userOrg, user, provider); ref != "" {
			if v := readOverrideRef(ref); v != "" {
				return v, "user"
			}
		}
	}
	if ref := OrgProviderKeyRef(org, provider); ref != "" {
		if v := readOverrideRef(ref); v != "" {
			return v, "org"
		}
	}
	return "", ""
}
