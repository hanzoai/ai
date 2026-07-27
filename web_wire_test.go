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

package ai

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/ai/routers"
	"github.com/zap-proto/zip"
)

// wiredStatus drives one request through the fully wired native router — the
// filter chain (rewrites, auth, tenant, …) plus reflection dispatch — over the
// same /v1/* mount the unified binary uses, and returns the status code.
func wiredStatus(t *testing.T, method, path, body string) int {
	t.Helper()
	wireTestSessions()
	routers.InstallFilters()
	SetHandler(routers.App)
	defer SetHandler(nil)

	app := zip.New(zip.Config{DisableStartupMessage: true})
	mountRoutes(app)

	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, "http://example.com"+path, r)
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	return resp.StatusCode
}

// TestWiredResourceRouteDispatches proves the generated resource surface is
// live through the fully-wired router: a namespaced REST path reaches its
// handler rather than 404-ing.
func TestWiredResourceRouteDispatches(t *testing.T) {
	for _, c := range []struct{ method, path string }{
		{"GET", "/v1/iam/account"},
		{"GET", "/v1/iam/applications"},
		{"GET", "/v1/rag/stores"},
		{"GET", "/v1/ops/version"},
		{"POST", "/v1/iam/signin"},
	} {
		if code := wiredStatus(t, c.method, c.path, "{}"); code == http.StatusNotFound {
			t.Errorf("%s %s must dispatch, got 404", c.method, c.path)
		}
	}
}

// TestOldAddressesAreGone is the "one way" guarantee, stated as a test. Every
// endpoint used to be reachable at up to THREE URLs — the flat compound route,
// the same route under /v1/cloud/*, and (for auth) under /v1/iam/*. Each extra
// address was a place a policy could be applied inconsistently. They must 404.
func TestOldAddressesAreGone(t *testing.T) {
	for _, c := range []struct{ method, path string }{
		{"GET", "/v1/get-account"},
		{"GET", "/v1/get-applications"},
		{"GET", "/v1/get-stores"},
		{"GET", "/v1/get-providers"},
		{"POST", "/v1/add-application"},
		{"POST", "/v1/signin"},
		{"GET", "/v1/cloud/get-account"},
		{"GET", "/v1/cloud/get-providers"},
		{"GET", "/v1/iam/get-account"},
	} {
		// "Gone" means NO HANDLER RUNS. Usually that shows up as a 404 from the
		// router. For a path whose name is still in the super-admin policy set
		// (the set is keyed by name, and policyKey still derives that name for
		// the live REST route), the authz filter denies an unauthenticated caller
		// BEFORE routing — 401/403, also without dispatching. Both outcomes are
		// the property under test; a 2xx or a 405 would mean a handler was found.
		code := wiredStatus(t, c.method, c.path, "{}")
		switch code {
		case http.StatusNotFound, http.StatusUnauthorized, http.StatusForbidden:
		default:
			t.Errorf("%s %s must be GONE, got %d — a second address for one resource",
				c.method, c.path, code)
		}
	}
}

// TestWiredGatedResourceAuthReject proves the super-admin gate still fires at the
// NEW address: unauthenticated provider reads are denied before dispatch.
func TestWiredGatedResourceAuthReject(t *testing.T) {
	code := wiredStatus(t, "GET", "/v1/ai/providers", "")
	if code != http.StatusUnauthorized && code != http.StatusForbidden {
		t.Errorf("unauthenticated /v1/ai/providers must be denied (401/403), got %d", code)
	}
}
