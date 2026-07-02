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

// trace_emit.go adapts a completed AI operation (a usageRecord + timing) into the
// Langfuse-shaped trace/observation event and hands it to the async o11y writer,
// AND updates the Prometheus AI metrics. It is the ONE bridge from the request
// handlers to object.EmitTrace — every OpenAI-compatible surface (chat, tools,
// anthropic, embeddings, rerank, …) funnels through recordTrace, which calls this.
//
// SECURITY: the org (project_id / tenant) is taken from record.Organization /
// record.Owner — the SERVER-VERIFIED principal set at each call site from
// authUser.Owner (the JWT `owner` claim or the IAM key's user), NEVER a request
// body or client header. An operation with no resolved org yields no trace
// (EmitTrace drops it) rather than an unscoped row. The metadata map is an
// explicit allowlist — no Authorization header, no API key, no raw client IP.

package controllers

import (
	"time"

	"github.com/hanzoai/ai/object"
	"github.com/sashabaranov/go-openai"
)

// chatParams extracts the model parameters worth persisting on the observation
// (temperature, top_p, max_tokens, n, presence/frequency penalty), omitting
// zero-value fields so the model_parameters JSON stays clean.
func chatParams(req *openai.ChatCompletionRequest) map[string]any {
	p := map[string]any{}
	if req == nil {
		return p
	}
	if req.Temperature != 0 {
		p["temperature"] = req.Temperature
	}
	if req.TopP != 0 {
		p["top_p"] = req.TopP
	}
	if req.MaxTokens != 0 {
		p["max_tokens"] = req.MaxTokens
	}
	if req.N != 0 {
		p["n"] = req.N
	}
	if req.PresencePenalty != 0 {
		p["presence_penalty"] = req.PresencePenalty
	}
	if req.FrequencyPenalty != 0 {
		p["frequency_penalty"] = req.FrequencyPenalty
	}
	return p
}

// messagesToText flattens a chat message array to a plain-text prompt for the
// trace input (the raw structured request is not persisted; the writer scrubs +
// truncates this). Used by the tool-proxy paths that carry openai messages.
func messagesToText(messages []openai.ChatCompletionMessage) string {
	var b []byte
	for _, m := range messages {
		text := m.Content
		if text == "" && len(m.MultiContent) > 0 {
			for _, part := range m.MultiContent {
				if part.Type == openai.ChatMessagePartTypeText {
					text += part.Text
				}
			}
		}
		if text == "" {
			continue
		}
		if len(b) > 0 {
			b = append(b, '\n')
		}
		b = append(b, m.Role...)
		b = append(b, ": "...)
		b = append(b, text...)
	}
	return string(b)
}

// sessionID resolves an optional conversation/session id for trace grouping from
// the request header. Never fails; empty means "no session".
func (c *ApiController) sessionID() string {
	if c == nil || c.Ctx == nil || c.Ctx.Request == nil {
		return ""
	}
	if s := c.Ctx.Request.Header.Get("X-Session-Id"); s != "" {
		return s
	}
	return c.Ctx.Request.Header.Get("X-Session-ID")
}

// emitAIObservability builds the trace event from a completed operation record
// and emits it (non-blocking) plus records the Prometheus metrics. Safe to call
// inline on the request path — both sinks are non-blocking.
func emitAIObservability(record *usageRecord, startTime time.Time) {
	if record == nil {
		return
	}

	org := resolveTraceOrg(record)

	end := record.traceEnd
	if end.IsZero() {
		end = time.Now().UTC()
	}
	if startTime.IsZero() {
		startTime = end
	}
	latency := end.Sub(startTime)

	status := record.Status
	if status == "" {
		status = "success"
	}

	name := record.traceName
	if name == "" {
		name = "inference"
	}

	costCents := int64(calculateCostCentsWithCache(
		record.Model, record.PromptTokens, record.CompletionTokens,
		record.CacheReadTokens, record.CacheWriteTokens,
	))

	// Prometheus metrics — bounded labels (model/provider/status), no org.
	object.RecordAIMetrics(
		record.Model, record.Provider, status, record.Stream,
		latency, record.traceTTFT,
		record.PromptTokens, record.CompletionTokens, record.TotalTokens, costCents,
	)

	ev := object.TraceEvent{
		TraceID:          record.RequestID,
		Org:              org,
		User:             record.User,
		SessionID:        record.traceSession,
		Name:             name,
		Input:            record.traceInput,
		Output:           record.traceOutput,
		Tags:             traceTags(record, status),
		Metadata:         traceMetadata(record),
		StartTime:        startTime,
		EndTime:          end,
		Model:            record.Model,
		Provider:         record.Provider,
		PromptTokens:     record.PromptTokens,
		CompletionTokens: record.CompletionTokens,
		TotalTokens:      record.TotalTokens,
		CacheReadTokens:  record.CacheReadTokens,
		CacheWriteTokens: record.CacheWriteTokens,
		CostCents:        costCents,
		Params:           record.traceParams,
		Stream:           record.Stream,
		Status:           status,
		ErrorMsg:         record.ErrorMsg,
	}
	if !record.traceFirstByte.IsZero() {
		ev.WithCompletionStart(record.traceFirstByte)
	}

	object.EmitTrace(ev)
}

// resolveTraceOrg returns the tenant (project_id) for the trace. It is ALWAYS the
// server-verified org that each call site set from authUser.Owner (the JWT `owner`
// claim / the IAM key's user) — Organization first, then Owner. It NEVER consults
// a request body or client header, so a caller cannot write into another tenant's
// trace stream. An empty result makes EmitTrace drop the event (fail-secure).
func resolveTraceOrg(record *usageRecord) string {
	if record.Organization != "" {
		return record.Organization
	}
	return record.Owner
}

// traceTags builds the searchable tag set. Bounded, low-cardinality values only.
func traceTags(record *usageRecord, status string) []string {
	tags := make([]string, 0, 4)
	if record.Model != "" {
		tags = append(tags, record.Model)
	}
	if record.Provider != "" {
		tags = append(tags, record.Provider)
	}
	tags = append(tags, status)
	if record.Premium {
		tags = append(tags, "premium")
	}
	return tags
}

// traceMetadata is an explicit allowlist — NEVER auth headers, API keys, or raw
// client IP (PII). Only fields safe to persist and useful to filter on.
func traceMetadata(record *usageRecord) map[string]string {
	md := map[string]string{}
	if record.Provider != "" {
		md["provider"] = record.Provider
	}
	if record.RequestID != "" {
		md["request_id"] = record.RequestID
	}
	if record.Premium {
		md["premium"] = "true"
	}
	if record.Stream {
		md["stream"] = "true"
	}
	return md
}
