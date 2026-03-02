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

	"github.com/beego/beego/logs"
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
	Links       []string       `json:"links"`
	Structured  StructuredData `json:"structured"`
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
	PagesScraped     int      `json:"pagesScraped"`
	DocumentsIndexed int      `json:"documentsIndexed"`
	Engine           string   `json:"engine"`
	Errors           []string `json:"errors,omitempty"`
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

// CrawlSite performs a crawl starting from req.URL and returns scraped pages.
// Engine selection:
//   - "browser": always use crawl4ai (errors if unavailable)
//   - "fast": always use the Go HTML scraper
//   - "" (auto): try crawl4ai first, fall back to Go scraper if unreachable
//
// The returned engine string indicates which engine was actually used.
func CrawlSite(req *ScrapeRequest) (results []ScrapeResult, crawlErrors []string, engine string) {
	engine = req.Engine

	switch engine {
	case "browser":
		results, crawlErrors = crawlWithBrowserEngine(req)
		return results, crawlErrors, "browser"
	case "fast":
		results, crawlErrors = crawlWithGoScraper(req)
		return results, crawlErrors, "fast"
	default:
		// Auto mode: try crawl4ai, fall back to Go scraper
		if IsCrawl4AIAvailable() {
			logs.Info("scraper: crawl4ai is available, using browser engine for %s", req.URL)
			results, crawlErrors = crawlWithBrowserEngine(req)
			return results, crawlErrors, "browser"
		}
		logs.Info("scraper: crawl4ai is not available, falling back to Go scraper for %s", req.URL)
		results, crawlErrors = crawlWithGoScraper(req)
		return results, crawlErrors, "fast"
	}
}

// crawlWithBrowserEngine uses crawl4ai to crawl URLs with a headless browser.
// It first crawls the start URL, then follows discovered same-domain links up to
// the configured depth and maxPages limits.
func crawlWithBrowserEngine(req *ScrapeRequest) ([]ScrapeResult, []string) {
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
		return nil, []string{fmt.Sprintf("invalid start URL: %v", err)}
	}
	if startURL.Scheme == "" {
		startURL.Scheme = "https"
	}

	baseDomain := startURL.Hostname()
	visited := make(map[string]bool)
	normalizedStart := normalizeURL(startURL.String())
	visited[normalizedStart] = true

	var results []ScrapeResult
	var crawlErrors []string

	// BFS over discovered links, batching URLs to crawl4ai
	currentLevel := []string{normalizedStart}

	for depth := 0; depth <= maxDepth && len(results) < maxPages && len(currentLevel) > 0; depth++ {
		// Cap the batch to remaining page budget
		remaining := maxPages - len(results)
		batch := currentLevel
		if len(batch) > remaining {
			batch = batch[:remaining]
		}

		crawl4aiResults, crawlErr := CrawlWithCrawl4AI(batch)
		if crawlErr != nil {
			crawlErrors = append(crawlErrors, fmt.Sprintf("crawl4ai batch at depth %d: %v", depth, crawlErr))
			break
		}

		var nextLevel []string

		for _, cr := range crawl4aiResults {
			if !cr.Success {
				crawlErrors = append(crawlErrors, fmt.Sprintf("%s: crawl4ai reported failure", cr.URL))
				continue
			}

			sr := Crawl4AIResultToScrapeResult(cr)
			results = append(results, sr)

			if len(results) >= maxPages {
				break
			}

			// Collect same-domain links for the next BFS level
			if depth < maxDepth {
				for _, link := range sr.Links {
					linkParsed, linkErr := url.Parse(link)
					if linkErr != nil {
						continue
					}
					if linkParsed.Hostname() != baseDomain {
						continue
					}
					normalized := normalizeURL(link)
					if visited[normalized] {
						continue
					}
					visited[normalized] = true
					nextLevel = append(nextLevel, normalized)
				}
			}
		}

		currentLevel = nextLevel
	}

	return results, crawlErrors
}

