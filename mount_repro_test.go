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
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/ai/web"
	"github.com/zap-proto/zip"
)

// reproCtrl is a minimal controller exposing the same OpenAI-compatible
// endpoints routers/router.go registers (chat/completions, models). It lets
// the test exercise the REAL controller-dispatch path through the /v1/* mount.
type reproCtrl struct{ web.Controller }

// Chat echoes the raw request body, proving the router populated
// Ctx.Input.RequestBody (the real ChatCompletions controller json.Unmarshals
// that same field, so an empty body there yields "unexpected end of JSON
// input").
func (c *reproCtrl) Chat() {
	if len(c.Ctx.Input.RequestBody) == 0 {
		c.Ctx.Output.SetStatus(http.StatusBadRequest)
		c.Ctx.Output.Body([]byte(`{"error":"empty body"}`))
		return
	}
	c.Ctx.Output.Body(c.Ctx.Input.RequestBody)
}
func (c *reproCtrl) Models() { c.Ctx.Output.Body([]byte(`{"models":"ok"}`)) }

// reproHandler wires a session-bound router with the repro routes and publishes
// it as the request handler the /v1/* mount serves.
func reproHandler() {
	r := web.NewRouter()
	r.UseSessions(web.NewMemorySessions("cloud_session_id", time.Hour))
	r.Router("/v1/chat/completions", &reproCtrl{}, "POST:Chat")
	r.Router("/v1/models", &reproCtrl{}, "GET:Models")
	r.Router("/v1/repro-body-echo", &reproCtrl{}, "POST:Chat")
	SetHandler(r)
}

// TestMountForwardsNestedOpenAIRoutes proves the bare gateway paths
// /v1/chat/completions and /v1/models reach the routes registered at those
// exact paths through the unified binary's /v1/* mount, with session handling
// alive (no 500) and the nested route resolving (no 404).
func TestMountForwardsNestedOpenAIRoutes(t *testing.T) {
	reproHandler()
	defer SetHandler(nil)

	app := zip.New(zip.Config{DisableStartupMessage: true})
	routes(app)

	cases := []struct {
		name, method, path string
	}{
		{"models", http.MethodGet, "/v1/models"},
		{"chat-completions", http.MethodPost, "/v1/chat/completions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(tc.method, "http://example.com"+tc.path, nil)
			resp, err := app.Fiber().Test(req)
			if err != nil {
				t.Fatalf("Test: %v", err)
			}
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode == http.StatusInternalServerError {
				t.Fatalf("%s %s: status=500 (session/dispatch regressed) body=%q", tc.method, tc.path, body)
			}
			if resp.StatusCode == http.StatusNotFound {
				t.Fatalf("%s %s: status=404 (nested route did not resolve) body=%q", tc.method, tc.path, body)
			}
		})
	}
}

// TestChatCompletionsReceivesRequestBody guards that the router caches the
// request body: the OpenAI controllers json.Unmarshal Ctx.Input.RequestBody,
// so an empty echo (400) means the body-cache regressed.
func TestChatCompletionsReceivesRequestBody(t *testing.T) {
	reproHandler()
	defer SetHandler(nil)

	app := zip.New(zip.Config{DisableStartupMessage: true})
	routes(app)

	const payload = `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/repro-body-echo", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusBadRequest {
		t.Fatalf("body did not reach controller (body cache off): %q", body)
	}
	if string(body) != payload {
		t.Fatalf("controller saw body %q, want %q (body must forward unchanged)", body, payload)
	}
}
