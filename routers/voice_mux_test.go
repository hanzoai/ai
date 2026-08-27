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
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"
)

// The /v1/voice routes are the one place this router hands a request to a
// FOREIGN ROUTER rather than to a handler. hanzoai/voice publishes exactly one
// way in — Routes(*http.ServeMux) — and keeps session, talk and health
// unexported, so what those three routes are registered on is a ServeMux that
// matches the path a second time, after zip has already matched it.
//
// Two routers matching one path is the thing worth pinning, because they do not
// agree about paths. A ServeMux cleans dot and empty segments and answers a
// redirect to the cleaned form before it matches; zip does not clean, so it can
// match a path the ServeMux will then refuse or bounce. This table records what
// each of those paths actually answers today, so the day voice offers a native
// entry point the conversion has something to be identical to.

// voiceMux is shaped exactly like (*voice.Voice).Routes — the same three method
// patterns, in the same order — with the bodies stubbed. What is under test is
// which router answers, not what the handlers say.
func voiceMux() http.Handler {
	mux := http.NewServeMux()
	say := func(who string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, who)
		}
	}
	mux.HandleFunc("POST /v1/voice/session", say("session"))
	mux.HandleFunc("GET /v1/voice", say("talk"))
	mux.HandleFunc("GET /v1/voice/health", say("health"))
	return mux
}

// voiceApp registers that mux the way routers.Register does: one adapted
// handler on three zip routes.
func voiceApp() *zip.App {
	app := zip.New(zip.Config{})
	talk := zip.AdaptNetHTTP(voiceMux())
	app.Post("/v1/voice/session", talk)
	app.Get("/v1/voice", talk)
	app.Get("/v1/voice/health", talk)
	return app
}

func TestVoiceMuxBoundary(t *testing.T) {
	app := voiceApp()

	for _, tc := range []struct {
		name   string
		method string
		target string
		status int
		body   string
		// location is the redirect a ServeMux answers with when it cleans a
		// path. Empty means none is expected.
		location string
	}{
		{
			name: "the three routes answer", method: "GET", target: "/v1/voice",
			status: http.StatusOK, body: "talk",
		},
		{
			name: "session", method: "POST", target: "/v1/voice/session",
			status: http.StatusOK, body: "session",
		},
		{
			name: "health", method: "GET", target: "/v1/voice/health",
			status: http.StatusOK, body: "health",
		},
		{
			// Neither router has a pattern for this, so nothing reaches the mux.
			name: "an unrouted path is zip's answer, not the mux's", method: "GET",
			target: "/v1/voice/nope", status: http.StatusNotFound,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.target, http.NoBody)
			resp, err := app.Fiber().Test(req)
			if err != nil {
				t.Fatalf("Test(%s %s): %v", tc.method, tc.target, err)
			}
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != tc.status {
				t.Errorf("status = %d, want %d (body %q)", resp.StatusCode, tc.status, string(b))
			}
			if tc.body != "" && string(b) != tc.body {
				t.Errorf("body = %q, want %q", string(b), tc.body)
			}
			if got := resp.Header.Get("Location"); got != tc.location {
				t.Errorf("Location = %q, want %q", got, tc.location)
			}
		})
	}
}

// notFound is the 404 this service answers with everywhere else — RFC 9457,
// which is what a client of api.hanzo.ai parses.
const notFound = `{"detail":"Not Found","status":404,"title":"Not Found","type":"about:blank"}`

// Where the two routers disagree, measured. zip is tolerant of a trailing
// slash, so GET /v1/voice/ MATCHES the /v1/voice route and is handed on — and
// the ServeMux, which has no pattern for it, answers out of net/http instead:
// "404 page not found", text/plain, nosniff. That is the only 404 on this
// surface that is not RFC 9457, and it is net/http's answer rather than this
// service's, reached through a route this service published.
//
// Dot, parent and empty segments never get that far: zip has no route for them
// and refuses first, so the ServeMux never sees a path to clean and its
// redirect-to-the-cleaned-form never happens here.
//
// Both halves go away together when hanzoai/voice publishes handlers instead
// of a router — see Register in router.go. Until then this is the shape, and
// it is pinned so that it changes on purpose.
func TestVoiceMuxDisagrees(t *testing.T) {
	app := voiceApp()

	for _, tc := range []struct {
		name        string
		target      string
		body        string
		contentType string
		nosniff     bool
	}{
		{
			name:        "a trailing slash is answered by net/http, not by this service",
			target:      "/v1/voice/",
			body:        "404 page not found\n",
			contentType: "text/plain; charset=utf-8",
			nosniff:     true,
		},
		{
			name: "a dot segment never reaches the mux", target: "/v1/voice/./health",
			body: notFound, contentType: "application/problem+json",
		},
		{
			name: "a parent segment never reaches the mux", target: "/v1/voice/sub/../health",
			body: notFound, contentType: "application/problem+json",
		},
		{
			name: "an empty segment never reaches the mux", target: "/v1/voice//health",
			body: notFound, contentType: "application/problem+json",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := app.Fiber().Test(httptest.NewRequest("GET", tc.target, http.NoBody))
			if err != nil {
				t.Fatalf("Test(GET %s): %v", tc.target, err)
			}
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("status = %d, want 404", resp.StatusCode)
			}
			if string(b) != tc.body {
				t.Errorf("body = %q, want %q", string(b), tc.body)
			}
			if got := resp.Header.Get("Content-Type"); got != tc.contentType {
				t.Errorf("Content-Type = %q, want %q", got, tc.contentType)
			}
			want := ""
			if tc.nosniff {
				want = "nosniff"
			}
			if got := resp.Header.Get("X-Content-Type-Options"); got != want {
				t.Errorf("X-Content-Type-Options = %q, want %q", got, want)
			}
			// The load-bearing one: a path the two routers do not agree on must
			// never reach a voice handler.
			for _, name := range []string{"session", "talk", "health"} {
				if string(b) == name {
					t.Errorf("%s reached the %s handler", tc.target, name)
				}
			}
		})
	}
}
