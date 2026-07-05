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

//go:build metrics

package object

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// TestMetricsHandlerServesDefaultRegistry proves the fix under the shipping
// build (`-tags metrics`, as set in the Dockerfile): GET /v1/metrics exposes the
// SAME DefaultRegistry the app records into, so a recorded series shows up as
// non-empty, valid Prometheus output — instead of the throwaway empty registry
// metric.Handler() used to bind, which answered 200 with a zero-length body.
func TestMetricsHandlerServesDefaultRegistry(t *testing.T) {
	ApiThroughput.WithLabelValues("/v1/chat", "POST").Set(42)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/metrics", nil)

	MetricsHandler().ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if body == "" {
		t.Fatal("empty body: handler is not bound to the DefaultRegistry the app records into")
	}
	for _, want := range []string{"# HELP ", "# TYPE ", "cloud_api_throughput"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q\n----- body -----\n%s", want, body)
		}
	}
}
