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

package object

import (
	"net/http/httptest"
	"strings"
	"testing"

	metric "github.com/luxfi/metric"
)

// TestMetricsHandlerContract locks the invariant that GET /v1/metrics answers
// 200 in the Prometheus text-exposition format and never 500s, in ANY build
// (metrics compiled in or no-op). Regression guard for the live incident where
// the endpoint returned HTTP 500 with a zero-length body.
func TestMetricsHandlerContract(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/metrics", nil)

	MetricsHandler().ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200 (body=%q)", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Fatalf("content-type = %q, want prometheus text exposition", ct)
	}
	// The handler must be bound to the same registry the app records into, and
	// that registry must gather cleanly — a gather error is exactly what the
	// upstream promhttp handler turned into the empty-body 500.
	if _, err := metric.DefaultRegistry.Gather(); err != nil {
		t.Fatalf("DefaultRegistry.Gather() error = %v, want nil", err)
	}
}
