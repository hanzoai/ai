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

package controllers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/router"
)

// newFeatureEngine starts a stub zen-router /route endpoint that returns a
// servable model plus a feature vector, and returns its base URL. Closed on test
// cleanup.
func newFeatureEngine(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"deepseek-v4-flash","task":"cheap_chat","confidence":0.7,"features":[0.1,0.2,0.3]}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// stubLookups installs org-settings lookups backed by an in-memory map keyed by
// owner (the reserved "*" row is just another key). It returns a restore func.
func stubLookups(t *testing.T, auto, session map[string]string) {
	t.Helper()
	prevAuto, prevSession := orgAutoRoutingLookup, sessionRoutingLookup
	orgAutoRoutingLookup = func(owner string) string { return auto[owner] }
	sessionRoutingLookup = func(owner string) string { return session[owner] }
	t.Cleanup(func() {
		orgAutoRoutingLookup = prevAuto
		sessionRoutingLookup = prevSession
	})
}

// TestEffectiveAutoRoutingStarFold proves the extended precedence for the
// AutoRouting field: the org's own row wins, else the reserved "*" row, else ""
// (defer to conf). A row's "disabled" beats a lower level's "enabled".
func TestEffectiveAutoRoutingStarFold(t *testing.T) {
	const org = "acme"
	cases := []struct {
		name    string
		orgPref string
		star    string
		want    string
	}{
		{"org-set wins over star", object.AutoRoutingEnabled, object.AutoRoutingDisabled, object.AutoRoutingEnabled},
		{"org-disabled beats star-enabled", object.AutoRoutingDisabled, object.AutoRoutingEnabled, object.AutoRoutingDisabled},
		{"org-unset → star decides (enabled)", object.AutoRoutingUnset, object.AutoRoutingEnabled, object.AutoRoutingEnabled},
		{"org-unset → star decides (disabled)", object.AutoRoutingUnset, object.AutoRoutingDisabled, object.AutoRoutingDisabled},
		{"both unset → unset (conf decides)", object.AutoRoutingUnset, object.AutoRoutingUnset, object.AutoRoutingUnset},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubLookups(t,
				map[string]string{org: tc.orgPref, object.GlobalDefaultOwner: tc.star},
				map[string]string{})
			if got := effectiveAutoRouting(org); got != tc.want {
				t.Errorf("effectiveAutoRouting = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestEffectiveSessionRoutingStarFold proves the same org > "*" > conf(disabled)
// precedence for the DefaultSessionRouting field, and the bool the endpoint
// exposes (only "enabled" → true; unset/"" folds to conf-default disabled).
func TestEffectiveSessionRoutingStarFold(t *testing.T) {
	const org = "acme"
	cases := []struct {
		name        string
		orgPref     string
		star        string
		wantPref    string
		wantEnabled bool
	}{
		{"org-set wins over star", object.AutoRoutingDisabled, object.AutoRoutingEnabled, object.AutoRoutingDisabled, false},
		{"org-enabled beats star-disabled", object.AutoRoutingEnabled, object.AutoRoutingDisabled, object.AutoRoutingEnabled, true},
		{"org-unset → star enabled", object.AutoRoutingUnset, object.AutoRoutingEnabled, object.AutoRoutingEnabled, true},
		{"both unset → disabled (conf default)", object.AutoRoutingUnset, object.AutoRoutingUnset, object.AutoRoutingUnset, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubLookups(t,
				map[string]string{},
				map[string]string{org: tc.orgPref, object.GlobalDefaultOwner: tc.star})
			if got := effectiveSessionRouting(org); got != tc.wantPref {
				t.Errorf("effectiveSessionRouting = %q, want %q", got, tc.wantPref)
			}
			if got := effectiveSessionRouting(org) == object.AutoRoutingEnabled; got != tc.wantEnabled {
				t.Errorf("default_session_routing bool = %v, want %v", got, tc.wantEnabled)
			}
		})
	}
}

// TestRoutingDefaultsResolution proves the two booleans /v1/get-routing-defaults
// returns are resolved exactly as the endpoint composes them: auto_routing_active
// blends the "*"-folded AutoRouting pref with the global router flag, and
// default_session_routing is the "*"-folded session pref. This exercises the same
// code path the controller runs, minus beego plumbing.
func TestRoutingDefaultsResolution(t *testing.T) {
	prev := globalModelConfig
	t.Cleanup(func() { globalModelConfig = prev })

	const org = "acme"
	cases := []struct {
		name        string
		globalOn    bool
		autoOrg     string
		autoStar    string
		sessOrg     string
		sessStar    string
		wantAuto    bool
		wantSession bool
	}{
		{"global on, no rows → auto on, session off", true, "", "", "", "", true, false},
		{"global off, star enables auto", false, "", object.AutoRoutingEnabled, "", "", true, false},
		{"org disables auto over global on", true, object.AutoRoutingDisabled, "", "", "", false, false},
		{"star enables session", false, "", "", "", object.AutoRoutingEnabled, false, true},
		{"org session disabled beats star enabled", false, "", "", object.AutoRoutingDisabled, object.AutoRoutingEnabled, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			globalModelConfig = routerTestConfig(tc.globalOn)
			stubLookups(t,
				map[string]string{org: tc.autoOrg, object.GlobalDefaultOwner: tc.autoStar},
				map[string]string{org: tc.sessOrg, object.GlobalDefaultOwner: tc.sessStar})

			gotAuto := GetModelConfig().AutoRoutingActive(effectiveAutoRouting(org))
			gotSession := effectiveSessionRouting(org) == object.AutoRoutingEnabled
			if gotAuto != tc.wantAuto {
				t.Errorf("auto_routing_active = %v, want %v", gotAuto, tc.wantAuto)
			}
			if gotSession != tc.wantSession {
				t.Errorf("default_session_routing = %v, want %v", gotSession, tc.wantSession)
			}
		})
	}
}

// TestRoutingDefaultsFailSafe proves the store-error path: the object lookups
// return "" (unset) on any DB error, so with the global flag off and no readable
// rows both defaults resolve to their safe off state (conf decides / disabled) —
// a lookup failure never flips routing on.
func TestRoutingDefaultsFailSafe(t *testing.T) {
	prev := globalModelConfig
	t.Cleanup(func() { globalModelConfig = prev })
	globalModelConfig = routerTestConfig(false) // global router off

	// Simulate a total store failure: every lookup yields "" (the fail-safe value
	// GetCachedOrg* return on error).
	stubLookups(t, map[string]string{}, map[string]string{})

	if GetModelConfig().AutoRoutingActive(effectiveAutoRouting("acme")) {
		t.Error("auto_routing_active = true on store failure, want false (fail-safe)")
	}
	if effectiveSessionRouting("acme") == object.AutoRoutingEnabled {
		t.Error("default_session_routing = true on store failure, want false (fail-safe)")
	}
}

// TestRoutingEventFiredOnResolve proves a routing event is collected on a
// successful auto resolution, carrying the resolved task/model/source and NO
// prompt text, and that the requested alias and user are recorded.
func TestRoutingEventFiredOnResolve(t *testing.T) {
	prevCfg, prevSink := globalModelConfig, routingEventSink
	t.Cleanup(func() { globalModelConfig = prevCfg; routingEventSink = prevSink })
	globalModelConfig = routerTestConfig(true)

	var got []object.RoutingEvent
	routingEventSink = func(e object.RoutingEvent) { got = append(got, e) }

	model, ok := resolveAutoModel("auto", "acme", "acme/alice", "req-fired",
		chatReq("auto", "please refactor this function"), router.Slo{})
	if !ok || model != "glm-5.2" {
		t.Fatalf("resolveAutoModel = (%q, %v), want (glm-5.2, true)", model, ok)
	}
	if len(got) != 1 {
		t.Fatalf("collected %d events, want 1", len(got))
	}
	e := got[0]
	if e.Owner != "acme" || e.User != "acme/alice" {
		t.Errorf("event owner/user = %q/%q, want acme/acme/alice", e.Owner, e.User)
	}
	if e.RequestId != "req-fired" {
		t.Errorf("event request_id = %q, want req-fired (the reward join key)", e.RequestId)
	}
	if e.Task != "code" || e.RoutedModel != "glm-5.2" || e.RequestedModel != "auto" {
		t.Errorf("event task/routed/requested = %q/%q/%q, want code/glm-5.2/auto", e.Task, e.RoutedModel, e.RequestedModel)
	}
	if e.Source != router.SourceHeuristic {
		t.Errorf("event source = %q, want %q", e.Source, router.SourceHeuristic)
	}
	// Privacy invariant: no field carries prompt text.
	blob := strings.ToLower(e.Task + e.RoutedModel + e.RequestedModel + e.Features + e.User + e.Owner + e.RequestId)
	if strings.Contains(blob, "refactor") {
		t.Errorf("routing event leaked prompt text: %+v", e)
	}
}

// TestRoutingEventAbsentOnNonAuto proves no event is collected when routing does
// not fire: a concrete (non-auto) model, and an auto request with routing off.
func TestRoutingEventAbsentOnNonAuto(t *testing.T) {
	prevCfg, prevSink := globalModelConfig, routingEventSink
	t.Cleanup(func() { globalModelConfig = prevCfg; routingEventSink = prevSink })

	var count int
	routingEventSink = func(object.RoutingEvent) { count++ }

	globalModelConfig = routerTestConfig(true)
	if _, ok := resolveAutoModel("gpt-4o", "acme", "acme/alice", "", chatReq("gpt-4o", "hi"), router.Slo{}); ok {
		t.Fatal("resolveAutoModel routed a concrete model")
	}

	globalModelConfig = routerTestConfig(false) // routing globally off
	if _, ok := resolveAutoModel("auto", "acme", "acme/alice", "", chatReq("auto", "hi"), router.Slo{}); ok {
		t.Fatal("resolveAutoModel routed with routing disabled")
	}

	if count != 0 {
		t.Errorf("collected %d events on non-routed requests, want 0", count)
	}
}

// TestRoutingEventFeaturesSerialized proves the engine feature vector is passed
// through and stored as a JSON array string (never prompt text). The router
// heuristic never emits features; an engine reply does — this drives the engine
// path via a stub /route server through the real router.Client.
func TestRoutingEventFeaturesSerialized(t *testing.T) {
	prevCfg, prevSink := globalModelConfig, routingEventSink
	t.Cleanup(func() { globalModelConfig = prevCfg; routingEventSink = prevSink })

	var got []object.RoutingEvent
	routingEventSink = func(e object.RoutingEvent) { got = append(got, e) }

	// A ModelConfig whose router points at an engine returning a feature vector.
	cfg := routerTestConfig(true)
	cfg.router.Endpoint = newFeatureEngine(t)
	globalModelConfig = cfg

	if _, ok := resolveAutoModel("auto", "acme", "acme/alice", "", chatReq("auto", "hi"), router.Slo{}); !ok {
		t.Fatal("resolveAutoModel not ok")
	}
	if len(got) != 1 {
		t.Fatalf("collected %d events, want 1", len(got))
	}
	var feats []float64
	if err := json.Unmarshal([]byte(got[0].Features), &feats); err != nil {
		t.Fatalf("features not a JSON array: %q (%v)", got[0].Features, err)
	}
	if len(feats) != 3 || feats[0] != 0.1 {
		t.Errorf("features = %v, want [0.1 0.2 0.3]", feats)
	}
	if got[0].Source != router.SourceEngine {
		t.Errorf("source = %q, want %q", got[0].Source, router.SourceEngine)
	}
}

// TestWriteRoutingLedgerJSONL proves the export contract: one JSON object per
// line, "model" aliases the routed model (for build_dataset.py --ledger), the
// feature vector round-trips as a raw JSON array, NO prompt field is emitted, and
// only the passed (already-filtered) events appear.
func TestWriteRoutingLedgerJSONL(t *testing.T) {
	events := []*object.RoutingEvent{
		{
			Id: "e1", CreatedTime: "2026-07-07T00:00:00Z", Owner: "acme", User: "acme/alice",
			Task: "code", RequestedModel: "auto", RoutedModel: "glm-5.2",
			Confidence: 0.9, Source: "engine", Features: "[0.1,0.2]",
		},
		{
			Id: "e2", CreatedTime: "2026-07-07T00:01:00Z", Owner: "acme", User: "",
			Task: "general", RequestedModel: "zen-router", RoutedModel: "gpt-4o",
			Source: "heuristic", // no features
		},
	}

	var buf bytes.Buffer
	if err := writeRoutingLedgerJSONL(&buf, events); err != nil {
		t.Fatalf("writeRoutingLedgerJSONL: %v", err)
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("emitted %d lines, want 2", len(lines))
	}

	var l0 map[string]json.RawMessage
	if err := json.Unmarshal([]byte(lines[0]), &l0); err != nil {
		t.Fatalf("line 0 not JSON: %v", err)
	}
	if _, hasPrompt := l0["prompt"]; hasPrompt {
		t.Error("export leaked a prompt field")
	}
	if string(l0["model"]) != `"glm-5.2"` || string(l0["routed_model"]) != `"glm-5.2"` {
		t.Errorf("model/routed_model = %s/%s, want glm-5.2", l0["model"], l0["routed_model"])
	}
	if string(l0["task"]) != `"code"` {
		t.Errorf("task = %s, want code", l0["task"])
	}
	if string(l0["features"]) != `[0.1,0.2]` {
		t.Errorf("features = %s, want [0.1,0.2]", l0["features"])
	}

	// The featureless event must omit "features" entirely (omitempty).
	var l1 map[string]json.RawMessage
	if err := json.Unmarshal([]byte(lines[1]), &l1); err != nil {
		t.Fatalf("line 1 not JSON: %v", err)
	}
	if _, has := l1["features"]; has {
		t.Error("featureless event emitted a features field, want omitted")
	}
}
