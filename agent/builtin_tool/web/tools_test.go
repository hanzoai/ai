// Copyright (c) 2014-present Hanzo AI, Inc.
// Licensed under Apache-2.0. See LICENSE.

package webtools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ThinkInAIXYZ/go-mcp/protocol"
)

func body(t *testing.T, r *protocol.CallToolResult) string {
	t.Helper()
	if len(r.Content) == 0 {
		t.Fatal("result carried no content")
	}
	tc, ok := r.Content[0].(*protocol.TextContent)
	if !ok {
		t.Fatalf("content is %T, want *protocol.TextContent", r.Content[0])
	}
	return tc.Text
}

func reset() { SetSearch(nil); SetResearch(nil); SetFetch(nil) }

// AN UNAVAILABLE CAPABILITY MUST BE AN ERROR, NOT AN EMPTY ANSWER.
//
// This is the single most important property here. A model reads ordinary tool text
// as a FINDING: hand it "no results" when the backend was never installed and it
// concludes the web holds nothing on the subject, then answers from memory in a
// confident voice. That is a fabricated answer caused by a deployment gap, and
// nothing downstream can tell it from a real one. Absent and empty are different
// facts and must arrive as different results.
func TestUnconfiguredCapabilityIsAnErrorNotAnEmptyResult(t *testing.T) {
	reset()
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		run  func() (*protocol.CallToolResult, error)
	}{
		{"web_search", func() (*protocol.CallToolResult, error) {
			return (&SearchTool{}).Execute(ctx, map[string]interface{}{"query": "anything"})
		}},
		{"deep_research", func() (*protocol.CallToolResult, error) {
			return (&ResearchTool{}).Execute(ctx, map[string]interface{}{"question": "anything"})
		}},
		{"fetch_url", func() (*protocol.CallToolResult, error) {
			return (&FetchTool{}).Execute(ctx, map[string]interface{}{"urls": []interface{}{"https://example.com"}})
		}},
	} {
		res, err := tc.run()
		if err != nil {
			t.Fatalf("%s returned a transport error: %v", tc.name, err)
		}
		if !res.IsError {
			t.Errorf("%s with no backend returned a NON-error result (%q) — a model reads that "+
				"as a finding and answers from memory", tc.name, body(t, res))
		}
		if !strings.Contains(body(t, res), "not available in this deployment") {
			t.Errorf("%s does not name the deployment as the cause: %q", tc.name, body(t, res))
		}
	}
}

// A genuinely empty search IS a finding, and must NOT be an error — otherwise an
// agent retries a query that correctly returned nothing.
func TestGenuinelyEmptySearchIsAFindingNotAnError(t *testing.T) {
	reset()
	SetSearch(func(context.Context, string, int) ([]SearchResult, error) { return nil, nil })
	res, _ := (&SearchTool{}).Execute(context.Background(), map[string]interface{}{"query": "zzz"})
	if res.IsError {
		t.Error("an empty result set was reported as an error; the search worked and found nothing")
	}
}

func TestSearchReturnsResultsAsJSON(t *testing.T) {
	reset()
	SetSearch(func(_ context.Context, q string, limit int) ([]SearchResult, error) {
		return []SearchResult{{Title: "T", URL: "https://e.com", Snippet: "s"}}, nil
	})
	res, _ := (&SearchTool{}).Execute(context.Background(), map[string]interface{}{"query": "q"})
	var got []SearchResult
	if err := json.Unmarshal([]byte(body(t, res)), &got); err != nil {
		t.Fatalf("results are not JSON a model can parse: %v", err)
	}
	if len(got) != 1 || got[0].URL != "https://e.com" {
		t.Errorf("got %+v", got)
	}
}

