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

// The growing transcript answers over HTTP, every verb on both paths. It was
// served only over ZAP, so api.hanzo.ai answered 404 to all three calls while the
// handler sat registered. Without a bearer the handler itself refuses with 401,
// which proves the request reached it.
func TestTheGrowingTranscriptIsServedOverHTTP(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true, ReadBufferSize: 32 << 10})
	routes(app)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/audio/transcript", `{"model":"zen-scribe"}`},
		{http.MethodPost, "/v1/audio/transcript/ats_x_y", "\x00\x00"},
		{http.MethodDelete, "/v1/audio/transcript/ats_x_y", ""},
	} {
		req, _ := http.NewRequest(c.method, "http://example.com"+c.path, strings.NewReader(c.body))
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("%s %s: %v", c.method, c.path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s = %d %q, want 401 from the transcript handler", c.method, c.path, resp.StatusCode, body)
		}
	}
}
