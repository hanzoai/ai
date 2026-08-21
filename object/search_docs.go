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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/model"
	meilisearch "github.com/hanzoai/search-go"
)

// DocIndex represents a single indexed documentation chunk.
type DocIndex struct {
	ID          string     `json:"id"`
	PageID      string     `json:"page_id"`
	Title       string     `json:"title"`
	URL         string     `json:"url"`
	Content     string     `json:"content"`
	Section     string     `json:"section,omitempty"`
	SectionID   string     `json:"section_id,omitempty"`
	Tag         string     `json:"tag,omitempty"`
	Breadcrumbs StringList `json:"breadcrumbs,omitempty"`

	// FileID scopes a chunk to a single uploaded file (the retired rag-api's
	// LibreChat contract). It is a filterable dimension on the SAME unified
	// index — file-scoped retrieval is a filter over {owner}-{store}-docs, not
	// a parallel store. Empty for doc/crawl/github chunks.
	FileID string `json:"file_id,omitempty"`
}

// DocSearchRequest is the request body for searching documents.
type DocSearchRequest struct {
	Query string `json:"query"`
	Tag   string `json:"tag,omitempty"`
	Limit int    `json:"limit,omitempty"`
	Mode  string `json:"mode,omitempty"` // "hybrid", "fulltext", "vector"

	// FileIDs restricts results to chunks of the given uploaded files. Empty
	// means unrestricted (doc-wide search). Powers the file-scoped RAG surface
	// (/v1/ai/rag/query, /v1/ai/rag/query-multiple) without a separate index.
	FileIDs []string `json:"file_ids,omitempty"`
}

// DocSearchResult is a single result returned by SearchDocuments.
type DocSearchResult struct {
	ID          string     `json:"id"`
	URL         string     `json:"url"`
	Type        string     `json:"type"` // "page", "heading", "text"
	Content     string     `json:"content"`
	Breadcrumbs StringList `json:"breadcrumbs,omitempty"`
	FileID      string     `json:"file_id,omitempty"`
	Title       string     `json:"title,omitempty"`
}

// DocIndexRequest is the request body for indexing documents.
type DocIndexRequest struct {
	Documents []DocIndex `json:"documents"`
	Replace   bool       `json:"replace,omitempty"`

	// Mirror declares the corpus these documents are the WHOLE of. Set, the write
	// is a mirror of it: whatever it holds that these documents omit is removed,
	// so a page taken off a site stops being retrieved and cited. Nil — the
	// default — the write only adds, which is the only honest thing a source that
	// sees part of a corpus at a time can do.
	//
	// It is bounded, so mirroring one source never reaches another sharing the
	// index. That is what separates it from Replace, which empties the index
	// whoever else is in it.
	Mirror *Mirror `json:"mirror,omitempty"`
}

// Mirror bounds what a write may remove. BOTH bounds are required, and a prune
// crosses neither.
type Mirror struct {
	// Tag is the source the documents belong to.
	Tag string `json:"tag"`

	// Root is the URL they live under, and it is not redundant with Tag. A crawl
	// tags by hostname when asked for no tag, so a docs tree and a blog on one
	// host carry the SAME tag — bounded by tag alone, each crawl would delete the
	// other's pages. A crawl is authoritative over the subtree it started from
	// and nothing else, and this is that subtree.
	Root string `json:"root"`
}

// under reports whether u lies inside the subtree at root, boundary included:
// ".../docs" holds ".../docs/a" and ".../docs#x" and does NOT hold
// ".../docsearch".
func under(root, u string) bool {
	root = strings.TrimRight(root, "/")
	if u == root {
		return true
	}
	if !strings.HasPrefix(u, root) {
		return false
	}
	switch u[len(root)] {
	case '/', '#', '?':
		return true
	}
	return false
}

// DocChatRequest is the request body for RAG chat over documentation.
type DocChatRequest struct {
	Query  string `json:"query"`
	Tag    string `json:"tag,omitempty"`
	Stream bool   `json:"stream,omitempty"`
	Prompt string `json:"prompt,omitempty"` // Custom system prompt (set by client)
}

// DocStatsResponse contains index statistics.
type DocStatsResponse struct {
	DocumentCount int              `json:"documentCount"`
	IsIndexing    bool             `json:"isIndexing"`
	Fields        map[string]int64 `json:"fields,omitempty"`
}

// qdrantSearchRequest is the JSON body for Hanzo Vector's search endpoint.
type qdrantSearchRequest struct {
	Vector []float32     `json:"vector"`
	Limit  int           `json:"limit"`
	Filter *qdrantFilter `json:"filter,omitempty"`
	With   bool          `json:"with_payload"`
}
type qdrantFilter struct {
	Must   []qdrantCondition `json:"must,omitempty"`
	Should []qdrantCondition `json:"should,omitempty"`
}
type qdrantCondition struct {
	Key   string      `json:"key"`
	Match qdrantMatch `json:"match"`
}
type qdrantMatch struct {
	Value string `json:"value"`
}

// qdrantSearchResponse is the JSON response from Hanzo Vector's search endpoint.
type qdrantSearchResponse struct {
	Result []qdrantScoredPoint `json:"result"`
}
type qdrantScoredPoint struct {
	ID      interface{}            `json:"id"`
	Score   float64                `json:"score"`
	Payload map[string]interface{} `json:"payload"`
}

// qdrantUpsertRequest is the JSON body for Hanzo Vector's upsert endpoint.
type qdrantUpsertRequest struct {
	Points []qdrantPoint `json:"points"`
}
type qdrantPoint struct {
	ID      string                 `json:"id"`
	Vector  []float32              `json:"vector"`
	Payload map[string]interface{} `json:"payload"`
}

const rrfK = 60

// GetSearchIndexName returns the Hanzo Search index name for a given owner/store.
func GetSearchIndexName(owner, store string) string {
	return fmt.Sprintf("%s-%s-docs", owner, store)
}

