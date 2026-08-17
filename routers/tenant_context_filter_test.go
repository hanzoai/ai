// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package routers

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/hanzoai/ai/object"
)

// TestTenantContextFilter_ThreadsAttribution proves the filter stashes the project
// sub-scope and a NON-reversible credential ref onto the Go request context, so the
// single telemetry funnel (recordTrace) can stamp the ledger + span from one place.
func TestTenantContextFilter_ThreadsAttribution(t *testing.T) {
	p := ask("POST", "/v1/chat/completions")
	p = p.with("X-Project-Id", "research")
	p = p.with("X-Session-Id", "conv-42")
	p = p.with("X-Environment", "staging")
	p = p.with("Authorization", "Bearer sk-secret-key")

	p = p.through(TenantContextFilter)

	attr := object.GenAIAttributionFromContext(p.left())
	if attr.Project != "research" {
		t.Fatalf("project not threaded onto request context: %q", attr.Project)
	}
	if attr.Session != "conv-42" {
		t.Fatalf("session not threaded onto request context: %q", attr.Session)
	}
	if attr.Environment != "staging" {
		t.Fatalf("environment not threaded onto request context: %q", attr.Environment)
	}
	want := sha256.Sum256([]byte("sk-secret-key"))
	if attr.APIKeyHash != hex.EncodeToString(want[:]) {
		t.Fatalf("api key hash = %q, want the SHA-256 ref", attr.APIKeyHash)
	}
	if strings.Contains(attr.APIKeyHash, "sk-secret-key") {
		t.Fatal("plaintext credential must NEVER appear in the ref")
	}
}

// TestTenantContextFilter_NoAttributionWhenBare: with no project header and no
// bearer, nothing is threaded (an honest zero, no wrapped context).
func TestTenantContextFilter_NoAttributionWhenBare(t *testing.T) {
	p := ask("GET", "/v1/models")

	p = p.through(TenantContextFilter)

	if got := object.GenAIAttributionFromContext(p.left()); got != (object.GenAIAttribution{}) {
		t.Fatalf("bare request must carry no attribution, got %+v", got)
	}
}

// TestTenantContextFilter_SessionAliasConversationID proves the session id is honored
// under the OpenAI/librechat-style X-Conversation-Id header when X-Session-Id is absent,
// so either client convention turns the o11y sessions view on.
func TestTenantContextFilter_SessionAliasConversationID(t *testing.T) {
	p := ask("POST", "/v1/chat/completions")
	p = p.with("X-Conversation-Id", "thread-7")
	p = p.with("Authorization", "Bearer sk-secret-key")

	p = p.through(TenantContextFilter)

	if attr := object.GenAIAttributionFromContext(p.left()); attr.Session != "thread-7" {
		t.Fatalf("session via X-Conversation-Id = %q, want \"thread-7\"", attr.Session)
	}
}

func TestHashBearer(t *testing.T) {
	if got := hashBearer(""); got != "" {
		t.Fatalf("empty → %q, want \"\"", got)
	}
	if got := hashBearer("   "); got != "" {
		t.Fatalf("blank → %q, want \"\"", got)
	}
	want := sha256.Sum256([]byte("sk-abc"))
	if got := hashBearer("Bearer sk-abc"); got != hex.EncodeToString(want[:]) {
		t.Fatalf("hashBearer(Bearer sk-abc) = %q", got)
	}
	if hashBearer("sk-abc") != hashBearer("Bearer sk-abc") {
		t.Fatal("raw and Bearer-prefixed tokens must hash identically")
	}
}
