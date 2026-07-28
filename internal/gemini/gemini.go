// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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

// Package gemini is the ONE client for Google's Gemini API, covering exactly
// the four calls this module makes: countTokens, generateContent,
// batchEmbedContents and models.list.
//
// It exists instead of google.golang.org/genai because that SDK reaches
// cloud.google.com/go/auth for credentials, which drags s2a-go, gRPC and
// protobuf — 100+ packages — into every binary that links it. Gemini is an
// API-key REST service; none of that machinery buys anything here.
//
// Contract (base = https://generativelanguage.googleapis.com/v1beta, auth =
// "x-goog-api-key: {apiKey}"):
//
//	count    POST {base}/models/{model}:countTokens
//	         {"contents":[{"role":"user","parts":[{"text":"…"}]}]}
//	         → {"totalTokens":12}
//	generate POST {base}/models/{model}:generateContent
//	         {"contents":[…]}
//	         → {"candidates":[{"content":{"parts":[{"text":"…"}]},"tokenCount":34}]}
//	embed    POST {base}/models/{model}:batchEmbedContents
//	         {"requests":[{"model":"models/…","content":{"parts":[{"text":"…"}]}}]}
//	         → {"embeddings":[{"values":[0.1,…]}]}
//	list     GET  {base}/models?pageSize=50&pageToken=…
//	         → {"models":[{"name":"models/…"}],"nextPageToken":"…"}
package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// baseURL is the v1beta surface — the one that serves countTokens and
// batchEmbedContents alongside generateContent.
const baseURL = "https://generativelanguage.googleapis.com/v1beta/"

// RoleUser authors a caller turn; replies come back with role "model".
const RoleUser = "user"

type Part struct {
	Text string `json:"text,omitempty"`
}

type Content struct {
	Role  string  `json:"role,omitempty"`
	Parts []*Part `json:"parts,omitempty"`
}

// Text builds the single-part content that every call site here needs.
func Text(text, role string) *Content {
	return &Content{Role: role, Parts: []*Part{{Text: text}}}
}

type Candidate struct {
	Content    *Content `json:"content"`
	TokenCount int      `json:"tokenCount"`
}

type GenerateResponse struct {
	Candidates []*Candidate `json:"candidates"`
}

type Model struct {
	Name string `json:"name"`
}

type ModelPage struct {
	Models        []*Model `json:"models"`
	NextPageToken string   `json:"nextPageToken"`
}

// Client is an API-key Gemini caller. It is stateless beyond the key and the
// HTTP client, so it is safe to build per request and share across goroutines.
type Client struct {
	apiKey string
	hc     *http.Client
}

// New binds an API key to an HTTP client; hc carries the process proxy
// configuration (proxy.ProxyHttpClient) and may be nil.
func New(apiKey string, hc *http.Client) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{apiKey: apiKey, hc: hc}
}

// resource qualifies a bare model name the way the API requires; names that
// already carry their collection pass through untouched.
func resource(model string) string {
	if strings.HasPrefix(model, "models/") || strings.HasPrefix(model, "tunedModels/") {
		return model
	}
	return "models/" + model
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, payload)
	if err != nil {
		return err
	}
	req.Header.Set("x-goog-api-key", c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Gemini distinguishes bad key, quota and unknown model only in the
		// body; the status code alone is not actionable for the operator.
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return fmt.Errorf("gemini: %s %s: %s", method, path, strings.TrimSpace(string(detail)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// CountTokens reports the prompt token count the API will bill for contents.
func (c *Client) CountTokens(ctx context.Context, model string, contents []*Content) (int, error) {
	var out struct {
		TotalTokens int `json:"totalTokens"`
	}
	err := c.do(ctx, http.MethodPost, resource(model)+":countTokens",
		map[string]any{"contents": contents}, &out)
	return out.TotalTokens, err
}

// GenerateContent runs one non-streaming completion. A response with no usable
// candidate — what a safety block returns — is an error, never a nil deref.
func (c *Client) GenerateContent(ctx context.Context, model string, contents []*Content) (*GenerateResponse, error) {
	out := &GenerateResponse{}
	if err := c.do(ctx, http.MethodPost, resource(model)+":generateContent",
		map[string]any{"contents": contents}, out); err != nil {
		return nil, err
	}
	if len(out.Candidates) == 0 || out.Candidates[0].Content == nil {
		return nil, fmt.Errorf("gemini: %s returned no candidate", model)
	}
	return out, nil
}

// Embed returns one vector per content, in order. batchEmbedContents is the
// only embed endpoint that accepts more than one content, so it serves the
// single-content case too rather than needing a second code path.
func (c *Client) Embed(ctx context.Context, model string, contents []*Content) ([][]float32, error) {
	requests := make([]map[string]any, len(contents))
	for i, content := range contents {
		requests[i] = map[string]any{"model": resource(model), "content": content}
	}
	var out struct {
		Embeddings []struct {
			Values []float32 `json:"values"`
		} `json:"embeddings"`
	}
	if err := c.do(ctx, http.MethodPost, resource(model)+":batchEmbedContents",
		map[string]any{"requests": requests}, &out); err != nil {
		return nil, err
	}
	if len(out.Embeddings) != len(contents) {
		return nil, fmt.Errorf("gemini: %s returned %d embeddings for %d contents", model, len(out.Embeddings), len(contents))
	}
	vectors := make([][]float32, len(out.Embeddings))
	for i, embedding := range out.Embeddings {
		vectors[i] = embedding.Values
	}
	return vectors, nil
}

// ListModels walks the model catalogue one page at a time; an empty
// NextPageToken ends the walk.
func (c *Client) ListModels(ctx context.Context, pageSize int, pageToken string) (*ModelPage, error) {
	query := url.Values{"pageSize": {strconv.Itoa(pageSize)}}
	if pageToken != "" {
		query.Set("pageToken", pageToken)
	}
	out := &ModelPage{}
	return out, c.do(ctx, http.MethodGet, "models?"+query.Encode(), nil, out)
}
