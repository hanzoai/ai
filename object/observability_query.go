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

// Query API — the org-scoped reads cloud's GET /v1/evals/traces (+ observations,
// scores) DELEGATE to. Every read is a native port of Langfuse's repository layer
// hardened for multi-tenant OLAP:
//
//   - Org is MANDATORY and bound as a NAMED PARAMETER ({org:String}) — never
//     string-interpolated. A crafted org/trace can't inject ClickHouse SQL.
//   - project_id (org) is the FIRST ORDER BY key, so a tenant read is a prefix
//     scan, and no query can return another tenant's rows.
//   - Every read carries a bounded LIMIT (no full-table OLAP-scan DoS).
package object

import (
	"context"
	"fmt"
	"strings"
	"time"

	clickhouse "github.com/ClickHouse/clickhouse-go/v2"
)

const (
	obsQueryDefaultLimit = 50
	obsQueryMaxLimit     = 500
)

// TraceFilter / ObservationFilter / ScoreFilter bound a read. Org is REQUIRED.
type TraceFilter struct {
	Org       string
	UserID    string
	SessionID string
	Name      string
	Limit     int
}

type ObservationFilter struct {
	Org     string
	TraceID string
	Type    string
	Model   string
	Limit   int
}

type ScoreFilter struct {
	Org     string
	TraceID string
	Name    string
	Limit   int
}

// TraceView / ObservationView / ScoreView are the read projections returned to
// cloud's handlers (JSON-friendly, Langfuse-shaped field names).
type TraceView struct {
	ID        string            `json:"id"`
	Timestamp time.Time         `json:"timestamp"`
	Name      string            `json:"name"`
	UserID    string            `json:"userId"`
	SessionID string            `json:"sessionId"`
	Input     string            `json:"input"`
	Output    string            `json:"output"`
	Tags      []string          `json:"tags"`
	Metadata  map[string]string `json:"metadata"`
}

type ObservationView struct {
	ID                  string         `json:"id"`
	TraceID             string         `json:"traceId"`
	Type                string         `json:"type"`
	Name                string         `json:"name"`
	Model               string         `json:"model"`
	StartTime           time.Time      `json:"startTime"`
	EndTime             time.Time      `json:"endTime"`
	CompletionStartTime *time.Time     `json:"completionStartTime,omitempty"`
	Level               string         `json:"level"`
	StatusMessage       string         `json:"statusMessage"`
	Input               string         `json:"input"`
	Output              string         `json:"output"`
	Usage               map[string]uint64 `json:"usage"`
	Latency             float64        `json:"latencyMs"`
}

type ScoreView struct {
	ID            string    `json:"id"`
	Timestamp     time.Time `json:"timestamp"`
	TraceID       string    `json:"traceId"`
	ObservationID string    `json:"observationId"`
	Name          string    `json:"name"`
	Value         float64   `json:"value"`
	StringValue   string    `json:"stringValue"`
	DataType      string    `json:"dataType"`
	Source        string    `json:"source"`
	Comment       string    `json:"comment"`
}

