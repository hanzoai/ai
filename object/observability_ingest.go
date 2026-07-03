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

// Ingestion consumer — the native port of Langfuse worker's IngestionService.
//
// IngestBatch is the API cloud's thin /v1/traces (+ /v1/evals ingest) handler
// DELEGATES to: it accepts a Langfuse-compatible batch of {type,id,body} events
// (trace-create, observation/generation/span/event-create, score-create) and
// funnels each into the SAME async, drop-safe writer the in-process emitter uses.
// No Redis/BullMQ — the Langfuse worker's queue is replaced by the in-process Go
// channel + flush goroutine (object/observability.go).
//
// SECURITY — tenant isolation on ingest. The org is an ARGUMENT (the verified
// principal's org, resolved by cloud's handler), and it is FORCED onto every
// event. Any projectId/org inside the event body is IGNORED. This is the exact
// cross-tenant hole an attacker probes ("ingest a trace under projectId=victim")
// — closed here: a caller can only ever write into their own org's stream.
package object

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Langfuse ingestion event types (the subset the AI observability store accepts).
const (
	EventTraceCreate      = "trace-create"
	EventObservationCreate = "observation-create"
	EventObservationUpdate = "observation-update"
	EventGenerationCreate = "generation-create"
	EventGenerationUpdate = "generation-update"
	EventSpanCreate       = "span-create"
	EventSpanUpdate       = "span-update"
	EventEventCreate      = "event-create"
	EventScoreCreate      = "score-create"
)

// IngestEvent is one Langfuse-shaped ingestion record. Body is the entity payload
// whose shape depends on Type.
type IngestEvent struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Timestamp string          `json:"timestamp"`
	Body      json.RawMessage `json:"body"`
}

// IngestBatchRequest is the Langfuse-compatible wire body: {"batch": [...]}.
type IngestBatchRequest struct {
	Batch []IngestEvent `json:"batch"`
}

// IngestResult mirrors Langfuse's 207 response: per-event successes + errors.
type IngestResult struct {
	Successes []IngestSuccess `json:"successes"`
	Errors    []IngestError   `json:"errors"`
}

type IngestSuccess struct {
	ID     string `json:"id"`
	Status int    `json:"status"`
}

type IngestError struct {
	ID      string `json:"id"`
	Status  int    `json:"status"`
	Message string `json:"message"`
}

// IngestBatch processes a Langfuse-shaped batch under the SERVER-VERIFIED org.
// Every event is emitted into org (never a body-supplied project). Returns a
// per-event success/error result; a bad single event never fails the batch.
func IngestBatch(org string, events []IngestEvent) IngestResult {
	res := IngestResult{Successes: []IngestSuccess{}, Errors: []IngestError{}}
	org = strings.TrimSpace(org)
	if org == "" {
		// No verified tenant → refuse the whole batch (never write unscoped).
		for _, e := range events {
			res.Errors = append(res.Errors, IngestError{ID: e.ID, Status: 403, Message: "org required"})
		}
		return res
	}
	for _, e := range events {
		if err := ingestOne(org, e); err != nil {
			res.Errors = append(res.Errors, IngestError{ID: e.ID, Status: 400, Message: err.Error()})
			continue
		}
		res.Successes = append(res.Successes, IngestSuccess{ID: e.ID, Status: 201})
	}
	return res
}

func ingestOne(org string, e IngestEvent) error {
	switch e.Type {
	case EventTraceCreate:
		ev, err := parseTraceBody(org, e.Body)
		if err != nil {
			return err
		}
		EmitTraceOnly(ev)
	case EventObservationCreate, EventObservationUpdate, EventGenerationCreate,
		EventGenerationUpdate, EventSpanCreate, EventSpanUpdate, EventEventCreate:
		ev, err := parseObservationBody(org, e.Type, e.Body)
		if err != nil {
			return err
		}
		EmitObservation(ev)
	case EventScoreCreate:
		sc, err := parseScoreBody(org, e.Body)
		if err != nil {
			return err
		}
		EmitScore(sc)
	default:
		return fmt.Errorf("unsupported event type %q", e.Type)
	}
	return nil
}

// ── Body parsers ──────────────────────────────────────────────────────────────

type traceBody struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	UserID    string            `json:"userId"`
	SessionID string            `json:"sessionId"`
	Input     json.RawMessage   `json:"input"`
	Output    json.RawMessage   `json:"output"`
	Metadata  map[string]string `json:"metadata"`
	Tags      []string          `json:"tags"`
	Timestamp string            `json:"timestamp"`
}

func parseTraceBody(org string, raw json.RawMessage) (TraceEvent, error) {
	var b traceBody
	if err := json.Unmarshal(raw, &b); err != nil {
		return TraceEvent{}, fmt.Errorf("invalid trace body: %w", err)
	}
	if b.ID == "" {
		return TraceEvent{}, fmt.Errorf("trace body missing id")
	}
	return TraceEvent{
		Org:        org, // FORCED — body cannot select a tenant
		TraceRowID: b.ID,
		Name:       b.Name,
		User:       b.UserID,
		SessionID:  b.SessionID,
		Input:      rawToText(b.Input),
		Output:     rawToText(b.Output),
		Metadata:   b.Metadata,
		Tags:       b.Tags,
		StartTime:  parseIngestTime(b.Timestamp),
	}, nil
}

