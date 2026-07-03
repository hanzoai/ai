// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package object

import (
	"encoding/json"
	"testing"
)

// TestMarkdownFieldUnmarshalJSON locks the shape-polymorphic decode of Crawl4AI's
// `markdown` field: a bare string (older builds) OR an object
// {fit_markdown, raw_markdown, ...} (deployed 0.8.x/0.9.x). The object form is
// what silently returned empty content before the fix, so both must decode and
// the object form must prefer fit_markdown with raw_markdown as fallback.
func TestMarkdownFieldUnmarshalJSON(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bare string", `"hello world"`, "hello world"},
		{"object prefers fit", `{"raw_markdown":"RAW","fit_markdown":"FIT"}`, "FIT"},
		{"object falls back to raw", `{"raw_markdown":"RAW","fit_markdown":""}`, "RAW"},
		{"object with extra fields", `{"raw_markdown":"R","fit_markdown":"F","markdown_with_citations":"C"}`, "F"},
		{"empty string", `""`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var m MarkdownField
			if err := json.Unmarshal([]byte(c.in), &m); err != nil {
				t.Fatalf("Unmarshal(%s) error: %v", c.in, err)
			}
			if string(m) != c.want {
				t.Fatalf("Unmarshal(%s) = %q, want %q", c.in, string(m), c.want)
			}
		})
	}
}

// TestCrawl4AIResponseDecodeObjectMarkdown is the regression guard: the deployed
// Hanzo Crawl (Crawl4AI 0.8.x) returns a synchronous envelope with a boolean
// `success` (no top-level status, no task_id) whose per-result `markdown` is an
// OBJECT. The whole response must decode, and CrawlWithCrawl4AI's "inline results
// present" branch must be reachable (len(Results) > 0).
func TestCrawl4AIResponseDecodeObjectMarkdown(t *testing.T) {
	body := `{
		"success": true,
		"results": [{
			"url": "https://example.com",
			"success": true,
			"markdown": {"raw_markdown":"# Raw","fit_markdown":"# Example\n\nClean body."},
			"metadata": {"title":"Example Domain","description":"An example page."}
		}]
	}`
	var resp Crawl4AIResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode object-markdown envelope failed: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("Results len = %d, want 1", len(resp.Results))
	}
	r := resp.Results[0]
	if !r.Success {
		t.Fatalf("per-result Success = false, want true")
	}
	if string(r.Markdown) != "# Example\n\nClean body." {
		t.Fatalf("Markdown = %q, want fit_markdown content", string(r.Markdown))
	}
}

// TestCrawl4AIResultDecodeHeterogeneousLinks guards the real 0.8.6 link-object
// shape: values are mixed types (string href/text, float score, null head_data).
// A map-of-string value errored the whole decode; the interface{} value typing
// must accept it and the href read must still work.
func TestCrawl4AIResultDecodeHeterogeneousLinks(t *testing.T) {
	body := `{
		"url": "https://example.com",
		"success": true,
		"markdown": {"raw_markdown":"# Example Domain","fit_markdown":""},
		"links": {"external": [
			{"href":"https://iana.org/domains/example","text":"Learn more","title":"",
			 "base_domain":"iana.org","head_data":null,"intrinsic_score":0.0,"total_score":null}
		]},
		"metadata": {"title":"Example Domain"}
	}`
	var r Crawl4AIResult
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("decode heterogeneous links failed: %v", err)
	}
	// fit_markdown empty → raw_markdown fallback carries the content.
	if string(r.Markdown) != "# Example Domain" {
		t.Fatalf("Markdown = %q, want raw_markdown fallback", string(r.Markdown))
	}
	// The structured projection lifts href strings out of the interface{} values.
	sr := Crawl4AIResultToScrapeResult(r)
	found := false
	for _, l := range sr.Links {
		if l == "https://iana.org/domains/example" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected external href in ScrapeResult.Links, got %v", sr.Links)
	}
}

// TestCrawl4AIResultToCrawlResult checks the canonical projection backing
// POST /v1/crawl: markdown carried through, title/description lifted out of the
// page metadata, success preserved.
func TestCrawl4AIResultToCrawlResult(t *testing.T) {
	raw := Crawl4AIResult{
		URL:      "https://example.com",
		Markdown: "# Example\n\nBody.",
		Success:  true,
		Metadata: map[string]interface{}{
			"title":       "Example Domain",
			"description": "An example page.",
		},
	}
	got := crawl4AIResultToCrawlResult(raw)
	if got.URL != raw.URL {
		t.Fatalf("URL = %q, want %q", got.URL, raw.URL)
	}
	if got.Markdown != "# Example\n\nBody." {
		t.Fatalf("Markdown = %q", got.Markdown)
	}
	if !got.Success {
		t.Fatalf("Success = false, want true")
	}
	if got.Title != "Example Domain" {
		t.Fatalf("Title = %q, want %q", got.Title, "Example Domain")
	}
	if got.Description != "An example page." {
		t.Fatalf("Description = %q, want %q", got.Description, "An example page.")
	}
}

// TestCrawl4AIResultToCrawlResultNoMetadata guards the nil-metadata path: no
// panic, empty title/description, markdown still carried.
func TestCrawl4AIResultToCrawlResultNoMetadata(t *testing.T) {
	got := crawl4AIResultToCrawlResult(Crawl4AIResult{URL: "https://x.test", Markdown: "hi", Success: true})
	if got.Title != "" || got.Description != "" {
		t.Fatalf("expected empty title/description, got %q/%q", got.Title, got.Description)
	}
	if got.Markdown != "hi" || !got.Success {
		t.Fatalf("markdown/success not carried: %+v", got)
	}
}
