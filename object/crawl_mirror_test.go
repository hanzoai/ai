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

// crawl_mirror_test.go — proofs for the docs grounding ingest being a MIRROR of
// the site it crawls rather than a pile that only grows.
//
// The crawl here is REAL: a site on loopback, fetched by the Go scraper through
// its own BFS, robots check and link extraction. Only the two stores are faked,
// because what is under test is which documents survive a crawl — not whether
// Meilisearch can delete by primary key. Faking the crawl instead would fake the
// exact thing the guard reads.
//
// The property that matters is not "deleted pages disappear". It is that they
// disappear ONLY when the crawl saw the whole site. A crawl that was cut short
// saw part of a site, and a page missing from part of a site has not been
// removed from it — so these tests spend most of their length proving the
// absence of a delete.

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// a site that can lose a page
// ---------------------------------------------------------------------------

type site struct {
	mu    sync.Mutex
	pages map[string]string // path -> section heading; "" once removed
	fail  map[string]bool   // path -> answer 500
	srv   *httptest.Server
}

// serve starts a site whose index links every page, so the crawl discovers them
// the way it discovers a real docs site: by following links.
func serve(t *testing.T, paths ...string) *site {
	t.Helper()
	s := &site{pages: map[string]string{}, fail: map[string]bool{}}
	for _, p := range paths {
		s.pages[p] = "Section " + strings.TrimPrefix(p, "/")
	}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		if s.fail[r.URL.Path] {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		if r.URL.Path == "/" {
			var links strings.Builder
			for p := range s.pages {
				fmt.Fprintf(&links, `<a href="%s">%s</a>`, p, p)
			}
			// The index keeps linking a page that answers 500: a page that is
			// unreachable is not a page that was taken down.
			for p := range s.fail {
				fmt.Fprintf(&links, `<a href="%s">%s</a>`, p, p)
			}
			fmt.Fprintf(w, `<html><head><title>Index</title></head><body>
				<h2>Home</h2><p>the index page</p>%s</body></html>`, links.String())
			return
		}
		heading, ok := s.pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `<html><head><title>%s</title></head><body>
			<h2>%s</h2><p>body of %s</p><a href="/">home</a></body></html>`,
			r.URL.Path, heading, r.URL.Path)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *site) remove(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pages, path)
}

func (s *site) breaks(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pages, path)
	s.fail[path] = true
}

// docIDs are the ids the ingest gives the page at path: the page itself and its
// one section. Computed the way the ingest computes them, from the URL.
func (s *site) docIDs(path string) (page string, section string) {
	u := s.srv.URL + path
	if path == "/" {
		u = s.srv.URL
		return hashID(u), hashID(u + "#" + slugify("Home"))
	}
	return hashID(u), hashID(u + "#" + slugify("Section "+strings.TrimPrefix(path, "/")))
}

// ---------------------------------------------------------------------------
// two stores that remember what was deleted
// ---------------------------------------------------------------------------

type stores struct {
	keyword map[string]DocIndex // id -> doc, the authoritative store
	vector  map[string]DocIndex // id -> doc, the semantic store
	cut     []string            // ids deleted, in order, across both
	vecErr  error               // when set, every vector prune fails
	peaks   map[string]int      // (tag,root) -> largest that corpus has been
}

func fakeStores(t *testing.T) *stores {
	t.Helper()
	f := &stores{keyword: map[string]DocIndex{}, vector: map[string]DocIndex{}, peaks: map[string]int{}}

	origIndex, origVec := indexSink, vectorSink
	origStale, origPrune, origVecPrune := staleSink, pruneSink, vectorPruneSink
	origPeak, origPeakReset, origPeaks := peakSink, peakResetSink, peaksSink

	indexSink = func(_ string, docs []DocIndex, replace bool) (int, error) {
		if replace {
			f.keyword = map[string]DocIndex{}
		}
		for _, d := range docs {
			f.keyword[d.ID] = d
		}
		return len(docs), nil
	}
	vectorSink = func(_ string, docs []DocIndex, replace bool, _ string) error {
		if replace {
			f.vector = map[string]DocIndex{}
		}
		for _, d := range docs {
			f.vector[d.ID] = d
		}
		return nil
	}
	// The same set difference the real one computes, over the same authoritative
	// store, held to the same two bounds.
	staleSink = func(_ string, m *Mirror, live map[string]bool, scopes []string) (extent, error) {
		var out extent
		pages := map[string]bool{}
		outside := map[string]map[string]bool{}
		for _, sc := range scopes {
			outside[sc] = map[string]bool{}
		}
		for id, d := range f.keyword {
			if d.Tag != m.Tag {
				continue
			}
			page := d.PageID
			if page == "" {
				page = id
			}
			if !under(m.Root, d.URL) {
				for _, sc := range scopes {
					if inScope(sc, d.URL) {
						outside[sc][page] = true
					}
				}
				continue
			}
			out.docs++
			pages[page] = true
			if !live[id] {
				out.stale = append(out.stale, id)
			}
		}
		out.pages = len(pages)
		out.outside = map[string]int{}
		for sc, set := range outside {
			out.outside[sc] = len(set)
		}
		return out, nil
	}
	pruneSink = func(_ string, ids []string) error {
		for _, id := range ids {
			delete(f.keyword, id)
			f.cut = append(f.cut, id)
		}
		return nil
	}
	vectorPruneSink = func(_ string, ids []string) error {
		if f.vecErr != nil {
			return f.vecErr
		}
		for _, id := range ids {
			delete(f.vector, id)
		}
		return nil
	}

	// The peak persists across passes within a test, which is the whole point:
	// it is the only thing that can see a sequence of crawls.
	peakSink = func(_ string, tag, root string, pages int) (int, error) {
		k := tag + "\n" + root
		if pages > f.peaks[k] {
			f.peaks[k] = pages
		}
		return f.peaks[k], nil
	}
	peakResetSink = func(string) error {
		f.peaks = map[string]int{}
		return nil
	}
	peaksSink = func(_ string, tag string) ([]peak, error) {
		var out []peak
		for k, pages := range f.peaks {
			t, root, found := strings.Cut(k, "\n")
			if !found || t != tag {
				continue
			}
			out = append(out, peak{Tag: t, Root: root, Pages: pages})
		}
		return out, nil
	}

	t.Cleanup(func() {
		indexSink, vectorSink = origIndex, origVec
		staleSink, pruneSink, vectorPruneSink = origStale, origPrune, origVecPrune
		peakSink, peakResetSink, peaksSink = origPeak, origPeakReset, origPeaks
	})
	return f
}

func (f *stores) has(t *testing.T, id, why string) {
	t.Helper()
	if _, ok := f.keyword[id]; !ok {
		t.Errorf("%s: gone from the keyword store", why)
	}
	if _, ok := f.vector[id]; !ok {
		t.Errorf("%s: gone from the vector store", why)
	}
}

func (f *stores) lacks(t *testing.T, id, why string) {
	t.Helper()
	if _, ok := f.keyword[id]; ok {
		t.Errorf("%s: still in the keyword store", why)
	}
	if _, ok := f.vector[id]; ok {
		t.Errorf("%s: still in the vector store", why)
	}
}

// crawl runs the real ingest against the site with the Go scraper.
func crawl(t *testing.T, s *site, tag string, maxPages int) *ScrapeStats {
	t.Helper()
	stats, err := ScrapeAndIndex("acme", &ScrapeRequest{
		URL:      s.srv.URL,
		Engine:   "fast",
		Tag:      tag,
		MaxPages: maxPages,
	}, "en")
	if err != nil {
		t.Fatalf("ScrapeAndIndex: %v", err)
	}
	return stats
}

// ---------------------------------------------------------------------------
// (a) a page taken off the site stops being retrievable
// ---------------------------------------------------------------------------

