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
	"math/rand"
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
	MaxCost      float64 `json:"max_cost"`       // cost cap, USD PER 1K TOKENS (per-1k); 0 disables
	MaxLatencyMs int     `json:"max_latency_ms"` // latency cap, milliseconds; 0 disables
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
	// Explore is the epsilon exploration floor in [0,1]: the probability the
	// heuristic samples a RANDOM servable NON-champion arm instead of the champion,
	// so the bandit keeps collecting reward on the whole pool (and reacts to newly
	// added models) rather than collapsing onto today's champion — the reward from
	// an explore request trains it exactly like an exploit request. 0 = pure
	// exploit. The engine strategy does its own UCB exploration, so this governs the
	// heuristic path.
	Explore float64
	// Rand, if set, sources the exploration coin flip (injectable for deterministic
	// tests); nil uses the auto-seeded package generator.
	Rand *rand.Rand
}

// engineRequest is the /route wire contract sent to the engine.
type engineRequest struct {
	Prompt string   `json:"prompt"`
	Tasks  []string `json:"tasks,omitempty"`
	Slo    Slo      `json:"slo"`
}

// engineResponse is the /route wire contract returned by the engine. Features is
// the OPTIONAL frozen-backbone embedding the engine may return alongside its
// choice; it is tolerated when absent and passed through to the routing ledger
// for training (never prompt text).
type engineResponse struct {
	Model      string    `json:"model"`
	Task       string    `json:"task"`
	Confidence float64   `json:"confidence"`
	Features   []float64 `json:"features,omitempty"`
}

// Source labels which strategy produced a Decision.
const (
	SourceEngine    = "engine"
	SourceHeuristic = "heuristic"
	SourceExplore   = "explore" // an epsilon-floor sample of a non-champion arm
)

// Decision is the full outcome of resolving a request: the chosen model, the
// task it was classified as, the engine confidence (0 for the heuristic), the
// Source that produced it, and the engine's optional feature vector. It carries
// exactly what the routing ledger records — no prompt text.
type Decision struct {
	Model      string
	Task       Task
	Confidence float64
	Source     string
	Features   []float64
}

// Route resolves req to a concrete model id and the task it was classified as.
// It never errors: on any engine failure it returns the heuristic decision. It
// is a thin projection of RouteDecision for callers that only need the id.
func (c Client) Route(ctx context.Context, req Request, slo Slo) (model string, task Task) {
	d := c.RouteDecision(ctx, req, slo)
	return d.Model, d.Task
}

// RouteDecision resolves req to a full Decision (model, task, confidence, source,
// features). It never errors: on any engine failure it returns the heuristic
// decision (Source == SourceHeuristic, no confidence or features).
func (c Client) RouteDecision(ctx context.Context, req Request, slo Slo) Decision {
	if c.Endpoint != "" {
		if d, ok := c.routeEngine(ctx, req, slo); ok {
			return d
		}
	}
	t := Classify(req)
	// Exploration floor: with probability Explore, sample a non-champion servable
	// arm so the router keeps learning about the whole pool instead of collapsing
	// onto the current champion. The reward it earns trains the bandit identically.
	if c.Explore > 0 {
		if m, ok := c.exploreArm(t); ok {
			return Decision{Model: m, Task: t, Source: SourceExplore}
		}
	}
	m := c.Policy.ForTask(t, c.Known)
	if m == "" {
		// Last resort: ignore servability so `auto` never dead-ends on a strict
		// predicate — a misrouted-but-listed model is better than an empty id.
		m = c.Policy.ForTask(t, nil)
	}
	return Decision{Model: m, Task: t, Source: SourceHeuristic}
}

// exploreArm returns a uniformly-random NON-champion servable arm for the task with
// probability Explore, or (,false) to exploit. It needs ≥2 servable arms — with one
// (or none) there is nothing to explore toward, so it always exploits.
func (c Client) exploreArm(t Task) (string, bool) {
	f, ni := rand.Float64, rand.Intn
	if c.Rand != nil {
		f, ni = c.Rand.Float64, c.Rand.Intn
	}
	if f() >= c.Explore {
		return "", false
	}
	cands := c.Policy.Candidates(t, c.Known)
	if len(cands) < 2 {
		return "", false
	}
	return cands[1+ni(len(cands)-1)], true // index 0 is the champion; sample [1:]
}

// ObserveRequest is the online-learning feedback the gateway posts to the engine
// after a reward is joined: the request's feature vector `x` (the same vector the
// engine returned at decision time), the arm that served, the caller (keys the
// per-user LinUCB bandit), the caller org (selects the scope), and the outcome reward
// in [0,1]. No prompt text.
type ObserveRequest struct {
	User     string    `json:"user"`
	Org      string    `json:"org"`
	Features []float64 `json:"features"`
	Model    string    `json:"model"`
	Reward   float64   `json:"reward"`
}

// Observe forwards a joined reward to the engine's /route/observe endpoint so the
// learned policy updates per-user theta in-process (the fast online loop). It is
// best-effort and fire-and-forget by contract: any error (no endpoint, unreachable,
// non-2xx) is ignored — a missed online update never fails the reward write, and the
// offline corpus (the rewarded ledger) still captures the signal. Hard-capped by
// Timeout so it never hangs the caller's goroutine.
func (c Client) Observe(ctx context.Context, obs ObserveRequest) {
	if c.Endpoint == "" || len(obs.Features) == 0 || obs.Model == "" {
		return
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body, err := json.Marshal(obs)
	if err != nil {
		return
	}
	url := strings.TrimRight(c.Endpoint, "/") + "/route/observe"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	cli := c.HTTP
	if cli == nil {
		cli = http.DefaultClient
	}
	resp, err := cli.Do(req)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}

// routeEngine performs the constrained /route call. Returns ok=false on any
// error so the caller falls back to the heuristic.
func (c Client) routeEngine(ctx context.Context, req Request, slo Slo) (Decision, bool) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body, err := json.Marshal(engineRequest{Prompt: req.Text, Slo: slo})
	if err != nil {
		return Decision{}, false
	}
	url := strings.TrimRight(c.Endpoint, "/") + "/route"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Decision{}, false
	}
	httpReq.Header.Set("Content-Type", "application/json")

	cli := c.HTTP
	if cli == nil {
		cli = http.DefaultClient
	}
	resp, err := cli.Do(httpReq)
	if err != nil {
		return Decision{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Decision{}, false
	}
	var out engineResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil {
		return Decision{}, false
	}

	// Prefer an explicit, servable model; else map the returned task via the
	// policy table (an engine that emits only a task label).
	if out.Model != "" && (c.Known == nil || c.Known(out.Model)) {
		return Decision{Model: out.Model, Task: Task(out.Task), Confidence: out.Confidence, Source: SourceEngine, Features: out.Features}, true
	}
	if out.Task != "" {
		if m := c.Policy.ForTask(Task(out.Task), c.Known); m != "" {
			return Decision{Model: m, Task: Task(out.Task), Confidence: out.Confidence, Source: SourceEngine, Features: out.Features}, true
		}
	}
	return Decision{}, false
}
