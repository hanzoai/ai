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

// Secret resolution for provider credentials.
//
// ai runs INSIDE the unified cloud binary, which embeds luxfi/kms in-process
// (hanzoai/cloud apps/kms). So a secret read is a function call against the
// embedded SecretStore — never a network hop, never HTTP, never a hostname.
//
// This file used to hold a bespoke HTTP client that dialled the STANDALONE KMS
// deployment. It was wrong three ways at once and had never worked in
// production:
//
//   - Transport. It spoke HTTP to `kms.hanzo.ai` from a process that already
//     holds the store in memory — a round trip to itself, out through the
//     internet edge and back, to read a value that was one map lookup away.
//   - Path. KMS_ENDPOINT carried an `/api` prefix, so every call went to
//     `/api/v1/kms/...`. Measured from the running pod: **404**. On the correct
//     `/v1/...` path the universal-auth credentials came back **401**. Both
//     doors shut.
//   - Target. The standalone deployment it aimed at reports
//     `secrets plane disabled` at boot and has its ZAP port closed, while the
//     embedded KMS in this very binary answers `{"ready":true}`.
//
// Nothing noticed because resolution fell back to the environment, and every
// working provider key happened to be an env var from a K8s Secret. That
// fallback is the thing being retired: a `kms://` reference that silently means
// "read an env var" is not key management, and it is why a key can be stored in
// KMS and still be unavailable to the service that needs it.
//
// Order is now KMS-FIRST, env-fallback. A non-empty value from the store wins;
// anything else (absent, empty, error, or no store injected at all — the
// standalone `cmd/aid` case) falls through to the env var, so the providers that
// are still env-fed keep serving while their keys migrate into KMS. When the
// last key has moved, the fallback goes.
package object

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/log"
)

// SecretStore is the in-process KMS seam: the read/write subset of
// cloud.KMSClient that ai actually needs. Declared here, structurally, rather
// than imported — object must not depend on cloud (cloud mounts ai, so the
// import would be a cycle), and a two-method interface is a smaller contract to
// honour than the whole client.
//
// `ref` is cloud's flat secret reference, parsed by apps/kms parseRef:
//
//	"OPENROUTER_API_KEY"        → path "/",          name, env "default"
//	"myservice/DATABASE_URL"    → path "/myservice", name, env "default"
//	"myservice/DB@main"         → path "/myservice", name, env "main"
type SecretStore interface {
	GetSecret(ctx context.Context, ref string) ([]byte, error)
	PutSecret(ctx context.Context, ref string, value []byte) error
}

var (
	secretsMu sync.RWMutex
	secrets   SecretStore

	// Secret value cache: key = ref.
	kmsSecrets = make(map[string]*kmsSecretEntry)
	kmsSecMu   sync.RWMutex
	kmsSecTTL  = 5 * time.Minute
	// Failures are cached too, briefly. cloud ALWAYS hands over a non-nil client:
	// when KMS is not mounted it is a fail-closed stub whose every read errors. An
	// uncached failure would therefore re-probe and re-log on every provider
	// resolution — which is every completion request — so a deployment without KMS
	// would drown its own logs on the hot path. Short so a store coming back is
	// picked up in seconds, not minutes.
	kmsFailTTL = 30 * time.Second
)

// errStoreUnavailable is the cached-failure sentinel: the store errored recently
// and we are not re-probing until the failure entry expires.
var errStoreUnavailable = errors.New("kms: store unavailable (cached)")

// isNotFound reports whether a store error means "no such secret" rather than
// "the store could not answer". The two are opposite operator signals: the first
// is the normal state of a key that has not migrated yet, the second is a fault.
//
// Matched on the sentinel's TEXT rather than with errors.Is, because the sentinel
// lives in luxfi/kms/pkg/store and ai does not depend on luxfi/kms — the seam
// above is declared structurally precisely so this package needs no KMS library.
// Importing an entire key-management module to compare one error value would cost
// more than this costs. If the match ever goes stale the failure is benign and
// bounded: absence degrades into the cached-failure path, which still falls back
// to the env var and merely logs.
func isNotFound(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "not found")
}

type kmsSecretEntry struct {
	value     string
	fetchedAt time.Time
	failed    bool // the read errored; value is meaningless
}

