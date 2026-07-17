// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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

	"github.com/hanzoai/dbx"

	"github.com/hanzoai/ai/util"
)

// RouterTrainingLog is the APPEND-ONLY history of retrain runs — one immutable row
// per 4:20am spark fit. RouterArtifactMeta keeps only the LATEST outcome per scope
// (upsert, one row/owner); this log keeps the whole TIMELINE so the world.hanzo.ai
// "Model Improvement" panel can plot the flywheel getting smarter over time (each
// fit's holdout metric + gate verdict, with version markers). Like RouterArtifactMeta
// it carries NO weights and NO event content — only the version tag, the train time,
// coverage count, and the gate verdict/metric — so it is safe on the public surface.
// Rows are never mutated or deleted; the timeline is exactly what happened.
type RouterTrainingLog struct {
	Id         string `db:"pk" json:"id"`
	Owner      string `json:"owner"`      // scope: "*" = shared base heads, else org id
	LoggedTime string `json:"loggedTime"` // RFC3339 when this row was appended

	Version     string  `json:"version"`     // artifact version tag
	TrainedTime string  `json:"trainedTime"` // RFC3339 when the fit ran
	Events      int     `json:"events"`      // rows the fit trained on (coverage, not content)
	GatePassed  bool    `json:"gatePassed"`
	Published   bool    `json:"published"`
	GateKind    string  `json:"gateKind"`   // "routerbench" | "holdout"
	GateMetric  string  `json:"gateMetric"` // e.g. "AIQ" | "holdout_reward" | "holdout_accuracy"
	GateValue   float64 `json:"gateValue"`  // the new artifact's metric
	GateBase    float64 `json:"gateBase"`   // the incumbent's metric it had to beat/match
	Note        string  `json:"note"`
}

// AppendRouterTrainingLog inserts one immutable retrain-outcome row. Stamps Id +
// LoggedTime when unset. Best-effort at the callsite: a nil adapter or insert error
// is returned for logging but must never fail the write it rides along with (the
// upsert of the latest RouterArtifactMeta is the source of truth for "current").
func AppendRouterTrainingLog(row *RouterTrainingLog) error {
	if adapter == nil || adapter.db == nil {
		return nil
	}
	if row.Id == "" {
		row.Id = util.GenerateId()
	}
	if row.LoggedTime == "" {
		row.LoggedTime = time.Now().UTC().Format(time.RFC3339)
	}
	return insertRow(adapter.db, row)
}

// ListRouterTrainingLog returns retrain-log rows for a scope, oldest first,
// optionally filtered by a since timestamp (trained_time >= since) and capped at
// limit (<= 0 = uncapped). Empty owner returns all scopes' rows. The retrain
// timeline the history endpoint plots.
func ListRouterTrainingLog(owner, since string, limit int) ([]*RouterTrainingLog, error) {
	if adapter == nil || adapter.db == nil {
		return nil, nil
	}
	var where []dbx.Expression
	if owner != "" {
		where = append(where, dbx.HashExp{"owner": owner})
	}
	if since != "" {
		where = append(where, dbx.NewExp("trained_time >= {:since}", dbx.Params{"since": since}))
	}
	var expr dbx.Expression
	switch len(where) {
	case 0:
		expr = nil
	case 1:
		expr = where[0]
	default:
		expr = dbx.And(where...)
	}
	rows := []*RouterTrainingLog{}
	if err := findAll(adapter.db, "router_training_log", &rows, expr, "trained_time ASC"); err != nil {
		return rows, err
	}
	// Keep the MOST RECENT `limit` when capped (the timeline reads oldest→newest, so
	// trim from the front) — a bounded, still-chronological window.
	if limit > 0 && len(rows) > limit {
		rows = rows[len(rows)-limit:]
	}
	return rows, nil
}
