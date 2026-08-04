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
	"errors"
	"strings"
	"testing"
)

// fakeStore is an in-memory SecretStore — the same shape cloud hands over.
type fakeStore struct {
	vals map[string]string
	err  error
	gets int
}

func (f *fakeStore) GetSecret(_ context.Context, ref string) ([]byte, error) {
	f.gets++
	if f.err != nil {
		return nil, f.err
	}
	v, ok := f.vals[ref]
	if !ok {
		return nil, nil
	}
	return []byte(v), nil
}

func (f *fakeStore) PutSecret(_ context.Context, ref string, value []byte) error {
	if f.err != nil {
		return f.err
	}
	if f.vals == nil {
		f.vals = map[string]string{}
	}
	f.vals[ref] = string(value)
	return nil
}

// bind installs a store and clears the read cache, which is process-global.
func bind(t *testing.T, s SecretStore) {
	t.Helper()
	SetSecretStore(s)
	kmsSecMu.Lock()
	kmsSecrets = map[string]*kmsSecretEntry{}
	kmsSecMu.Unlock()
	t.Cleanup(func() {
		SetSecretStore(nil)
		kmsSecMu.Lock()
		kmsSecrets = map[string]*kmsSecretEntry{}
		kmsSecMu.Unlock()
	})
}

// TestResolve_StoreBeatsEnv is the ordering that matters: the embedded KMS is
// authoritative. The env var is the LEGACY K8s-Secret path being retired, so a
// key present in both must resolve to the store's value — otherwise migrating a
// secret into KMS would silently change nothing.
func TestResolve_StoreBeatsEnv(t *testing.T) {
	bind(t, &fakeStore{vals: map[string]string{"OPENROUTER_API_KEY": "from-kms"}})
	t.Setenv("OPENROUTER_API_KEY", "from-env")

	p := &Provider{Name: "openrouter", ClientSecret: "kms://OPENROUTER_API_KEY"}
	if err := ResolveProviderSecret(p); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if p.ClientSecret != "from-kms" {
		t.Errorf("ClientSecret = %q, want the STORE value %q", p.ClientSecret, "from-kms")
	}
}

// TestResolve_FallsBackToEnv: a provider whose key has not migrated yet keeps
// serving. Removing this fallback before every key is in KMS would take the
// env-fed providers (do-ai, openai-direct, anthropic, fireworks) offline.
func TestResolve_FallsBackToEnv(t *testing.T) {
	bind(t, &fakeStore{vals: map[string]string{}})
	t.Setenv("DO_AI_API_KEY", "env-only")

	p := &Provider{Name: "do-ai", ClientSecret: "kms://DO_AI_API_KEY"}
	if err := ResolveProviderSecret(p); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if p.ClientSecret != "env-only" {
		t.Errorf("ClientSecret = %q, want %q", p.ClientSecret, "env-only")
	}
}

// TestResolve_StoreErrorFallsBackNotFatal: a store that is down must not take
// down providers whose keys are still env-fed.
func TestResolve_StoreErrorFallsBackNotFatal(t *testing.T) {
	bind(t, &fakeStore{err: errors.New("store down")})
	t.Setenv("DO_AI_API_KEY", "env-only")

	p := &Provider{Name: "do-ai", ClientSecret: "kms://DO_AI_API_KEY"}
	if err := ResolveProviderSecret(p); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if p.ClientSecret != "env-only" {
		t.Errorf("ClientSecret = %q, want %q", p.ClientSecret, "env-only")
	}
}

// TestResolve_UnresolvableRefErrorsAndNeverEmitsTheRef is the invariant that
// keeps a reference off the wire. Returning "kms://NAME" verbatim would
// authenticate upstream AS that string — a guaranteed 401 that also discloses
// the reference.
func TestResolve_UnresolvableRefErrorsAndNeverEmitsTheRef(t *testing.T) {
	bind(t, &fakeStore{vals: map[string]string{}})

	p := &Provider{Name: "openrouter", ClientSecret: "kms://NOPE_ABSENT"}
	err := ResolveProviderSecret(p)
	if err == nil {
		t.Fatal("want an error for an unresolvable reference")
	}
	if strings.Contains(err.Error(), "kms://") {
		t.Errorf("error echoes the ref scheme: %v", err)
	}
	if p.ClientSecret != "kms://NOPE_ABSENT" {
		t.Errorf("source mutated on failure: %q", p.ClientSecret)
	}
}

