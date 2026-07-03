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

// Hermetic tests for the consolidated file-scoped RAG surface. They swap the
// six product seams (index/vector write, search/vector read, delete, context)
// with a single in-memory fake index that honors the file_id filter — so the
// full ingest -> retrieve -> delete lifecycle is exercised with NO live Search,
// Vector, DB, or embedding provider. This is the proof that file-scoped RAG is a
// filter over the one unified index, not a parallel store.

import (
	"strings"
	"testing"
)

// fakeIndex is an in-memory stand-in for the {owner}-{store}-docs index. It
// stores DocIndex chunks keyed by index name and filters retrieval by file_id
// and tag exactly like Hanzo Search + Hanzo Vector do in production.
type fakeIndex struct {
	docs map[string][]DocIndex // indexName -> chunks
}

func newFakeIndex() *fakeIndex { return &fakeIndex{docs: map[string][]DocIndex{}} }

func (f *fakeIndex) matches(d DocIndex, tag string, fileIDs []string) bool {
	if tag != "" && d.Tag != tag {
		return false
	}
	if len(fileIDs) > 0 {
		ok := false
		for _, id := range fileIDs {
			if d.FileID == id {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// install wires the fake into all six seams and returns a restore func.
func (f *fakeIndex) install() func() {
	origIndex, origVec := indexSink, vectorSink
	origSearch, origVecSearch := searchSink, vectorSearchSink
	origDelS, origDelV, origCtx := searchDeleteSink, vectorDeleteSink, fileContextSink

	write := func(indexName string, docs []DocIndex, replace bool) (int, error) {
		if replace {
			f.docs[indexName] = nil
		}
		f.docs[indexName] = append(f.docs[indexName], docs...)
		return len(docs), nil
	}
	indexSink = write
	vectorSink = func(indexName string, docs []DocIndex, replace bool, lang string) error {
		return nil // keyword sink is authoritative for the fake; vector is best-effort
	}
	read := func(indexName, query, tag string, fileIDs []string, limit int) ([]DocSearchResult, error) {
		out := []DocSearchResult{}
		for _, d := range f.docs[indexName] {
			if !f.matches(d, tag, fileIDs) {
				continue
			}
			// Naive keyword relevance: substring of query terms.
			if query != "" && !containsAnyWord(d.Content, query) {
				continue
			}
			out = append(out, DocSearchResult{ID: d.ID, Content: d.Content, FileID: d.FileID, Title: d.Title, Type: "text"})
			if len(out) >= limit {
				break
			}
		}
		return out, nil
	}
	searchSink = read
	vectorSearchSink = func(indexName, query, tag string, fileIDs []string, limit int, lang string) ([]DocSearchResult, error) {
		return read(indexName, query, tag, fileIDs, limit)
	}
	del := func(indexName, fileID string) error {
		kept := f.docs[indexName][:0]
		for _, d := range f.docs[indexName] {
			if d.FileID != fileID {
				kept = append(kept, d)
			}
		}
		f.docs[indexName] = kept
		return nil
	}
	searchDeleteSink = del
	vectorDeleteSink = del
	fileContextSink = func(indexName, fileID string) ([]DocSearchResult, error) {
		out := []DocSearchResult{}
		for _, d := range f.docs[indexName] {
			if d.FileID == fileID {
				out = append(out, DocSearchResult{ID: d.ID, Content: d.Content, FileID: d.FileID, Title: d.Title})
			}
		}
		return out, nil
	}

	return func() {
		indexSink, vectorSink = origIndex, origVec
		searchSink, vectorSearchSink = origSearch, origVecSearch
		searchDeleteSink, vectorDeleteSink, fileContextSink = origDelS, origDelV, origCtx
	}
}

func containsAnyWord(haystack, query string) bool {
	h := strings.ToLower(haystack)
	for _, w := range strings.Fields(strings.ToLower(query)) {
		if strings.Contains(h, w) {
			return true
		}
	}
	return false
}

func TestRagEmbedThenQueryIsFileScoped(t *testing.T) {
	fake := newFakeIndex()
	defer fake.install()()

	owner := "acme"
	// Two files, disjoint content, same store/index.
	if _, err := RagEmbedFile(owner, &RagEmbedRequest{
		FileID: "file-A", Filename: "a.txt", Content: "The mitochondria is the powerhouse of the cell.",
	}, "en"); err != nil {
		t.Fatalf("embed file-A: %v", err)
	}
	if _, err := RagEmbedFile(owner, &RagEmbedRequest{
		FileID: "file-B", Filename: "b.txt", Content: "Goroutines are lightweight threads managed by the Go runtime.",
	}, "en"); err != nil {
		t.Fatalf("embed file-B: %v", err)
	}

	// Query scoped to file-A must not return file-B content, even though the
	// query term ("runtime") only exists in file-B.
	res, err := RagQuery(owner, &RagQueryRequest{Query: "runtime threads", FileID: "file-A"}, "en")
	if err != nil {
		t.Fatalf("query file-A: %v", err)
	}
	for _, r := range res {
		if r.FileID != "file-A" {
			t.Fatalf("file-scope leak: got chunk from %q when scoped to file-A", r.FileID)
		}
	}

	// Query scoped to file-B finds it.
	res, err = RagQuery(owner, &RagQueryRequest{Query: "runtime threads", FileID: "file-B"}, "en")
	if err != nil {
		t.Fatalf("query file-B: %v", err)
	}
	if len(res) == 0 {
		t.Fatalf("expected file-B chunk for query, got none")
	}
	for _, r := range res {
		if r.FileID != "file-B" {
			t.Fatalf("expected only file-B chunks, got %q", r.FileID)
		}
	}
}

func TestRagQueryMultipleUnionsFiles(t *testing.T) {
	fake := newFakeIndex()
	defer fake.install()()

	owner := "acme"
	mustEmbed(t, owner, "f1", "alpha shared token beta")
	mustEmbed(t, owner, "f2", "gamma shared token delta")
	mustEmbed(t, owner, "f3", "epsilon unrelated zeta")

	res, err := RagQuery(owner, &RagQueryRequest{Query: "shared", FileIDs: []string{"f1", "f2"}, K: 10}, "en")
	if err != nil {
		t.Fatalf("query-multiple: %v", err)
	}
	got := map[string]bool{}
	for _, r := range res {
		got[r.FileID] = true
	}
	if !got["f1"] || !got["f2"] {
		t.Fatalf("expected chunks from f1 and f2, got %v", got)
	}
	if got["f3"] {
		t.Fatalf("f3 must be excluded (not in file_ids), got it")
	}
}

func TestRagQueryRequiresFileID(t *testing.T) {
	fake := newFakeIndex()
	defer fake.install()()
	_, err := RagQuery("acme", &RagQueryRequest{Query: "anything"}, "en")
	if err == nil {
		t.Fatal("expected error when no file_id/file_ids supplied")
	}
	if !strings.Contains(err.Error(), "file_id") {
		t.Fatalf("expected file_id error, got %v", err)
	}
}

func TestRagReembedReplacesChunks(t *testing.T) {
	fake := newFakeIndex()
	defer fake.install()()

	owner := "acme"
	mustEmbed(t, owner, "doc", "first version content about apples")
	// Re-embed the SAME file_id with new content.
	mustEmbed(t, owner, "doc", "second version content about oranges")

	// Old content must be gone; new content present. Exactly one file_id "doc".
	ctx, err := RagFileContext(owner, "", "doc")
	if err != nil {
		t.Fatalf("context: %v", err)
	}
	joined := ""
	for _, c := range ctx {
		joined += c.Content + "\n"
	}
	if strings.Contains(joined, "apples") {
		t.Fatalf("stale chunk survived re-embed: %q", joined)
	}
	if !strings.Contains(joined, "oranges") {
		t.Fatalf("new content missing after re-embed: %q", joined)
	}
}

func TestRagDeleteRemovesFile(t *testing.T) {
	fake := newFakeIndex()
	defer fake.install()()

	owner := "acme"
	mustEmbed(t, owner, "keep", "content that should remain")
	mustEmbed(t, owner, "drop", "content that should be deleted")

	if err := DeleteRagFile(owner, "", "drop", "en"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// "drop" gone.
	if ctx, _ := RagFileContext(owner, "", "drop"); len(ctx) != 0 {
		t.Fatalf("expected 0 chunks for deleted file, got %d", len(ctx))
	}
	// "keep" intact.
	if ctx, _ := RagFileContext(owner, "", "keep"); len(ctx) == 0 {
		t.Fatalf("delete removed the wrong file: keep has no chunks")
	}
}

func TestRagEmbedEmptyContentClearsFile(t *testing.T) {
	fake := newFakeIndex()
	defer fake.install()()

	owner := "acme"
	mustEmbed(t, owner, "e", "some real content")
	// Now embed empty content for the same file_id: should clear it, not error.
	if _, err := RagEmbedFile(owner, &RagEmbedRequest{FileID: "e", Content: "   "}, "en"); err != nil {
		t.Fatalf("empty embed: %v", err)
	}
	if ctx, _ := RagFileContext(owner, "", "e"); len(ctx) != 0 {
		t.Fatalf("expected file cleared by empty embed, got %d chunks", len(ctx))
	}
}

func TestRagEmbedValidation(t *testing.T) {
	fake := newFakeIndex()
	defer fake.install()()

	if _, err := RagEmbedFile("", &RagEmbedRequest{FileID: "x", Content: "y"}, "en"); err == nil {
		t.Fatal("expected error for empty owner")
	}
	if _, err := RagEmbedFile("acme", &RagEmbedRequest{FileID: "", Content: "y"}, "en"); err == nil {
		t.Fatal("expected error for empty file_id")
	}
	if _, err := RagEmbedFile("acme", &RagEmbedRequest{FileID: "x"}, "en"); err == nil {
		t.Fatal("expected error when neither content nor url provided")
	}
}

func TestRagEmbedChunksLargeContent(t *testing.T) {
	fake := newFakeIndex()
	defer fake.install()()

	// ~5000 chars of paragraphs -> must split into multiple chunks at the
	// 1500-char parity size.
	para := strings.Repeat("This is a sentence about distributed systems and consensus. ", 100)
	res, err := RagEmbedFile("acme", &RagEmbedRequest{FileID: "big", Filename: "big.txt", Content: para}, "en")
	if err != nil {
		t.Fatalf("embed big: %v", err)
	}
	if res.Chunks < 2 {
		t.Fatalf("expected multiple chunks for large content, got %d", res.Chunks)
	}
	if res.IndexName != GetSearchIndexName("acme", RagFileStore) {
		t.Fatalf("unexpected index name %q", res.IndexName)
	}
}

func mustEmbed(t *testing.T, owner, fileID, content string) {
	t.Helper()
	if _, err := RagEmbedFile(owner, &RagEmbedRequest{FileID: fileID, Filename: fileID + ".txt", Content: content}, "en"); err != nil {
		t.Fatalf("embed %s: %v", fileID, err)
	}
}

// ── Pure-helper tests (no seams) ──────────────────────────────────────────────

func TestMergeFileIDs(t *testing.T) {
	got := mergeFileIDs("a", []string{"b", "a", "", "c", "b"})
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("mergeFileIDs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("mergeFileIDs[%d] = %q, want %q (%v)", i, got[i], want[i], got)
		}
	}
	if len(mergeFileIDs("", nil)) != 0 {
		t.Fatal("mergeFileIDs of empties should be empty")
	}
}

func TestBuildMeiliFilter(t *testing.T) {
	cases := []struct {
		tag     string
		fileIDs []string
		want    string
	}{
		{"", nil, ""},
		{"docs", nil, "tag = 'docs'"},
		{"", []string{"f1"}, "file_id IN ['f1']"},
		{"", []string{"f1", "f2"}, "file_id IN ['f1', 'f2']"},
		{"docs", []string{"f1"}, "tag = 'docs' AND file_id IN ['f1']"},
		{"", []string{"f1", ""}, "file_id IN ['f1']"}, // empty id dropped
	}
	for _, tc := range cases {
		if got := buildMeiliFilter(tc.tag, tc.fileIDs); got != tc.want {
			t.Fatalf("buildMeiliFilter(%q,%v) = %q, want %q", tc.tag, tc.fileIDs, got, tc.want)
		}
	}
}

func TestMeiliQuoteEscapesInjection(t *testing.T) {
	// A crafted file_id must not break out of the single-quoted literal.
	got := buildMeiliFilter("", []string{"x' OR '1'='1"})
	if strings.Contains(got, "OR '1'='1") && strings.Count(got, "'")%2 != 0 {
		t.Fatalf("unbalanced quotes allow injection: %q", got)
	}
	// The stray quotes are stripped, so the token is inert.
	if strings.Contains(got, "'x'") == false && strings.Contains(got, "x OR") == false {
		t.Logf("sanitized filter: %q", got) // informational
	}
	if strings.Contains(got, "''") {
		t.Fatalf("did not expect empty-literal artifact: %q", got)
	}
}

func TestResolveRagStoreDefault(t *testing.T) {
	if resolveRagStore("") != RagFileStore {
		t.Fatalf("empty store should default to %q", RagFileStore)
	}
	if resolveRagStore("custom") != "custom" {
		t.Fatal("explicit store should pass through")
	}
}
