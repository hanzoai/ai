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
	"os"
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/luxfi/zap"

	"github.com/hanzoai/ai/object"
)

func vmiscStatus(m *zap.Message) uint32 { return m.Root().Uint32(object.CloudRespStatus) }
func vmiscBody(m *zap.Message) []byte   { return m.Root().Bytes(object.CloudRespBody) }

// vmiscWriteMethods are the group's write routes that are NEITHER on the controller
// filter's benign-read exempt list NOR super-admin — every one requires an ORG admin
// at the outer gate, so an anonymous (no-principal) caller must be denied 403 BEFORE
// any DB access. No ORM adapter is initialized in this unit test, so a handler that
// reached the store would panic; reaching the 403 assertion proves the gate ran
// first (parity with routers/authz_filter.go permissionFilter's denyForbidden tail).
var vmiscWriteMethods = []string{
	"form.update", "form.add", "form.delete",
	"article.update", "article.add", "article.delete",
	"scale.update", "scale.add", "scale.delete",
}

// TestZapMiscWriteGateRejection asserts every org-admin-gated write fails closed with
// 403 on an empty Bearer credential, before any DB access.
func TestZapMiscWriteGateRejection(t *testing.T) {
	for _, method := range vmiscWriteMethods {
		h, ok := lookupCloudHandler(method)
		if !ok {
			t.Fatalf("method %q not registered", method)
		}
		msg, err := h(context.Background(), "", []byte("{}"))
		if err != nil {
			t.Fatalf("%s: unexpected err: %v", method, err)
		}
		if got := vmiscStatus(msg); got != 403 {
			t.Errorf("%s: empty auth status = %d, want 403", method, got)
		}
	}
}

// TestZapMiscSuperAdminGateRejection asserts the RESTful org-settings surface
// (/v1/ai/org/settings + /list) fails closed with 401 for an anonymous caller on EVERY
// verb, before any DB access — the ZAP-native super-admin gate. It also proves the one
// noun dispatches through the canonical registry as an HTTP-shaped (method-aware) route.
func TestZapMiscSuperAdminGateRejection(t *testing.T) {
	for _, tc := range []struct{ method, path string }{
		{"GET", "/v1/ai/org/settings"},
		{"PUT", "/v1/ai/org/settings"},
		{"DELETE", "/v1/ai/org/settings"},
		{"GET", "/v1/ai/org/settings/list"},
	} {
		msg, err := zapOrgSettingsHandler(context.Background(), tc.method, tc.path, "owner=acme", "", []byte("{}"))
		if err != nil {
			t.Fatalf("%s %s: unexpected err: %v", tc.method, tc.path, err)
		}
		if got := vmiscStatus(msg); got != 401 {
			t.Errorf("%s %s: empty auth status = %d, want 401", tc.method, tc.path, got)
		}
	}
	if len(lookupGatewayRoutes("/v1/ai/org/settings")) == 0 {
		t.Error("/v1/ai/org/settings not registered as an HTTP-shaped gateway route")
	}
}

// TestZapMiscAuthz asserts the gate mirrors permissionFilter: super-admin endpoints
// require the admin org; a write requires an org admin; a preview-mode read is open.
func TestZapMiscAuthz(t *testing.T) {
	superAdmin := &iam.User{Owner: "admin", Name: "z", IsAdmin: true}
	orgAdmin := &iam.User{Owner: "acme", Name: "boss", IsAdmin: true}
	plain := &iam.User{Owner: "acme", Name: "alice"}

	// Super-admin endpoint (the RESTful /v1/ai/org/settings noun): nil → 401, org-admin
	// (not super) → 403, super → open.
	if deny := zapMiscAuthz("org/settings", nil); deny == nil || vmiscStatus(deny) != 401 {
		t.Errorf("org-settings nil principal not 401")
	}
	if deny := zapMiscAuthz("org/settings", orgAdmin); deny == nil || vmiscStatus(deny) != 403 {
		t.Errorf("org-settings org-admin (non-super) not 403")
	}
	if deny := zapMiscAuthz("org/settings", superAdmin); deny != nil {
		t.Errorf("org-settings super-admin gated: status=%d", vmiscStatus(deny))
	}

	// Write endpoint: non-admin → 403, org admin → open.
	if deny := zapMiscAuthz("add-article", plain); deny == nil || vmiscStatus(deny) != 403 {
		t.Errorf("add-article non-admin not 403")
	}
	if deny := zapMiscAuthz("add-article", orgAdmin); deny != nil {
		t.Errorf("add-article org-admin gated: status=%d", vmiscStatus(deny))
	}

	// Benign-read exempt route: open even for a nil principal (each handler self-checks).
	if deny := zapMiscAuthz("get-forms", nil); deny != nil {
		t.Errorf("get-forms exempt read gated: status=%d", vmiscStatus(deny))
	}

	// Preview-mode read (default preview ON): a get- route is open for a nil principal.
	if deny := zapMiscAuthz("get-article", nil); deny != nil {
		t.Errorf("get-article preview read gated: status=%d", vmiscStatus(deny))
	}
}

