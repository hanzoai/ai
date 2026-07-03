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

// File-scoped RAG — the consolidated home of the retired standalone
// chat-rag-api (danny-avila/rag_api fork). LibreChat / hanzo.chat embeds each
// uploaded file under a `file_id`, then retrieves chunks scoped to that file
// (or a set of files). This is the SAME unified pipeline the doc/crawl RAG uses:
//
//   - chunking:   split.RecursiveSplitProvider (parity with rag-api's
//     RecursiveCharacterTextSplitter, CHUNK_SIZE=1500 / CHUNK_OVERLAP=100)
//   - parsing:    txt.GetParsedTextFromUrl (PDF/CSV/XLSX/PPTX/… same as ingest)
//   - storage:    IndexDocuments → Hanzo Search (keyword) + Hanzo Vector
//     (semantic) under {owner}-{store}-docs, with file_id as a filter dimension
//   - retrieval:  SearchDocuments with FileIDs set (file-scoped hybrid search)
//
// There is NO parallel store: file-scoped retrieval is a filter over the one
// tenant index, so uploaded-file RAG and doc RAG share one index, one embedder,
// one retrieval path. This is what makes chat-rag-api redundant.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/hanzoai/ai/split"
	"github.com/hanzoai/ai/txt"
	meilisearch "github.com/hanzoai/search-go"
)

// RagFileStore is the default store/index slug for uploaded-file RAG. It keeps
// per-file uploads in their own tenant index ({owner}-rag-files-docs) so a noisy
// upload set never pollutes the curated docs index, while still riding the exact
// same Search+Vector pipeline. Callers may override via the request `store`.
const RagFileStore = "rag-files"

// ragChunkSize / ragChunkOverlap mirror the retired rag-api defaults so chunk
// boundaries — and therefore retrieval recall — match what chat.hanzo.ai users
// already have. Overridable per call via RagEmbedRequest.
const (
	ragChunkSize    = 1500
	ragChunkOverlap = 100
)

// RagEmbedRequest ingests one file's text under a file_id. Supply exactly one of
// Content (inline UTF-8/text) or URL (fetched + parsed server-side). Filename's
// extension selects the parser (PDF/CSV/XLSX/… via txt.GetParsedTextFromUrl).
type RagEmbedRequest struct {
	FileID       string `json:"file_id"`
	Filename     string `json:"filename,omitempty"`
	Content      string `json:"content,omitempty"`
	URL          string `json:"url,omitempty"`
	Store        string `json:"store,omitempty"`
	Tag          string `json:"tag,omitempty"`
	ChunkSize    int    `json:"chunk_size,omitempty"`
	ChunkOverlap int    `json:"chunk_overlap,omitempty"`
}

// RagEmbedResult reports the outcome of embedding one file.
type RagEmbedResult struct {
	FileID    string `json:"file_id"`
	Filename  string `json:"filename,omitempty"`
	Store     string `json:"store"`
	IndexName string `json:"index_name"`
	Chunks    int    `json:"chunks"`
}

// RagQueryRequest retrieves chunks scoped to one or more uploaded files. Set
// FileID for the single-file case or FileIDs for the multi-file case; at least
// one is required (an unrestricted query belongs on /v1/search, not here).
type RagQueryRequest struct {
	Query   string   `json:"query"`
	FileID  string   `json:"file_id,omitempty"`
	FileIDs []string `json:"file_ids,omitempty"`
	K       int      `json:"k,omitempty"`
	Store   string   `json:"store,omitempty"`
	Mode    string   `json:"mode,omitempty"` // hybrid (default) | fulltext | vector
}

// resolveRagStore returns the store slug, defaulting to RagFileStore.
func resolveRagStore(store string) string {
	if store == "" {
		return RagFileStore
	}
	return store
}

// mergeFileIDs unions the single FileID and the FileIDs slice, dropping empties
// and duplicates. This lets /query (file_id) and /query-multiple (file_ids)
// share one retrieval call.
func mergeFileIDs(fileID string, fileIDs []string) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	add(fileID)
	for _, id := range fileIDs {
		add(id)
	}
	return out
}

// RagEmbedFile parses + chunks + indexes one file under its file_id into the
// unified Search+Vector index, scoped to the authenticated owner. Re-embedding
// the same file_id replaces its chunks (idempotent) so a re-upload doesn't
// duplicate. Returns the chunk count indexed.
func RagEmbedFile(owner string, req *RagEmbedRequest, lang string) (*RagEmbedResult, error) {
	if owner == "" {
		return nil, fmt.Errorf("owner must not be empty")
	}
	if req.FileID == "" {
		return nil, fmt.Errorf("file_id must not be empty")
	}
	store := resolveRagStore(req.Store)

	name := req.Filename
	if name == "" {
		name = req.FileID
	}

	text := req.Content
	if text == "" {
		if req.URL == "" {
			return nil, fmt.Errorf("either content or url must be provided")
		}
		var err error
		text, err = txt.GetParsedTextFromUrl(req.URL, strings.ToLower(filepath.Ext(name)), lang)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
	}
	text = strings.TrimSpace(text)

	result := &RagEmbedResult{
		FileID:    req.FileID,
		Filename:  req.Filename,
		Store:     store,
		IndexName: GetSearchIndexName(owner, store),
	}
	if text == "" {
		// Nothing to index, but a clean (empty) replace so a re-upload of an
		// emptied file clears stale chunks.
		if err := DeleteRagFile(owner, store, req.FileID, lang); err != nil {
			return nil, err
		}
		return result, nil
	}

	chunkSize := req.ChunkSize
	if chunkSize <= 0 {
		chunkSize = ragChunkSize
	}
	overlap := req.ChunkOverlap
	if overlap <= 0 {
		overlap = ragChunkOverlap
	}
	splitter, err := split.NewRecursiveSplitProvider(chunkSize, overlap)
	if err != nil {
		return nil, err
	}
	sections, err := splitter.SplitText(text)
	if err != nil {
		return nil, err
	}

	pageID := hashID(owner + "/" + store + "/file/" + req.FileID)
	docs := make([]DocIndex, 0, len(sections))
	for i, section := range sections {
		section = strings.TrimSpace(section)
		if section == "" {
			continue
		}
		docs = append(docs, DocIndex{
			ID:          hashID(fmt.Sprintf("%s/%s/file/%s#%d", owner, store, req.FileID, i)),
			PageID:      pageID,
			Title:       name,
			Content:     section,
			Tag:         req.Tag,
			FileID:      req.FileID,
			Breadcrumbs: []string{name},
		})
	}
	if len(docs) == 0 {
		return result, nil
	}

	// Replace this file's prior chunks without wiping the whole tenant index
	// (Replace=true on IndexDocuments clears the entire index — wrong here since
	// many files share one index). Delete just this file_id, then add.
	if err := DeleteRagFile(owner, store, req.FileID, lang); err != nil {
		return nil, err
	}
	n, err := IndexDocuments(owner, store, &DocIndexRequest{Documents: docs, Replace: false}, lang)
	if err != nil {
		return nil, err
	}
	result.Chunks = n
	return result, nil
}

