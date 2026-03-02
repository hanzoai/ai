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

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud/object"
)

// SearchDocs
// @Title SearchDocs
// @Tag Search Docs API
// @Description search documentation using hybrid fulltext + vector search
// @Param body body object.DocSearchRequest true "Search request"
// @Success 200 {array} object.DocSearchResult The search results (raw array, not wrapped)
// @router /search-docs [post]
func (c *ApiController) SearchDocs() {
	var req object.DocSearchRequest
	err := json.Unmarshal(c.Ctx.Input.RequestBody, &req)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	if req.Query == "" {
		c.ResponseError("query must not be empty")
		return
	}

	owner := c.resolveSearchOwner()
	store := c.resolveSearchStore()

	results, err := object.SearchDocuments(owner, store, &req, c.GetAcceptLanguage())
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	// Return raw array, NOT wrapped in Response{} envelope.
	// The frontend client expects SortedResult[] directly.
	c.Data["json"] = results
	c.ServeJSON()
}

// IndexDocs
// @Title IndexDocs
// @Tag Search Docs API
// @Description index documentation into Meilisearch and Qdrant
// @Param body body object.DocIndexRequest true "Index request"
// @Success 200 {object} controllers.Response The Response object
// @router /index-docs [post]
func (c *ApiController) IndexDocs() {
	if !c.requireIndexAuth() {
		return
	}

	var req object.DocIndexRequest
	err := json.Unmarshal(c.Ctx.Input.RequestBody, &req)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	if len(req.Documents) == 0 {
		c.ResponseError("documents must not be empty")
		return
	}

	owner := c.resolveSearchOwner()
	store := c.resolveSearchStore()

	count, err := object.IndexDocuments(owner, store, &req, c.GetAcceptLanguage())
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(count)
}

// ChatDocs
// @Title ChatDocs
// @Tag Search Docs API
// @Description RAG chat over documentation with search context
// @Param body body object.DocChatRequest true "Chat request"
// @Success 200 {stream} string "SSE stream of chat response"
// @router /chat-docs [post]
func (c *ApiController) ChatDocs() {
	var req object.DocChatRequest
	err := json.Unmarshal(c.Ctx.Input.RequestBody, &req)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	if req.Query == "" {
		c.ResponseError("query must not be empty")
		return
	}

	owner := c.resolveSearchOwner()
	store := c.resolveSearchStore()

	if req.Stream {
		c.Ctx.ResponseWriter.Header().Set("Content-Type", "text/event-stream")
		c.Ctx.ResponseWriter.Header().Set("Cache-Control", "no-cache")
		c.Ctx.ResponseWriter.Header().Set("Connection", "keep-alive")

		answer, _, err := object.GetDocChatAnswer(owner, store, &req, c.GetAcceptLanguage())
		if err != nil {
			event := fmt.Sprintf("event: myerror\ndata: %s\n\n", err.Error())
			_, _ = c.Ctx.ResponseWriter.Write([]byte(event))
			return
		}

		jsonData, err := ConvertMessageDataToJSON(answer)
		if err != nil {
			event := fmt.Sprintf("event: myerror\ndata: %s\n\n", err.Error())
			_, _ = c.Ctx.ResponseWriter.Write([]byte(event))
			return
		}

		_, _ = c.Ctx.ResponseWriter.Write([]byte(fmt.Sprintf("event: message\ndata: %s\n\n", jsonData)))
		_, _ = c.Ctx.ResponseWriter.Write([]byte("event: end\ndata: end\n\n"))
		c.Ctx.ResponseWriter.Flush()
		return
	}

	answer, _, err := object.GetDocChatAnswer(owner, store, &req, c.GetAcceptLanguage())
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(answer)
}

// SearchDocsStats
// @Title SearchDocsStats
// @Tag Search Docs API
// @Description get search index statistics
// @Success 200 {object} object.DocStatsResponse The stats response
// @router /search-docs/stats [get]
func (c *ApiController) SearchDocsStats() {
	owner := c.resolveSearchOwner()
	store := c.resolveSearchStore()

	stats, err := object.GetDocIndexStats(owner, store)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(stats)
}

// resolveSearchOwner determines the owner for search operations.
// Accepts session auth, Bearer token, pk-* publishable key, or defaults to "admin".
func (c *ApiController) resolveSearchOwner() string {
	user := c.GetSessionUser()
	if user != nil {
		return user.Owner
	}

	authHeader := c.Ctx.Request.Header.Get("Authorization")
	if authHeader != "" {
		token := strings.TrimPrefix(authHeader, "Bearer ")
		if strings.HasPrefix(token, "pk-") || strings.HasPrefix(token, "hk-") {
			return "admin"
		}
	}

	return "admin"
}

// resolveSearchStore determines the store name from the request or uses a default.
func (c *ApiController) resolveSearchStore() string {
	store := c.Input().Get("store")
	if store == "" {
		store = "docs-hanzo-ai"
	}
	return store
}

// requireIndexAuth checks that the caller has admin-level auth for write operations.
// Accepts session admin, hk-* API key, or specific service token.
func (c *ApiController) requireIndexAuth() bool {
	// Session admin
	if c.IsAdmin() {
		return true
	}

	// API key auth
	authHeader := c.Ctx.Request.Header.Get("Authorization")
	if authHeader != "" {
		token := strings.TrimPrefix(authHeader, "Bearer ")
		if strings.HasPrefix(token, "hk-") {
			return true
		}
	}

	// Preview mode allows all operations (for development)
	if c.IsPreviewMode() {
		return true
	}

	c.ResponseError(c.T("auth:this operation requires admin privilege"))
	return false
}
