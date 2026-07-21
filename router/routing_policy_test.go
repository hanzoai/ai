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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// rpEnabled builds an Enabled/Known predicate from an allow-list.
func rpEnabled(ids ...string) func(string) bool {
	allow := make(map[string]bool, len(ids))
	for _, id := range ids {
		allow[id] = true
	}
	return func(m string) bool { return allow[m] }
}

// rpEngineStub is a fake Enso engine that records /route hits and replies with a
// fixed decision, so a test can assert whether a strategy actually delegated to it.
func rpEngineStub(t *testing.T, model, task string) (endpoint string, hits *int32) {
	t.Helper()
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/route" {
			atomic.AddInt32(&n, 1)
			_ = json.NewEncoder(w).Encode(engineResponse{Model: model, Task: task, Confidence: 0.9})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &n
}

func TestRoutingPolicy_OverrideWinsWhenEnabled(t *testing.T) {
	c := Client{Policy: Policy{Prefer: map[string][]string{"code": {"heur-model"}}}}
	rp := RoutingPolicy{
		Overrides: map[string]string{"code": "pinned-model"},
		Enabled:   rpEnabled("pinned-model", "heur-model"),
	}
	d := c.RouteDecisionFor(context.Background(), Request{TaskHint: TaskCode}, Slo{}, rp)
	if d.Model != "pinned-model" || d.Source != SourceOverride {
		t.Fatalf("override not honored: got %+v", d)
	}
}

func TestRoutingPolicy_OverrideSkippedWhenNotEnabled(t *testing.T) {
	c := Client{Policy: Policy{Prefer: map[string][]string{"code": {"heur-model"}}}}
	rp := RoutingPolicy{
		Overrides: map[string]string{"code": "pinned-model"}, // absent from Enabled
		Enabled:   rpEnabled("heur-model"),
	}
	d := c.RouteDecisionFor(context.Background(), Request{TaskHint: TaskCode}, Slo{}, rp)
	if d.Model != "heur-model" || d.Source != SourceHeuristic {
		t.Fatalf("a disabled override must fall through to the heuristic: got %+v", d)
	}
}

func TestRoutingPolicy_EnabledRestrictsHeuristic(t *testing.T) {
	c := Client{Policy: Policy{Prefer: map[string][]string{"code": {"a", "b"}}}}
	rp := RoutingPolicy{Enabled: rpEnabled("b")} // a disabled, b allowed
	d := c.RouteDecisionFor(context.Background(), Request{TaskHint: TaskCode}, Slo{}, rp)
	if d.Model != "b" || d.Source != SourceHeuristic {
		t.Fatalf("enabled set should pick the first servable (b): got %+v", d)
	}
}

func TestRoutingPolicy_HeuristicStrategySkipsEngine(t *testing.T) {
	endpoint, hits := rpEngineStub(t, "engine-model", "code")
	c := Client{Endpoint: endpoint, Policy: Policy{Prefer: map[string][]string{"code": {"heur-model"}}}}
	rp := RoutingPolicy{Strategy: StrategyHeuristic, Enabled: rpEnabled("heur-model", "engine-model")}
	d := c.RouteDecisionFor(context.Background(), Request{TaskHint: TaskCode}, Slo{}, rp)
	if atomic.LoadInt32(hits) != 0 {
		t.Fatalf("heuristic strategy must not call the engine, got %d hits", *hits)
	}
	if d.Model != "heur-model" || d.Source != SourceHeuristic {
		t.Fatalf("expected heuristic decision, got %+v", d)
	}
}

func TestRoutingPolicy_EnsoStrategyUsesEngine(t *testing.T) {
	endpoint, hits := rpEngineStub(t, "engine-model", "code")
	c := Client{Endpoint: endpoint, Policy: Policy{Prefer: map[string][]string{"code": {"heur-model"}}}}
	rp := RoutingPolicy{Strategy: StrategyEnso, Enabled: rpEnabled("engine-model", "heur-model")}
	d := c.RouteDecisionFor(context.Background(), Request{TaskHint: TaskCode}, Slo{}, rp)
	if atomic.LoadInt32(hits) != 1 {
		t.Fatalf("enso strategy must call the engine once, got %d", *hits)
	}
	if d.Model != "engine-model" || d.Source != SourceEngine {
		t.Fatalf("expected engine decision, got %+v", d)
	}
}

func TestRoutingPolicy_ZeroPolicyMatchesRouteDecision(t *testing.T) {
	// No endpoint → pure heuristic; the zero policy must reproduce RouteDecision.
	c := Client{Policy: Policy{Prefer: map[string][]string{"code": {"z-model"}}}, Known: rpEnabled("z-model")}
	req := Request{TaskHint: TaskCode}
	got := c.RouteDecisionFor(context.Background(), req, Slo{}, RoutingPolicy{})
	want := c.RouteDecision(context.Background(), req, Slo{})
	if got.Model != want.Model || got.Task != want.Task || got.Source != want.Source {
		t.Fatalf("zero policy diverged from RouteDecision: %+v vs %+v", got, want)
	}
	if got.Model != "z-model" || got.Source != SourceHeuristic {
		t.Fatalf("unexpected zero-policy decision: %+v", got)
	}
}

// TestLastResortRespectsAllowlist pins the enabled-models allowlist as a HARD constraint
// the heuristic's last-resort fallback must not relax. Prefer offers "dear" first then
// "cheap"; the org disabled "dear" (Allow admits only "cheap"); Known rejects everything
// (nothing "servable"). The last resort must relax servability but NEVER the allowlist —
// so it routes to "cheap", never the disabled "dear". With no allowlist (Allow nil) it
// relaxes to all, the historical behavior.
func TestLastResortRespectsAllowlist(t *testing.T) {
	c := Client{
		Policy: Policy{Prefer: map[string][]string{DefaultTaskKey: {"dear", "cheap"}}},
		Known:  func(string) bool { return false },              // nothing servable
		Allow:  func(id string) bool { return id == "cheap" },   // only cheap allowed
	}
	if d := c.RouteDecisionFor(context.Background(), Request{Text: "hi"}, Slo{}, RoutingPolicy{}); d.Model != "cheap" {
		t.Fatalf("last resort routed to %q, want cheap (relax servability, NEVER the allowlist)", d.Model)
	}
	c.Allow = nil // no allowlist → last resort relaxes to all, historical behavior.
	if d := c.RouteDecisionFor(context.Background(), Request{Text: "hi"}, Slo{}, RoutingPolicy{}); d.Model != "dear" {
		t.Fatalf("no allowlist → last resort = %q, want dear (relax-all)", d.Model)
	}
}
