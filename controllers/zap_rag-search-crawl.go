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
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"github.com/hanzoai/account"
	"github.com/luxfi/zap"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
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

// table is what a table can be READ as: everything one owner holds, a page of
// it, how many there are, and how a row is masked before it leaves.
//
// Four functions rather than four copies of the handler that calls them. The
// paging arithmetic — which page was asked for, where that page starts, how many
// there are altogether — is the part worth having in one place; it was written
// out per table and is the kind of arithmetic that is wrong quietly.
type table[T any] struct {
	all   func(owner string) ([]*T, error)
	mask  func([]*T, bool) []*T
	count func(owner, field, value string) (int64, error)
	page  func(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*T, error)
}

// listed answers a read of one table: the whole of an owner's rows when no page
// was asked for, and a page with its count when one was.
func listed[T any](c *ApiController, l table[T]) {
	owner, allowed := c.GetScopedOwner()
	if !allowed {
		return
	}
	limit, page := c.Input().Get("pageSize"), c.Input().Get("p")
	field, value := c.Input().Get("field"), c.Input().Get("value")
	sortField, sortOrder := c.Input().Get("sortField"), c.Input().Get("sortOrder")

	if limit == "" || page == "" {
		rows, err := l.all(owner)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}
		c.ResponseOk(l.mask(rows, true))
		return
	}
	n := util.ParseInt(limit)
	count, err := l.count(owner, field, value)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	paginator := util.NewPaginator(c.PageAsked(), n, count)
	rows, err := l.page(owner, paginator.Offset(), n, field, value, sortField, sortOrder)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(rows, paginator.Nums())
}

// replaced writes a row over the one the request's id names. It is stored() with
// a key: the row arrives in the body and the id says which row it replaces.
//
// `whose` is the same answer the LISTING for this table uses — GetScopedOwner
// where the table belongs to organizations, RequireSignedIn where it belongs to
// people. The reads were scoped and the writes were not, so a row could be
// replaced by naming an id outside what its own listing would ever show.
func replaced[T any](c *ApiController, whose func() (string, bool), store func(string, *T) (bool, error)) {
	mine, ok := whose()
	if !ok {
		return
	}
	id := c.Input().Get("id")
	owner, _, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	if mine != "" && owner != mine {
		c.ResponseError(fmt.Sprintf("the record: %s does not exist", id))
		return
	}
	var row T
	if err := json.Unmarshal(c.Body(), &row); err != nil {
		c.ResponseError(err.Error())
		return
	}
	ok2, err := store(id, &row)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(ok2)
}

// stored is the HTTP shape of the same thing zapWrite is over ZAP: decode the
// body into a row, hand it to the store, answer with what the store said.
//
// It is a function rather than a method because a method cannot take a type
// parameter, and the fourteen handlers that had this written out keep their own
// names and signatures — the router finds them by name, and a name is the whole
// of what it needs from them.
// `whose` is the same answer the LISTING for this table uses, so a row is written
// where that listing would look for it. It arrives as a function because the two
// answers differ per table and both already exist: GetScopedOwner for a table
// that belongs to organizations, RequireSignedIn for one that belongs to people.
func stored[T any](c *ApiController, whose func() (string, bool), store func(*T) (bool, error)) {
	mine, ok := whose()
	if !ok {
		return
	}
	var row T
	if err := json.Unmarshal(c.Body(), &row); err != nil {
		c.ResponseError(err.Error())
		return
	}
	if err := ownedBy(&row, mine); err != nil {
		c.ResponseError(err.Error())
		return
	}
	ok2, err := store(&row)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(ok2)
}

// zapWrite is the shape a "take a row and store it" handler has: a signed-in
// caller, a body that decodes to the row, and a store call that says whether it
// landed. The store call arrives as a value, so adding a table is naming one.
// `whose` names the axis, as stored() does on the other surface: theirOrg where a
// table belongs to organizations, themselves where it belongs to people. So a row
// is written where that table's listing looks for it, whichever surface filed it.
func zapWrite[T any](auth string, body []byte, whose func(*iam.User) string, store func(*T) (bool, error)) (*zap.Message, error) {
	user := zapPrincipal(auth)
	if user == nil {
		return zapError(http.StatusUnauthorized, "auth:Please sign in first")
	}
	var row T
	if err := json.Unmarshal(body, &row); err != nil {
		return zapError(http.StatusBadRequest, "invalid request: "+err.Error())
	}
	if err := ownedBy(&row, whose(user)); err != nil {
		return zapError(http.StatusOK, err.Error())
	}
	stored, err := store(&row)
	if err != nil {
		return zapError(http.StatusOK, err.Error())
	}
	return zapOk(stored)
}

// zapError is THE cloud refusal, and it says so twice: in the envelope every
// client parses, and in the response's own error slot every diagnostic reads.
//
// Five of these existed and disagreed about which of the two to fill. One filled
// neither but the slot — so an endpoint that answered success in an envelope
// answered failure with an empty body, and a client parsing {status, msg} got
// nothing at all from the one reply it most needed to read.
func zapError(status int, msg string) (*zap.Message, error) {
	b, _ := json.Marshal(Response{Status: "error", Msg: msg})
	return object.BuildCloudResponse(uint32(status), b, msg)
}

