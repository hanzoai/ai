// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.

package object

import "testing"

func TestResolveTenantProviderKeyPrecedence(t *testing.T) {
	store := &fakeStore{vals: map[string]string{
		"orgs/acme/ai-keys/openrouter":            "sk-or-org-acme",
		"orgs/acme/users/alice/ai-keys/openrouter": "sk-or-user-alice",
		"orgs/hanzo/users/zach/ai-keys/nvidia":    "nvapi-user-zach",
		"orgs/acme/ai-keys/nvidia":                "nvapi-org-acme",
	}}
	bind(t, store)
	ResetProviderKeyOverrideCache()
	t.Cleanup(ResetProviderKeyOverrideCache)

	// 1. User override wins over org override in same org.
	key, scope := ResolveTenantProviderKey("acme", "acme", "alice", "openrouter")
	if key != "sk-or-user-alice" || scope != "user" {
		t.Fatalf("alice openrouter = (%q, %q), want (sk-or-user-alice, user)", key, scope)
	}

	// 2. Qualified "acme/alice" spelling resolves to the same user ref.
	key, scope = ResolveTenantProviderKey("acme", "acme", "acme/alice", "OPENROUTER_API_KEY")
	if key != "sk-or-user-alice" || scope != "user" {
		t.Fatalf("qualified alice = (%q, %q), want (sk-or-user-alice, user)", key, scope)
	}

	// 3. User without personal override inherits org override.
	key, scope = ResolveTenantProviderKey("acme", "acme", "bob", "openrouter")
	if key != "sk-or-org-acme" || scope != "org" {
		t.Fatalf("bob openrouter = (%q, %q), want (sk-or-org-acme, org)", key, scope)
	}

	// 4. Cross-org actor: user override in home org (hanzo/zach) wins over active org override (acme).
	key, scope = ResolveTenantProviderKey("acme", "hanzo", "zach", "nvidia")
	if key != "nvapi-user-zach" || scope != "user" {
		t.Fatalf("zach cross-org nvidia = (%q, %q), want (nvapi-user-zach, user)", key, scope)
	}

	// 5. No override configured returns empty so platform pool is used.
	key, scope = ResolveTenantProviderKey("other", "other", "bob", "openrouter")
	if key != "" || scope != "" {
		t.Fatalf("unconfigured = (%q, %q), want empty", key, scope)
	}

	// 6. Path traversal / invalid segments fail closed (empty ref).
	if ref := OrgProviderKeyRef("../etc", "openrouter"); ref != "" {
		t.Fatalf("unsafe org ref = %q, want empty", ref)
	}
	if ref := UserProviderKeyRef("acme", "..", "nvidia"); ref != "" {
		t.Fatalf("unsafe user ref = %q, want empty", ref)
	}
}