type observationBody struct {
	ID                  string          `json:"id"`
	TraceID             string          `json:"traceId"`
	Type                string          `json:"type"`
	Name                string          `json:"name"`
	StartTime           string          `json:"startTime"`
	EndTime             string          `json:"endTime"`
	CompletionStartTime string          `json:"completionStartTime"`
	Model               string          `json:"model"`
	ModelParameters     map[string]any  `json:"modelParameters"`
	Input               json.RawMessage `json:"input"`
	Output              json.RawMessage `json:"output"`
	Level               string          `json:"level"`
	StatusMessage       string          `json:"statusMessage"`
	ParentObservationID string          `json:"parentObservationId"`
	Metadata            map[string]string `json:"metadata"`
	Usage               struct {
		Input            int `json:"input"`
		Output           int `json:"output"`
		Total            int `json:"total"`
		PromptTokens     int `json:"promptTokens"`
		CompletionTokens int `json:"completionTokens"`
		TotalTokens      int `json:"totalTokens"`
	} `json:"usage"`
	UsageDetails map[string]int `json:"usageDetails"`
}

func parseObservationBody(org, eventType string, raw json.RawMessage) (TraceEvent, error) {
	var b observationBody
	if err := json.Unmarshal(raw, &b); err != nil {
		return TraceEvent{}, fmt.Errorf("invalid observation body: %w", err)
	}
	if b.ID == "" || b.TraceID == "" {
		return TraceEvent{}, fmt.Errorf("observation body needs id and traceId")
	}

	prompt := firstNonZero(b.Usage.Input, b.Usage.PromptTokens, b.UsageDetails["input"])
	completion := firstNonZero(b.Usage.Output, b.Usage.CompletionTokens, b.UsageDetails["output"])
	total := firstNonZero(b.Usage.Total, b.Usage.TotalTokens, b.UsageDetails["total"])
	if total == 0 {
		total = prompt + completion
	}

	status := "success"
	if strings.EqualFold(b.Level, "error") || b.StatusMessage != "" {
		status = "error"
	}

	ev := TraceEvent{
		Org:              org, // FORCED
		TraceRowID:       b.TraceID,
		ObsRowID:         b.ID,
		ObsType:          normalizeObsType(b.Type, eventType),
		Name:             b.Name,
		Model:            b.Model,
		Params:           b.ModelParameters,
		Input:            rawToText(b.Input),
		Output:           rawToText(b.Output),
		Metadata:         b.Metadata,
		PromptTokens:     prompt,
		CompletionTokens: completion,
		TotalTokens:      total,
		StartTime:        parseIngestTime(b.StartTime),
		EndTime:          parseIngestTime(b.EndTime),
		Status:           status,
		ErrorMsg:         b.StatusMessage,
	}
	ev.WithCompletionStart(parseIngestTime(b.CompletionStartTime))
	return ev, nil
}

type scoreBody struct {
	ID            string  `json:"id"`
	TraceID       string  `json:"traceId"`
	ObservationID string  `json:"observationId"`
	Name          string  `json:"name"`
	Value         float64 `json:"value"`
	StringValue   string  `json:"stringValue"`
	DataType      string  `json:"dataType"`
	Comment       string  `json:"comment"`
}

func parseScoreBody(org string, raw json.RawMessage) (ScoreEvent, error) {
	var b scoreBody
	if err := json.Unmarshal(raw, &b); err != nil {
		return ScoreEvent{}, fmt.Errorf("invalid score body: %w", err)
	}
	if b.TraceID == "" || b.Name == "" {
		return ScoreEvent{}, fmt.Errorf("score body needs traceId and name")
	}
	return ScoreEvent{
		ID:            b.ID,
		Org:           org, // FORCED
		TraceID:       b.TraceID,
		ObservationID: b.ObservationID,
		Name:          b.Name,
		Value:         b.Value,
		StringValue:   b.StringValue,
		DataType:      strings.ToUpper(strings.TrimSpace(b.DataType)),
		Source:        ScoreSourceAPI,
		Comment:       b.Comment,
	}, nil
}

// ── helpers ─────────────────────────────────────────────────────────────────

// normalizeObsType resolves the observation type from the explicit body.type, else
// the event-type prefix (generation-* → GENERATION, span-* → SPAN, event-* → EVENT).
func normalizeObsType(bodyType, eventType string) string {
	if t := strings.ToUpper(strings.TrimSpace(bodyType)); t != "" {
		return t
	}
	switch {
	case strings.HasPrefix(eventType, "span"):
		return ObservationSpan
	case strings.HasPrefix(eventType, "event"):
		return ObservationEvent
	default:
		return ObservationGeneration
	}
}

// rawToText renders a JSON input/output value as text: a JSON string decodes to
// its content, any other JSON keeps its compact encoding.
func rawToText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}

func parseIngestTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func firstNonZero(vals ...int) int {
	for _, v := range vals {
		if v != 0 {
			return v
		}
	}
	return 0
}
