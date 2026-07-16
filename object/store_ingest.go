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

// Unified RAG ingestion orchestrator.
//
// hanzoai/ai is the ONE RAG layer: it parses, chunks, embeds, and pipes every
// document to BOTH Hanzo Vector (semantic) AND Hanzo Search (keyword) via
// IndexDocuments — the SAME per-tenant index {owner}-{store}-docs that /v1/chat
// retrieval (object.SearchDocuments) reads. Every ingest source — uploaded
// files, store object-storage (S3), a GitHub repo, or a web crawl — converges
// here. The legacy SQL `vector` table is NO LONGER written on ingest (see
// vector_embedding.go); uploads now actually power chat RAG.

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hanzoai/ai/split"
	"github.com/hanzoai/ai/txt"
	"github.com/hanzoai/beego/logs"
)

// DefaultDocsStore is the tenant's default documentation store/index slug. It is
// brand-NEUTRAL by design: every org (hanzo, lux, zoo, any customer) gets its own
// isolated index `{owner}-docs-docs` because the owner prefix — bound to the
// authenticated principal — is what separates tenants. Baking a brand here (the old
// "docs-hanzo-ai") would have shown Hanzo's slug on every org's store, so it is not.
const DefaultDocsStore = "docs"

// IngestRequest is the source-pluggable body for POST /v1/docs/ingest. Exactly
// one source is selected by the `source` discriminator; its matching sub-object
// supplies the inputs. All sources land in the same Vector+Search index.
type IngestRequest struct {
	Store   string `json:"store,omitempty"`  // tenant store slug; default DefaultDocsStore
	Source  string `json:"source,omitempty"` // upload | github | crawl | s3 (default: upload)
	Replace bool   `json:"replace,omitempty"`
	Tag     string `json:"tag,omitempty"` // default tag applied to indexed docs

	// source=upload
	Documents []DocIndex   `json:"documents,omitempty"` // pre-chunked passthrough (same shape as /v1/index)
	Files     []IngestFile `json:"files,omitempty"`     // raw files: parsed -> split -> indexed

	// source=github
	GitHub *GitHubIngestRequest `json:"github,omitempty"`

	// source=crawl
	Crawl *ScrapeRequest `json:"crawl,omitempty"`

	// source=s3
	S3 *S3IngestRequest `json:"s3,omitempty"`
}

// IngestFile is a single raw document supplied inline (upload source). Provide
// either Content (UTF-8 text) or URL (fetched + parsed server-side). The Name's
// extension selects the parser and splitter (.md -> Markdown, etc).
type IngestFile struct {
	Name    string `json:"name"`
	Content string `json:"content,omitempty"`
	URL     string `json:"url,omitempty"`
	Tag     string `json:"tag,omitempty"`
}

// S3IngestRequest scopes an ingest of the store's backing Hanzo S3 space.
type S3IngestRequest struct {
	Prefix string `json:"prefix,omitempty"` // object-key prefix within the store's space
}

// IngestStats summarizes one ingest operation.
type IngestStats struct {
	Source           string   `json:"source"`
	Store            string   `json:"store"`
	IndexName        string   `json:"indexName"`
	FilesIngested    int      `json:"filesIngested"`
	FilesSkipped     int      `json:"filesSkipped"`
	DocumentsIndexed int      `json:"documentsIndexed"`
	Skipped          []string `json:"skipped,omitempty"`
	Errors           []string `json:"errors,omitempty"`

	// Async + WorkflowID are set when a long source (github/crawl/s3) was enqueued as
	// a durable tasks workflow instead of run inline. The caller tracks progress in
	// the Tasks product by this workflow id — there is no bespoke job entity.
	Async      bool   `json:"async,omitempty"`
	WorkflowID string `json:"workflowId,omitempty"`
}

// splitTypeForFile picks the splitter for a file name, honoring a store's
// configured default. Markdown and QA-docx get specialized splitters; SOURCE CODE
// gets the language-aware structural splitter (whole functions/types per chunk) so a
// github-indexed repo chunks along declarations, not blind line windows.
func splitTypeForFile(name, configured string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".md", ".mdx", ".markdown":
		return "Markdown"
	}
	if strings.HasPrefix(filepath.Base(name), "QA") && ext == ".docx" {
		return "QA"
	}
	if lang := codeLangForExt(ext); lang != "" {
		return "Code:" + lang
	}
	if configured != "" {
		return configured
	}
	return "Default"
}

