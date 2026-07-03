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

// Scores — the third leg of the Langfuse-v3 observability model (traces +
// observations + scores). A score is an append-only judgment attached to a trace
// (and optionally a specific observation): an LLM-as-judge verdict, a human
// annotation, or an API-submitted metric. Scores flow through the SAME async,
// drop-safe writer as traces/observations (one worker, one buffer) into
// hanzo.scores, and are org-scoped server-side exactly like traces.
package object

import (
	"crypto/rand"
	"encoding/hex"
	"math"
	"strings"
	"time"
)

// Score data types (Langfuse). NUMERIC carries Value; CATEGORICAL/BOOLEAN carry
// StringValue (BOOLEAN is a NUMERIC constrained to {0,1} with a "True"/"False"
// label). Same string values as the judge's JudgeNumeric/… so a judge verdict
// maps 1:1.
const (
	ScoreNumeric     = "NUMERIC"
	ScoreCategorical = "CATEGORICAL"
	ScoreBoolean     = "BOOLEAN"

	// Score sources (Langfuse `source`): who produced the score.
	ScoreSourceAPI        = "API"
	ScoreSourceEval       = "EVAL"
	ScoreSourceAnnotation = "ANNOTATION"
)

// ScoreEvent is one score to persist. Org (project_id) MUST be the server-verified
// tenant; an empty org is dropped (fail-secure). ID is generated when empty.
type ScoreEvent struct {
	ID            string
	Org           string // project_id (tenant) — server-verified, never a client value
	TraceID       string
	ObservationID string
	Name          string
	Value         float64
	StringValue   string
	DataType      string // NUMERIC | CATEGORICAL | BOOLEAN
	Source        string // API | EVAL | ANNOTATION
	Comment       string
	AuthorUserID  string
	ConfigID      string
	Timestamp     time.Time
}

// EmitScore hands one score to the async writer (non-blocking, drop-safe). Rejects
// an unscoped (no org) or non-finite score rather than persist a bad row.
func EmitScore(sc ScoreEvent) {
	sc.Org = strings.TrimSpace(sc.Org)
	if sc.Org == "" {
		aiTraceEvents.WithLabelValues("unscoped").Inc()
		return
	}
	if !finiteScore(sc.Value) {
		aiTraceEvents.WithLabelValues("invalid").Inc()
		return
	}
	sc.Comment = truncateTraceContent(sc.Comment, obsMaxContent)
	enqueueObs(obsItem{kind: kindScore, score: sc})
}

// Langfuse scores schema, verbatim (unclustered variant), in the hanzo db.
const scoresTableDDL = `
	CREATE TABLE IF NOT EXISTS hanzo.scores (
		id String,
		timestamp DateTime64(3),
		project_id String,
		trace_id String,
		observation_id Nullable(String),
		name String,
		value Float64,
		source String,
		comment Nullable(String) CODEC(ZSTD(1)),
		author_user_id Nullable(String),
		config_id Nullable(String),
		data_type String,
		string_value Nullable(String),
		queue_id Nullable(String),
		created_at DateTime64(3) DEFAULT now(),
		updated_at DateTime64(3) DEFAULT now(),
		event_ts DateTime64(3),
		is_deleted UInt8
	) ENGINE = ReplacingMergeTree(event_ts, is_deleted)
	PARTITION BY toYYYYMM(timestamp)
	PRIMARY KEY (project_id, toDate(timestamp), name)
	ORDER BY (project_id, toDate(timestamp), name, id)
	TTL toDateTime(timestamp) + INTERVAL 2 YEAR`

// scoresInsert is the explicit column list, matching scoreRow() order exactly.
const scoresInsert = "INSERT INTO hanzo.scores " +
	"(id, timestamp, project_id, trace_id, observation_id, name, value, source, " +
	"comment, author_user_id, config_id, data_type, string_value, queue_id, " +
	"event_ts, is_deleted)"

// scoreRow returns the hanzo.scores column values in scoresInsert order.
func (sc *ScoreEvent) scoreRow() []any {
	now := time.Now().UTC()
	ts := sc.Timestamp.UTC()
	if ts.IsZero() {
		ts = now
	}
	dt := sc.DataType
	if dt == "" {
		dt = ScoreNumeric
	}
	src := sc.Source
	if src == "" {
		src = ScoreSourceAPI
	}
	return []any{
		scoreOrNewID(sc.ID),          // id
		ts,                           // timestamp
		sc.Org,                       // project_id (tenant)
		sc.TraceID,                   // trace_id
		nullString(sc.ObservationID), // observation_id
		sc.Name,                      // name
		sc.Value,                     // value
		src,                          // source
		nullString(sc.Comment),       // comment
		nullString(sc.AuthorUserID),  // author_user_id
		nullString(sc.ConfigID),      // config_id
		dt,                           // data_type
		nullString(sc.StringValue),   // string_value
		(*string)(nil),               // queue_id
		now,                          // event_ts
		uint8(0),                     // is_deleted
	}
}

func finiteScore(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

func scoreOrNewID(id string) string {
	if strings.TrimSpace(id) != "" {
		return id
	}
	return "score-" + randHex(16)
}

// randHex returns n random bytes hex-encoded. Used for server-side score ids.
func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b)
}
