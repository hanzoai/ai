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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/beego/beego/logs"
	"github.com/hanzoai/cloud/conf"
)

const (
	crawl4aiPollInterval = 2 * time.Second
	crawl4aiPollTimeout  = 5 * time.Minute
	crawl4aiHTTPTimeout  = 30 * time.Second
)

// Crawl4AIRequest is the request body for the Hanzo Crawl /crawl endpoint.
type Crawl4AIRequest struct {
	Urls          []string               `json:"urls"`
	BrowserConfig *Crawl4AIBrowserConfig `json:"browser_config,omitempty"`
	CrawlerParams *Crawl4AICrawlerParams `json:"crawler_params,omitempty"`
}

// Crawl4AIBrowserConfig controls the headless browser settings.
type Crawl4AIBrowserConfig struct {
	Headless bool `json:"headless"`
}

// Crawl4AICrawlerParams controls the crawl extraction behavior.
type Crawl4AICrawlerParams struct {
	WordCountThreshold   int  `json:"word_count_threshold"`
	ExcludeExternalLinks bool `json:"exclude_external_links"`
	ProcessIframes       bool `json:"process_iframes"`
}

// Crawl4AIResponse is the response from the Hanzo Crawl /crawl endpoint.
type Crawl4AIResponse struct {
	TaskID  string           `json:"task_id"`
	Status  string           `json:"status"`
	Results []Crawl4AIResult `json:"results,omitempty"`
}

// Crawl4AIResult holds the crawl output for a single URL.
type Crawl4AIResult struct {
	URL      string                         `json:"url"`
	Markdown string                         `json:"markdown"`
	Success  bool                           `json:"success"`
	Links    map[string][]map[string]string `json:"links,omitempty"`
	Media    map[string][]map[string]string `json:"media,omitempty"`
	Metadata map[string]interface{}         `json:"metadata,omitempty"`
}

// getCrawlEndpoint returns the Hanzo Crawl service base URL from config.
func getCrawlEndpoint() string {
	host := conf.GetConfigString("crawlHost")
	if host == "" {
		host = "crawl.hanzo.svc.cluster.local"
	}
	port := conf.GetConfigString("crawlPort")
	if port == "" {
		port = "11235"
	}
	return fmt.Sprintf("http://%s:%s", host, port)
}

// getCrawlAPIToken returns the optional API token for Hanzo Crawl authentication.
func getCrawlAPIToken() string {
	return conf.GetConfigString("crawlApiToken")
}

// IsCrawl4AIAvailable checks whether the Hanzo Crawl service is reachable.
func IsCrawl4AIAvailable() bool {
	endpoint := getCrawlEndpoint()
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get(endpoint + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
}

// CrawlWithCrawl4AI sends URLs to the Hanzo Crawl service for JS-rendered crawling.
// It submits a crawl job, polls for completion, and returns the results.
func CrawlWithCrawl4AI(urls []string) ([]Crawl4AIResult, error) {
	if len(urls) == 0 {
		return nil, fmt.Errorf("no URLs provided")
	}

	endpoint := getCrawlEndpoint()
	apiToken := getCrawlAPIToken()

	reqBody := Crawl4AIRequest{
		Urls: urls,
		BrowserConfig: &Crawl4AIBrowserConfig{
			Headless: true,
		},
		CrawlerParams: &Crawl4AICrawlerParams{
			WordCountThreshold:   10,
			ExcludeExternalLinks: false,
			ProcessIframes:       false,
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal Hanzo Crawl request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, endpoint+"/crawl", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to build Hanzo Crawl request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+apiToken)
	}

	client := &http.Client{Timeout: crawl4aiHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Hanzo Crawl /crawl request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Hanzo Crawl /crawl returned %d: %s", resp.StatusCode, string(body))
	}

	var crawlResp Crawl4AIResponse
	if err := json.NewDecoder(resp.Body).Decode(&crawlResp); err != nil {
		return nil, fmt.Errorf("failed to decode Hanzo Crawl response: %w", err)
	}

	// If the response already contains completed results, return them directly
	if crawlResp.Status == "completed" && len(crawlResp.Results) > 0 {
		return crawlResp.Results, nil
	}

	// Otherwise poll the task endpoint until completion
	if crawlResp.TaskID == "" {
		return nil, fmt.Errorf("Hanzo Crawl returned no task_id and no results")
	}

	return pollCrawl4AITask(endpoint, apiToken, crawlResp.TaskID)
}

// pollCrawl4AITask polls the Hanzo Crawl /task/{id} endpoint until the job completes or times out.
func pollCrawl4AITask(endpoint, apiToken, taskID string) ([]Crawl4AIResult, error) {
	client := &http.Client{Timeout: crawl4aiHTTPTimeout}
	taskURL := fmt.Sprintf("%s/task/%s", endpoint, taskID)

	deadline := time.Now().Add(crawl4aiPollTimeout)

	for time.Now().Before(deadline) {
		time.Sleep(crawl4aiPollInterval)

		req, err := http.NewRequest(http.MethodGet, taskURL, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to build task poll request: %w", err)
		}
		if apiToken != "" {
			req.Header.Set("Authorization", "Bearer "+apiToken)
		}

		resp, err := client.Do(req)
		if err != nil {
			logs.Warning("Hanzo Crawl: task poll failed (will retry): %v", err)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("Hanzo Crawl /task/%s returned %d: %s", taskID, resp.StatusCode, string(body))
		}

		var taskResp Crawl4AIResponse
		decodeErr := json.NewDecoder(resp.Body).Decode(&taskResp)
		resp.Body.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("failed to decode Hanzo Crawl task response: %w", decodeErr)
		}

		switch taskResp.Status {
		case "completed":
			return taskResp.Results, nil
		case "failed":
			return nil, fmt.Errorf("Hanzo Crawl task %s failed", taskID)
		}
		// "pending" or "processing" -- continue polling
	}

	return nil, fmt.Errorf("Hanzo Crawl task %s timed out after %v", taskID, crawl4aiPollTimeout)
}

