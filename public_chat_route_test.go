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

// Armed, the ceiling is enforced on the way in: the second call from one visitor at a
// ceiling of one is refused by the lane, before any provider is resolved.
func TestPublicChatCeilingHoldsThroughTheRouter(t *testing.T) {
	wireTestSessions()
	t.Setenv("PUBLIC_CHAT_DAILY", "1")

	const visitor = "198.51.100.21"
	// The first call spends the day. Whatever it answers (this test stands up no
	// provider), the count was taken before the pipeline was entered.
	askPublicly(t, visitor)

	status, code := askPublicly(t, visitor)
	if status != http.StatusPaymentRequired || code != "public_allowance_spent" {
		t.Fatalf("second call answered %d/%q, want 402/public_allowance_spent", status, code)
	}

	// A different visitor is untouched by the first's exhaustion.
	if _, code := askPublicly(t, "198.51.100.22"); code == "public_allowance_spent" {
		t.Fatal("one visitor's exhaustion refused another; the buckets are shared")
	}
}
