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
	"encoding/json"
	"testing"

	"github.com/hanzoai/ai/object"
)

// securityGroupMethods are the group's native cloud methods. Every one fails
// closed with 401 on an anonymous request BEFORE touching the DB — the native
// path re-enforcement of the the router sign-in requirement (there is no filter chain
// on the ZAP wire). No adapter is initialized in this unit test, so a handler
// that reached the store would panic; reaching the 401 assertion proves the gate
// ran first.
var securityGroupMethods = []string{
	"assets.list", "asset.get", "asset.update", "asset.add", "asset.delete",
	"asset.scan", "assets.scan",
	"scans.list", "scan.get", "scan.update", "scan.add", "scan.delete",
	"permissions.list", "permission.get", "permission.update",
	"permission.add", "permission.delete",
}

// TestZapSecurityAuthRejection asserts every handler denies an empty Bearer
// credential with 401 and short-circuits before any DB access.
func TestZapSecurityAuthRejection(t *testing.T) {
	for _, method := range securityGroupMethods {
		h, ok := lookupCloudHandler(method)
		if !ok {
			t.Fatalf("method %q not registered", method)
		}
		msg, err := h(context.Background(), "", nil)
		if err != nil {
			t.Fatalf("%s: unexpected err: %v", method, err)
		}
		if got := msg.Root().Uint32(object.CloudRespStatus); got != 401 {
			t.Errorf("%s: empty auth status = %d, want 401", method, got)
		}
	}
}

// TestZapSecurityBogusTokenRejected asserts an unverifiable Bearer token resolves
// to no principal (fail-secure) and is denied 401 — never granted.
func TestZapSecurityBogusTokenRejected(t *testing.T) {
	msg, _ := zapGetAssetsHandler(context.Background(), "Bearer not-a-real-token", nil)
	if got := msg.Root().Uint32(object.CloudRespStatus); got != 401 {
		t.Errorf("bogus token status = %d, want 401", got)
	}
}

// TestZapSecurityOkEnvelopeParity asserts the success encoding matches the controller
// ResponseOk envelope ({status:"ok",data}) and the paginated two-value form sets
// data2 — the console frontend contract the native path must preserve.
func TestZapSecurityOkEnvelopeParity(t *testing.T) {
	data := []string{"asset-a", "asset-b"}
	msg, err := zapOk(data)
	if err != nil {
		t.Fatalf("zapSecOk: %v", err)
	}
	if got := msg.Root().Uint32(object.CloudRespStatus); got != 200 {
		t.Fatalf("ok status = %d, want 200", got)
	}
	want, _ := json.Marshal(Response{Status: "ok", Data: data})
	if got := msg.Root().Bytes(object.CloudRespBody); string(got) != string(want) {
		t.Errorf("ok body = %s, want %s", got, want)
	}

	msg2, _ := zapOk(data, int64(2))
	want2, _ := json.Marshal(Response{Status: "ok", Data: data, Data2: int64(2)})
	if got := msg2.Root().Bytes(object.CloudRespBody); string(got) != string(want2) {
		t.Errorf("paginated body = %s, want %s", got, want2)
	}
}

// TestZapSecurityRegistry asserts the group self-registered its full native
// method set into the shared registry.
func TestZapSecurityRegistry(t *testing.T) {
	for _, method := range securityGroupMethods {
		if _, ok := lookupCloudHandler(method); !ok {
			t.Errorf("cloud method %q not registered", method)
		}
	}

	// This group claims no gateway path: every route it serves is a resource on
	// routers.App, which the gateway fallback replays with its verb and its
	// :owner/:name intact. The plural/singular longest-prefix hazard a prefix
	// registration used to carry is gone with it.
}
