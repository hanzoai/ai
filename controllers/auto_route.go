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
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/router"
	"github.com/hanzoai/go-openai"
	iam "github.com/hanzoai/iam"
)

// orgAutoRoutingLookup resolves an org's own auto-routing preference ("",
// "enabled", "disabled"). Indirected through a var so tests can exercise the
// precedence matrix without a live DB; in production it reads the cached
// OrgSettings row.
var orgAutoRoutingLookup = object.GetCachedOrgAutoRouting

// sessionRoutingLookup resolves an org's own default-session-routing preference
// ("", "enabled", "disabled"), indirected for tests like orgAutoRoutingLookup.
var sessionRoutingLookup = object.GetCachedOrgSessionRouting

// effectiveAutoRouting folds the auto-routing precedence into one three-state
// preference for AutoRoutingActive:
//
//  1. the org's own OrgSettings row (if set) wins;
//  2. else the reserved "*" GlobalDefaultOwner row — the SINGLE source of truth
//     for the platform-wide routing default, edited via /v1/update-org-settings
//     from admin.hanzo.ai;
//  3. else the DEPRECATED ROUTER_ENABLED env, honored only while the global row
//     is unset (see deprecatedGlobalRouterEnv).
//
// A row's "disabled" therefore beats a lower level's "enabled", and the "*" row
// authoritatively overrides the env. Returning "" (all three unset) defers to the
// conf router.enabled flag in AutoRoutingActive.
func effectiveAutoRouting(org string) string {
	if pref := orgAutoRoutingLookup(org); pref != object.AutoRoutingUnset {
		return pref
	}
	if pref := orgAutoRoutingLookup(object.GlobalDefaultOwner); pref != object.AutoRoutingUnset {
		return pref
	}
	return deprecatedGlobalRouterEnv()
}

// routerEnabledEnvOnce guards the ROUTER_ENABLED deprecation log so it fires at
// most once per process — effectiveAutoRouting is on the per-request hot path.
var routerEnabledEnvOnce sync.Once

// deprecatedGlobalRouterEnv resolves the platform-wide routing default from the
// DEPRECATED ROUTER_ENABLED env. It is consulted ONLY when the "*"
// GlobalDefaultOwner row is unset (effectiveAutoRouting checks the row first), so
// the row stays authoritative and the env is a reversible transitional fallback.
// When the env is the deciding factor it returns "enabled" and logs a one-time
// deprecation pointing at the settings row; otherwise it returns "" so the conf
// router.enabled flag remains the last-resort default.
func deprecatedGlobalRouterEnv() string {
	if os.Getenv("ROUTER_ENABLED") != "true" {
		return object.AutoRoutingUnset
	}
	routerEnabledEnvOnce.Do(func() {
		log.Warning("ROUTER_ENABLED env is DEPRECATED: set the GlobalDefaultOwner (%q) OrgSettings.AutoRouting row via /v1/update-org-settings (admin.hanzo.ai); env honored as a fallback only because the global row is unset", object.GlobalDefaultOwner)
	})
	return object.AutoRoutingEnabled
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
			log.Warning("routing event persist failed: %v", err)
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
// routing is honored via the `known` predicate, which admits a model only when it
// resolves a route for the org (resolveModelRouteForOrg) AND the caller can actually
// serve it (modelServable): a family SKU the caller's tier or grant would make the
// serve path refuse is NOT admitted, so `auto` never routes to a model that would then
// 403/404 (HIP-510 §1). authUser is the request principal, threaded in for that check.
//
// Whether routing is active for THIS org is decided by AutoRoutingActive, which
// blends the global router.enabled flag with the org's OrgSettings preference:
// "disabled" opts the org out even when the global flag is on; "enabled" opts it
// in even when the global flag is off (as long as router config is present); ""
// defers to the global flag. When routing is not active, `auto` is left
// unchanged (no rewrite, no X-Routed-Model header) — its pre-routing behavior.
// resolveAutoModel returns (routedModel, task, ok). task is the router's task
// classification for the request (code/reasoning/chat/…), returned so the caller —
// which holds the request context (and thus the edge geo headers) — can fold the
// region's task-mix into the live-traffic globe without geo ever entering this
// routing logic. task is "" whenever ok is false.
func resolveAutoModel(requested, orgId, userId, requestId string, authUser *iam.User, req *openai.ChatCompletionRequest, slo router.Slo) (string, string, bool) {
	if !isAutoModel(requested) {
		return "", "", false
	}
	cfg := GetModelConfig()
	if cfg == nil {
		return "", "", false
	}
	if !cfg.AutoRoutingActive(effectiveAutoRouting(orgId)) {
		return "", "", false
	}
	client := cfg.RouterClient(func(id string) bool {
		return resolveModelRouteForOrg(id, orgId) != nil && modelServable(id, orgId, authUser)
	})
	// Fold the per-org router policy (org > "*" > conf) into this decision: the
	// org's own Prefer table wins per task key, and its cost ceiling fills the
	// SLO when the caller didn't send X-Max-Cost (an explicit header wins).
	client.Policy.Prefer = effectiveRouterPrefer(orgId, client.Policy.Prefer)
	client.Policy.CostCeiling = effectiveRouterCostCeiling(orgId, client.Policy.CostCeiling)
	// Exploration floor: a fraction of `auto` traffic samples a non-champion arm so
	// the bandit keeps earning reward on the whole pool (and discovers newly-added
	// models) instead of collapsing onto the champion. 0 (unset) = pure exploit.
	client.Explore = envFloat("ROUTER_EXPLORE_EPSILON", 0)
	slo = mergeCostCeiling(slo, client.Policy.CostCeiling)
	rreq := router.Request{
		Text:         lastUserText(req),
		ApproxTokens: estimatePromptTokens(req),
		HasMedia:     requestHasMedia(req),
	}
	dec := client.RouteDecision(context.Background(), rreq, slo)
	if dec.Model == "" {
		return "", "", false
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

	return dec.Model, string(dec.Task), true
}

// SourceExplicit marks a routing event for a request whose model the CALLER chose
// (not the virtual `auto`). Recorded so EVERY request — not only auto — is a
// labeled data point the moment the caller rates it: up/down feedback works for
// ALL models, and the ledger captures which model was selected for which task.
const SourceExplicit = "explicit"

// recordExplicitRouting records a content-free RoutingEvent for a NON-auto request:
// the caller's explicit model IS the served arm, classified into a task and keyed
// by request id, so an up/down reward attaches by that id and trains the router on
// which model the caller preferred for which task. Best-effort/async (never blocks
// the chat), never prompt text. Returns the classified task for the traffic globe.
func recordExplicitRouting(model, orgId, userId, requestId string, req *openai.ChatCompletionRequest) string {
	m := strings.ToLower(strings.TrimSpace(model))
	task := router.Classify(router.Request{
		Text:         lastUserText(req),
		ApproxTokens: estimatePromptTokens(req),
		HasMedia:     requestHasMedia(req),
	})
	routingEventSink(object.RoutingEvent{
		Owner:          orgId,
		User:           userId,
		RequestId:      requestId,
		Task:           string(task),
		RequestedModel: m,
		RoutedModel:    m,
		Source:         SourceExplicit,
	})
	return string(task)
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
// body: `X-Max-Cost` (USD per 1k tokens / per-1k, float) and `X-Max-Latency-Ms` (int).
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
