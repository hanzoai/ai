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
// beego controller, no HTTP writer. Each handler re-implements the beego method's
// logic against object/ + iam, preserving identity, authz, and response shape
// EXACTLY.
//
// These are HTTP-shaped admin/self routes (query strings, GET vs POST, multipart
// uploads), so they ride the gateway HTTP-over-ZAP projection (MsgType 200):
// method(0) + path(8) + headers(16) + body(24) + query(32). The group
// self-registers its path prefixes from THIS file's init() — no edit to
// zap_native.go or any shared registration file. The beego routes in router.go
// keep serving in parallel until Integrate flips the gateway dispatch here.
//
// Reused, never duplicated: the registry primitive registerGatewayPath and the
// response/identity helpers (zapGwOk, zapGwError, zapPrincipalUser, zapPageOffset)
// live in zap_model-routing-config.go — this file only calls them. The video
// upload helpers (getAudioSegments, updateVideoCoverUrl) live in video.go and are
// called directly since we share the package.

package controllers

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/hanzoai/ai/conf"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/txt"
	"github.com/hanzoai/ai/util"
	"github.com/hanzoai/ai/video"
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
// per-controller ownership/sign-in checks then run, exactly as in beego).
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
	if user.Owner == "admin" {
		requested := strings.TrimSpace(q.Get("owner"))
		if requested != "" {
			return requested, true, nil
		}
	}
	return user.Owner, true, nil
}

// zapSessionUsername mirrors ApiController.GetSessionUsername: the bare user.Name
// (NOT owner/name) — the value the beego ownership checks compare against.
func zapSessionUsername(user *iam.User) string {
	if user == nil {
		return ""
	}
	return user.Name
}

func zapGetTemplates(owner string, q url.Values) (*zap.Message, error) {
	limit := q.Get("pageSize")
	page := q.Get("p")
	field := q.Get("field")
	value := q.Get("value")
	sortField := q.Get("sortField")
	sortOrder := q.Get("sortOrder")

	if limit == "" || page == "" {
		templates, err := object.GetTemplates(owner)
		if err != nil {
			return zapGwError(200, err.Error())
		}
		return zapGwOk(templates)
	}
	lim := util.ParseInt(limit)
	count, err := object.GetTemplateCount(owner, field, value)
	if err != nil {
		return zapGwError(200, err.Error())
	}
	offset := zapPageOffset(page, lim)
	templates, err := object.GetPaginationTemplates(owner, offset, lim, field, value, sortField, sortOrder)
	if err != nil {
		return zapGwError(200, err.Error())
	}
	return zapGwOk(templates, count)
}

func zapGetWorkflows(owner string, q url.Values) (*zap.Message, error) {
	limit := q.Get("pageSize")
	page := q.Get("p")
	field := q.Get("field")
	value := q.Get("value")
	sortField := q.Get("sortField")
	sortOrder := q.Get("sortOrder")

	if limit == "" || page == "" {
		workflows, err := object.GetWorkflows(owner)
		if err != nil {
			return zapGwError(200, err.Error())
		}
		return zapGwOk(object.GetMaskedWorkflows(workflows, true))
	}
	lim := util.ParseInt(limit)
	count, err := object.GetWorkflowCount(owner, field, value)
	if err != nil {
		return zapGwError(200, err.Error())
	}
	offset := zapPageOffset(page, lim)
	workflows, err := object.GetPaginationWorkflows(owner, offset, lim, field, value, sortField, sortOrder)
	if err != nil {
		return zapGwError(200, err.Error())
	}
	return zapGwOk(workflows, count)
}

func zapGetTasks(owner string, q url.Values) (*zap.Message, error) {
	limit := q.Get("pageSize")
	page := q.Get("p")
	field := q.Get("field")
	value := q.Get("value")
	sortField := q.Get("sortField")
	sortOrder := q.Get("sortOrder")

	if limit == "" || page == "" {
		tasks, err := object.GetTasks(owner)
		if err != nil {
			return zapGwError(200, err.Error())
		}
		return zapGwOk(object.GetMaskedTasks(tasks, true))
	}
	lim := util.ParseInt(limit)
	count, err := object.GetTaskCount(owner, field, value)
	if err != nil {
		return zapGwError(200, err.Error())
	}
	offset := zapPageOffset(page, lim)
	tasks, err := object.GetPaginationTasks(owner, offset, lim, field, value, sortField, sortOrder)
	if err != nil {
		return zapGwError(200, err.Error())
	}
	return zapGwOk(tasks, count)
}

func zapAnalyzeTask(user *iam.User, q url.Values) (*zap.Message, error) {
	if user == nil {
		return zapGwError(401, "auth:Please sign in first")
	}
	id := q.Get("id")
	log.Info("[analyze-task] ZAP request id=%s user=%s", id, zapSessionUsername(user))

	task, err := object.GetTask(id)
	if err != nil {
		return zapGwError(200, err.Error())
	}
	if task == nil {
		return zapGwError(200, "general:The task does not exist")
	}
	if !util.IsAdmin(user) && !conf.IsPreviewMode() && task.Owner != zapSessionUsername(user) {
		return zapGwError(200, "auth:Unauthorized operation")
	}

	result, err := object.AnalyzeTask(task, "en")
	if err != nil {
		return zapGwError(200, err.Error())
	}
	resultBytes, err := json.Marshal(result)
	if err != nil {
		return zapGwError(200, err.Error())
	}
	task.Result = string(resultBytes)
	task.Score = result.Score
	if _, err = object.UpdateTask(id, task); err != nil {
		return zapGwError(200, err.Error())
	}
	return zapGwOk(result)
}

