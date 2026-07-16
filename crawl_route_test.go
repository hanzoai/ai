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

	"github.com/hanzoai/ai/controllers"
	_ "github.com/hanzoai/ai/routers" // registers /v1/* routes (incl. /v1/crawl)
	"github.com/hanzoai/beego"
	_ "github.com/hanzoai/beego/session" // memory session provider registration
	"github.com/zap-proto/zip"
)

// TestCrawlRouteIsRegisteredAndFailClosed proves POST /v1/crawl is wired to the
// real Crawl controller through the unified binary's /v1/* mount and is
// auth-gated fail-closed:
//   - NOT 404 → the route registered (the canonical /v1/crawl exists).
//   - NOT 500 → no session/nil panic.
//   - body {"status":"error"} with NO backend hit → requireIndexAuth rejected the
//     unauthenticated request BEFORE any crawl, i.e. fail-closed. Preview mode is
//     forced off so the reject branch (not the dev-preview admin bypass) runs.
func TestCrawlRouteIsRegisteredAndFailClosed(t *testing.T) {
	beego.BConfig.CopyRequestBody = true
	beego.BConfig.WebConfig.Session.SessionOn = true
	beego.BConfig.WebConfig.Session.SessionName = "cloud_session_id"
	beego.BConfig.WebConfig.Session.SessionProvider = "memory"
	beego.BConfig.WebConfig.Session.SessionProviderConfig = ""
	beego.GlobalSessions = nil
	if err := initSessionManager(); err != nil {
		t.Fatalf("initSessionManager: %v", err)
	}

	// Force fail-closed: with preview mode on (the default), requireIndexAuth
	// grants dev admin and would proceed to the crawl backend. Off, an
	// unauthenticated caller must be rejected outright.
	if beego.AppConfig != nil {
		prev := beego.AppConfig.String("disablePreviewMode")
		_ = beego.AppConfig.Set("disablePreviewMode", "true")
		defer beego.AppConfig.Set("disablePreviewMode", prev)
	}

	SetHandler(beego.BeeApp.Handlers)
	defer SetHandler(nil)

	app := zip.New(zip.Config{DisableStartupMessage: true})
	mountRoutes(app)

	req, _ := http.NewRequest(http.MethodPost, "http://example.com/v1/crawl",
		strings.NewReader(`{"url":"https://example.com"}`))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)

	if resp.StatusCode == http.StatusNotFound {
		t.Fatalf("POST /v1/crawl: 404 — route not registered. body=%q", bs)
	}
	if resp.StatusCode == http.StatusInternalServerError {
		t.Fatalf("POST /v1/crawl: 500 (panic). body=%q", bs)
	}
	if strings.Contains(bs, "beego application error") || strings.Contains(bs, "nil pointer") {
		t.Fatalf("beego panic page returned: %q", bs)
	}
	if strings.Contains(bs, `"success"`) {
		t.Fatalf("POST /v1/crawl succeeded without auth — NOT fail-closed. body=%q", bs)
	}
	// requireIndexAuth rejected the unauthenticated caller at the auth layer,
	// before any crawl backend call — the definitive fail-closed proof.
	if !strings.Contains(bs, "admin privilege") {
		t.Fatalf("POST /v1/crawl: expected requireIndexAuth rejection, got %q", bs)
	}
}

// TestSearchRouteStillRegistered guards that the native full-text /v1/search
// (Meilisearch, SearchDocs) remains wired after the crawl consolidation — NOT
// 404, and fail-closed (error body) for an unauthenticated caller.
func TestSearchRouteStillRegistered(t *testing.T) {
	beego.BConfig.CopyRequestBody = true
	beego.BConfig.WebConfig.Session.SessionOn = true
	beego.BConfig.WebConfig.Session.SessionName = "cloud_session_id"
	beego.BConfig.WebConfig.Session.SessionProvider = "memory"
	beego.BConfig.WebConfig.Session.SessionProviderConfig = ""
	beego.GlobalSessions = nil
	if err := initSessionManager(); err != nil {
		t.Fatalf("initSessionManager: %v", err)
	}

	SetHandler(beego.BeeApp.Handlers)
	defer SetHandler(nil)

	app := zip.New(zip.Config{DisableStartupMessage: true})
	mountRoutes(app)

	req, _ := http.NewRequest(http.MethodPost, "http://example.com/v1/search",
		strings.NewReader(`{"query":"hello"}`))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)

	if resp.StatusCode == http.StatusNotFound {
		t.Fatalf("POST /v1/search: 404 — native search route lost. body=%q", bs)
	}
	if resp.StatusCode == http.StatusInternalServerError {
		t.Fatalf("POST /v1/search: 500 (panic). body=%q", bs)
	}
	if !strings.Contains(bs, "authentication required") {
		t.Fatalf("POST /v1/search: expected fail-closed auth rejection, got %q", bs)
	}
}

// ensure the controllers package is linked (Crawl method exists).
var _ = (&controllers.ApiController{}).Crawl
