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

// Native ZAP handlers for the knowledge group: stores, storage-providers,
// files, tree-files, the file activation cache, and vectors.
//
// STRANGLER: each controller method (ApiController.Get/Update/Add/DeleteStore,
// RefreshStoreVectors, GetStoreNames, GetStorageProviders, Get/Update/Add/
// DeleteFile, RefreshFileVectors, UploadFile, Update/Add/DeleteTreeFile,
// ActivateFile, GetActiveFile, Get/Update/Add/DeleteVector, DeleteAllVectors)
// is re-implemented here as a pure ZAP handler against object/ + iam, mirroring
// zap_native.go:zapChatHandler. It NEVER wraps or drives the controller
// and never holds an http.ResponseWriter. The same routes stay live on
// routers.App, which also backs the gateway fallback.
//
// REGISTRATION (recipe, collision-free — mirrors zap_responses.go): this file
// declares NO shared registry symbol, so it compiles standalone in the strangler
// hybrid and never collides with a sibling group's registerCloud/registerGateway
// definition. It exposes its native-method and gateway-path tables as
// uniquely-named data (zapKnowledgeCloud / zapKnowledgeGateway) keyed to this
// group's handlers. This file's init() ranges over these to registerCloud(method,
// h) / registerGatewayPath(path, h). No group edits a shared registration file.
//
// PARITY NOTES:
//   - Query params (owner, id, store, pageSize, p, field, value, sortField,
//     sortOrder, key, filename, isLeaf, prefix) and the former multipart file /
//     Host header have no ZAP carrier, so they are decoded from the ONE JSON
//     body — the same pattern zapBalanceHandler / zap_router-policy-stats use.
//     Identity is ALWAYS the resolved credential (auth), NEVER a body field.
//   - The gateway table below is EMPTY, and the note that used to describe
//     overlapping /v1/get-store… prefixes described entries that are not there.
//     It is empty on purpose: these resources live at
//     /v1/ai/{stores,files,tree-files,vectors}, where a member answers four verbs
//     at /:owner/:name and a path-only matcher would pick the wrong handler
//     (zap_gateway_fallback.go). The MsgType 100 method names are this group's
//     whole ZAP surface; a gateway request reaches these resources through the
//     router.

package controllers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"sort"
	"strings"

	"github.com/luxfi/zap"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/log"

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

// ── ZAP dispatch tables (group-local, collision-free) ────────────────────
//
// A type ALIAS (not a new named type) so no shared symbol is declared.

type zapKnowledgeHandlerFn = func(ctx context.Context, auth string, body []byte) (*zap.Message, error)

// zapKnowledgeCloud maps native cloud method → handler (MsgType 100).
// init self-registers this group's dispatch tables into the canonical registry.
func init() {
	for method, h := range zapKnowledgeCloud {
		registerCloud(method, h)
	}
	for path, h := range zapKnowledgeGateway {
		registerGatewayPath(path, h)
	}
}

var zapKnowledgeCloud = map[string]zapKnowledgeHandlerFn{
	"stores.get-global":      zapGetGlobalStoresHandler,
	"stores.get":             zapGetStoresHandler,
	"stores.get-one":         zapGetStoreHandler,
	"stores.update":          zapUpdateStoreHandler,
	"stores.add":             zapAddStoreHandler,
	"stores.delete":          zapDeleteStoreHandler,
	"stores.refresh-vectors": zapRefreshStoreVectorsHandler,
	"stores.get-names":       zapGetStoreNamesHandler,
	"storage.get-providers":  zapGetStorageProvidersHandler,

	"files.get-global":      zapGetGlobalFilesHandler,
	"files.get":             zapGetFilesHandler,
	"files.get-one":         zapGetFileHandler,
	"files.update":          zapUpdateFileHandler,
	"files.add":             zapAddFileHandler,
	"files.delete":          zapDeleteFileHandler,
	"files.refresh-vectors": zapRefreshFileVectorsHandler,
	"files.upload":          zapUploadFileHandler,
	"files.activate":        zapActivateFileHandler,
	"files.get-active":      zapGetActiveFileHandler,

	"treefiles.update": zapUpdateTreeFileHandler,
	"treefiles.add":    zapAddTreeFileHandler,
	"treefiles.delete": zapDeleteTreeFileHandler,

	"vectors.get-global": zapGetGlobalVectorsHandler,
	"vectors.get":        zapGetVectorsHandler,
	"vectors.get-one":    zapGetVectorHandler,
	"vectors.update":     zapUpdateVectorHandler,
	"vectors.add":        zapAddVectorHandler,
	"vectors.delete":     zapDeleteVectorHandler,
	"vectors.delete-all": zapDeleteAllVectorsHandler,
}

