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

package routers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newTestTierCache builds a TierCache pointed at a test server.
func newTestTierCache(endpoint string) *TierCache {
	return &TierCache{
		entries:  make(map[string]*tierCacheEntry),
		endpoint: endpoint,
		token:    "test-token",
		client:   &http.Client{Timeout: 2 * time.Second},
		inflight: make(map[string]struct{}),
	}
}

// TestCommerceTierLookupQueriesByUser is the regression guard for the tier
// lookup bug: commerce GET /v1/billing/tier requires ?user=<org> and returns
// 400 "user query parameter is required" for any other param. The lookup must
// (1) hit the canonical path, (2) send the org slug as ?user=, and (3) parse
// the nested {"tier":{"name":...}} response — not a flat {"plan":...}.
func TestCommerceTierLookupQueriesByUser(t *testing.T) {
	var gotPath, gotUser, gotApiKey, gotOrgHdr string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotUser = r.URL.Query().Get("user")
		gotApiKey = r.URL.Query().Get("apiKey")
		gotOrgHdr = r.Header.Get("X-Org-Id")
		// Mirror commerce's contract: 400 unless ?user is present.
		if gotUser == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"type":"api-error","message":"user query parameter is required"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// commerce GetTier shape: plan name lives at tier.name.
		_, _ = w.Write([]byte(`{"user":"acme","tier":{"name":"pro","displayName":"Pro"},"balance":{"effectiveAvailable":5000}}`))
	}))
	defer srv.Close()

	tc := newTestTierCache(srv.URL)
	tier, err := tc.commerceTierLookup("acme")
	if err != nil {
		t.Fatalf("commerceTierLookup returned error: %v", err)
	}

	if gotPath != "/v1/billing/tier" {
		t.Errorf("path = %q, want /v1/billing/tier", gotPath)
	}
	if gotUser != "acme" {
		t.Errorf("?user = %q, want acme (the org slug)", gotUser)
	}
	if gotApiKey != "" {
		t.Errorf("?apiKey = %q, want empty (must not send apiKey — that triggers 400)", gotApiKey)
	}
	if gotOrgHdr != "acme" {
		t.Errorf("X-Org-Id = %q, want acme", gotOrgHdr)
	}
	if tier != TierZenPro {
		t.Errorf("tier = %q, want %q (parsed from tier.name=pro)", tier, TierZenPro)
	}
}

// TestCommerceTierLookupFreeTier verifies a free-tier org parses to zen-free.
func TestCommerceTierLookupFreeTier(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("user") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"tier":{"name":"free"}}`))
	}))
	defer srv.Close()

	tc := newTestTierCache(srv.URL)
	tier, err := tc.commerceTierLookup("hanzo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tier != TierZenFree {
		t.Errorf("tier = %q, want %q", tier, TierZenFree)
	}
}

// TestCommerceTierLookupFailOpen verifies a non-2xx response fails open to
// zen-free (rate limiting must never hard-fail because commerce is unhappy).
func TestCommerceTierLookupFailOpen(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	tc := newTestTierCache(srv.URL)
	tier, err := tc.commerceTierLookup("hanzo")
	if err == nil {
		t.Errorf("expected error on 500, got nil")
	}
	if tier != TierZenFree {
		t.Errorf("tier = %q, want %q (fail-open)", tier, TierZenFree)
	}
}
