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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

// attachRoutingReward and getRewardedRoutingEvents are indirected through vars so
// the reward-ingest and export contracts are testable without a live DB;
// production reads/writes the routing_event ledger.
var (
	attachRoutingReward      = object.AttachRoutingReward
	getRewardedRoutingEvents = object.GetRewardedRoutingEvents
)

// routingRewardRequest is the reward-ingestion body: the request id (the response
// `chatcmpl-<id>` or the raw usage-ledger request_id) plus EITHER a normalized
// reward (0..1) or a 1..5 star rating. Pointers tell "absent" apart from a real 0.
// No prompt text is ever accepted or stored.
type routingRewardRequest struct {
	RequestId string   `json:"request_id"`
	Reward    *float64 `json:"reward,omitempty"`
	Rating    *float64 `json:"rating,omitempty"`
}

// routingRewardResult echoes the normalized request id and the canonical stored
// reward so the caller can confirm what was recorded.
type routingRewardResult struct {
	RequestId string  `json:"request_id"`
	Reward    float64 `json:"reward"`
}

// resolveReward folds the two accepted inputs into the ONE canonical stored form —
// a reward in [0,1]: an explicit reward is taken as-is; otherwise a 1..5 star
// rating is normalized ((rating-1)/4, so 1★→0 and 5★→1). Exactly one must be
// present and in range; an explicit reward wins if both are sent.
func resolveReward(reward, rating *float64) (float64, error) {
	switch {
	case reward != nil:
		if *reward < 0 || *reward > 1 {
			return 0, fmt.Errorf("reward must be within 0..1")
		}
		return *reward, nil
	case rating != nil:
		if *rating < 1 || *rating > 5 {
			return 0, fmt.Errorf("rating must be within 1..5")
		}
		return (*rating - 1) / 4, nil
	default:
		return 0, fmt.Errorf("reward (0..1) or rating (1..5) is required")
	}
}

// normalizeRequestId trims the id and strips the response-object "chatcmpl-"
// prefix, so a client may pass either the raw request id (as the usage ledger
// stores it) or the response `id` field verbatim — one stored form, both inputs.
func normalizeRequestId(s string) string {
	return strings.TrimPrefix(strings.TrimSpace(s), "chatcmpl-")
}

// AddRoutingReward attaches a per-request outcome reward to the routing decision
// that served request_id — the enso training loop's quality signal. Org-scoped via
// the same session-OR-Bearer principal the usage read uses (RequirePrincipal): the
// reward lands only on the caller's OWN org's event, so a request_id from another
// org (or unknown) is a 404 — cross-org writes are impossible and unknown ids are
// indistinguishable from foreign ones. Idempotent: a repeat overwrites. The body
// carries NO prompt text — only {request_id, reward|rating}.
//
// @Title AddRoutingReward
// @Tag Router API
// @Description attach an outcome reward (0..1) or 1..5 rating to a routed request, for enso training
// @Param body body controllers.routingRewardRequest true "request_id + reward (0..1) or rating (1..5)"
// @Success 200 {object} controllers.routingRewardResult The Response object
// @router /add-routing-reward [post]
func (c *ApiController) AddRoutingReward() {
	user, ok := c.RequirePrincipal()
	if !ok {
		return
	}
	// A training-signal write is never accepted from an anonymous guest (bound to
	// the deployment org): only a real signed-in identity may score its org's
	// requests. Mirrors the usage read's guard.
	if util.IsAnonymousUser(user) {
		c.ResponseUnauthorized(c.T("auth:Please sign in first"))
		return
	}

	var body routingRewardRequest
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, &body); err != nil {
		c.ResponseErrorWithStatus(http.StatusBadRequest, "invalid request body")
		return
	}
	requestId := normalizeRequestId(body.RequestId)
	if requestId == "" {
		c.ResponseErrorWithStatus(http.StatusBadRequest, "request_id is required")
		return
	}
	reward, err := resolveReward(body.Reward, body.Rating)
	if err != nil {
		c.ResponseErrorWithStatus(http.StatusBadRequest, err.Error())
		return
	}

	found, err := attachRoutingReward(user.Owner, requestId, reward)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	if !found {
		c.ResponseErrorWithStatus(http.StatusNotFound, "unknown request_id")
		return
	}
	c.ResponseOk(routingRewardResult{RequestId: requestId, Reward: reward})
}

// rewardTuple is the JSONL training-tuple shape: the enso loop's (features x,
// chosen model, reward) plus the decision time (at) and the optional task class.
// snake_case matches the routing-ledger export contract. No prompt text.
type rewardTuple struct {
	Features json.RawMessage `json:"features,omitempty"`
	Model    string          `json:"model"`
	Task     string          `json:"task,omitempty"`
	Reward   float64         `json:"reward"`
	At       string          `json:"at"`
}

// ExportRoutingRewards streams the LABELED training tuples — the rewarded slice of
// the routing ledger — as JSONL for the enso loop's fit_base/observe. Each line is
// the dumb tuple {features, model, task?, reward, at}: the engine feature vector,
// the routed model, the outcome reward, and the decision time (createdTime, when
// the features were produced — the reward is the label attached later). Super-admin
// only (platform-wide), like the raw ledger export. Filters: ?org= and ?since=.
//
// @Title ExportRoutingRewards
// @Tag Router API
// @Description stream rewarded routing tuples (features, model, reward) as JSONL for enso training (super admin only)
// @Param org query string false "filter to one org"
// @Param since query string false "only events at/after this RFC3339 timestamp"
// @Success 200 {string} string "JSONL, one training tuple per line"
// @router /export-routing-rewards [get]
func (c *ApiController) ExportRoutingRewards() {
	if !c.RequireSuperAdmin() {
		return
	}
	events, err := getRewardedRoutingEvents(c.Input().Get("org"), c.Input().Get("since"))
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.Ctx.Output.Header("Content-Type", "application/x-ndjson")
	_ = writeRoutingRewardsJSONL(c.Ctx.ResponseWriter, events)
	c.EnableRender = false
}

// writeRoutingRewardsJSONL streams rewarded events as JSONL, one training tuple
// per line. Pure in its inputs, so the export contract is unit-testable without a
// DB or beego context. A write error stops the stream (a truncated download beats
// a partial-then-error line).
func writeRoutingRewardsJSONL(w io.Writer, events []*object.RoutingEvent) error {
	enc := json.NewEncoder(w)
	for _, e := range events {
		t := rewardTuple{
			Model:  e.RoutedModel,
			Task:   e.Task,
			Reward: e.Reward,
			At:     e.CreatedTime,
		}
		if e.Features != "" {
			t.Features = json.RawMessage(e.Features)
		}
		if err := enc.Encode(t); err != nil {
			return err
		}
	}
	return nil
}