// zapKnowledgeGateway maps gateway path → handler (MsgType 200). Longest-prefix
// wins (see PARITY NOTES).
var zapKnowledgeGateway = map[string]zapKnowledgeHandlerFn{}

// ── Shared seams (identity + response envelope + param parity) ───────────

// zapKSFVPrincipal resolves the request principal STRICTLY from its verified
// credential — the ZAP analogue of GetSessionUser (there is no session cookie on
// the ZAP path). pk-/sk- IAM keys route through getUserByAccessKey; JWTs through
// object.ParseAndValidateJWT (signature + iss/aud, never raw iam.ParseJwtToken).
// Returns nil for an empty/invalid/unsupported credential — fail-secure.
func zapKSFVPrincipal(auth string) *iam.User {
	token := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	if token == "" {
		return nil
	}
	if isIAMApiKey(token) {
		if user, err := getUserByAccessKey(token); err == nil && user != nil {
			return user
		}
		return nil
	}
	if isJwtToken(token) {
		if claims, err := object.ParseAndValidateJWT(token); err == nil && claims != nil {
			u := claims.User
			return &u
		}
	}
	return nil
}

// zapKSFVOk renders the ResponseOk envelope ({status:"ok", data}) as a 200
// cloud response, preserving the exact JSON shape the HTTP handlers return.
func zapKSFVOk(data interface{}) (*zap.Message, error) {
	b, _ := json.Marshal(Response{Status: "ok", Data: data})
	return object.BuildCloudResponse(http.StatusOK, b, "")
}

// zapKSFVOk2 mirrors ResponseOk(data, data2) (paginator nums / populate error).
func zapKSFVOk2(data, data2 interface{}) (*zap.Message, error) {
	b, _ := json.Marshal(Response{Status: "ok", Data: data, Data2: data2})
	return object.BuildCloudResponse(http.StatusOK, b, "")
}

// zapKSFVError mirrors ResponseError (HTTP 200 error envelope).
func zapKSFVError(msg string) (*zap.Message, error) {
	b, _ := json.Marshal(Response{Status: "error", Msg: msg})
	return object.BuildCloudResponse(http.StatusOK, b, "")
}

// zapKSFVStatusError mirrors ResponseErrorWithStatus (401/403 auth failures).
func zapKSFVStatusError(status int, msg string) (*zap.Message, error) {
	b, _ := json.Marshal(Response{Status: "error", Msg: msg})
	return object.BuildCloudResponse(uint32(status), b, "")
}

// zapKSFVScopedOwner mirrors GetScopedOwner: authenticated principal required;
// the admin org may target another owner via a body "owner" field, every other
// principal is scoped to its own org. Returns (owner, user, nil) or (…, deny).
func zapKSFVScopedOwner(auth string, bodyOwner string) (string, *iam.User, *zap.Message) {
	user := zapKSFVPrincipal(auth)
	if user == nil {
		msg, _ := zapKSFVStatusError(http.StatusUnauthorized, "auth:Please sign in first")
		return "", nil, msg
	}
	if user.Owner == "admin" {
		if requested := strings.TrimSpace(bodyOwner); requested != "" {
			return requested, user, nil
		}
	}
	return user.Owner, user, nil
}

