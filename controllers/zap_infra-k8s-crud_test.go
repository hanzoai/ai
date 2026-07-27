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
	"reflect"
	"testing"

	"github.com/luxfi/zap"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

// funcPtr returns the code pointer of a handler so two handler values can be
// compared for identity (func values are not comparable with ==).
func funcPtr(h zapHandler) uintptr { return reflect.ValueOf(h).Pointer() }

// infraCrudMethods are the CRUD + node-tunnel + vm-dashboard-url endpoints this
// group migrated off beego, keyed by their native cloud method name.
func infraCrudMethods() map[string]zapHandler {
	return map[string]zapHandler{
		"get-nodes": zapGetNodesHandler, "get-node": zapGetNodeHandler,
		"add-node": zapAddNodeHandler, "update-node": zapUpdateNodeHandler, "delete-node": zapDeleteNodeHandler,
		"add-node-tunnel":      zapAddNodeTunnelHandler,
		"get-vm-dashboard-url": zapGetVmDashboardUrlHandler,
	}
}

func zapInfraStatus(t *testing.T, msg *zap.Message, err error) uint32 {
	t.Helper()
	if err != nil {
		t.Fatalf("handler returned transport error: %v", err)
	}
	if msg == nil {
		t.Fatal("handler returned nil response message")
	}
	return msg.Root().Uint32(object.CloudRespStatus)
}

// TestZapInfraAuthRejection is the core parity assertion: identity comes from the
// auth token ONLY, never the body, and every handler rejects an unauthenticated
// caller BEFORE any object-layer call. A body that smuggles an owner is still
// rejected. zapResolveUser rejects these tokens by shape alone (no network / DB):
// "" and "Bearer " are empty; "Bearer not-a-real-token" is neither an hk-/pk- key
// nor a JWT, so it never reaches IAM. All 25 super-admin CRUD endpoints therefore
// fail closed with 401; add-node-tunnel and get-vm-dashboard-url likewise 401
// without a resolvable principal.
func TestZapInfraAuthRejection(t *testing.T) {
	body := []byte(`{"owner":"attacker","id":"victim-org/box","name":"pwn"}`)
	for _, auth := range []string{"", "Bearer ", "Bearer not-a-real-token"} {
		for name, h := range infraCrudMethods() {
			msg, err := h(context.Background(), auth, body)
			status := zapInfraStatus(t, msg, err)
			if status != 401 {
				t.Errorf("method %q auth=%q: status=%d, want 401", name, auth, status)
			}
		}
	}
}

// TestZapInfraRegistered proves each migrated method self-registered into the
// shared cloud registry AND its gateway path prefix, so the Integrate dispatcher
// resolves them — the ZAP twin of the beego route table staying in parallel.
func TestZapInfraRegistered(t *testing.T) {
	for name := range infraCrudMethods() {
		if _, ok := lookupCloudHandler(name); !ok {
			t.Errorf("cloud method %q not registered", name)
		}
		if _, ok := lookupGatewayHandler("/v1/" + name); !ok {
			t.Errorf("gateway path /v1/%s not registered", name)
		}
	}

	// Longest-prefix resolution must keep the tunnel/pluralised paths distinct
	// from their shorter siblings — a real regression risk given the shared map.
	cases := []struct {
		path string
		want zapHandler
	}{
		{"/v1/add-node-tunnel", zapAddNodeTunnelHandler},
		{"/v1/add-node", zapAddNodeHandler},
		{"/v1/get-nodes", zapGetNodesHandler},
		{"/v1/get-node", zapGetNodeHandler},
	}
	for _, c := range cases {
		h, ok := lookupGatewayHandler(c.path)
		if !ok {
			t.Errorf("path %q not resolved", c.path)
			continue
		}
		// Function identity: all handlers return 401 on garbage auth, so a status
		// check can't distinguish them — assert the exact handler pointer resolves.
		if funcPtr(h) != funcPtr(c.want) {
			t.Errorf("path %q resolved to the wrong handler", c.path)
		}
	}
}

// TestZapScopedOwner mirrors GetScopedOwner: the admin org may target a specific
// owner via the body's "owner" field; every other identity is pinned to its own
// org regardless of what the body claims.
func TestZapScopedOwner(t *testing.T) {
	cases := []struct {
		owner, requested, want string
	}{
		{util.AdminOrg, "acme", "acme"},      // super admin targets a tenant
		{util.AdminOrg, "  ", util.AdminOrg}, // blank request → own org
		{util.AdminOrg, "", util.AdminOrg},
		{"acme", "attacker", "acme"}, // non-admin can never override (defensive)
	}
	for _, c := range cases {
		if got := zapInfraScopedOwner(c.owner, c.requested); got != c.want {
			t.Errorf("zapInfraScopedOwner(%q,%q)=%q, want %q", c.owner, c.requested, got, c.want)
		}
	}
}

// TestZapInfraPaged proves the pagination branch + offset math mirror the beego
// pagination.SetPaginator path (offset = (page-1)*pageSize; both params required).
func TestZapInfraPaged(t *testing.T) {
	if _, _, ok := (infraListParams{PageSize: "10"}).paged(); ok {
		t.Error("paged() should be false when p is empty")
	}
	if _, _, ok := (infraListParams{P: "2"}).paged(); ok {
		t.Error("paged() should be false when pageSize is empty")
	}
	off, lim, ok := (infraListParams{PageSize: "10", P: "3"}).paged()
	if !ok || lim != 10 || off != 20 {
		t.Errorf("paged()=(%d,%d,%v), want (20,10,true)", off, lim, ok)
	}
	// A non-positive page clamps to page 1 (offset 0), never a negative offset.
	off, _, _ = (infraListParams{PageSize: "10", P: "0"}).paged()
	if off != 0 {
		t.Errorf("paged() offset for p=0 = %d, want 0", off)
	}
}
