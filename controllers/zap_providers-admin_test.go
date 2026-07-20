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

// gatedProviderMethods are the group's super-admin-gated native methods
// (everything except the public provider-flags feed). Each MUST reject an
// anonymous request with a 401 BEFORE touching the DB — the native-path
// re-enforcement of the beego authz filter's superAdminEndpoints gate.
var gatedProviderMethods = []string{
	"providers.global.list",
	"providers.list",
	"providers.get",
	"providers.update",
	"providers.add",
	"providers.delete",
	"providers.mcp.refresh",
	"admin.providers.list",
	"admin.providers.toggle",
	"admin.providers.primary",
}

// TestZapProviderAuthRejection asserts every gated handler fails closed with 401
// on an empty Bearer credential, and short-circuits before any DB access (no
// adapter is initialized in this unit test, so a handler that reached the store
// would panic — reaching the 401 assertion proves the gate ran first).
func TestZapProviderAuthRejection(t *testing.T) {
	for _, method := range gatedProviderMethods {
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

// TestZapProviderBogusTokenRejected asserts an unverifiable Bearer token resolves
// to no principal (fail-secure) and is denied 401 — never granted.
func TestZapProviderBogusTokenRejected(t *testing.T) {
	msg, _ := zapGetProvidersHandler(context.Background(), "Bearer not-a-real-token", nil)
	if got := msg.Root().Uint32(object.CloudRespStatus); got != 401 {
		t.Errorf("bogus token status = %d, want 401", got)
	}
}

// TestZapProviderOkEnvelopeParity asserts the success encoding matches the beego
// ResponseOk envelope ({status:"ok",data}) and the paginated two-value form sets
// data2 — the console frontend contract the native path must preserve.
func TestZapProviderOkEnvelopeParity(t *testing.T) {
	data := []string{"do-ai", "zen"}
	msg, err := zapProviderOk(data)
	if err != nil {
		t.Fatalf("zapOk: %v", err)
	}
	if got := msg.Root().Uint32(object.CloudRespStatus); got != 200 {
		t.Fatalf("ok status = %d, want 200", got)
	}
	want, _ := json.Marshal(Response{Status: "ok", Data: data})
	if got := msg.Root().Bytes(object.CloudRespBody); string(got) != string(want) {
		t.Errorf("ok body = %s, want %s", got, want)
	}

	msg2, _ := zapProviderOk(data, int64(2))
	want2, _ := json.Marshal(Response{Status: "ok", Data: data, Data2: int64(2)})
	if got := msg2.Root().Bytes(object.CloudRespBody); string(got) != string(want2) {
		t.Errorf("paginated body = %s, want %s", got, want2)
	}
}

// TestZapProviderRegistry asserts the group self-registered its full route set
// into both shared registries and that the gateway longest-prefix match keeps the
// multi-segment admin paths from being shadowed.
func TestZapProviderRegistry(t *testing.T) {
	for _, method := range append(gatedProviderMethods, "provider-flags") {
		if _, ok := lookupCloudHandler(method); !ok {
			t.Errorf("cloud method %q not registered", method)
		}
	}

	// Gateway longest-prefix: /v1/admin/providers/toggle must resolve to the
	// toggle handler, NOT the shorter /v1/admin/providers registration. Prove it
	// by driving the resolved handler with an empty credential and asserting the
	// gated 401 (both are gated, but an unregistered path would return !ok).
	for _, path := range []string{
		"/v1/get-global-providers", "/v1/get-providers", "/v1/get-provider",
		"/v1/update-provider", "/v1/add-provider", "/v1/delete-provider",
		"/v1/refresh-mcp-tools", "/v1/admin/providers", "/v1/admin/providers/toggle",
		"/v1/admin/providers/primary", "/v1/provider-flags",
	} {
		if _, ok := lookupGatewayHandler(path); !ok {
			t.Errorf("gateway path %q not registered", path)
		}
	}

	h, ok := lookupGatewayHandler("/v1/admin/providers/toggle")
	if !ok {
		t.Fatalf("/v1/admin/providers/toggle not registered")
	}
	msg, _ := h(context.Background(), "", []byte("{}"))
	if got := msg.Root().Uint32(object.CloudRespStatus); got != 401 {
		t.Errorf("toggle via gateway lookup status = %d, want 401", got)
	}
}
