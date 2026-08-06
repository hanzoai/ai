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

// Native ZAP handlers for the RAG / Search / Crawl route-group (strangler
// migration off beego). Each handler is a pure ZAP handler
//
//	func(ctx context.Context, auth string, body []byte) (*zap.Message, error)
//
// that re-implements the beego controller's logic against object/ + iam — it
// NEVER wraps or transforms the beego controller. It mirrors zapChatHandler
// exactly: identity is derived ONLY from the auth token (never the body), org
// scoping is the resolved principal's Owner, billing runs through the ONE
// recordSearchUsage meter, and responses marshal the SAME shape the HTTP path
// returns.
//
// Registration is self-contained in THIS file (per-group convention, zero
// shared-file write contention): the group's tables + init() below register every
// method/path into the shared registry (zap_registry.go). The same routes stay
// live on routers.App, which also backs the gateway fallback.

package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/hanzoai/account"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/luxfi/zap"

	"github.com/hanzoai/ai/object"
)

// ── Per-group registration tables (collision-free, own file) ────────────
//
// Each ZAP group publishes its OWN uniquely-named method→handler and
// path→handler tables as data; this group's init() below ranges over them into
// registerCloud / registerGatewayPath (zap_registry.go), which handleCloudService
// and the gateway dispatch consult. No group edits a shared registration file.
// The handler shape is an anonymous func alias so no shared symbol is declared
// here.

type zapRSCHandler = func(ctx context.Context, auth string, body []byte) (*zap.Message, error)

// zapRagSearchCrawlCloud maps native cloud method → handler (MsgType 100).
// init below ranges over this to registerCloud(method, h).
// init self-registers this group's dispatch tables into the canonical registry.
func init() {
	for method, h := range zapRagSearchCrawlCloud {
		registerCloud(method, h)
	}
	for path, h := range zapRagSearchCrawlGateway {
		registerGatewayPath(path, h)
	}
}

var zapRagSearchCrawlCloud = map[string]zapRSCHandler{
	"search":             zapSearchHandler,
	"search.index":       zapIndexHandler,
	"search.stats":       zapSearchStatsHandler,
	"scrape":             zapScrapeHandler,
	"scrape.preview":     zapCrawlHandler, // deprecated alias of crawl
	"crawl":              zapCrawlHandler,
	"docs.ingest":        zapIngestHandler,
	"rag.embed":          zapRagEmbedHandler,
	"rag.query":          zapRagQueryHandler,
	"rag.query-multiple": zapRagQueryHandler,
	"rag.delete":         zapRagDeleteHandler,
	"rag.context":        zapRagContextHandler,
	// LibreChat-compat native methods.
	"embed":             zapRagEmbedHandler,
	"query":             zapRagQueryCompatHandler,
	"query-multiple":    zapRagQueryCompatHandler,
	"documents.delete":  zapDocumentsDeleteHandler,
	"documents.context": zapDocumentContextHandler,
}

// zapRagSearchCrawlGateway maps gateway path → handler (MsgType 200). init ranges
// over this to registerGatewayPath(path, h); lookup matches longest-prefix so
// "/v1/search/stats" wins over "/v1/search" and "/v1/rag/query-multiple" over
// "/v1/rag/query".
var zapRagSearchCrawlGateway = map[string]zapRSCHandler{
	"/v1/search/stats":       zapSearchStatsHandler,
	"/v1/search":             zapSearchHandler,
	"/v1/index":              zapIndexHandler,
	"/v1/crawl":              zapCrawlHandler,
	"/v1/docs/ingest":        zapIngestHandler,
	"/v1/rag/embed":          zapRagEmbedHandler,
	"/v1/rag/query-multiple": zapRagQueryHandler,
	"/v1/rag/query":          zapRagQueryHandler,
	"/v1/rag/delete":         zapRagDeleteHandler,
	"/v1/rag/context":        zapRagContextHandler,
	"/v1/embed":              zapRagEmbedHandler,
	"/v1/query_multiple":     zapRagQueryCompatHandler,
	"/v1/query":              zapRagQueryCompatHandler,
	// "/v1/documents" and "/v1/documents/{id}/context" share a prefix; the
	// body-shape router picks delete (JSON array) vs context (JSON object).
	"/v1/documents": zapDocumentsGatewayHandler,
}

