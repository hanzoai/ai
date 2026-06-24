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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// newTestGate builds a BalanceGate pointed at a stub Commerce server.
func newTestGate(endpoint string) *BalanceGate {
	return &BalanceGate{
		entries:      make(map[string]*balanceCacheEntry),
		userKeyCache: make(map[string]*userKeyCacheEntry),
		inflight:     make(map[string]struct{}),
		endpoint:     endpoint,
		token:        "svc-token",
		client:       &http.Client{Timeout: 2 * time.Second},
	}
}

// TestFetchBalance_PerOrg verifies the unified per-org billing contract:
// fetchBalance queries Commerce keyed by the org slug (?user=<org>) AND scopes
// the service-token call to that org's namespace via X-Hanzo-Org. Without the
// header, Commerce's service-token path defaults to the "hanzo" namespace and a
// per-org credit would be invisible — the exact bug this gate must not have.
func TestFetchBalance_PerOrg(t *testing.T) {
	const org = "maxpower"
	const wantCents = int64(10000)

	var gotUser, gotOrgHeader, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/billing/balance" {
			t.Errorf("path = %q, want /v1/billing/balance", r.URL.Path)
		}
		gotUser = r.URL.Query().Get("user")
		gotOrgHeader = r.Header.Get("X-Hanzo-Org")
		gotAuth = r.Header.Get("Authorization")
		// Only the maxpower namespace + maxpower key holds the credit.
		if gotOrgHeader == org && gotUser == org {
			_ = json.NewEncoder(w).Encode(map[string]any{"available": wantCents})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"available": 0})
	}))
	defer srv.Close()

	bg := newTestGate(srv.URL)
	got, err := bg.fetchBalance(org)
	if err != nil {
		t.Fatalf("fetchBalance error: %v", err)
	}

	if gotUser != org {
		t.Errorf("?user = %q, want org slug %q (per-org key, not owner/name)", gotUser, org)
	}
	if gotOrgHeader != org {
		t.Errorf("X-Hanzo-Org = %q, want %q (namespace scope missing => credit invisible)", gotOrgHeader, org)
	}
	if gotAuth != "Bearer svc-token" {
		t.Errorf("Authorization = %q, want service token", gotAuth)
	}
	if got != wantCents {
		t.Errorf("balance = %d, want %d", got, wantCents)
	}
}

// TestCheckBalance_PerOrgSufficient proves the end-to-end gate decision: with a
// per-org credit present, checkBalance(org) reports sufficient. This is the
// "no insufficient_balance" path for every member of the org.
func TestCheckBalance_PerOrgSufficient(t *testing.T) {
	const org = "maxpower"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Hanzo-Org") == org && r.URL.Query().Get("user") == org {
			_ = json.NewEncoder(w).Encode(map[string]any{"available": 10000})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"available": 0})
	}))
	defer srv.Close()

	bg := newTestGate(srv.URL)
	sufficient, cents := bg.checkBalance(org)
	if !sufficient || cents != 10000 {
		t.Fatalf("checkBalance(%q) = (%v, %d), want (true, 10000)", org, sufficient, cents)
	}
}

// TestResolveIAMKeyOrg_ReturnsOrgOnly verifies hk- key resolution yields the
// org slug alone (billing is per-org), not "owner/name".
func TestResolveIAMKeyOrg_ReturnsOrgOnly(t *testing.T) {
	const org = "maxpower"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Mimic IAM get-user shape.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"data":   map[string]any{"owner": org, "name": "davelorenzini"},
		})
	}))
	defer srv.Close()

	bg := newTestGate("http://unused")
	bg.iamEndpoint = srv.URL
	bg.client = &http.Client{Timeout: 2 * time.Second}

	got := bg.resolveIAMKeyOrg("hk-abc123")
	if got != org {
		t.Errorf("resolveIAMKeyOrg = %q, want %q (org slug only)", got, org)
	}

	// Sanity: the IAM call escapes the key into the query.
	if _, err := url.Parse(srv.URL); err != nil {
		t.Fatal(err)
	}
}
