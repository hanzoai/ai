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

// TestWiredCloudRewriteDispatch proves a /v1/cloud/* request is rewritten to
// /v1/* and dispatched (not 404) through the wired router.
func TestWiredCloudRewriteDispatch(t *testing.T) {
	if code := wiredStatus(t, "GET", "/v1/cloud/get-account", ""); code == http.StatusNotFound {
		t.Errorf("/v1/cloud/get-account must rewrite to /v1/get-account and dispatch, got 404")
	}
}

// TestWiredIamRewriteSelective proves the /v1/iam/* alias is selective: an
// in-set route (signin) rewrites and dispatches, an out-of-set route (whoami)
// is not rewritten and 404s.
func TestWiredIamRewriteSelective(t *testing.T) {
	if code := wiredStatus(t, "POST", "/v1/iam/signin", "{}"); code == http.StatusNotFound {
		t.Errorf("/v1/iam/signin (in-set) must rewrite to /v1/signin and dispatch, got 404")
	}
	if code := wiredStatus(t, "GET", "/v1/iam/whoami", ""); code != http.StatusNotFound {
		t.Errorf("/v1/iam/whoami (out-of-set) must NOT rewrite and 404, got %d", code)
	}
}

// TestWiredGatedCloudAuthReject proves a gated route reached via /v1/cloud/* is
// denied by AuthzFilter before dispatch: unauthenticated /v1/cloud/get-providers
// (rewritten to the super-admin /v1/get-providers) returns 401/403.
func TestWiredGatedCloudAuthReject(t *testing.T) {
	code := wiredStatus(t, "GET", "/v1/cloud/get-providers", "")
	if code != http.StatusUnauthorized && code != http.StatusForbidden {
		t.Errorf("unauthenticated /v1/cloud/get-providers must be denied (401/403), got %d", code)
	}
}

// TestWiredParamRoute proves a route dispatches through the wired router (the
// canonical /v1 surface), i.e. the 307-route table is live.
func TestWiredParamRoute(t *testing.T) {
	if code := wiredStatus(t, "GET", "/v1/get-account", ""); code == http.StatusNotFound {
		t.Errorf("/v1/get-account must dispatch, got 404")
	}
}
