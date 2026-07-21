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

// vmiscWriteMethods are the group's write routes that are NEITHER on the beego
// filter's benign-read exempt list NOR super-admin — every one requires an ORG admin
// at the outer gate, so an anonymous (no-principal) caller must be denied 403 BEFORE
// any DB access. No ORM adapter is initialized in this unit test, so a handler that
// reached the store would panic; reaching the 403 assertion proves the gate ran
// first (parity with routers/authz_filter.go permissionFilter's denyForbidden tail).
var vmiscWriteMethods = []string{
	"hospital.update", "hospital.add", "hospital.delete",
	"doctor.update", "doctor.add", "doctor.delete",
	"patient.update", "patient.add", "patient.delete",
	"caase.update", "caase.add", "caase.delete",
	"consultation.update", "consultation.add", "consultation.delete",
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

// vmiscSuperAdminMethods are the per-org-settings routes: super-admin gated first, so
// an anonymous caller is denied 401 before any DB access (parity with the filter's
// superAdminEndpoints + the beego RequireSuperAdmin self-guard).
var vmiscSuperAdminMethods = []string{
	"org-settings.list", "org-settings.get",
	"org-settings.add", "org-settings.update", "org-settings.delete",
}

// TestZapMiscSuperAdminGateRejection asserts the org-settings surface fails closed
// with 401 for an anonymous caller.
func TestZapMiscSuperAdminGateRejection(t *testing.T) {
	for _, method := range vmiscSuperAdminMethods {
		h, ok := lookupCloudHandler(method)
		if !ok {
			t.Fatalf("method %q not registered", method)
		}
		msg, err := h(context.Background(), "", []byte("{}"))
		if err != nil {
			t.Fatalf("%s: unexpected err: %v", method, err)
		}
		if got := vmiscStatus(msg); got != 401 {
			t.Errorf("%s: empty auth status = %d, want 401", method, got)
		}
	}
}

// TestZapMiscAuthz asserts the gate mirrors permissionFilter: super-admin endpoints
// require the admin org; a write requires an org admin; a preview-mode read is open.
func TestZapMiscAuthz(t *testing.T) {
	superAdmin := &iam.User{Owner: "admin", Name: "z", IsAdmin: true}
	orgAdmin := &iam.User{Owner: "acme", Name: "boss", IsAdmin: true}
	plain := &iam.User{Owner: "acme", Name: "alice"}

	// Super-admin endpoint: nil → 401, org-admin (not super) → 403, super → open.
	if deny := zapMiscAuthz("get-org-settings-list", nil); deny == nil || vmiscStatus(deny) != 401 {
		t.Errorf("org-settings nil principal not 401")
	}
	if deny := zapMiscAuthz("get-org-settings-list", orgAdmin); deny == nil || vmiscStatus(deny) != 403 {
		t.Errorf("org-settings org-admin (non-super) not 403")
	}
	if deny := zapMiscAuthz("get-org-settings-list", superAdmin); deny != nil {
		t.Errorf("org-settings super-admin gated: status=%d", vmiscStatus(deny))
	}

	// Write endpoint: non-admin → 403, org admin → open.
	if deny := zapMiscAuthz("add-hospital", plain); deny == nil || vmiscStatus(deny) != 403 {
		t.Errorf("add-hospital non-admin not 403")
	}
	if deny := zapMiscAuthz("add-hospital", orgAdmin); deny != nil {
		t.Errorf("add-hospital org-admin gated: status=%d", vmiscStatus(deny))
	}

	// Benign-read exempt route: open even for a nil principal (each handler self-checks).
	if deny := zapMiscAuthz("get-forms", nil); deny != nil {
		t.Errorf("get-forms exempt read gated: status=%d", vmiscStatus(deny))
	}

	// Preview-mode read (default preview ON): a get- route is open for a nil principal.
	if deny := zapMiscAuthz("get-hospital", nil); deny != nil {
		t.Errorf("get-hospital preview read gated: status=%d", vmiscStatus(deny))
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
// {status:"ok",data,data2} Response the beego ResponseOk produces (the console
// contract), including the two-value paginated form.
func TestZapMiscOkEnvelope(t *testing.T) {
	data := []string{"a", "b"}
	msg, err := zapMiscOk(data)
	if err != nil {
		t.Fatalf("zapMiscOk: %v", err)
	}
	want, _ := json.Marshal(Response{Status: "ok", Data: data})
	if got := vmiscBody(msg); string(got) != string(want) {
		t.Errorf("ok body = %s, want %s", got, want)
	}

	msg2, _ := zapMiscOk(data, 42)
	want2, _ := json.Marshal(Response{Status: "ok", Data: data, Data2: 42})
	if got := vmiscBody(msg2); string(got) != string(want2) {
		t.Errorf("ok paged body = %s, want %s", got, want2)
	}
}