func zapUploadTaskDocument(user *iam.User, q url.Values, body []byte) (*zap.Message, error) {
	if user == nil {
		return zapGwError(401, "auth:Please sign in first")
	}
	userName := zapSessionUsername(user)

	form := zapFormValues(body)
	taskId := q.Get("id")
	fileBase64 := form.Get("file")
	fileType := form.Get("type")
	fileName := form.Get("name")

	if taskId == "" || fileBase64 == "" || fileType == "" || fileName == "" {
		return zapGwError(200, "application:Missing required parameters")
	}

	task, err := object.GetTask(taskId)
	if err != nil {
		return zapGwError(200, err.Error())
	}
	if task == nil {
		return zapGwError(200, "general:The task does not exist")
	}
	if !util.IsAdmin(user) && task.Owner != userName {
		return zapGwError(200, "auth:Unauthorized operation")
	}

	ext := strings.ToLower(filepath.Ext(fileName))
	if ext != ".docx" && ext != ".pdf" {
		return zapGwError(200, "resource:Only docx and pdf files are allowed")
	}

	index := strings.Index(fileBase64, ",")
	if index == -1 {
		return zapGwError(200, "resource:Invalid file data format")
	}
	fileBytes, err := base64.StdEncoding.DecodeString(fileBase64[index+1:])
	if err != nil {
		return zapGwError(200, err.Error())
	}

	filePath := fmt.Sprintf("cloud/task-documents/%s/%s", userName, fileName)
	fileUrl, err := object.UploadFileToStorageSafe(userName, "file", "UploadTaskDocument", filePath, fileBytes)
	if err != nil {
		return zapGwError(200, err.Error())
	}

	documentText, err := txt.GetParsedTextFromUrl(fileUrl, ext, "en")
	if err != nil {
		log.Error("Failed to parse text from %s: %v", fileUrl, err)
		documentText = ""
	}

	task.DocumentUrl = fileUrl
	task.DocumentText = documentText

	success, err := object.UpdateTask(taskId, task)
	if err != nil {
		return zapGwError(200, err.Error())
	}
	if !success {
		return zapGwError(200, "general:Failed to update")
	}

	return zapGwOk(map[string]interface{}{
		"url":  fileUrl,
		"text": documentText,
	})
}

func zapGetVideos(q url.Values) (*zap.Message, error) {
	// Parity with beego GetVideos: owner is forced to "" (list is global-scoped).
	owner := ""
	limit := q.Get("pageSize")
	page := q.Get("p")
	field := q.Get("field")
	value := q.Get("value")
	sortField := q.Get("sortField")
	sortOrder := q.Get("sortOrder")

	if limit == "" || page == "" {
		videos, err := object.GetVideos(owner, "en")
		if err != nil {
			return zapGwError(200, err.Error())
		}
		return zapGwOk(videos)
	}
	lim := util.ParseInt(limit)
	count, err := object.GetVideoCount(owner, field, value)
	if err != nil {
		return zapGwError(200, err.Error())
	}
	offset := zapPageOffset(page, lim)
	videos, err := object.GetPaginationVideos(owner, offset, lim, field, value, sortField, sortOrder, "en")
	if err != nil {
		return zapGwError(200, err.Error())
	}
	return zapGwOk(videos, count)
}

func zapUploadVideo(user *iam.User, body []byte) (*zap.Message, error) {
	if user == nil {
		return zapGwError(401, "auth:Please sign in first")
	}
	userName := zapSessionUsername(user)

	part, ok := zapMultipartFile(body, "file")
	if !ok {
		return zapGwError(200, "http: no such file")
	}
	filename := part.filename
	fileId := util.RemoveExt(filename)
	ext := filepath.Ext(filename)
	fileBuffer := bytes.NewBuffer(part.data)

	fileType := "unknown"
	parts := strings.Split(part.contentType, "/")
	if len(parts) > 0 {
		fileType = parts[0]
	}
	if fileType != "video" {
		return zapGwError(200, fmt.Sprintf("contentType: %s is not video", part.contentType))
	}

	if err := object.SetDefaultVodClient("en"); err != nil {
		return zapGwError(200, err.Error())
	}

	videoId, err := video.UploadVideo(fileId, filename, fileBuffer)
	if err != nil {
		return zapGwError(200, err.Error())
	}
	if videoId == "" {
		return zapGwError(200, "video:UploadVideo() error, videoId should not be empty")
	}

	audioUrl, segments, err := getAudioSegments(userName, filename, fileBuffer, "en")
	if err != nil {
		return zapGwError(200, err.Error())
	}

	v := &object.Video{
		Owner:       userName,
		Name:        fileId,
		CreatedTime: util.GetCurrentTime(),
		DisplayName: fileId,
		Tag:         "",
		Type:        ext,
		VideoId:     videoId,
		CoverUrl:    "",
		AudioUrl:    audioUrl,
		Labels:      []*object.Label{},
		Segments:    segments,
		DataUrls:    []string{},
		DataUrl:     "",
		TagOnPause:  false,
		Remarks:     []*object.Remark{},
		Remarks2:    []*object.Remark{},
		State:       "Draft",
		Keywords:    []string{},
	}
	if _, err = object.AddVideo(v); err != nil {
		return zapGwError(200, err.Error())
	}

	id := util.GetIdFromOwnerAndName(userName, fileId)
	if err = updateVideoCoverUrl(id, videoId, "en"); err != nil {
		return zapGwError(200, err.Error())
	}

	return zapGwOk(fileId)
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
// urlencoded body, matching beego c.GetString across both content types.
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
