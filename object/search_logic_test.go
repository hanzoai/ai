// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package object

import (
	"strings"
	"testing"
)

// A crawl scope is a prefix, and a prefix is not a boundary by itself:
// docs.example.com is a prefix of docs.example.com.evil.com. What makes it a
// boundary is what comes next.
func TestWhatCountsAsInsideAScope(t *testing.T) {
	const root = "https://docs.example.com/guide"
	for _, u := range []string{
		root,
		root + "/",
		root + "/page",
		root + "/a/b/c",
		root + "#section",
		root + "?q=1",
		"https://docs.example.com/guide/",
	} {
		if !under(root, u) {
			t.Errorf("%q is inside %q and was not", u, root)
		}
	}
	for _, u := range []string{
		"https://docs.example.com/guides",         // a longer word, not a longer path
		"https://docs.example.com/guide-other",    // ditto
		"https://docs.example.com.evil.com/guide", // the prefix trick
		"https://docs.example.com",                // the parent
		"https://elsewhere.example.com/guide",
		"",
	} {
		if under(root, u) {
			t.Errorf("%q is outside %q and was not", u, root)
		}
	}

	// A trailing slash on the root is not part of the boundary.
	if !under("https://docs.example.com/guide/", "https://docs.example.com/guide/page") {
		t.Error("a root written with a trailing slash rejected its own child")
	}

	// No scope means everything, and nothing is still nothing.
	if !inScope("", "https://anywhere.example.com") {
		t.Error("an unscoped crawl excluded a page")
	}
	if inScope("", "") {
		t.Error("an empty address was called a page")
	}
	if inScope(root, "https://elsewhere.example.com") {
		t.Error("a scoped crawl included a page outside it")
	}
}

// A tag and a file id reach the search as parts of a filter expression, so what
// a caller writes must stay inside the quoted value it was put in.
func TestAFilterValueStaysInsideItsQuotes(t *testing.T) {
	hostile := []string{
		`x' OR tag = 'y`,
		`x\' OR 1=1 --`,
		`' OR file_id EXISTS OR tag = '`,
		`x\\`,
	}
	for _, value := range hostile {
		filter := buildMeiliFilter(value, nil)
		// One opening and one closing quote around one value: nothing the caller
		// wrote reopened the literal.
		if n := strings.Count(filter, "'"); n != 2 {
			t.Errorf("tag %q produced %d quotes: %s", value, n, filter)
		}
		if strings.Contains(filter, `\`) {
			t.Errorf("tag %q left a backslash: %s", value, filter)
		}
	}

	// The same for a list of file ids: two quotes per id and no more.
	filter := buildMeiliFilter("", []string{`a' OR '1`, "b"})
	if n := strings.Count(filter, "'"); n != 4 {
		t.Errorf("two ids produced %d quotes: %s", n, filter)
	}

	// An empty id is left out rather than matching the document called "".
	if f := buildMeiliFilter("", []string{"", "b"}); strings.Contains(f, "''") {
		t.Errorf("an empty id became a value to match: %s", f)
	}
	// Nothing to narrow by is no filter at all.
	if f := buildMeiliFilter("", nil); f != "" {
		t.Errorf("no tag and no ids produced %q", f)
	}
	if f := buildMeiliFilter("", []string{"", ""}); f != "" {
		t.Errorf("only empty ids produced %q", f)
	}
}

// Fusion ranks a document by where it placed in EACH search, so one that both
// found beats one that only one did, however well.
func TestFusingTwoSearches(t *testing.T) {
	byWords := []DocSearchResult{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	byMeaning := []DocSearchResult{{ID: "c"}, {ID: "d"}, {ID: "a"}}

	got := mergeRRF(byWords, byMeaning, 10)
	if len(got) != 4 {
		t.Fatalf("fused to %d documents, want the 4 distinct ones", len(got))
	}
	// a placed 1st and 3rd; c placed 3rd and 1st — both beat b and d, which one
	// search each found.
	top := map[string]bool{got[0].ID: true, got[1].ID: true}
	if !top["a"] || !top["c"] {
		t.Errorf("the two documents both searches found ranked %s, %s", got[0].ID, got[1].ID)
	}

	// The limit is a limit.
	if got := mergeRRF(byWords, byMeaning, 2); len(got) != 2 {
		t.Errorf("a limit of 2 returned %d", len(got))
	}
	// And nothing found is nothing returned, not a panic.
	if got := mergeRRF(nil, nil, 10); len(got) != 0 {
		t.Errorf("two empty searches fused to %d documents", len(got))
	}
}

// One page must not crowd out every other: a document's page is capped in the
// answer so a single long page cannot be the whole of it.
func TestOnePageCannotFillTheAnswer(t *testing.T) {
	many := []DocSearchResult{}
	for i := 0; i < 20; i++ {
		many = append(many, DocSearchResult{ID: string(rune('a' + i)), URL: "https://x/page#anchor"})
	}
	other := []DocSearchResult{{ID: "z", URL: "https://x/elsewhere"}}

	got := mergeRRF(many, other, 20)
	fromPage := 0
	for _, r := range got {
		if strings.HasPrefix(r.URL, "https://x/page") {
			fromPage++
		}
	}
	if fromPage > 7 {
		t.Errorf("%d results came from one page", fromPage)
	}
	found := false
	for _, r := range got {
		if r.ID == "z" {
			found = true
		}
	}
	if !found {
		t.Error("the other page was crowded out entirely")
	}
}
