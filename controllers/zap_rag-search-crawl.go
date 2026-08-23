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
// migration off the controller layer). Each handler is a pure ZAP handler
//
//	func(ctx context.Context, auth string, body []byte) (*zap.Message, error)
//
// that re-implements the controller's logic against object/ + iam — it
// NEVER wraps or transforms the controller. It mirrors zapChatHandler
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
	"rag.ingest":         zapIngestHandler,
	"rag.embed":          zapRagEmbedHandler,
	"rag.query":          zapRagQueryHandler,
	"rag.query-multiple": zapRagQueryHandler,
	"rag.delete":         zapRagDeleteHandler,
	"rag.context":        zapRagContextHandler,
	// The multipart file embed hanzo.chat posts, as a native method.
	"embed": zapRagEmbedHandler,
}

// zapRagSearchCrawlGateway maps gateway path → handler (MsgType 200). init ranges
// over this to registerGatewayPath(path, h); lookup matches longest-prefix so
// "/v1/search/stats" wins over "/v1/search" and "/v1/ai/rag/query-multiple" over
// "/v1/ai/rag/query".
var zapRagSearchCrawlGateway = map[string]zapRSCHandler{
	"/v1/search/stats":          zapSearchStatsHandler,
	"/v1/search":                zapSearchHandler,
	"/v1/index":                 zapIndexHandler,
	"/v1/crawl":                 zapCrawlHandler,
	"/v1/ai/rag/ingest":         zapIngestHandler,
	"/v1/ai/rag/embed":          zapRagEmbedHandler,
	"/v1/ai/rag/query-multiple": zapRagQueryHandler,
	"/v1/ai/rag/query":          zapRagQueryHandler,
	"/v1/ai/rag/delete":         zapRagDeleteHandler,
	"/v1/ai/rag/context":        zapRagContextHandler,
}

// ── Shared response + auth seam ─────────────────────────────────────────

// zapAuthErr carries the (status, message) of an auth rejection so the handler
// renders it with the correct code (401/403), never a silent 200.
type zapAuthErr struct {
	status int
	msg    string
}

// zapOk marshals the Response{status:"ok",data:…} envelope the c.ResponseOk
// HTTP path returns.
// zapOk is THE cloud answer: {status:"ok"} at 200, carrying whatever the handler
// has to hand back.
//
// It is variadic because the second payload is the paginator's count and most
// answers have no such thing — one call shape rather than an Ok and an Ok2 that
// differ by an argument. Nine of these existed, one per group, identical but for
// whether they wrote `switch` or `if` and `200` or http.StatusOK.
func zapOk(data ...interface{}) (*zap.Message, error) {
	resp := Response{Status: "ok"}
	if len(data) > 0 {
		resp.Data = data[0]
	}
	if len(data) > 1 {
		resp.Data2 = data[1]
	}
	b, _ := json.Marshal(resp)
	return object.BuildCloudResponse(http.StatusOK, b, "")
}

// zapError is THE cloud refusal: {status:"error", msg} at the status given.
func zapError(status int, msg string) (*zap.Message, error) {
	b, _ := json.Marshal(Response{Status: "error", Msg: msg})
	return object.BuildCloudResponse(uint32(status), b, "")
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

// zapBodyStore extracts the optional "store" field from a JSON body, falling back
// to def (the native equivalent of resolveSearchStore's query param) and refusing
// a name the caller could not have been issued — see object.ResolveStore.
func zapBodyStore(owner string, body []byte, def string) (string, error) {
	var s struct {
		Store string `json:"store"`
	}
	_ = json.Unmarshal(body, &s)
	return object.ResolveStore(owner, s.Store, def)
}

// zapResolveSearchAuth mirrors ApiController.resolveSearchAuth for the ZAP
// transport: identity from the Bearer token ONLY (no session cookie exists over
// ZAP). Read-level: sk-*/pk-* IAM keys and iss/aud-validated JWTs are accepted;
// pk-* is read-only but valid here.
func zapResolveSearchAuth(auth string) (*searchAuth, *zapAuthErr) {
	token := strings.TrimPrefix(auth, "Bearer ")
	if token == "" {
		return nil, &zapAuthErr{http.StatusUnauthorized, "authentication required: provide a Bearer token"}
	}

	if isIAMApiKey(token) {
		u, err := getUserByAccessKey(token)
		if err != nil || u == nil {
			return nil, &zapAuthErr{http.StatusUnauthorized, "API key validation failed"}
		}
		return &searchAuth{Owner: u.Owner, UserID: u.Owner + "/" + u.Name}, nil
	}

	// The publishable half has its own door: it answers with the org holding the
	// key and never a person, which is why get-user above refuses a pk-.
	if isPublishableKey(token) {
		org, err := publishableOrg(token)
		if err != nil {
			return nil, &zapAuthErr{http.StatusUnauthorized, "publishable key validation failed"}
		}
		return &searchAuth{Owner: org, UserID: org + "/publishable"}, nil
	}

	if isJwtToken(token) {
		claims, err := object.ParseAndValidateJWT(token)
		if err != nil {
			return nil, &zapAuthErr{http.StatusUnauthorized, "invalid token: " + err.Error()}
		}
		u := &claims.User
		return &searchAuth{Owner: u.Owner, UserID: u.Owner + "/" + u.Name}, nil
	}

	return nil, &zapAuthErr{http.StatusUnauthorized, "unrecognized token format: expected pk-*, sk-*, or JWT"}
}

// zapRequireIndexAuth mirrors ApiController.requireIndexAuth for ZAP: write-level
// auth for index/scrape/crawl/ingest/delete. sk-*/JWT are permitted; pk-* is a
// valid credential but read-only (403); an unknown token is denied. There is no
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
		return &searchAuth{Owner: u.Owner, UserID: u.Owner + "/" + u.Name}, nil
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
		return &searchAuth{Owner: u.Owner, UserID: u.Owner + "/" + u.Name}, nil
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

	store, serr := zapBodyStore(sa.Owner, body, "docs-hanzo-ai")
	if serr != nil {
		return zapErr(http.StatusBadRequest, serr.Error())
	}
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

	store, serr := zapBodyStore(sa.Owner, body, "docs-hanzo-ai")
	if serr != nil {
		return zapErr(http.StatusBadRequest, serr.Error())
	}
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

	store, serr := zapBodyStore(sa.Owner, body, "docs-hanzo-ai")
	if serr != nil {
		return zapErr(http.StatusBadRequest, serr.Error())
	}
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
	// (identical policy to the the router Crawl handler).
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

// ── /v1/ai/rag/ingest ───────────────────────────────────────────────────

func zapIngestHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	sa, aerr := zapRequireIndexAuth(auth)
	if aerr != nil {
		return zapErr(aerr.status, aerr.msg)
	}

	var req object.IngestRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return zapErr(http.StatusBadRequest, "invalid request: "+err.Error())
	}
	store, serr := object.ResolveStore(sa.Owner, req.Store, object.DefaultDocsStore)
	if serr != nil {
		return zapErr(http.StatusBadRequest, serr.Error())
	}
	req.Store = store

	// Gate external/bulk sources (github/crawl/s3) on balance; pure inline
	// "upload" is ungated, matching the the router IngestDocs handler.
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

// ── /v1/ai/rag/embed  +  /v1/embed (native JSON body) ───────────────────
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

// ── /v1/ai/rag/query  +  /v1/ai/rag/query-multiple ──────────────────────

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

// ── /v1/ai/rag/delete ───────────────────────────────────────────────────

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

// ── /v1/ai/rag/context ──────────────────────────────────────────────────

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
