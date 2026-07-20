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

package controllers

import (
	"context"
	"testing"

	"github.com/luxfi/zap"
)

// gwResp (status@0, body@4) is shared with zap_model-routing-config_test.go — one
// helper per package, not redefined here.

// TestZapRecordsSessionsConnectionsRegistered proves the group self-registers ALL
// its gateway path prefixes from init() — the strangler seam Integrate flips
// handleGatewayHTTPRequest onto. Every migrated beego route must resolve here.
func TestZapRecordsSessionsConnectionsRegistered(t *testing.T) {
	want := []string{
		// Sessions.
		"/v1/get-sessions", "/v1/get-session", "/v1/update-session",
		"/v1/add-session", "/v1/delete-session", "/v1/is-session-duplicated",
		// Connections.
		"/v1/get-connections", "/v1/get-connection", "/v1/update-connection",
		"/v1/add-connection", "/v1/delete-connection", "/v1/start-connection",
		"/v1/stop-connection",
		// Records.
		"/v1/get-records", "/v1/get-record", "/v1/update-record",
		"/v1/add-records", "/v1/add-record", "/v1/delete-record",
		"/v1/commit-record", "/v1/commit-record-second",
		"/v1/query-record", "/v1/query-record-second",
	}
	for _, p := range want {
		found := false
		for _, r := range zapGatewayRoutes {
			if r.prefix == p {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("gateway prefix %q not registered", p)
		}
	}
}

// TestZapRecordsSessionsConnectionsAuthRejected pins the ONE authz seam, mirroring
// the beego GetScopedOwner / RequireSignedInUser gates: an empty or unverifiable
// credential is refused with 401 BEFORE any object/DB work — fail-closed. Covers a
// scoped-list route, a by-id fetch, and a body write across all three sub-handlers.
func TestZapRecordsSessionsConnectionsAuthRejected(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		fn   func() (*zap.Message, error)
	}{
		{"sessions-list-empty-auth", func() (*zap.Message, error) {
			return zapSessionsHandler(ctx, "GET", "/v1/get-sessions", "", "", nil)
		}},
		{"sessions-list-bogus-bearer", func() (*zap.Message, error) {
			return zapSessionsHandler(ctx, "GET", "/v1/get-sessions", "", "Bearer nonsense", nil)
		}},
		{"session-get-empty-auth", func() (*zap.Message, error) {
			return zapSessionsHandler(ctx, "GET", "/v1/get-session", "sessionId=admin/x", "", nil)
		}},
		{"session-add-empty-auth", func() (*zap.Message, error) {
			return zapSessionsHandler(ctx, "POST", "/v1/add-session", "", "", []byte(`{"owner":"admin","name":"x"}`))
		}},
		{"connections-list-empty-auth", func() (*zap.Message, error) {
			return zapConnectionsHandler(ctx, "GET", "/v1/get-connections", "", "", nil)
		}},
		{"connection-stop-empty-auth", func() (*zap.Message, error) {
			return zapConnectionsHandler(ctx, "POST", "/v1/stop-connection", "id=admin/x", "", nil)
		}},
		{"records-list-empty-auth", func() (*zap.Message, error) {
			return zapRecordsHandler(ctx, "GET", "/v1/get-records", "", "", nil)
		}},
		{"record-add-empty-auth", func() (*zap.Message, error) {
			return zapRecordsHandler(ctx, "POST", "/v1/add-record", "", "", []byte(`{"owner":"admin"}`))
		}},
		{"commit-record-empty-auth", func() (*zap.Message, error) {
			return zapRecordsHandler(ctx, "POST", "/v1/commit-record", "", "", []byte(`{"owner":"admin"}`))
		}},
		{"query-record-empty-auth", func() (*zap.Message, error) {
			return zapRecordsHandler(ctx, "GET", "/v1/query-record", "id=admin/x", "", nil)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg, err := c.fn()
			if err != nil {
				t.Fatalf("handler err: %v", err)
			}
			status, body := gwResp(t, msg)
			if status != 401 {
				t.Fatalf("got status %d want 401", status)
			}
			if body.Status != "error" {
				t.Fatalf("got envelope status %q want \"error\"", body.Status)
			}
		})
	}
}

// TestZapRecordsSessionsConnectionsUnknownPath pins that each group dispatcher
// returns a 404 error envelope for a path it does not own (parity with the shared
// builders' not-found shape) — a registered prefix that reaches the wrong handler
// never silently 200s.
func TestZapRecordsSessionsConnectionsUnknownPath(t *testing.T) {
	ctx := context.Background()
	handlers := map[string]func() (*zap.Message, error){
		"sessions": func() (*zap.Message, error) {
			return zapSessionsHandler(ctx, "GET", "/v1/nope", "", "", nil)
		},
		"connections": func() (*zap.Message, error) {
			return zapConnectionsHandler(ctx, "GET", "/v1/nope", "", "", nil)
		},
		"records": func() (*zap.Message, error) {
			return zapRecordsHandler(ctx, "GET", "/v1/nope", "", "", nil)
		},
	}
	for name, fn := range handlers {
		t.Run(name, func(t *testing.T) {
			msg, err := fn()
			if err != nil {
				t.Fatalf("handler err: %v", err)
			}
			status, body := gwResp(t, msg)
			if status != 404 || body.Status != "error" {
				t.Fatalf("got status=%d envelope=%q want 404/error", status, body.Status)
			}
		})
	}
}

// TestZapRSCScopedOwnerNoPrincipal pins the tenant seam: no verified principal →
// 401 refusal, never a defaulted owner (fail-closed multi-tenant scoping).
func TestZapRSCScopedOwnerNoPrincipal(t *testing.T) {
	owner, refuse := zapRSCScopedOwner("", nil)
	if refuse == nil {
		t.Fatal("expected refusal for empty auth, got none")
	}
	if owner != "" {
		t.Fatalf("expected empty owner on refusal, got %q", owner)
	}
	status, body := gwResp(t, refuse)
	if status != 401 || body.Status != "error" {
		t.Fatalf("got status=%d envelope=%q want 401/error", status, body.Status)
	}
}
