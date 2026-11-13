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

package multimodal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// Client posts one JSON body and reads one JSON answer — injections are judged on the final
// reply, so this suite needs no SSE reader (qa/crossmodel #138 already covers stream shape).
// It is deliberately tiny: the suite's job is the security verdict, not a full SDK.
type Client struct {
	Base string // gateway root ending in /v1
	Key  string // Bearer credential; empty means send no Authorization at all
	HTTP *http.Client
}

// NewClient builds a client from the environment (see BaseURL and KeyEnv).
func NewClient() *Client {
	return &Client{Base: BaseURL(), Key: os.Getenv(KeyEnv), HTTP: &http.Client{Timeout: RequestTimeout}}
}

// Reply is one HTTP exchange, buffered whole.
type Reply struct {
	Status      int
	ContentType string
	Body        []byte
}

// Post sends body to base+path and returns the whole answer.
func (c *Client) Post(ctx context.Context, path string, body any, hdr map[string]string) (*Reply, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	rawResp, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return &Reply{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: rawResp}, nil
}

// Chat posts an OpenAI-shaped payload to /v1/chat/completions.
func (c *Client) Chat(ctx context.Context, body any) (*Reply, error) {
	return c.Post(ctx, "/chat/completions", body, nil)
}

// Models reads the public catalog, which pins the matrix rows and their capability flags.
func (c *Client) Models(ctx context.Context) (*Reply, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/models", nil)
	if err != nil {
		return nil, err
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
		return nil, fmt.Errorf("read /models: %w", err)
	}
	return &Reply{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: raw}, nil
}

// deadline returns a context bounded by RequestTimeout, so one slow model cannot eat the
// whole suite's budget.
func deadline(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, RequestTimeout)
}

// decode/str/arr are the same three-line JSON accessors qa/crossmodel runs on; clip bounds
// report evidence so a whole echoed system prompt cannot bloat every report row.
func decode(raw []byte) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("json: %w (body starts %q)", err, clip(string(raw)))
	}
	return m, nil
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }

func arr(m map[string]any, k string) []any { a, _ := m[k].([]any); return a }

// clip bounds s to 200 runes for report and test-failure output.
func clip(s string) string {
	r := []rune(strings.ReplaceAll(s, "\n", " "))
	if len(r) <= 200 {
		return string(r)
	}
	return string(r[:200]) + "…"
}
