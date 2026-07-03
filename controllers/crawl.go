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

	"github.com/hanzoai/ai/object"
)

// maxCrawlURLs bounds a single /v1/crawl request. Each URL is a headless-browser
// render on the self-hosted Hanzo Crawl service, so the batch is capped to keep
// latency and cost bounded.
const maxCrawlURLs = 10

// crawlRequest is the POST /v1/crawl body. A caller may pass a single `url` or a
// batch of `urls` (or both — they are merged). This is the ONE crawl contract.
type crawlRequest struct {
	URL  string   `json:"url,omitempty"`
	Urls []string `json:"urls,omitempty"`
}

// Crawl
// @Title Crawl
// @Tag Crawl API
// @Description crawl one or more URLs via the self-hosted Hanzo Crawl (Crawl4AI)
// @Description service and return clean, LLM-ready markdown. The single canonical
// @Description "crawl a URL, get content back" endpoint.
// @Param body body controllers.crawlRequest true "Crawl request ({url} or {urls})"
// @Success 200 {object} controllers.Response "{results: []object.CrawlResult}"
// @router /crawl [post]
func (c *ApiController) Crawl() {
	// Write-level auth, fail-closed: crawl performs billable outbound fetches, so
	// it is gated like /v1/scrape (rejects read-only pk-* / widget keys).
	auth := c.requireIndexAuth()
	if auth == nil {
		return
	}

	var req crawlRequest
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, &req); err != nil {
		c.ResponseError(err.Error())
		return
	}

	// Merge single `url` and batch `urls` into one deduplicated, non-empty list.
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
		c.ResponseError("url (or urls) must not be empty")
		return
	}
	if len(urls) > maxCrawlURLs {
		c.ResponseError("too many urls: at most 10 per request")
		return
	}

	// Balance gate before the expensive crawl, against the billing SUBJECT within
	// the org NAMESPACE — parity with the chat gate, the scrape gate, and the
	// usage debit. (The balance filter also gates this path; this is the same
	// belt-and-suspenders check /v1/scrape performs.)
	if auth.Owner != "" {
		balance, balanceErr := getUserBalance(object.BillingSubjectFromUserKey(auth.Owner, auth.UserID), auth.Owner)
		if balanceErr == nil && balance <= 0 {
			c.ResponseError("insufficient balance for crawl operation. Add funds at https://hanzo.ai/billing")
			return
		}
	}

	results, err := object.Crawl(urls)
	if err != nil {
		recordSearchUsage(auth, "crawl", "crawl4ai", "error", 0, c.Ctx.Request.RemoteAddr)
		c.ResponseError(err.Error())
		return
	}

	recordSearchUsage(auth, "crawl", "crawl4ai", "success", len(results), c.Ctx.Request.RemoteAddr)

	c.Data["json"] = map[string]interface{}{"results": results}
	c.ServeJSON()
}
