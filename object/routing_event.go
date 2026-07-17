// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package object

import (
	"time"

	"github.com/hanzoai/ai/util"
	"github.com/hanzoai/dbx"
)

// RoutingEvent is a privacy-preserving record of one `auto`/`zen-router`
// resolution: which task the request was classified as and which concrete model
// it was routed to, plus the confidence and whether the decision came from the
// engine or the local heuristic. It NEVER stores prompt text or any hash of it —
// the only content-derived value it may carry is the engine's opaque feature
// vector (Features, serialized JSON, nullable), which is the frozen-backbone
// embedding produced at inference time, not the prompt. This is the ledger that
// feeds router data-refinement and heads-fit training (see
// universe/docs/architecture/personal-router-training.md).
//
// RequestId ties the decision to the request the client sees — the response
// `chatcmpl-<id>` and the usage ledger's request_id — so an outcome Reward (0..1,
// with RewardedTime the nullable "scored yet?" signal) can be joined back to the
// event's (features, routed model). That triple is the enso loop's per-request
// training label. No field ever holds prompt text.
type RoutingEvent struct {
	Id               string  `db:"pk" json:"id"`
	CreatedTime      string  `json:"createdTime"`
	Owner            string  `json:"owner"`     // org
	User             string  `json:"user"`      // owner/name of the caller, "" if unauthenticated
	RequestId        string  `json:"requestId"` // response/usage-ledger request id — the reward join key
	Task             string  `json:"task"`
	RequestedModel   string  `json:"requestedModel"` // the virtual alias ("auto") or the family SKU ("enso")
	RoutedModel      string  `json:"routedModel"`    // the concrete model/arm that served
	Confidence       float64 `json:"confidence"`
	Source           string  `json:"source"`      // "engine" | "heuristic" | "family"
	Features         string  `json:"features"`    // serialized JSON array; "" when the engine gave none
	ShadowModel      string  `json:"shadowModel"` // engine's counterfactual pick for a family call; "" if none
	PromptTokens     int     `json:"promptTokens"`
	CompletionTokens int     `json:"completionTokens"`
	CostCents        int64   `json:"costCents"`
	LatencyMs        int64   `json:"latencyMs"`
	Reward           float64 `json:"reward"`       // outcome signal 0..1; meaningful only when RewardedTime != ""
	RewardedTime     string  `json:"rewardedTime"` // RFC3339 when scored; "" = not yet scored (the nullable signal)
}

// AddRoutingEvent persists a routing decision. It fills in the id and created
// time when unset. Callers invoke this best-effort (off the request hot path) —
// a nil adapter or insert error must never surface to the chat request, so the
// error is returned for logging but the caller ignores it.
func AddRoutingEvent(e *RoutingEvent) error {
	if adapter == nil || adapter.db == nil {
		return nil
	}
	if e.Id == "" {
		e.Id = util.GenerateId()
	}
	if e.CreatedTime == "" {
		e.CreatedTime = time.Now().UTC().Format(time.RFC3339)
	}
	return insertRow(adapter.db, e)
}

// GetRoutingEvents returns routing events for training export, oldest first,
// optionally filtered by org (owner) and a since timestamp (RFC3339; events with
// created_time >= since). Empty filters return all events.
func GetRoutingEvents(org, since string) ([]*RoutingEvent, error) {
	if adapter == nil || adapter.db == nil {
		return nil, nil
	}
	var where []dbx.Expression
	if org != "" {
		where = append(where, dbx.HashExp{"owner": org})
	}
	if since != "" {
		where = append(where, dbx.NewExp("created_time >= {:since}", dbx.Params{"since": since}))
	}

	events := []*RoutingEvent{}
	var expr dbx.Expression
	switch len(where) {
	case 0:
		expr = nil
	case 1:
		expr = where[0]
	default:
		expr = dbx.And(where...)
	}
	err := findAll(adapter.db, "routing_event", &events, expr, "created_time ASC")
	if err != nil {
		return events, err
	}
	return events, nil
}

