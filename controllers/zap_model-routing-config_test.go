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

	"github.com/luxfi/zap"
)

// gwResp reads status + body back out of a gateway response message (the shape
// object.BuildGatewayResponse writes: status@0:Uint32, body@4:Bytes).
func gwResp(t *testing.T, msg *zap.Message) (uint32, Response) {
	t.Helper()
	if msg == nil {
		t.Fatal("nil response message")
	}
	root := msg.Root()
	status := root.Uint32(0)
	var r Response
	if body := root.Bytes(4); len(body) > 0 {
		if err := json.Unmarshal(body, &r); err != nil {
			t.Fatalf("decode body: %v", err)
		}
	}
	return status, r
}

// TestZapModelRoutingRegistered proves the group self-registers its gateway path
// prefixes via init() — the strangler seam Integrate flips handleGatewayHTTPRequest
// onto. Registration is by longest-prefix, so every migrated path resolves here.
func TestZapModelRoutingRegistered(t *testing.T) {
	want := []string{
		"/v1/get-model-routes", "/v1/get-model-route", "/v1/add-model-route",
		"/v1/update-model-route", "/v1/delete-model-route",
		"/v1/models/", "/v1/admin/model-access",
		"/v1/admin/reload-model-config", "/v1/admin/refresh-model-pricing",
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

// TestZapModelRoutingAuthRejected pins the ONE authz seam, mirroring the beego
// RequireSuperAdmin / principalUser gates: an empty or unverifiable credential is
// refused BEFORE any object work — 401 (no principal) for the admin routes, and
// 401 for a self-scoped access route with no principal. A bogus bearer that
// resolves to no user is treated identically (fail-closed).
func TestZapModelRoutingAuthRejected(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		fn     func() (*zap.Message, error)
		status uint32
	}{
		{"routes-empty-auth", func() (*zap.Message, error) {
			return zapModelRouteHandler(ctx, "GET", "/v1/get-model-routes", "owner=admin", "", nil)
		}, 401},
		{"routes-bogus-bearer", func() (*zap.Message, error) {
			return zapModelRouteHandler(ctx, "GET", "/v1/get-model-routes", "", "Bearer nonsense", nil)
		}, 401},
		{"admin-access-empty-auth", func() (*zap.Message, error) {
			return zapAdminModelAccessHandler(ctx, "GET", "/v1/admin/model-access", "", "", nil)
		}, 401},
		{"reload-config-empty-auth", func() (*zap.Message, error) {
			return zapModelConfigAdminHandler(ctx, "POST", "/v1/admin/reload-model-config", "", "", nil)
		}, 401},
		{"self-access-empty-auth", func() (*zap.Message, error) {
			return zapModelAccessSelfHandler(ctx, "GET", "/v1/models/enso/access", "", "", nil)
		}, 401},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg, err := c.fn()
			if err != nil {
				t.Fatalf("handler err: %v", err)
			}
			status, body := gwResp(t, msg)
			if status != c.status {
				t.Fatalf("got status %d want %d", status, c.status)
			}
			if body.Status != "error" {
				t.Fatalf("got envelope status %q want \"error\"", body.Status)
			}
		})
	}
}

// TestZapModelFromAccessPath pins the :model path-param extraction that replaces
// beego's c.Ctx.Input.Param(":model"): only "/v1/models/{model}/access" yields a
// model; the bare list path and malformed shapes yield "" (so they fall through).
func TestZapModelFromAccessPath(t *testing.T) {
	cases := map[string]string{
		"/v1/models/enso/access":  "enso",
		"/v1/models/gpt-5/access": "gpt-5",
		"/v1/models":              "",
		"/v1/models/":             "",
		"/v1/models/enso":         "", // no /access suffix segment
		"/v1/models/a/b/access":   "", // nested — not a flat model id
		"/v1/admin/model-access":  "",
	}
	for in, want := range cases {
		if got := zapModelFromAccessPath(in); got != want {
			t.Errorf("zapModelFromAccessPath(%q) = %q want %q", in, got, want)
		}
	}
}

// TestZapPageOffset pins parity with beego pagination.SetPaginator().Offset():
// 1-based page, clamped to >= 1, offset == (page-1)*perPage.
func TestZapPageOffset(t *testing.T) {
	cases := []struct {
		page    string
		perPage int
		want    int
	}{
		{"1", 25, 0},
		{"2", 25, 25},
		{"3", 10, 20},
		{"", 25, 0},  // unset → page 1
		{"0", 25, 0}, // clamped to 1
		{"-4", 25, 0},
	}
	for _, c := range cases {
		if got := zapPageOffset(c.page, c.perPage); got != c.want {
			t.Errorf("zapPageOffset(%q,%d) = %d want %d", c.page, c.perPage, got, c.want)
		}
	}
}

// TestZapGatewayEnvelope pins STEP 7 encoding parity: zapGwOk emits the beego
// {status:"ok",data,data2} envelope at HTTP 200, and zapGwError emits
// {status:"error",msg} at the given status — byte-shape identical to what the
// beego ResponseOk/ResponseError path returns.
func TestZapGatewayEnvelope(t *testing.T) {
	msg, err := zapGwOk([]string{"a", "b"}, int64(7))
	if err != nil {
		t.Fatalf("zapGwOk: %v", err)
	}
	status, body := gwResp(t, msg)
	if status != 200 || body.Status != "ok" {
		t.Fatalf("zapGwOk: status=%d envelope=%q", status, body.Status)
	}
	if body.Data2 == nil {
		t.Fatalf("zapGwOk: data2 (pagination count) dropped")
	}

	emsg, _ := zapGwError(200, "owner and modelName are required")
	estatus, ebody := gwResp(t, emsg)
	if estatus != 200 || ebody.Status != "error" || ebody.Msg == "" {
		t.Fatalf("zapGwError: status=%d envelope=%q msg=%q", estatus, ebody.Status, ebody.Msg)
	}
}
