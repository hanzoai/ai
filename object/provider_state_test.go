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
	"testing"
	"time"
)

// ── ModelProviderUsable: the single-point routing State gate ────────────────
//
// This is also the LOW-2 predicate: SetPrimaryAdminProvider rejects making a
// provider primary unless ModelProviderUsable(provider) is true (State=="Active"),
// so a disabled provider can never become primary. The "disabled model → false"
// and "paused model → false" cases below are that guard's regression coverage.

func TestModelProviderUsable(t *testing.T) {
	cases := []struct {
		name string
		p    *Provider
		want bool
	}{
		{"nil provider", nil, false},
		{"active model", &Provider{Category: "Model", State: "Active"}, true},
		{"disabled model", &Provider{Category: "Model", State: "Disabled"}, false},
		{"paused model (empty state)", &Provider{Category: "Model", State: ""}, false},
		{"arbitrary non-active state", &Provider{Category: "Model", State: "Paused"}, false},
		// Non-Model providers are not governed by this gate — always usable here.
		{"storage provider ignores state", &Provider{Category: "Storage", State: "Disabled"}, true},
		{"embedding provider ignores state", &Provider{Category: "Embedding", State: ""}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ModelProviderUsable(tc.p); got != tc.want {
				t.Errorf("ModelProviderUsable(%+v) = %v, want %v", tc.p, got, tc.want)
			}
		})
	}
}

// ── ProviderKeyPresent: boolean-only key signal, never leaks the value ──────

func TestProviderKeyPresent_PlaintextValue(t *testing.T) {
	// A non-kms (dev) plaintext secret is reported present when non-empty. This
	// path does not touch KMS.
	p := &Provider{Name: "dev", Category: "Model", ClientSecret: "sk-plaintext-dev-key"}
	if !ProviderKeyPresent(p) {
		t.Error("ProviderKeyPresent = false for a non-empty plaintext secret, want true")
	}
	// The helper must NOT mutate the caller's provider (no resolved/masked value
	// bleeding back into a response object).
	if p.ClientSecret != "sk-plaintext-dev-key" {
		t.Errorf("ProviderKeyPresent mutated ClientSecret to %q, want it unchanged", p.ClientSecret)
	}
}

func TestProviderKeyPresent_EmptyAbsent(t *testing.T) {
	if ProviderKeyPresent(&Provider{Name: "empty", Category: "Model", ClientSecret: ""}) {
		t.Error("ProviderKeyPresent = true for an empty secret, want false")
	}
	if ProviderKeyPresent(nil) {
		t.Error("ProviderKeyPresent(nil) = true, want false")
	}
}

func TestProviderKeyPresent_KmsRefWithoutKMSIsAbsent(t *testing.T) {
	// With KMS not configured (no KMS_CLIENT_ID / KMS_SERVICE_TOKEN in the unit
	// env) and no matching env var, a "kms://…" reference cannot be confirmed and
	// is fail-closed reported as NOT present — the operator sees the key is
	// unavailable rather than a false positive. It also must not leak the ref.
	t.Setenv("KMS_CLIENT_ID", "")
	t.Setenv("KMS_SERVICE_TOKEN", "")
	t.Setenv("HANZO_API_KEY", "")
	p := &Provider{Name: "do-ai", Category: "Model", ClientSecret: "kms://DO_AI_API_KEY_TEST_ABSENT"}
	if ProviderKeyPresent(p) {
		t.Error("ProviderKeyPresent = true for an unresolved kms:// ref (KMS disabled), want false")
	}
	if p.ClientSecret != "kms://DO_AI_API_KEY_TEST_ABSENT" {
		t.Errorf("ProviderKeyPresent mutated the kms:// ref to %q, want unchanged", p.ClientSecret)
	}
}

// ── InvalidateProviderNameCache: toggles take effect immediately ────────────