// ── Shared response + auth seam ─────────────────────────────────────────

// zapAuthErr carries the (status, message) of an auth rejection so the handler
// renders it with the correct code (401/403), never a silent 200.
type zapAuthErr struct {
	status int
	msg    string
}

// zapOk marshals the beego Response{status:"ok",data:…} envelope the c.ResponseOk
// HTTP path returns.
func zapOk(data interface{}) (*zap.Message, error) {
	b, _ := json.Marshal(Response{Status: "ok", Data: data})
	return object.BuildCloudResponse(http.StatusOK, b, "")
}

// zapRaw marshals a bare payload (endpoints that write c.Data["json"] directly,
// e.g. {hits:…}, the raw results array, or the LangChain tuple shape).
func zapRaw(data interface{}) (*zap.Message, error) {
	b, _ := json.Marshal(data)
	return object.BuildCloudResponse(http.StatusOK, b, "")
}

func zapErr(status int, msg string) (*zap.Message, error) {
	return object.BuildCloudResponse(uint32(status), nil, msg)
}

// zapBodyStore extracts the optional "store" field from a JSON body, falling
// back to def (the native equivalent of resolveSearchStore's query param).
func zapBodyStore(body []byte, def string) string {
	var s struct {
		Store string `json:"store"`
	}
	_ = json.Unmarshal(body, &s)
	if s.Store == "" {
		return def
	}
	return s.Store
}

// zapResolveSearchAuth mirrors ApiController.resolveSearchAuth for the ZAP
// transport: identity from the Bearer token ONLY (no session cookie exists over
// ZAP). Read-level: pk-*/sk-* IAM keys, hz_* widget keys, and iss/aud-validated
// JWTs are all accepted; pk-* is read-only but valid here.
func zapResolveSearchAuth(auth string) (*searchAuth, *zapAuthErr) {
	token := strings.TrimPrefix(auth, "Bearer ")
	if token == "" {
		return nil, &zapAuthErr{http.StatusUnauthorized, "authentication required: provide a Bearer token"}
	}

	if isIAMApiKey(token) || isPublishableKey(token) {
		u, err := getUserByAccessKey(token)
		if err != nil || u == nil {
			return nil, &zapAuthErr{http.StatusUnauthorized, "API key validation failed"}
		}
		return &searchAuth{Owner: u.Owner, UserID: u.Owner + "/" + u.Name, User: u}, nil
	}

	if isWidgetKey(token) {
		if !validateWidgetKey(token) {
			return nil, &zapAuthErr{http.StatusUnauthorized, "invalid widget key"}
		}
		owner := widgetKeyOwner(token)
		return &searchAuth{Owner: owner, UserID: owner + "/widget", User: &iam.User{Owner: owner, Name: "widget", Type: "widget-key"}}, nil
	}

	if isJwtToken(token) {
		claims, err := object.ParseAndValidateJWT(token)
		if err != nil {
			return nil, &zapAuthErr{http.StatusUnauthorized, "invalid token: " + err.Error()}
		}
		u := &claims.User
		return &searchAuth{Owner: u.Owner, UserID: u.Owner + "/" + u.Name, User: u}, nil
	}

	return nil, &zapAuthErr{http.StatusUnauthorized, "unrecognized token format: expected pk-*, sk-*, or JWT"}
}