// storeSlug is the shape a store a CALLER chose may take: lowercase letters,
// digits and underscores, opening on a letter or digit.
//
// THE HYPHEN IS THE WHOLE REASON. An index is named "{owner}-{store}-docs", so a
// store carrying a hyphen can push that boundary and land on another tenant's
// index: org "acme" asking for "corp-docs-hanzo-ai" builds exactly the string org
// "acme-corp" builds for "docs-hanzo-ai". Barred from the store, the last hyphen
// before the suffix always delimits it and one index name has one reading —
// whatever hyphens the OWNER contains, because the owner is not the part a caller
// chooses. A slash is the same disclosure by a shorter route: the name goes into
// the search backend's URL path unescaped, so "../" leaves the collection.
var storeSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9_]*$`)

// ResolveStore is the store a request acts on: the one it asked for, or the one this
// surface supplies when it asked for none.
//
// IT IS THE ONE PLACE A STORE DEFAULT IS APPLIED, because applying the default and
// admitting the caller's choice are the same decision and splitting them is how a
// surface ends up trusting a name nobody checked. A default is OURS — code, not
// input — so it is taken as given, which is how the estate's own hyphenated names
// keep working while no caller can spell one. Asking for the default by name is
// the default, since it selects the identical index.
//
// IT TAKES THE OWNER because the thing being protected is the PAIR. An index name
// is owner and store joined by the one character that separates them, so which
// index a store reaches is never a fact about the store alone, and a rule that
// cannot see the owner can only close half of the ambiguity.
//
// The two halves it closes:
//
//	The store may not carry a hyphen. Then the last hyphen before the suffix always
//	delimits the store, so a caller cannot spell a longer owner than its own: org
//	"acme" asking for "corp-docs-hanzo-ai" is refused, and org "acme-corp" keeps
//	its index.
//
//	A HYPHENATED OWNER may not choose a store at all. That is the mirror, and the
//	default is what opens it: a default may carry hyphens, so an org can be named
//	to end where a victim's default begins — "acme-docs-hanzo" asking for "ai"
//	spells the index "acme" gets from the default "docs-hanzo-ai". The refusal is
//	exact rather than cautious: with a hyphen-free store, a collision needs the
//	ASKING owner to contain a hyphen, so refusing that case removes the last one
//	and no other request is affected. Such an org keeps its default store and can
//	choose again once the hyphenated defaults are renamed and reindexed.
func ResolveStore(owner, requested, def string) (string, error) {
	r := strings.TrimSpace(requested)
	if r == "" || r == def {
		return def, nil
	}
	if !storeSlug.MatchString(r) {
		return "", fmt.Errorf("store %q is not a valid name: lowercase letters, digits and underscores only", requested)
	}
	if strings.Contains(owner, "-") {
		return "", fmt.Errorf("organization %q may not select a store; it is served by its default store %q", owner, def)
	}
	return r, nil
}

func getSearchClient() (meilisearch.ServiceManager, error) {
	host := conf.GetConfigString("searchHost")
	if host == "" {
		host = os.Getenv("SEARCH_HOST")
	}
	if host == "" {
		host = "search.hanzo.svc.cluster.local"
	}
	port := conf.GetConfigString("searchPort")
	if port == "" {
		port = "7700"
	}
	apiKey := resolveSecretName("searchApiKey")
	if apiKey == "" {
		apiKey = os.Getenv("SEARCH_API_KEY")
	}
	url := host
	if !strings.HasPrefix(host, "http") {
		url = fmt.Sprintf("http://%s:%s", host, port)
	}
	client := meilisearch.New(url, meilisearch.WithAPIKey(apiKey))
	return client, nil
}

func getVectorEndpoint() (string, string) {
	host := conf.GetConfigString("vectorHost")
	if host == "" {
		host = "vector.hanzo.svc.cluster.local"
	}
	port := conf.GetConfigString("vectorPort")
	if port == "" {
		port = "6333"
	}
	apiKey := resolveSecretName("vectorApiKey")
	url := host
	if !strings.HasPrefix(host, "http") {
		url = fmt.Sprintf("http://%s:%s", host, port)
	}
	return url, apiKey
}

// awaitTask blocks until a Hanzo Search task settles, checking every 200ms and
// giving up after cap. WaitForTask's second parameter is the POLL INTERVAL —
// handing it a duration meant as a deadline parks the caller for that long
// before the first re-check, so every write costs the full "timeout".
func awaitTask(client meilisearch.ServiceManager, taskUID int64, cap time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), cap)
	defer cancel()
	_, err := client.WaitForTaskWithContext(ctx, taskUID, 200*time.Millisecond)
	return err
}

// ensureSearchIndex creates the Hanzo Search index if it does not exist and
// configures it. Settings are written ONCE, when the index is created — Hanzo
// Search reindexes the WHOLE corpus on any settings write (unchanged or not),
// which on a large index is 80-100s and blocked every ingest behind a settings
// task that only ever restated attributes it already had. An existing index
// already carries the right settings, so a settings write on it is pure cost.
func ensureSearchIndex(client meilisearch.ServiceManager, indexName string) error {
	if _, err := client.GetIndex(indexName); err == nil {
		return nil // exists and configured; a settings write here only triggers a full reindex
	}
	task, createErr := client.CreateIndex(&meilisearch.IndexConfig{
		Uid:        indexName,
		PrimaryKey: "id",
	})
	if createErr != nil {
		return fmt.Errorf("failed to create index %s: %w", indexName, createErr)
	}
	if err := awaitTask(client, task.TaskUID, 30*time.Second); err != nil {
		return fmt.Errorf("failed waiting for index creation: %w", err)
	}
	index := client.Index(indexName)
	task, err := index.UpdateSettings(&meilisearch.Settings{
		SearchableAttributes: []string{"title", "content", "section"},
		FilterableAttributes: []string{"tag", "page_id", "file_id"},
		SortableAttributes:   []string{},
	})
	if err != nil {
		return fmt.Errorf("failed to update index settings: %w", err)
	}
	if err = awaitTask(client, task.TaskUID, 30*time.Second); err != nil {
		return fmt.Errorf("failed waiting for settings update: %w", err)
	}
	return nil
}

// ensureVectorCollection creates the Hanzo Vector collection if it does not exist.
func ensureVectorCollection(baseURL, apiKey, collectionName string, dimension int) error {
	url := fmt.Sprintf("%s/collections/%s", baseURL, collectionName)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("failed to build Hanzo Vector request: %w", err)
	}
	if apiKey != "" {
		req.Header.Set("api-key", apiKey)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to check Hanzo Vector collection: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	createBody := map[string]interface{}{
		"vectors": map[string]interface{}{
			"size":     dimension,
			"distance": "Cosine",
		},
	}
	bodyBytes, err := json.Marshal(createBody)
	if err != nil {
		return fmt.Errorf("failed to marshal Hanzo Vector create body: %w", err)
	}
	req, err = http.NewRequest(http.MethodPut, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("failed to build Hanzo Vector create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("api-key", apiKey)
	}
	resp, err = client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to create Hanzo Vector collection: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Hanzo Vector create collection returned %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// buildMeiliFilter ANDs a tag equality with a file_id membership. Empty
// components are omitted; an all-empty filter returns "" (unrestricted).
// Values are single-quoted with embedded quotes stripped so a crafted tag or
// file_id can't break out of the filter expression (Meili has no bind params).
func buildMeiliFilter(tag string, fileIDs []string) string {
	clauses := []string{}
	if tag != "" {
		clauses = append(clauses, fmt.Sprintf("tag = '%s'", meiliQuote(tag)))
	}
	if len(fileIDs) > 0 {
		quoted := make([]string, 0, len(fileIDs))
		for _, id := range fileIDs {
			if id == "" {
				continue
			}
			quoted = append(quoted, fmt.Sprintf("'%s'", meiliQuote(id)))
		}
		if len(quoted) > 0 {
			clauses = append(clauses, fmt.Sprintf("file_id IN [%s]", strings.Join(quoted, ", ")))
		}
	}
	return strings.Join(clauses, " AND ")
}

// meiliQuote strips single quotes and backslashes so a value is safe inside a
// single-quoted Meilisearch filter literal.
func meiliQuote(s string) string {
	return strings.NewReplacer("'", "", "\\", "").Replace(s)
}

// searchFulltext searches Hanzo Search and returns ranked document IDs.
func searchFulltext(client meilisearch.ServiceManager, indexName string, query string, tag string, fileIDs []string, limit int) ([]DocSearchResult, error) {
	index := client.Index(indexName)
	searchReq := &meilisearch.SearchRequest{
		Limit: int64(limit),
	}
	if filter := buildMeiliFilter(tag, fileIDs); filter != "" {
		searchReq.Filter = filter
	}
	resp, err := index.Search(query, searchReq)
	if err != nil {
		return nil, fmt.Errorf("Hanzo Search query failed: %w", err)
	}
	results := make([]DocSearchResult, 0, len(resp.Hits))
	for _, hit := range resp.Hits {
		m := make(map[string]interface{})
		if decErr := hit.DecodeInto(&m); decErr != nil {
			continue
		}
		result := docResultFromMap(m)
		results = append(results, result)
	}
	return results, nil
}

// searchVectorRaw queries Hanzo Vector with a pre-computed vector. An optional
// tag (AND) and file_id set (OR, matching any listed file) scope the search on
// the same collection — file-scoped retrieval is a filter, not a parallel store.
func searchVectorRaw(baseURL, apiKey, collectionName string, vector []float32, tag string, fileIDs []string, limit int) ([]DocSearchResult, error) {
	url := fmt.Sprintf("%s/collections/%s/points/search", baseURL, collectionName)
	reqBody := qdrantSearchRequest{
		Vector: vector,
		Limit:  limit,
		With:   true,
	}
	var filter *qdrantFilter
	if tag != "" {
		filter = &qdrantFilter{Must: []qdrantCondition{{Key: "tag", Match: qdrantMatch{Value: tag}}}}
	}
	if len(fileIDs) > 0 {
		if filter == nil {
			filter = &qdrantFilter{}
		}
		for _, id := range fileIDs {
			if id == "" {
				continue
			}
			filter.Should = append(filter.Should, qdrantCondition{Key: "file_id", Match: qdrantMatch{Value: id}})
		}
	}
	reqBody.Filter = filter
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal Hanzo Vector search body: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to build Hanzo Vector search request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("api-key", apiKey)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Hanzo Vector search request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Hanzo Vector search returned %d: %s", resp.StatusCode, string(body))
	}
	var qdrantResp qdrantSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&qdrantResp); err != nil {
		return nil, fmt.Errorf("failed to decode Hanzo Vector response: %w", err)
	}
	results := make([]DocSearchResult, 0, len(qdrantResp.Result))
	for _, point := range qdrantResp.Result {
		result := docResultFromMap(point.Payload)
		results = append(results, result)
	}
	return results, nil
}

// mergeRRF merges results from two ranked lists using Reciprocal Rank Fusion.
func mergeRRF(fulltextResults, vectorResults []DocSearchResult, limit int) []DocSearchResult {
	scores := make(map[string]float64)
	docs := make(map[string]DocSearchResult)
	for rank, r := range fulltextResults {
		scores[r.ID] += 1.0 / float64(rrfK+rank+1)
		docs[r.ID] = r
	}
	for rank, r := range vectorResults {
		scores[r.ID] += 1.0 / float64(rrfK+rank+1)
		if _, exists := docs[r.ID]; !exists {
			docs[r.ID] = r
		}
	}
	type scored struct {
		id    string
		score float64
	}
	ranked := make([]scored, 0, len(scores))
	for id, s := range scores {
		ranked = append(ranked, scored{id: id, score: s})
	}
	sort.Slice(ranked, func(i, j int) bool {
		return ranked[i].score > ranked[j].score
	})
	// Group by page_id, limit to 7 results per page
	pageCount := make(map[string]int)
	results := make([]DocSearchResult, 0, limit)
	for _, s := range ranked {
		if len(results) >= limit {
			break
		}
		doc := docs[s.id]
		pageID := extractPageID(doc)
		if pageCount[pageID] >= 7 {
			continue
		}
		pageCount[pageID]++
		results = append(results, doc)
	}
	return results
}

// extractPageID derives a page identifier from the result for grouping.
func extractPageID(r DocSearchResult) string {
	if r.URL != "" {
		parts := strings.Split(r.URL, "#")
		return parts[0]
	}
	return r.ID
}

// docResultFromMap converts a map (from Hanzo Search hit or Hanzo Vector payload) to a DocSearchResult.
func docResultFromMap(m map[string]interface{}) DocSearchResult {
	r := DocSearchResult{}
	if v, ok := m["id"].(string); ok {
		r.ID = v
	}
	if v, ok := m["url"].(string); ok {
		r.URL = v
	}
	if v, ok := m["content"].(string); ok {
		r.Content = v
	}
	if v, ok := m["file_id"].(string); ok {
		r.FileID = v
	}
	if v, ok := m["title"].(string); ok {
		r.Title = v
	}
	if v, ok := m["section"].(string); ok && v != "" {
		r.Type = "heading"
	} else if v, ok := m["title"].(string); ok && v != "" {
		r.Type = "page"
	} else {
		r.Type = "text"
	}
	if v, ok := m["breadcrumbs"].([]interface{}); ok {
		crumbs := make([]string, 0, len(v))
		for _, c := range v {
			if s, ok := c.(string); ok {
				crumbs = append(crumbs, s)
			}
		}
		r.Breadcrumbs = crumbs
	}
	return r
}

// ── Product sinks: Hanzo Vector (semantic) and Hanzo Search (keyword) ─────────
// Hanzo Vector (Qdrant) and Hanzo Search (Meilisearch) are TWO SEPARATE
// products and are NEVER conflated. The RAG orchestrator
// (IndexDocuments/SearchDocuments) fans out to BOTH through the independent
// seams below — one write+read pair per product — so each product stays
// independently addressable and the fan-out is unit-testable (tests swap in
// in-memory fakes). Production code must route through these, never inline.
var (
	// indexSink writes documents to Hanzo Search (keyword/full-text).
	indexSink = writeDocsToSearch
	// vectorSink writes embedded documents to Hanzo Vector (semantic).
	vectorSink = writeDocsToVector
	// searchSink queries Hanzo Search (keyword) for an owner's index.
	searchSink = queryDocsFromSearch
	// vectorSearchSink queries Hanzo Vector (semantic) for an owner's index.
	vectorSearchSink = queryDocsFromVector
	// searchDeleteSink removes a file_id's chunks from Hanzo Search (keyword).
	searchDeleteSink = deleteFileFromSearch
	// vectorDeleteSink removes a file_id's chunks from Hanzo Vector (semantic).
	vectorDeleteSink = deleteFileFromVector
	// fileContextSink fetches all stored chunks of a file_id from Hanzo Search.
	fileContextSink = fetchFileChunksFromSearch
	// staleSink lists the ids inside a mirror's bounds that the write omits, with
	// the count of ids inside those bounds at all.
	staleSink = staleTagIDs
	// pruneSink removes ids from Hanzo Search (keyword).
	pruneSink = deleteIDsFromSearch
	// vectorPruneSink removes ids from Hanzo Vector (semantic).
	vectorPruneSink = deleteIDsFromVector
	// peakSink notes a corpus size and returns the largest that corpus has been.
	peakSink = recordPeak
	// peaksSink lists every recorded peak carrying a tag.
	peaksSink = peaks
	// peakResetSink forgets an index's peaks, which is what a Replace does.
	peakResetSink = clearPeaks
)

// resolveEmbedder resolves the admin-context embedding provider ONCE and returns
// a closure to embed text with it. Ingest and query MUST share this provider so
// the vectors written and the query vector live in the same space.
func resolveEmbedder(lang string) (func(string) ([]float32, error), error) {
	_, obj, err := GetEmbeddingProviderFromContext("admin", "", lang)
	if err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, fmt.Errorf("embedding provider not available")
	}
	return func(text string) ([]float32, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		vec, _, e := obj.QueryVector(text, ctx, lang)
		return vec, e
	}, nil
}

// ingestEmbed embeds a single piece of text (query path).
func ingestEmbed(text, lang string) ([]float32, error) {
	embed, err := resolveEmbedder(lang)
	if err != nil {
		return nil, err
	}
	return embed(text)
}

// writeDocsToSearch indexes documents into Hanzo Search (Meilisearch, keyword).
func writeDocsToSearch(indexName string, docs []DocIndex, replace bool) (int, error) {
	searchClient, err := getSearchClient()
	if err != nil {
		return 0, fmt.Errorf("search client unavailable: %w", err)
	}
	if err = ensureSearchIndex(searchClient, indexName); err != nil {
		return 0, err
	}
	index := searchClient.Index(indexName)
	if replace {
		task, delErr := index.DeleteAllDocuments((*meilisearch.DocumentOptions)(nil))
		if delErr != nil {
			return 0, fmt.Errorf("failed to clear index: %w", delErr)
		}
		if err = awaitTask(searchClient, task.TaskUID, 60*time.Second); err != nil {
			return 0, fmt.Errorf("failed waiting for index clear: %w", err)
		}
	}
	pk := "id"
	task, err := index.AddDocuments(docs, &meilisearch.DocumentOptions{PrimaryKey: &pk})
	if err != nil {
		return 0, fmt.Errorf("failed to index documents in Hanzo Search: %w", err)
	}
	if err = awaitTask(searchClient, task.TaskUID, 120*time.Second); err != nil {
		return 0, fmt.Errorf("failed waiting for Hanzo Search indexing: %w", err)
	}
	return len(docs), nil
}

// writeDocsToVector embeds and upserts documents into Hanzo Vector (Qdrant,
// semantic). Best-effort: a missing embedding provider returns an error that
// the orchestrator logs but does not surface — keyword indexing already won.
func writeDocsToVector(indexName string, docs []DocIndex, replace bool, lang string) error {
	if len(docs) == 0 {
		return nil
	}
	embed, err := resolveEmbedder(lang)
	if err != nil {
		return fmt.Errorf("embedding provider not available: %w", err)
	}
	sampleVector, err := embed(docs[0].Content)
	if err != nil {
		return fmt.Errorf("failed to get sample embedding: %w", err)
	}
	baseURL, apiKey := getVectorEndpoint()
	if err = ensureVectorCollection(baseURL, apiKey, indexName, len(sampleVector)); err != nil {
		return fmt.Errorf("ensure Hanzo Vector collection: %w", err)
	}
	if replace {
		if err = deleteAllVectorPoints(baseURL, apiKey, indexName); err != nil {
			return fmt.Errorf("clear Hanzo Vector collection: %w", err)
		}
		// The clear drops the collection itself, so the upserts below have nowhere
		// to land until it exists again.
		if err = ensureVectorCollection(baseURL, apiKey, indexName, len(sampleVector)); err != nil {
			return fmt.Errorf("recreate Hanzo Vector collection: %w", err)
		}
	}
	const batchSize = 50
	// Embeds within a batch run concurrently: each is an independent HTTP call
	// to the gateway, and running them one at a time made ingest wall-time the
	// sum of every round trip.
	const embedWorkers = 8
	for i := 0; i < len(docs); i += batchSize {
		end := i + batchSize
		if end > len(docs) {
			end = len(docs)
		}
		batch := docs[i:end]
		vecs := make([][]float32, len(batch))
		var wg sync.WaitGroup
		sem := make(chan struct{}, embedWorkers)
		for j, doc := range batch {
			wg.Add(1)
			sem <- struct{}{}
			go func(j int, content, id string) {
				defer wg.Done()
				defer func() { <-sem }()
				vec, embErr := embed(content)
				if embErr != nil {
					log.Warning("failed to embed document %s, skipping: %v", id, embErr)
					return
				}
				vecs[j] = vec
			}(j, doc.Content, doc.ID)
		}
		wg.Wait()
		points := make([]qdrantPoint, 0, len(batch))
		for j, doc := range batch {
			if vecs[j] == nil {
				continue
			}
			points = append(points, qdrantPoint{
				// The payload keeps the original id for reads; the point id is
				// derived, and derived in ONE place so deletes hit what writes made.
				ID:     vectorPointID(doc.ID),
				Vector: vecs[j],
				Payload: map[string]interface{}{
					"id": doc.ID, "page_id": doc.PageID, "title": doc.Title,
					"url": doc.URL, "content": doc.Content, "section": doc.Section,
					"section_id": doc.SectionID, "tag": doc.Tag, "breadcrumbs": doc.Breadcrumbs,
					"file_id": doc.FileID,
				},
			})
		}
		if len(points) > 0 {
			if err = upsertVectorPoints(baseURL, apiKey, indexName, points); err != nil {
				log.Warning("failed to upsert vector batch at %d: %v", i, err)
			}
		}
	}
	return nil
}

// queryDocsFromSearch queries Hanzo Search (keyword) for an index.
func queryDocsFromSearch(indexName, query, tag string, fileIDs []string, limit int) ([]DocSearchResult, error) {
	searchClient, err := getSearchClient()
	if err != nil {
		return nil, err
	}
	return searchFulltext(searchClient, indexName, query, tag, fileIDs, limit)
}

// queryDocsFromVector embeds the query and searches Hanzo Vector (semantic).
func queryDocsFromVector(indexName, query, tag string, fileIDs []string, limit int, lang string) ([]DocSearchResult, error) {
	vec, err := ingestEmbed(query, lang)
	if err != nil {
		return nil, fmt.Errorf("vector embedding failed: %w", err)
	}
	baseURL, apiKey := getVectorEndpoint()
	return searchVectorRaw(baseURL, apiKey, indexName, vec, tag, fileIDs, limit)
}

// SearchDocuments performs hybrid retrieval over the SAME unified index that
// IndexDocuments writes — Hanzo Search (keyword) + Hanzo Vector (semantic),
// fused with RRF. The two products are queried through independent seams and
// never conflated.
func SearchDocuments(owner, store string, req *DocSearchRequest, lang string) ([]DocSearchResult, error) {
	if req.Query == "" {
		return nil, fmt.Errorf("query must not be empty")
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 20
	}
	mode := req.Mode
	if mode == "" {
		mode = "hybrid"
	}
	indexName := GetSearchIndexName(owner, store)
	var fulltextResults []DocSearchResult
	var vectorResults []DocSearchResult
	if mode == "fulltext" || mode == "hybrid" {
		r, err := searchSink(indexName, req.Query, req.Tag, req.FileIDs, limit)
		if err != nil {
			log.Warning("keyword search failed for %s: %v", indexName, err)
			if mode == "fulltext" {
				return nil, err
			}
		} else {
			fulltextResults = r
		}
	}
	if mode == "vector" || mode == "hybrid" {
		r, err := vectorSearchSink(indexName, req.Query, req.Tag, req.FileIDs, limit, lang)
		if err != nil {
			log.Warning("vector search failed for %s: %v", indexName, err)
			if mode == "vector" {
				return nil, err
			}
		} else {
			vectorResults = r
		}
	}
	if mode == "fulltext" {
		return fulltextResults, nil
	}
	if mode == "vector" {
		return vectorResults, nil
	}
	// Hybrid: merge with RRF.
	if len(fulltextResults) == 0 && len(vectorResults) == 0 {
		return []DocSearchResult{}, nil
	}
	return mergeRRF(fulltextResults, vectorResults, limit), nil
}

// IndexDocuments is the single ingest fan-out: it writes every document to BOTH
// Hanzo Search (keyword) AND Hanzo Vector (semantic) under the per-tenant index
// {owner}-{store}-docs — the EXACT index SearchDocuments (and therefore /v1/chat
// retrieval) reads. Keyword indexing is authoritative; semantic indexing is
// best-effort so a missing embedding provider never drops the document.
func IndexDocuments(owner, store string, req *DocIndexRequest, lang string) (int, error) {
	if len(req.Documents) == 0 {
		// Nothing written means nothing known, so a mirror of nothing removes
		// nothing. A crawl that came back empty must not empty the index.
		return 0, nil
	}
	indexName := GetSearchIndexName(owner, store)
	// Read the tag's stored ids BEFORE writing: afterwards a document that just
	// arrived and one that was always there are the same row.
	var stale []string
	if req.Mirror != nil {
		var mErr error
		if stale, mErr = mirrorStale(indexName, req.Mirror, req.Documents); mErr != nil {
			// The documents are still written; the write simply stays additive,
			// which is where the index already was. A stale index beats a hole.
			log.Warning("mirror of %s in %s skipped, indexing additively: %v", req.Mirror.Root, indexName, mErr)
			stale = nil
		}
	}
	count, err := indexSink(indexName, req.Documents, req.Replace)
	if err != nil {
		return 0, err
	}
	if err := vectorSink(indexName, req.Documents, req.Replace, lang); err != nil {
		log.Warning("vector indexing skipped for %s: %v", indexName, err)
	}
	if req.Replace {
		// The corpus has just been stated outright, so how big it used to be says
		// nothing about how big it is.
		if err := peakResetSink(indexName); err != nil {
			log.Warning("could not clear corpus peaks for %s: %v", indexName, err)
		}
	}
	// Prune AFTER the write, so what sits between them is a SUPERSET of the tag
	// and never a subset: a mirror interrupted here leaves pages that should have
	// gone, never a gap where a live page should be, and the next crawl converges.
	if len(stale) > 0 {
		if err := prune(indexName, stale); err != nil {
			log.Warning("mirror of %s in %s left %d stale doc(s): %v", req.Mirror.Root, indexName, len(stale), err)
		} else {
			log.Info("mirror of %s in %s removed %d stale doc(s)", req.Mirror.Root, indexName, len(stale))
		}
	}
	return count, nil
}

// mirrorStale is what a mirror of tag would remove: the ids stored under that
// tag that this write does not carry.
//
// It REFUSES when the write carries no document under the tag at all. That reads
// as "every page of this site is gone", which is the one answer no caller ever
// means and the one that empties a corpus.
// absent reports whether an error means the thing simply is not there, which for
// every reader here is the same as finding it empty.
func absent(err error) bool {
	if err == nil {
		return false
	}
	e := strings.ToLower(err.Error())
	return strings.Contains(e, "index_not_found") || strings.Contains(e, "document_not_found")
}

// pruneFloor is the corpus size below which one pass may remove whatever it
// likes. See mirrorLimit.
const pruneFloor = 8

// extent is what the store already holds inside a mirror's bounds.
type extent struct {
	stale []string // ids the write does not carry
	docs  int      // documents inside the bounds
	pages int      // distinct pages those documents belong to

	// outside counts, for each scope the listing was asked about, the distinct
	// pages inside THAT scope that lie outside the mirror's bounds. This pass
	// cannot touch them, so they survive it — and counted per scope rather than
	// once for the whole tag, they credit a scope only with mass that scope
	// actually contains.
	outside map[string]int
}

// inScope reports whether a stored row lies inside a ceiling's subtree.
//
// A mirror deletes by URL containment, so a row with no URL at all — an uploaded
// file's chunks, which no crawl reaches and no mirror can remove — is inside no
// scope whatever. That is not a detail: counting unreachable rows as mass credits
// a ceiling with pages that were never at risk, and then DELETING them by some
// other route shrinks that credit and freezes a mirror that did nothing wrong.
//
// The empty scope root is the tag's whole URL space, the weakest ancestor there
// is, and it contains every row that has a URL.
func inScope(scopeRoot, u string) bool {
	if u == "" {
		return false
	}
	if scopeRoot == "" {
		return true
	}
	return under(scopeRoot, u)
}

// peak is the largest a corpus under one (tag, root) has been.
type peak struct {
	ID    string `json:"id"`
	Tag   string `json:"tag"`
	Root  string `json:"root"`
	Pages int    `json:"pages"`
}

// peakIndex is where corpus peaks live: BESIDE the documents, never among them.
//
// A peak is a fact about a CORPUS, not about any document in it. Kept on the
// documents it would be one fact written in a thousand places, and a record
// sitting in the docs index would answer searches. Its own index is the same
// store the mirror already talks to, so it costs no new dependency and no new
// credential, and nothing reads it but this. The name cannot collide with a docs
// index: those end in "-docs", and a caller may not spell a store with a hyphen.
func peakIndex(indexName string) string { return indexName + "-peak" }

// peakID names a peak record. The fields are length-prefixed because both are
// free text a caller supplies: joined by a separator either could contain, the
// tag record of tag "a\n" and the root record of (tag "a", root "\n") derive the
// same key.
func peakID(tag, root string) string {
	return hashID(fmt.Sprintf("%d:%s%d:%s", len(tag), tag, len(root), root))
}

// peaks lists every recorded peak carrying a tag.
func peaks(indexName, tag string) ([]peak, error) {
	client, err := getSearchClient()
	if err != nil {
		return nil, err
	}
	index := client.Index(peakIndex(indexName))
	const page = int64(1000)
	const maxRounds = 100
	var out []peak
	for round := 0; round < maxRounds; round++ {
		var resp meilisearch.DocumentsResult
		if err := index.GetDocuments(&meilisearch.DocumentsQuery{
			Limit:  page,
			Offset: int64(round) * page,
		}, &resp); err != nil {
			if absent(err) {
				return nil, nil
			}
			return nil, err
		}
		for _, hit := range resp.Results {
			var pk peak
			if decErr := hit.DecodeInto(&pk); decErr != nil {
				return nil, fmt.Errorf("undecodable peak in %s: %w", indexName, decErr)
			}
			if pk.Tag == tag {
				out = append(out, pk)
			}
		}
		if int64(len(resp.Results)) < page {
			return out, nil
		}
	}
	return nil, fmt.Errorf("listing peaks in %s did not end after %d pages", indexName, maxRounds)
}

// recordPeak notes that a corpus was seen at pages and returns the largest it has
// ever been. It only ever rises; mirrorCoverage says why.
//
// It is called BEFORE the refusals, so a REFUSED pass records too. That is
// deliberate and load-bearing, not an oversight to tidy up later: what gets
// recorded is the corpus's true size — the larger of what is stored and what the
// crawl carries — never the thin number a degraded crawl came back with. Moving
// this behind the checks would mean a run of refused passes taught the ceiling
// nothing, and the first pass that squeaked through would set it from a corpus
// nobody had measured since. The write must not move.
func recordPeak(indexName, tag, root string, pages int) (int, error) {
	client, err := getSearchClient()
	if err != nil {
		return 0, err
	}
	name := peakIndex(indexName)
	id := peakID(tag, root)
	var stored peak
	if getErr := client.Index(name).GetDocument(id, nil, &stored); getErr != nil && !absent(getErr) {
		return 0, getErr
	}
	if stored.Pages >= pages {
		return stored.Pages, nil // already at least this high; nothing to write
	}
	if err := ensurePeakIndex(client, name); err != nil {
		return 0, err
	}
	pk := "id"
	task, err := client.Index(name).AddDocuments(
		[]peak{{ID: id, Tag: tag, Root: root, Pages: pages}},
		&meilisearch.DocumentOptions{PrimaryKey: &pk})
	if err != nil {
		return 0, err
	}
	if err := awaitTask(client, task.TaskUID, 30*time.Second); err != nil {
		return 0, err
	}
	return pages, nil
}

// ensurePeakIndex creates the peak index if it is not there. It carries no
// searchable or filterable settings because nothing ever searches it — every
// read is a lookup by primary key.
func ensurePeakIndex(client meilisearch.ServiceManager, name string) error {
	if _, err := client.GetIndex(name); err == nil {
		return nil
	}
	task, err := client.CreateIndex(&meilisearch.IndexConfig{Uid: name, PrimaryKey: "id"})
	if err != nil {
		return fmt.Errorf("failed to create index %s: %w", name, err)
	}
	return awaitTask(client, task.TaskUID, 30*time.Second)
}

// clearPeaks forgets every peak for an index. A Replace states the whole corpus
// outright, so what that corpus used to be stops being evidence about what it is
// — and this is the one deliberate act that lets a genuinely shrunken corpus
// start converging again.
func clearPeaks(indexName string) error {
	client, err := getSearchClient()
	if err != nil {
		return err
	}
	task, err := client.DeleteIndex(peakIndex(indexName))
	if err != nil {
		if absent(err) {
			return nil
		}
		return err
	}
	return awaitTask(client, task.TaskUID, 30*time.Second)
}

// mirrorCoverage and mirrorLimit are BOTH load-bearing, and neither is the
// other's special case. Coverage counts PAGES and catches a crawl that saw too
// little of the corpus; the limit counts DOCUMENTS and catches what coverage
// cannot, because coverage's live side is derived from the crawl's own page ids
// — inflate those and coverage is satisfied while the documents still drain.
// Deleting either re-opens the other's blind spot.

// mirrorCoverage refuses a mirror whose crawl covered too little of what is
// already stored.
//
// This is the backstop for the one thing the crawl cannot prove about itself.
// The frontier proves a crawl READ everything it DISCOVERED; nothing proves it
// discovered everything there is, and nothing can, because a crawl only ever
// sees the links its source reported. A docs shell that renders its nav in the
// browser has no href to find. A crawl service that renames the field its links
// arrive in returns none. Either way the crawl reports one page, no unread URLs
// and no errors — a flawless crawl of a site that appears to be one page long,
// and from inside the crawl that is indistinguishable from a site that is.
//
// From outside it is not, because the store already knows how many pages this
// corpus spans. So ask it. A write covering at most half of them is not a site
// that lost the rest; it is a crawl that could not see them.
//
// It is the same boundary mirrorLimit draws, at the other granularity: no pass
// may remove more than half the PAGES, just as none may remove more than half
// the DOCUMENTS. Pages need no floor to go with it, which is the point — the
// proportion still means something at three pages, where a count of documents
// has to stand down. A page is also the unit this failure arrives in: losing the
// links on one page loses whole pages, never a scattering of sections.
//
// It is measured against the largest that corpus has recently been — its PEAK —
// and not the size it happens to be right now. That difference is the whole
// defence against a SEQUENCE. Measured against the current size, halving is
// legal every single time, so twenty pages reach one in five individually legal
// steps: no errors, nothing unread, each pass looking exactly like a site that
// shed half its pages. A site progressively unlinking pages it still serves — a
// nav migrating to JavaScript section by section over a week of deploys — walks
// that ladder without anyone doing anything wrong. Against the peak, the second
// step is refused and the ladder never starts.
//
// The peak only ever RISES. It is deliberately not allowed to settle back to
// whatever the last pass happened to see, because a mark that follows the corpus
// down is no mark at all — that is the drain again, one indirection later. It
// does not age out either: a slow enough drain would simply outwait any clock.
//
// So the cost is a real one, stated plainly: a corpus that genuinely loses more
// than half its pages stops converging until someone states the new corpus
// outright with Replace, which clears the peak. A prune wrongly refused costs
// staleness; a prune wrongly allowed costs the corpus. That is the trade, and it
// is not close.
func mirrorCoverage(livePages, peakPages int, scope string) error {
	if livePages*2 < peakPages {
		return fmt.Errorf("pass leaves %d pages in %s, against %d at its largest; refusing to read the rest as deleted", livePages, scope, peakPages)
	}
	return nil
}

// mirrorLimit refuses a prune that would take most of a corpus in one pass.
//
// mirrorStale already refuses a crawl offering NO document under the bounds,
// because "every page is gone" is an answer no caller means. A crawl offering
// two documents against two hundred stored is the same answer one notch weaker,
// and earns the same refusal. This is the backstop for the truncation nobody has
// enumerated yet: a crawl can go wrong in a way no signal catches, and this still
// keeps the damage to a fraction of the corpus.
//
// Two bounds, because either alone is wrong. A ratio alone would deny a small
// site any real edit — three of five pages retired is 60% and perfectly
// ordinary. A floor alone would wave through nine hundred deletions in a corpus
// of a thousand. Together: under the floor, absolute size makes the ratio
// meaningless and ordinary churn passes; over it, no single pass takes more
// than half.
//
// The floor is about one page's worth of documents — a page indexes as itself
// plus one document per section — so a page or two may always come and go, and
// if that judgement is ever wrong the cost is a re-crawl.
//
// A refusal is not a failure: the write still lands, the index simply stays
// additive, and the log names the shape. A corpus that really did lose most of
// itself is retired deliberately with Replace, by someone who meant it.
func mirrorLimit(stale, stored int, m *Mirror) error {
	// The ratio applies once the CORPUS clears the floor, not once the deletion
	// does. Gated the other way round, a corpus of ten was exempt whenever the
	// prune was eight or fewer — which is eighty per cent of it, at exactly the
	// size where a proportion is the only thing left to judge by.
	if stored > pruneFloor && stale*2 > stored {
		return fmt.Errorf("refusing to remove %d of %d documents under %s in one pass", stale, stored, m.Root)
	}
	return nil
}

func mirrorStale(indexName string, m *Mirror, docs []DocIndex) ([]string, error) {
	if m.Tag == "" || m.Root == "" {
		return nil, fmt.Errorf("a mirror needs both a tag and a root; got %q and %q", m.Tag, m.Root)
	}
	live := make(map[string]bool, len(docs))
	livePages := make(map[string]bool)
	for _, d := range docs {
		// A document outside the bounds is no part of this mirror. Writing it is
		// fine; letting it widen what the mirror removes is not.
		if d.Tag == m.Tag && under(m.Root, d.URL) {
			live[d.ID] = true
			// Every document a page produces — the page and one per section — carries
			// that page's id, so this counts PAGES READ rather than documents
			// written. A single page with forty sections is still one page. A
			// document with no page of its own stands for one, exactly as it is
			// counted on the stored side.
			if page := d.PageID; page != "" {
				livePages[page] = true
			} else {
				livePages[d.ID] = true
			}
		}
	}
	if len(live) == 0 {
		return nil, fmt.Errorf("no document lies in %q under %s", m.Tag, m.Root)
	}
	// The scopes that can legitimately constrain this pass are exactly the roots
	// that CONTAIN it, because a mirror deletes exactly the rows under its own
	// root. Ancestry IS that containment relation, so there is no third scope to
	// escape to: a deeper root is still inside every root above it.
	//
	// Keying the ceiling on the pass's own root alone let a caller step around it
	// by adding a path segment — a root never seen before starts from whatever is
	// there now, which is already the reduced count the last step left. Keying it
	// on the whole tag did not bind either: the tag credits a pass with every page
	// under it, and the pages a nested walk consumes are exactly the ones NOT in
	// that credit, so any sibling corpus under the same tag handed the walk all the
	// headroom it wanted. Measured per ancestor, every subtree is judged on its own
	// mass, and a sibling corpus credits nothing to the subtree being drained.
	recorded, err := peaksSink(indexName, m.Tag)
	if err != nil {
		return nil, fmt.Errorf("corpus peaks unavailable: %w", err)
	}
	scopes := []string{m.Root, ""}
	ceiling := map[string]int{}
	proper := false
	for _, pk := range recorded {
		if pk.Root == "" || pk.Root == m.Root || !under(pk.Root, m.Root) {
			continue
		}
		ceiling[pk.Root] = pk.Pages
		scopes = append(scopes, pk.Root)
		proper = true
	}
	e, err := staleSink(indexName, m, live, scopes)
	if err != nil {
		return nil, err
	}
	// What survives this pass inside a scope: the pages of that scope it cannot
	// touch, plus the ones it read. Every live page is under m.Root, and m.Root is
	// inside every scope here, so the live count applies to all of them.
	survivors := func(scope string) int { return e.outside[scope] + len(livePages) }

	// Note how big each scope is BEFORE judging the pass, and judge against the
	// largest it has ever been. A pass can only measure itself against the store
	// as it stands, and a rule that only ever looks at now cannot see a sequence.
	own := e.pages
	if v := survivors(m.Root); v > own {
		own = v
	}
	high, err := peakSink(indexName, m.Tag, m.Root, own)
	if err != nil {
		// Without the history there is no way to tell a shrinking site from a
		// shrinking crawl, so nothing is removed.
		return nil, fmt.Errorf("corpus peak unavailable: %w", err)
	}
	ceiling[m.Root] = high

	// The tag record is the weakest ancestor — the whole URL space of the tag. It
	// is recorded ALWAYS, so it can never be seeded fresh from an already reduced
	// count, and applied only when no narrower recorded root contains this pass.
	// With a narrower one present it would add nothing but a coarser number, and a
	// coarser number is exactly what mass shrinking somewhere else under the tag
	// turns into a refusal of a pass that did nothing wrong.
	whole := e.outside[""] + e.pages
	if v := survivors(""); v > whole {
		whole = v
	}
	tagHigh, err := peakSink(indexName, m.Tag, "", whole)
	if err != nil {
		return nil, fmt.Errorf("tag peak unavailable: %w", err)
	}
	if !proper {
		ceiling[""] = tagHigh
	}

	// Two refusals, asking opposite questions of the same pass: did the crawl see
	// enough of the corpus to speak for it, and is it taking too much of it. The
	// first is asked once per ancestor, and the pass must clear every one.
	for _, scope := range scopes {
		highWater, ok := ceiling[scope]
		if !ok {
			continue
		}
		name := scope
		if scope == "" {
			name = fmt.Sprintf("tag %q", m.Tag)
		}
		if err := mirrorCoverage(survivors(scope), highWater, name); err != nil {
			return nil, err
		}
	}
	if err := mirrorLimit(len(e.stale), e.docs, m); err != nil {
		return nil, err
	}
	return e.stale, nil
}

// prune removes the given ids from BOTH stores.
//
// Vector goes FIRST because the ids were read from the keyword store: erase them
// there while the vector delete is still failing and the only record of what is
// left to remove goes with them. This order makes a failure retry cleanly on the
// next crawl instead of stranding embeddings that answer for deleted pages.
func prune(indexName string, ids []string) error {
	if err := vectorPruneSink(indexName, ids); err != nil {
		return err
	}
	return pruneSink(indexName, ids)
}

// staleTagIDs returns the ids inside the mirror's bounds that live does not
// contain.
//
// The listing is filtered by tag AND every row is checked against it again on
// the way out. The filter is the control; the second check is what holds if it
// silently does not apply, because everything this returns gets deleted and a
// filter that quietly matched everything would take the whole index with it.
// It also returns how much lies inside the bounds at all — documents, and the
// distinct pages they belong to — which is what the two refusals are measured
// against.
//
// The listing is read before the write and the delete happens after it, so a
// page added in between is listed as stale and removed, and the next crawl puts
// it back. That window is one crawl wide and self-heals; closing it would need a
// transaction the search backend does not offer.
func staleTagIDs(indexName string, m *Mirror, live map[string]bool, scopes []string) (extent, error) {
	if m.Tag == "" || m.Root == "" {
		return extent{}, fmt.Errorf("refusing to prune on an unbounded mirror")
	}
	client, err := getSearchClient()
	if err != nil {
		return extent{}, err
	}
	index := client.Index(indexName)
	const page = int64(1000)
	// A backend that ignores Offset returns the same page forever. Bound the walk
	// and fail rather than spin: an error withholds the prune, which is the safe
	// end of a listing that cannot be trusted to terminate.
	const maxRounds = 1000
	var out extent
	pages := make(map[string]bool)
	// One survivor set per scope. The listing already visits every row carrying
	// the tag, so counting them per scope costs no extra read — the same reason
	// the tag-wide count was free.
	outside := make(map[string]map[string]bool, len(scopes))
	for _, sc := range scopes {
		outside[sc] = map[string]bool{}
	}
	for round := 0; round < maxRounds; round++ {
		var resp meilisearch.DocumentsResult
		if err := index.GetDocuments(&meilisearch.DocumentsQuery{
			Filter: buildMeiliFilter(m.Tag, nil),
			Fields: []string{"id", "tag", "url", "page_id"},
			Limit:  page,
			Offset: int64(round) * page,
		}, &resp); err != nil {
			if absent(err) {
				return extent{}, nil // nothing stored yet, so nothing is stale
			}
			return extent{}, err
		}
		for _, hit := range resp.Results {
			row := make(map[string]interface{})
			if decErr := hit.DecodeInto(&row); decErr != nil {
				// One unreadable row and the whole prune is off. Skipping it would
				// price a partial listing as a complete one, and a prune reading a
				// partial listing deletes everything the missing rows held.
				return extent{}, fmt.Errorf("undecodable document in %s: %w", indexName, decErr)
			}
			id, _ := row["id"].(string)
			rowTag, _ := row["tag"].(string)
			rowURL, _ := row["url"].(string)
			rowPage, _ := row["page_id"].(string)
			if id == "" || rowTag != m.Tag {
				continue
			}
			// A row with no page id of its own stands for a page of its own.
			if rowPage == "" {
				rowPage = id
			}
			if !under(m.Root, rowURL) {
				// Untouched by this pass, so it survives — and it survives INSIDE
				// whichever scopes contain it, and no others.
				for _, sc := range scopes {
					if inScope(sc, rowURL) {
						outside[sc][rowPage] = true
					}
				}
				continue
			}
			out.docs++
			pages[rowPage] = true
			if !live[id] {
				out.stale = append(out.stale, id)
			}
		}
		if int64(len(resp.Results)) < page {
			out.pages = len(pages)
			out.outside = make(map[string]int, len(outside))
			for sc, set := range outside {
				out.outside[sc] = len(set)
			}
			return out, nil
		}
	}
	return extent{}, fmt.Errorf("listing %s under %s did not end after %d pages", indexName, m.Root, maxRounds)
}

// deleteIDsFromSearch removes documents from Hanzo Search by primary key.
func deleteIDsFromSearch(indexName string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	client, err := getSearchClient()
	if err != nil {
		return err
	}
	index := client.Index(indexName)
	task, err := index.DeleteDocuments(ids, nil)
	if err != nil {
		if absent(err) {
			return nil
		}
		return err
	}
	return awaitTask(client, task.TaskUID, 60*time.Second)
}

// deleteIDsFromVector removes the points of the given document ids from Hanzo
// Vector. A point id is derived from the document id, so what goes is exactly
// the set named — there is no filter here to widen.
func deleteIDsFromVector(indexName string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	baseURL, apiKey := getVectorEndpoint()
	const batch = 1000
	for i := 0; i < len(ids); i += batch {
		end := i + batch
		if end > len(ids) {
			end = len(ids)
		}
		points := make([]string, 0, end-i)
		for _, id := range ids[i:end] {
			points = append(points, vectorPointID(id))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := deleteVectorPoints(ctx, baseURL, apiKey, indexName, map[string]interface{}{"points": points})
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}

// vectorPointID is the Hanzo Vector point a document id occupies. Hanzo Vector
// accepts only unsigned-integer or UUID point ids and a chunk id is a hex hash,
// so it rides as a deterministic UUID: same id, same point, so a write stays an
// upsert and a delete finds what the write put there. Both sides call HERE —
// derive it twice and the day they disagree is the day deletes silently miss.
func vectorPointID(docID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(docID)).String()
}

// upsertVectorPoints sends a batch of points to Hanzo Vector.
func upsertVectorPoints(baseURL, apiKey, collectionName string, points []qdrantPoint) error {
	url := fmt.Sprintf("%s/collections/%s/points", baseURL, collectionName)
	reqBody := qdrantUpsertRequest{Points: points}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal Hanzo Vector upsert body: %w", err)
	}
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("failed to build Hanzo Vector upsert request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("api-key", apiKey)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Hanzo Vector upsert request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("qdrant upsert returned %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// deleteAllVectorPoints drops a Qdrant collection outright.
//
// It reports failure rather than swallowing it. A Replace whose vector half
// quietly failed leaves embeddings for documents the keyword half no longer
// holds, and since the stale set is read FROM the keyword store, nothing can
// ever find them again — they answer queries about deleted pages forever.
func deleteAllVectorPoints(baseURL, apiKey, collectionName string) error {
	url := fmt.Sprintf("%s/collections/%s", baseURL, collectionName)
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("failed to build Hanzo Vector delete request: %w", err)
	}
	if apiKey != "" {
		req.Header.Set("api-key", apiKey)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Hanzo Vector delete request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotFound {
		return nil // absent is the state we were asking for
	}
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("Hanzo Vector delete returned %d: %s", resp.StatusCode, string(body))
}

// deleteVectorPointsByFilter removes the points matching a payload filter from a
// Qdrant collection (e.g. all chunks of one file_id).
func deleteVectorPointsByFilter(ctx context.Context, baseURL, apiKey, collectionName string, filter *qdrantFilter) error {
	return deleteVectorPoints(ctx, baseURL, apiKey, collectionName, map[string]interface{}{"filter": filter})
}

// deleteVectorPoints posts one delete selector — a payload filter, or an
// explicit list of point ids — to a Hanzo Vector collection. A missing
// collection is treated as success (nothing to delete).
func deleteVectorPoints(ctx context.Context, baseURL, apiKey, collectionName string, selector map[string]interface{}) error {
	url := fmt.Sprintf("%s/collections/%s/points/delete", baseURL, collectionName)
	body, err := json.Marshal(selector)
	if err != nil {
		return fmt.Errorf("failed to marshal Hanzo Vector delete body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to build Hanzo Vector delete request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("api-key", apiKey)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Hanzo Vector delete request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil // collection absent -> nothing to delete
	}
	respBody, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("Hanzo Vector delete returned %d: %s", resp.StatusCode, string(respBody))
}

// GetDocIndexStats returns statistics about the Meilisearch index.
func GetDocIndexStats(owner, store string) (*DocStatsResponse, error) {
	indexName := GetSearchIndexName(owner, store)
	searchClient, err := getSearchClient()
	if err != nil {
		return nil, err
	}
	index := searchClient.Index(indexName)
	stats, err := index.GetStats()
	if err != nil {
		return nil, fmt.Errorf("failed to get index stats: %w", err)
	}
	return &DocStatsResponse{
		DocumentCount: int(stats.NumberOfDocuments),
		IsIndexing:    stats.IsIndexing,
		Fields:        stats.FieldDistribution,
	}, nil
}

// GetDocChatAnswer performs RAG: searches for relevant docs, then generates an answer.
func GetDocChatAnswer(owner, store string, req *DocChatRequest, lang string) (string, *model.ModelResult, error) {
	searchReq := &DocSearchRequest{
		Query: req.Query,
		Tag:   req.Tag,
		Limit: 10,
		Mode:  "hybrid",
	}
	results, err := SearchDocuments(owner, store, searchReq, lang)
	if err != nil {
		return "", nil, fmt.Errorf("search for RAG context failed: %w", err)
	}
	knowledge := make([]*model.RawMessage, 0, len(results))
	for _, r := range results {
		text := r.Content
		if len(r.Breadcrumbs) > 0 {
			text = fmt.Sprintf("[%s] %s", strings.Join(r.Breadcrumbs, " > "), text)
		}
		knowledge = append(knowledge, &model.RawMessage{
			Text:   text,
			Author: "System",
		})
	}
	prompt := req.Prompt
	if prompt == "" {
		prompt = "You are a documentation assistant. " +
			"Answer the user's question using the provided documentation context. " +
			"Be concise and direct. Use markdown formatting. Include links when relevant."
	}
	history := []*model.RawMessage{}
	provider := os.Getenv("CHAT_MODEL_PROVIDER")
	if provider == "" {
		provider = "fireworks"
	}
	answer, modelResult, err := GetAnswerWithContext(provider, req.Query, history, knowledge, prompt, lang)
	if err != nil {
		return "", nil, fmt.Errorf("RAG answer generation failed: %w", err)
	}
	return answer, modelResult, nil
}
