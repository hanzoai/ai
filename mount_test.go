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
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// These tests exercise the route-adapter concern (bare /v1/* forwarding and the
// "runtime not initialized" 503) in isolation via mountRoutes, which has no
// infrastructure dependencies. The full Mount() additionally runs Bootstrap
// (DB, providers, billing); its runtime-init behavior needs a backend, so the
// adapter tests deliberately use mountRoutes.
//
// AI mounts the controller handler at BARE /v1/* (no prefix, no rewrite) because the
// production gateway forwards the bare /v1 routes unchanged (/v1/chat/completions,
// /v1/models, …). See mountRoutes for why this is collision-safe.

func TestMountRoutesWithoutHandlerReturns503(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	mountRoutes(app)
	SetHandler(nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", resp.StatusCode)
	}
}

func TestMountRoutesForwardsBarePathUnchanged(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	mountRoutes(app)

	var sawPath string
	SetHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer SetHandler(nil)

	// Bare gateway path must reach the router UNCHANGED — no /v1/ai rewrite.
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != `{"ok":true}` {
		t.Fatalf("body=%q want {\"ok\":true}", body)
	}
	if sawPath != "/v1/chat/completions" {
		t.Fatalf("sawPath=%q want /v1/chat/completions (no rewrite)", sawPath)
	}
}

// TestMountRoutesPropagatesTraceContext proves the mount adapter carries the request
// context ACROSS the zip→net/http boundary, so the OTel SERVER span cloud stashes via
// zip SetContext reaches the ai handler's r.Context(). Without this the gen_ai
// generation span (emitGenAISpan) starts parent-less and ORPHANS a fresh trace root —
// the exact defect (empty, observation-less traces in o11y Observe) this fix closes.
// It asserts the handler sees the SAME trace id as the upstream span, i.e. a child the
// generation span would nest under, mirroring how the in-process agent path already nests.
func TestMountRoutesPropagatesTraceContext(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	_, parent := tp.Tracer("test").Start(context.Background(), "POST /v1/chat/completions")
	parentSC := parent.SpanContext()
	parentCtx := trace.ContextWithSpan(context.Background(), parent)

	app := zip.New(zip.Config{DisableStartupMessage: true})
	// Simulate cloud's TracingMiddleware: stash the server-span context on the request
	// BEFORE the /v1/* mount runs.
	app.Use(zip.H(func(c *zip.Ctx) error {
		c.SetContext(parentCtx)
		return c.Continue()
	}))
	mountRoutes(app)

	var gotSC trace.SpanContext
	SetHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSC = trace.SpanContextFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	defer SetHandler(nil)

	resp, err := app.Fiber().Test(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	if !gotSC.IsValid() {
		t.Fatal("handler saw NO span context — the parent span did not propagate (generation span would orphan a new trace)")
	}
	if gotSC.TraceID() != parentSC.TraceID() {
		t.Fatalf("handler trace id = %s, want parent %s (context severed at the mount boundary)", gotSC.TraceID(), parentSC.TraceID())
	}
}

func TestMountRoutesForwardsNestedAndSingleSegment(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	mountRoutes(app)

	var sawPath string
	SetHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer SetHandler(nil)

	for _, path := range []string{"/v1/models", "/v1/chat/completions", "/v1/get-chats"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("Test %s: %v", path, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status=%d want 200", path, resp.StatusCode)
		}
		if sawPath != path {
			t.Fatalf("%s: sawPath=%q want %q (no rewrite)", path, sawPath, path)
		}
	}
}