// codeLangForExt maps a file extension to the code-splitter language key, or "" when
// the file is not source we structural-split (prose/data fall through to the
// configured/Default splitter). Aliases collapse to their canonical key.
func codeLangForExt(ext string) string {
	switch ext {
	case ".go":
		return "go"
	case ".py", ".pyi":
		return "py"
	case ".ts":
		return "ts"
	case ".tsx":
		return "tsx"
	case ".js", ".mjs", ".cjs":
		return "js"
	case ".jsx":
		return "jsx"
	case ".rs":
		return "rs"
	case ".java":
		return "java"
	case ".kt", ".kts":
		return "kt"
	case ".rb":
		return "rb"
	case ".php":
		return "php"
	case ".cpp", ".cc", ".cxx", ".hpp":
		return "cpp"
	case ".c", ".h":
		return "c"
	case ".cs":
		return "cs"
	case ".swift":
		return "swift"
	case ".scala":
		return "scala"
	case ".sol":
		return "sol"
	case ".sh", ".bash", ".zsh":
		return "sh"
	case ".proto":
		return "proto"
	}
	return ""
}

// chunkTextToDocs splits text and maps each chunk to a DocIndex with a
// deterministic ID per (owner, store, source, chunkIndex). Deterministic IDs
// make re-ingesting the same source idempotent (it overwrites its own chunks)
// rather than duplicating — so refresh is safe without a destructive index wipe.
func chunkTextToDocs(owner, store, source, url, title, text, tag, splitType, lang string) ([]DocIndex, error) {
	provider, err := split.GetSplitProvider(splitType)
	if err != nil {
		return nil, err
	}
	sections, err := provider.SplitText(text)
	if err != nil {
		return nil, err
	}
	pageID := hashID(owner + "/" + store + "/" + source)
	docs := make([]DocIndex, 0, len(sections))
	for i, section := range sections {
		section = strings.TrimSpace(section)
		if section == "" {
			continue
		}
		docs = append(docs, DocIndex{
			ID:          hashID(fmt.Sprintf("%s/%s/%s#%d", owner, store, source, i)),
			PageID:      pageID,
			Title:       title,
			URL:         url,
			Content:     section,
			Tag:         tag,
			Breadcrumbs: []string{title},
		})
	}
	return docs, nil
}

// ingestDocsForFile parses a stored file (by URL or local path), splits it, and
// indexes the chunks into the unified Vector+Search index. This is the path that
// REPLACES the deprecated SQL-vector write: an uploaded store file now lands in
// the exact index /v1/chat retrieval reads. Returns the chunk count indexed.
func ingestDocsForFile(owner, store, fileKey, fileURL, configuredSplit string, replace bool, lang string) (int, error) {
	ext := strings.ToLower(filepath.Ext(fileKey))
	text, err := txt.GetParsedTextFromUrl(fileURL, ext, lang)
	if err != nil {
		return 0, err
	}
	docs, err := chunkTextToDocs(owner, store, fileKey, fileURL, fileKey, text, store, splitTypeForFile(fileKey, configuredSplit), lang)
	if err != nil {
		return 0, err
	}
	return IndexDocuments(owner, store, &DocIndexRequest{Documents: docs, Replace: replace}, lang)
}

// IngestStoreStorage lists a store's backing object storage (Hanzo S3 / IAM
// provider) under an optional prefix and ingests each supported file into the
// unified Vector+Search index. Shared by RefreshStoreVectors (whole store) and
// the source:"s3" ingest path (prefix-scoped). Ingestion is additive and
// idempotent (deterministic chunk IDs); it does NOT wipe the index, so docs from
// other sources (crawl, framework) in the same store survive a refresh. Returns
// (filesIngested, documentsIndexed).
func IngestStoreStorage(store *Store, prefix, lang string) (int, int, error) {
	storageProviderObj, err := store.GetStorageProviderObj(lang)
	if err != nil {
		return 0, 0, err
	}
	files, err := storageProviderObj.ListObjects(prefix)
	if err != nil {
		return 0, 0, err
	}
	files = filterTextFiles(files)
	fileCount, docCount := 0, 0
	var fileErr error
	for _, f := range files {
		var n int
		_, statusErr := withFileStatus(store.Owner, store.Name, f.Key, func() (bool, int, error) {
			c, e := ingestDocsForFile(store.Owner, store.Name, f.Key, f.Url, store.SplitProvider, false, lang)
			n = c
			return c > 0, c, e
		})
		if statusErr != nil {
			logs.Error("ingest store file %s/%s failed: %v", store.Name, f.Key, statusErr)
			fileErr = errors.Join(fileErr, statusErr)
			continue
		}
		fileCount++
		docCount += n
	}
	return fileCount, docCount, fileErr
}

