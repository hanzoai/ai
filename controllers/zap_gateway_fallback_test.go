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
	"context"
	"net/http"
	"strings"
	"testing"
)

// These pin the reason the fallback exists: the ad-hoc MsgType 200 registry
// matches on PATH ALONE, which cannot express a RESTful surface. Everything the
// registry does not claim must reach the real router — with its method and its
// path parameters intact — or a ZAP caller silently gets the wrong handler.

func TestGatewayFallbackCarriesMethodAndPathParams(t *testing.T) {
	var gotMethod, gotPath, gotQuery, gotAuth string
	SetGatewayFallback(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotQuery = r.Method, r.URL.Path, r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer SetGatewayFallback(nil)

	// A member URL: three things the registry could not have distinguished —
	// the DELETE verb, the two id segments, and the query.
	_, handled, err := serveGatewayViaRouter(context.Background(),
		"DELETE", "/v1/ai/stores/acme/my-store", "force=1", "Bearer t", nil)
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	if !handled {
		t.Fatal("a request must be handled when a fallback is installed")
	}
	if gotMethod != "DELETE" {
		t.Errorf("method = %q, want DELETE — the verb is lost", gotMethod)
	}
	if gotPath != "/v1/ai/stores/acme/my-store" {
		t.Errorf("path = %q, want the full member path — the id segments are lost", gotPath)
	}
	if gotQuery != "force=1" {
		t.Errorf("query = %q, want force=1", gotQuery)
	}
	if gotAuth != "Bearer t" {
		t.Errorf("auth = %q, want the forwarded credential", gotAuth)
	}
}

// A non-2xx from the router must reach the caller as that status, not be
// flattened into a generic failure — a 401 has to stay a 401.
func TestGatewayFallbackPreservesStatus(t *testing.T) {
	SetGatewayFallback(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"status":"error","msg":"nope"}`))
	}))
	defer SetGatewayFallback(nil)

	msg, handled, err := serveGatewayViaRouter(context.Background(), "GET", "/v1/ai/stores", "", "", nil)
	if err != nil || !handled || msg == nil {
		t.Fatalf("serve: handled=%v err=%v msg=%v", handled, err, msg)
	}
	if got := msg.Root().Uint32(0); got != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", got)
	}
}

// The /v1/ai namespace is where routers/resources.go generates the RESTful CRUD
// surface: a collection at /v1/ai/<path> and a member at /v1/ai/<path>/:owner/:name
// answering GET, PATCH, PUT and DELETE. A prefix registered anywhere in there
// cannot pick the right handler — /v1/ai/stores/acme/thing prefix-matches
// /v1/ai/stores and would reach the LIST handler with the id dropped — so the
// whole namespace belongs to the router.
//
// This is the executable form of that. It fails the moment someone registers a
// gateway prefix under /v1/ai, which is the one edit that would turn a correct
// answer into a plausible wrong one with nothing else going red.
func TestNoGatewayPrefixOverTheResourceNamespace(t *testing.T) {
	const ns = "/v1/ai"
	owns := func(p string) bool { return p == ns || strings.HasPrefix(p, ns+"/") }

	zapRegistryMu.RLock()
	defer zapRegistryMu.RUnlock()
	for p := range zapGatewayRegistry {
		if owns(p) {
			t.Errorf("gateway prefix %q sits over the generated resource surface; "+
				"a path-only match cannot tell its verbs or its :owner/:name apart. "+
				"Leave it to the router — see the file comment above", p)
		}
	}
	for _, r := range zapGatewayRoutes {
		if owns(r.prefix) {
			t.Errorf("gateway route %q sits over the generated resource surface; "+
				"the router already resolves method + path params there", r.prefix)
		}
	}
}

// With no fallback installed the caller keeps its own 404 — installing the
// fallback is what turns a miss into a served request, and nothing else changes.
func TestGatewayFallbackAbsentIsNotHandled(t *testing.T) {
	SetGatewayFallback(nil)
	_, handled, err := serveGatewayViaRouter(context.Background(), "GET", "/v1/ai/stores", "", "", nil)
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	if handled {
		t.Error("with no fallback installed the request must NOT be reported handled")
	}
}
