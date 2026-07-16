// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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
	"encoding/json"
	"strconv"
	"strings"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/router"
	"github.com/hanzoai/beego/logs"
	"github.com/sashabaranov/go-openai"
)

// orgAutoRoutingLookup resolves an org's own auto-routing preference ("",
// "enabled", "disabled"). Indirected through a var so tests can exercise the
// precedence matrix without a live DB; in production it reads the cached
// OrgSettings row.
var orgAutoRoutingLookup = object.GetCachedOrgAutoRouting

// sessionRoutingLookup resolves an org's own default-session-routing preference
// ("", "enabled", "disabled"), indirected for tests like orgAutoRoutingLookup.
var sessionRoutingLookup = object.GetCachedOrgSessionRouting

// effectiveAutoRouting folds the two-level OrgSettings precedence into one
// three-state preference for AutoRoutingActive: the org's own row wins, else the
// reserved "*" global-default row, else "" (defer to the conf router.enabled
// flag). A row's "disabled" therefore beats a lower level's "enabled".
func effectiveAutoRouting(org string) string {
	if pref := orgAutoRoutingLookup(org); pref != object.AutoRoutingUnset {
		return pref
	}
	return orgAutoRoutingLookup(object.GlobalDefaultOwner)
}

// effectiveSessionRouting folds the same org > "*" precedence for the
// default-session-routing preference. Unset at both levels means the conf
// default, which for session routing is disabled (there is no conf flag).
func effectiveSessionRouting(org string) string {
	if pref := sessionRoutingLookup(org); pref != object.AutoRoutingUnset {
		return pref
	}
	return sessionRoutingLookup(object.GlobalDefaultOwner)
}

// routingEventSink records a routing decision for training. The default is an
// async, best-effort DB write so a collection failure NEVER fails or slows the
// chat request; tests swap it for a synchronous capture. It is only ever called
// on a SUCCESSFUL resolveAutoModel resolution.
var routingEventSink = asyncRecordRoutingEvent

func asyncRecordRoutingEvent(e object.RoutingEvent) {
	go func() {
		if err := object.AddRoutingEvent(&e); err != nil {
			logs.Warning("routing event persist failed: %v", err)
		}
	}()
}

// RoutedModelHeader is the transparency header the chat controller sets when a
// virtual `auto`/`zen-router` request was resolved to a concrete model. Callers
// read it to see which model actually served (the response body echoes the same
// id in its `model` field).
const RoutedModelHeader = "X-Routed-Model"

// isAutoModel reports whether the requested model is the virtual routing alias.
func isAutoModel(model string) bool {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "auto", "zen-router":
		return true
	}
	return false
}

