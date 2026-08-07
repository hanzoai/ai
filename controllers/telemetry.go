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

	// provider_cost = Hanzo's COGS to serve the call (0 for BYO); margin = billed −
	// provider_cost. These add the cost/margin view beside the customer-price
	// total_cost/billed_cost above (which stay the legacy figures o11y already reads).
	attrO11yProviderCost = "_o11y.gen_ai.provider_cost"
	attrO11yMargin       = "_o11y.gen_ai.margin"

	attrSessionID      = "session.id"
	attrConversationID = "gen_ai.conversation.id"
	attrUserID         = "user.id"
	attrOrgID          = "gen_ai.hanzo.org_id"
	attrProject        = "gen_ai.hanzo.project"
	attrAPIKeyHash     = "gen_ai.hanzo.api_key_hash"
	attrProvider       = "gen_ai.hanzo.provider"
	attrRequestID      = "gen_ai.hanzo.request_id"
	attrServedBy       = "gen_ai.hanzo.served_by"
	attrClusterID      = "gen_ai.hanzo.cluster_id"
	attrRoutePolicy    = "gen_ai.hanzo.route_policy"
	// attrPriced is emitted (=false) ONLY when the model had no configured price and
	// billed at the default — so o11y can flag the row. Absent ⇒ priced normally.
	attrPriced         = "gen_ai.hanzo.priced"
	attrInputMessages  = "gen_ai.input.messages"
	attrOutputMessages = "gen_ai.output.messages"

	// attrDeploymentEnv is the standard OTel key o11y reads for Environment. Emitted
	// per-request from record.Environment (the caller's X-Environment) so Observe
	// narrows by environment instead of defaulting to "default".
	attrDeploymentEnv = "deployment.environment"

	// servedByDefault labels a call served by Hanzo cloud (the majority). BYO
	// provider/GPU paths override it via record.ServedBy.
	servedByDefault = "hanzo"

	// orgSystemDefault is the reserved system org stamped on a span when an internal
	// system caller (router probe/judge, warmups) carries NEITHER Organization nor
	// Owner. o11y's llmobs views apply a mandatory gen_ai.hanzo.org_id filter that
	// silently drops empty-org spans, so a last-resort org keeps internal traffic
	// visible. Real tenant traffic always resolves its own org before this fallback.
	orgSystemDefault = "hanzo"
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
// Pure: no I/O, no globals, no pricing lookup. Every dollar figure is passed in
// (totalCostUSD is the full customer-price cost; billedCostUSD what the ledger charged;
// providerCostUSD Hanzo's COGS; marginUSD = billed − provider; breakdown is the
// per-component split for token-billed calls, nil for image/video). This keeps the o11y
// attribute contract unit-testable without a pricing table. Message capture is gated by
// the caller (captureMessages) — PII is emitted only on opt-in.
func buildGenAISpanFields(record *usageRecord, totalCostUSD, billedCostUSD, providerCostUSD float64, marginUSD *float64, breakdown *costBreakdown, captureMessages bool) genAISpanFields {
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
		// total_cost = full customer-price cost (analytics). billed_cost = what the
		// ledger charged (== total for Hanzo-served, ~1% fee for BYO). provider_cost =
		// Hanzo's COGS (0 for BYO). These three are always emitted so the o11y cost
		// views have the columns and Observe reconciles; margin is conditional below.
		attribute.Float64(attrO11yTotalCost, totalCostUSD),
		attribute.Float64(attrO11yBilledCost, billedCostUSD),
		attribute.Float64(attrO11yProviderCost, providerCostUSD),
	}

	// Margin, only when it is computable. An unpriced model's COGS is the
	// conservative default, so billed − COGS would report a heavy loss that nobody
	// took. The priced=false attribute below says why the number is absent.
	if marginUSD != nil {
		attrs = append(attrs, attribute.Float64(attrO11yMargin, *marginUSD))
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
	// org_id is MANDATORY: o11y's llmobs views hard-filter on gen_ai.hanzo.org_id and
	// drop empty-org spans. Resolve from the record (Organization → Owner) and fall
	// back to the reserved system org for internal system callers that carry neither,
	// so every ai-emitted span stays visible. Always emitted (never conditionally).
	org := firstNonEmptyStr(record.Organization, record.Owner, orgSystemDefault)
	attrs = append(attrs, attribute.String(attrOrgID, org))
	// Environment (X-Environment) → deployment.environment, when the caller set one.
	if record.Environment != "" {
		attrs = append(attrs, attribute.String(attrDeploymentEnv, record.Environment))
	}
	// Project is the org SUB-SCOPE (the caller's X-Project-Id). Emitting it lets the
	// o11y views + the cloud metrics board narrow WITHIN an org by project; empty
	// (the org's default project) emits no attribute — honest, never a fabricated scope.
	if record.Project != "" {
		attrs = append(attrs, attribute.String(attrProject, record.Project))
	}
	// APIKeyHash is a NON-reversible ref (SHA-256 hex) of the caller credential —
	// never the plaintext key. It correlates a span to a key without the span store
	// ever holding a secret.
	if record.APIKeyHash != "" {
		attrs = append(attrs, attribute.String(attrAPIKeyHash, record.APIKeyHash))
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
	// Unpriced call: billed at the conservative default because the model has no
	// configured price. Emit priced=false so o11y flags it; omit otherwise.
	if record.Unpriced {
		attrs = append(attrs, attribute.Bool(attrPriced, false))
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
// genAIMoney is the money view of one call as the o11y span reports it, in USD.
type genAIMoney struct {
	total    float64 // the FULL provider cost of the call (analytics)
	billed   float64 // what the ledger actually charged
	provider float64 // Hanzo's COGS to serve it
	// margin is billed − provider, and NIL when the COGS was invented (an unpriced
	// model). The span then carries no margin attribute at all, beside the
	// priced=false it already emits — honest, rather than a fabricated loss.
	margin *float64
}

// spanMoney derives that view from usageMargin — the ONE billed path, the same trio
// stamped on the cloud_usage row and taken by the debit, so the span, the warehouse
// and the invoice carry one number each.
//
// billed used to be recomputed here from the cent-precision rate table while margin
// beside it came from usageMargin, so a span reported a billed amount and a margin
// derived from two DIFFERENT figures whenever a caller knew its exact one. For a
// model with no configured price the table invents the conservative default, so the
// span could show several times what the invoice charged.
func spanMoney(record *usageRecord) genAIMoney {
	mg := usageMargin(record)
	money := genAIMoney{
		total:    float64(usageCostCents(record)) / 100.0,
		billed:   float64(mg.BilledNano) / 1e9,
		provider: float64(mg.CostNano) / 1e9,
	}
	if mg.MarginNano != nil {
		margin := float64(*mg.MarginNano) / 1e9
		money.margin = &margin
	}
	return money
}

func emitGenAISpan(ctx context.Context, record *usageRecord, startTime time.Time) {
	if record == nil || !object.TelemetryEnabled() {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	money := spanMoney(record)
	totalCostUSD, billedCostUSD := money.total, money.billed
	providerCostUSD, marginUSD := money.provider, money.margin
	var breakdown *costBreakdown
	// A token breakdown describes a token-billed call. Image, video and audio all
	// bill per unit and carry no tokens, so computing one would report a cost
	// structure the call does not have.
	if record.ImageCount == 0 && record.VideoCount == 0 && !recordIsAudio(record) {
		b := modelCostBreakdown(record.Model, record.PromptTokens, record.CompletionTokens, record.CacheReadTokens, record.CacheWriteTokens)
		breakdown = &b
	}

	fields := buildGenAISpanFields(record, totalCostUSD, billedCostUSD, providerCostUSD, marginUSD, breakdown, genAICaptureMessages())
	_, span := object.GenAITracer().Start(
		ctx, fields.name,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithTimestamp(startTime),
		trace.WithAttributes(fields.attrs...),
	)
	span.SetStatus(fields.statusCode, fields.statusMsg)
	span.End(trace.WithTimestamp(time.Now().UTC()))
}

// firstNonEmptyStr returns the first argument that is non-empty after trimming
// surrounding whitespace — the single shared helper (finetune, telemetry, …).
func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