// RagQuery retrieves the top-K chunks relevant to the query, scoped to the
// given file(s), via the unified hybrid retrieval path.
func RagQuery(owner string, req *RagQueryRequest, lang string) ([]DocSearchResult, error) {
	if owner == "" {
		return nil, fmt.Errorf("owner must not be empty")
	}
	if req.Query == "" {
		return nil, fmt.Errorf("query must not be empty")
	}
	fileIDs := mergeFileIDs(req.FileID, req.FileIDs)
	if len(fileIDs) == 0 {
		return nil, fmt.Errorf("at least one file_id is required")
	}
	k := req.K
	if k <= 0 {
		k = 4
	}
	store := resolveRagStore(req.Store)
	searchReq := &DocSearchRequest{
		Query:   req.Query,
		Limit:   k,
		Mode:    req.Mode,
		FileIDs: fileIDs,
	}
	return SearchDocuments(owner, store, searchReq, lang)
}

// DeleteRagFile removes all chunks of a file_id from BOTH Hanzo Search and Hanzo
// Vector for the owner's index. Best-effort per product: a failure to reach one
// product is returned, but does not prevent attempting the other.
func DeleteRagFile(owner, store, fileID, lang string) error {
	if fileID == "" {
		return fmt.Errorf("file_id must not be empty")
	}
	store = resolveRagStore(store)
	indexName := GetSearchIndexName(owner, store)
	var errs []string
	if err := searchDeleteSink(indexName, fileID); err != nil {
		errs = append(errs, "search: "+err.Error())
	}
	if err := vectorDeleteSink(indexName, fileID); err != nil {
		errs = append(errs, "vector: "+err.Error())
	}
	if len(errs) > 0 {
		return fmt.Errorf("delete file %s: %s", fileID, strings.Join(errs, "; "))
	}
	return nil
}

// RagFileContext returns every chunk of a file_id (ordered as stored), for the
// "load full document context" use case (rag-api's /documents/{id}/context).
func RagFileContext(owner, store, fileID string) ([]DocSearchResult, error) {
	if fileID == "" {
		return nil, fmt.Errorf("file_id must not be empty")
	}
	store = resolveRagStore(store)
	indexName := GetSearchIndexName(owner, store)
	return fileContextSink(indexName, fileID)
}

// deleteFileFromSearch removes a file's chunks from Hanzo Search by filter.
func deleteFileFromSearch(indexName, fileID string) error {
	client, err := getSearchClient()
	if err != nil {
		return err
	}
	index := client.Index(indexName)
	task, err := index.DeleteDocumentsByFilter(buildMeiliFilter("", []string{fileID}), nil)
	if err != nil {
		// A missing index means nothing to delete — treat as success.
		if strings.Contains(strings.ToLower(err.Error()), "index_not_found") {
			return nil
		}
		return err
	}
	if _, err = client.WaitForTask(task.TaskUID, 30*time.Second); err != nil {
		return err
	}
	return nil
}

// fetchFileChunksFromSearch returns all stored chunks for a file_id.
func fetchFileChunksFromSearch(indexName, fileID string) ([]DocSearchResult, error) {
	client, err := getSearchClient()
	if err != nil {
		return nil, err
	}
	index := client.Index(indexName)
	var resp meilisearch.DocumentsResult
	err = index.GetDocuments(&meilisearch.DocumentsQuery{
		Filter: buildMeiliFilter("", []string{fileID}),
		Limit:  1000,
	}, &resp)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "index_not_found") {
			return []DocSearchResult{}, nil
		}
		return nil, err
	}
	out := make([]DocSearchResult, 0, len(resp.Results))
	for _, hit := range resp.Results {
		m := make(map[string]interface{})
		if decErr := hit.DecodeInto(&m); decErr != nil {
			continue
		}
		out = append(out, docResultFromMap(m))
	}
	return out, nil
}

// deleteFileFromVector removes a file's points from Hanzo Vector by payload
// filter (file_id match).
func deleteFileFromVector(indexName, fileID string) error {
	baseURL, apiKey := getVectorEndpoint()
	filter := &qdrantFilter{Must: []qdrantCondition{{Key: "file_id", Match: qdrantMatch{Value: fileID}}}}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return deleteVectorPointsByFilter(ctx, baseURL, apiKey, indexName, filter)
}
