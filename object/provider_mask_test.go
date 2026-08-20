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

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/util"
)

// upstream builds a provider row carrying every platform credential field, so a
// test that forgets one fails rather than passing by omission.
func upstream() *Provider {
	return &Provider{
		Owner:        util.AdminOrg,
		Name:         "openrouter",
		Type:         "OpenRouter",
		ClientSecret: "sk-client-secret",
		ProviderKey:  "sk-upstream-provider-key",
		UserKey:      "sk-upstream-user-key",
		ConfigText:   "sk-embedded-in-config",
		SignKey:      "sk-sign-key",
	}
}

// A tenant-org administrator is a CUSTOMER. They administer their own org and
// nothing of ours, so the platform's upstream credentials — the keys that buy
// calls from OpenAI, Anthropic and OpenRouter directly, off our meter — must
// come back masked. `isAdmin` is satisfied by every customer who runs their own
// org, which is why it cannot be the predicate that unmasks money.
func TestTenantAdminNeverSeesPlatformUpstreamKeys(t *testing.T) {
	tenant := &iam.User{Owner: "maxpower", Name: "dave", IsAdmin: true}

	if util.IsSuperAdmin(tenant) {
		t.Fatal("a tenant-org admin satisfies IsSuperAdmin — the platform predicate is not narrower than the tenant one")
	}

	got := GetMaskedProvider(upstream(), tenant)

	for _, f := range []struct {
		name, value string
	}{
		{"ClientSecret", got.ClientSecret},
		{"ProviderKey", got.ProviderKey},
		{"UserKey", got.UserKey},
		{"ConfigText", got.ConfigText},
		{"SignKey", got.SignKey},
	} {
		if f.value != SecretMask {
			t.Errorf("%s = %q, want %q — a customer can spend this key directly", f.name, f.value, SecretMask)
		}
	}
}

// The same row read by a member of the reserved admin org is the operator path:
// configuring an upstream credential requires seeing it.
func TestSuperAdminStillReadsUpstreamKeys(t *testing.T) {
	super := &iam.User{Owner: util.AdminOrg, Name: "z", IsAdmin: true}

	if !util.IsSuperAdmin(super) {
		t.Fatal("a member of the admin org is not recognised as super admin")
	}

	got := GetMaskedProvider(upstream(), super)

	if got.ProviderKey != "sk-upstream-provider-key" {
		t.Errorf("ProviderKey = %q, want it readable by the operator", got.ProviderKey)
	}
	if got.UserKey != "sk-upstream-user-key" {
		t.Errorf("UserKey = %q, want it readable by the operator", got.UserKey)
	}
	// ClientSecret is masked for EVERY caller: it is never read back, only written,
	// and provider.go restores it from the row when a save carries the mask.
	if got.ClientSecret != SecretMask {
		t.Errorf("ClientSecret = %q, want %q for every caller", got.ClientSecret, SecretMask)
	}
}

// An anonymous caller — no session — is the weakest identity there is.
func TestAnonymousCallerSeesNoUpstreamKeys(t *testing.T) {
	got := GetMaskedProvider(upstream(), nil)

	for name, value := range map[string]string{
		"ClientSecret": got.ClientSecret,
		"ProviderKey":  got.ProviderKey,
		"UserKey":      got.UserKey,
		"ConfigText":   got.ConfigText,
		"SignKey":      got.SignKey,
	} {
		if value != SecretMask {
			t.Errorf("%s = %q, want %q", name, value, SecretMask)
		}
	}
}

// The list path masks every row. GetMaskedProviders once assigned the result of
// GetMaskedProvider to a loop variable, so this asserts the SLICE the caller
// serializes, not the value the loop happened to hold.
func TestListedProvidersAreMaskedForTenantAdmins(t *testing.T) {
	tenant := &iam.User{Owner: "maxpower", Name: "dave", IsAdmin: true}

	rows := []*Provider{upstream(), upstream()}
	for _, p := range GetMaskedProviders(rows, tenant) {
		if p.ProviderKey != SecretMask || p.UserKey != SecretMask {
			t.Errorf("listed provider leaked keys: providerKey=%q userKey=%q", p.ProviderKey, p.UserKey)
		}
	}
}
