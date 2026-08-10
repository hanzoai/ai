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
	"testing"
	"time"
)

func testPolicy() Policy {
	return Policy{Prefer: map[string][]string{
		"code":    {"zen4-coder"},
		"math":    {"zen4-ultra"},
		"default": {"zen4"},
	}}
}

// TestRouteHeuristicOnly: no endpoint → pure heuristic classification.
func TestRouteHeuristicOnly(t *testing.T) {
	c := Client{Policy: testPolicy()}
	model, task := c.Route(context.Background(), Request{Text: "refactor this function", ApproxTokens: 40}, Slo{})
	if task != TaskCode || model != "zen4-coder" {
		t.Fatalf("got (%q,%q), want (zen4-coder,code)", model, task)
	}
}

// TestRouteEngineSuccess: the engine's explicit, servable model is honored.
func TestRouteEngineSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/route" {
			t.Errorf("path = %q, want /route", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(engineResponse{Model: "zen5-max", Task: "reasoning", Confidence: 0.9})
	}))
	defer srv.Close()

	c := Client{Endpoint: srv.URL, Policy: testPolicy()}
	model, task := c.Route(context.Background(), Request{Text: "hello", ApproxTokens: 2}, Slo{})
	if model != "zen5-max" || task != TaskReasoning {
		t.Fatalf("got (%q,%q), want (zen5-max,reasoning)", model, task)
	}
}

// TestRouteEngineTaskOnly: engine returns only a task → policy maps it.
func TestRouteEngineTaskOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(engineResponse{Task: "math", Confidence: 0.7})
	}))
	defer srv.Close()

	c := Client{Endpoint: srv.URL, Policy: testPolicy()}
	model, task := c.Route(context.Background(), Request{Text: "hi", ApproxTokens: 2}, Slo{})
	if model != "zen4-ultra" || task != TaskMath {
		t.Fatalf("got (%q,%q), want (zen4-ultra,math)", model, task)
	}
}

// TestRouteEngine500FallsBack: an engine 500 falls through to the heuristic.
func TestRouteEngine500FallsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := Client{Endpoint: srv.URL, Policy: testPolicy()}
	model, task := c.Route(context.Background(), Request{Text: "refactor this function", ApproxTokens: 40}, Slo{})
	if model != "zen4-coder" || task != TaskCode {
		t.Fatalf("500 fallback got (%q,%q), want heuristic (zen4-coder,code)", model, task)
	}
}

// TestRouteEngineTimeoutFallsBack: a slow engine is abandoned at the hard cap.
func TestRouteEngineTimeoutFallsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(engineResponse{Model: "should-not-be-used"})
	}))
	defer srv.Close()

	c := Client{Endpoint: srv.URL, Timeout: 30 * time.Millisecond, Policy: testPolicy()}
	start := time.Now()
	model, _ := c.Route(context.Background(), Request{Text: "hi", ApproxTokens: 2}, Slo{})
	if elapsed := time.Since(start); elapsed > 150*time.Millisecond {
		t.Errorf("route took %v, expected fast timeout fallback", elapsed)
	}
	if model != "zen4" {
		t.Fatalf("timeout fallback got %q, want heuristic default zen4", model)
	}
}

// TestRouteEngineUnservableModelFallsBack: engine names a model the caller can't
// serve and no task → fall through to the heuristic.
func TestRouteEngineUnservableModelFallsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(engineResponse{Model: "phantom-model"})
	}))
	defer srv.Close()

	c := Client{
		Endpoint: srv.URL,
		Policy:   testPolicy(),
		Known:    func(id string) bool { return id == "zen4-coder" || id == "zen4" },
	}
	model, task := c.Route(context.Background(), Request{Text: "refactor this function", ApproxTokens: 40}, Slo{})
	if model != "zen4-coder" || task != TaskCode {
		t.Fatalf("unservable fallback got (%q,%q), want (zen4-coder,code)", model, task)
	}
}

// TestMissesCountEveryFallback proves the failure that hid an unconfigured engine
// for fourteen days is now COUNTED. Each engine exit — non-200, unreachable, and an
// unservable choice — must bump Misses, so a share stuck at zero can be told apart
// from a strategy that never ran at all.
func TestMissesCountEveryFallback(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer down.Close()
	phantom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(engineResponse{Model: "phantom-model"})
	}))
	defer phantom.Close()

	unreachable := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	unreachableURL := unreachable.URL
	unreachable.Close() // nothing is listening now → transport error

	cases := []struct {
		name string
		c    Client
	}{
		{"non-200", Client{Endpoint: down.URL, Policy: testPolicy()}},
		{"transport", Client{Endpoint: unreachableURL, Policy: testPolicy()}},
		{"unservable choice", Client{
			Endpoint: phantom.URL,
			Policy:   testPolicy(),
			Known:    func(id string) bool { return id == "zen4" },
		}},
	}
	for _, tc := range cases {
		before := Misses()
		if model, _ := tc.c.Route(context.Background(), Request{Text: "hi", ApproxTokens: 2}, Slo{}); model == "" {
			t.Fatalf("%s: routing must still resolve via the heuristic", tc.name)
		}
		if got := Misses(); got != before+1 {
			t.Fatalf("%s: Misses = %d, want %d — a silent fallback is exactly the bug", tc.name, got, before+1)
		}
	}
}

// TestNoMissOnSuccess: a healthy engine must NOT be counted as a miss, or the
// counter cannot be trusted to mean "the engine is dead".
func TestNoMissOnSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(engineResponse{Model: "zen5-max", Task: "reasoning"})
	}))
	defer srv.Close()

	before := Misses()
	c := Client{Endpoint: srv.URL, Policy: testPolicy()}
	if model, _ := c.Route(context.Background(), Request{Text: "hello", ApproxTokens: 2}, Slo{}); model != "zen5-max" {
		t.Fatalf("engine pick = %q, want zen5-max", model)
	}
	if got := Misses(); got != before {
		t.Fatalf("Misses moved on a healthy engine: %d → %d", before, got)
	}
}
