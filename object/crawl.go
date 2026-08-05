// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package object

// crawl.go — POST /v1/crawl: fetch a URL, get its content back.
//
// It reads through the ONE crawl, github.com/hanzoai/cloud/apps/crawl, which is
// the same code the answer engine's read stage grounds on. There is no crawl
// SERVICE to call: this used to dial crawl.hanzo.svc.cluster.local:11235, a name
// that does not resolve, so every request returned success:false and the endpoint
// looked merely unlucky rather than unwired.
//
// The native fetch is also strictly safer than the dial it replaces: apps/crawl
// carries a guarded dialer that refuses non-public addresses on EVERY hop,
// including redirects, so a caller-supplied URL cannot be aimed at the cluster's
// own internals. That is the property an SSRF-facing endpoint has to have, and
// the crawl4ai leg never had it.

import (
	"context"
	"errors"
	"sync"
	"time"

	crawlpkg "github.com/hanzoai/cloud/apps/crawl"
)

// errNoURLs is the only way Crawl fails as a whole: it was handed nothing to do.
var errNoURLs = errors.New("crawl: no urls")

// crawlBudget bounds one /v1/crawl batch. Each URL is an outbound fetch with its
// own transport timeout; this is the ceiling on the whole request, so a batch of
// slow hosts cannot hold a handler open indefinitely.
const crawlBudget = 60 * time.Second

// CrawlResult is the canonical, clean output of POST /v1/crawl for one URL: the
// fetched page as LLM-ready markdown plus lightweight metadata. It is the ONE
// crawl result shape — distinct from ScrapeResult, which is docs-structured for
// search ingest.
type CrawlResult struct {
	URL         string                 `json:"url"`
	Title       string                 `json:"title,omitempty"`
	Description string                 `json:"description,omitempty"`
	Markdown    string                 `json:"markdown"`
	Success     bool                   `json:"success"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// Crawl fetches every URL and returns one result each, in the order asked.
//
// A URL that cannot be read is NOT an error: it comes back Success:false with the
// reason in Metadata, because a batch where nine pages read and one was blocked
// has nine pages worth of answer in it. The error return is reserved for a
// request that asked for nothing.
func Crawl(urls []string) ([]CrawlResult, error) {
	if len(urls) == 0 {
		return nil, errNoURLs
	}
	ctx, cancel := context.WithTimeout(context.Background(), crawlBudget)
	defer cancel()

	out := make([]CrawlResult, len(urls))
	var wg sync.WaitGroup
	for i, u := range urls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = crawlOne(ctx, u)
		}()
	}
	wg.Wait()
	return out, nil
}

// crawlOne reads one URL. The result's URL is the one that was ASKED for, never
// the post-redirect address: a caller that submitted a batch matches results back
// to requests by it, and handing back the redirected address would break that
// match on exactly the pages that redirect. Where the page actually came from is
// in Metadata["sourceURL"].
func crawlOne(ctx context.Context, url string) CrawlResult {
	page, err := crawlpkg.Fetch(ctx, url)
	if err != nil {
		return CrawlResult{URL: url, Metadata: map[string]interface{}{"error": err.Error()}}
	}
	r := CrawlResult{URL: url, Title: page.Title, Markdown: page.Markdown, Success: true, Metadata: page.Metadata}
	if desc, ok := page.Metadata["description"].(string); ok {
		r.Description = desc
	}
	return r
}
