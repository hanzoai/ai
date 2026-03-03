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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/beego/beego/logs"
	"github.com/hanzoai/cloud/conf"
	"github.com/hanzoai/cloud/object"
	iamsdk "github.com/hanzoid/go-sdk/casdoorsdk"
)

// searchAuth holds the validated identity for a search API request.
// Every search/index/scrape endpoint must obtain this before processing.
type searchAuth struct {
	Owner  string       // organization that owns the search index
	UserID string       // "owner/name" format for billing
	User   *iamsdk.User // nil for session-only auth without IAM lookup
}

// resolveSearchAuth validates the caller's identity via session, JWT, or IAM API key.
// Returns nil and sends an HTTP 401 error if authentication fails.
// This replaces the old resolveSearchOwner() which returned "admin" for all key types.
func (c *ApiController) resolveSearchAuth() *searchAuth {
	// 1. Session auth (highest trust -- user already authenticated via IAM SSO)
	user := c.GetSessionUser()
	if user != nil {
		return &searchAuth{
			Owner:  user.Owner,
			UserID: user.Owner + "/" + user.Name,
			User:   user,
		}
	}

	// 2. Bearer token auth
	authHeader := c.Ctx.Request.Header.Get("Authorization")
	if authHeader == "" {
		c.ResponseError("authentication required: provide a session cookie or Bearer token")
		return nil
	}

	token := strings.TrimPrefix(authHeader, "Bearer ")
	if token == "" || token == authHeader {
		c.ResponseError("invalid Authorization header: expected 'Bearer <token>'")
		return nil
	}

	// 3. IAM API key (hk-*) -- validate via IAM and resolve owner
	if isIAMApiKey(token) {
		iamUser, err := getUserByAccessKey(token)
		if err != nil {
			logs.Warning("search auth: hk-* key validation failed: %s", err.Error())
			c.ResponseError("API key validation failed")
			return nil
		}
		if iamUser == nil {
			c.ResponseError("invalid API key")
			return nil
		}
		return &searchAuth{
			Owner:  iamUser.Owner,
			UserID: iamUser.Owner + "/" + iamUser.Name,
			User:   iamUser,
		}
	}

	// 4. Publishable key (pk-*) -- validate via IAM (read-only access)
	if isPublishableKey(token) {
		iamUser, err := getUserByAccessKey(token)
		if err != nil {
			logs.Warning("search auth: pk-* key validation failed: %s", err.Error())
			c.ResponseError("publishable key validation failed")
			return nil
		}
		if iamUser == nil {
			c.ResponseError("invalid publishable key")
			return nil
		}
		return &searchAuth{
			Owner:  iamUser.Owner,
			UserID: iamUser.Owner + "/" + iamUser.Name,
			User:   iamUser,
		}
	}

	// 5. JWT token -- validate via IAM OIDC
	if isJwtToken(token) {
		claims, err := iamsdk.ParseJwtToken(token)
		if err != nil {
			c.ResponseError("invalid token: " + err.Error())
			return nil
		}
		jwtUser := &claims.User
		return &searchAuth{
			Owner:  jwtUser.Owner,
			UserID: jwtUser.Owner + "/" + jwtUser.Name,
			User:   jwtUser,
		}
	}

	c.ResponseError("unrecognized token format: expected hk-*, pk-*, or JWT")
	return nil
}

// resolveSearchStore determines the store name from the query parameter or request body field.
// The store is scoped to the authenticated owner's namespace via GetSearchIndexName.
func (c *ApiController) resolveSearchStore() string {
	store := c.Input().Get("store")
	if store == "" {
		store = "docs-hanzo-ai"
	}
	return store
}