// zapRaw marshals a bare payload (endpoints that write c.Data["json"] directly,
// e.g. {hits:…}, the raw results array, or the LangChain tuple shape).
func zapRaw(data interface{}) (*zap.Message, error) {
	b, _ := json.Marshal(data)
	return object.BuildCloudResponse(http.StatusOK, b, "")
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

	// The publishable half has its own endpoint: it answers with the org holding the
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
		return zapError(aerr.status, aerr.msg)
	}

	var req object.DocSearchRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return zapError(http.StatusBadRequest, "invalid request: "+err.Error())
	}
	if req.Query == "" {
		return zapError(http.StatusBadRequest, "query must not be empty")
	}

	store, serr := zapBodyStore(sa.Owner, body, "docs-hanzo-ai")
	if serr != nil {
		return zapError(http.StatusBadRequest, serr.Error())
	}
	results, err := object.SearchDocuments(sa.Owner, store, &req, "en")
	if err != nil {
		recordSearchUsage(sa, "search-query", req.Mode, "error", 0, "")
		return zapError(http.StatusInternalServerError, err.Error())
	}

	recordSearchUsage(sa, "search-query", req.Mode, "success", len(results), "")
	return zapRaw(map[string]interface{}{"hits": results})
}

// ── /v1/index ───────────────────────────────────────────────────────────

func zapIndexHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	sa, aerr := zapRequireIndexAuth(auth)
	if aerr != nil {
		return zapError(aerr.status, aerr.msg)
	}

	var req object.DocIndexRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return zapError(http.StatusBadRequest, "invalid request: "+err.Error())
	}
	if len(req.Documents) == 0 {
		return zapError(http.StatusBadRequest, "documents must not be empty")
	}

	store, serr := zapBodyStore(sa.Owner, body, "docs-hanzo-ai")
	if serr != nil {
		return zapError(http.StatusBadRequest, serr.Error())
	}
	count, err := object.IndexDocuments(sa.Owner, store, &req, "en")
	if err != nil {
		recordSearchUsage(sa, "index-docs", "meilisearch", "error", 0, "")
		return zapError(http.StatusInternalServerError, err.Error())
	}

	recordSearchUsage(sa, "index-docs", "meilisearch", "success", count, "")
	go purgeCFCacheTag("search:" + object.GetSearchIndexName(sa.Owner, store))
	return zapOk(count)
}

// ── /v1/search/stats ────────────────────────────────────────────────────

func zapSearchStatsHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	sa, aerr := zapResolveSearchAuth(auth)
	if aerr != nil {
		return zapError(aerr.status, aerr.msg)
	}

	store, serr := zapBodyStore(sa.Owner, body, "docs-hanzo-ai")
	if serr != nil {
		return zapError(http.StatusBadRequest, serr.Error())
	}
	stats, err := object.GetDocIndexStats(sa.Owner, store)
	if err != nil {
		return zapError(http.StatusInternalServerError, err.Error())
	}
	return zapOk(stats)
}

// ── /v1/scrape (crawl-and-index) ────────────────────────────────────────

func zapScrapeHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	sa, aerr := zapRequireIndexAuth(auth)
	if aerr != nil {
		return zapError(aerr.status, aerr.msg)
	}

	var req object.ScrapeRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return zapError(http.StatusBadRequest, "invalid request: "+err.Error())
	}
	if req.URL == "" {
		return zapError(http.StatusBadRequest, "url must not be empty")
	}

	if gerr := zapBalanceGate(sa); gerr != nil {
		return zapError(gerr.status, gerr.msg)
	}

	stats, err := object.ScrapeAndIndex(sa.Owner, &req, "en")
	if err != nil {
		recordSearchUsage(sa, "scrape", "crawl", "error", 0, "")
		return zapError(http.StatusInternalServerError, err.Error())
	}

	recordSearchUsage(sa, "scrape", stats.Engine, "success", stats.PagesScraped, "")
	return zapOk(stats)
}

// ── /v1/crawl ──────────────────────────────────────────────────────────

func zapCrawlHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	sa, aerr := zapRequireIndexAuth(auth)
	if aerr != nil {
		return zapError(aerr.status, aerr.msg)
	}

	var req crawlRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return zapError(http.StatusBadRequest, "invalid request: "+err.Error())
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
		return zapError(http.StatusBadRequest, "url (or urls) must not be empty")
	}
	if len(urls) > maxCrawlURLs {
		return zapError(http.StatusBadRequest, "too many urls: at most 10 per request")
	}

	if gerr := zapBalanceGate(sa); gerr != nil {
		return zapError(gerr.status, gerr.msg)
	}

	results, err := object.Crawl(urls)
	if err != nil {
		recordSearchUsage(sa, "crawl", "crawl4ai", "error", 0, "")
		return zapError(http.StatusInternalServerError, err.Error())
	}

	recordSearchUsage(sa, "crawl", "crawl4ai", "success", len(results), "")
	return zapRaw(map[string]interface{}{"results": results})
}

