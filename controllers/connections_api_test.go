// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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

package controllers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanzoai/ai/object"
	iam "github.com/hanzoai/ai/internal/iam"
)

const rawConnKey = "sk-raw-super-secret-do-not-persist-1234567890"

// TestConnectionSpecs pins the exact provider Type/Name mapping the login-manager
// stores rows as — the canonical Type strings the completion paths key on.
func TestConnectionSpecs(t *testing.T) {
	want := map[string]struct{ typ, name string }{
		"openai":    {"OpenAI", "openai"},
		"anthropic": {"Claude", "anthropic"},
		"google":    {"Gemini", "google"},
	}
	for slug, w := range want {
		spec, ok := aiConnSpecFor(slug)
		if !ok {
			t.Fatalf("aiConnSpecFor(%q) not found", slug)
		}
		if spec.typ != w.typ || spec.name != w.name {
			t.Errorf("spec %q = {type:%q name:%q}, want {type:%q name:%q}", slug, spec.typ, spec.name, w.typ, w.name)
		}
	}
	if _, ok := aiConnSpecFor("cohere"); ok {
		t.Error("aiConnSpecFor(cohere) = ok, want rejected (not in allow-list)")
	}
	if _, ok := aiConnSpecFor("OpenAI"); !ok {
		t.Error("aiConnSpecFor is not case-insensitive")
	}
}

// TestConnectionSecretNameDeterministic proves the KMS secret name is stable per
// (org, provider) so upsert + delete address the same secret, and reuses the ONE
// BYOK sealing convention (byokSecretName).
func TestConnectionSecretNameDeterministic(t *testing.T) {
	a := connectionSecretName("acme", "openai")
	b := connectionSecretName("acme", "openai")
	if a != b {
		t.Errorf("connectionSecretName not deterministic: %q vs %q", a, b)
	}
	if a != byokSecretName("acme", "openai") {
		t.Errorf("connectionSecretName %q != byokSecretName convention %q", a, byokSecretName("acme", "openai"))
	}
	if connectionSecretName("acme", "openai") == connectionSecretName("acme", "google") {
		t.Error("distinct providers must map to distinct secret names")
	}
	if strings.Contains(a, rawConnKey) {
		t.Error("secret NAME must not embed a key")
	}
}

// TestBuildConnectionProvider_SealInvariant is the SEAL INVARIANT: buildConnection
// Provider is the ONLY constructor of a connection row, and it copies the sealed
// "kms://…" reference into ClientSecret verbatim — the raw key is never even an
// argument, so it can never land in the persisted row.
func TestBuildConnectionProvider_SealInvariant(t *testing.T) {
	spec, _ := aiConnSpecFor("openai")
	ref := "kms://" + connectionSecretName("acme", "openai")

	row := buildConnectionProvider("acme", spec, ref)

	if row.ClientSecret != ref {
		t.Errorf("ClientSecret = %q, want the kms ref %q", row.ClientSecret, ref)
	}
	if !strings.HasPrefix(row.ClientSecret, "kms://") {
		t.Errorf("ClientSecret %q is not a kms:// reference", row.ClientSecret)
	}
	if strings.Contains(row.ClientSecret, rawConnKey) {
		t.Fatalf("SEAL INVARIANT VIOLATED: row persisted the raw key: %q", row.ClientSecret)
	}
	if row.Owner != "acme" || row.Name != "openai" || row.Type != "OpenAI" || row.Category != "Model" || row.State != "Active" {
		t.Errorf("row shape wrong: %+v", row)
	}
}

// TestStoreProviderSecret_FailsClosed proves the handler's fail-closed path: with
// KMS unconfigured (the unit env), sealing a raw key ERRORS rather than returning a
// ref — so AddAIConnection returns 503 and NEVER builds/writes a row holding the
// raw key. The returned ref is empty (no plaintext leaks back to the caller).
func TestStoreProviderSecret_FailsClosed(t *testing.T) {
	ref, err := object.StoreProviderSecret(connectionSecretName("acme", "openai"), rawConnKey)
	if err == nil {
		t.Skip("KMS is configured in this env — fail-closed path not exercisable here")
	}
	if ref != "" {
		t.Errorf("fail-closed seal returned a non-empty ref %q, want empty (no plaintext)", ref)
	}
	if strings.Contains(err.Error(), rawConnKey) {
		t.Error("error message leaked the raw key")
	}
}