// Crawl4AIResultToScrapeResult converts a Hanzo Crawl result to our ScrapeResult format.
// It parses the markdown output to extract title, headings, and structured content blocks.
func Crawl4AIResultToScrapeResult(result Crawl4AIResult) ScrapeResult {
	sr := ScrapeResult{
		URL:     result.URL,
		Content: result.Markdown,
	}

	// Extract title from metadata if available
	if result.Metadata != nil {
		if title, ok := result.Metadata["title"].(string); ok {
			sr.Title = title
		}
		if desc, ok := result.Metadata["description"].(string); ok {
			sr.Description = desc
		}
	}

	// Parse markdown to extract headings and structured content
	lines := strings.Split(result.Markdown, "\n")
	var currentSection string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// Detect heading lines (# H1, ## H2, etc.)
		if strings.HasPrefix(trimmed, "#") {
			level, text := parseMarkdownHeading(trimmed)
			if level > 0 {
				// If no title yet, use first h1
				if sr.Title == "" && level == 1 {
					sr.Title = text
				}

				heading := Heading{
					Level: level,
					ID:    slugify(text),
					Text:  text,
				}
				sr.Headings = append(sr.Headings, heading)
				sr.Structured.Headings = append(sr.Structured.Headings, heading)
				currentSection = text
				continue
			}
		}

		// Detect code blocks
		if strings.HasPrefix(trimmed, "```") {
			continue
		}

		// Detect list items
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || isOrderedListItem(trimmed) {
			sr.Structured.Contents = append(sr.Structured.Contents, ContentBlock{
				Type:    "list",
				Text:    trimmed,
				Section: currentSection,
			})
			continue
		}

		// Everything else is a paragraph
		sr.Structured.Contents = append(sr.Structured.Contents, ContentBlock{
			Type:    "paragraph",
			Text:    trimmed,
			Section: currentSection,
		})
	}

	// Extract links from the Hanzo Crawl links map
	if internalLinks, ok := result.Links["internal"]; ok {
		for _, link := range internalLinks {
			if href, exists := link["href"]; exists && href != "" {
				sr.Links = append(sr.Links, href)
			}
		}
	}
	if externalLinks, ok := result.Links["external"]; ok {
		for _, link := range externalLinks {
			if href, exists := link["href"]; exists && href != "" {
				sr.Links = append(sr.Links, href)
			}
		}
	}

	return sr
}

// parseMarkdownHeading parses a markdown heading line and returns (level, text).
// Returns (0, "") if the line is not a valid heading.
func parseMarkdownHeading(line string) (int, string) {
	level := 0
	for _, ch := range line {
		if ch == '#' {
			level++
		} else {
			break
		}
	}

	if level == 0 || level > 6 {
		return 0, ""
	}

	text := strings.TrimSpace(line[level:])
	if text == "" {
		return 0, ""
	}

	return level, text
}

// isOrderedListItem checks whether a line starts with a numeric ordered list marker (e.g., "1. ").
func isOrderedListItem(line string) bool {
	for i, ch := range line {
		if ch >= '0' && ch <= '9' {
			continue
		}
		if ch == '.' && i > 0 && i < len(line)-1 && line[i+1] == ' ' {
			return true
		}
		return false
	}
	return false
}
