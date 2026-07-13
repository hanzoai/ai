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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// finetune_hf.go is the server-side HuggingFace Hub bridge: it lets the console's
// base-model and dataset pickers browse/search the Hub and read a repo's file
// list, and resolves a HuggingFace access token from KMS (never the browser) so
// PRIVATE and gated repos work. The token is used only here, server-side, as a
// Bearer credential — it is never returned to a client.
//
// The actual multi-GB weight/dataset bytes are NOT pulled through this process:
// the cluster training operator's dataset-/model-initializers pull `hf://…`
// directly into the job pod (authenticated by the in-namespace HF-token Secret
// that finetune_k8s.go provisions). This bridge is the lightweight discovery +
// auth plane; finetune_k8s.go is the data plane.

// hfBaseURL is the HuggingFace Hub endpoint (override with HF_HUB_URL / HF_ENDPOINT
// for an enterprise Hub or a mirror).
func hfBaseURL() string {
	for _, k := range []string{"HF_HUB_URL", "HF_ENDPOINT"} {
		if v := strings.TrimRight(os.Getenv(k), "/"); v != "" {
			return v
		}
	}
	return "https://huggingface.co"
}

func hfHTTP() *http.Client { return &http.Client{Timeout: 20 * time.Second} }

// HfModel is a HuggingFace model as the Hub `/api/models` list returns it (only
// the fields the picker shows). `Gated` is `false` or a string ("auto"/"manual"),
// so it is typed `any` and interpreted by HfGated.
type HfModel struct {
	Id           string   `json:"id"`
	Downloads    int64    `json:"downloads"`
	Likes        int64    `json:"likes"`
	Tags         []string `json:"tags,omitempty"`
	PipelineTag  string   `json:"pipeline_tag,omitempty"`
	LibraryName  string   `json:"library_name,omitempty"`
	Private      bool     `json:"private"`
	Gated        any      `json:"gated,omitempty"`
	LastModified string   `json:"lastModified,omitempty"`
}

// HfDataset is a HuggingFace dataset as `/api/datasets` returns it.
type HfDataset struct {
	Id           string   `json:"id"`
	Downloads    int64    `json:"downloads"`
	Likes        int64    `json:"likes"`
	Tags         []string `json:"tags,omitempty"`
	Private      bool     `json:"private"`
	Gated        any      `json:"gated,omitempty"`
	LastModified string   `json:"lastModified,omitempty"`
}

// HfSibling is one file inside a repo.
type HfSibling struct {
	Rfilename string `json:"rfilename"`
	Size      int64  `json:"size,omitempty"`
}

// HfRepoInfo is the detail view for one model/dataset repo.
type HfRepoInfo struct {
	Id           string      `json:"id"`
	Tags         []string    `json:"tags,omitempty"`
	PipelineTag  string      `json:"pipeline_tag,omitempty"`
	Downloads    int64       `json:"downloads"`
	Likes        int64       `json:"likes"`
	Private      bool        `json:"private"`
	Gated        any         `json:"gated,omitempty"`
	Siblings     []HfSibling `json:"siblings,omitempty"`
	LastModified string      `json:"lastModified,omitempty"`
}

// HfGated normalizes the Hub's mixed `gated` value (bool|string) to a boolean —
// true means the repo needs an accepted license / token to pull.
func HfGated(v any) bool {
	switch g := v.(type) {
	case bool:
		return g
	case string:
		return g != "" && g != "false"
	}
	return false
}

// ResolveHfToken returns a HuggingFace access token for server-side Hub calls and
// for the in-cluster pull Secret, resolved env-var-first (mirroring object/kms.go
// ResolveProviderSecret): HUGGINGFACE_TOKEN / HF_TOKEN env, then the org's KMS
// project (when orgProjectID is set), then the system KMS project. Returns "" when
// no token is configured — public repos still work; private/gated do not.
func ResolveHfToken(orgProjectID string) string {
	for _, k := range []string{"HUGGINGFACE_TOKEN", "HF_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	if orgProjectID != "" {
		if v, err := GetOrgKMSSecret("HUGGINGFACE_TOKEN", orgProjectID); err == nil && v != "" {
			return v
		}
		if v, err := GetOrgKMSSecret("HF_TOKEN", orgProjectID); err == nil && v != "" {
			return v
		}
	}
	if v, err := GetKMSSecret("HUGGINGFACE_TOKEN"); err == nil && v != "" {
		return v
	}
	if v, err := GetKMSSecret("HF_TOKEN"); err == nil && v != "" {
		return v
	}
	return ""
}

func hfGet(path string, token string, out any) error {
	req, err := http.NewRequest(http.MethodGet, hfBaseURL()+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := hfHTTP().Do(req)
	if err != nil {
		return fmt.Errorf("huggingface request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read huggingface response: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("huggingface denied access (HTTP %d) — a private/gated repo needs an HF token in KMS", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("huggingface returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("failed to parse huggingface response: %w", err)
	}
	return nil
}

func hfLimit(limit int) int {
	if limit <= 0 {
		return 30
	}
	if limit > 100 {
		return 100
	}
	return limit
}

// SearchHfModels searches the Hub for models, newest+most-downloaded first. `task`
// optionally filters by pipeline tag (e.g. "text-generation"). A token (when
// configured) widens results to the org's private models and raises rate limits.
func SearchHfModels(query, task string, limit int, token string) ([]*HfModel, error) {
	q := url.Values{}
	if query != "" {
		q.Set("search", query)
	}
	if task != "" {
		q.Set("filter", task)
	}
	q.Set("limit", fmt.Sprintf("%d", hfLimit(limit)))
	q.Set("sort", "downloads")
	q.Set("direction", "-1")
	models := []*HfModel{}
	if err := hfGet("/api/models?"+q.Encode(), token, &models); err != nil {
		return nil, err
	}
	return models, nil
}

// SearchHfDatasets searches the Hub for datasets.
func SearchHfDatasets(query string, limit int, token string) ([]*HfDataset, error) {
	q := url.Values{}
	if query != "" {
		q.Set("search", query)
	}
	q.Set("limit", fmt.Sprintf("%d", hfLimit(limit)))
	q.Set("sort", "downloads")
	q.Set("direction", "-1")
	datasets := []*HfDataset{}
	if err := hfGet("/api/datasets?"+q.Encode(), token, &datasets); err != nil {
		return nil, err
	}
	return datasets, nil
}

// GetHfRepoInfo reads one repo's detail (tags, files, gated/private state). kind
// is "model" or "dataset".
func GetHfRepoInfo(repoId, kind, token string) (*HfRepoInfo, error) {
	repoId = strings.Trim(strings.TrimSpace(repoId), "/")
	if repoId == "" {
		return nil, fmt.Errorf("empty repo id")
	}
	seg := "models"
	if kind == "dataset" {
		seg = "datasets"
	}
	info := &HfRepoInfo{}
	if err := hfGet("/api/"+seg+"/"+repoId, token, info); err != nil {
		return nil, err
	}
	if info.Id == "" {
		info.Id = repoId
	}
	return info, nil
}
