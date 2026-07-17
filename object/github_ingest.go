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

// GitHub-repo ingest: "we index your GitHub repos".
//
// Given {repo, ref?, paths?}, fetch the repo's docs/code over the GitHub REST
// API (tree + blob contents — NO local git clone in the pod), filter to
// text-like docs/code, parse+split each, and pipe to Hanzo Vector + Hanzo Search
// under {owner}-{store}-docs. Private repos auth with a token resolved from the
// request, then env GITHUB_TOKEN, then KMS secret GITHUB_TOKEN (never plaintext
// stored). Bounded by file count + size; everything skipped is reported, never
// silently dropped.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/hanzoai/ai/log"
)

// githubAPIBase is the GitHub REST API root (overridable in tests).
var githubAPIBase = "https://api.github.com"

const (
	githubDefaultMaxFiles    = 2000
	githubDefaultMaxFileSize = 1 << 20 // 1 MiB per file
	githubRequestTimeout     = 30 * time.Second
)

// GitHubIngestRequest selects a repo (and optional ref/paths) to index.
type GitHubIngestRequest struct {
	Repo        string   `json:"repo"`                  // "owner/name"
	Ref         string   `json:"ref,omitempty"`         // branch/tag/sha; default = repo default branch
	Paths       []string `json:"paths,omitempty"`       // include only blobs under these path prefixes
	Token       string   `json:"token,omitempty"`       // BYO token; else env/KMS GITHUB_TOKEN
	IncludeExts []string `json:"includeExts,omitempty"` // override the default text/code extension allowlist
	MaxFiles    int      `json:"maxFiles,omitempty"`    // cap (default githubDefaultMaxFiles)
	MaxFileSize int      `json:"maxFileSize,omitempty"` // per-file byte cap (default githubDefaultMaxFileSize)
}

// githubTextExts is the default allowlist of ingestable text/code extensions.
var githubTextExts = map[string]bool{
	".md": true, ".mdx": true, ".markdown": true, ".txt": true, ".rst": true,
	".yaml": true, ".yml": true, ".json": true, ".toml": true, ".proto": true,
	".go": true, ".py": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true,
	".rs": true, ".java": true, ".rb": true, ".c": true, ".h": true, ".cpp": true,
	".cc": true, ".hpp": true, ".cs": true, ".php": true, ".swift": true, ".kt": true,
	".scala": true, ".sh": true, ".sql": true,
}

// githubTreeResponse is the recursive git/trees payload.
type githubTreeResponse struct {
	SHA       string           `json:"sha"`
	Tree      []githubTreeNode `json:"tree"`
	Truncated bool             `json:"truncated"`
}

type githubTreeNode struct {
	Path string `json:"path"`
	Type string `json:"type"` // "blob" | "tree"
	SHA  string `json:"sha"`
	Size int    `json:"size"`
}

type githubBlobResponse struct {
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
	Size     int    `json:"size"`
}

type githubRepoResponse struct {
	DefaultBranch string `json:"default_branch"`
}

// resolveGitHubToken resolves the auth token for private repos: explicit request
// token first, then env GITHUB_TOKEN, then KMS secret GITHUB_TOKEN. Empty = use
// the unauthenticated public API.
func resolveGitHubToken(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if v := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); v != "" {
		return v
	}
	if v, err := GetKMSSecret("GITHUB_TOKEN"); err == nil && v != "" {
		return strings.TrimSpace(v)
	}
	return ""
}

// githubGet performs an authenticated GET against the GitHub REST API and
// decodes the JSON body into out.
func githubGet(urlStr, token string, out interface{}) error {
	req, err := http.NewRequest(http.MethodGet, urlStr, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "HanzoBot/1.0 (+https://hanzo.ai/bot)")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: githubRequestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github API %s returned %d: %s", urlStr, resp.StatusCode, truncateContent(string(body), 300))
	}
	return json.Unmarshal(body, out)
}

// githubDefaultBranch returns a repo's default branch (e.g. "main"/"master").
func githubDefaultBranch(owner, repo, token string) (string, error) {
	var r githubRepoResponse
	url := fmt.Sprintf("%s/repos/%s/%s", githubAPIBase, owner, repo)
	if err := githubGet(url, token, &r); err != nil {
		return "", err
	}
	if r.DefaultBranch == "" {
		return "", fmt.Errorf("github: repo %s/%s has no default branch", owner, repo)
	}
	return r.DefaultBranch, nil
}

// githubFetchBlob fetches and base64-decodes a blob's content by SHA.
func githubFetchBlob(owner, repo, sha, token string) (string, error) {
	var b githubBlobResponse
	url := fmt.Sprintf("%s/repos/%s/%s/git/blobs/%s", githubAPIBase, owner, repo, sha)
	if err := githubGet(url, token, &b); err != nil {
		return "", err
	}
	if b.Encoding != "base64" {
		return b.Content, nil
	}
	clean := strings.NewReplacer("\n", "", "\r", "").Replace(b.Content)
	decoded, err := base64.StdEncoding.DecodeString(clean)
	if err != nil {
		return "", fmt.Errorf("github: decode blob %s: %w", sha, err)
	}
	return string(decoded), nil
}

