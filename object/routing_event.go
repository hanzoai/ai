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
type RoutingEvent struct {
	Id             string  `db:"pk" json:"id"`
	CreatedTime    string  `json:"createdTime"`
	Owner          string  `json:"owner"` // org
	User           string  `json:"user"`  // owner/name of the caller, "" if unauthenticated
	Task           string  `json:"task"`
	RequestedModel string  `json:"requestedModel"` // the virtual alias, e.g. "auto"
	RoutedModel    string  `json:"routedModel"`    // the concrete model that served
	Confidence     float64 `json:"confidence"`
	Source         string  `json:"source"`   // "engine" | "heuristic"
	Features       string  `json:"features"` // serialized JSON array; "" when the engine gave none
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
