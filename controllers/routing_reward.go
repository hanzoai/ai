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
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/router"
	"github.com/hanzoai/ai/util"
)

// attachRoutingReward and getRewardedRoutingEvents are indirected through vars so
// the reward-ingest and export contracts are testable without a live DB;
// production reads/writes the routing_event ledger.
var (
	attachRoutingReward      = object.AttachRoutingReward
	getRewardedRoutingEvents = object.GetRewardedRoutingEvents
)

// routingRewardRequest is the /v1/feedback body: the request id (the gateway's own
// response id — the chatcmpl-/msg id the client received) plus a client feedback
// signal the SERVER maps to a reward. The canonical shapes (clients code to these):
//
//	{request_id, signal:"up"|"down"|"regenerate"|"switch"|"abandon"|"accept"|"revert"}
//	{request_id, signal:"rating", rating:1|2|3}   (1=neg, 2=neutral, 3=strong-pos)
//	{request_id, signal:"dismiss"}                 (NO reward — analytics only)
//
// Reward (an explicit 0..1) stays accepted as an internal override. No prompt text is
// ever accepted or stored. `/v1/feedback` is the ONE reward endpoint (the old
// `/v1/add-routing-reward` alias was dropped) — one endpoint, one server-owned reward mapping.
type routingRewardRequest struct {
	RequestId string   `json:"request_id"`
	Signal    string   `json:"signal,omitempty"`
	Rating    *float64 `json:"rating,omitempty"`
	Reward    *float64 `json:"reward,omitempty"` // explicit 0..1 override (internal)
}

// routingRewardResult echoes the normalized request id, the canonical stored reward,
// and whether it was actually recorded (false for a `dismiss` or an opted-out org).
type routingRewardResult struct {
	RequestId string  `json:"request_id"`
	Reward    float64 `json:"reward"`
	Recorded  bool    `json:"recorded"`
}

// trainingOptedIn reports whether an org contributes feedback as training labels —
// the SERVER-SIDE enforcement point (clients POST unconditionally; only an opted-in
// org's reward is recorded). It reads OrgSettings.TrainingContribution, a privacy-safe
// opt-OUT default (Unset ⇒ contribute); only an explicit "disabled" drops the reward.
// Indirected through a var so tests can flip it.
var trainingOptedIn = func(org string) bool {
	return object.GetCachedOrgTrainingContribution(org) != object.TrainingContributionDisabled
}

// trainingExplicitlyEnabled reports whether an org has EXPLICITLY set contribution
// to "enabled" — not merely left it at the opt-out default (unset). The per-request
// EU/EEA/UK judge guard (shouldJudge) requires this explicit consent before judging
// a turn geolocated to those jurisdictions; the opt-out default is insufficient
// there. Indirected through a var so tests can flip it.
var trainingExplicitlyEnabled = func(org string) bool {
	return object.GetCachedOrgTrainingContribution(org) == object.TrainingContributionEnabled
}

// resolveReward folds the accepted inputs into the ONE canonical stored reward in
// [0,1], and reports whether a reward should be RECORDED at all: the `dismiss` signal
// records nothing (it is prompt-fatigue analytics, never a reward=0 training label).
// The server owns the signal→reward mapping (signalReward). An explicit `reward` (0..1)
// is an internal override; a bare `rating` is only meaningful under signal "rating".
func resolveReward(reward, rating *float64, signal string) (value float64, record bool, err error) {
	if reward != nil {
		if *reward < 0 || *reward > 1 {
			return 0, false, fmt.Errorf("reward must be within 0..1")
		}
		return *reward, true, nil
	}
	if strings.TrimSpace(signal) == "" {
		return 0, false, fmt.Errorf("signal is required (up|down|regenerate|switch|abandon|accept|revert|rating|dismiss)")
	}
	return signalReward(signal, rating)
}