// TestZapMiscScopedOwner asserts GetScopedOwner parity: nil → 401, a member of the
// admin org may target a specific owner, everyone else is pinned to their own Owner.
func TestZapMiscScopedOwner(t *testing.T) {
	if _, deny := zapMiscScopedOwner(nil, "acme"); deny == nil || vmiscStatus(deny) != 401 {
		t.Errorf("nil principal not denied 401")
	}

	adminOrg := &iam.User{Owner: "admin", Name: "z"}
	if owner, deny := zapMiscScopedOwner(adminOrg, "acme"); deny != nil || owner != "acme" {
		t.Errorf("admin-org override got (%q, deny=%v), want (acme, nil)", owner, deny != nil)
	}
	if owner, deny := zapMiscScopedOwner(adminOrg, ""); deny != nil || owner != "admin" {
		t.Errorf("admin-org no-override got (%q, deny=%v), want (admin, nil)", owner, deny != nil)
	}

	plain := &iam.User{Owner: "acme", Name: "alice"}
	if owner, deny := zapMiscScopedOwner(plain, "other"); deny != nil || owner != "acme" {
		t.Errorf("non-admin pin got (%q, deny=%v), want (acme, nil)", owner, deny != nil)
	}
}

// TestZapMiscHealthHappyPath asserts the no-dependency happy path: health returns 200
// with the shared {status:"ok"} envelope, reachable via the registry.
func TestZapMiscHealthHappyPath(t *testing.T) {
	h, ok := lookupCloudHandler("system.health")
	if !ok {
		t.Fatalf("system.health not registered")
	}
	msg, err := h(context.Background(), "", nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got := vmiscStatus(msg); got != 200 {
		t.Fatalf("health status = %d, want 200", got)
	}
	want, _ := json.Marshal(Response{Status: "ok"})
	if got := vmiscBody(msg); string(got) != string(want) {
		t.Errorf("health body = %s, want %s", got, want)
	}
}

// TestZapMiscAgentsDashboardHappyPath asserts a read happy path end-to-end: with the
// default (preview-on) gate open, the handler returns 200 and the configured URL in
// the {status:"ok",data} envelope (parity with GetAgentsDashboardUrl, env-driven).
func TestZapMiscAgentsDashboardHappyPath(t *testing.T) {
	t.Setenv("AGENTS_DASHBOARD_URL", "http://agents.example/ui")
	if got := os.Getenv("AGENTS_DASHBOARD_URL"); got != "http://agents.example/ui" {
		t.Fatalf("env not set: %q", got)
	}
	h, ok := lookupCloudHandler("agents.dashboard-url")
	if !ok {
		t.Fatalf("agents.dashboard-url not registered")
	}
	msg, err := h(context.Background(), "", nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got := vmiscStatus(msg); got != 200 {
		t.Fatalf("dashboard status = %d, want 200", got)
	}
	want, _ := json.Marshal(Response{Status: "ok", Data: "http://agents.example/ui"})
	if got := vmiscBody(msg); string(got) != string(want) {
		t.Errorf("dashboard body = %s, want %s", got, want)
	}
}

// TestZapMiscOkEnvelope asserts the group's success builder emits the SAME
// {status:"ok",data,data2} Response the ResponseOk produces (the console
// contract), including the two-value paginated form.
func TestZapMiscOkEnvelope(t *testing.T) {
	data := []string{"a", "b"}
	msg, err := zapOk(data)
	if err != nil {
		t.Fatalf("zapMiscOk: %v", err)
	}
	want, _ := json.Marshal(Response{Status: "ok", Data: data})
	if got := vmiscBody(msg); string(got) != string(want) {
		t.Errorf("ok body = %s, want %s", got, want)
	}

	msg2, _ := zapOk(data, 42)
	want2, _ := json.Marshal(Response{Status: "ok", Data: data, Data2: 42})
	if got := vmiscBody(msg2); string(got) != string(want2) {
		t.Errorf("ok paged body = %s, want %s", got, want2)
	}
}