// zapRequireIndexAuth mirrors ApiController.requireIndexAuth for ZAP: write-level
// auth for index/scrape/crawl/ingest/delete. sk-*/JWT are permitted; pk-* is a
// valid credential but read-only (403); widget/unknown are denied. There is no
// session-admin or preview-mode fallback over ZAP — a no-credential caller must
// never reach the admin tenant's index.
func zapRequireIndexAuth(auth string) (*searchAuth, *zapAuthErr) {
	token := strings.TrimPrefix(auth, "Bearer ")
	if token == "" {
		return nil, &zapAuthErr{http.StatusUnauthorized, "this operation requires write authorization"}
	}

	if isIAMApiKey(token) {
		u, err := getUserByAccessKey(token)
		if err != nil || u == nil {
			return nil, &zapAuthErr{http.StatusUnauthorized, "API key validation failed"}
		}
		return &searchAuth{Owner: u.Owner, UserID: u.Owner + "/" + u.Name, User: u}, nil
	}

	if isPublishableKey(token) {
		return nil, &zapAuthErr{http.StatusForbidden, "publishable keys (pk-*) cannot perform write operations"}
	}

	if isJwtToken(token) {
		claims, err := object.ParseAndValidateJWT(token)
		if err != nil {
			return nil, &zapAuthErr{http.StatusUnauthorized, "invalid token: " + err.Error()}
		}
		u := &claims.User
		return &searchAuth{Owner: u.Owner, UserID: u.Owner + "/" + u.Name, User: u}, nil
	}

	return nil, &zapAuthErr{http.StatusUnauthorized, "this operation requires write authorization"}
}

// zapBalanceGate mirrors the belt-and-suspenders balance check in the scrape /
// crawl / ingest HTTP handlers: reject when the billing subject within the org
// namespace has no funds. Returns nil when funded or unverifiable (fail-open on
// a lookup error, exactly like the HTTP path).
func zapBalanceGate(auth *searchAuth) *zapAuthErr {
	if auth.Owner == "" {
		return nil
	}
	balance, err := getUserBalance(account.PayerOf(auth.Owner, auth.UserID).Subject(), auth.Owner)
	if err == nil && balance <= 0 {
		return &zapAuthErr{http.StatusPaymentRequired, "insufficient balance for this operation. Add funds at https://hanzo.ai/billing"}
	}
	return nil
}

// ── /v1/search ──────────────────────────────────────────────────────────

func zapSearchHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	sa, aerr := zapResolveSearchAuth(auth)
	if aerr != nil {
		return zapErr(aerr.status, aerr.msg)
	}

	var req object.DocSearchRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return zapErr(http.StatusBadRequest, "invalid request: "+err.Error())
	}
	if req.Query == "" {
		return zapErr(http.StatusBadRequest, "query must not be empty")
	}

	store := zapBodyStore(body, "docs-hanzo-ai")
	results, err := object.SearchDocuments(sa.Owner, store, &req, "en")
	if err != nil {
		recordSearchUsage(sa, "search-query", req.Mode, "error", 0, "")
		return zapErr(http.StatusInternalServerError, err.Error())
	}

	recordSearchUsage(sa, "search-query", req.Mode, "success", len(results), "")
	return zapRaw(map[string]interface{}{"hits": results})
}

// ── /v1/index ───────────────────────────────────────────────────────────

func zapIndexHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	sa, aerr := zapRequireIndexAuth(auth)
	if aerr != nil {
		return zapErr(aerr.status, aerr.msg)
	}

	var req object.DocIndexRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return zapErr(http.StatusBadRequest, "invalid request: "+err.Error())
	}
	if len(req.Documents) == 0 {
		return zapErr(http.StatusBadRequest, "documents must not be empty")
	}

	store := zapBodyStore(body, "docs-hanzo-ai")
	count, err := object.IndexDocuments(sa.Owner, store, &req, "en")
	if err != nil {
		recordSearchUsage(sa, "index-docs", "meilisearch", "error", 0, "")
		return zapErr(http.StatusInternalServerError, err.Error())
	}

	recordSearchUsage(sa, "index-docs", "meilisearch", "success", count, "")
	go purgeCFCacheTag("search:" + object.GetSearchIndexName(sa.Owner, store))
	return zapOk(count)
}

// ── /v1/search/stats ────────────────────────────────────────────────────

func zapSearchStatsHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	sa, aerr := zapResolveSearchAuth(auth)
	if aerr != nil {
		return zapErr(aerr.status, aerr.msg)
	}

	store := zapBodyStore(body, "docs-hanzo-ai")
	stats, err := object.GetDocIndexStats(sa.Owner, store)
	if err != nil {
		return zapErr(http.StatusInternalServerError, err.Error())
	}
	return zapOk(stats)
}

// ── /v1/scrape (crawl-and-index) ────────────────────────────────────────

func zapScrapeHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	sa, aerr := zapRequireIndexAuth(auth)
	if aerr != nil {
		return zapErr(aerr.status, aerr.msg)
	}

	var req object.ScrapeRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return zapErr(http.StatusBadRequest, "invalid request: "+err.Error())
	}
	if req.URL == "" {
		return zapErr(http.StatusBadRequest, "url must not be empty")
	}

	if gerr := zapBalanceGate(sa); gerr != nil {
		return zapErr(gerr.status, gerr.msg)
	}

	stats, err := object.ScrapeAndIndex(sa.Owner, &req, "en")
	if err != nil {
		recordSearchUsage(sa, "scrape", "crawl", "error", 0, "")
		return zapErr(http.StatusInternalServerError, err.Error())
	}

	recordSearchUsage(sa, "scrape", stats.Engine, "success", stats.PagesScraped, "")
	return zapOk(stats)
}

// ── /v1/crawl ──────────────────────────────────────────────────────────

func zapCrawlHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	sa, aerr := zapRequireIndexAuth(auth)
	if aerr != nil {
		return zapErr(aerr.status, aerr.msg)
	}

	var req crawlRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return zapErr(http.StatusBadRequest, "invalid request: "+err.Error())
	}

	// Merge single `url` and batch `urls` into one deduplicated, non-empty list
	// (identical policy to the beego Crawl handler).
	urls := make([]string, 0, len(req.Urls)+1)
	seen := map[string]struct{}{}
	for _, u := range append([]string{req.URL}, req.Urls...) {
		if u == "" {
			continue
		}
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		urls = append(urls, u)
	}
	if len(urls) == 0 {
		return zapErr(http.StatusBadRequest, "url (or urls) must not be empty")
	}
	if len(urls) > maxCrawlURLs {
		return zapErr(http.StatusBadRequest, "too many urls: at most 10 per request")
	}

	if gerr := zapBalanceGate(sa); gerr != nil {
		return zapErr(gerr.status, gerr.msg)
	}

	results, err := object.Crawl(urls)
	if err != nil {
		recordSearchUsage(sa, "crawl", "crawl4ai", "error", 0, "")
		return zapErr(http.StatusInternalServerError, err.Error())
	}

	recordSearchUsage(sa, "crawl", "crawl4ai", "success", len(results), "")
	return zapRaw(map[string]interface{}{"results": results})
}

// ── /v1/docs/ingest ─────────────────────────────────────────────────────

func zapIngestHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	sa, aerr := zapRequireIndexAuth(auth)
	if aerr != nil {
		return zapErr(aerr.status, aerr.msg)
	}

	var req object.IngestRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return zapErr(http.StatusBadRequest, "invalid request: "+err.Error())
	}

	// Gate external/bulk sources (github/crawl/s3) on balance; pure inline
	// "upload" is ungated, matching the beego IngestDocs handler.
	if req.Source != "" && req.Source != "upload" {
		if gerr := zapBalanceGate(sa); gerr != nil {
			return zapErr(gerr.status, gerr.msg)
		}
	}

	// A long source (github/crawl/s3) is enqueued as a durable tasks workflow;
	// any enqueue failure falls through to inline so ingest ALWAYS works.
	if object.IsAsyncIngestSource(req.Source) {
		wfID, err := object.EnqueueIngest(ctx, sa.Owner, &req, "en")
		if err == nil {
			recordSearchUsage(sa, "index-docs", req.Source, "enqueued", 0, "")
			return zapOk(&object.IngestStats{
				Source:     req.Source,
				Store:      req.Store,
				IndexName:  object.GetSearchIndexName(sa.Owner, req.Store),
				Async:      true,
				WorkflowID: wfID,
			})
		}
		// fall through to inline ingest
	}

	stats, err := object.IngestSource(sa.Owner, &req, "en")
	if err != nil {
		recordSearchUsage(sa, "index-docs", req.Source, "error", 0, "")
		return zapErr(http.StatusInternalServerError, err.Error())
	}

	recordSearchUsage(sa, "index-docs", req.Source, "success", stats.DocumentsIndexed, "")
	go purgeCFCacheTag("search:" + stats.IndexName)
	return zapOk(stats)
}