// QueryTraces returns the org's traces, newest first, bounded.
func QueryTraces(ctx context.Context, f TraceFilter) ([]TraceView, error) {
	if strings.TrimSpace(f.Org) == "" {
		return nil, fmt.Errorf("traces read requires org")
	}
	if !DatastoreEnabled() {
		return nil, fmt.Errorf("trace store unavailable")
	}
	q := "SELECT id, timestamp, name, user_id, session_id, input, output, tags, metadata FROM " +
		tracesTable + " WHERE project_id = {org:String}"
	args := []any{clickhouse.Named("org", f.Org)}
	q, args = appendEq(q, args, "user_id", "uid", f.UserID)
	q, args = appendEq(q, args, "session_id", "sid", f.SessionID)
	q, args = appendEq(q, args, "name", "nm", f.Name)
	q += " ORDER BY timestamp DESC LIMIT {lim:UInt32}"
	args = append(args, clickhouse.Named("lim", uint32(boundedObsLimit(f.Limit))))

	rows, err := DatastoreQuery(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	out := make([]TraceView, 0, len(rows))
	for _, r := range rows {
		out = append(out, TraceView{
			ID:        cuString(r["id"]),
			Timestamp: cuTime(r["timestamp"]),
			Name:      cuString(r["name"]),
			UserID:    cuString(r["user_id"]),
			SessionID: cuString(r["session_id"]),
			Input:     cuString(r["input"]),
			Output:    cuString(r["output"]),
			Tags:      cuStringSlice(r["tags"]),
			Metadata:  cuStringMap(r["metadata"]),
		})
	}
	return out, nil
}

// QueryObservations returns the org's observations (optionally one trace), bounded.
func QueryObservations(ctx context.Context, f ObservationFilter) ([]ObservationView, error) {
	if strings.TrimSpace(f.Org) == "" {
		return nil, fmt.Errorf("observations read requires org")
	}
	if !DatastoreEnabled() {
		return nil, fmt.Errorf("trace store unavailable")
	}
	q := "SELECT id, trace_id, type, name, provided_model_name, start_time, end_time, " +
		"completion_start_time, level, status_message, input, output, usage_details FROM " +
		observationsTable + " WHERE project_id = {org:String}"
	args := []any{clickhouse.Named("org", f.Org)}
	q, args = appendEq(q, args, "trace_id", "tid", f.TraceID)
	q, args = appendEq(q, args, "type", "ty", f.Type)
	q, args = appendEq(q, args, "provided_model_name", "mdl", f.Model)
	q += " ORDER BY start_time DESC LIMIT {lim:UInt32}"
	args = append(args, clickhouse.Named("lim", uint32(boundedObsLimit(f.Limit))))

	rows, err := DatastoreQuery(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	out := make([]ObservationView, 0, len(rows))
	for _, r := range rows {
		start := cuTime(r["start_time"])
		end := cuTime(r["end_time"])
		view := ObservationView{
			ID:            cuString(r["id"]),
			TraceID:       cuString(r["trace_id"]),
			Type:          cuString(r["type"]),
			Name:          cuString(r["name"]),
			Model:         cuString(r["provided_model_name"]),
			StartTime:     start,
			EndTime:       end,
			Level:         cuString(r["level"]),
			StatusMessage: cuString(r["status_message"]),
			Input:         cuString(r["input"]),
			Output:        cuString(r["output"]),
			Usage:         cuUint64Map(r["usage_details"]),
		}
		if cs := cuTime(r["completion_start_time"]); !cs.IsZero() {
			view.CompletionStartTime = &cs
		}
		if !start.IsZero() && !end.IsZero() {
			view.Latency = float64(end.Sub(start).Milliseconds())
		}
		out = append(out, view)
	}
	return out, nil
}

// QueryScores returns the org's scores, newest first, bounded.
func QueryScores(ctx context.Context, f ScoreFilter) ([]ScoreView, error) {
	if strings.TrimSpace(f.Org) == "" {
		return nil, fmt.Errorf("scores read requires org")
	}
	if !DatastoreEnabled() {
		return nil, fmt.Errorf("trace store unavailable")
	}
	q := "SELECT id, timestamp, trace_id, observation_id, name, value, string_value, data_type, source, comment FROM " +
		scoresTable + " WHERE project_id = {org:String}"
	args := []any{clickhouse.Named("org", f.Org)}
	q, args = appendEq(q, args, "trace_id", "tid", f.TraceID)
	q, args = appendEq(q, args, "name", "nm", f.Name)
	q += " ORDER BY timestamp DESC LIMIT {lim:UInt32}"
	args = append(args, clickhouse.Named("lim", uint32(boundedObsLimit(f.Limit))))

	rows, err := DatastoreQuery(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	out := make([]ScoreView, 0, len(rows))
	for _, r := range rows {
		out = append(out, ScoreView{
			ID:            cuString(r["id"]),
			Timestamp:     cuTime(r["timestamp"]),
			TraceID:       cuString(r["trace_id"]),
			ObservationID: cuString(r["observation_id"]),
			Name:          cuString(r["name"]),
			Value:         cuFloat(r["value"]),
			StringValue:   cuString(r["string_value"]),
			DataType:      cuString(r["data_type"]),
			Source:        cuString(r["source"]),
			Comment:       cuString(r["comment"]),
		})
	}
	return out, nil
}

const scoresTable = "hanzo.scores"

// appendEq adds " AND col = {param:String}" with a bound value when v is non-empty.
// The column name is a compile-time constant (never user input); only the VALUE is
// bound — so no injection surface is opened.
func appendEq(q string, args []any, col, param, v string) (string, []any) {
	if strings.TrimSpace(v) == "" {
		return q, args
	}
	q += fmt.Sprintf(" AND %s = {%s:String}", col, param)
	args = append(args, clickhouse.Named(param, v))
	return q, args
}

func boundedObsLimit(n int) int {
	if n <= 0 {
		return obsQueryDefaultLimit
	}
	if n > obsQueryMaxLimit {
		return obsQueryMaxLimit
	}
	return n
}

// ── ClickHouse scan-type coercers (complement cloud_usage.go's cuInt64/cuString) ──

func cuFloat(v interface{}) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	default:
		return float64(cuInt64(v))
	}
}

func cuStringSlice(v interface{}) []string {
	if s, ok := v.([]string); ok {
		return s
	}
	return []string{}
}

func cuStringMap(v interface{}) map[string]string {
	if m, ok := v.(map[string]string); ok {
		return m
	}
	return map[string]string{}
}

func cuUint64Map(v interface{}) map[string]uint64 {
	if m, ok := v.(map[string]uint64); ok {
		return m
	}
	return map[string]uint64{}
}
