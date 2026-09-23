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
	"net/http"
	"strings"
	"testing"
)

// An origin this deployment cannot vouch for is refused, and the refusal says so
// in the caller's terms. When the IAM check itself fails, the reason is ours: it
// used to be echoed verbatim, so a cross-origin GET /v1/models read back the
// internal IAM address and its 401.
func TestAnUnlistedOriginIsRefusedWithoutOurInternals(t *testing.T) {
	t.Setenv("IAM_URL", "")
	t.Setenv("IAM_APP_NAME", "")

	q := ask(http.MethodGet, "/v1/models").with("Origin", "https://example.com").through(CorsFilter)
	if q.status() != http.StatusForbidden {
		t.Fatalf("unlisted origin = %d, want 403", q.status())
	}
	if !strings.Contains(q.wrote, "is not allowed") {
		t.Errorf("refusal %s does not say the origin is not allowed", q.wrote)
	}
	if strings.Contains(q.wrote, "IAM") {
		t.Errorf("refusal %s carries the internal reason; that belongs in the log", q.wrote)
	}
}

// The paired control: a first-party origin passes with its CORS headers, and a
// request with no Origin is not a CORS question at all.
func TestAListedOriginPasses(t *testing.T) {
	q := ask(http.MethodGet, "/v1/models").with("Origin", "https://console.hanzo.ai").through(CorsFilter)
	if q.status() != http.StatusOK {
		t.Fatalf("console origin = %d, want 200", q.status())
	}
	if got := q.head.Get("Access-Control-Allow-Origin"); got != "https://console.hanzo.ai" {
		t.Errorf("Access-Control-Allow-Origin = %q, want the origin echoed", got)
	}
	if q := ask(http.MethodGet, "/v1/models").through(CorsFilter); q.status() != http.StatusOK {
		t.Errorf("no Origin = %d, want 200", q.status())
	}
}
