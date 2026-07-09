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
	"time"

	"github.com/hanzoai/ai/object"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// genAISpanFields is the pure projection of a usageRecord onto OTel GenAI
// semantic-convention span data. It is separated from the side-effecting emit so
// the attribute mapping (the contract with o11y) is unit-testable without any
// tracer/provider global state.
type genAISpanFields struct {
	name       string
	attrs      []attribute.KeyValue
	statusCode codes.Code
	statusMsg  string
}

// buildGenAISpanFields maps a completed LLM call to its GenAI span, following the
// OpenTelemetry GenAI semantic conventions (span name "chat {model}",
// gen_ai.* attributes). Attribution (user/org/provider/request-id) rides on span
// attributes. Pure: no I/O, no globals.
func buildGenAISpanFields(record *usageRecord) genAISpanFields {
	model := record.Model
	if model == "" {
		model = "unknown"
	}

	attrs := []attribute.KeyValue{
		attribute.String("gen_ai.system", "hanzo"),
		attribute.String("gen_ai.operation.name", "chat"),
		attribute.String("gen_ai.request.model", model),
		attribute.String("gen_ai.response.model", model),
		attribute.Int("gen_ai.usage.input_tokens", record.PromptTokens),
		attribute.Int("gen_ai.usage.output_tokens", record.CompletionTokens),
	}
	if record.User != "" {
		attrs = append(attrs, attribute.String("user.id", record.User))
	}
	if org := firstNonEmptyStr(record.Organization, record.Owner); org != "" {
		attrs = append(attrs, attribute.String("gen_ai.hanzo.org_id", org))
	}
	if record.Provider != "" {
		attrs = append(attrs, attribute.String("gen_ai.hanzo.provider", record.Provider))
	}
	if record.RequestID != "" {
		attrs = append(attrs, attribute.String("gen_ai.hanzo.request_id", record.RequestID))
	}

	fields := genAISpanFields{name: "chat " + model, attrs: attrs, statusCode: codes.Ok}
	if record.Status == "error" {
		fields.statusCode = codes.Error
		fields.statusMsg = record.ErrorMsg
	}
	return fields
}

// emitGenAISpan emits one OTel GenAI span per LLM call to o11y. It is
// called from recordTrace — the single funnel every completion surface (chat,
// Anthropic, tool-proxy, embeddings, ZAP) passes through — so there is exactly
// one emit site. No-op when telemetry is disabled (OTEL_EXPORTER_OTLP_ENDPOINT
// unset); span export is batched/async, so this never blocks the request.
func emitGenAISpan(record *usageRecord, startTime time.Time) {
	if record == nil || !object.TelemetryEnabled() {
		return
	}

	fields := buildGenAISpanFields(record)
	_, span := object.GenAITracer().Start(
		context.Background(), fields.name,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithTimestamp(startTime),
		trace.WithAttributes(fields.attrs...),
	)
	span.SetStatus(fields.statusCode, fields.statusMsg)
	span.End(trace.WithTimestamp(time.Now().UTC()))
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