// TestResolve_NonRefUntouched: a key set directly on the admin row is a
// deliberate operator choice and is passed through unchanged.
func TestResolve_NonRefUntouched(t *testing.T) {
	bind(t, &fakeStore{vals: map[string]string{}})

	p := &Provider{Name: "openrouter", ClientSecret: "sk-or-v1-literal"}
	if err := ResolveProviderSecret(p); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if p.ClientSecret != "sk-or-v1-literal" {
		t.Errorf("ClientSecret = %q, want unchanged", p.ClientSecret)
	}
}

// TestStoreProviderSecret_FailsClosedWithoutStore: with nothing bound, sealing
// must ERROR rather than let the caller persist a raw key.
func TestStoreProviderSecret_FailsClosedWithoutStore(t *testing.T) {
	bind(t, nil)
	if _, err := StoreProviderSecret("byok_org_openai", "sk-secret"); err == nil {
		t.Fatal("want an error with no store bound (never store plaintext)")
	}
}

// TestStoreProviderSecret_RoundTrip: seal returns the ref, and resolving that
// ref returns the value — the BYOK loop, entirely in-process.
func TestStoreProviderSecret_RoundTrip(t *testing.T) {
	fs := &fakeStore{vals: map[string]string{}}
	bind(t, fs)

	ref, err := StoreProviderSecret("byok_acme_openai", "sk-acme")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if ref != "kms://byok_acme_openai" {
		t.Fatalf("ref = %q", ref)
	}

	p := &Provider{Name: "openai", ClientSecret: ref}
	if err := ResolveProviderSecret(p); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if p.ClientSecret != "sk-acme" {
		t.Errorf("round trip = %q, want %q", p.ClientSecret, "sk-acme")
	}
}

// TestKeyPresent_AgreesWithResolution: the admin view's keyPresent must not
// claim a key the completion path would fail to find, so both go through the
// one precedence rule.
func TestKeyPresent_AgreesWithResolution(t *testing.T) {
	bind(t, &fakeStore{vals: map[string]string{"HAVE_IT": "v"}})

	if !ProviderKeyPresent(&Provider{Name: "a", ClientSecret: "kms://HAVE_IT"}) {
		t.Error("keyPresent = false for a key the store holds")
	}
	absent := &Provider{Name: "b", ClientSecret: "kms://MISSING"}
	if ProviderKeyPresent(absent) {
		t.Error("keyPresent = true for a key nothing holds")
	}
	if absent.ClientSecret != "kms://MISSING" {
		t.Errorf("keyPresent mutated the row: %q", absent.ClientSecret)
	}
}

// TestKmsGet_CachesFailures is a hot-path guard, not a nicety. cloud ALWAYS
// hands over a non-nil client — when KMS is not mounted it is a fail-closed stub
// whose every read errors — so an uncached failure would re-probe and re-log on
// every provider resolution, i.e. every completion request. The failure is cached
// briefly instead, which bounds both.
func TestKmsGet_CachesFailures(t *testing.T) {
	fs := &fakeStore{err: errors.New("store down")}
	bind(t, fs)

	for i := 0; i < 5; i++ {
		if _, err := kmsGet("K"); err == nil {
			t.Fatal("want an error from a failing store")
		}
	}
	if fs.gets != 1 {
		t.Errorf("store probes = %d, want 1 (failure cached)", fs.gets)
	}
}

// TestResolve_FailureCacheStillFallsBack: caching the failure must not turn into
// caching an empty answer — the env fallback has to keep working while the store
// is down, or an outage takes the env-fed providers with it.
func TestResolve_FailureCacheStillFallsBack(t *testing.T) {
	bind(t, &fakeStore{err: errors.New("store down")})
	t.Setenv("DO_AI_API_KEY", "env-only")

	for i := 0; i < 3; i++ {
		p := &Provider{Name: "do-ai", ClientSecret: "kms://DO_AI_API_KEY"}
		if err := ResolveProviderSecret(p); err != nil {
			t.Fatalf("resolve %d: %v", i, err)
		}
		if p.ClientSecret != "env-only" {
			t.Fatalf("resolve %d = %q, want %q", i, p.ClientSecret, "env-only")
		}
	}
}

// TestKmsGet_CachesReads keeps the hot path off the store for the TTL.
func TestKmsGet_CachesReads(t *testing.T) {
	fs := &fakeStore{vals: map[string]string{"K": "v"}}
	bind(t, fs)

	for i := 0; i < 3; i++ {
		if v, err := kmsGet("K"); err != nil || v != "v" {
			t.Fatalf("kmsGet = %q, %v", v, err)
		}
	}
	if fs.gets != 1 {
		t.Errorf("store reads = %d, want 1 (cached)", fs.gets)
	}
}