func TestMirrorRemovesDeletedPage(t *testing.T) {
	f := fakeStores(t)
	s := serve(t, "/a", "/b")

	crawl(t, s, "docs", 0)
	gonePage, goneSection := s.docIDs("/b")
	keptPage, keptSection := s.docIDs("/a")
	f.has(t, gonePage, "/b page doc after the first crawl")
	f.has(t, goneSection, "/b section doc after the first crawl")

	s.remove("/b")
	crawl(t, s, "docs", 0)

	f.lacks(t, gonePage, "page doc of the deleted page")
	f.lacks(t, goneSection, "section doc of the deleted page")
	f.has(t, keptPage, "page doc of a page that still exists")
	f.has(t, keptSection, "section doc of a page that still exists")
	if _, ok := f.keyword[mustID(t, s, "/")]; !ok {
		t.Error("the index page was pruned")
	}
}

func mustID(t *testing.T, s *site, path string) string {
	t.Helper()
	page, _ := s.docIDs(path)
	return page
}

// ---------------------------------------------------------------------------
// (b) THE GUARD — a crawl that saw part of a site removes nothing
// ---------------------------------------------------------------------------

func TestFetchErrorNeverPrunes(t *testing.T) {
	f := fakeStores(t)
	s := serve(t, "/a", "/b")
	crawl(t, s, "docs", 0)
	page, section := s.docIDs("/b")

	// /b now answers 500 and is still linked. It is unreachable, not removed.
	s.breaks("/b")
	stats := crawl(t, s, "docs", 0)

	if len(stats.Errors) == 0 {
		t.Fatal("expected the failed fetch to be reported; the guard reads this")
	}
	f.has(t, page, "page doc behind a failing fetch")
	f.has(t, section, "section doc behind a failing fetch")
	if len(f.cut) != 0 {
		t.Errorf("a crawl with %d fetch error(s) deleted %v", len(stats.Errors), f.cut)
	}
}

func TestTruncatedCrawlNeverPrunes(t *testing.T) {
	f := fakeStores(t)
	s := serve(t, "/a", "/b")
	crawl(t, s, "docs", 0)
	before := len(f.keyword)

	// Two pages of a three-page site. Every page not reached is still there.
	crawl(t, s, "docs", 2)

	if len(f.cut) != 0 {
		t.Errorf("a crawl truncated by the page budget deleted %v", f.cut)
	}
	if len(f.keyword) != before {
		t.Errorf("keyword store went from %d to %d docs on a truncated crawl", before, len(f.keyword))
	}
}

func TestEmptyCrawlNeverPrunes(t *testing.T) {
	f := fakeStores(t)
	s := serve(t, "/a")
	crawl(t, s, "docs", 0)
	before := len(f.keyword)

	// The whole site is unreachable. That is an outage, not a deletion.
	s.srv.Close()
	crawl(t, s, "docs", 0)

	if len(f.cut) != 0 {
		t.Errorf("a crawl that reached nothing deleted %v", f.cut)
	}
	if len(f.keyword) != before {
		t.Errorf("keyword store went from %d to %d docs when the site was down", before, len(f.keyword))
	}
}

// ---------------------------------------------------------------------------
// (c) an unchanged site is a no-op
// ---------------------------------------------------------------------------

func TestUnchangedCrawlDeletesNothing(t *testing.T) {
	f := fakeStores(t)
	s := serve(t, "/a", "/b")
	crawl(t, s, "docs", 0)
	first := len(f.keyword)

	crawl(t, s, "docs", 0)

	if len(f.cut) != 0 {
		t.Errorf("re-crawling an unchanged site deleted %v", f.cut)
	}
	if len(f.keyword) != first {
		t.Errorf("re-crawling an unchanged site changed the corpus: %d -> %d", first, len(f.keyword))
	}
}

// ---------------------------------------------------------------------------
// (d) one tag's mirror never reaches another's documents
// ---------------------------------------------------------------------------

func TestMirrorLeavesOtherTagsAlone(t *testing.T) {
	f := fakeStores(t)
	// Another source already lives in this index: an uploaded file, a second
	// site, anything. It is not part of the crawl and must survive it.
	other := DocIndex{ID: "other-1", PageID: "other-1", Title: "Handbook", Content: "x", Tag: "handbook", URL: "https://elsewhere/1"}
	untagged := DocIndex{ID: "untagged-1", PageID: "untagged-1", Title: "Loose", Content: "y", URL: "https://elsewhere/2"}
	f.keyword[other.ID], f.vector[other.ID] = other, other
	f.keyword[untagged.ID], f.vector[untagged.ID] = untagged, untagged

	s := serve(t, "/a", "/b")
	crawl(t, s, "docs", 0)
	s.remove("/b")
	crawl(t, s, "docs", 0)

	gone, _ := s.docIDs("/b")
	f.lacks(t, gone, "the deleted page")
	f.has(t, other.ID, "a document under another tag")
	f.has(t, untagged.ID, "a document carrying no tag at all")
}

// ---------------------------------------------------------------------------
// the refusals, directly
// ---------------------------------------------------------------------------

func TestMirrorRefusesTagNoDocumentCarries(t *testing.T) {
	docs := []DocIndex{{ID: "1", Tag: "a", URL: "https://h/x"}, {ID: "2", Tag: "a", URL: "https://h/y"}}
	if _, err := mirrorStale("idx", &Mirror{Tag: "b", Root: "https://h"}, docs); err == nil {
		t.Fatal("mirroring a tag no document carries would delete every document under it")
	}
}

func TestMirrorRefusesMissingBound(t *testing.T) {
	docs := []DocIndex{{ID: "1", Tag: "a", URL: "https://h/x"}}
	for _, m := range []*Mirror{{Tag: "a"}, {Root: "https://h"}, {}} {
		if _, err := mirrorStale("idx", m, docs); err == nil {
			t.Fatalf("mirror %+v has no bound on one side and was accepted", m)
		}
	}
}

func TestUnderStopsAtTheBoundary(t *testing.T) {
	root := "https://h.ai/docs"
	for _, in := range []string{root, root + "/a", root + "#s", root + "?q=1", root + "/"} {
		if !under(root, in) {
			t.Errorf("under(%q, %q) = false, want true", root, in)
		}
	}
	for _, out := range []string{"https://h.ai/docsearch", "https://h.ai/blog", "https://h.ai", "https://evil.ai/docs"} {
		if under(root, out) {
			t.Errorf("under(%q, %q) = true, want false", root, out)
		}
	}
}

func TestMirrorScopesLiveSetToItsOwnTag(t *testing.T) {
	f := fakeStores(t)
	// A document under tag "b" rides along in a write mirroring tag "a". It
	// must not count as live for "a", or a mixed write silently spares rows.
	f.keyword["keep"] = DocIndex{ID: "keep", Tag: "a", URL: "https://h/1"}
	f.keyword["drop"] = DocIndex{ID: "drop", Tag: "a", URL: "https://h/2"}
	stale, err := mirrorStale("idx", &Mirror{Tag: "a", Root: "https://h"},
		[]DocIndex{{ID: "keep", Tag: "a", URL: "https://h/1"}, {ID: "drop", Tag: "b", URL: "https://h/2"}})
	if err != nil {
		t.Fatalf("mirrorStale: %v", err)
	}
	if len(stale) != 1 || stale[0] != "drop" {
		t.Fatalf("stale = %v, want [drop]: a doc tagged b cannot keep a row alive under tag a", stale)
	}
	_ = f
}

func TestStaleRefusesUnboundedMirror(t *testing.T) {
	if _, err := staleTagIDs("idx", &Mirror{Root: "https://h"}, map[string]bool{"x": true}, nil); err == nil {
		t.Fatal("an empty tag bounds a prune to nothing, so it must be refused before it reaches the store")
	}
	if _, err := staleTagIDs("idx", &Mirror{Tag: "a"}, map[string]bool{"x": true}, nil); err == nil {
		t.Fatal("an empty root bounds a prune to nothing, so it must be refused before it reaches the store")
	}
}

