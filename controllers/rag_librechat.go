// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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

// LibreChat-compat RAG surface — the drop-in replacement for the retired
// standalone chat-rag-api (danny-avila/rag_api fork). hanzo.chat's RAG client
// (api/server/services/Files/VectorDB/crud.js + createContextHandlers.js) calls
// a FIXED contract at RAG_API_URL:
//
//	POST   {base}/embed                     multipart(file_id,file[,entity_id])
//	                                        -> {status, known_type, file_id, filename}
//	POST   {base}/query                     {file_id,query,k} -> [[{page_content,metadata},score],...]
//	POST   {base}/query_multiple            {file_ids,query,k} -> same tuple shape
//	DELETE {base}/documents                 [file_id,...]
//	GET    {base}/documents/{file_id}/context -> [{page_content,metadata},...]
//
// Pointing RAG_API_URL at https://api.hanzo.ai/v1 makes these resolve here with
// ZERO chat-repo change. The handlers are a thin HTTP projection over the ONE
// canonical RAG logic (object.Rag*); they hold no retrieval logic of their own.
// The native /v1/rag/* surface (controllers/rag.go) shares that same logic.

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/txt"
)

// lcDocument mirrors LangChain's Document JSON ({page_content, metadata}) that
// the retired rag-api returned and hanzo.chat's client parses (item[0].page_content).
type lcDocument struct {
	PageContent string                 `json:"page_content"`
	Metadata    map[string]interface{} `json:"metadata"`
}

// lcResultToDocument converts a canonical DocSearchResult into the LangChain
// Document shape, preserving file_id/title/url in metadata as the old rag-api did.
func lcResultToDocument(r object.DocSearchResult) lcDocument {
	return lcDocument{
		PageContent: r.Content,
		Metadata: map[string]interface{}{
			"file_id": r.FileID,
			"title":   r.Title,
			"url":     r.URL,
			"id":      r.ID,
		},
	}
}

// lcTuples renders results as [[document, score], ...]. The retired rag-api
// returned (Document, distance) tuples; hanzo.chat only reads item[0], so the
// score slot carries the rank position (lower = better) as a stable placeholder
// — the client does not use it, and the native /v1/rag/query exposes real scores.
func lcTuples(results []object.DocSearchResult) []interface{} {
	out := make([]interface{}, 0, len(results))
	for i, r := range results {
		out = append(out, []interface{}{lcResultToDocument(r), i})
	}
	return out
}

// isKnownType reports whether the filename's extension is a parser-supported
// type. hanzo.chat treats known_type=false as "filetype not supported".
func isKnownType(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		return false
	}
	for _, t := range txt.GetSupportedFileTypes() {
		if t == ext {
			return true
		}
	}
	// Source-code / plaintext-ish files are parsed as plain text.
	return true
}

// RagEmbedMultipart handles POST /v1/embed — the multipart upload hanzo.chat's
// uploadVectors() sends. It saves the upload to a temp file, then reuses
// object.RagEmbedFile (same parse+chunk+index as the native surface).
//
// @Title RagEmbedMultipart
// @Tag RAG API (LibreChat-compat)
// @Description Multipart file embed (drop-in for the retired rag-api POST /embed).
// @router /embed [post]
func (c *ApiController) RagEmbedMultipart() {
	auth := c.requireIndexAuth()
	if auth == nil {
		return
	}

	fileID := c.GetString("file_id")
	if fileID == "" {
		c.ragCompatError("file_id is required", "")
		return
	}
	entityID := c.GetString("entity_id") // reserved for shared-resource scoping

	f, header, err := c.GetFile("file")
	if err != nil {
		c.ragCompatError("file is required: "+err.Error(), fileID)
		return
	}
	defer f.Close()

	filename := header.Filename
	if !isKnownType(filename) {
		// Match the retired rag-api: report the unsupported type, don't 500.
		c.Data["json"] = map[string]interface{}{
			"status": false, "known_type": false, "file_id": fileID, "filename": filename,
		}
		c.ServeJSON()
		return
	}

	tmpPath, err := saveUploadToTemp(f, filename)
	if err != nil {
		c.ragCompatError("failed to buffer upload: "+err.Error(), fileID)
		return
	}
	defer os.Remove(tmpPath)

	// URL as a local path -> object.RagEmbedFile parses via txt.GetParsedTextFromUrl
	// (which treats a non-http url as a path), the SAME parser doc-ingest uses.
	req := &object.RagEmbedRequest{
		FileID:   fileID,
		Filename: filename,
		URL:      tmpPath,
		Tag:      entityID,
	}
	result, err := object.RagEmbedFile(auth.Owner, req, c.GetAcceptLanguage())
	if err != nil {
		recordSearchUsage(auth, "index-docs", "rag-embed", "error", 0, c.Ctx.Request.RemoteAddr)
		c.ragCompatError(err.Error(), fileID)
		return
	}
	recordSearchUsage(auth, "index-docs", "rag-embed", "success", result.Chunks, c.Ctx.Request.RemoteAddr)

	c.Data["json"] = map[string]interface{}{
		"status":     true,
		"known_type": true,
		"file_id":    fileID,
		"filename":   filename,
		"chunks":     result.Chunks,
	}
	c.ServeJSON()
}