// IngestSource is the dispatcher behind POST /v1/docs/ingest. The owner is the
// authenticated principal (tenant isolation); never trust client-supplied owner.
func IngestSource(owner string, req *IngestRequest, lang string) (*IngestStats, error) {
	if owner == "" {
		return nil, fmt.Errorf("owner must not be empty")
	}
	store := req.Store
	if store == "" {
		store = DefaultDocsStore
	}
	source := req.Source
	if source == "" {
		source = "upload"
	}
	stats := &IngestStats{Source: source, Store: store, IndexName: GetSearchIndexName(owner, store)}
	switch source {
	case "upload":
		return ingestUploadSource(owner, store, req, stats, lang)
	case "github":
		if req.GitHub == nil {
			return nil, fmt.Errorf("source \"github\" requires a \"github\" object")
		}
		return ingestGitHubSource(owner, store, req, stats, lang)
	case "crawl":
		if req.Crawl == nil {
			return nil, fmt.Errorf("source \"crawl\" requires a \"crawl\" object")
		}
		return ingestCrawlSource(owner, store, req, stats, lang)
	case "s3":
		return ingestS3Source(owner, store, req, stats, lang)
	default:
		return nil, fmt.Errorf("unknown ingest source %q (want: upload|github|crawl|s3)", source)
	}
}

// ingestUploadSource handles inline documents (pre-chunked passthrough) and raw
// files (parsed + split server-side), indexing them in one fan-out.
func ingestUploadSource(owner, store string, req *IngestRequest, stats *IngestStats, lang string) (*IngestStats, error) {
	docs := make([]DocIndex, 0, len(req.Documents))
	docs = append(docs, req.Documents...)
	for _, f := range req.Files {
		if f.Name == "" {
			stats.FilesSkipped++
			stats.Skipped = append(stats.Skipped, "(unnamed file)")
			continue
		}
		var (
			text string
			err  error
		)
		switch {
		case f.Content != "":
			text = f.Content
		case f.URL != "":
			text, err = txt.GetParsedTextFromUrl(f.URL, strings.ToLower(filepath.Ext(f.Name)), lang)
		default:
			stats.FilesSkipped++
			stats.Skipped = append(stats.Skipped, f.Name+" (no content or url)")
			continue
		}
		if err != nil {
			stats.Errors = append(stats.Errors, fmt.Sprintf("%s: %v", f.Name, err))
			continue
		}
		tag := f.Tag
		if tag == "" {
			tag = req.Tag
		}
		fileDocs, err := chunkTextToDocs(owner, store, f.Name, f.URL, f.Name, text, tag, splitTypeForFile(f.Name, ""), lang)
		if err != nil {
			stats.Errors = append(stats.Errors, fmt.Sprintf("%s: %v", f.Name, err))
			continue
		}
		docs = append(docs, fileDocs...)
		stats.FilesIngested++
	}
	if len(docs) == 0 {
		return stats, nil
	}
	n, err := IndexDocuments(owner, store, &DocIndexRequest{Documents: docs, Replace: req.Replace}, lang)
	if err != nil {
		return stats, err
	}
	stats.DocumentsIndexed = n
	return stats, nil
}

// ingestCrawlSource delegates to the existing scrape+index pipeline, which
// already fans out to Vector+Search via IndexDocuments.
func ingestCrawlSource(owner, store string, req *IngestRequest, stats *IngestStats, lang string) (*IngestStats, error) {
	cr := *req.Crawl
	if cr.Store == "" {
		cr.Store = store
	}
	if cr.Tag == "" {
		cr.Tag = req.Tag
	}
	ss, err := ScrapeAndIndex(owner, &cr, lang)
	if err != nil {
		return stats, err
	}
	stats.FilesIngested = ss.PagesScraped
	stats.DocumentsIndexed = ss.DocumentsIndexed
	stats.Errors = append(stats.Errors, ss.Errors...)
	return stats, nil
}

// ingestS3Source ingests the store's backing Hanzo S3 space under an optional
// prefix, reusing the store's existing StorageProvider (S3 is an existing
// provider type). Private-space credentials resolve via the provider's
// KMS-backed secret — never plaintext.
func ingestS3Source(owner, store string, req *IngestRequest, stats *IngestStats, lang string) (*IngestStats, error) {
	st, err := getStore(owner, store)
	if err != nil {
		return stats, err
	}
	if st == nil {
		return stats, fmt.Errorf("store %q not found for owner %q", store, owner)
	}
	prefix := ""
	if req.S3 != nil {
		prefix = req.S3.Prefix
	}
	files, docsN, err := IngestStoreStorage(st, prefix, lang)
	stats.FilesIngested = files
	stats.DocumentsIndexed = docsN
	if err != nil {
		stats.Errors = append(stats.Errors, err.Error())
	}
	return stats, nil
}
