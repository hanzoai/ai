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
	"bytes"
	"mime/multipart"
	"net/url"
	"testing"

	iam "github.com/hanzoai/iam"
)

// TestZapWorkspaceRegistered proves the group binds every migrated route from its
// own init() into the shared gateway registry — the strangler wiring seam.
func TestZapWorkspaceRegistered(t *testing.T) {
	want := []string{
		"/v1/get-templates", "/v1/get-template", "/v1/update-template", "/v1/add-template", "/v1/delete-template",
		"/v1/get-global-workflows", "/v1/get-workflows", "/v1/get-workflow", "/v1/update-workflow", "/v1/add-workflow", "/v1/delete-workflow",
		"/v1/get-global-tasks", "/v1/get-tasks", "/v1/get-task", "/v1/update-task", "/v1/add-task", "/v1/delete-task", "/v1/upload-task-document", "/v1/analyze-task",
		"/v1/get-global-videos", "/v1/get-videos", "/v1/get-video", "/v1/update-video", "/v1/add-video", "/v1/delete-video", "/v1/upload-video",
	}
	registered := map[string]bool{}
	for _, r := range zapGatewayRoutes {
		registered[r.prefix] = true
	}
	for _, p := range want {
		if !registered[p] {
			t.Errorf("gateway path %q not registered", p)
		}
	}
}

// TestZapWorkspaceScopedOwner mirrors ApiController.GetScopedOwner: no principal →
// 401 refusal; a super-admin may target ?owner=; everyone else is pinned to own org.
func TestZapWorkspaceScopedOwner(t *testing.T) {
	// No principal → 401.
	if _, ok, refuse := zapScopedOwner(nil, url.Values{}); ok || refuse == nil {
		t.Fatal("nil user must be refused")
	} else if st, _ := decodeCloudRPS(t, refuse); st != 401 {
		t.Errorf("nil user: status = %d, want 401", st)
	}

	// Super-admin (owner=="admin") with ?owner=maxpower → targets maxpower.
	admin := &iam.User{Owner: "admin", Name: "z"}
	q := url.Values{}
	q.Set("owner", "maxpower")
	if owner, ok, _ := zapScopedOwner(admin, q); !ok || owner != "maxpower" {
		t.Errorf("admin scoped owner = %q,%v, want maxpower,true", owner, ok)
	}

	// Regular org user is pinned to their own org, ignoring ?owner=.
	user := &iam.User{Owner: "maxpower", Name: "dave"}
	if owner, ok, _ := zapScopedOwner(user, q); !ok || owner != "maxpower" {
		t.Errorf("tenant scoped owner = %q,%v, want maxpower,true", owner, ok)
	}
}

// TestZapWorkspaceGate mirrors permissionFilter for this group's routes: a write by
// a non-admin is refused 403; an org-admin passes; self-scoped exempt writes and the
// upload verbs skip the coarse admin gate entirely.
func TestZapWorkspaceGate(t *testing.T) {
	nonAdmin := &iam.User{Owner: "maxpower", Name: "dave"}
	orgAdmin := &iam.User{Owner: "maxpower", Name: "boss", IsAdmin: true}

	// A non-exempt write (add-template) by a non-admin → 403.
	if refuse := zapWorkspaceGate(nonAdmin, "add-template"); refuse == nil {
		t.Error("add-template non-admin must be refused")
	} else if st, _ := decodeCloudRPS(t, refuse); st != 403 {
		t.Errorf("add-template non-admin: status = %d, want 403", st)
	}

	// The same write by an org-admin passes the coarse gate.
	if refuse := zapWorkspaceGate(orgAdmin, "add-template"); refuse != nil {
		t.Error("add-template org-admin must pass the coarse gate")
	}

	// Exempt self-scoped write (update-task) passes for a non-admin — ownership is
	// enforced in the handler body, not the coarse gate.
	if refuse := zapWorkspaceGate(nonAdmin, "update-task"); refuse != nil {
		t.Error("update-task is exempt and must pass the coarse gate")
	}

	// upload-video is neither a get- nor a write- verb → coarse gate does not apply.
	if refuse := zapWorkspaceGate(nonAdmin, "upload-video"); refuse != nil {
		t.Error("upload-video must pass the coarse gate (self-authenticates in handler)")
	}
}

// TestZapWorkspaceControllerName strips the /v1/ prefix the gate keys on.
func TestZapWorkspaceControllerName(t *testing.T) {
	if got := zapControllerName("/v1/add-template"); got != "add-template" {
		t.Errorf("controllerName = %q, want add-template", got)
	}
}

// TestZapWorkspaceMultipartDecode proves the boundary-sniffing decoder recovers both
// form values and the named file part from a multipart body WITHOUT a Content-Type
// header — the gateway projection hands the handler only the raw body.
func TestZapWorkspaceMultipartDecode(t *testing.T) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("type", "video/mp4")
	_ = w.WriteField("name", "clip.mp4")
	fw, err := w.CreateFormFile("file", "clip.mp4")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	payload := []byte("BINARY-VIDEO-BYTES")
	_, _ = fw.Write(payload)
	_ = w.Close()

	body := buf.Bytes()

	values := zapFormValues(body)
	if values.Get("type") != "video/mp4" {
		t.Errorf("form type = %q, want video/mp4", values.Get("type"))
	}
	if values.Get("name") != "clip.mp4" {
		t.Errorf("form name = %q, want clip.mp4", values.Get("name"))
	}

	part, ok := zapMultipartFile(body, "file")
	if !ok {
		t.Fatal("file part not found")
	}
	if part.filename != "clip.mp4" {
		t.Errorf("filename = %q, want clip.mp4", part.filename)
	}
	if !bytes.Equal(part.data, payload) {
		t.Errorf("file data = %q, want %q", part.data, payload)
	}
}

// TestZapWorkspaceFormValuesUrlencoded proves the decoder also handles a urlencoded
// body (beego c.GetString reads both content types).
func TestZapWorkspaceFormValuesUrlencoded(t *testing.T) {
	values := zapFormValues([]byte("type=.pdf&name=report.pdf"))
	if values.Get("type") != ".pdf" || values.Get("name") != "report.pdf" {
		t.Errorf("urlencoded decode = %v", values)
	}
}
