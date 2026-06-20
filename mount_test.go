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
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/zip"
)

// These tests exercise the route-adapter concern (prefix forwarding and the
// "runtime not initialized" 503) in isolation via mountRoutes, which has no
// infrastructure dependencies. The full Mount() additionally runs Bootstrap
// (DB, providers, billing); its runtime-init behavior needs a backend, so the
// adapter tests deliberately use mountRoutes.

func TestMountRoutesWithoutHandlerReturns503(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	mountRoutes(app)
	SetHandler(nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/ai/health", nil)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", resp.StatusCode)
	}
}

func TestMountRoutesForwardsToRegisteredHandlerStripsPrefix(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	mountRoutes(app)

	var sawPath string
	SetHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer SetHandler(nil)

	// /v1/ai/health → beego sees /v1/health
	req := httptest.NewRequest(http.MethodGet, "/v1/ai/health", nil)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != `{"ok":true}` {
		t.Fatalf("body=%q want {\"ok\":true}", body)
	}
	if sawPath != "/v1/health" {
		t.Fatalf("sawPath=%q want /v1/health (prefix should strip /v1/ai to /v1)", sawPath)
	}
}

func TestMountRoutesForwardsRootRequest(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	mountRoutes(app)

	var sawPath string
	SetHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer SetHandler(nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/ai/", nil)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	if sawPath != "/v1/" {
		t.Fatalf("sawPath=%q want /v1/", sawPath)
	}
}