func TestInvalidateProviderNameCache_ByName(t *testing.T) {
	// Seed the hot-path caches directly (no DB) and confirm a named invalidation
	// drops the global entry AND every per-org BYOK entry for that name, while
	// leaving unrelated providers cached.
	providerByNameCacheMu.Lock()
	providerByNameCache["do-ai"] = &providerByNameEntry{provider: &Provider{Name: "do-ai"}, fetchedAt: time.Now()}
	providerByNameCache["fireworks"] = &providerByNameEntry{provider: &Provider{Name: "fireworks"}, fetchedAt: time.Now()}
	providerByNameCacheMu.Unlock()

	orgProviderCacheMu.Lock()
	orgProviderCache["maxpower/do-ai"] = &providerByNameEntry{provider: &Provider{Name: "do-ai"}, fetchedAt: time.Now()}
	orgProviderCache["acme/do-ai"] = &providerByNameEntry{provider: &Provider{Name: "do-ai"}, fetchedAt: time.Now()}
	orgProviderCache["maxpower/fireworks"] = &providerByNameEntry{provider: &Provider{Name: "fireworks"}, fetchedAt: time.Now()}
	orgProviderCacheMu.Unlock()

	InvalidateProviderNameCache("do-ai")

	providerByNameCacheMu.RLock()
	_, doai := providerByNameCache["do-ai"]
	_, fw := providerByNameCache["fireworks"]
	providerByNameCacheMu.RUnlock()
	if doai {
		t.Error("global do-ai entry should be evicted")
	}
	if !fw {
		t.Error("unrelated fireworks entry should remain cached")
	}

	orgProviderCacheMu.RLock()
	_, mpDoai := orgProviderCache["maxpower/do-ai"]
	_, acmeDoai := orgProviderCache["acme/do-ai"]
	_, mpFw := orgProviderCache["maxpower/fireworks"]
	orgProviderCacheMu.RUnlock()
	if mpDoai || acmeDoai {
		t.Error("all per-org do-ai overrides should be evicted")
	}
	if !mpFw {
		t.Error("unrelated per-org fireworks override should remain cached")
	}

	// Cleanup so we don't leak state into other tests.
	InvalidateProviderNameCache("")
}

func TestInvalidateProviderNameCache_FlushAll(t *testing.T) {
	providerByNameCacheMu.Lock()
	providerByNameCache["do-ai"] = &providerByNameEntry{provider: &Provider{Name: "do-ai"}, fetchedAt: time.Now()}
	providerByNameCacheMu.Unlock()
	orgProviderCacheMu.Lock()
	orgProviderCache["maxpower/do-ai"] = &providerByNameEntry{provider: &Provider{Name: "do-ai"}, fetchedAt: time.Now()}
	orgProviderCacheMu.Unlock()

	InvalidateProviderNameCache("") // flush everything

	providerByNameCacheMu.RLock()
	nGlobal := len(providerByNameCache)
	providerByNameCacheMu.RUnlock()
	orgProviderCacheMu.RLock()
	nOrg := len(orgProviderCache)
	orgProviderCacheMu.RUnlock()
	if nGlobal != 0 || nOrg != 0 {
		t.Errorf("flush-all left entries: global=%d org=%d, want 0/0", nGlobal, nOrg)
	}
}

func TestProviderKeyPresent_KmsRefResolvedFromEnv(t *testing.T) {
	// ResolveProviderSecret is env-var-first: a matching env var makes a "kms://…"
	// reference resolve to a real value → present. (This is the working prod hot
	// path — no live KMS dependency.) The env var name is the kms:// suffix.
	t.Setenv("DO_AI_API_KEY_TEST_PRESENT", "resolved-secret-value")
	p := &Provider{Name: "do-ai", Category: "Model", ClientSecret: "kms://DO_AI_API_KEY_TEST_PRESENT"}
	if !ProviderKeyPresent(p) {
		t.Error("ProviderKeyPresent = false for a kms:// ref resolvable via env, want true")
	}
	// Caller's record is untouched — the resolved value must never persist onto it.
	if p.ClientSecret != "kms://DO_AI_API_KEY_TEST_PRESENT" {
		t.Errorf("ProviderKeyPresent leaked the resolved value onto the record: %q", p.ClientSecret)
	}
}