// live reports whether this cache entry is still usable.
func (e *kmsSecretEntry) live() bool {
	ttl := kmsSecTTL
	if e.failed {
		ttl = kmsFailTTL
	}
	return time.Since(e.fetchedAt) < ttl
}

// SetSecretStore injects the embedded KMS. cloud calls this when it mounts ai,
// handing over the SAME in-process client every other subsystem uses. Called
// with nil (or never called — standalone cmd/aid) leaves ai on the env
// fallback rather than failing: a deployment with no KMS is a valid deployment,
// it just has no key management.
func SetSecretStore(s SecretStore) {
	secretsMu.Lock()
	defer secretsMu.Unlock()
	secrets = s
	if s != nil {
		log.Info("kms: secret store bound (embedded, in-process)")
	}
}

func secretStore() SecretStore {
	secretsMu.RLock()
	defer secretsMu.RUnlock()
	return secrets
}

// KMSConfigured reports whether an embedded store is available.
func KMSConfigured() bool { return secretStore() != nil }

// kmsGet reads one secret by ref, through a short TTL cache. An absent or empty
// secret is ("", nil) — "no value here", which is not an error and is what lets
// the caller fall through to the env var. A real failure (store error) is
// returned so a caller that must fail closed can, and is cached briefly so a
// KMS-less deployment does not re-probe on every request.
func kmsGet(ref string) (string, error) {
	store := secretStore()
	if store == nil {
		return "", nil
	}

	kmsSecMu.RLock()
	entry, ok := kmsSecrets[ref]
	kmsSecMu.RUnlock()
	if ok && entry.live() {
		if entry.failed {
			return "", errStoreUnavailable
		}
		return entry.value, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	b, err := store.GetSecret(ctx, ref)
	if err != nil {
		// ABSENT is not FAILED. The store reports a missing secret as an error, and
		// during the migration off env vars most secrets ARE missing — so treating
		// absence as failure would warn on every provider resolution, i.e. every
		// completion request, about the expected state of the world. Absence caches
		// as an ordinary empty value and says nothing; the caller falls through to
		// the env var, which is exactly right.
		if isNotFound(err) {
			kmsSecMu.Lock()
			kmsSecrets[ref] = &kmsSecretEntry{fetchedAt: time.Now()}
			kmsSecMu.Unlock()
			return "", nil
		}
		kmsSecMu.Lock()
		kmsSecrets[ref] = &kmsSecretEntry{fetchedAt: time.Now(), failed: true}
		kmsSecMu.Unlock()
		// Logged HERE, on the probe, not at the call site: the failure is cached, so
		// this fires at most once per kmsFailTTL per secret instead of once per
		// request. A store that cannot open a sealed key is an operator problem even
		// when resolution degrades gracefully to the env var.
		log.Warn("kms: read failed, falling back to env", "secret", ref, "error", err)
		return "", fmt.Errorf("kms: read %q: %w", ref, err)
	}
	value := string(b)

	kmsSecMu.Lock()
	kmsSecrets[ref] = &kmsSecretEntry{value: value, fetchedAt: time.Now()}
	kmsSecMu.Unlock()
	return value, nil
}

// kmsPut writes one secret by ref and invalidates the read cache.
func kmsPut(ref, value string) error {
	store := secretStore()
	if store == nil {
		return fmt.Errorf("kms: no secret store bound")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := store.PutSecret(ctx, ref, []byte(value)); err != nil {
		return fmt.Errorf("kms: write %q: %w", ref, err)
	}
	kmsSecMu.Lock()
	delete(kmsSecrets, ref)
	kmsSecMu.Unlock()
	return nil
}

// resolveSecretName returns the value behind a bare secret NAME: the embedded
// store first, then configuration. This is the one precedence rule; every
// caller goes through it so keyPresent can never disagree with what a
// completion would actually use.
//
// The second leg is conf.GetConfigString rather than os.Getenv, so that this
// reads everything a plain configuration read would — the environment first,
// then app.conf. Reading only the environment meant a caller had to choose
// between the store and its config file, which is why the credentials most
// worth governing were the ones still bypassing this.
func resolveSecretName(name string) string {
	// A store error is not fatal here — it must not take down a provider whose key
	// is still env-fed. kmsGet does the logging, on the probe rather than the call,
	// so this stays silent on the hot path.
	if v, err := kmsGet(name); err == nil && strings.TrimSpace(v) != "" {
		return v
	}
	return conf.GetConfigString(name)
}

// ResolveProviderSecret resolves "kms://NAME" references on a provider record
// in place. A non-reference value is left exactly as it is (an operator may set
// a key directly on the admin row; that is a deliberate, visible choice and not
// this function's business).
//
// An unresolvable reference is an ERROR, not a pass-through. Returning the
// literal "kms://NAME" would authenticate upstream as that string — a
// guaranteed 401 that also puts the reference on the wire.
func ResolveProviderSecret(provider *Provider) error {
	if provider == nil {
		return nil
	}
	resolveField := func(fieldName, currentValue string) (string, error) {
		if !strings.HasPrefix(currentValue, "kms://") {
			return currentValue, nil
		}
		secretName := strings.TrimPrefix(currentValue, "kms://")
		if secretName == "" {
			return "", fmt.Errorf("kms: empty secret reference for provider %q field %s", provider.Name, fieldName)
		}
		if v := resolveSecretName(secretName); v != "" {
			return v, nil
		}
		return "", fmt.Errorf("kms: no value for %q (provider %q field %s)", secretName, provider.Name, fieldName)
	}

	clientSecret, err := resolveField("clientSecret", provider.ClientSecret)
	if err != nil {
		return err
	}
	userKey, err := resolveField("userKey", provider.UserKey)
	if err != nil {
		return err
	}
	signKey, err := resolveField("signKey", provider.SignKey)
	if err != nil {
		return err
	}
	provider.ClientSecret = clientSecret
	provider.UserKey = userKey
	provider.SignKey = signKey
	return nil
}

// ProviderKeyPresent reports whether a provider's ClientSecret resolves to a
// non-empty value — WITHOUT ever returning the value itself. This is the ONLY
// key signal the admin management view is allowed to expose (keyPresent).
//
// It resolves through resolveSecretName, the same precedence a completion uses,
// so the admin view cannot claim a key the hot path would fail to find.
func ProviderKeyPresent(provider *Provider) bool {
	if provider == nil {
		return false
	}
	secret := provider.ClientSecret
	if strings.HasPrefix(secret, "kms://") {
		secretName := strings.TrimPrefix(secret, "kms://")
		if secretName == "" {
			return false
		}
		return strings.TrimSpace(resolveSecretName(secretName)) != ""
	}
	// A value set directly on the row (dev, or an operator's deliberate choice):
	// present iff non-empty.
	return strings.TrimSpace(secret) != ""
}

// GetKMSSecret fetches a secret by name for non-provider callers.
func GetKMSSecret(name string) (string, error) {
	if v := resolveSecretName(name); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("kms: no value for %q", name)
}

// GetOrgKMSSecret fetches a secret scoped to one org's path. The org is a path
// segment in the ref, which is the store's isolation partition — the same role
// the `org` column plays in every table.
func GetOrgKMSSecret(name, orgPath string) (string, error) {
	if orgPath == "" {
		return "", fmt.Errorf("kms: org path is empty")
	}
	ref := strings.TrimSuffix(orgPath, "/") + "/" + name
	v, err := kmsGet(ref)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(v) == "" {
		return "", fmt.Errorf("kms: no value for %q", ref)
	}
	return v, nil
}

// StoreProviderSecret seals a tenant-supplied (BYOK) provider key into the
// embedded KMS and returns the "kms://NAME" reference to persist on the provider
// record — so a raw key NEVER lands in the database as plaintext.
//
// FAILS CLOSED: with no store bound it errors rather than letting the caller
// fall back to storing the raw key. The name is caller-namespaced (e.g. by org)
// to avoid cross-tenant collision.
func StoreProviderSecret(name, value string) (string, error) {
	if !KMSConfigured() {
		return "", fmt.Errorf("kms: no secret store — refusing to store a provider key as plaintext")
	}
	if err := kmsPut(name, value); err != nil {
		return "", err
	}
	return "kms://" + name, nil
}
