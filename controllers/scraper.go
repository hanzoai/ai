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

	"github.com/hanzoai/account"

	"github.com/hanzoai/ai/object"
)

// ScrapeDocs is the ingest-specific route: it crawls a site and WRITES the
// structured content into the owner's search index, returning ScrapeStats. This
// is orthogonal to POST /v1/crawl (which fetches a URL and returns its content
// without indexing) — /v1/scrape is "crawl-and-index", /v1/crawl is "crawl".
//
// @Title ScrapeDocs
// @Tag Scraper API
// @Description crawl a website and index structured content into search (ingest)
// @Param body body object.ScrapeRequest true "Scrape request"
// @Success 200 {object} object.ScrapeStats "Scrape and index statistics"
// @router /scrape [post]
func (c *ApiController) ScrapeDocs() {
	auth := c.requireIndexAuth()
	if auth == nil {
		return
	}

	var req object.ScrapeRequest
	err := json.Unmarshal(c.Ctx.Input.RequestBody, &req)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	if req.URL == "" {
		c.ResponseError("url must not be empty")
		return
	}

	// Check balance before expensive scrape operation against the billing
	// SUBJECT within the org NAMESPACE (per-user for a personal-billing org),
	// matching the chat gate and the usage debit.
	if auth.Owner != "" {
		balance, balanceErr := getUserBalance(account.PayerOf(auth.Owner, auth.UserID).Subject(), auth.Owner)
		if balanceErr == nil && balance <= 0 {
			c.ResponseError("insufficient balance for scrape operation. Add funds at https://hanzo.ai/billing")
			return
		}
	}

	stats, err := object.ScrapeAndIndex(auth.Owner, &req, c.GetAcceptLanguage())
	if err != nil {
		recordSearchUsage(auth, "scrape", "crawl", "error", 0, c.Ctx.Request.RemoteAddr)
		c.ResponseError(err.Error())
		return
	}

	recordSearchUsage(auth, "scrape", stats.Engine, "success", stats.PagesScraped, c.Ctx.Request.RemoteAddr)

	c.ResponseOk(stats)
}

// ScrapePreview is a DEPRECATED alias of POST /v1/crawl — the single canonical
// "crawl a URL, get content back" endpoint (Crawl4AI). It forwards to that one
// handler so there is exactly ONE crawl implementation; there is no parallel
// scrape-preview path. New callers MUST use /v1/crawl.
//
// @Title ScrapePreview
// @Tag Scraper API
// @Description DEPRECATED — use POST /v1/crawl. Crawl a URL and return its content.
// @Param body body controllers.crawlRequest true "Crawl request ({url} or {urls})"
// @Success 200 {object} controllers.Response "{results: []object.CrawlResult}"
// @router /scrape/preview [post]
func (c *ApiController) ScrapePreview() {
	c.Crawl()
}
