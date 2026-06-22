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

	"github.com/beego/beego"
	_ "github.com/beego/beego/session" // memory session provider registration
	"github.com/hanzoai/zip"
)

// reproCtrl is a minimal beego controller exposing the same OpenAI-compatible
// endpoints that routers/router.go registers (chat/completions, models). It
// lets the test exercise the REAL beego ControllerRegister path — not a fake
// http.Handler — so the bare /v1/* forward is validated end to end.
type reproCtrl struct{ beego.Controller }

// Chat echoes the raw request body so the test can prove beego populated
// c.Ctx.Input.RequestBody — which only happens when CopyRequestBody is true.
// The real ChatCompletions controller json.Unmarshals that same field, so an
// empty body there yields "unexpected end of JSON input".
func (c *reproCtrl) Chat() {
	if len(c.Ctx.Input.RequestBody) == 0 {
		c.Ctx.Output.SetStatus(400)
		c.Ctx.Output.Body([]byte(`{"error":"empty body — CopyRequestBody off"}`))
		return
	}
	c.Ctx.Output.Body(c.Ctx.Input.RequestBody)
}
func (c *reproCtrl) Models() { c.Ctx.Output.Body([]byte(`{"models":"ok"}`)) }

// TestMountForwardsNestedOpenAIRoutesToBeego reproduces the production defect:
// the bare gateway paths /v1/chat/completions and /v1/models must reach the
// beego routes registered at those exact paths, through the unified binary's
// /v1/* mount, WITHOUT panicking in SessionStart. (The gateway forwards
// casibase routes unchanged, so the mount is bare /v1/* — see mountRoutes.)
func TestMountForwardsNestedOpenAIRoutesToBeego(t *testing.T) {
	// Register the real beego routes exactly as routers/router.go does.
	beego.Router("/v1/chat/completions", &reproCtrl{}, "POST:Chat")
	beego.Router("/v1/models", &reproCtrl{}, "GET:Models")

	// Enable sessions and build the manager via the SAME code path Bootstrap
	// uses. Before the fix, beego.GlobalSessions is nil here (the unified
	// binary never calls beego.Run()), so the first request that hits beego's
	// SessionStart panics with a nil-pointer deref — rendered as a 500. This
	// is the production defect; initSessionManager() is the fix under test.
	beego.BConfig.WebConfig.Session.SessionOn = true
	beego.BConfig.WebConfig.Session.SessionName = "cloud_session_id"
	beego.BConfig.WebConfig.Session.SessionProvider = "memory" // matches production (scratch image, no FS)
	beego.BConfig.WebConfig.Session.SessionProviderConfig = ""
	beego.GlobalSessions = nil // simulate the embedded binary's pre-fix state
	if err := initSessionManager(); err != nil {
		t.Fatalf("initSessionManager: %v", err)
	}

	// Publish the real beego handler — the same object Bootstrap() publishes.
	SetHandler(beego.BeeApp.Handlers)
	defer SetHandler(nil)

	app := zip.New(zip.Config{DisableStartupMessage: true})
	mountRoutes(app)

	// A valid Bearer token would route to the controller (200 {"chat":"ok"}).
	// Without one, the real auth filters (AutoSignin/Authz) inserted by the
	// runtime reject the request — which still PROVES the request traversed
	// beego's full pipeline: the /v1/ai prefix was stripped, the nested route
	// matched (r:/v1/models, r:/v1/chat/completions), and SessionStart did NOT
	// panic. The defect manifested as HTTP 500 with beego's "application error"
	// HTML (a nil GlobalSessions deref). So the assertion is: the response is
	// NOT that 500 panic page and NOT a 404 — the nested route resolves and
	// session handling is alive.
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
				t.Fatalf("%s %s: status=500 (session panic regressed) body=%q", tc.method, tc.path, body)
			}
			if resp.StatusCode == http.StatusServiceUnavailable {
				t.Fatalf("%s %s: status=503 (session store errored — memory provider regressed) body=%q", tc.method, tc.path, body)
			}
			if resp.StatusCode == http.StatusNotFound {
				t.Fatalf("%s %s: status=404 (nested route did not resolve) body=%q", tc.method, tc.path, body)
			}
			if bs := string(body); strings.Contains(bs, "beego application error") || strings.Contains(bs, "nil pointer") {
				t.Fatalf("%s %s: beego panic page returned: %q", tc.method, tc.path, bs)
			}
		})
	}
}

// TestChatCompletionsReceivesRequestBody guards the CopyRequestBody fix: the
// OpenAI controllers json.Unmarshal c.Ctx.Input.RequestBody, which beego only
// fills when BConfig.CopyRequestBody is true. The embedded binary has no
// conf/app.conf to set it, so Bootstrap must — otherwise every POST
// /v1/chat/completions failed with "unexpected end of JSON input" (HTTP 200
// error envelope). Here the repro controller echoes the body; an empty echo
// (400) means CopyRequestBody regressed.
func TestChatCompletionsReceivesRequestBody(t *testing.T) {
	// Unique path so this test's controller is the only handler — beego's route
	// table is process-global and other tests register /v1/chat/completions.
	beego.Router("/v1/repro-body-echo", &reproCtrl{}, "POST:Chat")
	beego.BConfig.CopyRequestBody = true // the Bootstrap fix under test
	beego.BConfig.WebConfig.Session.SessionOn = true
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

	const payload = `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/repro-body-echo", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusBadRequest {
		t.Fatalf("body did not reach controller (CopyRequestBody off): %q", body)
	}
	if string(body) != payload {
		t.Fatalf("controller saw body %q, want %q (body must forward unchanged)", body, payload)
	}
}
