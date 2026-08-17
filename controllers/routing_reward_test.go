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
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"

	"github.com/hanzoai/ai/object"
)

func fptr(f float64) *float64 { return &f }

// newRewardController builds an ApiController wired to an httptest recorder with a
// request body and an optional session principal (via ctrlFakeSession, so
// GetSessionUser resolves without a live session manager), for the reward
// endpoints' status/scope tests.
func newRewardController(method, url, body string, user *iam.User) (*ApiController, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, url, strings.NewReader(body))
	ctx := web.NewContext()
	ctx.Reset(rec, req)
	ctx.Input.RequestBody = []byte(body)
	sess := &ctrlFakeSession{data: map[interface{}]interface{}{}}
	if user != nil {
		sess.data["user"] = iam.Claims{User: *user}
	}
	ctx.Input.CruSession = sess
	c := visit("GET", "/v1/")
	return c, rec
}

// TestResolveReward pins the /v1/feedback contract: the signal set maps to a reward
// server-side; `rating` (1..3) normalizes to [0,1]; `dismiss` records NOTHING; an
// explicit reward overrides; a missing/unknown signal is rejected.
func TestResolveReward(t *testing.T) {
	cases := []struct {
		name       string
		reward     *float64
		rating     *float64
		signal     string
		want       float64
		wantRecord bool
		wantErr    bool
	}{
		{"explicit reward passthrough", fptr(0.7), nil, "", 0.7, true, false},
		{"explicit reward zero", fptr(0), nil, "", 0, true, false},
		{"explicit reward one", fptr(1), nil, "", 1, true, false},
		{"explicit reward below range", fptr(-0.1), nil, "", 0, false, true},
		{"explicit reward above range", fptr(1.1), nil, "", 0, false, true},
		{"signal up → 1", nil, nil, "up", 1, true, false},
		{"signal accept → 1", nil, nil, "accept", 1, true, false},
		{"signal down → 0", nil, nil, "down", 0, true, false},
		{"signal switch → 0", nil, nil, "switch", 0, true, false},
		{"signal abandon → 0", nil, nil, "abandon", 0, true, false},
		{"signal revert → 0", nil, nil, "revert", 0, true, false},
		{"signal regenerate → 0.25", nil, nil, "regenerate", 0.25, true, false},
		{"signal case-insensitive", nil, nil, "UP", 1, true, false},
		{"rating 1 → 0 (neg)", nil, fptr(1), "rating", 0, true, false},
		{"rating 2 → 0.5 (neutral)", nil, fptr(2), "rating", 0.5, true, false},
		{"rating 3 → 1 (strong-pos)", nil, fptr(3), "rating", 1, true, false},
		{"rating out of range", nil, fptr(4), "rating", 0, false, true},
		{"rating signal without rating", nil, nil, "rating", 0, false, true},
		{"dismiss records nothing", nil, nil, "dismiss", 0, false, false},
		{"no signal, no reward", nil, nil, "", 0, false, true},
		{"unknown signal rejected", nil, nil, "meh", 0, false, true},
		{"explicit reward wins over signal", fptr(0.9), nil, "down", 0.9, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, record, err := resolveReward(tc.reward, tc.rating, tc.signal)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveReward = (%v, %v, nil), want error", got, record)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveReward error: %v", err)
			}
			if record != tc.wantRecord {
				t.Errorf("record = %v, want %v", record, tc.wantRecord)
			}
			if record && got != tc.want {
				t.Errorf("resolveReward = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestNormalizeRequestId proves both accepted request-id forms — the raw usage
// -ledger id and the response `chatcmpl-<id>` — collapse to the one stored form.
func TestNormalizeRequestId(t *testing.T) {
	cases := map[string]string{
		"chatcmpl-abc":    "abc",
		"abc":             "abc",
		"  chatcmpl-abc ": "abc",
		"   ":             "",
	}
	for in, want := range cases {
		if got := normalizeRequestId(in); got != want {
			t.Errorf("normalizeRequestId(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestWriteRoutingRewardsJSONL proves the training-tuple contract: one JSON object
// per line carrying features/model/task/reward/at, the feature vector round-trips
// as a raw array, a featureless event omits it, and NO identity or prompt field
// (owner/user/id/prompt) leaks into the training export.
func TestWriteRoutingRewardsJSONL(t *testing.T) {
	events := []*object.RoutingEvent{
		{
			Id: "e1", CreatedTime: "2026-07-07T00:00:00Z", Owner: "acme", User: "acme/alice",
			RequestId: "req-1", Task: "code", RoutedModel: "glm-5.2",
			Features: "[0.1,0.2]", Reward: 0.75, RewardedTime: "2026-07-07T01:00:00Z",
		},
		{
			Id: "e2", CreatedTime: "2026-07-07T00:01:00Z", Owner: "acme", RequestId: "req-2",
			Task: "general", RoutedModel: "glm-5.2-mini", Reward: 0, RewardedTime: "2026-07-07T01:01:00Z",
		},
	}

	var buf bytes.Buffer
	if err := writeRoutingRewardsJSONL(&buf, events); err != nil {
		t.Fatalf("writeRoutingRewardsJSONL: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("emitted %d lines, want 2", len(lines))
	}

	var l0 map[string]json.RawMessage
	if err := json.Unmarshal([]byte(lines[0]), &l0); err != nil {
		t.Fatalf("line 0 not JSON: %v", err)
	}
	for _, leak := range []string{"owner", "user", "id", "prompt", "request_id", "requestId"} {
		if _, has := l0[leak]; has {
			t.Errorf("training tuple leaked %q field", leak)
		}
	}
	if string(l0["features"]) != `[0.1,0.2]` {
		t.Errorf("features = %s, want [0.1,0.2]", l0["features"])
	}
	if string(l0["model"]) != `"glm-5.2"` || string(l0["task"]) != `"code"` {
		t.Errorf("model/task = %s/%s, want glm-5.2/code", l0["model"], l0["task"])
	}
	if string(l0["reward"]) != `0.75` || string(l0["at"]) != `"2026-07-07T00:00:00Z"` {
		t.Errorf("reward/at = %s/%s, want 0.75/2026-07-07T00:00:00Z", l0["reward"], l0["at"])
	}

	// The featureless event omits "features" but still carries reward 0 (a valid score).
	var l1 map[string]json.RawMessage
	if err := json.Unmarshal([]byte(lines[1]), &l1); err != nil {
		t.Fatalf("line 1 not JSON: %v", err)
	}
	if _, has := l1["features"]; has {
		t.Error("featureless tuple emitted a features field, want omitted")
	}
	if string(l1["reward"]) != `0` {
		t.Errorf("featureless reward = %s, want 0", l1["reward"])
	}
}

// TestAddRoutingRewardStatus drives the ingest endpoint: no principal → 401, a bad
// / incomplete / out-of-range body → 400, an unknown request_id → 404 (with the
// reward scoped to the principal's OWN org and the request_id normalized), and a
// good body → 200 echoing the canonical reward (a 5★ rating normalized to 1).
func TestAddRoutingRewardStatus(t *testing.T) {
	prev := attachRoutingReward
	t.Cleanup(func() { attachRoutingReward = prev })

	acme := &iam.User{Owner: "acme", Name: "alice"}
	mustNotCall := func(string, string, float64) (bool, error) {
		t.Fatalf("attachRoutingReward called on a reject path")
		return false, nil
	}

	// 401: no credential at all.
	attachRoutingReward = mustNotCall
	c, rec := newRewardController("POST", "/v1/feedback", `{"request_id":"r","reward":0.5}`, nil)
	c.AddRoutingReward()
	if answered(c) != 401 {
		t.Errorf("no principal → %d, want 401", answered(c))
	}

	// 400: malformed body, missing request_id, out-of-range reward.
	for _, body := range []string{`{`, `{"reward":0.5}`, `{"request_id":"r","reward":1.5}`} {
		attachRoutingReward = mustNotCall
		c, rec := newRewardController("POST", "/v1/feedback", body, acme)
		c.AddRoutingReward()
		if answered(c) != 400 {
			t.Errorf("body %q → %d, want 400", body, answered(c))
		}
	}

	// 404: unknown request_id. Capture what the object layer was asked for — the
	// reward must be scoped to the principal's org and the id normalized.
	var gotOrg, gotReq string
	var gotReward float64
	attachRoutingReward = func(org, requestId string, reward float64) (bool, error) {
		gotOrg, gotReq, gotReward = org, requestId, reward
		return false, nil
	}
	c, rec = newRewardController("POST", "/v1/feedback", `{"request_id":"chatcmpl-abc","reward":0.5}`, acme)
	c.AddRoutingReward()
	if answered(c) != 404 {
		t.Errorf("unknown request_id → %d, want 404", answered(c))
	}
	if gotOrg != "acme" || gotReq != "abc" || gotReward != 0.5 {
		t.Errorf("attach args = (%q, %q, %v), want (acme, abc, 0.5)", gotOrg, gotReq, gotReward)
	}

	// 200: found. A 3-rating ("strong-pos") normalizes to the canonical reward 1,
	// echoed back with recorded=true. The online forward is stubbed (no live engine).
	prevFwd := forwardRewardToEngine
	forwardRewardToEngine = func(string, string, float64) {}
	t.Cleanup(func() { forwardRewardToEngine = prevFwd })
	attachRoutingReward = func(org, requestId string, reward float64) (bool, error) {
		gotReward = reward
		return true, nil
	}
	c, rec = newRewardController("POST", "/v1/feedback", `{"request_id":"abc","signal":"rating","rating":3}`, acme)
	c.AddRoutingReward()
	if answered(c) != 200 {
		t.Fatalf("found → %d, want 200 (body %s)", answered(c), sent(c))
	}
	if gotReward != 1 {
		t.Errorf("rating 3 stored reward = %v, want 1", gotReward)
	}
	var env struct {
		Status string              `json:"status"`
		Data   routingRewardResult `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if env.Status != "ok" || env.Data.RequestId != "abc" || env.Data.Reward != 1 || !env.Data.Recorded {
		t.Errorf("echo = %+v, want ok/abc/1/recorded", env)
	}

	// dismiss: accepted (200) but records NOTHING — never reaches the attach.
	attachRoutingReward = mustNotCall
	c, rec = newRewardController("POST", "/v1/feedback", `{"request_id":"abc","signal":"dismiss"}`, acme)
	c.AddRoutingReward()
	if answered(c) != 200 {
		t.Fatalf("dismiss → %d, want 200 (body %s)", answered(c), sent(c))
	}
	env = struct {
		Status string              `json:"status"`
		Data   routingRewardResult `json:"data"`
	}{}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Data.Recorded {
		t.Errorf("dismiss must not record, got recorded=true")
	}
}

// TestExportRoutingRewardsStatus proves the training export is gated and streams the
// rewarded tuples as JSONL — over the ZAP-native handler, the ONE implementation. The
// gate is super-admin OR the ROUTER_ADMIN_TOKEN service token (zapRPSRouterAdminAuthorized).
func TestExportRoutingRewardsStatus(t *testing.T) {
	prev := getRewardedRoutingEvents
	t.Cleanup(func() { getRewardedRoutingEvents = prev })

	// Gated: no credential → 401 (ZAP-native RequireSuperAdmin), the export never runs.
	getRewardedRoutingEvents = func(string, string) ([]*object.RoutingEvent, error) {
		t.Fatalf("export ran for an unauthenticated caller")
		return nil, nil
	}
	msg := okMsg(zapExportRoutingRewardsHandler(context.Background(), "", nil))
	if st := msg.Root().Uint32(object.CloudRespStatus); st != 401 {
		t.Errorf("no credential → %d, want 401", st)
	}

	// Authorized via the ROUTER_ADMIN_TOKEN service token → 200 + the JSONL tuple stream.
	t.Setenv("ROUTER_ADMIN_TOKEN", "svc-tok")
	getRewardedRoutingEvents = func(string, string) ([]*object.RoutingEvent, error) {
		return []*object.RoutingEvent{
			{RoutedModel: "glm-5.2", Task: "code", Features: "[0.1]", Reward: 0.9, CreatedTime: "2026-07-07T00:00:00Z", RewardedTime: "2026-07-07T01:00:00Z"},
		}, nil
	}
	msg = okMsg(zapExportRoutingRewardsHandler(context.Background(), "Bearer svc-tok", nil))
	if st := msg.Root().Uint32(object.CloudRespStatus); st != 200 {
		t.Fatalf("service token → %d, want 200", st)
	}
	line := strings.TrimSpace(string(msg.Root().Bytes(object.CloudRespBody)))
	var tup map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &tup); err != nil {
		t.Fatalf("export line not JSON: %v (line %q)", err, line)
	}
	if string(tup["model"]) != `"glm-5.2"` || string(tup["reward"]) != `0.9` {
		t.Errorf("tuple model/reward = %s/%s, want glm-5.2/0.9", tup["model"], tup["reward"])
	}
}