// zapKSFVRequireAdmin mirrors ApiController.RequireAdmin (preview passes; else an
// org-level admin principal is required).
func zapKSFVRequireAdmin(auth string) (*iam.User, *zap.Message) {
	user := zapKSFVPrincipal(auth)
	if conf.IsPreviewMode() {
		return user, nil
	}
	if !util.IsAdmin(user) {
		msg, _ := zapKSFVStatusError(http.StatusForbidden, "auth:this operation requires admin privilege")
		return nil, msg
	}
	return user, nil
}

// zapKSFVRequireSignedIn mirrors RequireSignedIn / RequireSessionOwner: a
// principal is required, else 401.
func zapKSFVRequireSignedIn(auth string) (*iam.User, *zap.Message) {
	user := zapKSFVPrincipal(auth)
	if user == nil {
		msg, _ := zapKSFVStatusError(http.StatusUnauthorized, "auth:Please sign in first")
		return nil, msg
	}
	return user, nil
}

// zapKSFVEnforceStoreIsolation mirrors ApiController.EnforceStoreIsolation.
func zapKSFVEnforceStoreIsolation(user *iam.User, requested string) (string, *zap.Message) {
	if user == nil || user.Homepage == "" {
		return requested, nil
	}
	if requested == "" || requested == "All" {
		return user.Homepage, nil
	}
	if requested != user.Homepage {
		msg, _ := zapKSFVError("controllers:You can only access data from your assigned store")
		return "", msg
	}
	return requested, nil
}

// zapKSFVLang mirrors GetAcceptLanguage on a carrier that has no Accept-Language
// header: an optional body "language", defaulting to "en" (as zapChatHandler).
func zapKSFVLang(lang string) string {
	if lang == "" {
		return "en"
	}
	return lang
}

// zapKnowledgeParams is the body-decoded projection of the former query params.
type zapKnowledgeParams struct {
	Owner     string `json:"owner"`
	Id        string `json:"id"`
	Store     string `json:"store"`
	Name      string `json:"name"`
	PageSize  string `json:"pageSize"`
	P         string `json:"p"`
	Field     string `json:"field"`
	Value     string `json:"value"`
	SortField string `json:"sortField"`
	SortOrder string `json:"sortOrder"`
	Prefix    string `json:"prefix"`
	Key       string `json:"key"`
	Filename  string `json:"filename"`
	IsLeaf    bool   `json:"isLeaf"`
	Host      string `json:"host"`
	Language  string `json:"language"`
	// File upload / tree-leaf content (base64, dataURL-prefixed or raw).
	File     string `json:"file"`
	FileType string `json:"type"`
}

func zapDecodeParams(body []byte) zapKnowledgeParams {
	var p zapKnowledgeParams
	if len(body) > 0 {
		_ = json.Unmarshal(body, &p)
	}
	return p
}

// zapPaginated reports whether both pageSize and p are present (the the router branch
// that pages + admin-gates) and computes offset/limit like pagination.SetPaginator.
func zapPaginated(p zapKnowledgeParams) (bool, int, int) {
	if p.PageSize == "" || p.P == "" {
		return false, 0, 0
	}
	limit := util.ParseInt(p.PageSize)
	page := util.ParseInt(p.P)
	if page < 1 {
		page = 1
	}
	return true, (page - 1) * limit, limit
}

// zapDecodeBase64File mirrors UploadFile's decode: strips an optional dataURL
// prefix ("data:...;base64,") then base64-decodes.
func zapDecodeBase64File(fileBase64 string) ([]byte, error) {
	payload := fileBase64
	if i := strings.Index(fileBase64, ","); i != -1 {
		payload = fileBase64[i+1:]
	}
	return base64.StdEncoding.DecodeString(payload)
}

// zapBytesFile adapts a byte slice to multipart.File (AddTreeFile only io.Copy's
// it). bytes.Reader already implements Read/ReadAt/Seek; add a no-op Close.
type zapBytesFile struct{ *bytes.Reader }

func (zapBytesFile) Close() error { return nil }

