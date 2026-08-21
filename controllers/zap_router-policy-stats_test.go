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

package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/luxfi/zap"

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/object"
)

// decodeCloudRPS pulls the (status, envelope) out of a native ZAP cloud response.
func decodeCloudRPS(t *testing.T, msg *zap.Message) (uint32, Response) {
	t.Helper()
	status := msg.Root().Uint32(object.CloudRespStatus)
	var env Response
	if body := msg.Root().Bytes(object.CloudRespBody); len(body) > 0 {
		if err := json.Unmarshal(body, &env); err != nil {
			t.Fatalf("decode envelope: %v (body=%s)", err, string(body))
		}
	}
	return status, env
}

// TestZapRPSRoutesRegistered proves the RESTful nouns dispatch through the ONE
// canonical registry: the multi-verb policy noun as an HTTP-shaped ROUTE (method
// aware), the single-verb nouns as body-only PATHS, and the native cloud methods.
func TestZapRPSRoutesRegistered(t *testing.T) {
	if len(lookupGatewayRoutes("/v1/ai/router/policy")) == 0 {
		t.Error("/v1/ai/router/policy not registered as an HTTP-shaped gateway route")
	}
	for _, p := range []string{
		"/v1/ai/router/stats", "/v1/ai/router/defaults", "/v1/ai/router/ledger",
		"/v1/ai/router/rewards", "/v1/ai/router/artifact-meta",
		"/v1/ai/traffic/globe",
	} {
		if _, ok := lookupGatewayHandler(p); !ok {
			t.Errorf("gateway path %q not registered", p)
		}
	}
	for _, m := range []string{
		"router.stats", "router.defaults", "router.ledger", "router.rewards",
		"router.artifact-meta", "feedback", "traffic.globe",
		"training.get-contribution", "training.update-contribution",
	} {
		if _, ok := lookupCloudHandler(m); !ok {
			t.Errorf("cloud method %q not registered", m)
		}
	}
}

// TestZapRPSOldCompoundPathsGone is the guard: every OLD compound-verb path this
// refactor retired must dispatch NOWHERE — neither an HTTP-shaped route nor a
// body-only path claims it — so the gateway 404s it. No backwards-compat alias
// survives, and no route carries both a the router and a ZAP registration.
func TestZapRPSOldCompoundPathsGone(t *testing.T) {
	for _, p := range []string{
		"/v1/get-router-policy", "/v1/update-router-policy",
		"/v1/get-routing-defaults", "/v1/export-routing-ledger",
		"/v1/export-routing-rewards", "/v1/ai/router/publish-artifact-meta",
		"/v1/add-routing-reward",
		"/v1/get-org-settings", "/v1/update-org-settings", "/v1/delete-org-settings",
		"/v1/add-org-settings", "/v1/get-org-settings-list",
	} {
		if len(lookupGatewayRoutes(p)) > 0 {
			t.Errorf("retired path %q still resolves an HTTP-shaped route", p)
		}
		if _, ok := lookupGatewayHandler(p); ok {
			t.Errorf("retired path %q still resolves a body-only handler", p)
		}
	}
}

// TestZapRouterPolicyMethodAware proves the one /v1/ai/router/policy noun dispatches by
// method: GET → the read (admin-gated, so 403 for a non-admin under non-preview),
// and any unsupported verb → 405.
func TestZapRouterPolicyMethodAware(t *testing.T) {
	if st, _ := decodeCloudRPS(t, okMsg(zapRouterPolicyHandler(context.Background(), http.MethodPatch, "/v1/ai/router/policy", "", "", nil))); st != http.StatusMethodNotAllowed {
		t.Errorf("PATCH /v1/ai/router/policy: status = %d, want 405", st)
	}
	if !conf.IsPreviewMode() {
		if st, _ := decodeCloudRPS(t, okMsg(zapRouterPolicyHandler(context.Background(), http.MethodGet, "/v1/ai/router/policy", "", "", nil))); st != http.StatusForbidden {
			t.Errorf("GET /v1/ai/router/policy non-admin: status = %d, want 403", st)
		}
	}
}

