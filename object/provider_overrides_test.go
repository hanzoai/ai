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

func TestResolveTenantLinkedComputePrecedence(t *testing.T) {
	store := &fakeStore{vals: map[string]string{
		"orgs/acme/linked-compute/url":                "https://gpu.acme.internal/v1",
		"orgs/acme/linked-compute/key":                "lc-acme-org-key",
		"orgs/acme/linked-compute/model":              "zen5-10m",
		"orgs/acme/users/alice/linked-compute/url":    "https://alice-box.acme.internal/v1",
		"orgs/acme/users/alice/linked-compute/key":    "lc-alice-user-key",
		"orgs/acme/users/alice/linked-compute/model":  "enso-ultra-local",
		"orgs/hanzo/users/zach/linked-compute/default": `{"url":"https://zach.hanzo.internal/v1","key":"lc-zach-json","model":"zen5-coder"}`,
	}}
	bind(t, store)
	ResetProviderKeyOverrideCache()
	t.Cleanup(ResetProviderKeyOverrideCache)

	// 1. User linked-compute beats org linked-compute.
	lc, scope := ResolveTenantLinkedCompute("acme", "acme", "alice")
	if scope != "user" || lc.URL != "https://alice-box.acme.internal/v1" || lc.Key != "lc-alice-user-key" || lc.Model != "enso-ultra-local" {
		t.Fatalf("alice linked compute = (%+v, %q)", lc, scope)
	}

	// 2. User without personal linked compute inherits org linked compute.
	lc, scope = ResolveTenantLinkedCompute("acme", "acme", "bob")
	if scope != "org" || lc.URL != "https://gpu.acme.internal/v1" || lc.Key != "lc-acme-org-key" || lc.Model != "zen5-10m" {
		t.Fatalf("bob linked compute = (%+v, %q)", lc, scope)
	}

	// 3. Cross-org home user JSON default overrides active org.
	lc, scope = ResolveTenantLinkedCompute("acme", "hanzo", "zach")
	if scope != "user" || lc.URL != "https://zach.hanzo.internal/v1" || lc.Key != "lc-zach-json" || lc.Model != "zen5-coder" {
		t.Fatalf("zach cross-org linked compute = (%+v, %q)", lc, scope)
	}

	// 4. Unconfigured tenant returns empty.
	lc, scope = ResolveTenantLinkedCompute("other", "other", "bob")
	if !lc.Empty() || scope != "" {
		t.Fatalf("unconfigured linked compute = (%+v, %q), want empty", lc, scope)
	}

	// 5. Path traversal fails closed.
	if ref := OrgLinkedComputeRef("../etc", "url"); ref != "" {
		t.Fatalf("unsafe org ref = %q, want empty", ref)
	}
	if ref := UserLinkedComputeRef("acme", "..", "key"); ref != "" {
		t.Fatalf("unsafe user ref = %q, want empty", ref)
	}
}
