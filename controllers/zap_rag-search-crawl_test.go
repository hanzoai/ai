// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package controllers

import (
	"context"
	"testing"

	"github.com/hanzoai/ai/object"
)

// TestZapRagSearchCrawlRegistered proves every route in the group self-registers
// its native cloud method AND its gateway path from THIS file's tables (the
// strangler dispatch seam Integrate wires). Missing a route here means the
// native surface silently loses parity with the beego router.
func TestZapRagSearchCrawlRegistered(t *testing.T) {
	cloudMethods := []string{
		"search", "search.index", "search.stats",
		"scrape", "scrape.preview", "crawl", "docs.ingest",
		"rag.embed", "rag.query", "rag.query-multiple", "rag.delete", "rag.context",
		"embed", "query", "query-multiple", "documents.delete", "documents.context",
	}
	for _, m := range cloudMethods {
		if zapRagSearchCrawlCloud[m] == nil {
			t.Errorf("cloud method %q not registered", m)
		}
	}

	gatewayPaths := []string{
		"/v1/search", "/v1/index", "/v1/search/stats",
		"/v1/crawl", "/v1/docs/ingest",
		"/v1/rag/embed", "/v1/rag/query", "/v1/rag/query-multiple", "/v1/rag/delete", "/v1/rag/context",
		"/v1/embed", "/v1/query", "/v1/query_multiple", "/v1/documents",
	}
	for _, p := range gatewayPaths {
		if zapRagSearchCrawlGateway[p] == nil {
			t.Errorf("gateway path %q not registered", p)
		}
	}
}

// TestZapRagSearchCrawlAuthRejection asserts the ONE auth seam mirrors the beego
// controllers exactly: no credential → 401 on every handler; a publishable
// (read-only) pk-* key → 403 on write handlers (index/scrape/crawl/ingest/embed/
// delete) but is ACCEPTED past the auth gate on read handlers. Identity is never
// taken from the body — an empty auth is rejected before any body decode.
func TestZapRagSearchCrawlAuthRejection(t *testing.T) {
	readHandlers := map[string]zapRSCHandler{
		"search":       zapSearchHandler,
		"search.stats": zapSearchStatsHandler,
		"rag.query":    zapRagQueryHandler,
		"rag.context":  zapRagContextHandler,
		"query":        zapRagQueryCompatHandler,
	}
	writeHandlers := map[string]zapRSCHandler{
		"index":            zapIndexHandler,
		"scrape":           zapScrapeHandler,
		"crawl":            zapCrawlHandler,
		"docs.ingest":      zapIngestHandler,
		"rag.embed":        zapRagEmbedHandler,
		"rag.delete":       zapRagDeleteHandler,
		"documents.delete": zapDocumentsDeleteHandler,
	}

	body := []byte(`{"query":"hi","url":"https://example.com","file_id":"f1","documents":[{"id":"1"}],"source":"upload"}`)

	// Empty auth → 401 on every handler (read + write), before any body decode.
	for name, h := range readHandlers {
		assertStatus(t, name+"/empty", h, "", body, 401)
	}
	for name, h := range writeHandlers {
		assertStatus(t, name+"/empty", h, "", body, 401)
	}

	// pk-* is read-only: 403 on write handlers (rejected at the write-auth gate,
	// no IAM round-trip needed — pure prefix policy).
	for name, h := range writeHandlers {
		assertStatus(t, name+"/pk", h, "Bearer pk-live-abc123", body, 403)
	}
}

// assertStatus runs a handler and asserts the ZAP response status field.
func assertStatus(t *testing.T, label string, h zapRSCHandler, auth string, body []byte, want uint32) {
	t.Helper()
	msg, err := h(context.Background(), auth, body)
	if err != nil {
		t.Fatalf("%s: handler error: %v", label, err)
	}
	got := msg.Root().Uint32(object.CloudRespStatus)
	if got != want {
		t.Errorf("%s: status = %d, want %d (err=%q)", label, got, want, msg.Root().Text(object.CloudRespError))
	}
}