// TestConnectionView_NoLeak is the NO-LEAK INVARIANT: a connected provider's public
// view reports connected:true yet its serialized JSON contains neither the raw key
// (resolved env-first) nor the "kms://…" reference. Env-var-first lets ProviderKey
// Present confirm the key without a live KMS (mirrors ResolveProviderSecret).
func TestConnectionView_NoLeak(t *testing.T) {
	spec, _ := aiConnSpecFor("openai")
	secretName := connectionSecretName("acme", "openai")
	ref := "kms://" + secretName
	// Env-first: ProviderKeyPresent resolves this without touching KMS.
	t.Setenv(secretName, rawConnKey)

	row := buildConnectionProvider("acme", spec, ref)
	view := connectionView("openai", row)

	if !view.Connected {
		t.Fatal("connected = false for a present, Active key, want true")
	}
	if view.AccountLabel != "acme/openai" {
		t.Errorf("account_label = %q, want acme/openai", view.AccountLabel)
	}

	blob, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	js := string(blob)
	if strings.Contains(js, rawConnKey) {
		t.Fatalf("NO-LEAK VIOLATED: response leaked the raw key: %s", js)
	}
	if strings.Contains(js, "kms://") || strings.Contains(js, secretName) {
		t.Fatalf("NO-LEAK VIOLATED: response leaked the kms ref: %s", js)
	}

	// A missing row → not connected, no label. A deactivated (disconnected) row →
	// not connected even though its sealed ref lingers.
	if v := connectionView("openai", nil); v.Connected {
		t.Error("nil row reported connected")
	}
	disabled := buildConnectionProvider("acme", spec, ref)
	disabled.State = "Disabled"
	if v := connectionView("openai", disabled); v.Connected {
		t.Error("disabled (disconnected) row reported connected")
	}
}

// TestProviderBYO_FeeOnce ties PART 2 (BYO classification) to PART 1 (fee): an
// org-owned provider is BYO and bills the 1% fee; the global Hanzo built-in is not
// BYO and bills no surcharge (the full token cost path). FEE-ONCE.
func TestProviderBYO_FeeOnce(t *testing.T) {
	user := &iam.User{Owner: "acme", Name: "z"}

	// Org's OWN connected account → BYO, fee applies.
	own := &object.Provider{Owner: "acme", Name: "openai"}
	byo, account := providerBYO(own, user)
	if !byo || account != "acme/openai" {
		t.Fatalf("providerBYO(org-owned) = (%v, %q), want (true, acme/openai)", byo, account)
	}
	if fee := platformFeeCents(10000, byo); fee != 100 {
		t.Errorf("BYO fee on 10000c = %d, want 100 (1%%)", fee)
	}

	// Global Hanzo built-in ("admin") → not BYO, no surcharge.
	global := &object.Provider{Owner: globalProviderOwner, Name: "do-ai"}
	byo, account = providerBYO(global, user)
	if byo || account != "hanzo" {
		t.Fatalf("providerBYO(global) = (%v, %q), want (false, hanzo)", byo, account)
	}
	if fee := platformFeeCents(10000, byo); fee != 0 {
		t.Errorf("Hanzo-served fee = %d, want 0", fee)
	}

	// Another org's row must NOT be treated as this caller's BYO account.
	foreign := &object.Provider{Owner: "other-org", Name: "openai"}
	if byo, _ := providerBYO(foreign, user); byo {
		t.Error("providerBYO cross-org = true, want false")
	}
	// Nil provider / nil user → Hanzo-served.
	if byo, acct := providerBYO(nil, user); byo || acct != "hanzo" {
		t.Error("providerBYO(nil provider) not Hanzo-served")
	}
}
