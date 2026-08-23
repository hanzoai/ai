package controllers

// $ai_generation — the analytics view of one LLM call.
//
// Three planes read one call, and each wants a different fact about it:
// the ledger wants money (zapWriteUsage → cloud_usage), tracing wants a span
// (emitGenAISpan → o11y), and LLM Analytics wants an event. This is the third,
// emitted from the same recordTrace funnel as the other two so all three are
// stamped from one place with one attribution.
//
// The event vocabulary is not ours to choose: the LLM Analytics product reads
// $ai_generation and its $ai_* properties, so anything instrumented with the SDK
// and anything served through this gateway land in the same views. Sending our
// own shape would mean a second product that happens to look similar.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	// insightsIngestPath is the SDK capture endpoint, so gateway traffic and SDK
	// traffic arrive through the same path and normalize identically.
	insightsIngestPath = "/i/v0/e/"
	genAIEventTimeout  = 5 * time.Second
)

var (
	insightsIngestOnce sync.Once
	insightsIngestURL  string
	insightsIngestKey  string
	genAIEventClient   = &http.Client{Timeout: genAIEventTimeout}
)

// insightsIngest resolves the destination once. Both halves are required: a URL
// with no key would post events the plane refuses, and a key with no URL has
// nowhere to go. Unset means this emit is off, which is how it behaves anywhere
// the plane is not reachable — no error, no retry, no request held open.
func insightsIngest() (string, string) {
	insightsIngestOnce.Do(func() {
		url := strings.TrimRight(strings.TrimSpace(os.Getenv("INSIGHTS_INGEST_URL")), "/")
		key := strings.TrimSpace(os.Getenv("INSIGHTS_INGEST_KEY"))
		if url != "" && key != "" {
			insightsIngestURL, insightsIngestKey = url+insightsIngestPath, key
		}
	})
	return insightsIngestURL, insightsIngestKey
}

// genAIEventProps is the pure shape of the event, split out so a test can assert
// the vocabulary without a server. Latency is SECONDS — the product renders it as
// a duration and would read milliseconds as a call that took an hour.
func genAIEventProps(record *usageRecord, latency time.Duration) map[string]any {
	money := spanMoney(record)
	props := map[string]any{
		"$ai_model":          record.Model,
		"$ai_provider":       record.Provider,
		"$ai_input_tokens":   record.PromptTokens,
		"$ai_output_tokens":  record.CompletionTokens,
		"$ai_total_cost_usd": money.total,
		"$ai_latency":        latency.Seconds(),
		"$ai_is_error":       record.Status != "" && record.Status != "success",
		"$ai_span_name":      "generation",
		"$ai_trace_id":       record.RequestID,
		"$ai_generation_id":  record.RequestID,
		"$ai_stream":         record.Stream,
		"organization":       record.Organization,
	}
	if record.CacheReadTokens > 0 {
		props["$ai_cache_read_input_tokens"] = record.CacheReadTokens
	}
	if record.CacheWriteTokens > 0 {
		props["$ai_cache_creation_input_tokens"] = record.CacheWriteTokens
	}
	if record.ErrorMsg != "" {
		props["$ai_error"] = record.ErrorMsg
	}
	if record.Session != "" {
		props["$ai_session_id"] = record.Session
	}
	// Prompts and completions are deliberately absent. The span emit gates them
	// behind O11Y_GENAI_CAPTURE_MESSAGES; an analytics event that carried them
	// unasked would put customer prompts in a second store under a different
	// retention.
	return props
}

// genAIEventBody builds the capture payload. distinct_id falls back through the
// identities a call can carry so the event still attributes when the caller is a
// machine credential rather than a person.
func genAIEventBody(key string, record *usageRecord, startTime time.Time, latency time.Duration) ([]byte, error) {
	distinct := record.User
	if distinct == "" {
		distinct = record.Owner
	}
	if distinct == "" {
		distinct = record.Organization
	}
	return json.Marshal(map[string]any{
		"api_key":     key,
		"event":       "$ai_generation",
		"distinct_id": distinct,
		"timestamp":   startTime.UTC().Format(time.RFC3339Nano),
		"properties":  genAIEventProps(record, latency),
	})
}

// emitGenAIEvent ships the event without blocking the request. Called from
// recordTrace beside emitGenAISpan; a nil record or an unconfigured plane is a
// no-op. The context is deliberately NOT the request's: the request is finished
// by the time this runs, and inheriting its cancellation would drop the event
// for exactly the calls that completed.
func emitGenAIEvent(_ context.Context, record *usageRecord, startTime time.Time) {
	url, key := insightsIngest()
	if record == nil || url == "" {
		return
	}
	body, err := genAIEventBody(key, record, startTime, time.Since(startTime))
	if err != nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), genAIEventTimeout)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := genAIEventClient.Do(req)
		if err != nil {
			return
		}
		// Drained and closed so the connection returns to the pool; the body is
		// an ack we have nothing to do with.
		resp.Body.Close()
	}()
}
