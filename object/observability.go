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

// Observability emission — the ONE source that feeds Hanzo's native evals/o11y.
//
// Every AI operation (chat/completions, completions, embeddings, rerank, images,
// audio, and multi-step agent/tool/RAG chains) emits a Langfuse-shaped trace tree
// into the ClickHouse analytics datastore:
//
//	hanzo.traces        — one row per request (the tenant-scoped envelope)
//	hanzo.observations  — one GENERATION span per model call (nested under the trace)
//
// The column shapes are Langfuse's OWN ClickHouse schema, VERBATIM, so the ported
// insights UI (retired Langfuse fork → native evals) renders our data unchanged
// and the evals agent's read/ingestion side needs no translation. project_id is
// the Langfuse tenant column; in Hanzo it is the ORG (the JWT `owner`). It is set
// SERVER-SIDE from the verified principal — never a client-supplied value — so a
// caller can never write into another tenant's trace stream.
//
// This is the SINGLE writer of these two tables (HIP: "one writer per table"). The
// prior "colliding observations write" was removed because it was a SECOND,
// incompatible shape; this replaces it with the agreed Langfuse shape, making the
// ai module the one canonical writer the read side expects.
//
// NON-BLOCKING BY CONSTRUCTION. Instrumentation must never add latency to the
// user's request. EmitTrace does a non-blocking send onto a bounded buffer and
// returns; under backpressure it DROPS (and counts the drop) rather than block.
// Content is truncated at capture (bounds channel memory) and secret-scrubbed in
// the drain goroutine (keeps regex off the hot path). A background writer batches
// the buffer into one ClickHouse block per flush. With no DATASTORE_ADDR the whole
// pipeline is a counted no-op — boot never depends on the warehouse.
package object

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/beego/beego/logs"
	metric "github.com/luxfi/metric"
	"github.com/shopspring/decimal"
)

// ── Trace / observation event model ──────────────────────────────────────────

// Observation types mirror Langfuse. Every model call is a GENERATION; multi-step
// chains attach SPAN/EVENT children under the same trace_id.
const (
	ObservationGeneration = "GENERATION"
	ObservationSpan       = "SPAN"
	ObservationEvent      = "EVENT"

	levelDefault = "DEFAULT"
	levelError   = "ERROR"
)

// TraceEvent is one fully-captured AI operation, ready to persist as a trace row
// plus a generation-observation row. The caller builds it from SERVER-VERIFIED
// values (Org from the authenticated principal). It is a plain value — pure to
// map, safe to buffer.
type TraceEvent struct {
	// Identity / scope — all server-derived.
	TraceID   string // request UUID; the observation links to it
	Org       string // project_id (tenant). MUST be the verified principal's org.
	User      string // "owner/name" end-user subject (Langfuse user_id)
	SessionID string // optional conversation/session id (Langfuse session_id)

	// Envelope.
	Name     string            // operation: "chat.completions" | "embeddings" | …
	Input    string            // prompt / request text (redacted+truncated on flush)
	Output   string            // completion / response text (redacted+truncated)
	Tags     []string          // e.g. [model, provider, status]
	Metadata map[string]string // allowlisted; never headers/tokens/raw IP

	// Timing.
	StartTime          time.Time
	EndTime            time.Time
	CompletionStartAt  time.Time // first-token time (streams) → TTFT
	hasCompletionStart bool

	// Generation (the model call).
	Model            string
	Provider         string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CacheReadTokens  int
	CacheWriteTokens int
	CostCents        int64
	Params           map[string]any // temperature, top_p, max_tokens, stream …
	Stream           bool

	// Status.
	Status   string // "success" | "error"
	ErrorMsg string

	// captureContent, when false, forces input/output to NULL for this event
	// regardless of the global default (per-request PII opt-out). A nil value
	// means "use the global default".
	captureContent *bool
}

// WithCompletionStart records the first-token instant for a streamed generation,
// enabling the time-to-first-token metric and Langfuse's completion_start_time.
func (ev *TraceEvent) WithCompletionStart(t time.Time) {
	if !t.IsZero() {
		ev.CompletionStartAt = t
		ev.hasCompletionStart = true
	}
}

