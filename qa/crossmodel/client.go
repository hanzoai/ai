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

package crossmodel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// Client speaks both wire formats of the gateway: it posts an OpenAI-shaped body to
// /v1/chat/completions and an Anthropic-shaped one to /v1/messages, and reads either
// a JSON answer or an SSE stream. It is deliberately tiny — the suite's job is the
// response contract, not a full SDK.
type Client struct {
	Base string // gateway root ending in /v1
	Key  string // Bearer credential; empty means send no Authorization at all
	HTTP *http.Client
}

// NewClient builds a client from the environment (see BaseURL and KeyEnv).
func NewClient() *Client {
	return &Client{Base: BaseURL(), Key: os.Getenv(KeyEnv), HTTP: &http.Client{Timeout: RequestTimeout}}
}

// Reply is one HTTP exchange. Body carries the whole payload for a JSON answer;
// Frames carries the SSE payloads in wire order for a stream, including the terminal
// `data: [DONE]` sentinel so its presence can be asserted.
type Reply struct {
	Status      int
	ContentType string
	Body        []byte
	Frames      []Frame
}

// Frame is one SSE data payload, paired with the event name that preceded it when
// the producer sent one (Anthropic does, OpenAI does not).
type Frame struct {
	Event string
	Data  string
}

// Post sends body to base+path and returns the whole JSON answer.
func (c *Client) Post(ctx context.Context, path string, body any, hdr map[string]string) (*Reply, error) {
	req, err := c.newRequest(ctx, path, body, hdr, "application/json")
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return &Reply{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: raw}, nil
}

// PostStream sends a streaming request and returns every SSE frame in order. A
// response that is not an event stream (an error envelope, say) comes back with its
// Body set and no frames, which is itself a finding the caller reports.
func (c *Client) PostStream(ctx context.Context, path string, body any, hdr map[string]string) (*Reply, error) {
	h := map[string]string{"Accept": "text/event-stream"}
	for k, v := range hdr {
		h[k] = v
	}
	req, err := c.newRequest(ctx, path, body, h, "application/json")
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	reply := &Reply{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type")}
	if !strings.Contains(strings.ToLower(reply.ContentType), "text/event-stream") {
		reply.Body, err = io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		return reply, nil
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20) // a tool-call or image chunk can outrun the default 64 KiB line
	var event string
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		switch {
		case line == "" || strings.HasPrefix(line, ":"): // event boundary / keep-alive comment
			if line == "" {
				event = ""
			}
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			reply.Frames = append(reply.Frames, Frame{Event: event, Data: strings.TrimSpace(strings.TrimPrefix(line, "data:"))})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("stream %s: %w", path, err)
	}
	return reply, nil
}

func (c *Client) newRequest(ctx context.Context, path string, body any, hdr map[string]string, contentType string) (*http.Request, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	if c.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return req, nil
}

// Chat posts an OpenAI-shaped payload to /v1/chat/completions.
func (c *Client) Chat(ctx context.Context, body any) (*Reply, error) {
	return c.Post(ctx, "/chat/completions", body, nil)
}

// ChatStream posts a streaming OpenAI payload with "stream": true already set by the caller.
func (c *Client) ChatStream(ctx context.Context, body any) (*Reply, error) {
	return c.PostStream(ctx, "/chat/completions", body, nil)
}

// Messages posts an Anthropic-shaped payload to /v1/messages.
func (c *Client) Messages(ctx context.Context, body any) (*Reply, error) {
	return c.Post(ctx, "/messages", body, map[string]string{"anthropic-version": AnthropicVersion})
}

// MessagesStream posts a streaming Anthropic payload with "stream": true set by the caller.
func (c *Client) MessagesStream(ctx context.Context, body any) (*Reply, error) {
	return c.PostStream(ctx, "/messages", body, map[string]string{"anthropic-version": AnthropicVersion})
}

// Models reads the public catalog, which is the source of the capability flags the
// matrix skips on.
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

// deadline returns a context bounded by RequestTimeout, so one slow family cannot
// eat the whole suite's budget.
func deadline(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, RequestTimeout)
}