// resolveAutoModel maps the virtual `auto`/`zen-router` model to a concrete,
// servable model id when routing is enabled for the deployment. It returns
// (id, true) to signal a rewrite, or ("", false) to leave request.Model
// unchanged (not an auto request, or routing disabled).
//
// The resolved id then flows through the EXISTING chat path unchanged — provider
// resolution, ModelRoute fallbacks, per-org pricing, the zen identity prompt,
// balance reserve/settle, the usage record, and the response `model` echo — so
// `auto` is billed and reported as exactly the model that served it. Per-org
// routing is honored via the `known` predicate (resolveModelRouteForOrg), which
// only admits models the org can actually serve.
//
// Whether routing is active for THIS org is decided by AutoRoutingActive, which
// blends the global router.enabled flag with the org's OrgSettings preference:
// "disabled" opts the org out even when the global flag is on; "enabled" opts it
// in even when the global flag is off (as long as router config is present); ""
// defers to the global flag. When routing is not active, `auto` is left
// unchanged (no rewrite, no X-Routed-Model header) — its pre-routing behavior.
func resolveAutoModel(requested, orgId, userId, requestId string, req *openai.ChatCompletionRequest, slo router.Slo) (string, bool) {
	if !isAutoModel(requested) {
		return "", false
	}
	cfg := GetModelConfig()
	if cfg == nil {
		return "", false
	}
	if !cfg.AutoRoutingActive(effectiveAutoRouting(orgId)) {
		return "", false
	}
	client := cfg.RouterClient(func(id string) bool {
		return resolveModelRouteForOrg(id, orgId) != nil
	})
	// Fold the per-org router policy (org > "*" > conf) into this decision: the
	// org's own Prefer table wins per task key, and its cost ceiling fills the
	// SLO when the caller didn't send X-Max-Cost (an explicit header wins).
	client.Policy.Prefer = effectiveRouterPrefer(orgId, client.Policy.Prefer)
	client.Policy.CostCeiling = effectiveRouterCostCeiling(orgId, client.Policy.CostCeiling)
	slo = mergeCostCeiling(slo, client.Policy.CostCeiling)
	rreq := router.Request{
		Text:         lastUserText(req),
		ApproxTokens: estimatePromptTokens(req),
		HasMedia:     requestHasMedia(req),
	}
	dec := client.RouteDecision(context.Background(), rreq, slo)
	if dec.Model == "" {
		return "", false
	}

	// Collect the decision for training — fire-and-forget, NEVER prompt text.
	// Serialize the engine's optional feature vector; a marshal failure just
	// drops the features (the row is still useful).
	features := ""
	if len(dec.Features) > 0 {
		if b, err := json.Marshal(dec.Features); err == nil {
			features = string(b)
		}
	}
	routingEventSink(object.RoutingEvent{
		Owner:          orgId,
		User:           userId,
		RequestId:      requestId,
		Task:           string(dec.Task),
		RequestedModel: strings.ToLower(strings.TrimSpace(requested)),
		RoutedModel:    dec.Model,
		Confidence:     dec.Confidence,
		Source:         dec.Source,
		Features:       features,
	})

	return dec.Model, true
}

// routingUserId returns the verified principal as "owner/name" (the repo's
// user-attribution convention) for the routing ledger, or "" for a provider-key
// or unauthenticated request. It reuses the same verified-principal source as org
// resolution — never a raw client header.
func (c *ApiController) routingUserId() string {
	u := c.principalUser()
	if u == nil {
		return ""
	}
	return u.Owner + "/" + u.Name
}

// lastUserText returns the text of the last user message — enough for routing
// (mirrors hanzo-router: the last user turn drives classification). Falls back to
// any user text if the final user turn is empty.
func lastUserText(req *openai.ChatCompletionRequest) string {
	var fallback string
	for i := len(req.Messages) - 1; i >= 0; i-- {
		m := req.Messages[i]
		if m.Role != openai.ChatMessageRoleUser {
			continue
		}
		text := messageText(m)
		if text != "" {
			return text
		}
		if fallback == "" {
			fallback = text
		}
	}
	return fallback
}

// requestHasMedia reports whether any message carries non-text (image) content.
func requestHasMedia(req *openai.ChatCompletionRequest) bool {
	for _, m := range req.Messages {
		for _, part := range m.MultiContent {
			if part.Type != openai.ChatMessagePartTypeText {
				return true
			}
		}
	}
	return false
}

// messageText extracts the text of a chat message from Content or the text parts
// of MultiContent.
func messageText(m openai.ChatCompletionMessage) string {
	if m.Content != "" {
		return m.Content
	}
	var parts []string
	for _, p := range m.MultiContent {
		if p.Type == openai.ChatMessagePartTypeText && p.Text != "" {
			parts = append(parts, p.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// sloFromHeaders reads the optional per-request SLO from headers, so callers can
// express a cost/latency budget for `auto` without changing the OpenAI request
// body: `X-Max-Cost` (per-1k, float) and `X-Max-Latency-Ms` (int).
func (c *ApiController) sloFromHeaders() router.Slo {
	var slo router.Slo
	if v := c.Ctx.Request.Header.Get("X-Max-Cost"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			slo.MaxCost = f
		}
	}
	if v := c.Ctx.Request.Header.Get("X-Max-Latency-Ms"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			slo.MaxLatencyMs = n
		}
	}
	return slo
}
