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

// Native ZAP handlers for the workspace-CRUD group (strangler migration of
// template.go, workflow.go, task.go, task_upload.go, video.go). Pure ZAP — no
// controller, no HTTP writer. Each handler re-implements the controller method's
// logic against object/ + iam, preserving identity, authz, and response shape
// EXACTLY.
//
// These are HTTP-shaped admin/self routes (query strings, GET vs POST, multipart
// uploads), so they ride the gateway HTTP-over-ZAP projection (MsgType 200):
// method(0) + path(8) + headers(16) + body(24) + query(32). The group
// self-registers its path prefixes from THIS file's init() — no edit to
// zap_native.go or any shared registration file. The same routes stay live on
// routers.App, which also backs the gateway fallback.
//
// Reused, never duplicated: the registry primitive registerGatewayPath and the
// response/identity helpers (zapGwOk, zapGwError, zapPrincipalUser, zapPageOffset)
// live in zap_model-routing-config.go — this file only calls them. The video
// upload helpers (getAudioSegments, updateVideoCoverUrl) live in video.go and are
// called directly since we share the package.

package controllers

import (
	"bytes"
	"mime/multipart"
	"net/url"
	"strings"

	"github.com/hanzoai/ai/conf"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/util"
	"github.com/luxfi/zap"
)

func init() {
	// Templates.

	// Workflows.

	// Tasks.

	// Videos (workspace CRUD + upload — NOT the async /v1/videos/generations family,
	// which is a separate group in zap_videos-generation.go).
}

// ── Shared authz + identity for this group ─────────────────────────────────────

// zapWorkspaceGate reproduces routers/authz_filter.go permissionFilter for THIS
// group's non-super-admin, non-present-credential routes (belt-AND-suspenders, the
// same convention zap_model-routing-config.go uses for its SuperAdmin routes). It
// keys on controllerName (the path minus "/v1/") and honors conf.DisablePreviewMode
// so the native gate is identical to the HTTP gate. Returns a refusal message to
// return verbatim, or nil when the coarse gate allows the request through (the
// per-controller ownership/sign-in checks then run, exactly as in the controller layer).
func zapWorkspaceGate(user *iam.User, name string) *zap.Message {
	disablePreview := conf.DisablePreviewMode()
	isUpdate := strings.HasPrefix(name, "update-") || strings.HasPrefix(name, "add-") ||
		strings.HasPrefix(name, "delete-") || strings.HasPrefix(name, "refresh-") ||
		strings.HasPrefix(name, "deploy-")
	isGet := strings.HasPrefix(name, "get-")

	// Preview-mode ON: reads are open (the controller still self-scopes).
	if !disablePreview && isGet {
		return nil
	}
	// Neither a get- nor a write- verb (e.g. upload-*, analyze-task): the coarse
	// admin gate does not apply; the controller self-authenticates.
	if !isGet && !isUpdate {
		return nil
	}
	if zapWorkspaceExempt[name] {
		return nil
	}
	if !util.IsAdmin(user) {
		m, _ := zapGwError(403, "auth:this operation requires admin privilege")
		return m
	}
	return nil
}

// zapWorkspaceExempt mirrors permissionFilter's exemptedPaths for the entries this
// group serves — these routes self-scope / self-authenticate in their controller,
// so they skip the coarse org-admin gate.
var zapWorkspaceExempt = map[string]bool{
	"get-global-videos":    true,
	"get-videos":           true,
	"get-video":            true,
	"get-tasks":            true,
	"get-task":             true,
	"update-task":          true,
	"add-task":             true,
	"delete-task":          true,
	"upload-task-document": true,
}

// zapControllerName is the path minus "/v1/", the key permissionFilter gates on.
func zapControllerName(path string) string {
	return strings.TrimPrefix(path, "/v1/")
}

// zapScopedOwner reproduces ApiController.GetScopedOwner: a super-admin (owner ==
// "admin") may target a specific ?owner=; everyone else is pinned to their own org.
// Returns ("", false, refusal) when there is no verified principal (401).
func zapScopedOwner(user *iam.User, q url.Values) (string, bool, *zap.Message) {
	if user == nil {
		m, _ := zapGwError(401, "auth:Please sign in first")
		return "", false, m
	}
	return util.ScopeOwner(user.Owner, q.Get("owner")), true, nil
}

// zapSessionUsername mirrors ApiController.GetSessionUsername: the bare user.Name
// (NOT owner/name) — the value the the router ownership checks compare against.
func zapSessionUsername(user *iam.User) string {
	if user == nil {
		return ""
	}
	return user.Name
}

// ── Multipart / form decoding over the gateway body ────────────────────────────
//
// The gateway HTTP-over-ZAP projection hands the native handler the raw request
// body but not the Content-Type header, so multipart bodies are parsed by sniffing
// the boundary from the leading delimiter line ("--<boundary>\r\n"). This keeps the
// upload handlers self-contained without a second header seam.

type zapFilePart struct {
	filename    string
	contentType string
	data        []byte
}

// zapMultipartReader returns a multipart.Reader when body looks like a multipart
// payload (starts with the "--<boundary>" delimiter), else ok=false.
func zapMultipartReader(body []byte) (*multipart.Reader, bool) {
	if !bytes.HasPrefix(body, []byte("--")) {
		return nil, false
	}
	nl := bytes.IndexByte(body, '\n')
	if nl < 0 {
		return nil, false
	}
	first := bytes.TrimRight(body[:nl], "\r")
	boundary := string(bytes.TrimPrefix(first, []byte("--")))
	if boundary == "" {
		return nil, false
	}
	return multipart.NewReader(bytes.NewReader(body), boundary), true
}

// zapMultipartFile extracts the named file part from a multipart body.
func zapMultipartFile(body []byte, field string) (*zapFilePart, bool) {
	mr, ok := zapMultipartReader(body)
	if !ok {
		return nil, false
	}
	for {
		p, err := mr.NextPart()
		if err != nil {
			break
		}
		if p.FormName() != field || p.FileName() == "" {
			continue
		}
		data, err := readAllPart(p)
		if err != nil {
			return nil, false
		}
		return &zapFilePart{
			filename:    p.FileName(),
			contentType: p.Header.Get("Content-Type"),
			data:        data,
		}, true
	}
	return nil, false
}

// zapFormValues decodes non-file form fields from either a multipart body or a
// urlencoded body, matching the router c.GetString across both content types.
func zapFormValues(body []byte) url.Values {
	values := url.Values{}
	if mr, ok := zapMultipartReader(body); ok {
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			if p.FileName() != "" {
				continue
			}
			data, err := readAllPart(p)
			if err != nil {
				continue
			}
			values.Set(p.FormName(), string(data))
		}
		return values
	}
	if parsed, err := url.ParseQuery(string(body)); err == nil {
		return parsed
	}
	return values
}

func readAllPart(p *multipart.Part) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(p); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