// A failed vector delete must leave the keyword store alone: the ids to remove
// are READ from the keyword store, so deleting them there first would erase the
// only record of what the vector store still owes.
func TestVectorPruneFailureLeavesKeywordIntact(t *testing.T) {
	f := fakeStores(t)
	f.vecErr = fmt.Errorf("vector unreachable")
	s := serve(t, "/a", "/b")
	crawl(t, s, "docs", 0)
	s.remove("/b")
	crawl(t, s, "docs", 0)

	gone, _ := s.docIDs("/b")
	if _, ok := f.keyword[gone]; !ok {
		t.Error("keyword store pruned while the vector store still held the doc; the next crawl can no longer find it to retry")
	}
	if len(f.cut) != 0 {
		t.Errorf("keyword deletes ran despite the vector failure: %v", f.cut)
	}

	// With the vector store back, the same crawl converges.
	f.vecErr = nil
	crawl(t, s, "docs", 0)
	if _, ok := f.keyword[gone]; ok {
		t.Error("the retry did not converge; the deleted page is still cited")
	}
}

// ---------------------------------------------------------------------------
// the crawl itself — what the guard reads
// ---------------------------------------------------------------------------

// A crawl must return the pages it fetched. This one used to close its queue on
// a count of RUNNING pages, which is zero before the first page is picked up, so
// it returned while its own fetches were still in flight: no pages, no errors,
// nothing to say anything had gone wrong. Every "fast" crawl indexed nothing —
// and a crawl that reports "no pages, no errors" is exactly the shape a mirror
// must never read as "the site is empty now". Run repeatedly: it was a race, and
// a race that passes once has not been fixed.
func TestCrawlReturnsEveryPageItFetched(t *testing.T) {
	s := serve(t, "/a", "/b")
	for i := 0; i < 5; i++ {
		results, errs, cut := crawlWithGoScraper(&ScrapeRequest{URL: s.srv.URL})
		if len(errs) != 0 {
			t.Fatalf("run %d: unexpected errors %v", i, errs)
		}
		if cut != "" {
			t.Fatalf("run %d: whole site reachable, but the crawl reports cut=%q", i, cut)
		}
		if len(results) != 3 {
			t.Fatalf("run %d: got %d pages, want 3 (index, /a, /b)", i, len(results))
		}
	}
}

// The budget is a truncation, and a truncation must say so — it is the only
// thing standing between "we stopped early" and "those pages are gone".
func TestPageBudgetIsReported(t *testing.T) {
	s := serve(t, "/a", "/b")
	results, _, cut := crawlWithGoScraper(&ScrapeRequest{URL: s.srv.URL, MaxPages: 2})
	if cut == "" {
		t.Fatalf("crawl stopped at %d of 3 pages and reported no truncation", len(results))
	}
}

// The depth limit leaves pages linked-but-unread, and leaves NO error behind. A
// guard reading only the error list would prune every one of them.
func TestDepthLimitIsReported(t *testing.T) {
	// A chain: / -> /a -> /b. At depth 1, /b is known and never read.
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next := map[string]string{"/": "/a", "/a": "/b", "/b": ""}[r.URL.Path]
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `<html><head><title>%s</title></head><body><h2>H</h2><p>t</p><a href="%s">n</a></body></html>`,
			r.URL.Path, next)
	}))
	defer srv.Close()

	results, errs, cut := crawlWithGoScraper(&ScrapeRequest{URL: srv.URL, Depth: 1})
	if len(errs) != 0 {
		t.Fatalf("no fetch failed, yet errors were reported: %v", errs)
	}
	// The reason is derived, not enumerated: /b was discovered and never read, and
	// that alone is what withholds the mirror. Nothing failed, so the error list is
	// empty — a guard reading only that list would have pruned /b.
	if cut == "" {
		t.Fatalf("read %d pages with /b linked but never reached, and reported completeness", len(results))
	}
	t.Logf("cut = %q", cut)
}

// Two docs sets on ONE host share a hostname tag, because that is what a crawl
// tags with when it is asked for no tag. Mirroring the blog must not empty the
// docs. This is the failure a tag-only bound would ship: not a hostile caller,
// just two crawls of the same site.
func TestMirrorLeavesSiblingPathsAlone(t *testing.T) {
	f := fakeStores(t)
	live := map[string]bool{"/docs": true, "/docs/a": true, "/blog": true, "/blog/a": true}
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		ok := live[r.URL.Path]
		mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		// A section page links onward; a leaf links nowhere, so each crawl ends
		// inside its own subtree and counts as having seen the whole of it.
		var link string
		mu.Lock()
		if live[r.URL.Path+"/a"] {
			link = fmt.Sprintf(`<a href="%s/a">next</a>`, r.URL.Path)
		}
		mu.Unlock()
		fmt.Fprintf(w, `<html><head><title>%s</title></head><body><h2>H</h2><p>t</p>%s</body></html>`,
			r.URL.Path, link)
	}))
	defer srv.Close()

	// No tag asked for, so both crawls land on the same hostname tag.
	crawlFrom := func(path string) {
		t.Helper()
		if _, err := ScrapeAndIndex("acme", &ScrapeRequest{URL: srv.URL + path, Engine: "fast"}, "en"); err != nil {
			t.Fatalf("crawl %s: %v", path, err)
		}
	}
	crawlFrom("/docs")
	crawlFrom("/blog")
	if len(f.keyword) != 8 { // 4 pages, each a page doc and a section doc
		t.Fatalf("expected 8 docs across both subtrees, got %d", len(f.keyword))
	}
	docsPage := hashID(srv.URL + "/docs")
	docsLeaf := hashID(srv.URL + "/docs/a")

	// The blog loses a page. Re-mirroring the blog must touch only the blog.
	mu.Lock()
	delete(live, "/blog/a")
	mu.Unlock()
	crawlFrom("/blog")

	f.lacks(t, hashID(srv.URL+"/blog/a"), "the deleted blog page")
	f.has(t, docsPage, "a docs page sharing the blog's tag")
	f.has(t, docsLeaf, "a docs leaf sharing the blog's tag")
	f.has(t, hashID(srv.URL+"/blog"), "the blog page that still exists")
}

// ---------------------------------------------------------------------------
// completeness — proved from what was READ, not from a list of known failures
// ---------------------------------------------------------------------------

// The decision to mirror comes from the difference between discovered and read.
// No reason is recorded here at all: an unread page is disqualifying by itself,
// which is what makes a truncation nobody enumerated fail safe.
func TestFrontierWithholdsMirrorOnAnyUnreadPage(t *testing.T) {
	f := newFrontier()
	f.discover("https://h/a")
	f.discover("https://h/b")
	f.readPage("https://h/a")
	if f.cut() == "" {
		t.Fatal("one discovered page went unread and the crawl still called itself whole")
	}
	f.readPage("https://h/b")
	if got := f.cut(); got != "" {
		t.Fatalf("every discovered page was read, yet cut = %q", got)
	}
}

// Links live where a docs site puts them: the sidebar tree, the TOC list, the
// prose, the footer. Text extraction stops at or skips every one of these
// containers, so discovery must not ride on it. Each case here made two live
// pages invisible while the crawl reported itself whole.
func TestLinksAreFoundInEveryContainer(t *testing.T) {
	cases := []struct{ name, open, close string }{
		{"bare", "", ""},
		{"nav", "<nav>", "</nav>"},
		{"paragraph", "<p>see ", "</p>"},
		{"unordered list", "<ul><li>", "</li></ul>"},
		{"ordered list", "<ol><li>", "</li></ol>"},
		{"header", "<header>", "</header>"},
		{"footer", "<footer>", "</footer>"},
		{"aside", "<aside>", "</aside>"},
		{"preformatted", "<pre>", "</pre>"},
		{"sidebar tree", "<nav><ul><li>", "</li></ul></nav>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/":
					fmt.Fprintf(w, `<html><head><title>Index</title></head><body>
						<h2>Home</h2>%s<a href="/a">a</a><a href="/b">b</a>%s</body></html>`, tc.open, tc.close)
				case "/a", "/b":
					fmt.Fprintf(w, `<html><head><title>%s</title></head><body>
						<h2>Section %s</h2><p>body</p></body></html>`, r.URL.Path, r.URL.Path[1:])
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			results, errs, cut := crawlWithGoScraper(&ScrapeRequest{URL: srv.URL})
			if len(errs) != 0 {
				t.Fatalf("unexpected fetch errors: %v", errs)
			}
			if len(results) != 3 {
				t.Fatalf("links inside <%s> were not followed: read %d of 3 pages", tc.name, len(results))
			}
			if cut != "" {
				t.Fatalf("whole site read, yet cut = %q", cut)
			}
		})
	}
}