func TestSearchLimitIsClampedNotTrusted(t *testing.T) {
	reset()
	var asked int
	SetSearch(func(_ context.Context, _ string, limit int) ([]SearchResult, error) {
		asked = limit
		return nil, nil
	})
	run := func(v interface{}) int {
		asked = 0
		(&SearchTool{}).Execute(context.Background(), map[string]interface{}{"query": "q", "limit": v})
		return asked
	}
	// One confused call must not be able to exhaust the agent's context window.
	if got := run(float64(9999)); got != maxResults {
		t.Errorf("limit 9999 asked for %d, want clamp to %d", got, maxResults)
	}
	if got := run(float64(-3)); got != defaultResult {
		t.Errorf("negative limit asked for %d, want the default %d", got, defaultResult)
	}
	if got := run("not a number"); got != defaultResult {
		t.Errorf("junk limit asked for %d, want the default %d", got, defaultResult)
	}
}

// fetch_url must never follow a non-http(s) scheme. The URLs it is handed come from
// PAGES the model read, so following file:// or a custom scheme would read the host's
// own disk on behalf of whoever wrote that page.
func TestFetchRefusesNonHttpSchemes(t *testing.T) {
	reset()
	var got []string
	SetFetch(func(_ context.Context, urls []string) ([]FetchResult, error) {
		got = urls
		return nil, nil
	})
	res, _ := (&FetchTool{}).Execute(context.Background(), map[string]interface{}{
		"urls": []interface{}{"file:///etc/passwd", "javascript:alert(1)", "ftp://x", "https://ok.com"},
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", body(t, res))
	}
	if len(got) != 1 || got[0] != "https://ok.com" {
		t.Errorf("fetcher was handed %v — only absolute http(s) may pass", got)
	}
}

func TestFetchAcceptsABareStringBecauseModelsSendOne(t *testing.T) {
	reset()
	var got []string
	SetFetch(func(_ context.Context, urls []string) ([]FetchResult, error) { got = urls; return nil, nil })
	(&FetchTool{}).Execute(context.Background(), map[string]interface{}{"urls": "https://one.com"})
	if len(got) != 1 || got[0] != "https://one.com" {
		t.Errorf("a single string url was not accepted: %v", got)
	}
}

func TestFetchCapsTheNumberOfURLs(t *testing.T) {
	reset()
	var got []string
	SetFetch(func(_ context.Context, urls []string) ([]FetchResult, error) { got = urls; return nil, nil })
	many := make([]interface{}, 0, 20)
	for i := 0; i < 20; i++ {
		many = append(many, "https://e.com/")
	}
	(&FetchTool{}).Execute(context.Background(), map[string]interface{}{"urls": many})
	if len(got) != maxURLs {
		t.Errorf("passed %d urls, want a cap of %d", len(got), maxURLs)
	}
}

// A page that failed is NAMED. Silently omitting it looks like an empty page, and an
// agent cannot tell "this source is unreachable" from "this source said nothing".
func TestFetchReportsAFailedPage(t *testing.T) {
	reset()
	SetFetch(func(context.Context, []string) ([]FetchResult, error) {
		return []FetchResult{{URL: "https://gone.com", Success: false}}, nil
	})
	res, _ := (&FetchTool{}).Execute(context.Background(), map[string]interface{}{
		"urls": []interface{}{"https://gone.com"},
	})
	if !strings.Contains(body(t, res), "could not be fetched") {
		t.Errorf("a failed page was not named: %q", body(t, res))
	}
}

func TestBackendErrorSurfacesAsAToolError(t *testing.T) {
	reset()
	SetSearch(func(context.Context, string, int) ([]SearchResult, error) {
		return nil, errors.New("upstream is down")
	})
	res, _ := (&SearchTool{}).Execute(context.Background(), map[string]interface{}{"query": "q"})
	if !res.IsError || !strings.Contains(body(t, res), "upstream is down") {
		t.Errorf("a backend failure did not surface: isError=%v body=%q", res.IsError, body(t, res))
	}
}

func TestEmptyArgumentsAreRefused(t *testing.T) {
	reset()
	SetSearch(func(context.Context, string, int) ([]SearchResult, error) { return nil, nil })
	SetResearch(func(context.Context, string) (string, error) { return "x", nil })
	if res, _ := (&SearchTool{}).Execute(context.Background(), map[string]interface{}{"query": "  "}); !res.IsError {
		t.Error("blank query accepted")
	}
	if res, _ := (&ResearchTool{}).Execute(context.Background(), map[string]interface{}{}); !res.IsError {
		t.Error("missing question accepted")
	}
}