// crawlWithGoScraper performs a BFS crawl using the built-in Go HTML scraper.
func crawlWithGoScraper(req *ScrapeRequest) ([]ScrapeResult, []string) {
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
		return nil, []string{fmt.Sprintf("invalid start URL: %v", err)}
	}
	if startURL.Scheme == "" {
		startURL.Scheme = "https"
	}

	baseDomain := startURL.Hostname()
	robots := fetchRobotsTxt(startURL.Scheme + "://" + startURL.Host)

	visited := &sync.Map{}
	var results []ScrapeResult
	var resultsMu sync.Mutex
	var crawlErrors []string
	var errorsMu sync.Mutex

	sem := make(chan struct{}, scraperConcurrency)
	queue := make(chan crawlItem, maxPages*2)
	var wg sync.WaitGroup

	normalizedStart := normalizeURL(startURL.String())
	visited.Store(normalizedStart, true)
	queue <- crawlItem{url: normalizedStart, depth: 0}

	var pageCount int
	var pageCountMu sync.Mutex

	done := make(chan struct{})

	go func() {
		for item := range queue {
			pageCountMu.Lock()
			if pageCount >= maxPages {
				pageCountMu.Unlock()
				continue
			}
			pageCountMu.Unlock()

			if item.depth > maxDepth {
				continue
			}

			wg.Add(1)
			sem <- struct{}{}

			go func(ci crawlItem) {
				defer wg.Done()
				defer func() { <-sem }()

				pageCountMu.Lock()
				if pageCount >= maxPages {
					pageCountMu.Unlock()
					return
				}
				pageCount++
				pageCountMu.Unlock()

				time.Sleep(scraperRequestDelay)

				if isDisallowed(robots, ci.url) {
					logs.Info("scraper: robots.txt disallows %s", ci.url)
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

				if ci.depth < maxDepth {
					for _, link := range result.Links {
						linkParsed, linkErr := url.Parse(link)
						if linkErr != nil {
							continue
						}

						if linkParsed.Hostname() != baseDomain {
							continue
						}

						normalized := normalizeURL(link)
						if _, loaded := visited.LoadOrStore(normalized, true); loaded {
							continue
						}

						select {
						case queue <- crawlItem{url: normalized, depth: ci.depth + 1}:
						default:
						}
					}
				}
			}(item)
		}
		close(done)
	}()

	go func() {
		wg.Wait()
		close(queue)
	}()

	<-done

	return results, crawlErrors
}

// ScrapeAndIndex crawls a site and indexes the results into the owner's search index.
// The owner parameter determines tenant isolation -- each org gets its own index namespace.
// If Hanzo Storage is configured, crawl results are archived asynchronously for persistence.
func ScrapeAndIndex(owner string, req *ScrapeRequest, lang string) (*ScrapeStats, error) {
	if req.URL == "" {
		return nil, fmt.Errorf("url must not be empty")
	}

	results, crawlErrors, engine := CrawlSite(req)

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

	store := req.Store
	if store == "" {
		store = "docs-hanzo-ai"
	}

	docs := scrapeResultsToDocIndex(results, tag)

	indexReq := &DocIndexRequest{
		Documents: docs,
		Replace:   false,
	}

	count, err := IndexDocuments(owner, store, indexReq, lang)
	if err != nil {
		return stats, fmt.Errorf("indexing failed after scraping %d pages: %w", len(results), err)
	}

	stats.DocumentsIndexed = count
	return stats, nil
}

// scrapeResultsToDocIndex converts scrape results to the DocIndex format for search indexing.
func scrapeResultsToDocIndex(results []ScrapeResult, tag string) []DocIndex {
	var docs []DocIndex

	for _, result := range results {
		pageID := hashID(result.URL)

		// Create a document for the page itself
		pageDoc := DocIndex{
			ID:      pageID,
			PageID:  pageID,
			Title:   result.Title,
			URL:     result.URL,
			Content: truncateContent(result.Content, 10000),
			Tag:     tag,
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
					ID:        hashID(result.URL + "#" + currentSectionID),
					PageID:    pageID,
					Title:     result.Title,
					URL:       sectionURL(result.URL, currentSectionID),
					Content:   truncateContent(sectionContent.String(), 5000),
					Section:   currentSection,
					SectionID: currentSectionID,
					Tag:       tag,
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
				ID:        hashID(result.URL + "#" + currentSectionID),
				PageID:    pageID,
				Title:     result.Title,
				URL:       sectionURL(result.URL, currentSectionID),
				Content:   truncateContent(sectionContent.String(), 5000),
				Section:   currentSection,
				SectionID: currentSectionID,
				Tag:       tag,
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

			case tag == "a":
				href := getAttr(n, "href")
				if href != "" {
					resolved := resolveHref(href, baseURL)
					if resolved != "" {
						result.Links = append(result.Links, resolved)
					}
				}
			}
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}

	walk(doc)
	result.Content = strings.TrimSpace(contentBuilder.String())
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

// resolveHref resolves a relative or absolute href against the base URL.
func resolveHref(href string, baseURL *url.URL) string {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "javascript:") || strings.HasPrefix(href, "mailto:") {
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