// ── /v1/ai/rag/ingest ───────────────────────────────────────────────────

func zapIngestHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	sa, aerr := zapRequireIndexAuth(auth)
	if aerr != nil {
		return zapError(aerr.status, aerr.msg)
	}

	var req object.IngestRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return zapError(http.StatusBadRequest, "invalid request: "+err.Error())
	}
	store, serr := object.ResolveStore(sa.Owner, req.Store, object.DefaultDocsStore)
	if serr != nil {
		return zapError(http.StatusBadRequest, serr.Error())
	}
	req.Store = store

	// Gate external/bulk sources (github/crawl/s3) on balance; pure inline
	// "upload" is ungated, matching the the router IngestDocs handler.
	if req.Source != "" && req.Source != "upload" {
		if gerr := zapBalanceGate(sa); gerr != nil {
			return zapError(gerr.status, gerr.msg)
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
		return zapError(http.StatusInternalServerError, err.Error())
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
		return zapError(aerr.status, aerr.msg)
	}

	var req object.RagEmbedRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return zapError(http.StatusBadRequest, "invalid request: "+err.Error())
	}
	if req.FileID == "" {
		return zapError(http.StatusBadRequest, "file_id must not be empty")
	}

	result, err := object.RagEmbedFile(sa.Owner, &req, "en")
	if err != nil {
		recordSearchUsage(sa, "index-docs", "rag-embed", "error", 0, "")
		return zapError(http.StatusInternalServerError, err.Error())
	}

	recordSearchUsage(sa, "index-docs", "rag-embed", "success", result.Chunks, "")
	return zapOk(result)
}

// ── /v1/ai/rag/query  +  /v1/ai/rag/query-multiple ──────────────────────

func zapRagQueryHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	sa, aerr := zapResolveSearchAuth(auth)
	if aerr != nil {
		return zapError(aerr.status, aerr.msg)
	}

	var req object.RagQueryRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return zapError(http.StatusBadRequest, "invalid request: "+err.Error())
	}

	results, err := object.RagQuery(sa.Owner, &req, "en")
	if err != nil {
		recordSearchUsage(sa, "search-query", "rag", "error", 0, "")
		return zapError(http.StatusInternalServerError, err.Error())
	}

	recordSearchUsage(sa, "search-query", "rag", "success", len(results), "")
	return zapRaw(results)
}

// ── /v1/ai/rag/delete ───────────────────────────────────────────────────

func zapRagDeleteHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	sa, aerr := zapRequireIndexAuth(auth)
	if aerr != nil {
		return zapError(aerr.status, aerr.msg)
	}

	var b ragDeleteBody
	if err := json.Unmarshal(body, &b); err != nil {
		return zapError(http.StatusBadRequest, "invalid request: "+err.Error())
	}

	ids := b.FileIDs
	if b.FileID != "" {
		ids = append(ids, b.FileID)
	}
	if len(ids) == 0 {
		return zapError(http.StatusBadRequest, "file_id or file_ids must be provided")
	}

	deleted := 0
	for _, id := range ids {
		if id == "" {
			continue
		}
		if err := object.DeleteRagFile(sa.Owner, b.Store, id, "en"); err != nil {
			return zapError(http.StatusInternalServerError, err.Error())
		}
		deleted++
	}
	return zapOk(map[string]interface{}{"deleted": deleted})
}

// ── /v1/ai/rag/context ──────────────────────────────────────────────────

func zapRagContextHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	sa, aerr := zapResolveSearchAuth(auth)
	if aerr != nil {
		return zapError(aerr.status, aerr.msg)
	}

	var p struct {
		FileID string `json:"file_id"`
		Store  string `json:"store"`
	}
	_ = json.Unmarshal(body, &p)
	if p.FileID == "" {
		return zapError(http.StatusBadRequest, "file_id must not be empty")
	}

	results, err := object.RagFileContext(sa.Owner, p.Store, p.FileID)
	if err != nil {
		return zapError(http.StatusInternalServerError, err.Error())
	}
	return zapRaw(results)
}

// ownedBy writes whose row this is, over whatever the request said.
//
// The row is a type parameter, so the field is found by name rather than through
// an interface the nine tables would each have to implement. A type with no Owner
// to write is refused rather than stored unscoped: a table that arrives here
// without one is a table nobody decided the ownership of, and silence is how that
// would go unnoticed.
func ownedBy(row any, owner string) error {
	v := reflect.ValueOf(row)
	if v.Kind() != reflect.Ptr || v.IsNil() {
		return fmt.Errorf("cannot say who owns a %T", row)
	}
	field := v.Elem().FieldByName("Owner")
	if !field.IsValid() || field.Kind() != reflect.String || !field.CanSet() {
		return fmt.Errorf("a %T has no owner to write", row)
	}
	field.SetString(owner)
	return nil
}

// The two axes a table can belong to. Named, because "u.Owner" at a call site is
// a field and "theirOrg" is the decision. (orgOf is taken, and is a different
// thing: the owner half of an "owner/name" identity.)
func theirOrg(u *iam.User) string   { return u.Owner }
func themselves(u *iam.User) string { return u.Name }