// RagQueryCompat handles POST /v1/query — {file_id,query,k}. Returns LangChain
// (document,score) tuples.
//
// @Title RagQueryCompat
// @Tag RAG API (LibreChat-compat)
// @Description File-scoped query (drop-in for the retired rag-api POST /query).
// @router /query [post]
func (c *ApiController) RagQueryCompat() {
	c.ragQueryCompat()
}

// RagQueryMultipleCompat handles POST /v1/query_multiple — {file_ids,query,k}.
//
// @Title RagQueryMultipleCompat
// @Tag RAG API (LibreChat-compat)
// @Description Multi-file query (drop-in for the retired rag-api POST /query_multiple).
// @router /query_multiple [post]
func (c *ApiController) RagQueryMultipleCompat() {
	c.ragQueryCompat()
}

// ragQueryCompat is the shared read path for /v1/query and /v1/query_multiple.
// The canonical RagQueryRequest already carries both file_id and file_ids.
func (c *ApiController) ragQueryCompat() {
	auth := c.resolveSearchAuth()
	if auth == nil {
		return
	}

	var req object.RagQueryRequest
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, &req); err != nil {
		c.ResponseError(err.Error())
		return
	}

	results, err := object.RagQuery(auth.Owner, &req, c.GetAcceptLanguage())
	if err != nil {
		recordSearchUsage(auth, "search-query", "rag", "error", 0, c.Ctx.Request.RemoteAddr)
		c.ResponseError(err.Error())
		return
	}
	recordSearchUsage(auth, "search-query", "rag", "success", len(results), c.Ctx.Request.RemoteAddr)

	c.Data["json"] = lcTuples(results)
	c.ServeJSON()
}

// RagDeleteDocuments handles DELETE /v1/documents — a JSON array of file_ids.
//
// @Title RagDeleteDocuments
// @Tag RAG API (LibreChat-compat)
// @Description Delete files by id (drop-in for the retired rag-api DELETE /documents).
// @router /documents [delete]
func (c *ApiController) RagDeleteDocuments() {
	auth := c.requireIndexAuth()
	if auth == nil {
		return
	}

	var fileIDs []string
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, &fileIDs); err != nil {
		c.ResponseError(err.Error())
		return
	}
	if len(fileIDs) == 0 {
		c.ResponseError("expected a non-empty array of file_ids")
		return
	}

	lang := c.GetAcceptLanguage()
	deleted := 0
	for _, id := range fileIDs {
		if id == "" {
			continue
		}
		if err := object.DeleteRagFile(auth.Owner, "", id, lang); err != nil {
			c.ResponseError(err.Error())
			return
		}
		deleted++
	}
	c.Data["json"] = map[string]interface{}{
		"message": "Documents deleted successfully",
		"deleted": deleted,
	}
	c.ServeJSON()
}

// RagDocumentContext handles GET /v1/documents/:file_id/context — every chunk of
// a file, as LangChain Documents (used when RAG_USE_FULL_CONTEXT is on).
//
// @Title RagDocumentContext
// @Tag RAG API (LibreChat-compat)
// @Description Full file context (drop-in for rag-api GET /documents/{id}/context).
// @router /documents/:file_id/context [get]
func (c *ApiController) RagDocumentContext() {
	auth := c.resolveSearchAuth()
	if auth == nil {
		return
	}

	fileID := c.Ctx.Input.Param(":file_id")
	if fileID == "" {
		c.ResponseError("file_id must not be empty")
		return
	}

	results, err := object.RagFileContext(auth.Owner, "", fileID)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	docs := make([]lcDocument, 0, len(results))
	for _, r := range results {
		docs = append(docs, lcResultToDocument(r))
	}
	c.Data["json"] = docs
	c.ServeJSON()
}

// ragCompatError emits the retired rag-api's failure shape ({status:false,...})
// so hanzo.chat's uploadVectors() surfaces a clean "File embedding failed".
func (c *ApiController) ragCompatError(msg, fileID string) {
	c.Data["json"] = map[string]interface{}{
		"status": false, "known_type": true, "file_id": fileID, "detail": msg,
	}
	c.ServeJSON()
}

// saveUploadToTemp streams an uploaded file to a temp path, preserving the
// extension so the parser can dispatch on it.
func saveUploadToTemp(f io.Reader, filename string) (string, error) {
	ext := filepath.Ext(filename)
	tmp, err := os.CreateTemp("", "rag-upload-*"+ext)
	if err != nil {
		return "", err
	}
	defer tmp.Close()
	if _, err := io.Copy(tmp, f); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}
