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

package routers

import (
	"github.com/zap-proto/zip"

	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRouterConfigRoutesServeOverHTTP is the regression guard the router refactor
// was missing: it drives the REAL App router (the api.hanzo.ai :8000 transport),
// not the ZAP registry in isolation. The refactor moved the router-config surface
// to ZAP-native handlers and DELETED the controller routes, on the false premise that
// ZAP-native serves HTTP. It does not — :8000 is the custom web.Router, and a
// request only reaches a handler if a route is registered here. So every one of
// these nouns 404'd in production while the ZAP-registry unit tests stayed green.
//
// The assertion is transport-level and auth-independent: a registered route
// reaches RouterConfigBridge (→ the ONE native ZAP handler) and comes back with
// the handler's verdict (401/403/200/…), NEVER the router's 404. A retired
// compound path matches no route and MUST 404. "*" mapping means no 405 either.
func TestRouterConfigRoutesServeOverHTTP(t *testing.T) {
	// Routes only, no filters: registerAPI puts the table on the app without the
	// chain, so this exercises pure route dispatch and nothing a filter could
	// answer first.
	app := zip.New(zip.Config{DisableStartupMessage: true, ReadBufferSize: 32 << 10})
	registerResources(app)
	registerAPI(app)
	serve := func(method, path string) int {
		resp, err := app.Fiber().Test(httptest.NewRequest(method, path, nil))
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		return resp.StatusCode
	}

	// The RESTful nouns must resolve to the bridge — anything but 404 proves the
	// route is wired (the handler's own auth/verdict is asserted in the controllers
	// package; here we only prove the transport binding exists).
	live := []struct{ method, path string }{
		{http.MethodGet, "/v1/router/policy"},
		{http.MethodPut, "/v1/router/policy"}, // method-aware: PUT must also route
		{http.MethodGet, "/v1/router/defaults"},
		{http.MethodGet, "/v1/router/ledger"},
		{http.MethodGet, "/v1/router/rewards"},
		{http.MethodPost, "/v1/router/artifact-meta"},
		{http.MethodGet, "/v1/org/settings"},
		{http.MethodGet, "/v1/org/settings/list"}, // distinct segment count — own route
	}
	for _, r := range live {
		if code := serve(r.method, r.path); code == http.StatusNotFound {
			t.Errorf("%s %s: 404 — route not wired over HTTP (the reorg regression)", r.method, r.path)
		} else if code == http.StatusMethodNotAllowed {
			t.Errorf("%s %s: 405 — route registered but does not accept this verb", r.method, r.path)
		}
	}

	// The retired compound-verb paths must be gone from the HTTP transport too —
	// no controller twin, no backwards-compat alias.
	for _, p := range []string{
		"/v1/get-router-policy", "/v1/update-router-policy",
		"/v1/get-routing-defaults", "/v1/export-routing-ledger",
		"/v1/export-routing-rewards", "/v1/router/publish-artifact-meta",
		"/v1/add-routing-reward",
		"/v1/get-org-settings", "/v1/update-org-settings", "/v1/delete-org-settings",
		"/v1/add-org-settings", "/v1/get-org-settings-list",
	} {
		if code := serve(http.MethodGet, p); code != http.StatusNotFound {
			t.Errorf("retired compound path %s: status %d, want 404 (no controller twin, no alias)", p, code)
		}
	}
}