// zapAddRecordForFile mirrors addRecordForFile without an HTTP context: it builds
// the audit Record directly (NewRecord's ctx-derived fields have no ZAP carrier)
// and writes it via the SAME object.AddRecord the HTTP path uses.
func zapAddRecordForFile(user *iam.User, action, sessionId, key, filename string, isLeaf bool, lang string) error {
	typ := "Folder"
	if isLeaf {
		typ = "File"
	}
	_, storeName, err := util.GetOwnerAndNameFromIdWithError(sessionId)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/%s/%s", key, filename)
	if filename == "" {
		path = key
	}
	text := fmt.Sprintf("%s%s, Session: %s, Path: %s", action, typ, storeName, path)
	rec := &object.Record{
		User:         GetUserName(user),
		Organization: user.Owner,
		RequestUri:   text,
	}
	_, _, err = object.AddRecord(rec, lang)
	return err
}

// ── stores ───────────────────────────────────────────────────────────────

func zapGetGlobalStoresHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	p := zapDecodeParams(body)
	paged, offset, limit := zapPaginated(p)
	if !paged {
		stores, err := object.GetGlobalStores()
		if err != nil {
			return zapKSFVError(err.Error())
		}
		return zapKSFVOk(stores)
	}

	if _, deny := zapKSFVRequireAdmin(auth); deny != nil {
		return deny, nil
	}
	count, err := object.GetStoreCount(p.Name, p.Field, p.Value)
	if err != nil {
		return zapKSFVError(err.Error())
	}
	stores, err := object.GetPaginationStores(offset, limit, p.Name, p.Field, p.Value, p.SortField, p.SortOrder)
	if err != nil {
		return zapKSFVError(err.Error())
	}
	sort.SliceStable(stores, func(i, j int) bool {
		return stores[i].IsDefault && !stores[j].IsDefault
	})
	if err = object.PopulateStoreCounts(stores); err != nil {
		return zapKSFVError(err.Error())
	}
	return zapKSFVOk2(stores, count)
}

func zapGetStoresHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	p := zapDecodeParams(body)
	owner, user, deny := zapKSFVScopedOwner(auth, p.Owner)
	if deny != nil {
		return deny, nil
	}
	stores, err := object.GetStores(owner)
	if err != nil {
		return zapKSFVError(err.Error())
	}
	stores = FilterStoresByHomepage(stores, user)
	return zapKSFVOk(stores)
}

func zapGetStoreHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	p := zapDecodeParams(body)

	var store *object.Store
	var err error
	if p.Id == "admin/_cloud_default_store_" {
		store, err = object.GetDefaultStore("admin")
	} else {
		store, err = object.GetStore(p.Id)
	}
	if err != nil {
		return zapKSFVError(err.Error())
	}

	// Populate builds absolute URLs from the request origin. ZAP has no Host, so
	// populate only when the caller supplies one; a populate error is non-fatal
	// (mirrors ResponseOk(store, err.Error())).
	if store != nil && strings.TrimSpace(p.Host) != "" {
		origin := getOriginFromHost(p.Host)
		if err = store.Populate(origin, zapKSFVLang(p.Language)); err != nil {
			return zapKSFVOk2(store, err.Error())
		}
	}
	return zapKSFVOk(store)
}

// zapDecodeStoreWithId decodes the store object from the body and resolves the
// target id: an explicit sibling "id" key wins (rename case), else the store's
// own owner/name — preserving the HTTP body-is-store wire shape.
func zapDecodeStoreWithId(body []byte) (*object.Store, string, error) {
	var store object.Store
	if err := json.Unmarshal(body, &store); err != nil {
		return nil, "", err
	}
	var idHolder struct {
		Id string `json:"id"`
	}
	_ = json.Unmarshal(body, &idHolder)
	id := idHolder.Id
	if id == "" {
		id = store.GetId()
	}
	return &store, id, nil
}

func zapUpdateStoreHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if _, deny := zapKSFVRequireSignedIn(auth); deny != nil {
		return deny, nil
	}
	store, id, err := zapDecodeStoreWithId(body)
	if err != nil {
		return zapKSFVError(err.Error())
	}

	oldStore, err := object.GetStore(id)
	if err != nil {
		return zapKSFVError(err.Error())
	}
	if oldStore.IsDefault && !store.IsDefault {
		return zapKSFVError("store:given that there must be one default store in Hanzo Cloud, you cannot set this store to non-default. You can directly set another store as default")
	}

	success, err := object.UpdateStore(id, store)
	if err != nil {
		return zapKSFVError(err.Error())
	}

	if !oldStore.IsDefault && store.IsDefault {
		stores, err := object.GetGlobalStores()
		if err != nil {
			return zapKSFVError(err.Error())
		}
		for _, store2 := range stores {
			if store2.GetId() != store.GetId() && store2.IsDefault {
				store2.IsDefault = false
				success, err = object.UpdateStore(store2.GetId(), store2)
				if err != nil {
					return zapKSFVError(err.Error())
				}
			}
		}
	}
	return zapKSFVOk(success)
}

func zapAddStoreHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	var store object.Store
	if err := json.Unmarshal(body, &store); err != nil {
		return zapKSFVError(err.Error())
	}

	// A store is created into the same org GetStores reads back, and no other. The
	// owner arrived on the request body, so a member of ANY org could file a store
	// into the chat plane's own tenant — where it is reachable as a default store,
	// and a default store names the model every chat answer runs and bills. This is
	// the scope the controller twin resolves (AddStore, store.go); only the admin org may
	// name an owner, and it names it through this seam rather than on the row.
	owner, _, deny := zapKSFVScopedOwner(auth, store.Owner)
	if deny != nil {
		return deny, nil
	}
	store.Owner = owner

	if err := object.SyncDefaultProvidersToStore(&store); err != nil {
		return zapKSFVError(err.Error())
	}
	if store.ModelProvider == "" {
		modelProvider, err := object.GetDefaultModelProvider()
		if err != nil {
			return zapKSFVError(err.Error())
		}
		if modelProvider != nil {
			store.ModelProvider = modelProvider.Name
		}
	}
	if store.EmbeddingProvider == "" {
		embeddingProvider, err := object.GetDefaultEmbeddingProvider()
		if err != nil {
			return zapKSFVError(err.Error())
		}
		if embeddingProvider != nil {
			store.EmbeddingProvider = embeddingProvider.Name
		}
	}

	success, err := object.AddStore(&store)
	if err != nil {
		return zapKSFVError(err.Error())
	}
	return zapKSFVOk(success)
}

func zapDeleteStoreHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if _, deny := zapKSFVRequireSignedIn(auth); deny != nil {
		return deny, nil
	}
	var store object.Store
	if err := json.Unmarshal(body, &store); err != nil {
		return zapKSFVError(err.Error())
	}
	if store.IsDefault {
		return zapKSFVError("store:Cannot delete the default store")
	}
	success, err := object.DeleteStore(&store)
	if err != nil {
		return zapKSFVError(err.Error())
	}
	return zapKSFVOk(success)
}

func zapRefreshStoreVectorsHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if _, deny := zapKSFVRequireSignedIn(auth); deny != nil {
		return deny, nil
	}
	var store object.Store
	if err := json.Unmarshal(body, &store); err != nil {
		return zapKSFVError(err.Error())
	}
	p := zapDecodeParams(body)
	ok, err := object.RefreshStoreVectors(&store, zapKSFVLang(p.Language))
	if err != nil {
		return zapKSFVError(err.Error())
	}
	return zapKSFVOk(ok)
}

func zapGetStoreNamesHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	p := zapDecodeParams(body)
	owner, user, deny := zapKSFVScopedOwner(auth, p.Owner)
	if deny != nil {
		return deny, nil
	}
	storeNames, err := object.GetStoresByFields(owner, "name", "display_name")
	if err != nil {
		return zapKSFVError(err.Error())
	}
	storeNames = FilterStoresByHomepage(storeNames, user)
	return zapKSFVOk(storeNames)
}

// ── storage providers ────────────────────────────────────────────────────

func zapGetStorageProvidersHandler(_ context.Context, auth string, _ []byte) (*zap.Message, error) {
	if _, deny := zapKSFVRequireSignedIn(auth); deny != nil {
		return deny, nil
	}
	providers, err := getStorageProviders()
	if err != nil {
		return zapKSFVError(err.Error())
	}
	return zapKSFVOk(providers)
}