// TestZapRPSAuthRejection asserts the ONE identity seam refuses an unauthenticated
// caller on every gated handler, with the SAME status shape the router path returns:
// principal-required endpoints → 401, and the admin CRUD → 403 (non-admin).
func TestZapRPSAuthRejection(t *testing.T) {
	// feedback / add-routing-reward: no principal → 401.
	if st, _ := decodeCloudRPS(t, okMsg(zapAddRoutingRewardHandler(context.Background(), "", []byte(`{"request_id":"x","signal":"up"}`)))); st != 401 {
		t.Errorf("add-routing-reward empty auth: status = %d, want 401", st)
	}
	// stats org-scope (no scope=platform): no principal → 401.
	if st, _ := decodeCloudRPS(t, okMsg(zapGetRouterStatsHandler(context.Background(), "", []byte(`{}`)))); st != 401 {
		t.Errorf("router.stats org-scope empty auth: status = %d, want 401", st)
	}
	// publish-artifact-meta: super-admin only, no principal → 401.
	if st, _ := decodeCloudRPS(t, okMsg(zapPublishRouterArtifactMetaHandler(context.Background(), "", []byte(`{}`)))); st != 401 {
		t.Errorf("publish-artifact-meta empty auth: status = %d, want 401", st)
	}
	// get-router-policy: org admin-gated → 403 for a non-admin (skipped under
	// preview mode, which intentionally opens the admin CRUD).
	if !conf.IsPreviewMode() {
		if st, _ := decodeCloudRPS(t, okMsg(zapGetRouterPolicyHandler(context.Background(), "", nil))); st != 403 {
			t.Errorf("get-router-policy non-admin: status = %d, want 403", st)
		}
	}
}

// TestZapRPSTrafficGlobePublic asserts the public, auth-exempt traffic globe
// returns a 200 ok envelope with no credential — parity with the controller handler.
func TestZapRPSTrafficGlobePublic(t *testing.T) {
	st, env := decodeCloudRPS(t, okMsg(zapGetTrafficGlobeHandler(context.Background(), "", nil)))
	if st != 200 {
		t.Fatalf("traffic globe: status = %d, want 200", st)
	}
	if env.Status != "ok" {
		t.Fatalf("traffic globe: envelope status = %q, want ok", env.Status)
	}
}

// TestZapRPSStatsPlatformHappyPath stubs the ledger + retrain seams and asserts the
// public platform aggregate is computed and returned (200 ok) with the window
// event count reflecting the stubbed events — no auth, no DB, no absolute cost.
func TestZapRPSStatsPlatformHappyPath(t *testing.T) {
	now := time.Now().UTC()
	origEvents := getRoutingEvents
	origMeta := getRouterArtifactMeta
	defer func() { getRoutingEvents = origEvents; getRouterArtifactMeta = origMeta }()

	getRoutingEvents = func(org, since string) ([]*object.RoutingEvent, error) {
		return []*object.RoutingEvent{
			{RoutedModel: "gpt-4o", Task: "chat", Source: "engine", CreatedTime: now.Format(time.RFC3339)},
			{RoutedModel: "gpt-4o", Task: "chat", Source: "engine", CreatedTime: now.Format(time.RFC3339)},
		}, nil
	}
	getRouterArtifactMeta = func(owner string) (*object.RouterArtifactMeta, error) { return nil, nil }

	st, env := decodeCloudRPS(t, okMsg(zapGetRouterStatsHandler(context.Background(), "", []byte(`{"scope":"platform"}`))))
	if st != 200 {
		t.Fatalf("stats platform: status = %d, want 200", st)
	}
	var stats routerStats
	raw, _ := json.Marshal(env.Data)
	if err := json.Unmarshal(raw, &stats); err != nil {
		t.Fatalf("decode routerStats: %v", err)
	}
	if stats.Scope != scopePlatform {
		t.Errorf("scope = %q, want %q", stats.Scope, scopePlatform)
	}
	if stats.Window.Events != 2 {
		t.Errorf("window events = %d, want 2", stats.Window.Events)
	}
	// Platform scope shows the REAL model roster (owner product decision — see
	// GetRouterStats): ids are never masked; only absolute $ and org identity are
	// redacted. gpt-4o must appear with both events.
	if n := stats.ByModel["gpt-4o"]; n != 2 {
		t.Errorf("by_model[gpt-4o] = %d, want 2", n)
	}
	total := 0
	for _, n := range stats.ByModel {
		total += n
	}
	if total != 2 {
		t.Errorf("by_model total = %d, want 2", total)
	}
}

// okMsg unwraps a handler's (msg, err) return — its two params consume the
// handler's two-value result directly, so decodeCloudRPS(t, okMsg(handler(…)))
// composes cleanly. A ZAP handler never errors on a policy denial (it encodes the
// status in the message), so any err here is a real fault.
func okMsg(msg *zap.Message, err error) *zap.Message {
	if err != nil {
		panic(err)
	}
	return msg
}