// AttachRoutingReward records an outcome reward (0..1) on the routing event that
// served request requestId for org — the per-request quality label the enso loop
// pairs with the event's (features, routed model). It is scoped to org: a
// requestId owned by another org, or unknown, matches no row, so found is false
// and the caller returns 404 without revealing cross-org existence. Idempotent —
// a repeat overwrites the reward and advances rewarded_time. It touches no prompt
// text (the ledger holds none).
func AttachRoutingReward(org, requestId string, reward float64) (found bool, err error) {
	if adapter == nil || adapter.db == nil {
		return false, nil
	}
	where := dbx.HashExp{"owner": org, "request_id": requestId}
	n, err := countWhere(adapter.db, "routing_event", where)
	if err != nil || n == 0 {
		return false, err
	}
	if _, err = updateCols(adapter.db, "routing_event", where, dbx.Params{
		"reward":        reward,
		"rewarded_time": time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return false, err
	}
	return true, nil
}

// GetRoutingEventByRequestId returns the routing event for (org, requestId), or nil
// when none matches (unknown or cross-org — the caller reveals nothing either way).
// It backs the online-learning forward: after a reward lands, ai reads the event's
// (features, routed model) here and forwards them with the reward to the engine's
// observe endpoint so per-user LinUCB theta updates in-process. Most recent wins.
func GetRoutingEventByRequestId(org, requestId string) (*RoutingEvent, error) {
	if adapter == nil || adapter.db == nil {
		return nil, nil
	}
	events := []*RoutingEvent{}
	err := findAll(adapter.db, "routing_event", &events,
		dbx.HashExp{"owner": org, "request_id": requestId}, "created_time DESC")
	if err != nil || len(events) == 0 {
		return nil, err
	}
	return events[0], nil
}

// GetRewardedRoutingEvents returns routing events that carry a reward
// (rewarded_time set), oldest first, optionally scoped to org (owner) and a since
// timestamp (created_time >= since). These are the labeled tuples — (features,
// routed model, reward) — the enso loop trains on; empty filters return all
// rewarded events.
func GetRewardedRoutingEvents(org, since string) ([]*RoutingEvent, error) {
	if adapter == nil || adapter.db == nil {
		return nil, nil
	}
	where := []dbx.Expression{dbx.NewExp("rewarded_time <> ''")}
	if org != "" {
		where = append(where, dbx.HashExp{"owner": org})
	}
	if since != "" {
		where = append(where, dbx.NewExp("created_time >= {:since}", dbx.Params{"since": since}))
	}
	events := []*RoutingEvent{}
	err := findAll(adapter.db, "routing_event", &events, dbx.And(where...), "created_time ASC")
	return events, err
}

// DeleteRoutingEvents removes ALL routing events for an org — the org-scoped
// right-to-be-forgotten path behind /v1/delete-my-routing-data. The rows are
// content-free (features + reward, never prompt text), but ownership completeness
// means an org can take its data back. Never a blanket delete: an empty org
// deletes nothing. Returns the number of rows removed.
func DeleteRoutingEvents(org string) (int64, error) {
	if adapter == nil || adapter.db == nil || org == "" {
		return 0, nil
	}
	return deleteWhere(adapter.db, "routing_event", dbx.HashExp{"owner": org})
}

// GetRewardedRoutingEventsForOwners is GetRewardedRoutingEvents scoped to a SET of
// owners (rewarded rows whose owner ∈ owners), oldest first. It is the
// consent-respecting read for the shared "*" base fit: the trainer passes the
// orgs that opted in (ListTrainingContributorOrgs) plus the reserved internal
// orgs, so the cross-org base learns ONLY from data it is allowed to. An empty
// owner set returns no rows (fail-closed: no consent → no training data), never
// all rows.
func GetRewardedRoutingEventsForOwners(owners []string, since string) ([]*RoutingEvent, error) {
	if adapter == nil || adapter.db == nil {
		return nil, nil
	}
	if len(owners) == 0 {
		return nil, nil
	}
	vals := make([]interface{}, len(owners))
	for i, o := range owners {
		vals[i] = o
	}
	where := []dbx.Expression{
		dbx.NewExp("rewarded_time <> ''"),
		dbx.In("owner", vals...),
	}
	if since != "" {
		where = append(where, dbx.NewExp("created_time >= {:since}", dbx.Params{"since": since}))
	}
	events := []*RoutingEvent{}
	err := findAll(adapter.db, "routing_event", &events, dbx.And(where...), "created_time ASC")
	return events, err
}
