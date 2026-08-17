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
	// (/v1/rag/query, /v1/rag/query-multiple) without a separate index.
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

// ensureSearchIndex creates the Hanzo Search index if it does not exist and configures it.
func ensureSearchIndex(client meilisearch.ServiceManager, indexName string) error {
	_, err := client.GetIndex(indexName)
	if err != nil {
		task, createErr := client.CreateIndex(&meilisearch.IndexConfig{
			Uid:        indexName,
			PrimaryKey: "id",
		})
		if createErr != nil {
			return fmt.Errorf("failed to create index %s: %w", indexName, createErr)
		}
		if err = awaitTask(client, task.TaskUID, 30*time.Second); err != nil {
			return fmt.Errorf("failed waiting for index creation: %w", err)
		}
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
		deleteAllVectorPoints(baseURL, apiKey, indexName)
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
				// Hanzo Vector accepts only unsigned-integer or UUID point ids;
				// the chunk id is a hex hash, so it rides as a deterministic
				// UUID (same id → same point → upsert stays an upsert) and the
				// payload keeps the original for reads.
				ID:     uuid.NewSHA1(uuid.NameSpaceOID, []byte(doc.ID)).String(),
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
		return 0, nil
	}
	indexName := GetSearchIndexName(owner, store)
	count, err := indexSink(indexName, req.Documents, req.Replace)
	if err != nil {
		return 0, err
	}
	if err := vectorSink(indexName, req.Documents, req.Replace, lang); err != nil {
		log.Warning("vector indexing skipped for %s: %v", indexName, err)
	}
	return count, nil
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

// deleteAllVectorPoints removes all points from a Qdrant collection.
func deleteAllVectorPoints(baseURL, apiKey, collectionName string) {
	url := fmt.Sprintf("%s/collections/%s", baseURL, collectionName)
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		log.Warning("failed to build qdrant delete request: %v", err)
		return
	}
	if apiKey != "" {
		req.Header.Set("api-key", apiKey)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Warning("qdrant delete request failed: %v", err)
		return
	}
	defer resp.Body.Close()
}

// deleteVectorPointsByFilter removes the points matching a payload filter from a
// Qdrant collection (e.g. all chunks of one file_id). A missing collection is
// treated as success (nothing to delete).
func deleteVectorPointsByFilter(ctx context.Context, baseURL, apiKey, collectionName string, filter *qdrantFilter) error {
	url := fmt.Sprintf("%s/collections/%s/points/delete", baseURL, collectionName)
	body, err := json.Marshal(map[string]interface{}{"filter": filter})
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
