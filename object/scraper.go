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

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/ai/log"
	"golang.org/x/net/html"
)

const (
	scraperUserAgent      = "HanzoBot/1.0 (+https://hanzo.ai/bot)"
	scraperDefaultDepth   = 3
	scraperDefaultMax     = 100
	scraperConcurrency    = 5
	scraperRequestDelay   = 200 * time.Millisecond
	scraperRequestTimeout = 30 * time.Second
)

// ScrapeRequest is the request body for web scraping operations.
type ScrapeRequest struct {
	URL      string `json:"url"`
	Depth    int    `json:"depth,omitempty"`
	MaxPages int    `json:"maxPages,omitempty"`
	Selector string `json:"selector,omitempty"`
	Tag      string `json:"tag,omitempty"`
	Store    string `json:"store,omitempty"`
	Engine   string `json:"engine,omitempty"` // "fast" (Go scraper), "browser" (crawl4ai), or "" (auto)
}

// ScrapeResult holds the extracted content from a single page.
type ScrapeResult struct {
	URL         string         `json:"url"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Content     string         `json:"content"`
	Headings    []Heading      `json:"headings"`
	Links       StringList     `json:"links"`
	Structured  StructuredData `json:"structured"`

	// Dropped counts hrefs on the page that address somewhere else on the web but
	// could not be parsed into a URL. Each one may be a page the crawl will never
	// learn about, and a page a crawl never learns about is one it can delete
	// without ever having looked at it — so this is a reason not to mirror.
	Dropped int `json:"dropped,omitempty"`
}

// Heading represents a single heading element with its hierarchy level and anchor.
type Heading struct {
	Level int    `json:"level"`
	ID    string `json:"id,omitempty"`
	Text  string `json:"text"`
}

// ContentBlock represents a block of extracted content (paragraph, code, list).
type ContentBlock struct {
	Type    string `json:"type"` // "paragraph", "code", "list"
	Text    string `json:"text"`
	Section string `json:"section,omitempty"`
}

// StructuredData mirrors the docs framework format for search indexing.
type StructuredData struct {
	Headings []Heading      `json:"headings"`
	Contents []ContentBlock `json:"contents"`
}

// ScrapeStats is the summary returned after a scrape-and-index operation.
type ScrapeStats struct {
	PagesScraped     int        `json:"pagesScraped"`
	DocumentsIndexed int        `json:"documentsIndexed"`
	Engine           string     `json:"engine"`
	Errors           StringList `json:"errors,omitempty"`
}

// robotsRules holds parsed robots.txt disallow rules for User-agent: *.
type robotsRules struct {
	disallow []string
}

// crawlItem is a BFS queue entry.
type crawlItem struct {
	url   string
	depth int
}

// ScrapePage fetches a single URL and extracts structured content.
func ScrapePage(pageURL string) (*ScrapeResult, error) {
	parsed, err := url.Parse(pageURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL %q: %w", pageURL, err)
	}
	if parsed.Scheme == "" {
		parsed.Scheme = "https"
	}
	pageURL = parsed.String()
	req, err := http.NewRequest(http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build request for %s: %w", pageURL, err)
	}
	req.Header.Set("User-Agent", scraperUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	client := &http.Client{
		Timeout: scraperRequestTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch %s: %w", pageURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d fetching %s", resp.StatusCode, pageURL)
	}
	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "text/html") && !strings.Contains(contentType, "application/xhtml") {
		return nil, fmt.Errorf("non-HTML content type %q for %s", contentType, pageURL)
	}
	doc, err := html.Parse(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML from %s: %w", pageURL, err)
	}
	result := &ScrapeResult{URL: pageURL}
	extractContent(doc, result, parsed)
	return result, nil
}

// frontier is what a crawl learned a site contains, and what it managed to read.
//
// Completeness is the DIFFERENCE between the two. It is never a list of the ways
// a crawl can fall short, because that list is an allowlist: state it as "no
// budget was hit and no fetch failed, therefore the site is whole" and every
// truncation nobody has thought of yet reads as a complete crawl — which is
// licence to delete. Counting what was READ makes the unknown case fail the safe
// way. A page dropped by a path no one enumerated is still a URL sitting in
// discovered with nothing opposite it in read, and that alone withholds the
// mirror.
type frontier struct {
	mu         sync.Mutex
	discovered map[string]bool
	read       map[string]bool
	reason     string
}

func newFrontier() *frontier {
	return &frontier{discovered: map[string]bool{}, read: map[string]bool{}}
}

// discover records an in-bounds URL the crawl now knows exists. Every link the
// crawl resolves onto its own host goes here, whether or not it is queued —
// including the ones a depth or a budget will keep it from ever reading.
func (f *frontier) discover(u string) {
	f.mu.Lock()
	f.discovered[u] = true
	f.mu.Unlock()
}

// read records a URL the crawl actually fetched and parsed.
func (f *frontier) readPage(u string) {
	f.mu.Lock()
	f.read[u] = true
	f.mu.Unlock()
}

// note names a reason the crawl fell short. The decision does not rest on it —
// that comes from what went unread — but a named reason also stands on its own,
// so a drop that leaves nothing unread still withholds the mirror.
func (f *frontier) note(reason string) {
	f.mu.Lock()
	if f.reason == "" {
		f.reason = reason
	}
	f.mu.Unlock()
}

// cut is why this crawl is not the whole site, and is empty only when it is.
func (f *frontier) cut() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	unread := 0
	for u := range f.discovered {
		if !f.read[u] {
			unread++
		}
	}
	switch {
	case unread == 0 && f.reason == "":
		return ""
	case unread == 0:
		return f.reason
	case f.reason == "":
		return fmt.Sprintf("%d of %d discovered pages unread", unread, len(f.discovered))
	}
	return fmt.Sprintf("%s (%d of %d discovered pages unread)", f.reason, unread, len(f.discovered))
}

// CrawlSite performs a crawl starting from req.URL and returns scraped pages.
// Engine selection:
//   - "browser": always use crawl4ai (errors if unavailable)
//   - "fast": always use the Go HTML scraper
//   - "" (auto): try crawl4ai first, fall back to Go scraper if unreachable
//
// The returned engine string indicates which engine was actually used.
//
// cut is why the crawl is not the whole site, and is empty only when every URL
// it discovered on that site was also read. It is what lets a caller tell "this
// page is gone" from "this page was never reached", which are the same absence
// and opposite facts.
func CrawlSite(req *ScrapeRequest) (results []ScrapeResult, crawlErrors []string, engine string, cut string) {
	engine = req.Engine
	switch engine {
	case "browser":
		results, crawlErrors, cut = crawlWithBrowserEngine(req)
		return results, crawlErrors, "browser", cut
	case "fast":
		results, crawlErrors, cut = crawlWithGoScraper(req)
		return results, crawlErrors, "fast", cut
	default:
		// Auto mode: try crawl4ai, fall back to Go scraper
		if IsCrawl4AIAvailable() {
			log.Info("scraper: crawl4ai is available, using browser engine for %s", req.URL)
			results, crawlErrors, cut = crawlWithBrowserEngine(req)
			return results, crawlErrors, "browser", cut
		}
		log.Info("scraper: crawl4ai is not available, falling back to Go scraper for %s", req.URL)
		results, crawlErrors, cut = crawlWithGoScraper(req)
		return results, crawlErrors, "fast", cut
	}
}

// crawlWithBrowserEngine uses crawl4ai to crawl URLs with a headless browser.
// It first crawls the start URL, then follows discovered same-domain links up to
// the configured depth and maxPages limits.
func crawlWithBrowserEngine(req *ScrapeRequest) (results []ScrapeResult, crawlErrors []string, cut string) {
	maxPages := req.MaxPages
	if maxPages <= 0 {
		maxPages = scraperDefaultMax
	}
	maxDepth := req.Depth
	if maxDepth <= 0 {
		maxDepth = scraperDefaultDepth
	}
	startURL, err := url.Parse(req.URL)
	if err != nil {
		return nil, []string{fmt.Sprintf("invalid start URL: %v", err)}, "invalid start URL"
	}
	if startURL.Scheme == "" {
		startURL.Scheme = "https"
	}
	baseDomain := startURL.Hostname()
	// The same robots rules the Go scraper obeys. Both engines must account for a
	// blocked page the same way, or which engine ran decides which pages a site
	// keeps.
	robots := fetchRobotsTxt(startURL.Scheme + "://" + startURL.Host)
	visited := make(map[string]bool)
	normalizedStart := normalizeURL(startURL.String())
	visited[normalizedStart] = true
	f := newFrontier()
	f.discover(normalizedStart)
	// BFS over discovered links, batching URLs to crawl4ai
	currentLevel := []string{normalizedStart}
	for depth := 0; depth <= maxDepth && len(results) < maxPages && len(currentLevel) > 0; depth++ {
		// Cap the batch to remaining page budget
		remaining := maxPages - len(results)
		batch := currentLevel
		if len(batch) > remaining {
			batch = batch[:remaining]
			f.note("page budget")
		}
		submitted := make([]string, 0, len(batch))
		for _, u := range batch {
			if isDisallowed(robots, u) {
				log.Info("scraper: robots.txt disallows %s", u)
				// Blocked is not gone. It stays discovered and unread.
				f.note("robots.txt")
				continue
			}
			submitted = append(submitted, u)
		}
		if len(submitted) == 0 {
			break
		}
		crawl4aiResults, crawlErr := CrawlWithCrawl4AI(submitted)
		if crawlErr != nil {
			crawlErrors = append(crawlErrors, fmt.Sprintf("crawl4ai batch at depth %d: %v", depth, crawlErr))
			f.note("batch failed")
			break
		}
		// Walk what was SUBMITTED, not what came back. A URL the service drops —
		// an internal cap, a swallowed exception, a redirect collapsed onto
		// another result — returns nothing at all: no result, no Success:false, no
		// error. Iterating the response would never see it, and it is already
		// marked visited, so nothing ever retries it.
		returned := make(map[string]Crawl4AIResult, len(crawl4aiResults))
		for _, cr := range crawl4aiResults {
			returned[normalizeURL(cr.URL)] = cr
		}
		var nextLevel []string
		for _, u := range submitted {
			cr, ok := returned[u]
			if !ok {
				f.note("no result returned")
				continue
			}
			if !cr.Success {
				crawlErrors = append(crawlErrors, fmt.Sprintf("%s: crawl4ai reported failure", cr.URL))
				continue
			}
			sr := Crawl4AIResultToScrapeResult(cr)
			results = append(results, sr)
			f.readPage(u)
			if sr.Dropped > 0 {
				f.note("unparsable link")
			}
			if len(results) >= maxPages {
				f.note("page budget")
				break
			}
			// Collect same-domain links for the next BFS level. Everything on this
			// host is discovered here whether or not it is queued, so a link the
			// depth limit stops us following still counts against completeness.
			for _, link := range sr.Links {
				linkParsed, linkErr := url.Parse(link)
				if linkErr != nil {
					f.note("unparsable link")
					continue
				}
				if linkParsed.Hostname() != baseDomain {
					continue
				}
				normalized := normalizeURL(link)
				f.discover(normalized)
				if depth >= maxDepth || visited[normalized] {
					continue
				}
				visited[normalized] = true
				nextLevel = append(nextLevel, normalized)
			}
		}
		currentLevel = nextLevel
	}
	return results, crawlErrors, f.cut()
}

// crawlWithGoScraper performs a BFS crawl using the built-in Go HTML scraper.
func crawlWithGoScraper(req *ScrapeRequest) (results []ScrapeResult, crawlErrors []string, cut string) {
	maxPages := req.MaxPages
	if maxPages <= 0 {
		maxPages = scraperDefaultMax
	}
	maxDepth := req.Depth
	if maxDepth <= 0 {
		maxDepth = scraperDefaultDepth
	}
	startURL, err := url.Parse(req.URL)
	if err != nil {
		return nil, []string{fmt.Sprintf("invalid start URL: %v", err)}, "invalid start URL"
	}
	if startURL.Scheme == "" {
		startURL.Scheme = "https"
	}
	baseDomain := startURL.Hostname()
	robots := fetchRobotsTxt(startURL.Scheme + "://" + startURL.Host)
	visited := &sync.Map{}
	var resultsMu sync.Mutex
	var errorsMu sync.Mutex
	f := newFrontier()
	sem := make(chan struct{}, scraperConcurrency)
	queue := make(chan crawlItem, maxPages*2)
	// pending counts items that are QUEUED BUT NOT YET FINISHED. A page adds its
	// children before releasing itself, so the count reaches zero only when the
	// frontier is empty — and that is the only moment the queue may be closed.
	//
	// It replaces a wait that counted running pages instead of outstanding ones.
	// That count starts at zero, so the close fired before the first page had even
	// been picked up, the loop below ended on the closed queue, and the crawl
	// returned while its own fetches were still in flight: no pages, no errors, no
	// sign anything had gone wrong. Every "fast" crawl indexed nothing.
	var pending sync.WaitGroup
	normalizedStart := normalizeURL(startURL.String())
	visited.Store(normalizedStart, true)
	f.discover(normalizedStart)
	pending.Add(1)
	queue <- crawlItem{url: normalizedStart, depth: 0}
	go func() {
		pending.Wait()
		close(queue)
	}()
	var pageCount int
	var pageCountMu sync.Mutex
	// Every path that takes an item off the queue releases exactly one count,
	// including the two that decline to crawl it — miss one and the close never
	// comes.
	for item := range queue {
		pageCountMu.Lock()
		budget := pageCount >= maxPages
		pageCountMu.Unlock()
		if budget {
			f.note("page budget")
			pending.Done()
			continue
		}
		if item.depth > maxDepth {
			f.note("depth limit")
			pending.Done()
			continue
		}
		sem <- struct{}{}
		go func(ci crawlItem) {
			defer pending.Done()
			defer func() { <-sem }()
			pageCountMu.Lock()
			if pageCount >= maxPages {
				pageCountMu.Unlock()
				f.note("page budget")
				return
			}
			pageCount++
			pageCountMu.Unlock()
			time.Sleep(scraperRequestDelay)
			if isDisallowed(robots, ci.url) {
				log.Info("scraper: robots.txt disallows %s", ci.url)
				// A page we are not allowed to read is not a page that is gone.
				// One robots rule arriving must never empty what it now covers.
				f.note("robots.txt")
				return
			}
			result, scrapeErr := ScrapePage(ci.url)
			if scrapeErr != nil {
				errorsMu.Lock()
				crawlErrors = append(crawlErrors, fmt.Sprintf("%s: %v", ci.url, scrapeErr))
				errorsMu.Unlock()
				return
			}
			resultsMu.Lock()
			results = append(results, *result)
			resultsMu.Unlock()
			f.readPage(ci.url)
			if result.Dropped > 0 {
				f.note("unparsable link")
			}
			for _, link := range result.Links {
				linkParsed, linkErr := url.Parse(link)
				if linkErr != nil {
					f.note("unparsable link")
					continue
				}
				if linkParsed.Hostname() != baseDomain {
					continue
				}
				normalized := normalizeURL(link)
				// Discovered whether or not it is queued: a link the depth limit
				// stops us following is still a page we know exists and did not read.
				f.discover(normalized)
				if ci.depth >= maxDepth {
					continue
				}
				if _, loaded := visited.LoadOrStore(normalized, true); loaded {
					continue
				}
				// Claimed before the send, released if the send is declined: the
				// count may not drop to zero while this page still has children to
				// hand over.
				pending.Add(1)
				select {
				case queue <- crawlItem{url: normalized, depth: ci.depth + 1}:
				default:
					pending.Done()
					f.note("queue full")
				}
			}
		}(item)
	}
	// The loop ends when the queue closes, which is when pending reached zero,
	// which is after the last page finished. Everything appended is visible here.
	return results, crawlErrors, f.cut()
}

// ScrapeAndIndex crawls a site and indexes the results into the owner's search index.
// The owner parameter determines tenant isolation -- each org gets its own index namespace.
// If Hanzo Storage is configured, crawl results are archived asynchronously for persistence.
func ScrapeAndIndex(owner string, req *ScrapeRequest, lang string) (*ScrapeStats, error) {
	if req.URL == "" {
		return nil, fmt.Errorf("url must not be empty")
	}
	results, crawlErrors, engine, cut := CrawlSite(req)
	stats := &ScrapeStats{
		PagesScraped: len(results),
		Engine:       engine,
		Errors:       crawlErrors,
	}
	if len(results) == 0 {
		return stats, nil
	}
	// Archive crawl results to Hanzo Storage (fire-and-forget)
	if IsCrawlStorageConfigured() {
		jobID := hashID(fmt.Sprintf("%s-%s-%d", owner, req.URL, time.Now().UnixNano()))
		archiveCrawlResultAsync(owner, jobID, results, nil)
	}
	tag := req.Tag
	if tag == "" {
		parsed, err := url.Parse(req.URL)
		if err == nil {
			tag = parsed.Hostname()
		}
	}
	store, err := ResolveStore(owner, req.Store, "docs-hanzo-ai")
	if err != nil {
		return stats, err
	}
	docs := scrapeResultsToDocIndex(results, tag)
	indexReq := &DocIndexRequest{Documents: docs}
	// A crawl that read every page it discovered IS the site's current state, so
	// the index can mirror it and a page that was taken down stops being cited. A
	// crawl that left anything it found unread saw PART of the site, and a page
	// missing from part of a site is not a page that was removed from it — so that
	// crawl only adds, exactly as every crawl did before. The tag and the root are
	// what bound the mirror; without both there is nothing to bound it to.
	//
	// A consequence worth knowing before someone reports it as a fault: a site
	// with more pages than MaxPages (100 by default) always exhausts the budget,
	// always leaves pages unread, and therefore never mirrors. Deletions on a
	// large site do not begin until the budget covers it. That is the guard doing
	// its job — a crawl that saw 100 of 4000 pages knows nothing about the other
	// 3900 — not deletion being broken.
	root := crawlRoot(req.URL)
	switch {
	case tag == "" || root == "":
		log.Info("scraper: %s indexed additively, nothing to bound a mirror with", req.URL)
	case cut != "":
		log.Info("scraper: %s indexed additively, crawl cut short by %s", req.URL, cut)
	case len(crawlErrors) > 0:
		log.Info("scraper: %s indexed additively, %d page(s) failed to fetch", req.URL, len(crawlErrors))
	default:
		indexReq.Mirror = &Mirror{Tag: tag, Root: root}
	}
	count, err := IndexDocuments(owner, store, indexReq, lang)
	if err != nil {
		return stats, fmt.Errorf("indexing failed after scraping %d pages: %w", len(results), err)
	}
	stats.DocumentsIndexed = count
	return stats, nil
}

// crawlRoot is the subtree a crawl of rawURL is authoritative over: the start
// URL, spelled the way the pages it fetched are spelled. It defaults the scheme
// and drops the trailing slash exactly as the crawler and ScrapePage do, because
// a root that does not match the stored URLs bounds a mirror to nothing — which
// costs a prune, and is the right way to be wrong.
func crawlRoot(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	if parsed.Scheme == "" {
		parsed.Scheme = "https"
	}
	return normalizeURL(parsed.String())
}

// scrapeResultsToDocIndex converts scrape results to the DocIndex format for search indexing.
func scrapeResultsToDocIndex(results []ScrapeResult, tag string) []DocIndex {
	var docs []DocIndex
	for _, result := range results {
		pageID := hashID(result.URL)
		// Create a document for the page itself
		pageDoc := DocIndex{
			ID:          pageID,
			PageID:      pageID,
			Title:       result.Title,
			URL:         result.URL,
			Content:     truncateContent(result.Content, 10000),
			Tag:         tag,
			Breadcrumbs: []string{result.Title},
		}
		docs = append(docs, pageDoc)
		// Create a document for each heading + its following content
		currentSection := ""
		currentSectionID := ""
		var sectionContent strings.Builder
		for _, block := range result.Structured.Contents {
			if block.Section != currentSection && currentSection != "" {
				sectionDoc := DocIndex{
					ID:          hashID(result.URL + "#" + currentSectionID),
					PageID:      pageID,
					Title:       result.Title,
					URL:         sectionURL(result.URL, currentSectionID),
					Content:     truncateContent(sectionContent.String(), 5000),
					Section:     currentSection,
					SectionID:   currentSectionID,
					Tag:         tag,
					Breadcrumbs: []string{result.Title, currentSection},
				}
				docs = append(docs, sectionDoc)
				sectionContent.Reset()
			}
			if block.Section != "" {
				currentSection = block.Section
				currentSectionID = slugify(block.Section)
			}
			if sectionContent.Len() > 0 {
				sectionContent.WriteString("\n")
			}
			sectionContent.WriteString(block.Text)
		}
		// Flush the final section
		if currentSection != "" && sectionContent.Len() > 0 {
			sectionDoc := DocIndex{
				ID:          hashID(result.URL + "#" + currentSectionID),
				PageID:      pageID,
				Title:       result.Title,
				URL:         sectionURL(result.URL, currentSectionID),
				Content:     truncateContent(sectionContent.String(), 5000),
				Section:     currentSection,
				SectionID:   currentSectionID,
				Tag:         tag,
				Breadcrumbs: []string{result.Title, currentSection},
			}
			docs = append(docs, sectionDoc)
		}
	}
	return docs
}

// extractContent walks the HTML tree and populates the ScrapeResult.
func extractContent(doc *html.Node, result *ScrapeResult, baseURL *url.URL) {
	var contentBuilder strings.Builder
	var currentSection string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			tag := strings.ToLower(n.Data)
			// Skip elements that are not content
			if isSkippedTag(tag) {
				return
			}
			switch {
			case tag == "title":
				result.Title = getTextContent(n)
				return
			case tag == "meta":
				name := getAttr(n, "name")
				if strings.EqualFold(name, "description") {
					result.Description = getAttr(n, "content")
				}
				return
			case isHeadingTag(tag):
				level := int(tag[1] - '0')
				id := getAttr(n, "id")
				text := getTextContent(n)
				heading := Heading{Level: level, ID: id, Text: text}
				result.Headings = append(result.Headings, heading)
				result.Structured.Headings = append(result.Structured.Headings, heading)
				currentSection = text
			case tag == "p":
				text := strings.TrimSpace(getTextContent(n))
				if text != "" {
					contentBuilder.WriteString(text)
					contentBuilder.WriteString("\n")
					result.Structured.Contents = append(result.Structured.Contents, ContentBlock{
						Type:    "paragraph",
						Text:    text,
						Section: currentSection,
					})
				}
				return
			case tag == "pre" || tag == "code":
				text := strings.TrimSpace(getTextContent(n))
				if text != "" {
					contentBuilder.WriteString(text)
					contentBuilder.WriteString("\n")
					result.Structured.Contents = append(result.Structured.Contents, ContentBlock{
						Type:    "code",
						Text:    text,
						Section: currentSection,
					})
				}
				return
			case tag == "ul" || tag == "ol":
				text := strings.TrimSpace(extractListItems(n))
				if text != "" {
					contentBuilder.WriteString(text)
					contentBuilder.WriteString("\n")
					result.Structured.Contents = append(result.Structured.Contents, ContentBlock{
						Type:    "list",
						Text:    text,
						Section: currentSection,
					})
				}
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	result.Content = strings.TrimSpace(contentBuilder.String())
	// Links come from their own walk of the whole tree, including the containers
	// the walk above stops at and the regions it skips — and they resolve against
	// <base href> when the document sets one, the way a browser does.
	linkBase := baseURL
	if b := baseHref(doc, baseURL); b != nil {
		linkBase = b
	}
	collectLinks(doc, linkBase, result)
}

// getTextContent recursively extracts text from a node and its children.
func getTextContent(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var sb strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && isSkippedTag(strings.ToLower(c.Data)) {
			continue
		}
		sb.WriteString(getTextContent(c))
	}
	return sb.String()
}

// getAttr returns the value of the named attribute, or empty string.
func getAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// isHeadingTag returns true for h1-h6.
func isHeadingTag(tag string) bool {
	return len(tag) == 2 && tag[0] == 'h' && tag[1] >= '1' && tag[1] <= '6'
}

// isSkippedTag returns true for tags whose content should be stripped.
func isSkippedTag(tag string) bool {
	switch tag {
	case "script", "style", "nav", "footer", "header", "aside", "noscript", "iframe", "svg":
		return true
	}
	return false
}

// extractListItems extracts text from li elements within a list.
func extractListItems(n *html.Node) string {
	var sb strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && strings.ToLower(c.Data) == "li" {
			text := strings.TrimSpace(getTextContent(c))
			if text != "" {
				sb.WriteString("- ")
				sb.WriteString(text)
				sb.WriteString("\n")
			}
		}
	}
	return sb.String()
}

// pageHref reports whether an href addresses a page at all. A fragment, a
// mailto: or a javascript: goes nowhere the crawl could follow, so failing to
// resolve one costs it nothing — which is what separates those from an href that
// is simply malformed.
func pageHref(href string) bool {
	href = strings.TrimSpace(href)
	return href != "" &&
		!strings.HasPrefix(href, "#") &&
		!strings.HasPrefix(href, "javascript:") &&
		!strings.HasPrefix(href, "mailto:")
}

// baseHref is the document's <base href> resolved against the page URL, or nil
// when the document sets none.
//
// A browser resolves every relative href on the page against it. A crawl that
// ignores it walks to URLs the site does not serve and never reaches the ones it
// does — and when the misresolved URL happens to answer 200, the crawl reads a
// page it was never pointed at, leaves the real one undiscovered, and every
// signal still reads complete. Doxygen, Javadoc and Jekyll with a baseurl all
// emit one, so this is ordinary docs markup, not an edge case.
//
// Only the first <base href> counts, which is what the HTML spec says too.
func baseHref(n *html.Node, pageURL *url.URL) *url.URL {
	var found *url.URL
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found != nil {
			return
		}
		if n.Type == html.ElementNode && strings.ToLower(n.Data) == "base" {
			if href := strings.TrimSpace(getAttr(n, "href")); href != "" {
				if parsed, err := url.Parse(href); err == nil {
					found = pageURL.ResolveReference(parsed)
					return
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return found
}

// collectLinks walks the WHOLE document for hrefs and resolves them against the
// page they were found on.
//
// It is a pass of its own, and that is the point. Text extraction stops
// descending the moment it has a paragraph's or a list's text, and skips nav,
// header, footer and aside outright — right for prose, wrong for links, because
// a docs site keeps its links in precisely those places: the sidebar tree, the
// next/prev footer, the reference in the middle of a sentence. Riding discovery
// on that walk meant a page linked only from a <ul> was never discovered, never
// read, and then deleted as missing while it was still being served. Discovery
// and extraction answer different questions and no longer share a walk.
func collectLinks(n *html.Node, baseURL *url.URL, result *ScrapeResult) {
	if n.Type == html.ElementNode && strings.ToLower(n.Data) == "a" {
		if href := getAttr(n, "href"); href != "" {
			if resolved := resolveHref(href, baseURL); resolved != "" {
				result.Links = append(result.Links, resolved)
			} else if pageHref(href) {
				result.Dropped++
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		collectLinks(c, baseURL, result)
	}
}

// resolveHref resolves a relative or absolute href against the base URL.
func resolveHref(href string, baseURL *url.URL) string {
	href = strings.TrimSpace(href)
	if !pageHref(href) {
		return ""
	}
	parsed, err := url.Parse(href)
	if err != nil {
		return ""
	}
	resolved := baseURL.ResolveReference(parsed)
	// Strip fragment
	resolved.Fragment = ""
	return resolved.String()
}

// normalizeURL strips trailing slashes and fragments for deduplication.
func normalizeURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	parsed.Fragment = ""
	result := parsed.String()
	result = strings.TrimRight(result, "/")
	return result
}

// fetchRobotsTxt fetches and parses robots.txt for the given origin.
func fetchRobotsTxt(origin string) *robotsRules {
	rules := &robotsRules{}
	robotsURL := strings.TrimRight(origin, "/") + "/robots.txt"
	req, err := http.NewRequest(http.MethodGet, robotsURL, nil)
	if err != nil {
		return rules
	}
	req.Header.Set("User-Agent", scraperUserAgent)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return rules
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return rules
	}
	scanner := bufio.NewScanner(resp.Body)
	inWildcardAgent := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "user-agent:") {
			agent := strings.TrimSpace(strings.TrimPrefix(lower, "user-agent:"))
			inWildcardAgent = (agent == "*")
			continue
		}
		if inWildcardAgent && strings.HasPrefix(lower, "disallow:") {
			path := strings.TrimSpace(strings.TrimPrefix(line, strings.SplitN(line, ":", 2)[0]+":"))
			if path != "" {
				rules.disallow = append(rules.disallow, path)
			}
		}
	}
	return rules
}

// isDisallowed checks whether a URL path is disallowed by the robots.txt rules.
func isDisallowed(rules *robotsRules, rawURL string) bool {
	if rules == nil || len(rules.disallow) == 0 {
		return false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	path := parsed.Path
	for _, disallowed := range rules.disallow {
		if strings.HasPrefix(path, disallowed) {
			return true
		}
	}
	return false
}

// hashID produces a deterministic short ID from a string.
func hashID(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h[:12])
}

// slugify converts a heading text to a URL-safe slug.
func slugify(s string) string {
	s = strings.ToLower(s)
	var sb strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
		} else if r == ' ' || r == '-' || r == '_' {
			sb.WriteRune('-')
		}
	}
	result := sb.String()
	result = strings.Trim(result, "-")
	// Collapse multiple dashes
	for strings.Contains(result, "--") {
		result = strings.ReplaceAll(result, "--", "-")
	}
	return result
}

// sectionURL appends a fragment to a URL if the sectionID is non-empty.
func sectionURL(pageURL, sectionID string) string {
	if sectionID == "" {
		return pageURL
	}
	return pageURL + "#" + sectionID
}

// truncateContent limits content to maxLen characters.
func truncateContent(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}
