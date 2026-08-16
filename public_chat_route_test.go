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

package ai

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/ai/routers" // registers /v1/* routes (incl. /v1/chat/public)
	"github.com/zap-proto/zip"
)

// askPublicly serves one POST /v1/chat/public through the real mounted router and
// returns the status and the house error code, if the body carries one.
func askPublicly(t *testing.T, peer string) (int, string) {
	t.Helper()
	routers.InstallFilters()
	SetHandler(routers.App)
	defer SetHandler(nil)

	app := zip.New(zip.Config{DisableStartupMessage: true})
	routes(app)

	req, _ := http.NewRequest(http.MethodPost, "http://example.com/v1/chat/public",
		strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("CF-Connecting-IP", peer)

	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("serving /v1/chat/public: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var got struct {
		Error struct{ Code string } `json:"error"`
	}
	_ = json.Unmarshal(body, &got)
	return resp.StatusCode, got.Error.Code
}

// The route is WIRED through the unified binary's /v1/* mount, and a deployment that
// has not armed it serves nothing.
//
// A bare 404 would be ambiguous — an unregistered route answers 404 too — so the
// evidence is the CODE in the body, which only this lane's handler writes. Reaching
// it proves the request was routed to ChatCompletionsPublic and refused there.
func TestPublicChatRouteIsWiredAndClosedByDefault(t *testing.T) {
	wireTestSessions()
	t.Setenv("PUBLIC_CHAT_DAILY", "")

	status, code := askPublicly(t, "198.51.100.20")
	if code != "public_lane_closed" {
		t.Fatalf("POST /v1/chat/public answered %d/%q; want the lane's own public_lane_closed "+
			"(an empty code means the request never reached the handler — the route is not wired)", status, code)
	}
	if status != http.StatusNotFound {
		t.Fatalf("closed lane answered %d, want 404", status)
	}
}

// Armed, a request traverses the REAL router and filter chain into this handler.
//
// What this test can prove is ROUTING, and it is the only test that can: the unit
// tests call the handler directly, so none of them would notice the route being
// unregistered, shadowed by a neighbour's prefix, or refused by a filter ahead of it.
//
// What it deliberately does NOT prove is the ceiling. A test binary stands up no model
// catalog, so the pool cannot answer, and the pre-charge check refuses before the
// counter is ever consulted — which is the correct order and the whole point of it.
// Asserting a 402 here would mean weakening that order to suit a test. The ceiling is
// held by the unit tests, which can stand up a servable pool, and by the mutation that
// removes the bound.
//
// The typed code is the evidence either way: only this handler writes it, so reaching
// it proves the request was routed here rather than 404ing as unregistered.
func TestPublicChatRouteReachesTheHandlerWhenArmed(t *testing.T) {
	wireTestSessions()
	t.Setenv("PUBLIC_CHAT_DAILY", "1")

	status, code := askPublicly(t, "198.51.100.21")
	if code != "public_pool_unavailable" {
		t.Fatalf("armed lane answered %d/%q; want the handler's own public_pool_unavailable "+
			"(an empty code means the request never reached it — the route is not wired)", status, code)
	}
	if status != http.StatusServiceUnavailable {
		t.Fatalf("no-pool deployment answered %d, want 503 — and never a 500", status)
	}
}