// IngestGitHub walks a repo's tree and ingests each matching file into the
// unified Vector+Search index. owner/store scope the tenant index; the repo is
// fetched read-only over HTTP.
func IngestGitHub(owner, store string, gh *GitHubIngestRequest, replace bool, tag, lang string) (*IngestStats, error) {
	stats := &IngestStats{Source: "github", Store: store, IndexName: GetSearchIndexName(owner, store)}
	repoOwner, repoName, err := splitRepo(gh.Repo)
	if err != nil {
		return stats, err
	}
	token := resolveGitHubToken(gh.Token)
	ref := strings.TrimSpace(gh.Ref)
	if ref == "" {
		ref, err = githubDefaultBranch(repoOwner, repoName, token)
		if err != nil {
			return stats, err
		}
	}
	maxFiles := gh.MaxFiles
	if maxFiles <= 0 {
		maxFiles = githubDefaultMaxFiles
	}
	maxFileSize := gh.MaxFileSize
	if maxFileSize <= 0 {
		maxFileSize = githubDefaultMaxFileSize
	}
	exts := githubTextExts
	if len(gh.IncludeExts) > 0 {
		exts = map[string]bool{}
		for _, e := range gh.IncludeExts {
			if !strings.HasPrefix(e, ".") {
				e = "." + e
			}
			exts[strings.ToLower(e)] = true
		}
	}

	var tree githubTreeResponse
	treeURL := fmt.Sprintf("%s/repos/%s/%s/git/trees/%s?recursive=1", githubAPIBase, repoOwner, repoName, ref)
	if err := githubGet(treeURL, token, &tree); err != nil {
		return stats, fmt.Errorf("github: fetch tree %s/%s@%s: %w", repoOwner, repoName, ref, err)
	}
	if tree.Truncated {
		// The recursive tree is capped by GitHub; report it rather than silently
		// indexing a partial repo.
		stats.Skipped = append(stats.Skipped, "(repo tree truncated by GitHub; some files not listed)")
		log.Warning("github: tree for %s/%s@%s is truncated; indexing the listed subset", repoOwner, repoName, ref)
	}

	if tag == "" {
		tag = gh.Repo
	}
	allDocs := make([]DocIndex, 0, 256)
	for _, node := range tree.Tree {
		if node.Type != "blob" {
			continue
		}
		ext := strings.ToLower(path.Ext(node.Path))
		if !exts[ext] {
			continue // not an ingestable type — quietly out of scope (not a failure)
		}
		if len(gh.Paths) > 0 && !pathHasPrefix(node.Path, gh.Paths) {
			continue
		}
		if node.Size > maxFileSize {
			stats.FilesSkipped++
			stats.Skipped = append(stats.Skipped, fmt.Sprintf("%s (%d bytes > %d max)", node.Path, node.Size, maxFileSize))
			continue
		}
		if stats.FilesIngested >= maxFiles {
			stats.Skipped = append(stats.Skipped, fmt.Sprintf("(file cap %d reached; remaining files not indexed)", maxFiles))
			log.Warning("github: file cap %d reached for %s/%s@%s", maxFiles, repoOwner, repoName, ref)
			break
		}
		content, err := githubFetchBlob(repoOwner, repoName, node.SHA, token)
		if err != nil {
			stats.Errors = append(stats.Errors, fmt.Sprintf("%s: %v", node.Path, err))
			continue
		}
		source := fmt.Sprintf("github:%s/%s@%s:%s", repoOwner, repoName, ref, node.Path)
		htmlURL := fmt.Sprintf("https://github.com/%s/%s/blob/%s/%s", repoOwner, repoName, ref, node.Path)
		docs, err := chunkTextToDocs(owner, store, source, htmlURL, node.Path, content, tag, splitTypeForFile(node.Path, ""), lang)
		if err != nil {
			stats.Errors = append(stats.Errors, fmt.Sprintf("%s: %v", node.Path, err))
			continue
		}
		allDocs = append(allDocs, docs...)
		stats.FilesIngested++
	}

	if len(allDocs) == 0 {
		return stats, nil
	}
	n, err := IndexDocuments(owner, store, &DocIndexRequest{Documents: allDocs, Replace: replace}, lang)
	if err != nil {
		return stats, err
	}
	stats.DocumentsIndexed = n
	return stats, nil
}

// ingestGitHubSource adapts the dispatcher to IngestGitHub.
func ingestGitHubSource(owner, store string, req *IngestRequest, stats *IngestStats, lang string) (*IngestStats, error) {
	return IngestGitHub(owner, store, req.GitHub, req.Replace, req.Tag, lang)
}

// splitRepo parses "owner/name" into its parts.
func splitRepo(repo string) (string, string, error) {
	repo = strings.TrimSpace(strings.TrimSuffix(repo, ".git"))
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("github: repo must be \"owner/name\", got %q", repo)
	}
	return parts[0], parts[1], nil
}

// pathHasPrefix reports whether p is under any of the given path prefixes.
func pathHasPrefix(p string, prefixes []string) bool {
	for _, pre := range prefixes {
		pre = strings.Trim(pre, "/")
		if pre == "" || p == pre || strings.HasPrefix(p, pre+"/") {
			return true
		}
	}
	return false
}
