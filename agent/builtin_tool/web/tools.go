// Copyright (c) 2014-present Hanzo AI, Inc.
// Licensed under Apache-2.0. See LICENSE.

package webtools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ThinkInAIXYZ/go-mcp/protocol"
)

// Caps exist so one confused call cannot exhaust an agent's context window or hold
// a turn open indefinitely. They bound the TOOL, not the endpoint.
const (
	maxResults    = 10
	defaultResult = 5
	maxURLs       = 5
	maxMarkdown   = 20_000
)

func text(s string) *protocol.CallToolResult {
	return &protocol.CallToolResult{
		Content: []protocol.Content{&protocol.TextContent{Type: "text", Text: s}},
	}
}

// fail returns a tool ERROR rather than text. The distinction matters: a model reads
// ordinary text as a finding, so returning "no results" for a failure teaches it that
// the web is empty on the subject and it answers from memory instead — a fabricated
// answer delivered confidently. An error is a fact about the tool.
func fail(format string, a ...any) *protocol.CallToolResult {
	return &protocol.CallToolResult{
		IsError: true,
		Content: []protocol.Content{
			&protocol.TextContent{Type: "text", Text: fmt.Sprintf(format, a...)},
		},
	}
}

func stringArg(args map[string]interface{}, key string) string {
	s, _ := args[key].(string)
	return strings.TrimSpace(s)
}

func intArg(args map[string]interface{}, key string, def, max int) int {
	var n int
	switch v := args[key].(type) {
	case float64:
		n = int(v)
	case int:
		n = v
	default:
		return def
	}
	if n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// ── web_search ──────────────────────────────────────────────────────────────

type SearchTool struct{}

func (t *SearchTool) GetName() string { return "web_search" }

func (t *SearchTool) GetDescription() string {
	return "Search the web and return ranked results with titles, URLs and snippets. " +
		"Use it for anything current, anything you are unsure of, and anything you would " +
		"otherwise answer from memory. Follow up with fetch_url to read a result in full."
}

func (t *SearchTool) GetInputSchema() interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "What to search for.",
			},
			"limit": map[string]interface{}{
				"type":        "number",
				"description": fmt.Sprintf("How many results (1-%d, default %d).", maxResults, defaultResult),
			},
		},
		"required": []string{"query"},
	}
}

func (t *SearchTool) Execute(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	q := stringArg(args, "query")
	if q == "" {
		return fail("web_search needs a non-empty query"), nil
	}
	fn := Search()
	if fn == nil {
		return fail("web_search: %v", ErrNotConfigured), nil
	}
	hits, err := fn(ctx, q, intArg(args, "limit", defaultResult, maxResults))
	if err != nil {
		return fail("web_search failed: %v", err), nil
	}
	if len(hits) == 0 {
		// A genuine empty result IS a finding, and is reported as one.
		return text("No results for that query."), nil
	}
	out, err := json.Marshal(hits)
	if err != nil {
		return fail("web_search could not encode its results: %v", err), nil
	}
	return text(string(out)), nil
}

// ── fetch_url ───────────────────────────────────────────────────────────────

type FetchTool struct{}

func (t *FetchTool) GetName() string { return "fetch_url" }

func (t *FetchTool) GetDescription() string {
	return "Fetch one or more web pages and return their content as clean Markdown. " +
		"Use it to read a search result properly instead of relying on its snippet."
}

func (t *FetchTool) GetInputSchema() interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"urls": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": fmt.Sprintf("Absolute http(s) URLs to fetch (max %d).", maxURLs),
			},
		},
		"required": []string{"urls"},
	}
}

func (t *FetchTool) Execute(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	raw, ok := args["urls"].([]interface{})
	if !ok || len(raw) == 0 {
		// A single string is the mistake a model makes most often here; accept it
		// rather than fail on a shape that is unambiguous.
		if one := stringArg(args, "urls"); one != "" {
			raw = []interface{}{one}
		} else {
			return fail("fetch_url needs a non-empty list of urls"), nil
		}
	}
	urls := make([]string, 0, len(raw))
	for _, v := range raw {
		s, _ := v.(string)
		s = strings.TrimSpace(s)
		// Only http(s). A tool that followed file:// or a custom scheme would read
		// the host's own disk on behalf of whoever wrote the page that suggested it.
		if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
			urls = append(urls, s)
		}
		if len(urls) == maxURLs {
			break
		}
	}
	if len(urls) == 0 {
		return fail("fetch_url got no absolute http(s) url to fetch"), nil
	}

	fn := Fetch()
	if fn == nil {
		return fail("fetch_url: %v", ErrNotConfigured), nil
	}
	results, err := fn(ctx, urls)
	if err != nil {
		return fail("fetch_url failed: %v", err), nil
	}
	var b strings.Builder
	for _, r := range results {
		b.WriteString("## ")
		if r.Title != "" {
			b.WriteString(r.Title)
			b.WriteString("\n")
		} else {
			b.WriteString(r.URL)
			b.WriteString("\n")
		}
		b.WriteString(r.URL)
		b.WriteString("\n\n")
		if !r.Success {
			// Named per URL: an agent that reads "could not be fetched" can try
			// another source, where a silent omission looks like an empty page.
			b.WriteString("_This page could not be fetched._\n\n")
			continue
		}
		md := r.Markdown
		if len(md) > maxMarkdown {
			md = md[:maxMarkdown] + "\n\n_[truncated]_"
		}
		b.WriteString(md)
		b.WriteString("\n\n")
	}
	return text(b.String()), nil
}

// ── deep_research ───────────────────────────────────────────────────────────

type ResearchTool struct{}

func (t *ResearchTool) GetName() string { return "deep_research" }

func (t *ResearchTool) GetDescription() string {
	return "Investigate a question across many sources and return a cited report. " +
		"Slower and more expensive than web_search — use it for questions that need " +
		"synthesis across sources, not for a single fact."
}

func (t *ResearchTool) GetInputSchema() interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"question": map[string]interface{}{
				"type":        "string",
				"description": "The question to investigate.",
			},
		},
		"required": []string{"question"},
	}
}

func (t *ResearchTool) Execute(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	q := stringArg(args, "question")
	if q == "" {
		return fail("deep_research needs a non-empty question"), nil
	}
	fn := Research()
	if fn == nil {
		return fail("deep_research: %v", ErrNotConfigured), nil
	}
	report, err := fn(ctx, q)
	if err != nil {
		return fail("deep_research failed: %v", err), nil
	}
	if strings.TrimSpace(report) == "" {
		return fail("deep_research returned nothing"), nil
	}
	return text(report), nil
}