// End-to-end, and the one that mattered: a live 200-OK corpus deleted by a pure
// markup change. No page removed, no fetch failed, no budget or depth hit — the
// index page's links merely moved into a <ul>. Both pages must survive.
func TestMarkupChangeNeverPrunesLiveCorpus(t *testing.T) {
	f := fakeStores(t)
	var mu sync.Mutex
	wrapped := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inList := wrapped
		mu.Unlock()
		switch r.URL.Path {
		case "/docs":
			open, closing := "", ""
			if inList {
				open, closing = "<ul><li>", "</li></ul>"
			}
			fmt.Fprintf(w, `<html><head><title>Docs</title></head><body><h2>Home</h2>
				%s<a href="/docs/a">a</a><a href="/docs/b">b</a>%s</body></html>`, open, closing)
		case "/docs/a", "/docs/b":
			fmt.Fprintf(w, `<html><head><title>%s</title></head><body>
				<h2>Section %s</h2><p>body</p></body></html>`, r.URL.Path, r.URL.Path[6:])
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	run := func(label string) {
		t.Helper()
		if _, err := ScrapeAndIndex("acme", &ScrapeRequest{
			URL: srv.URL + "/docs", Engine: "fast", Tag: "docs",
		}, "en"); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
	}
	run("bare links")
	aPage, bPage := hashID(srv.URL+"/docs/a"), hashID(srv.URL+"/docs/b")
	f.has(t, aPage, "/docs/a after the first crawl")

	mu.Lock()
	wrapped = true // the ONLY change: the same two links, now inside a <ul>
	mu.Unlock()
	run("same links inside a <ul>")

	f.has(t, aPage, "/docs/a, still served, after a markup-only change")
	f.has(t, bPage, "/docs/b, still served, after a markup-only change")
	if len(f.cut) != 0 {
		t.Errorf("a markup change deleted live pages: %v", f.cut)
	}
}

// Root-relative hrefs are what a docs site actually emits. Unresolved, they carry
// no hostname, fail the same-domain test and are dropped — so the crawl would
// call itself whole having read one page.
func TestRootRelativeLinksResolveInBothEngines(t *testing.T) {
	base, err := url.Parse("https://docs.example.com/guide/install")
	if err != nil {
		t.Fatal(err)
	}
	sr := Crawl4AIResultToScrapeResult(Crawl4AIResult{
		URL:     "https://docs.example.com/guide/install",
		Success: true,
		Links: map[string][]map[string]interface{}{
			"internal": {{"href": "/guide/config"}, {"href": "../reference"}, {"href": "#anchor"}},
		},
	})
	if len(sr.Links) != 2 {
		t.Fatalf("browser engine resolved %d links, want 2: %v", len(sr.Links), sr.Links)
	}
	for _, l := range sr.Links {
		u, parseErr := url.Parse(l)
		if parseErr != nil || u.Hostname() != base.Hostname() {
			t.Errorf("link %q did not resolve onto the page's host", l)
		}
	}
	if sr.Dropped != 0 {
		t.Errorf("a fragment is not a dropped page link, but Dropped = %d", sr.Dropped)
	}
}

// ---------------------------------------------------------------------------
// the backstop
// ---------------------------------------------------------------------------

func TestMirrorLimitRefusesMostOfACorpus(t *testing.T) {
	m := &Mirror{Tag: "docs", Root: "https://h/docs"}
	// Ordinary churn on a small corpus stays allowed: the ratio is meaningless
	// down here, and refusing it would leave a small site unable to converge.
	for _, ok := range []struct{ stale, stored int }{{1, 2}, {3, 5}, {8, 16}, {60, 200}, {400, 1000}} {
		if err := mirrorLimit(ok.stale, ok.stored, m); err != nil {
			t.Errorf("removing %d of %d refused: %v", ok.stale, ok.stored, err)
		}
	}
	// A pass claiming most of a corpus vanished is describing a broken crawl.
	for _, bad := range []struct{ stale, stored int }{{8, 9}, {8, 10}, {9, 10}, {150, 200}, {495, 500}, {999, 1000}} {
		if err := mirrorLimit(bad.stale, bad.stored, m); err == nil {
			t.Errorf("removing %d of %d in one pass was allowed", bad.stale, bad.stored)
		}
	}
}

// The backstop is defence in depth, so it must hold through the real path even
// when every signal above it says the crawl was fine.
func TestBackstopHoldsWhenEverySignalSaysComplete(t *testing.T) {
	f := fakeStores(t)
	m := &Mirror{Tag: "docs", Root: "https://h/docs"}
	live := []DocIndex{{ID: "keep", PageID: "keep", Tag: "docs", URL: "https://h/docs/keep"}}
	for i := 0; i < 200; i++ {
		id := fmt.Sprintf("stored-%d", i)
		f.keyword[id] = DocIndex{ID: id, PageID: id, Tag: "docs", URL: "https://h/docs/" + id}
	}
	f.keyword["keep"] = live[0]
	if _, err := mirrorStale("idx", m, live); err == nil {
		t.Fatal("a crawl offering 1 document against 201 stored was allowed to prune the rest")
	}
}

// ---------------------------------------------------------------------------
// discovery is not provable from the inside — the store is asked instead
// ---------------------------------------------------------------------------

// The frontier proves a crawl READ everything it discovered. Nothing proves it
// DISCOVERED everything, because a crawl only sees the links its source
// reported. Each body here carries the same two links in a form the extractor
// cannot follow, so the crawl reads one page, reports no unread URLs and no
// errors, and looks flawless. The corpus must survive it anyway.
func TestUnfollowableLinksNeverPruneTheCorpus(t *testing.T) {
	cases := []struct{ name, body string }{
		{"anchor (control)", `<a href="/a">a</a><a href="/b">b</a>`},
		{"no links at all (SPA shell)", `<div id="root"></div>`},
		{"area href (image map)", `<map><area href="/a"><area href="/b"></map>`},
		{"iframe src", `<iframe src="/a"></iframe><iframe src="/b"></iframe>`},
		{"link rel", `<link rel="next" href="/a"><link rel="prev" href="/b">`},
		{"data-href (JS router)", `<div data-href="/a"></div><div data-href="/b"></div>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := fakeStores(t)
			var mu sync.Mutex
			body := `<a href="/a">a</a><a href="/b">b</a>` // build the corpus first
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				b := body
				mu.Unlock()
				if r.URL.Path == "/" {
					fmt.Fprintf(w, `<html><head><title>I</title></head><body><h2>H</h2>%s</body></html>`, b)
					return
				}
				fmt.Fprintf(w, `<html><head><title>%s</title></head><body><h2>S%s</h2><p>b</p></body></html>`,
					r.URL.Path, r.URL.Path[1:])
			}))
			defer srv.Close()

			run := func() {
				t.Helper()
				if _, err := ScrapeAndIndex("acme", &ScrapeRequest{URL: srv.URL, Engine: "fast", Tag: "docs"}, "en"); err != nil {
					t.Fatalf("ScrapeAndIndex: %v", err)
				}
			}
			run()
			aPage, bPage := hashID(srv.URL+"/a"), hashID(srv.URL+"/b")
			f.has(t, aPage, "/a after the seeding crawl")

			mu.Lock()
			body = tc.body // the links become invisible to the extractor
			mu.Unlock()
			run()

			f.has(t, aPage, "/a, still served, after the links became unfollowable")
			f.has(t, bPage, "/b, still served, after the links became unfollowable")
			if len(f.cut) != 0 {
				t.Errorf("a one-page view of a three-page site deleted %v", f.cut)
			}
		})
	}
}

// Every spelling still inside the mirror's root must clear the same-domain check,
// or it would be prunable while being invisible to the frontier. This is the
// lemma the whole inversion rests on: in-bounds implies discoverable.
func TestInBoundsImpliesDiscovered(t *testing.T) {
	for _, root := range []string{"https://h.ai/docs", "https://h.ai:8443/docs", "http://[::1]:8080/docs"} {
		ru, err := url.Parse(root)
		if err != nil {
			t.Fatalf("bad root %q: %v", root, err)
		}
		for _, suffix := range []string{"/a", "/a/b", "?q=1", "#s", "/A", "/%2E%2E", "/a?x=1#f"} {
			u := root + suffix
			if !under(root, u) {
				continue // out of bounds; the mirror cannot touch it
			}
			p, perr := url.Parse(u)
			if perr != nil {
				t.Errorf("in-bounds %q does not parse: %v", u, perr)
				continue
			}
			if p.Hostname() != ru.Hostname() {
				t.Errorf("in-bounds %q resolves off-host (%q != %q): prunable but never discovered",
					u, p.Hostname(), ru.Hostname())
			}
		}
	}
}

// A crawl that saw a small share of the pages the store holds is a crawl that
// could not see them, not a site that lost them. No floor: it holds at three
// pages and at three thousand.
func TestCoverageRefusesAThinCrawl(t *testing.T) {
	const m = "https://h/docs"
	for _, ok := range []struct{ live, stored int }{{1, 1}, {1, 2}, {2, 3}, {2, 4}, {3, 5}, {50, 100}, {400, 500}} {
		if err := mirrorCoverage(ok.live, ok.stored, m); err != nil {
			t.Errorf("covering %d of %d pages refused: %v", ok.live, ok.stored, err)
		}
	}
	for _, bad := range []struct{ live, stored int }{{1, 3}, {1, 4}, {1, 500}, {2, 5}, {40, 100}} {
		if err := mirrorCoverage(bad.live, bad.stored, m); err == nil {
			t.Errorf("covering only %d of %d pages was allowed to prune the rest", bad.live, bad.stored)
		}
	}
}

// ---------------------------------------------------------------------------
// the browser engine, driven for real against a fake crawl service
// ---------------------------------------------------------------------------

type fakeCrawl struct {
	srv   *httptest.Server
	reply func(urls []string) []Crawl4AIResult
	got   [][]string
}

// startFakeCrawl stands up a crawl service the browser engine will really talk
// to. Without it the auto path — the one production takes whenever crawl4ai is
// reachable — could not be exercised at all.
func startFakeCrawl(t *testing.T, reply func([]string) []Crawl4AIResult) *fakeCrawl {
	t.Helper()
	fc := &fakeCrawl{reply: reply}
	fc.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		var req Crawl4AIRequest
		json.NewDecoder(r.Body).Decode(&req)
		fc.got = append(fc.got, []string(req.Urls))
		json.NewEncoder(w).Encode(Crawl4AIResponse{
			Success: true, Status: "completed", Results: fc.reply([]string(req.Urls)),
		})
	}))
	t.Cleanup(fc.srv.Close)
	host := strings.TrimPrefix(fc.srv.URL, "http://")
	parts := strings.SplitN(host, ":", 2)
	t.Setenv("crawlHost", parts[0])
	t.Setenv("crawlPort", parts[1])
	return fc
}

func ok4ai(u string, hrefs ...string) Crawl4AIResult {
	links := make([]map[string]interface{}, 0, len(hrefs))
	for _, h := range hrefs {
		links = append(links, map[string]interface{}{"href": h})
	}
	return Crawl4AIResult{
		URL: u, Success: true, Markdown: "# T\n\nbody",
		Links: map[string][]map[string]interface{}{"internal": links},
	}
}

// A URL the service silently omits: no result, no Success:false, no error. It
// was submitted, so it is discovered, so it is unread, so the crawl is partial.
func TestBrowserShortBatchIsNotComplete(t *testing.T) {
	startFakeCrawl(t, func(urls []string) []Crawl4AIResult {
		var out []Crawl4AIResult
		for _, u := range urls {
			if strings.HasSuffix(u, "/b") {
				continue // swallowed
			}
			if strings.HasSuffix(u, "/docs") {
				out = append(out, ok4ai(u, "https://h.ai/docs/a", "https://h.ai/docs/b"))
				continue
			}
			out = append(out, ok4ai(u))
		}
		return out
	})
	_, _, cut := crawlWithBrowserEngine(&ScrapeRequest{URL: "https://h.ai/docs"})
	if cut == "" {
		t.Error("/docs/b was submitted and never returned, yet the crawl reported completeness")
	}
}

// Root-relative, path-relative and protocol-relative hrefs are what a docs site
// emits. Unresolved they carry no hostname, fail the same-domain test and vanish.
func TestBrowserRelativeHrefForms(t *testing.T) {
	startFakeCrawl(t, func(urls []string) []Crawl4AIResult {
		var out []Crawl4AIResult
		for _, u := range urls {
			if strings.HasSuffix(u, "/docs") {
				out = append(out, ok4ai(u, "/docs/root", "path", "//h.ai/docs/proto"))
				continue
			}
			out = append(out, ok4ai(u))
		}
		return out
	})
	results, _, cut := crawlWithBrowserEngine(&ScrapeRequest{URL: "https://h.ai/docs"})
	seen := map[string]bool{}
	for _, r := range results {
		seen[r.URL] = true
	}
	for _, want := range []string{"https://h.ai/docs/root", "https://h.ai/path", "https://h.ai/docs/proto"} {
		if !seen[want] {
			t.Errorf("relative form never followed: %s (cut=%q)", want, cut)
		}
	}
}

// A redirect reported under the FINAL url leaves the submitted url with no entry
// of its own. It must count as unread.
func TestBrowserRedirectCollapseIsNotComplete(t *testing.T) {
	startFakeCrawl(t, func(urls []string) []Crawl4AIResult {
		var out []Crawl4AIResult
		for _, u := range urls {
			if strings.HasSuffix(u, "/docs") {
				out = append(out, ok4ai(u, "https://h.ai/docs/old"))
				continue
			}
			out = append(out, ok4ai("https://h.ai/docs/new"))
		}
		return out
	})
	if _, _, cut := crawlWithBrowserEngine(&ScrapeRequest{URL: "https://h.ai/docs"}); cut == "" {
		t.Error("/docs/old never came back under its own key, yet the crawl reported completeness")
	}
}

// Both engines obey the same robots rules, so which engine ran cannot decide
// which pages a site keeps.
func TestBrowserRobotsWithholdsMirror(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			fmt.Fprint(w, "User-agent: *\nDisallow: /docs/secret\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer site.Close()
	startFakeCrawl(t, func(urls []string) []Crawl4AIResult {
		var out []Crawl4AIResult
		for _, u := range urls {
			if strings.HasSuffix(u, "/docs") {
				out = append(out, ok4ai(u, site.URL+"/docs/secret"))
				continue
			}
			out = append(out, ok4ai(u))
		}
		return out
	})
	if _, _, cut := crawlWithBrowserEngine(&ScrapeRequest{URL: site.URL + "/docs"}); cut == "" {
		t.Error("a disallowed page was discovered and not read, yet the crawl reported completeness")
	}
}

// The response shape has already drifted twice. A version that renames or omits
// the links group turns every crawl on the production auto path into a complete
// one-page view of the site. The corpus must survive that too.
func TestBrowserMissingLinksFieldNeverPrunes(t *testing.T) {
	f := fakeStores(t)
	m := &Mirror{Tag: "docs", Root: "https://h.ai/docs"}
	for _, u := range []string{"/docs", "/docs/a", "/docs/b"} {
		id := hashID("https://h.ai" + u)
		f.keyword[id] = DocIndex{ID: id, PageID: id, Tag: "docs", URL: "https://h.ai" + u}
	}
	startFakeCrawl(t, func(urls []string) []Crawl4AIResult {
		var out []Crawl4AIResult
		for _, u := range urls {
			out = append(out, Crawl4AIResult{URL: u, Success: true, Markdown: "# T\n\nbody"})
		}
		return out
	})
	results, errs, cut := crawlWithBrowserEngine(&ScrapeRequest{URL: "https://h.ai/docs"})
	t.Logf("links field absent: pages=%d errors=%v cut=%q", len(results), errs, cut)

	// Nothing upstream objects — that is the point. The refusal is downstream.
	docs := scrapeResultsToDocIndex(results, "docs")
	if _, err := mirrorStale("idx", m, docs); err == nil {
		t.Fatal("a one-page view of a three-page corpus was allowed to delete the other two")
	}
}

// <base href> retargets every relative href in a browser. Resolved against the
// page URL instead, the crawl reads a sibling that happens to exist and never
// discovers the real target — while every signal reads complete.
func TestBaseHrefMisresolutionNeverPrunes(t *testing.T) {
	f := fakeStores(t)
	page := func(w http.ResponseWriter, title string) {
		fmt.Fprintf(w, `<html><head><title>%s</title></head><body><h2>S%s</h2><p>b</p></body></html>`, title, title)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/docs":
			fmt.Fprint(w, `<html><head><title>I</title><base href="/docs/"></head><body>
				<h2>Home</h2><a href="a">a</a></body></html>`)
		case "/docs/a":
			page(w, "DocsA") // the REAL target
		case "/a":
			page(w, "SiblingA") // the misresolution, and it exists
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	real := DocIndex{ID: hashID(srv.URL + "/docs/a"), PageID: hashID(srv.URL + "/docs/a"),
		Title: "DocsA", URL: srv.URL + "/docs/a", Content: "b", Tag: "docs"}
	f.keyword[real.ID], f.vector[real.ID] = real, real

	stats, err := ScrapeAndIndex("acme", &ScrapeRequest{URL: srv.URL + "/docs", Engine: "fast", Tag: "docs"}, "en")
	if err != nil {
		t.Fatalf("ScrapeAndIndex: %v", err)
	}
	read := map[string]bool{}
	for id := range f.keyword {
		read[id] = true
	}
	if !read[hashID(srv.URL+"/docs/a")] {
		t.Errorf("%s/docs/a is live and was deleted; the crawl followed <base href> to the wrong page (pages=%d, deleted=%v)",
			srv.URL, stats.PagesScraped, f.cut)
	}
}

// ---------------------------------------------------------------------------
// the sequence — no chain of individually-legal passes may drain a corpus
// ---------------------------------------------------------------------------

// drainSite serves every page it ever had, always 200. Only the index's LINK
// LIST changes — the shape a site takes while a nav migrates to JavaScript
// section by section, or an IA reorg lands over successive deploys.
type drainSite struct {
	srv     *httptest.Server
	mu      sync.Mutex
	visible map[string]bool
	all     []string
}

func newDrainSite(t *testing.T, n int) *drainSite {
	t.Helper()
	d := &drainSite{visible: map[string]bool{}}
	for i := 0; i < n; i++ {
		p := fmt.Sprintf("/docs/p%02d", i)
		d.all = append(d.all, p)
		d.visible[p] = true
	}
	d.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/docs" {
			d.mu.Lock()
			var vis []string
			for p := range d.visible {
				vis = append(vis, p)
			}
			d.mu.Unlock()
			sort.Strings(vis)
			var b strings.Builder
			for _, p := range vis {
				fmt.Fprintf(&b, `<li><a href="%s">%s</a></li>`, p, p)
			}
			fmt.Fprintf(w, `<html><head><title>Docs</title></head><body><h2>Home</h2>
				<p>welcome</p><nav><ul>%s</ul></nav></body></html>`, b.String())
			return
		}
		for _, p := range d.all {
			if r.URL.Path == p {
				fmt.Fprintf(w, `<html><head><title>%s</title></head><body>
					<h2>Section %s</h2><p>body</p></body></html>`, p, p)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(d.srv.Close)
	return d
}

func (d *drainSite) show(paths []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.visible = map[string]bool{}
	for _, p := range paths {
		d.visible[p] = true
	}
}

// corpusPages counts distinct in-bounds pages currently stored.
func corpusPages(f *stores, root string) int {
	seen := map[string]bool{}
	for _, doc := range f.keyword {
		if doc.Tag != "docs" || !under(root, doc.URL) {
			continue
		}
		key := doc.PageID
		if key == "" {
			key = doc.ID
		}
		seen[key] = true
	}
	return len(seen)
}

func drainPass(t *testing.T, d *drainSite, f *stores, label string) int {
	t.Helper()
	stats, err := ScrapeAndIndex("acme", &ScrapeRequest{
		URL: d.srv.URL + "/docs", Engine: "fast", Tag: "docs", MaxPages: 500,
	}, "en")
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	got := corpusPages(f, d.srv.URL+"/docs")
	t.Logf("%-30s read=%2d pages  errors=%d  corpus=%2d pages", label, stats.PagesScraped, len(stats.Errors), got)
	return got
}

// THE ACCEPTANCE TEST. The index exposes exactly half the links it did last
// time. Every pass on its own is a site that lost half its pages — legal, no
// errors, nothing unread. Judged only against the store's current size the chain
// walks twenty pages down to one; judged against the corpus's peak it stops at
// the second step.
func TestMonotoneHalvingCannotDrainTheCorpus(t *testing.T) {
	f := fakeStores(t)
	d := newDrainSite(t, 20)

	start := drainPass(t, d, f, "pass 1: all 20 visible")
	vis := append([]string{}, d.all...)
	for i := 2; len(vis) > 0; i++ {
		vis = vis[:len(vis)/2]
		d.show(vis)
		n := drainPass(t, d, f, fmt.Sprintf("pass %d: %d visible", i, len(vis)))
		if n == 1 {
			t.Fatalf("drained: corpus went %d -> 1 page over %d passes, every pass allowed", start, i)
		}
	}
}

// A flaky renderer showing a RANDOM share each pass must not drain: the corpus
// tracks what the crawl sees and never accumulates loss.
func TestRandomFlakeDoesNotDrain(t *testing.T) {
	f := fakeStores(t)
	d := newDrainSite(t, 20)
	rng := rand.New(rand.NewSource(7))
	drainPass(t, d, f, "pass 1: all 20 visible")
	low := 99
	for i := 2; i <= 8; i++ {
		perm := rng.Perm(len(d.all))
		var vis []string
		for _, k := range perm[:12] {
			vis = append(vis, d.all[k])
		}
		d.show(vis)
		if n := drainPass(t, d, f, fmt.Sprintf("pass %d: random 12", i)); n < low {
			low = n
		}
	}
	if low < 13 {
		t.Errorf("drained under random flake: corpus fell to %d pages, expected it to track 13", low)
	}
}

// One good crawl restores everything a degraded run removed. The bound must not
// cost self-healing.
func TestOneGoodCrawlRestores(t *testing.T) {
	f := fakeStores(t)
	d := newDrainSite(t, 20)
	drainPass(t, d, f, "pass 1: all 20 visible")
	d.show(d.all[:11])
	after := drainPass(t, d, f, "pass 2: 11 visible")
	d.show(d.all)
	if restored := drainPass(t, d, f, "pass 3: all 20 again"); restored != 21 {
		t.Errorf("not self-healing: %d pages after degradation, %d after a full crawl, want 21", after, restored)
	}
}

// A degradation steeper than half in one pass is refused outright.
func TestSteepDegradationIsRefused(t *testing.T) {
	f := fakeStores(t)
	d := newDrainSite(t, 20)
	before := drainPass(t, d, f, "pass 1: all 20 visible")
	d.show(d.all[:2])
	if after := drainPass(t, d, f, "pass 2: 2 visible (steep)"); after != before {
		t.Errorf("steep degradation pruned %d -> %d pages", before, after)
	}
}

// The corpus never falls below what a crawl actually read: a refusal may leave
// it larger, nothing may leave it smaller.
func TestCorpusNeverFallsBelowWhatWasRead(t *testing.T) {
	f := fakeStores(t)
	d := newDrainSite(t, 20)
	for i, vis := range [][]string{d.all, d.all[:11], d.all[:6], d.all[:20]} {
		d.show(vis)
		stats, err := ScrapeAndIndex("acme", &ScrapeRequest{
			URL: d.srv.URL + "/docs", Engine: "fast", Tag: "docs", MaxPages: 500,
		}, "en")
		if err != nil {
			t.Fatalf("pass %d: %v", i+1, err)
		}
		got := corpusPages(f, d.srv.URL+"/docs")
		t.Logf("pass %d: read=%d corpus=%d", i+1, stats.PagesScraped, got)
		if got < stats.PagesScraped {
			t.Errorf("pass %d: corpus %d fell below what the crawl read (%d)", i+1, got, stats.PagesScraped)
		}
	}
}

// The peak rises with real growth, so a corpus that grows and then edits is not
// held to a stale ceiling; and a Replace clears it, which is how a genuinely
// shrunken corpus starts converging again.
func TestPeakRisesWithGrowthAndClearsOnReplace(t *testing.T) {
	f := fakeStores(t)
	m := &Mirror{Tag: "docs", Root: "https://h/docs"}
	seed := func(n int) []DocIndex {
		var out []DocIndex
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("p%d", i)
			out = append(out, DocIndex{ID: id, PageID: id, Tag: "docs", URL: "https://h/docs/" + id})
		}
		return out
	}
	// Grow to 20 pages, then edit down to 12: allowed, and the peak is now 20.
	for _, d := range seed(20) {
		f.keyword[d.ID] = d
	}
	if _, err := mirrorStale("idx", m, seed(12)); err != nil {
		t.Fatalf("editing 20 pages down to 12 was refused: %v", err)
	}
	// A later pass claiming 9 of that same corpus is below half the peak.
	if _, err := mirrorStale("idx", m, seed(9)); err == nil {
		t.Fatal("a pass covering 9 pages of a corpus that peaked at 20 was allowed to prune the rest")
	}
	// Replace states the corpus outright, and the ceiling goes with it.
	if _, err := IndexDocuments("acme", "docs_store", &DocIndexRequest{Documents: seed(9), Replace: true}, "en"); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if _, err := mirrorStale("idx", m, seed(9)); err != nil {
		t.Fatalf("after a Replace the corpus should converge again, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// the root cannot be changed to escape the ceiling
// ---------------------------------------------------------------------------

// corpusUnder counts distinct in-bounds pages currently stored under root.
func corpusUnder(f *stores, tag, root string) int {
	seen := map[string]bool{}
	for id, d := range f.keyword {
		if d.Tag != tag || !under(root, d.URL) {
			continue
		}
		k := d.PageID
		if k == "" {
			k = id
		}
		seen[k] = true
	}
	return len(seen)
}

// pagesAt builds one document per page for the first n pages under a prefix.
func pagesAt(prefix string, n int) []DocIndex {
	var out []DocIndex
	for i := 0; i < n; i++ {
		u := fmt.Sprintf("%s/p%02d", prefix, i)
		out = append(out, DocIndex{ID: hashID(u), PageID: hashID(u), Tag: "docs", URL: u, Content: "b"})
	}
	return out
}

func mirrorPass(t *testing.T, f *stores, root string, docs []DocIndex) (int, bool) {
	t.Helper()
	before := len(f.cut)
	if _, err := IndexDocuments("acme", "docs_store",
		&DocIndexRequest{Documents: docs, Mirror: &Mirror{Tag: "docs", Root: root}}, "en"); err != nil {
		t.Fatalf("IndexDocuments: %v", err)
	}
	return corpusUnder(f, "docs", root), len(f.cut) > before
}

// THE ACCEPTANCE TEST. Every prefix of a deep docs path is a valid root holding
// the SAME pages, and each would get its own ceiling. A ceiling the caller steps
// around by adding a path segment is not a ceiling.
func TestNestedRootsCannotWalkAroundThePeak(t *testing.T) {
	f := fakeStores(t)
	const deep = "https://h/docs/en/v2/guide/api"
	roots := []string{
		"https://h",
		"https://h/docs",
		"https://h/docs/en",
		"https://h/docs/en/v2",
		"https://h/docs/en/v2/guide",
		"https://h/docs/en/v2/guide/api",
	}
	all := pagesAt(deep, 32)
	n, _ := mirrorPass(t, f, roots[0], all)
	t.Logf("seed at %-38s corpus=%2d pages", roots[0], n)

	live := 32
	for _, root := range roots[1:] {
		live /= 2
		got, pruned := mirrorPass(t, f, root, all[:live])
		t.Logf("pass at %-38s live=%2d -> corpus=%2d pruned=%v", root, live, got, pruned)
	}
	if final := corpusUnder(f, "docs", roots[0]); final <= 2 {
		t.Errorf("corpus went 32 -> %d pages using one fresh root per step", final)
	}
}

// Just TWO nested roots, both ordinary crawl configs for one site: the host and
// its /docs subtree. Nobody is attacking anything and the allowance doubles.
func TestTwoInnocentRootsDoNotDoubleTheAllowance(t *testing.T) {
	f := fakeStores(t)
	all := pagesAt("https://h/docs", 40)
	mirrorPass(t, f, "https://h", all)
	mirrorPass(t, f, "https://h", all[:20])
	mirrorPass(t, f, "https://h/docs", all[:10])
	if got := corpusUnder(f, "docs", "https://h"); got <= 10 {
		t.Errorf("two nested roots took the corpus to %d of 40; one root caps at 20", got)
	}
}

// The control: the identical schedule at ONE root stops after the first step.
func TestSameRootIsRefusedAfterOneStep(t *testing.T) {
	f := fakeStores(t)
	root := "https://h"
	all := pagesAt("https://h/docs/en/v2/guide/api", 32)
	mirrorPass(t, f, root, all)
	live := 32
	for i := 0; i < 5; i++ {
		live /= 2
		mirrorPass(t, f, root, all[:live])
	}
	if got := corpusUnder(f, "docs", root); got < 16 {
		t.Errorf("same-root schedule drained to %d; the peak should have held at 16", got)
	}
}

// A deeper root must never delete rows outside its own subtree.
func TestDeeperRootCannotReachSiblings(t *testing.T) {
	f := fakeStores(t)
	guide := pagesAt("https://h/docs/guide", 10)
	blog := pagesAt("https://h/docs/blog", 10)
	mirrorPass(t, f, "https://h/docs", append(append([]DocIndex{}, guide...), blog...))
	mirrorPass(t, f, "https://h/docs/guide", guide[:5])
	if got := corpusUnder(f, "docs", "https://h/docs/blog"); got != 10 {
		t.Errorf("a /docs/guide mirror deleted %d of 10 sibling /docs/blog pages", 10-got)
	}
}

// THE OTHER HALF OF THE BARGAIN. One tag can hold genuinely separate roots — a
// docs tree and a blog under one hostname tag. A real cleanup under one of them
// must NOT be frozen by the tag-wide ceiling, and must keep working pass after
// pass. Pages under the untouched root count toward the tag-wide total, which is
// exactly what a nested walk cannot claim.
func TestDisjointRootCleanupIsNotFrozen(t *testing.T) {
	f := fakeStores(t)
	docs := pagesAt("https://h/docs", 60)
	blog := pagesAt("https://h/blog", 40)
	mirrorPass(t, f, "https://h", append(append([]DocIndex{}, docs...), blog...))

	// A real deletion under /blog: 40 pages down to 21, within its own ceiling.
	if _, pruned := mirrorPass(t, f, "https://h/blog", blog[:21]); !pruned {
		t.Fatal("a legitimate cleanup under /blog was refused; the tag ceiling froze a disjoint root")
	}
	if got := corpusUnder(f, "docs", "https://h/blog"); got != 21 {
		t.Errorf("/blog should hold 21 pages after the cleanup, holds %d", got)
	}
	if got := corpusUnder(f, "docs", "https://h/docs"); got != 60 {
		t.Errorf("the untouched /docs root lost %d pages", 60-got)
	}
	// And again, right at /blog's own ceiling. The tag-wide bound must add no
	// constraint of its own here: with the untouched root counted in, a pass that
	// clears the root ceiling always clears the tag ceiling too, because
	// (outside + live)*2 >= outside + rootPeak whenever live >= rootPeak/2. The
	// tag bound can only bite when a root's peak is YOUNGER than the tag's — a
	// root seen for the first time, seeded from an already-reduced count. That is
	// the nested walk, and nothing else.
	if _, pruned := mirrorPass(t, f, "https://h/blog", blog[:20]); !pruned {
		t.Error("a second legitimate cleanup under /blog was refused by the tag-wide ceiling")
	}
	if got := corpusUnder(f, "docs", "https://h/docs"); got != 60 {
		t.Errorf("the untouched /docs root lost %d pages to a /blog cleanup", 60-got)
	}
}

// Inflating the peak with pages that are not really there must not convert into
// a larger real deletion later.
func TestPhantomInflationCannotNetAPrune(t *testing.T) {
	f := fakeStores(t)
	root := "https://h/docs"
	real := pagesAt(root, 10)
	mirrorPass(t, f, root, real)
	phantom := pagesAt(root+"/phantom", 90)
	mirrorPass(t, f, root, append(append([]DocIndex{}, real...), phantom...))
	mirrorPass(t, f, root, real[:5])
	for _, d := range real {
		if _, ok := f.keyword[d.ID]; !ok {
			t.Errorf("phantom inflation netted a prune: real page %s deleted", d.URL)
			break
		}
	}
}

// Replace clears every peak, which is safe only because Replace's own blast
// radius is wider: it empties the whole index, so nothing survives for a lowered
// ceiling to threaten.
func TestReplaceIsWiderThanAnyMirror(t *testing.T) {
	f := fakeStores(t)
	f.keyword["other"] = DocIndex{ID: "other", Tag: "handbook", URL: "https://elsewhere/1"}
	mirrorPass(t, f, "https://h/docs", pagesAt("https://h/docs", 20))
	if _, ok := f.keyword["other"]; !ok {
		t.Fatal("setup: a mirror deleted another tag's row")
	}
	if _, err := IndexDocuments("acme", "docs_store",
		&DocIndexRequest{Documents: pagesAt("https://h/docs", 1), Replace: true}, "en"); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if _, ok := f.keyword["other"]; ok {
		t.Error("Replace left another tag's row: it is narrower than a mirror, so clearing every peak overreaches")
	}
	if len(f.peaks) != 0 {
		t.Errorf("Replace did not clear the peaks: %v", f.peaks)
	}
}

// ---------------------------------------------------------------------------
// ancestor scoping — a sibling corpus credits nothing to the subtree being drained
// ---------------------------------------------------------------------------

// The nested walk again, under a tag that also holds a sibling corpus the walk
// never touches. The default tag is the bare hostname, so /docs and /blog on one
// host share a tag — the very case Mirror.Root exists for. A ceiling measured
// tag-wide credits the pass with the blog's mass, and the pages the walk consumes
// are exactly the ones NOT in that credit, so the blog hands the walk all the
// headroom it wants. Measured per ancestor, /docs is judged on /docs.
func TestSiblingCorpusCannotRestoreTheNestedWalk(t *testing.T) {
	f := fakeStores(t)
	const deep = "https://h/docs/en/v2/guide/api"
	chain := pagesAt(deep, 32)
	blog := pagesAt("https://h/blog", 100)
	mirrorPass(t, f, "https://h", append(append([]DocIndex{}, chain...), blog...))

	roots := []string{"https://h/docs", "https://h/docs/en", "https://h/docs/en/v2",
		"https://h/docs/en/v2/guide", "https://h/docs/en/v2/guide/api"}
	live := 32
	for _, root := range roots {
		live /= 2
		// The pass offers the shrinking chain PLUS the untouched blog, exactly as
		// a crawl of that root would: the blog is out of bounds either way.
		mirrorPass(t, f, root, append(append([]DocIndex{}, chain[:live]...), blog...))
		t.Logf("pass at %-34s live=%2d -> chain=%2d", root, live, corpusUnder(f, "docs", deep))
	}
	if got := corpusUnder(f, "docs", deep); got <= 2 {
		t.Errorf("chain went 32 -> %d pages; the untouched blog (%d pages) supplied the headroom",
			got, corpusUnder(f, "docs", "https://h/blog"))
	}
}

// The mechanism in isolation: the same schedule must be refused whether or not a
// sibling corpus exists. Out-of-root mass must never be a key.
func TestOutsideMassIsNotAKey(t *testing.T) {
	deep := "https://h/docs/en"
	run := func(outside int) bool {
		f := fakeStores(t)
		chain := pagesAt(deep, 32)
		other := pagesAt("https://h/blog", outside)
		mirrorPass(t, f, "https://h", append(append([]DocIndex{}, chain...), other...))
		mirrorPass(t, f, "https://h/docs", append(append([]DocIndex{}, chain[:16]...), other...))
		_, pruned := mirrorPass(t, f, deep, append(append([]DocIndex{}, chain[:8]...), other...))
		return pruned
	}
	if none, some := run(0), run(100); none != some {
		t.Errorf("identical schedule behaved differently with a sibling corpus: outside=0 pruned=%v, outside=100 pruned=%v",
			none, some)
	} else if some {
		t.Error("the second halving was allowed; the ancestor ceiling did not bind")
	}
}

// A genuine purge of one root, under a tag holding others, at every size of
// sibling corpus. The ancestor ceiling must not turn a real cleanup into a
// refusal.
func TestDisjointCleanupIsNotFrozenAtAnyOutsideSize(t *testing.T) {
	for _, outside := range []int{0, 1, 2, 10, 100} {
		f := fakeStores(t)
		docs := pagesAt("https://h/docs", outside)
		blog := pagesAt("https://h/blog", 100)
		mirrorPass(t, f, "https://h", append(append([]DocIndex{}, docs...), blog...))
		if _, pruned := mirrorPass(t, f, "https://h/blog", blog[:50]); !pruned {
			t.Errorf("a genuine 100->50 purge of /blog was frozen with outside=%d", outside)
		}
	}
}

// A mirror declaring one tag must never delete rows carrying another, whatever
// URLs they share.
func TestTagScopingHoldsAcrossIdenticalURLs(t *testing.T) {
	f := fakeStores(t)
	for _, d := range pagesAt("https://h/docs", 20) {
		f.keyword[d.ID] = d
	}
	other := []DocIndex{{ID: "b1", PageID: "b1", Tag: "other", URL: "https://h/docs/p00", Content: "x"}}
	if _, err := IndexDocuments("acme", "docs_store",
		&DocIndexRequest{Documents: other, Mirror: &Mirror{Tag: "other", Root: "https://h/docs"}}, "en"); err != nil {
		t.Fatalf("IndexDocuments: %v", err)
	}
	if got := corpusUnder(f, "docs", "https://h/docs"); got != 20 {
		t.Errorf("a mirror declaring tag \"other\" deleted %d of 20 rows carrying tag \"docs\"", 20-got)
	}
}

// An uploaded file's chunks carry a tag and NO url. No crawl reaches them and no
// mirror can delete them, so they must not count as mass in any ceiling — count
// them and deleting them by the one route that can (a file delete) drops a credit
// the mirror never earned, freezing a crawl that did nothing wrong.
func TestUploadsNeverEnterACeiling(t *testing.T) {
	f := fakeStores(t)
	docs := pagesAt("https://h/docs", 20)
	uploads := make([]DocIndex, 0, 100)
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("u%03d", i)
		uploads = append(uploads, DocIndex{ID: id, PageID: id, Tag: "docs", FileID: "f1", Content: "x"})
	}
	for _, u := range uploads {
		f.keyword[u.ID] = u
	}
	mirrorPass(t, f, "https://h", docs)
	if got := f.peaks["docs\nhttps://h"]; got != 20 {
		t.Errorf("peak for https://h is %d; uploads with no url inflated it beyond the 20 crawled pages", got)
	}

	for _, u := range uploads {
		delete(f.keyword, u.ID) // the file is deleted by the one path that can
	}
	if _, pruned := mirrorPass(t, f, "https://h/docs", docs[:10]); !pruned {
		t.Error("a legitimate /docs mirror was frozen after uploaded chunks were deleted elsewhere")
	}
}

// Both fields of a peak key are free text a caller supplies, so they are
// length-prefixed: joined by a separator either could contain, the tag record of
// tag "a\n" and the root record of (tag "a", root "\n") derive the same key.
func TestPeakKeyIsUnambiguous(t *testing.T) {
	if peakID("a\n", "") == peakID("a", "\n") {
		t.Error("peak key collides across a separator either field may contain")
	}
	if peakID("a", "b") == peakID("ab", "") {
		t.Error("peak key collides on the field boundary")
	}
}
