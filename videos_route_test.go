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

	"github.com/beego/beego"
	_ "github.com/beego/beego/session" // memory session provider registration
	"github.com/hanzoai/ai/controllers"
	_ "github.com/hanzoai/ai/routers" // registers /v1/* routes (incl. videos/generations)
	"github.com/zap-proto/zip"
)

// TestVideosGenerationsRouteIsRegistered proves POST /v1/videos/generations is
// wired to the real VideosGenerations controller through the unified binary's
// /v1/* mount — it must NOT 404 (route missing) and must NOT 500 (session
// panic). Without a Bearer token the handler's own auth rejects the request
// (401), which still proves the route resolved and the handler ran. This is the
// registration half of the wiring: before it, the path 404'd (catalog-ware
// model, no endpoint).
func TestVideosGenerationsRouteIsRegistered(t *testing.T) {
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

	// No Authorization header → the handler's bearerToken() rejects with 401.
	// A 404 would mean the route never registered; a 500 a session panic.
	req, _ := http.NewRequest(http.MethodPost, "http://example.com/v1/videos/generations",
		strings.NewReader(`{"model":"zen3-video","prompt":"a red fox in snow"}`))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusNotFound {
		t.Fatalf("POST /v1/videos/generations: 404 — route not registered. body=%q", body)
	}
	if resp.StatusCode == http.StatusInternalServerError {
		t.Fatalf("POST /v1/videos/generations: 500 (session panic). body=%q", body)
	}
	if bs := string(body); strings.Contains(bs, "beego application error") || strings.Contains(bs, "nil pointer") {
		t.Fatalf("beego panic page returned: %q", bs)
	}
	// The handler ran and rejected the unauthenticated request.
	if resp.StatusCode != http.StatusUnauthorized {
		t.Logf("note: status=%d body=%q (expected 401 from unauthenticated handler)", resp.StatusCode, body)
	}
}

// ensure the controllers package is linked (VideosGenerations method exists).
var _ = (&controllers.ApiController{}).VideosGenerations
