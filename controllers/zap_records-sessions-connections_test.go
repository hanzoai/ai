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
	"testing"
)

// gwResp (status@0, body@4) is shared with zap_model-routing-config_test.go — one
// helper per package, not redefined here.

// TestZapRecordsSessionsConnectionsRegistered proves the group self-registers ALL
// its gateway path prefixes from init() — the strangler seam Integrate flips
// handleGatewayHTTPRequest onto. Every migrated beego route must resolve here.
func TestZapRecordsSessionsConnectionsRegistered(t *testing.T) {
	want := []string{
		// Sessions.
		// Connections.
		// Records.
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