// ── files ────────────────────────────────────────────────────────────────

func zapGetGlobalFilesHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	p := zapDecodeParams(body)
	paged, offset, limit := zapPaginated(p)
	if !paged {
		files, err := object.GetGlobalFiles()
		if err != nil {
			return zapKSFVError(err.Error())
		}
		return zapKSFVOk(files)
	}

	if _, deny := zapKSFVRequireAdmin(auth); deny != nil {
		return deny, nil
	}
	count, err := object.GetFileCount("", p.Field, p.Value)
	if err != nil {
		return zapKSFVError(err.Error())
	}
	files, err := object.GetPaginationFiles("", offset, limit, p.Field, p.Value, p.SortField, p.SortOrder)
	if err != nil {
		return zapKSFVError(err.Error())
	}
	return zapKSFVOk2(files, count)
}

func zapGetFilesHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	p := zapDecodeParams(body)
	owner, _, deny := zapKSFVScopedOwner(auth, p.Owner)
	if deny != nil {
		return deny, nil
	}
	var files []*object.File
	var err error
	if p.Store != "" {
		files, err = object.GetFilesByStore(owner, p.Store)
	} else {
		files, err = object.GetFiles(owner)
	}
	if err != nil {
		return zapKSFVError(err.Error())
	}
	return zapKSFVOk(files)
}

func zapGetFileHandler(_ context.Context, _ string, body []byte) (*zap.Message, error) {
	p := zapDecodeParams(body)
	file, err := object.GetFile(p.Id)
	if err != nil {
		return zapKSFVError(err.Error())
	}
	return zapKSFVOk(file)
}

func zapUpdateFileHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if _, deny := zapKSFVRequireSignedIn(auth); deny != nil {
		return deny, nil
	}
	p := zapDecodeParams(body)
	var file object.File
	if err := json.Unmarshal(body, &file); err != nil {
		return zapKSFVError(err.Error())
	}
	id := p.Id
	if id == "" {
		id = file.GetId()
	}
	success, err := object.UpdateFile(id, &file)
	if err != nil {
		return zapKSFVError(err.Error())
	}
	return zapKSFVOk(success)
}

func zapAddFileHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if _, deny := zapKSFVRequireSignedIn(auth); deny != nil {
		return deny, nil
	}
	var file object.File
	if err := json.Unmarshal(body, &file); err != nil {
		return zapKSFVError(err.Error())
	}
	success, err := object.AddFile(&file)
	if err != nil {
		return zapKSFVError(err.Error())
	}
	return zapKSFVOk(success)
}

func zapDeleteFileHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if _, deny := zapKSFVRequireSignedIn(auth); deny != nil {
		return deny, nil
	}
	p := zapDecodeParams(body)
	var file object.File
	if err := json.Unmarshal(body, &file); err != nil {
		return zapKSFVError(err.Error())
	}
	success, err := object.DeleteFile(&file, zapKSFVLang(p.Language))
	if err != nil {
		return zapKSFVError(err.Error())
	}
	return zapKSFVOk(success)
}

func zapRefreshFileVectorsHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if _, deny := zapKSFVRequireSignedIn(auth); deny != nil {
		return deny, nil
	}
	p := zapDecodeParams(body)
	var file object.File
	if err := json.Unmarshal(body, &file); err != nil {
		return zapKSFVError(err.Error())
	}
	ok, err := object.RefreshFileVectors(&file, zapKSFVLang(p.Language))
	if err != nil {
		return zapKSFVError(err.Error())
	}
	return zapKSFVOk(ok)
}

func zapUploadFileHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user, deny := zapKSFVRequireSignedIn(auth)
	if deny != nil {
		return deny, nil
	}
	userName := GetUserName(user)
	p := zapDecodeParams(body)

	if p.File == "" || p.FileType == "" || p.Name == "" {
		return zapKSFVError("application:Missing required parameters")
	}
	if !strings.Contains(p.File, ",") {
		return zapKSFVError("resource:Invalid file data format")
	}
	fileBytes, err := zapDecodeBase64File(p.File)
	if err != nil {
		return zapKSFVError(err.Error())
	}

	filePath := fmt.Sprintf("cloud/avatars/%s/%s", userName, p.Name)
	fileUrl, err := object.UploadFileToStorageSafe(userName, "file", "UploadStoreAvatar", filePath, fileBytes)
	if err != nil {
		return zapKSFVError(err.Error())
	}
	return zapKSFVOk(fileUrl)
}

// ── file activation cache ────────────────────────────────────────────────

func zapActivateFileHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if _, deny := zapKSFVRequireSignedIn(auth); deny != nil {
		return deny, nil
	}
	p := zapDecodeParams(body)

	prefix := getCachePrefix(p.Filename)
	if prefix == "" {
		return zapKSFVOk(false)
	}
	path := fmt.Sprintf("%s/%s", cacheDir, p.Key)
	cacheMap[prefix] = path
	log.Info("%v", cacheMap)

	if !util.FileExist(getAppPath(p.Filename)) {
		util.CopyFile(getAppPath(p.Filename), getAppPath(prefix))
	}
	return zapKSFVOk(true)
}

func zapGetActiveFileHandler(_ context.Context, _ string, body []byte) (*zap.Message, error) {
	p := zapDecodeParams(body)
	res := ""
	if v, ok := cacheMap[p.Prefix]; ok {
		res = v
	}
	return zapKSFVOk(res)
}

// ── tree files ───────────────────────────────────────────────────────────

func zapUpdateTreeFileHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user, deny := zapKSFVRequireSignedIn(auth)
	if deny != nil {
		return deny, nil
	}
	p := zapDecodeParams(body)

	var file object.TreeFile
	if err := json.Unmarshal(body, &file); err != nil {
		return zapKSFVError(err.Error())
	}

	lang := zapKSFVLang(p.Language)
	res := object.UpdateTreeFile(p.Store, p.Key, &file)
	if res {
		if err := zapAddRecordForFile(user, "Update", p.Store, p.Key, "", true, lang); err != nil {
			return zapKSFVError(err.Error())
		}
	}
	return zapKSFVOk(res)
}

func zapAddTreeFileHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user, deny := zapKSFVRequireSignedIn(auth)
	if deny != nil {
		return deny, nil
	}
	userName := GetUserName(user)
	p := zapDecodeParams(body)
	lang := zapKSFVLang(p.Language)

	// Leaf content arrives base64-encoded in the body (no multipart on ZAP).
	var file multipart.File
	if p.IsLeaf {
		if p.File == "" {
			return zapKSFVError("resource:Invalid file data format")
		}
		fileBytes, err := zapDecodeBase64File(p.File)
		if err != nil {
			return zapKSFVError(err.Error())
		}
		file = zapBytesFile{bytes.NewReader(fileBytes)}
	}

	res, bs, err := object.AddTreeFile(p.Store, userName, p.Key, p.IsLeaf, p.Filename, file, lang)
	if err != nil {
		return zapKSFVError(err.Error())
	}
	if res {
		if err = addFileToCache(p.Key, p.Filename, bs); err != nil {
			return zapKSFVError(err.Error())
		}
		if err = zapAddRecordForFile(user, "Add", p.Store, p.Key, p.Filename, p.IsLeaf, lang); err != nil {
			return zapKSFVError(err.Error())
		}
	}
	return zapKSFVOk(res)
}

func zapDeleteTreeFileHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user, deny := zapKSFVRequireSignedIn(auth)
	if deny != nil {
		return deny, nil
	}
	p := zapDecodeParams(body)
	lang := zapKSFVLang(p.Language)

	res, err := object.DeleteTreeFile(p.Store, p.Key, p.IsLeaf, lang)
	if err != nil {
		return zapKSFVError(err.Error())
	}
	if res {
		if err = zapAddRecordForFile(user, "Delete", p.Store, p.Key, "", p.IsLeaf, lang); err != nil {
			return zapKSFVError(err.Error())
		}
	}
	return zapKSFVOk(res)
}

