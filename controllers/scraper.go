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

	"github.com/hanzoai/cloud/object"
)

// ScrapeDocs
// @Title ScrapeDocs
// @Tag Scraper API
// @Description crawl a website and index structured content into search
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

	// Check balance before expensive scrape operation
	if auth.UserID != "" {
		balance, balanceErr := getUserBalance(auth.UserID)
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

// ScrapePreview
// @Title ScrapePreview
// @Tag Scraper API
// @Description scrape a single URL and return structured data without indexing
// @Param body body object.ScrapeRequest true "Preview request (url required, engine optional: fast|browser)"
// @Success 200 {object} object.ScrapeResult "Structured page content"
// @router /scrape/preview [post]
func (c *ApiController) ScrapePreview() {
	auth := c.requireIndexAuth()
	if auth == nil {
		return
	}

	var req struct {
		URL    string `json:"url"`
		Engine string `json:"engine,omitempty"` // "fast" (Go scraper), "browser" (crawl4ai), or "" (auto)
	}
	err := json.Unmarshal(c.Ctx.Input.RequestBody, &req)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	if req.URL == "" {
		c.ResponseError("url must not be empty")
		return
	}

	useBrowser := false
	switch req.Engine {
	case "browser":
		useBrowser = true
	case "fast":
		useBrowser = false
	default:
		// Auto: use crawl4ai if available
		useBrowser = object.IsCrawl4AIAvailable()
	}

	if useBrowser {
		results, crawlErr := object.CrawlWithCrawl4AI([]string{req.URL})
		if crawlErr != nil {
			c.ResponseError(crawlErr.Error())
			return
		}
		if len(results) == 0 {
			c.ResponseError("crawl4ai returned no results")
			return
		}
		if !results[0].Success {
			c.ResponseError("crawl4ai reported failure for " + req.URL)
			return
		}
		sr := object.Crawl4AIResultToScrapeResult(results[0])

		// Archive the browser crawl result to Hanzo Storage (fire-and-forget)
		if object.IsCrawlStorageConfigured() {
			object.ArchiveCrawlPreviewAsync(auth.Owner, req.URL, sr, results[0])
		}

		recordSearchUsage(auth, "scrape", "crawl4ai", "success", 1, c.Ctx.Request.RemoteAddr)
		c.ResponseOk(sr)
		return
	}

	result, err := object.ScrapePage(req.URL)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	recordSearchUsage(auth, "scrape", "fast", "success", 1, c.Ctx.Request.RemoteAddr)

	c.ResponseOk(result)
}
