// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package object

import (
	"context"
	"testing"
)

func TestGenAIAttribution_RoundTrip(t *testing.T) {
	ctx := WithGenAIAttribution(context.Background(), GenAIAttribution{Project: "research", Session: "conv-42", APIKeyHash: "abc123"})
	got := GenAIAttributionFromContext(ctx)
	if got.Project != "research" || got.Session != "conv-42" || got.APIKeyHash != "abc123" {
		t.Fatalf("round-trip = %+v, want {research conv-42 abc123}", got)
	}
}

func TestGenAIAttribution_SessionOnly(t *testing.T) {
	// A session-only attribution still wraps the context (it is not empty).
	ctx := WithGenAIAttribution(context.Background(), GenAIAttribution{Session: "s1"})
	if got := GenAIAttributionFromContext(ctx); got.Session != "s1" {
		t.Fatalf("session-only round-trip = %+v", got)
	}
}

func TestGenAIAttribution_EmptyIsNoop(t *testing.T) {
	base := context.Background()
	// An empty attribution must NOT wrap the context (nothing to carry).
	if WithGenAIAttribution(base, GenAIAttribution{}) != base {
		t.Fatal("empty attribution must return the context unchanged")
	}
	// Reading from a bare context yields the zero value, never a panic.
	if got := GenAIAttributionFromContext(context.Background()); got != (GenAIAttribution{}) {
		t.Fatalf("bare context = %+v, want zero", got)
	}
	if got := GenAIAttributionFromContext(nil); got != (GenAIAttribution{}) { //nolint:staticcheck
		t.Fatalf("nil context = %+v, want zero", got)
	}
}