// SetCaptureContent overrides content capture for this single event.
func (ev *TraceEvent) SetCaptureContent(v bool) { ev.captureContent = &v }

// ── Pipeline state ───────────────────────────────────────────────────────────

var (
	obsCh          chan TraceEvent
	obsBufSize     int
	obsBatchSize   int
	obsFlush       time.Duration
	obsMaxContent  int
	obsCaptureBase atomic.Bool // global default for content capture
	obsRunning     atomic.Bool
	obsWG          sync.WaitGroup
	obsTablesReady atomic.Bool
	obsStartOnce   sync.Once
)

// InitObservability starts the async trace/observation writer. Idempotent and
// opt-in: with no datastore it registers as a counted no-op. Called from the
// shared Bootstrap right after InitDatastore.
func InitObservability() {
	obsStartOnce.Do(func() {
		obsBufSize = envInt("TRACE_BUFFER_SIZE", 8192)
		obsBatchSize = envInt("TRACE_BATCH_SIZE", 512)
		obsFlush = time.Duration(envInt("TRACE_FLUSH_MS", 500)) * time.Millisecond
		obsMaxContent = envInt("TRACE_MAX_CONTENT_BYTES", 32768)
		obsCaptureBase.Store(envBool("TRACE_CAPTURE_CONTENT", true))

		obsCh = make(chan TraceEvent, obsBufSize)
		obsRunning.Store(true)
		obsWG.Add(1)
		go drainObservations()
		logs.Info("observability: trace writer started (buffer=%d batch=%d flush=%s captureContent=%t)",
			obsBufSize, obsBatchSize, obsFlush, obsCaptureBase.Load())
	})
}

// EmitTrace hands one captured operation to the async writer. It is the ONE
// emission entrypoint (internal instrumentation AND the /v1/traces ingestion
// endpoint both call it). NON-BLOCKING: a full buffer drops the event and counts
// it, so the caller's request path is never delayed and never blocks on the
// warehouse. Fail-secure on scope: an event with no server-verified org is
// dropped (never written into an unscoped/shared bucket).
func EmitTrace(ev TraceEvent) {
	ev.Org = strings.TrimSpace(ev.Org)
	if ev.Org == "" {
		aiTraceEvents.WithLabelValues("unscoped").Inc()
		return
	}
	if !obsRunning.Load() || obsCh == nil {
		aiTraceEvents.WithLabelValues("disabled").Inc()
		return
	}
	// Truncate at capture to bound buffer memory; scrubbing happens on flush.
	ev.Input = truncateTraceContent(ev.Input, obsMaxContent)
	ev.Output = truncateTraceContent(ev.Output, obsMaxContent)
	select {
	case obsCh <- ev:
		aiTraceEvents.WithLabelValues("emitted").Inc()
	default:
		aiTraceEvents.WithLabelValues("dropped").Inc()
	}
}

// FlushObservability drains and stops the writer for graceful shutdown, bounded
// by ctx. Safe to call once; further EmitTrace calls become counted no-ops.
func FlushObservability(ctx context.Context) {
	if !obsRunning.CompareAndSwap(true, false) {
		return
	}
	close(obsCh)
	done := make(chan struct{})
	go func() { obsWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		logs.Warn("observability: flush timed out; some trace events may be unwritten")
	}
}

