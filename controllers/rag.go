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

// File-scoped RAG API — the consolidated home of the retired standalone
// chat-rag-api. This is the SINGLE canonical RAG surface for uploaded-file
// retrieval: /v1/rag/embed (ingest one file), /v1/rag/query (retrieve within a
// file), /v1/rag/query-multiple (across files), /v1/rag/delete, /v1/rag/context.
//
// It reuses the exact auth (resolveSearchAuth / requireIndexAuth), billing
// (recordSearchUsage), embedding, and Search+Vector storage the doc RAG uses —
// no parallel infrastructure. The owner is always the AUTHENTICATED principal;
// a client-supplied owner is never trusted (tenant isolation).

import (
	"encoding/json"
	"net/http"

	"github.com/hanzoai/ai/object"
)

// RagEmbed
// @Title RagEmbed
// @Tag RAG API
// @Description Parse, chunk, and embed one uploaded file under its file_id into
//
//	the unified Search+Vector index, scoped to the authenticated owner. Provide
//	inline `content` or a `url` to fetch+parse (PDF/CSV/XLSX/PPTX/…). Re-embedding
//	the same file_id replaces its chunks. Consolidates the retired chat-rag-api
//	POST /embed and /local/embed.
//
// @Param body body object.RagEmbedRequest true "Embed request"
// @Success 200 {object} object.RagEmbedResult "Embed result"
// @router /rag/embed [post]
func (c *ApiController) RagEmbed() {
	auth := c.requireIndexAuth()
	if auth == nil {
		return
	}

	var req object.RagEmbedRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		c.ResponseError(err.Error())
		return
	}
	if req.FileID == "" {
		c.ResponseError("file_id must not be empty")
		return
	}

	result, err := object.RagEmbedFile(auth.Owner, &req, c.GetAcceptLanguage())
	if err != nil {
		recordSearchUsage(auth, "index-docs", "rag-embed", "error", 0, c.Fiber().IP())
		c.ResponseError(err.Error())
		return
	}

	recordSearchUsage(auth, "index-docs", "rag-embed", "success", result.Chunks, c.Fiber().IP())
	c.ResponseOk(result)
}

// RagQuery
// @Title RagQuery
// @Tag RAG API
// @Description Retrieve the top-K chunks relevant to a query, scoped to a single
//
//	uploaded file (`file_id`). Hybrid keyword+vector retrieval over the same
//	index. Consolidates the retired chat-rag-api POST /query.
//
// @Param body body object.RagQueryRequest true "Query request"
// @Success 200 {array} object.DocSearchResult "Matching chunks"
// @router /rag/query [post]
func (c *ApiController) RagQuery() {
	c.ragQuery()
}

// RagQueryMultiple
// @Title RagQueryMultiple
// @Tag RAG API
// @Description Retrieve the top-K chunks relevant to a query, scoped to a SET of
//
//	uploaded files (`file_ids`). Consolidates the retired chat-rag-api POST
//	/query_multiple. Shares one retrieval path with /rag/query.
//
// @Param body body object.RagQueryRequest true "Query request"
// @Success 200 {array} object.DocSearchResult "Matching chunks"
// @router /rag/query-multiple [post]
func (c *ApiController) RagQueryMultiple() {
	c.ragQuery()
}

// ragQuery is the shared read path for /rag/query and /rag/query-multiple. The
// request struct carries both file_id and file_ids, so one handler serves both.
func (c *ApiController) ragQuery() {
	auth := c.resolveSearchAuth()
	if auth == nil {
		return
	}

	var req object.RagQueryRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		c.ResponseError(err.Error())
		return
	}

	results, err := object.RagQuery(auth.Owner, &req, c.GetAcceptLanguage())
	if err != nil {
		recordSearchUsage(auth, "search-query", "rag", "error", 0, c.Fiber().IP())
		c.ResponseError(err.Error())
		return
	}

	recordSearchUsage(auth, "search-query", "rag", "success", len(results), c.Fiber().IP())
	// Raw array, matching the retired rag-api /query response shape (a list of
	// scored chunks) so LibreChat's RAG client parses it unchanged.
	c.JSON(http.StatusOK, results)
}

// ragDeleteBody is the body for POST /rag/delete.
type ragDeleteBody struct {
	FileID  string   `json:"file_id,omitempty"`
	FileIDs []string `json:"file_ids,omitempty"`
	Store   string   `json:"store,omitempty"`
}

// RagDelete
// @Title RagDelete
// @Tag RAG API
// @Description Delete all chunks of one or more uploaded files (by file_id) from
//
//	the owner's Search+Vector index. Consolidates the retired chat-rag-api
//	DELETE /documents.
//
// @Param body body controllers.ragDeleteBody true "Delete request"
// @Success 200 {object} controllers.Response The Response object
// @router /rag/delete [post]
func (c *ApiController) RagDelete() {
	auth := c.requireIndexAuth()
	if auth == nil {
		return
	}

	var body ragDeleteBody
	if err := json.Unmarshal(c.Body(), &body); err != nil {
		c.ResponseError(err.Error())
		return
	}

	ids := body.FileIDs
	if body.FileID != "" {
		ids = append(ids, body.FileID)
	}
	if len(ids) == 0 {
		c.ResponseError("file_id or file_ids must be provided")
		return
	}

	lang := c.GetAcceptLanguage()
	deleted := 0
	for _, id := range ids {
		if id == "" {
			continue
		}
		if err := object.DeleteRagFile(auth.Owner, body.Store, id, lang); err != nil {
			c.ResponseError(err.Error())
			return
		}
		deleted++
	}
	c.ResponseOk(map[string]interface{}{"deleted": deleted})
}

// RagContext
// @Title RagContext
// @Tag RAG API
// @Description Return every stored chunk of one file_id (full document context).
//
//	Consolidates the retired chat-rag-api GET /documents/{id}/context.
//
// @Param file_id query string true "The file_id"
// @Param store query string false "Store slug (default rag-files)"
// @Success 200 {array} object.DocSearchResult "All chunks of the file"
// @router /rag/context [get]
func (c *ApiController) RagContext() {
	auth := c.resolveSearchAuth()
	if auth == nil {
		return
	}

	fileID := c.Input().Get("file_id")
	if fileID == "" {
		c.ResponseError("file_id must not be empty")
		return
	}

	results, err := object.RagFileContext(auth.Owner, c.Input().Get("store"), fileID)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.JSON(http.StatusOK, results)
}