// requireIndexAuth checks that the caller has write-level auth for index/scrape operations.
// Returns the validated searchAuth on success, or nil (with HTTP error sent) on failure.
// Publishable keys (pk-*) are rejected since they are read-only.
func (c *ApiController) requireIndexAuth() *searchAuth {
	// Session admin has full access
	if c.IsAdmin() {
		user := c.GetSessionUser()
		return &searchAuth{
			Owner:  user.Owner,
			UserID: user.Owner + "/" + user.Name,
			User:   user,
		}
	}

	// Bearer token auth
	authHeader := c.Ctx.Request.Header.Get("Authorization")
	if authHeader != "" {
		token := strings.TrimPrefix(authHeader, "Bearer ")

		// hk-* API keys: validate via IAM
		if isIAMApiKey(token) {
			iamUser, err := getUserByAccessKey(token)
			if err != nil {
				logs.Warning("index auth: hk-* key validation failed: %s", err.Error())
				c.ResponseError("API key validation failed")
				return nil
			}
			if iamUser == nil {
				c.ResponseError("invalid API key")
				return nil
			}
			return &searchAuth{
				Owner:  iamUser.Owner,
				UserID: iamUser.Owner + "/" + iamUser.Name,
				User:   iamUser,
			}
		}

		// pk-* publishable keys cannot write
		if isPublishableKey(token) {
			c.ResponseError("publishable keys (pk-*) cannot perform write operations")
			return nil
		}

		// JWT token: validate via IAM OIDC
		if isJwtToken(token) {
			claims, err := iamsdk.ParseJwtToken(token)
			if err != nil {
				c.ResponseError("invalid token: " + err.Error())
				return nil
			}
			jwtUser := &claims.User
			return &searchAuth{
				Owner:  jwtUser.Owner,
				UserID: jwtUser.Owner + "/" + jwtUser.Name,
				User:   jwtUser,
			}
		}
	}

	// Preview mode allows all operations (for development)
	if c.IsPreviewMode() {
		return &searchAuth{
			Owner:  "admin",
			UserID: "admin/admin",
		}
	}

	c.ResponseError(c.T("auth:this operation requires admin privilege"))
	return nil
}

// recordSearchUsage sends a usage record to Commerce for search/scrape/chat-docs operations.
// This follows the same fire-and-forget pattern as recordUsage() in openai_api.go.
func recordSearchUsage(auth *searchAuth, model, provider, status string, units int, clientIP string) {
	// Calculate cost in cents based on operation type
	var costCents int64
	switch model {
	case "search-query":
		costCents = 0 // Search queries are included (Meilisearch cost is fixed infra)
	case "search-chat":
		costCents = 1 // $0.01 per RAG chat session (LLM inference cost)
	case "scrape":
		costCents = int64(units) // $0.01 per page scraped
	case "index-docs":
		costCents = 0 // Indexing is included (part of write operation cost)
	}

	record := &usageRecord{
		Owner:        auth.Owner,
		User:         auth.UserID,
		Organization: auth.Owner,
		Model:        model,
		Provider:     provider,
		TotalTokens:  units,
		Cost:         float64(costCents) / 100.0,
		Currency:     "USD",
		Status:       status,
		ClientIP:     clientIP,
	}

	recordUsage(record)
}

