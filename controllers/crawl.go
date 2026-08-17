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

// maxCrawlURLs bounds a single /v1/crawl request. Each URL is an outbound fetch,
// so the batch is capped to keep latency and cost bounded.
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
// @Description crawl one or more URLs through the native crawl and return clean,
// @Description LLM-ready markdown. The single canonical "crawl a URL, get content
// @Description back" endpoint.
// @Param body body controllers.crawlRequest true "Crawl request ({url} or {urls})"
// @Success 200 {object} controllers.Response "{results: []object.CrawlResult}"
