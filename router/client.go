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

package router

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultTimeout is the hard cap on the engine round-trip. Routing must add
// negligible latency to a completion, so a slow/unreachable engine falls through
// to the local heuristic well within budget.
const DefaultTimeout = 150 * time.Millisecond

// Slo is the per-request service-level objective forwarded to the engine. `0`
// fields disable the corresponding ceiling. It mirrors hanzo-router's Slo (the
// operator's budget for this request), distinct from a user's learned taste.
type Slo struct {
	MaxCost      float64 `json:"max_cost"`
	MaxLatencyMs int     `json:"max_latency_ms"`
}

// Client resolves a Request to a concrete model id. Strategy order:
//  1. engine — POST {Endpoint}/route and use its choice (a learned zen-router
//     served OpenAI-compatibly by hanzo-engine); hard-capped by Timeout.
//  2. heuristic — Classify + Policy, a pure-Go port of the Rust rule router.
//
// Any engine error (unreachable, timeout, non-200, bad body, unservable choice)
// falls through to the heuristic, so `auto` always resolves with zero extra
// infrastructure.
type Client struct {
	// Endpoint is the zen-router base URL (e.g. http://engine.hanzo.svc:3000).
	// Empty disables the engine strategy — heuristic only.
	Endpoint string
	// Timeout hard-caps the engine round-trip (default DefaultTimeout).
	Timeout time.Duration
	// Policy is the heuristic task→model table (and the map for an engine reply
	// that returns only a task).
	Policy Policy
	// HTTP is the client used for the engine call (default http.DefaultClient).
	HTTP *http.Client
	// Known, if set, restricts choices to models the caller can serve; a choice
	// it rejects is skipped. nil accepts all.
	Known func(string) bool
}

// engineRequest is the /route wire contract sent to the engine.
type engineRequest struct {
	Prompt string   `json:"prompt"`
	Tasks  []string `json:"tasks,omitempty"`
	Slo    Slo      `json:"slo"`
}

// engineResponse is the /route wire contract returned by the engine.
type engineResponse struct {
	Model      string  `json:"model"`
	Task       string  `json:"task"`
	Confidence float64 `json:"confidence"`
}

// Route resolves req to a concrete model id and the task it was classified as.
// It never errors: on any engine failure it returns the heuristic decision.
func (c Client) Route(ctx context.Context, req Request, slo Slo) (model string, task Task) {
	if c.Endpoint != "" {
		if m, t, ok := c.routeEngine(ctx, req, slo); ok {
			return m, t
		}
	}
	t := Classify(req)
	if m := c.Policy.ForTask(t, c.Known); m != "" {
		return m, t
	}
	// Last resort: ignore servability so `auto` never dead-ends on a strict
	// predicate — a misrouted-but-listed model is better than an empty id.
	return c.Policy.ForTask(t, nil), t
}

// routeEngine performs the constrained /route call. Returns ok=false on any
// error so the caller falls back to the heuristic.
func (c Client) routeEngine(ctx context.Context, req Request, slo Slo) (string, Task, bool) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body, err := json.Marshal(engineRequest{Prompt: req.Text, Slo: slo})
	if err != nil {
		return "", "", false
	}
	url := strings.TrimRight(c.Endpoint, "/") + "/route"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", "", false
	}
	httpReq.Header.Set("Content-Type", "application/json")

	cli := c.HTTP
	if cli == nil {
		cli = http.DefaultClient
	}
	resp, err := cli.Do(httpReq)
	if err != nil {
		return "", "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", false
	}
	var out engineResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil {
		return "", "", false
	}

	// Prefer an explicit, servable model; else map the returned task via the
	// policy table (an engine that emits only a task label).
	if out.Model != "" && (c.Known == nil || c.Known(out.Model)) {
		return out.Model, Task(out.Task), true
	}
	if out.Task != "" {
		if m := c.Policy.ForTask(Task(out.Task), c.Known); m != "" {
			return m, Task(out.Task), true
		}
	}
	return "", "", false
}