// purgeCFCacheTag purges Cloudflare edge cache entries matching the given tag.
// It requires cfZoneId and cfApiToken to be configured (via env or app.conf).
// The purge runs synchronously; callers should invoke this in a goroutine to avoid
// blocking the HTTP response.
func purgeCFCacheTag(tag string) {
	zoneID := conf.GetConfigString("cfZoneId")
	apiToken := conf.GetConfigString("cfApiToken")
	if zoneID == "" || apiToken == "" {
		return
	}

	purgeURL := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/purge_cache", zoneID)
	body := fmt.Sprintf(`{"tags":[%q]}`, tag)

	req, err := http.NewRequest(http.MethodPost, purgeURL, bytes.NewBufferString(body))
	if err != nil {
		logs.Warning("cf cache purge: failed to build request: %v", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+apiToken)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		logs.Warning("cf cache purge: request failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		logs.Warning("cf cache purge: returned %d: %s", resp.StatusCode, string(respBody))
	}
}

// SearchDocs
// @Title SearchDocs
// @Tag Search Docs API
// @Description search documentation using hybrid fulltext + vector search
// @Param body body object.DocSearchRequest true "Search request"
// @Success 200 {array} object.DocSearchResult The search results (raw array, not wrapped)
// @router /search-docs [post]
func (c *ApiController) SearchDocs() {
	auth := c.resolveSearchAuth()
	if auth == nil {
		return
	}

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

	store := c.resolveSearchStore()

	results, err := object.SearchDocuments(auth.Owner, store, &req, c.GetAcceptLanguage())
	if err != nil {
		recordSearchUsage(auth, "search-query", req.Mode, "error", 0, c.Ctx.Request.RemoteAddr)
		c.ResponseError(err.Error())
		return
	}

	recordSearchUsage(auth, "search-query", req.Mode, "success", len(results), c.Ctx.Request.RemoteAddr)

	// Cloudflare edge cache headers: 5 min browser cache, 24h edge cache.
	indexName := object.GetSearchIndexName(auth.Owner, store)
	c.Ctx.ResponseWriter.Header().Set("Cache-Control", "public, max-age=300, s-maxage=86400")
	c.Ctx.ResponseWriter.Header().Set("CF-Cache-Tag", "search:"+indexName)
	c.Ctx.ResponseWriter.Header().Set("Vary", "Accept-Encoding, Authorization")

	// Return {hits: [...]} envelope matching the TypeScript client's HanzoSearchResponse type.
	c.Data["json"] = map[string]interface{}{"hits": results}
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
	auth := c.requireIndexAuth()
	if auth == nil {
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

	store := c.resolveSearchStore()

	count, err := object.IndexDocuments(auth.Owner, store, &req, c.GetAcceptLanguage())
	if err != nil {
		recordSearchUsage(auth, "index-docs", "meilisearch", "error", 0, c.Ctx.Request.RemoteAddr)
		c.ResponseError(err.Error())
		return
	}

	recordSearchUsage(auth, "index-docs", "meilisearch", "success", count, c.Ctx.Request.RemoteAddr)

	// Purge Cloudflare edge cache for this search index so stale results
	// are not served after re-indexing. Runs async to avoid blocking.
	indexName := object.GetSearchIndexName(auth.Owner, store)
	go purgeCFCacheTag("search:" + indexName)

	c.ResponseOk(count)
}

// aiSDKMessage is a single message in the AI SDK useChat request format.
type aiSDKMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// aiSDKRequest is the request body sent by the AI SDK DefaultChatTransport.
type aiSDKRequest struct {
	Messages []aiSDKMessage `json:"messages"`
}

// ChatDocs
// @Title ChatDocs
// @Tag Search Docs API
// @Description RAG chat over documentation with search context
// @Param body body object.DocChatRequest true "Chat request"
// @Success 200 {stream} string "SSE stream of chat response or AI SDK data stream"
// @router /chat-docs [post]
func (c *ApiController) ChatDocs() {
	auth := c.resolveSearchAuth()
	if auth == nil {
		return
	}

	store := c.resolveSearchStore()
	lang := c.GetAcceptLanguage()

	// Detect AI SDK request format (has "messages" array from useChat hook).
	var aiReq aiSDKRequest
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, &aiReq); err == nil && len(aiReq.Messages) > 0 {
		c.chatDocsAISDK(auth, store, lang, aiReq)
		return
	}

	// Native format: { query, tag, stream }
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

	if req.Stream {
		c.Ctx.ResponseWriter.Header().Set("Content-Type", "text/event-stream")
		c.Ctx.ResponseWriter.Header().Set("Cache-Control", "no-cache")
		c.Ctx.ResponseWriter.Header().Set("Connection", "keep-alive")

		answer, modelResult, err := object.GetDocChatAnswer(auth.Owner, store, &req, lang)
		if err != nil {
			recordSearchUsage(auth, "search-chat", "rag", "error", 0, c.Ctx.Request.RemoteAddr)
			event := fmt.Sprintf("event: myerror\ndata: %s\n\n", err.Error())
			_, _ = c.Ctx.ResponseWriter.Write([]byte(event))
			return
		}

		tokenCount := 0
		if modelResult != nil {
			tokenCount = modelResult.TotalTokenCount
		}
		recordSearchUsage(auth, "search-chat", "rag", "success", tokenCount, c.Ctx.Request.RemoteAddr)

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

	answer, modelResult, err := object.GetDocChatAnswer(auth.Owner, store, &req, lang)
	if err != nil {
		recordSearchUsage(auth, "search-chat", "rag", "error", 0, c.Ctx.Request.RemoteAddr)
		c.ResponseError(err.Error())
		return
	}

	tokenCount := 0
	if modelResult != nil {
		tokenCount = modelResult.TotalTokenCount
	}
	recordSearchUsage(auth, "search-chat", "rag", "success", tokenCount, c.Ctx.Request.RemoteAddr)

	c.ResponseOk(answer)
}

// chatDocsAISDK handles the AI SDK useChat data stream protocol.
// Request: { "messages": [{ "role": "user", "content": "..." }] }
// Response: AI SDK data stream format (0:"text"\n d:{...}\n)
func (c *ApiController) chatDocsAISDK(auth *searchAuth, store, lang string, aiReq aiSDKRequest) {
	// Extract query from the last user message.
	var query string
	for i := len(aiReq.Messages) - 1; i >= 0; i-- {
		if aiReq.Messages[i].Role == "user" {
			query = aiReq.Messages[i].Content
			break
		}
	}
	if query == "" {
		c.writeAISDKError("no user message found")
		return
	}

	chatReq := &object.DocChatRequest{Query: query}
	answer, modelResult, err := object.GetDocChatAnswer(auth.Owner, store, chatReq, lang)
	if err != nil {
		recordSearchUsage(auth, "search-chat", "rag", "error", 0, c.Ctx.Request.RemoteAddr)
		c.writeAISDKError(err.Error())
		return
	}

	tokenCount := 0
	if modelResult != nil {
		tokenCount = modelResult.TotalTokenCount
	}
	recordSearchUsage(auth, "search-chat", "rag", "success", tokenCount, c.Ctx.Request.RemoteAddr)

	// Write AI SDK data stream protocol response.
	c.Ctx.ResponseWriter.Header().Set("Content-Type", "text/plain; charset=utf-8")
	c.Ctx.ResponseWriter.Header().Set("X-Vercel-AI-Data-Stream", "v1")
	c.Ctx.ResponseWriter.Header().Set("Cache-Control", "no-cache")

	// Text delta (type 0): the full answer as a single chunk.
	escaped, _ := json.Marshal(answer)
	_, _ = c.Ctx.ResponseWriter.Write([]byte(fmt.Sprintf("0:%s\n", string(escaped))))

	// Finish step (type d).
	_, _ = c.Ctx.ResponseWriter.Write([]byte("d:{\"finishReason\":\"stop\"}\n"))

	c.Ctx.ResponseWriter.Flush()
}

// writeAISDKError writes an error in the AI SDK data stream protocol format.
func (c *ApiController) writeAISDKError(msg string) {
	c.Ctx.ResponseWriter.Header().Set("Content-Type", "text/plain; charset=utf-8")
	c.Ctx.ResponseWriter.Header().Set("X-Vercel-AI-Data-Stream", "v1")
	escaped, _ := json.Marshal(msg)
	_, _ = c.Ctx.ResponseWriter.Write([]byte(fmt.Sprintf("3:%s\n", string(escaped))))
	c.Ctx.ResponseWriter.Flush()
}

// SearchDocsStats
// @Title SearchDocsStats
// @Tag Search Docs API
// @Description get search index statistics
// @Success 200 {object} object.DocStatsResponse The stats response
// @router /search-docs/stats [get]
func (c *ApiController) SearchDocsStats() {
	auth := c.resolveSearchAuth()
	if auth == nil {
		return
	}

	store := c.resolveSearchStore()

	stats, err := object.GetDocIndexStats(auth.Owner, store)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(stats)
}