// signalReward maps a client feedback signal to (reward, record). Positive intent
// (`up`, `accept`) → 1; negative intent (`down`, `switch`, `abandon`, `revert`) → 0;
// `regenerate` (re-rolled the same request) is a mild negative → 0.25. `rating` uses
// the 1..3 scale (1=neg→0, 2=neutral→0.5, 3=strong-pos→1). `dismiss` records NOTHING
// (record=false) — the user closed the prompt without judging; storing it as reward=0
// would poison training, so it is kept only as prompt-fatigue analytics upstream.
func signalReward(signal string, rating *float64) (value float64, record bool, err error) {
	switch strings.ToLower(strings.TrimSpace(signal)) {
	case "up", "accept":
		return 1, true, nil
	case "down", "switch", "abandon", "revert":
		return 0, true, nil
	case "regenerate":
		return 0.25, true, nil
	case "rating":
		if rating == nil {
			return 0, false, fmt.Errorf("signal \"rating\" requires rating (1..3)")
		}
		if *rating < 1 || *rating > 3 {
			return 0, false, fmt.Errorf("rating must be within 1..3")
		}
		return (*rating - 1) / 2, true, nil
	case "dismiss":
		return 0, false, nil
	default:
		return 0, false, fmt.Errorf("unknown signal %q", signal)
	}
}

// normalizeRequestId delegates to object.NormalizeRequestId — the ONE normalization
// the feedback join and the family-event write share, so both key identically.
func normalizeRequestId(s string) string {
	return object.NormalizeRequestId(s)
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
	if err := json.Unmarshal(c.Body(), &body); err != nil {
		c.ResponseErrorWithStatus(http.StatusBadRequest, "invalid request body")
		return
	}
	requestId := normalizeRequestId(body.RequestId)
	if requestId == "" {
		c.ResponseErrorWithStatus(http.StatusBadRequest, "request_id is required")
		return
	}
	reward, record, err := resolveReward(body.Reward, body.Rating, body.Signal)
	if err != nil {
		c.ResponseErrorWithStatus(http.StatusBadRequest, err.Error())
		return
	}
	// `dismiss` (and any non-recording signal) is accepted but writes NO reward — it
	// is prompt-fatigue analytics, never a training label. Return ok so the client's
	// unconditional POST succeeds; nothing lands in the ledger.
	if !record {
		c.ResponseOk(routingRewardResult{RequestId: requestId, Recorded: false})
		return
	}
	// Training opt-in enforced HERE (server-side): clients send feedback
	// unconditionally, but a reward is recorded as a training label only when the
	// caller's org has not opted out. An opted-out org's feedback is accepted, dropped.
	if !trainingOptedIn(user.Owner) {
		c.ResponseOk(routingRewardResult{RequestId: requestId, Recorded: false})
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
	// Online loop: forward the joined (features, arm, reward) to the engine so
	// per-user LinUCB theta updates in-process. Fire-and-forget, off the response
	// path — a missed update never fails the reward write (the offline rewarded
	// ledger still captured it).
	forwardRewardToEngine(user.Owner, requestId, reward)
	c.ResponseOk(routingRewardResult{RequestId: requestId, Reward: reward, Recorded: true})
}

// forwardRewardToEngine reads the rewarded event's (features, routed arm) and posts
// them with the reward to the engine's /route/observe endpoint, driving the online
// LinUCB update. Indirected through a var so tests can observe the forward without a
// live engine. No-op when the engine endpoint is unconfigured or the event carries no
// features (an auto/heuristic row with no engine vector, or a cold engine).
var forwardRewardToEngine = func(org, requestId string, reward float64) {
	cfg := GetModelConfig()
	if cfg == nil {
		return
	}
	cli := cfg.RouterClient(nil)
	if cli.Endpoint == "" {
		return
	}
	go func() {
		ev, err := object.GetRoutingEventByRequestId(org, requestId)
		if err != nil || ev == nil || ev.RoutedModel == "" || ev.Features == "" {
			return
		}
		var feats []float64
		if json.Unmarshal([]byte(ev.Features), &feats) != nil || len(feats) == 0 {
			return
		}
		cli.Observe(context.Background(), router.ObserveRequest{
			User:     ev.User,
			Org:      org,
			Features: feats,
			Model:    ev.RoutedModel,
			Reward:   reward,
		})
	}()
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

// The rewarded-tuple export (GET /v1/router/rewards) is served ZAP-native — the ONE
// implementation — by zapExportRoutingRewardsHandler in zap_router-policy-stats.go
// (same super-admin-OR-ROUTER_ADMIN_TOKEN gate, same getRewardedRoutingEvents +
// writeRoutingRewardsJSONL below). No controller twin.

// writeRoutingRewardsJSONL streams rewarded events as JSONL, one training tuple
// per line. Pure in its inputs, so the export contract is unit-testable without a
// DB or router context. A write error stops the stream (a truncated download beats
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