// ── /v1/rag/embed  +  /v1/embed (native JSON body) ──────────────────────
//
// The native/JSON embed path: file_id + inline content or a url to fetch+parse.
// (The LibreChat multipart-file upload used by hanzo.chat is served by routers.App;
// the gateway registry carries no multipart seam. This handler serves the JSON
// contract; a multipart body carries no JSON file_id and returns 400.)

func zapRagEmbedHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	sa, aerr := zapRequireIndexAuth(auth)
	if aerr != nil {
		return zapErr(aerr.status, aerr.msg)
	}

	var req object.RagEmbedRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return zapErr(http.StatusBadRequest, "invalid request: "+err.Error())
	}
	if req.FileID == "" {
		return zapErr(http.StatusBadRequest, "file_id must not be empty")
	}

	result, err := object.RagEmbedFile(sa.Owner, &req, "en")
	if err != nil {
		recordSearchUsage(sa, "index-docs", "rag-embed", "error", 0, "")
		return zapErr(http.StatusInternalServerError, err.Error())
	}

	recordSearchUsage(sa, "index-docs", "rag-embed", "success", result.Chunks, "")
	return zapOk(result)
}

// ── /v1/rag/query  +  /v1/rag/query-multiple ────────────────────────────

func zapRagQueryHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	sa, aerr := zapResolveSearchAuth(auth)
	if aerr != nil {
		return zapErr(aerr.status, aerr.msg)
	}

	var req object.RagQueryRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return zapErr(http.StatusBadRequest, "invalid request: "+err.Error())
	}

	results, err := object.RagQuery(sa.Owner, &req, "en")
	if err != nil {
		recordSearchUsage(sa, "search-query", "rag", "error", 0, "")
		return zapErr(http.StatusInternalServerError, err.Error())
	}

	recordSearchUsage(sa, "search-query", "rag", "success", len(results), "")
	return zapRaw(results)
}

// ── /v1/rag/delete ──────────────────────────────────────────────────────

func zapRagDeleteHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	sa, aerr := zapRequireIndexAuth(auth)
	if aerr != nil {
		return zapErr(aerr.status, aerr.msg)
	}

	var b ragDeleteBody
	if err := json.Unmarshal(body, &b); err != nil {
		return zapErr(http.StatusBadRequest, "invalid request: "+err.Error())
	}

	ids := b.FileIDs
	if b.FileID != "" {
		ids = append(ids, b.FileID)
	}
	if len(ids) == 0 {
		return zapErr(http.StatusBadRequest, "file_id or file_ids must be provided")
	}

	deleted := 0
	for _, id := range ids {
		if id == "" {
			continue
		}
		if err := object.DeleteRagFile(sa.Owner, b.Store, id, "en"); err != nil {
			return zapErr(http.StatusInternalServerError, err.Error())
		}
		deleted++
	}
	return zapOk(map[string]interface{}{"deleted": deleted})
}

// ── /v1/rag/context ─────────────────────────────────────────────────────

func zapRagContextHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	sa, aerr := zapResolveSearchAuth(auth)
	if aerr != nil {
		return zapErr(aerr.status, aerr.msg)
	}

	var p struct {
		FileID string `json:"file_id"`
		Store  string `json:"store"`
	}
	_ = json.Unmarshal(body, &p)
	if p.FileID == "" {
		return zapErr(http.StatusBadRequest, "file_id must not be empty")
	}

	results, err := object.RagFileContext(sa.Owner, p.Store, p.FileID)
	if err != nil {
		return zapErr(http.StatusInternalServerError, err.Error())
	}
	return zapRaw(results)
}

// ── LibreChat-compat: /v1/query, /v1/query_multiple ─────────────────────
// Same retrieval path as /v1/rag/query, but rendered as the LangChain
// [[{page_content,metadata},score],…] tuple shape hanzo.chat's RAG client parses.

func zapRagQueryCompatHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	sa, aerr := zapResolveSearchAuth(auth)
	if aerr != nil {
		return zapErr(aerr.status, aerr.msg)
	}

	var req object.RagQueryRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return zapErr(http.StatusBadRequest, "invalid request: "+err.Error())
	}

	results, err := object.RagQuery(sa.Owner, &req, "en")
	if err != nil {
		recordSearchUsage(sa, "search-query", "rag", "error", 0, "")
		return zapErr(http.StatusInternalServerError, err.Error())
	}

	recordSearchUsage(sa, "search-query", "rag", "success", len(results), "")
	return zapRaw(lcTuples(results))
}

// ── LibreChat-compat: DELETE /v1/documents  (JSON array of file_ids) ─────

func zapDocumentsDeleteHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	sa, aerr := zapRequireIndexAuth(auth)
	if aerr != nil {
		return zapErr(aerr.status, aerr.msg)
	}

	var fileIDs []string
	if err := json.Unmarshal(body, &fileIDs); err != nil {
		return zapErr(http.StatusBadRequest, "invalid request: "+err.Error())
	}
	if len(fileIDs) == 0 {
		return zapErr(http.StatusBadRequest, "expected a non-empty array of file_ids")
	}

	deleted := 0
	for _, id := range fileIDs {
		if id == "" {
			continue
		}
		if err := object.DeleteRagFile(sa.Owner, "", id, "en"); err != nil {
			return zapErr(http.StatusInternalServerError, err.Error())
		}
		deleted++
	}
	return zapRaw(map[string]interface{}{
		"message": "Documents deleted successfully",
		"deleted": deleted,
	})
}

// ── LibreChat-compat: GET /v1/documents/:file_id/context ────────────────
// Native body carries {file_id[,store]}; returns []lcDocument.

func zapDocumentContextHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	sa, aerr := zapResolveSearchAuth(auth)
	if aerr != nil {
		return zapErr(aerr.status, aerr.msg)
	}

	var p struct {
		FileID string `json:"file_id"`
		Store  string `json:"store"`
	}
	_ = json.Unmarshal(body, &p)
	if p.FileID == "" {
		return zapErr(http.StatusBadRequest, "file_id must not be empty")
	}

	results, err := object.RagFileContext(sa.Owner, p.Store, p.FileID)
	if err != nil {
		return zapErr(http.StatusInternalServerError, err.Error())
	}
	docs := make([]lcDocument, 0, len(results))
	for _, r := range results {
		docs = append(docs, lcResultToDocument(r))
	}
	return zapRaw(docs)
}

// zapDocumentsGatewayHandler routes the shared "/v1/documents" gateway prefix:
// a bare "/v1/documents" is the DELETE-by-array endpoint; a
// "/v1/documents/{id}/context" sub-path is the full-context read. The gateway
// carries the request method/path, but this file's handler signature is
// body-only, so we disambiguate by body shape (a JSON array → delete; a JSON
// object with file_id → context). Registering the two concrete methods
// (documents.delete / documents.context) on the HTTP-shaped registry
// (registerGatewayRoute) would make the body sniff unnecessary.
func zapDocumentsGatewayHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "[") {
		return zapDocumentsDeleteHandler(ctx, auth, body)
	}
	return zapDocumentContextHandler(ctx, auth, body)
}
