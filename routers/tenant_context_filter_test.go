// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package routers

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/ai/object"
	web "github.com/hanzoai/ai/web"
)

// TestTenantContextFilter_ThreadsAttribution proves the filter stashes the project
// sub-scope and a NON-reversible credential ref onto the Go request context, so the
// single telemetry funnel (recordTrace) can stamp the ledger + span from one place.
func TestTenantContextFilter_ThreadsAttribution(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("X-Project-Id", "research")
	req.Header.Set("X-Session-Id", "conv-42")
	req.Header.Set("Authorization", "Bearer hk-secret-key")

	ctx := web.NewContext()
	ctx.Reset(rec, req)

	TenantContextFilter(ctx)

	attr := object.GenAIAttributionFromContext(ctx.Request.Context())
	if attr.Project != "research" {
		t.Fatalf("project not threaded onto request context: %q", attr.Project)
	}
	if attr.Session != "conv-42" {
		t.Fatalf("session not threaded onto request context: %q", attr.Session)
	}
	want := sha256.Sum256([]byte("hk-secret-key"))
	if attr.APIKeyHash != hex.EncodeToString(want[:]) {
		t.Fatalf("api key hash = %q, want the SHA-256 ref", attr.APIKeyHash)
	}
	if strings.Contains(attr.APIKeyHash, "hk-secret-key") {
		t.Fatal("plaintext credential must NEVER appear in the ref")
	}
}

// TestTenantContextFilter_NoAttributionWhenBare: with no project header and no
// bearer, nothing is threaded (an honest zero, no wrapped context).
func TestTenantContextFilter_NoAttributionWhenBare(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/models", nil)
	ctx := web.NewContext()
	ctx.Reset(rec, req)

	TenantContextFilter(ctx)

	if got := object.GenAIAttributionFromContext(ctx.Request.Context()); got != (object.GenAIAttribution{}) {
		t.Fatalf("bare request must carry no attribution, got %+v", got)
	}
}

func TestHashBearer(t *testing.T) {
	if got := hashBearer(""); got != "" {
		t.Fatalf("empty → %q, want \"\"", got)
	}
	if got := hashBearer("   "); got != "" {
		t.Fatalf("blank → %q, want \"\"", got)
	}
	want := sha256.Sum256([]byte("hk-abc"))
	if got := hashBearer("Bearer hk-abc"); got != hex.EncodeToString(want[:]) {
		t.Fatalf("hashBearer(Bearer hk-abc) = %q", got)
	}
	if hashBearer("hk-abc") != hashBearer("Bearer hk-abc") {
		t.Fatal("raw and Bearer-prefixed tokens must hash identically")
	}
}
