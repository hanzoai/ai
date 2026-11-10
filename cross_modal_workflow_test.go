// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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

	"github.com/zap-proto/zip"
)

// TestCrossModalWorkflowEndpointsAreAllWired walks the whole surface a
// cross-modal workflow chains through — chat → image → video → audio — in one
// table so a single missing endpoint breaks one named test instead of silently
// leaving the chain broken between two callers. Every handler here starts with
// bearerToken(), so an unauthenticated probe must still reach the handler and
// answer 401; a 404 means the route vanished from the router (the failure each
// sibling *_route_test.go guards) and a 500 means a session panic. Chat is the
// one exception: its anonymous free tier can legitimately answer something
// other than 401, so there we only require that the route resolved at all.
func TestCrossModalWorkflowEndpointsAreAllWired(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true, ReadBufferSize: 32 << 10})
	routes(app)

	for _, c := range []struct {
		method, path, body string
		want               int // -1: any status except 404/500
	}{
		{http.MethodPost, "/v1/chat/completions", `{"model":"zen3-nano","messages":[{"role":"user","content":"hi"}]}`, -1},
		{http.MethodPost, "/v1/images/generations", `{"model":"zen3-image","prompt":"a cat"}`, http.StatusUnauthorized},
		{http.MethodPost, "/v1/videos/generations", `{"model":"zen3-video","prompt":"a red fox in snow"}`, http.StatusUnauthorized},
		{http.MethodGet, "/v1/videos/video_probe", "", http.StatusUnauthorized},
		{http.MethodGet, "/v1/videos/video_probe/content", "", http.StatusUnauthorized},
		{http.MethodPost, "/v1/audio/speech", `{"model":"zen-voice-mini","input":"hello"}`, http.StatusUnauthorized},
		{http.MethodPost, "/v1/audio/transcriptions", "--x--\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\nzen-scribe\r\n--x----", http.StatusUnauthorized},
	} {
		req, _ := http.NewRequest(c.method, "http://example.com"+c.path, strings.NewReader(c.body))
		req.Header.Set("Content-Type", "application/json")

		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("%s %s: Test: %v", c.method, c.path, err)
		}
		body, _ := io.ReadAll(resp.Body)

		if resp.StatusCode == http.StatusNotFound {
			t.Errorf("%s %s: 404 — route not registered, the cross-modal chain breaks here. body=%q", c.method, c.path, body)
			continue
		}
		if resp.StatusCode == http.StatusInternalServerError {
			t.Errorf("%s %s: 500 (session panic). body=%q", c.method, c.path, body)
			continue
		}
		if bs := string(body); strings.Contains(bs, "nil pointer") {
			t.Errorf("%s %s: a nil dereference reached the body: %q", c.method, c.path, bs)
		}
		if c.want >= 0 && resp.StatusCode != c.want {
			t.Errorf("%s %s = %d %q, want %d from the unauthenticated handler", c.method, c.path, resp.StatusCode, body, c.want)
		} else if c.want < 0 {
			t.Logf("%s %s answered %d (route resolved; chat may answer beyond 401 on its free tier)", c.method, c.path, resp.StatusCode)
		}
	}
}