// ── vectors ──────────────────────────────────────────────────────────────

func zapGetGlobalVectorsHandler(_ context.Context, _ string, _ []byte) (*zap.Message, error) {
	vectors, err := object.GetGlobalVectors()
	if err != nil {
		return zapKSFVError(err.Error())
	}
	return zapKSFVOk(vectors)
}

func zapGetVectorsHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user, deny := zapKSFVRequireSignedIn(auth)
	if deny != nil {
		return deny, nil
	}
	owner := user.Owner
	p := zapDecodeParams(body)

	storeName, isoDeny := zapKSFVEnforceStoreIsolation(user, p.Store)
	if isoDeny != nil {
		return isoDeny, nil
	}

	paged, offset, limit := zapPaginated(p)
	if !paged {
		vectors, err := object.GetVectors(owner)
		if err != nil {
			return zapKSFVError(err.Error())
		}
		return zapKSFVOk(vectors)
	}
	count, err := object.GetVectorCount(owner, storeName, p.Field, p.Value)
	if err != nil {
		return zapKSFVError(err.Error())
	}
	vectors, err := object.GetPaginationVectors(owner, storeName, offset, limit, p.Field, p.Value, p.SortField, p.SortOrder)
	if err != nil {
		return zapKSFVError(err.Error())
	}
	return zapKSFVOk2(vectors, count)
}

func zapGetVectorHandler(_ context.Context, _ string, body []byte) (*zap.Message, error) {
	p := zapDecodeParams(body)
	vector, err := object.GetVector(p.Id)
	if err != nil {
		return zapKSFVError(err.Error())
	}
	return zapKSFVOk(vector)
}

func zapUpdateVectorHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if _, deny := zapKSFVRequireSignedIn(auth); deny != nil {
		return deny, nil
	}
	p := zapDecodeParams(body)
	var vector object.Vector
	if err := json.Unmarshal(body, &vector); err != nil {
		return zapKSFVError(err.Error())
	}
	id := p.Id
	if id == "" {
		id = vector.GetId()
	}
	success, err := object.UpdateVector(id, &vector, zapKSFVLang(p.Language))
	if err != nil {
		return zapKSFVError(err.Error())
	}
	return zapKSFVOk(success)
}

func zapAddVectorHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if _, deny := zapKSFVRequireSignedIn(auth); deny != nil {
		return deny, nil
	}
	var vector object.Vector
	if err := json.Unmarshal(body, &vector); err != nil {
		return zapKSFVError(err.Error())
	}
	if vector.Provider == "" {
		embeddingProvider, err := object.GetDefaultEmbeddingProvider()
		if err != nil {
			return zapKSFVError(err.Error())
		}
		if embeddingProvider != nil {
			vector.Provider = embeddingProvider.Name
		}
	}
	success, err := object.AddVector(&vector)
	if err != nil {
		return zapKSFVError(err.Error())
	}
	return zapKSFVOk(success)
}

func zapDeleteVectorHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	if _, deny := zapKSFVRequireSignedIn(auth); deny != nil {
		return deny, nil
	}
	var vector object.Vector
	if err := json.Unmarshal(body, &vector); err != nil {
		return zapKSFVError(err.Error())
	}
	success, err := object.DeleteVector(&vector)
	if err != nil {
		return zapKSFVError(err.Error())
	}
	return zapKSFVOk(success)
}

func zapDeleteAllVectorsHandler(_ context.Context, auth string, _ []byte) (*zap.Message, error) {
	user, deny := zapKSFVRequireSignedIn(auth)
	if deny != nil {
		return deny, nil
	}
	owner := user.Owner

	vectors, err := object.GetVectors(owner)
	if err != nil {
		return zapKSFVError(err.Error())
	}
	allSuccess := true
	for i := range vectors {
		success, err := object.DeleteVector(vectors[i])
		if err != nil {
			return zapKSFVError(err.Error())
		}
		if !success {
			allSuccess = false
		}
	}
	return zapKSFVOk(allSuccess)
}
