// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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

package imagegen

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
)

// Client speaks just enough of the OpenAI images contract to drive the live
// lanes: one JSON body in, one reply out, plus a plain GET for fetching the
// hosted artifact. It is deliberately tiny — the suite's job is the image
// verdicts, not being an SDK.
type Client struct {
	Base string // gateway root ending in /v1
	Key  string // Bearer credential; empty means send no Authorization at all
	HTTP *http.Client
}

// NewClient builds a client from the environment (BaseURL and KeyEnv).
func NewClient() *Client {
	return &Client{Base: BaseURL(), Key: os.Getenv(KeyEnv), HTTP: &http.Client{Timeout: RequestTimeout}}
}

// Reply is one HTTP exchange, buffered whole.
type Reply struct {
	Status      int
	ContentType string
	Body        []byte
}

func (c *Client) do(ctx context.Context, method, url string, body []byte) (*Reply, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return &Reply{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: raw}, nil
}

// Generate POSTs one /v1/images/generations request (OpenAI shape: model,
// prompt, n, size). ResponseFormat is passed through as given so the silent-
// ignore lane can ask for b64_json and observe what actually comes back.
func (c *Client) Generate(ctx context.Context, model, prompt string, n int, size, responseFormat string) (*Reply, error) {
	reqBody := map[string]any{"model": model, "prompt": prompt}
	if n > 0 {
		reqBody["n"] = n
	}
	if size != "" {
		reqBody["size"] = size
	}
	if responseFormat != "" {
		reqBody["response_format"] = responseFormat
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	return c.do(ctx, http.MethodPost, c.Base+GenerationsPath, payload)
}

// PostRaw sends an arbitrary JSON body at a path — used by the edits-door lane
// to probe /images/edits, which the relay does not route.
func (c *Client) PostRaw(ctx context.Context, path string, body map[string]any) (*Reply, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return c.do(ctx, http.MethodPost, c.Base+path, payload)
}

// Fetch downloads a hosted artifact (a fal.media URL) so its bytes, not just
// its label, can be validated.
func (c *Client) Fetch(ctx context.Context, url string) (*Reply, error) {
	return c.do(ctx, http.MethodGet, url, nil)
}