// drainObservations batches buffered events and writes one ClickHouse block per
// flush (size- or time-triggered). Scrubbing and row mapping run HERE, off the
// request path.
func drainObservations() {
	defer obsWG.Done()
	ticker := time.NewTicker(obsFlush)
	defer ticker.Stop()

	batch := make([]TraceEvent, 0, obsBatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		writeObservationBatch(batch)
		batch = batch[:0]
	}

	for {
		select {
		case ev, ok := <-obsCh:
			if !ok {
				flush()
				return
			}
			batch = append(batch, ev)
			if len(batch) >= obsBatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// writeObservationBatch persists a batch into hanzo.traces + hanzo.observations.
// Failures are logged and counted — never propagated to a request. Two blocks
// (one per table) keep the inserts columnar and efficient.
func writeObservationBatch(events []TraceEvent) {
	if !DatastoreEnabled() {
		aiTraceEvents.WithLabelValues("nostore").Add(float64(len(events)))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := EnsureObservabilityTables(ctx); err != nil {
		logs.Warn("observability: ensure tables: %v", err)
		aiTraceEvents.WithLabelValues("failed").Add(float64(len(events)))
		return
	}

	traceBatch, err := DatastoreBatch(ctx, tracesInsert)
	if err != nil {
		logs.Warn("observability: prepare traces batch: %v", err)
		aiTraceEvents.WithLabelValues("failed").Add(float64(len(events)))
		return
	}
	obsBatch, err := DatastoreBatch(ctx, observationsInsert)
	if err != nil {
		_ = traceBatch.Abort()
		logs.Warn("observability: prepare observations batch: %v", err)
		aiTraceEvents.WithLabelValues("failed").Add(float64(len(events)))
		return
	}

	for i := range events {
		ev := &events[i]
		ev.applyCapturePolicy(obsCaptureBase.Load())
		if err := traceBatch.Append(ev.traceRow()...); err != nil {
			logs.Warn("observability: append trace: %v", err)
			continue
		}
		if err := obsBatch.Append(ev.observationRow()...); err != nil {
			logs.Warn("observability: append observation: %v", err)
		}
	}

	if err := traceBatch.Send(); err != nil {
		logs.Warn("observability: send traces: %v", err)
		aiTraceEvents.WithLabelValues("failed").Add(float64(len(events)))
		_ = obsBatch.Abort()
		return
	}
	if err := obsBatch.Send(); err != nil {
		logs.Warn("observability: send observations: %v", err)
		aiTraceEvents.WithLabelValues("failed").Add(float64(len(events)))
		return
	}
	aiTraceEvents.WithLabelValues("written").Add(float64(len(events)))
}

// applyCapturePolicy resolves the content-capture decision (per-event override
// falls back to the global default) and scrubs secrets out of what survives. When
// capture is off, input/output are cleared so they persist as NULL.
func (ev *TraceEvent) applyCapturePolicy(globalDefault bool) {
	capture := globalDefault
	if ev.captureContent != nil {
		capture = *ev.captureContent
	}
	if !capture {
		ev.Input = ""
		ev.Output = ""
		return
	}
	ev.Input = scrubSecrets(ev.Input)
	ev.Output = scrubSecrets(ev.Output)
}

// ── Schema (the ONE definition; Langfuse-compatible) ─────────────────────────

const (
	tracesTable       = "hanzo.traces"
	observationsTable = "hanzo.observations"

	// Explicit INSERT column lists — the ONLY columns the writer populates, in the
	// exact order traceRow()/observationRow() emit them. created_at/updated_at are
	// intentionally omitted so ClickHouse fills them via DEFAULT now(); an explicit
	// list also makes the writer robust to future additive schema columns.
	tracesInsert = "INSERT INTO hanzo.traces " +
		"(id, timestamp, name, user_id, metadata, release, version, project_id, " +
		"public, bookmarked, tags, input, output, session_id, event_ts, is_deleted)"

	observationsInsert = "INSERT INTO hanzo.observations " +
		"(id, trace_id, project_id, type, parent_observation_id, start_time, end_time, " +
		"name, metadata, level, status_message, version, input, output, " +
		"provided_model_name, internal_model_id, model_parameters, " +
		"provided_usage_details, usage_details, provided_cost_details, cost_details, " +
		"total_cost, completion_start_time, prompt_id, prompt_name, prompt_version, " +
		"event_ts, is_deleted)"
)

// Langfuse traces schema, verbatim (unclustered variant), in the hanzo db.
const tracesTableDDL = `
	CREATE TABLE IF NOT EXISTS hanzo.traces (
		id String,
		timestamp DateTime64(3),
		name String,
		user_id Nullable(String),
		metadata Map(LowCardinality(String), String),
		release Nullable(String),
		version Nullable(String),
		project_id String,
		public Bool,
		bookmarked Bool,
		tags Array(String),
		input Nullable(String) CODEC(ZSTD(3)),
		output Nullable(String) CODEC(ZSTD(3)),
		session_id Nullable(String),
		created_at DateTime64(3) DEFAULT now(),
		updated_at DateTime64(3) DEFAULT now(),
		event_ts DateTime64(3),
		is_deleted UInt8
	) ENGINE = ReplacingMergeTree(event_ts, is_deleted)
	PARTITION BY toYYYYMM(timestamp)
	PRIMARY KEY (project_id, toDate(timestamp))
	ORDER BY (project_id, toDate(timestamp), id)
	TTL toDateTime(timestamp) + INTERVAL 2 YEAR`

// Langfuse observations schema, verbatim (unclustered variant), in the hanzo db.
const observationsTableDDL = `
	CREATE TABLE IF NOT EXISTS hanzo.observations (
		id String,
		trace_id String,
		project_id String,
		type LowCardinality(String),
		parent_observation_id Nullable(String),
		start_time DateTime64(3),
		end_time Nullable(DateTime64(3)),
		name String,
		metadata Map(LowCardinality(String), String),
		level LowCardinality(String),
		status_message Nullable(String),
		version Nullable(String),
		input Nullable(String) CODEC(ZSTD(3)),
		output Nullable(String) CODEC(ZSTD(3)),
		provided_model_name Nullable(String),
		internal_model_id Nullable(String),
		model_parameters Nullable(String),
		provided_usage_details Map(LowCardinality(String), UInt64),
		usage_details Map(LowCardinality(String), UInt64),
		provided_cost_details Map(LowCardinality(String), Decimal64(12)),
		cost_details Map(LowCardinality(String), Decimal64(12)),
		total_cost Nullable(Decimal64(12)),
		completion_start_time Nullable(DateTime64(3)),
		prompt_id Nullable(String),
		prompt_name Nullable(String),
		prompt_version Nullable(UInt16),
		created_at DateTime64(3) DEFAULT now(),
		updated_at DateTime64(3) DEFAULT now(),
		event_ts DateTime64(3),
		is_deleted UInt8
	) ENGINE = ReplacingMergeTree(event_ts, is_deleted)
	PARTITION BY toYYYYMM(start_time)
	PRIMARY KEY (project_id, type, toDate(start_time))
	ORDER BY (project_id, type, toDate(start_time), id)
	TTL toDateTime(start_time) + INTERVAL 2 YEAR`

// EnsureObservabilityTables creates both tables once. Idempotent; latches success
// so a transient outage at boot doesn't poison later flushes.
func EnsureObservabilityTables(ctx context.Context) error {
	if obsTablesReady.Load() {
		return nil
	}
	if err := DatastoreExec(ctx, tracesTableDDL); err != nil {
		return err
	}
	if err := DatastoreExec(ctx, observationsTableDDL); err != nil {
		return err
	}
	obsTablesReady.Store(true)
	return nil
}

// ── Row mapping (pure; unit-tested without a warehouse) ──────────────────────

// traceRow returns the hanzo.traces column values in DDL/INSERT order. Nullable
// string columns use *string so "absent" persists as NULL, not ”.
func (ev *TraceEvent) traceRow() []any {
	now := time.Now().UTC()
	ts := ev.StartTime.UTC()
	if ts.IsZero() {
		ts = now
	}
	return []any{
		"trace-" + ev.TraceID,    // id
		ts,                       // timestamp
		ev.Name,                  // name
		nullString(ev.User),      // user_id
		ev.Metadata,              // metadata
		(*string)(nil),           // release
		(*string)(nil),           // version
		ev.Org,                   // project_id (tenant)
		false,                    // public
		false,                    // bookmarked
		ev.tagList(),             // tags
		nullString(ev.Input),     // input
		nullString(ev.Output),    // output
		nullString(ev.SessionID), // session_id
		now,                      // event_ts
		uint8(0),                 // is_deleted
	}
}

// observationRow returns the hanzo.observations column values in DDL/INSERT order.
func (ev *TraceEvent) observationRow() []any {
	now := time.Now().UTC()
	start := ev.StartTime.UTC()
	if start.IsZero() {
		start = now
	}
	end := ev.EndTime.UTC()
	if end.IsZero() {
		end = now
	}

	costDollars := decimal.New(ev.CostCents, -2) // cents → dollars, exact
	usage := ev.usageDetails()
	cost := map[string]decimal.Decimal{"total": costDollars}

	level := levelDefault
	if ev.Status == "error" {
		level = levelError
	}

	return []any{
		"gen-" + ev.TraceID,      // id
		"trace-" + ev.TraceID,    // trace_id
		ev.Org,                   // project_id (tenant)
		ObservationGeneration,    // type
		(*string)(nil),           // parent_observation_id (root generation)
		start,                    // start_time
		&end,                     // end_time
		ev.observationName(),     // name
		ev.Metadata,              // metadata
		level,                    // level
		nullString(ev.ErrorMsg),  // status_message
		(*string)(nil),           // version
		nullString(ev.Input),     // input
		nullString(ev.Output),    // output
		nullString(ev.Model),     // provided_model_name
		(*string)(nil),           // internal_model_id
		ev.modelParametersJSON(), // model_parameters
		usage,                    // provided_usage_details
		usage,                    // usage_details
		cost,                     // provided_cost_details
		cost,                     // cost_details
		&costDollars,             // total_cost
		ev.completionStartPtr(),  // completion_start_time (TTFT)
		(*string)(nil),           // prompt_id
		(*string)(nil),           // prompt_name
		(*uint16)(nil),           // prompt_version
		now,                      // event_ts
		uint8(0),                 // is_deleted
	}
}

func (ev *TraceEvent) observationName() string {
	if ev.Model != "" {
		return ev.Model
	}
	return ev.Name
}

// usageDetails is the Langfuse token map. Zero entries are omitted so the map is
// clean (cache_* only appear when non-zero).
func (ev *TraceEvent) usageDetails() map[string]uint64 {
	m := map[string]uint64{
		"input":  uint64(nonNeg(ev.PromptTokens)),
		"output": uint64(nonNeg(ev.CompletionTokens)),
		"total":  uint64(nonNeg(ev.TotalTokens)),
	}
	if ev.CacheReadTokens > 0 {
		m["cache_read"] = uint64(ev.CacheReadTokens)
	}
	if ev.CacheWriteTokens > 0 {
		m["cache_write"] = uint64(ev.CacheWriteTokens)
	}
	return m
}

// modelParametersJSON serializes generation params (temperature, top_p, …) as the
// Langfuse model_parameters JSON string, or NULL when none were set.
func (ev *TraceEvent) modelParametersJSON() *string {
	params := map[string]any{}
	for k, v := range ev.Params {
		params[k] = v
	}
	if _, ok := params["stream"]; !ok {
		params["stream"] = ev.Stream
	}
	if len(params) == 0 {
		return nil
	}
	b, err := json.Marshal(params)
	if err != nil {
		return nil
	}
	s := string(b)
	return &s
}

func (ev *TraceEvent) completionStartPtr() *time.Time {
	if !ev.hasCompletionStart {
		return nil
	}
	t := ev.CompletionStartAt.UTC()
	return &t
}

func (ev *TraceEvent) tagList() []string {
	if ev.Tags == nil {
		return []string{}
	}
	return ev.Tags
}

// ── Content safety: truncation + secret scrubbing ────────────────────────────

// secretPatterns match high-confidence credential shapes so a prompt/completion
// that carries a key never lands in the trace store verbatim.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._\-]{12,}`),                                          // Authorization: Bearer …
	regexp.MustCompile(`\b(?:sk|hk|pk|rk|hz)[-_][A-Za-z0-9_\-]{16,}`),                                // hanzo/openai-style keys
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),                                                       // AWS access key id
	regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{5,}\.[A-Za-z0-9_\-]{5,}\.[A-Za-z0-9_\-]{5,}`),            // JWT (eyJ-anchored)
	regexp.MustCompile(`(?i)(api[_-]?key|secret|password|token)\s*[:=]\s*["']?[A-Za-z0-9._\-]{12,}`), // key: value
}

const redactionMark = "[REDACTED]"

// scrubSecrets masks high-confidence credential patterns. It runs on the drain
// goroutine, never on the request path.
func scrubSecrets(s string) string {
	if s == "" {
		return s
	}
	for _, re := range secretPatterns {
		s = re.ReplaceAllString(s, redactionMark)
	}
	return s
}

// truncateTraceContent bounds a captured field to max bytes (rune-safe), appending an
// elision marker. Runs at capture to cap buffer memory.
func truncateTraceContent(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	// Trim to a rune boundary at or below max.
	cut := max
	for cut > 0 && !utf8RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…[truncated]"
}

// utf8RuneStart reports whether b is NOT a UTF-8 continuation byte (0b10xxxxxx).
func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nonNeg(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

func envInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	switch v {
	case "":
		return def
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}

// ── Metrics (full o11y; bounded cardinality) ─────────────────────────────────
//
// Prometheus labels are deliberately bounded (model/provider/status/kind) — org
// is NOT a label (unbounded tenant cardinality would be a metrics-store DoS). The
// per-ORG breakdown the product needs lives in the ClickHouse trace store, which
// is org-partitioned. So: per-model/provider rates+latency+cost from Prometheus,
// per-org analytics from hanzo.traces/observations.

var (
	aiRequests = metric.NewCounterVec(metric.CounterOpts{
		Name: "cloud_ai_requests_total",
		Help: "AI inference requests by model, provider, status, and stream mode",
	}, []string{"model", "provider", "status", "stream"})

	aiDuration = metric.NewHistogramVec(metric.HistogramOpts{
		Name:    "cloud_ai_request_duration_ms",
		Help:    "End-to-end AI request latency in milliseconds",
		Buckets: []float64{5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000, 60000, 120000},
	}, []string{"model", "provider", "status"})

	aiTTFT = metric.NewHistogramVec(metric.HistogramOpts{
		Name:    "cloud_ai_ttft_ms",
		Help:    "Time-to-first-token for streamed AI responses in milliseconds",
		Buckets: []float64{10, 25, 50, 100, 200, 400, 800, 1500, 3000, 6000, 12000, 30000},
	}, []string{"model", "provider"})

	aiTokens = metric.NewCounterVec(metric.CounterOpts{
		Name: "cloud_ai_tokens_total",
		Help: "AI tokens by model, provider, and kind (prompt|completion|total)",
	}, []string{"model", "provider", "kind"})

	aiCostCents = metric.NewCounterVec(metric.CounterOpts{
		Name: "cloud_ai_cost_cents_total",
		Help: "AI spend in cents by model and provider",
	}, []string{"model", "provider"})

	aiTraceEvents = metric.NewCounterVec(metric.CounterOpts{
		Name: "cloud_ai_trace_events_total",
		Help: "Observability pipeline events by result (emitted|dropped|written|failed|unscoped|disabled|nostore)",
	}, []string{"result"})
)

// RecordAIMetrics updates the Prometheus AI counters/histograms for one operation.
// Cheap and lock-free-ish — safe to call inline on the request path. Labels are
// bounded (no org), so cardinality stays flat.
func RecordAIMetrics(model, provider, status string, stream bool, latency, ttft time.Duration,
	promptTokens, completionTokens, totalTokens int, costCents int64) {
	model = labelOrUnknown(model)
	provider = labelOrUnknown(provider)
	status = labelOrUnknown(status)

	aiRequests.WithLabelValues(model, provider, status, strconv.FormatBool(stream)).Inc()
	if latency > 0 {
		aiDuration.WithLabelValues(model, provider, status).Observe(float64(latency.Milliseconds()))
	}
	if stream && ttft > 0 {
		aiTTFT.WithLabelValues(model, provider).Observe(float64(ttft.Milliseconds()))
	}
	if promptTokens > 0 {
		aiTokens.WithLabelValues(model, provider, "prompt").Add(float64(promptTokens))
	}
	if completionTokens > 0 {
		aiTokens.WithLabelValues(model, provider, "completion").Add(float64(completionTokens))
	}
	if totalTokens > 0 {
		aiTokens.WithLabelValues(model, provider, "total").Add(float64(totalTokens))
	}
	if costCents > 0 {
		aiCostCents.WithLabelValues(model, provider).Add(float64(costCents))
	}
}

func labelOrUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}
