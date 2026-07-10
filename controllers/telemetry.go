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
	"os"
	"strings"
	"time"

	"github.com/hanzoai/ai/object"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// GenAI span attribute keys. These are a WIRE CONTRACT with o11y: they MUST match
// o11y's pkg/types/{llmobstypes,llmpricingruletypes} constants exactly, because the
// o11y llmobs views project spans by these names. Kept as literals here so the ai
// module takes no dependency on the o11y module.
const (
	attrGenAISystem        = "gen_ai.system"
	attrGenAIOperation     = "gen_ai.operation.name"
	attrGenAIRequestModel  = "gen_ai.request.model"
	attrGenAIResponseModel = "gen_ai.response.model"
	attrGenAIInputTokens   = "gen_ai.usage.input_tokens"
	attrGenAIOutputTokens  = "gen_ai.usage.output_tokens"
	attrGenAITotalTokens   = "gen_ai.usage.total_tokens"
	attrGenAICacheRead     = "gen_ai.usage.cache_read.input_tokens"
	attrGenAICacheCreation = "gen_ai.usage.cache_creation.input_tokens"

	// _o11y.gen_ai.* cost attributes (dollars) the o11y cost views read.
	// total_cost = the FULL provider cost of the call (analytics). billed_cost = what
	// Hanzo actually charged the org ledger — equal to total_cost for a Hanzo-served
	// call, but only the ~1% platform fee for a BYO call (the customer paid the
	// upstream directly), so Observe reconciles with the invoice.
	attrO11yTotalCost      = "_o11y.gen_ai.total_cost"
	attrO11yBilledCost     = "_o11y.gen_ai.billed_cost"
	attrO11yCostInput      = "_o11y.gen_ai.cost_input"
	attrO11yCostOutput     = "_o11y.gen_ai.cost_output"
	attrO11yCostCacheRead  = "_o11y.gen_ai.cost_cache_read"
	attrO11yCostCacheWrite = "_o11y.gen_ai.cost_cache_write"

	attrSessionID      = "session.id"
	attrConversationID = "gen_ai.conversation.id"
	attrUserID         = "user.id"
	attrOrgID          = "gen_ai.hanzo.org_id"
	attrProvider       = "gen_ai.hanzo.provider"
	attrRequestID      = "gen_ai.hanzo.request_id"
	attrServedBy       = "gen_ai.hanzo.served_by"
	attrClusterID      = "gen_ai.hanzo.cluster_id"
	attrRoutePolicy    = "gen_ai.hanzo.route_policy"
	attrInputMessages  = "gen_ai.input.messages"
	attrOutputMessages = "gen_ai.output.messages"

	// servedByDefault labels a call served by Hanzo cloud (the majority). BYO
	// provider/GPU paths override it via record.ServedBy.
	servedByDefault = "hanzo"
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
// gen_ai.* attributes). Attribution (user/org/provider/request-id/session/served-by)
// and cost/token detail ride on span attributes.
//
// Pure: no I/O, no globals, no pricing lookup. Cost is passed in (totalCostUSD is
// the authoritative per-call cost in dollars; breakdown is the per-component split
// for token-billed calls, nil for image/video which have no component split). This
// keeps the o11y attribute contract unit-testable without a pricing table. Message
// capture is gated by the caller (captureMessages) — PII is emitted only on opt-in.
func buildGenAISpanFields(record *usageRecord, totalCostUSD, billedCostUSD float64, breakdown *costBreakdown, captureMessages bool) genAISpanFields {
	model := record.Model
	if model == "" {
		model = "unknown"
	}

	attrs := []attribute.KeyValue{
		attribute.String(attrGenAISystem, "hanzo"),
		attribute.String(attrGenAIOperation, "chat"),
		attribute.String(attrGenAIRequestModel, model),
		attribute.String(attrGenAIResponseModel, model),
		attribute.Int(attrGenAIInputTokens, record.PromptTokens),
		attribute.Int(attrGenAIOutputTokens, record.CompletionTokens),
		attribute.Int(attrGenAITotalTokens, record.TotalTokens),
		// total_cost = full provider cost (analytics). billed_cost = what the ledger
		// charged (== total for Hanzo-served, ~1% fee for BYO). Both always emitted
		// so the o11y cost views have the columns and Observe reconciles the invoice.
		attribute.Float64(attrO11yTotalCost, totalCostUSD),
		attribute.Float64(attrO11yBilledCost, billedCostUSD),
	}

	// Cache tokens (Anthropic-style prompt caching) — omitted when absent.
	if record.CacheReadTokens > 0 {
		attrs = append(attrs, attribute.Int(attrGenAICacheRead, record.CacheReadTokens))
	}
	if record.CacheWriteTokens > 0 {
		attrs = append(attrs, attribute.Int(attrGenAICacheCreation, record.CacheWriteTokens))
	}

	// Per-component cost detail — token-billed calls only (breakdown != nil).
	if breakdown != nil {
		attrs = append(
			attrs,
			attribute.Float64(attrO11yCostInput, breakdown.Input),
			attribute.Float64(attrO11yCostOutput, breakdown.Output),
			attribute.Float64(attrO11yCostCacheRead, breakdown.CacheRead),
			attribute.Float64(attrO11yCostCacheWrite, breakdown.CacheWrite),
		)
	}

	// Session turns the o11y sessions/conversations view on for this org. Emitted
	// under both the OTel conversation key and o11y's session.id grouping key.
	if record.Session != "" {
		attrs = append(
			attrs,
			attribute.String(attrSessionID, record.Session),
			attribute.String(attrConversationID, record.Session),
		)
	}

	if record.User != "" {
		attrs = append(attrs, attribute.String(attrUserID, record.User))
	}
	if org := firstNonEmptyStr(record.Organization, record.Owner); org != "" {
		attrs = append(attrs, attribute.String(attrOrgID, org))
	}
	if record.Provider != "" {
		attrs = append(attrs, attribute.String(attrProvider, record.Provider))
	}
	if record.RequestID != "" {
		attrs = append(attrs, attribute.String(attrRequestID, record.RequestID))
	}

	// served_by is always present (defaults to hanzo cloud); cluster/route-policy
	// only when a producer set them (BYO-GPU / enso routing).
	servedBy := record.ServedBy
	if servedBy == "" {
		servedBy = servedByDefault
	}
	attrs = append(attrs, attribute.String(attrServedBy, servedBy))
	if record.ClusterID != "" {
		attrs = append(attrs, attribute.String(attrClusterID, record.ClusterID))
	}
	if record.RoutePolicy != "" {
		attrs = append(attrs, attribute.String(attrRoutePolicy, record.RoutePolicy))
	}

	// Prompt/completion bodies are PII — emitted ONLY on explicit opt-in
	// (captureMessages), and only when non-empty. Default is redacted.
	if captureMessages {
		if record.InputMessages != "" {
			attrs = append(attrs, attribute.String(attrInputMessages, record.InputMessages))
		}
		if record.OutputMessages != "" {
			attrs = append(attrs, attribute.String(attrOutputMessages, record.OutputMessages))
		}
	}

	fields := genAISpanFields{name: "chat " + model, attrs: attrs, statusCode: codes.Ok}
	if record.Status == "error" {
		fields.statusCode = codes.Error
		fields.statusMsg = record.ErrorMsg
	}
	return fields
}

// genAICaptureMessages reports whether prompt/completion bodies may be attached to
// the span. Fail-secure: only an explicit opt-in
// (O11Y_GENAI_CAPTURE_MESSAGES=true/1) enables it; anything else redacts.
//
// OPS NOTE: leave O11Y_GENAI_CAPTURE_MESSAGES UNSET in production. Enabling it writes
// raw prompt/completion text (PII, and the customer's proprietary data) into the
// shared o11y_traces span store, where any org member with Observe access reads it and
// it rides the platform's retention/backup. Turn it on only for a scoped debugging
// window with the tenant's consent, then unset it. Default OFF = redacted is the
// safe, compliant posture.
func genAICaptureMessages() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("O11Y_GENAI_CAPTURE_MESSAGES"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// emitGenAISpan emits one OTel GenAI span per LLM call to o11y. It is
// called from recordTrace — the single funnel every completion surface (chat,
// Anthropic, tool-proxy, embeddings, ZAP) passes through — so there is exactly
// one emit site. The span starts from the REQUEST context (ctx), so it nests under
// any in-flight parent span and inherits its trace/baggage rather than orphaning at
// a fresh root. No-op when telemetry is disabled (exporter unset); span export is
// batched/async, so this never blocks the request.
func emitGenAISpan(ctx context.Context, record *usageRecord, startTime time.Time) {
	if record == nil || !object.TelemetryEnabled() {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Full provider cost (total, analytics) and the billed amount (what the ledger
	// charged — == total for Hanzo-served, ~1% platform fee for BYO). Both derive
	// from the ONE usageCostCents, so the span and the invoice never diverge.
	costCents := usageCostCents(record)
	totalCostUSD := float64(costCents) / 100.0
	billedCostUSD := float64(usageBilledCents(record, costCents)) / 100.0
	var breakdown *costBreakdown
	if record.ImageCount == 0 && record.VideoCount == 0 {
		b := modelCostBreakdown(record.Model, record.PromptTokens, record.CompletionTokens, record.CacheReadTokens, record.CacheWriteTokens)
		breakdown = &b
	}

	fields := buildGenAISpanFields(record, totalCostUSD, billedCostUSD, breakdown, genAICaptureMessages())
	_, span := object.GenAITracer().Start(
		ctx, fields.name,
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
