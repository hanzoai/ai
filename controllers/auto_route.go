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
	"sort"
	"strconv"
	"strings"
	"sync"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/router"
	"github.com/hanzoai/go-openai"
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
//     for the platform-wide routing default, edited via /v1/ai/org/settings
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
		log.Warning("ROUTER_ENABLED env is DEPRECATED: set the GlobalDefaultOwner (%q) OrgSettings.AutoRouting row via /v1/ai/org/settings (admin.hanzo.ai); env honored as a fallback only because the global row is unset", object.GlobalDefaultOwner)
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
	// Eligibility = servable for this org (route + tier/grant), then narrowed by the
	// org's ENABLED-MODELS allowlist (empty = all servable). This IS the `Known`
	// predicate the heuristic ForTask AND the engine-decision guard (routeEngine) both
	// honor, so a disabled model is never routed to on either path.
	allow := effectiveRouterEnabledModels(orgId)
	client := cfg.RouterClient(func(id string) bool {
		if resolveModelRouteForOrg(id, orgId) == nil || !modelServable(id, orgId, authUser) {
			return false
		}
		return len(allow) == 0 || allow[strings.ToLower(strings.TrimSpace(id))]
	})
	// The HARD allowlist floor: the heuristic's last-resort fallback relaxes servability
	// but must NEVER relax this, so a DISABLED model is never routed to even when no
	// servable preferred model remains. Left nil when the org set no allowlist (the
	// last resort then relaxes to all, unchanged). Set BEFORE the dial narrows Known, so
	// it is the pure allowlist — the soft cost budget is relaxable, the allowlist is not.
	if len(allow) > 0 {
		client.Allow = func(id string) bool { return allow[strings.ToLower(strings.TrimSpace(id))] }
	}
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
	// The SAVINGS-vs-QUALITY dial: below max-quality it narrows eligibility to models
	// within a per-request cost budget (so the heuristic picks the cheapest PREFERRED
	// model that fits) AND tightens the SLO for the engine path. Only ever tightens —
	// never loosens past the resolved ceiling or an explicit X-Max-Cost.
	client.Known, slo = applyRouterQualityBias(client.Known, slo, blendedPriceForOrg(orgId), effectiveRouterQualityBias(orgId), cfg)
	// FREE-TIER FLASH CAP: an org that commerce CONFIDENTLY reports as free tier has `auto`
	// confined to the flash pool — the router may select only models whose blended price is
	// within the flash ceiling, so a non-paying caller is never routed to a premium/expensive
	// model (which the balance gate would then 402 anyway, breaking the request). It reuses the
	// SAME cost-narrowing primitive as the savings dial and composes AFTER it, so it can only
	// TIGHTEN: a paid/trial org is untouched, an UNKNOWN tier (a commerce blip) is untouched —
	// uncertainty NEVER caps, so a paying caller is never degraded on a hiccup — and an explicit
	// X-Max-Cost still wins when lower. Reversible: ROUTER_FREE_TIER_CEILING_PER_MILLION=0 disables it.
	if freeTierFlashCapActive(orgId, authUser) {
		client.Known, slo = capKnownToBudget(client.Known, slo, blendedPriceForOrg(orgId), freeTierFlashCeilingPerMillion())
	}
	rreq := router.Request{
		Text:         lastUserText(req),
		ApproxTokens: estimatePromptTokens(req),
		HasMedia:     requestHasMedia(req),
	}
	// Per-org strategy + deterministic overrides complete the policy (the client
	// already carries the org's Enabled predicate and Prefer table above). Strategy
	// "" and nil Overrides are inert, so an org with neither set routes exactly as
	// before — the wiring is fail-safe by construction.
	rp := router.RoutingPolicy{
		Strategy:  effectiveRouterStrategy(orgId),
		Overrides: effectiveRouterOverrides(orgId),
	}
	dec := client.RouteDecisionFor(context.Background(), rreq, slo, rp)
	if dec.Model == "" {
		// The org's allowlist admitted no model its prefer table offers for this task.
		// Rather than dead-end `auto`, route to a deterministic ALLOWED + servable model
		// (the org restricted routing to these, so any is a valid answer) — an allowlist
		// always resolves to one of ITS OWN, never a disabled model and never empty. dec.Task
		// is already the classified task. When there is no allowlist, behavior is unchanged.
		if m := firstAllowedServableModel(allow, orgId, authUser, cfg); m != "" {
			dec.Model = m
		} else {
			return "", "", false
		}
	}

	// Congestion-aware mean-field layer — GATED, default OFF (meanFieldRoute returns the
	// champion untouched, so live routing is byte-identical). When an admin enables it at
	// admin.hanzo.ai, `auto` best-responds to the live load mean field over the task's
	// SERVABLE candidates, spreading traffic off a congested champion AS AN EQUILIBRIUM
	// (avoids stampeding the single best model). The base selection above is otherwise
	// untouched, and a re-rank is honestly re-tagged in the training ledger.
	routedModel, source := dec.Model, dec.Source
	if m, changed := meanFieldRoute(client, dec.Task, dec.Model); changed {
		routedModel, source = m, SourceMeanField
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
		RoutedModel:    routedModel,
		Confidence:     dec.Confidence,
		Source:         source,
		Features:       features,
	})

	return routedModel, string(dec.Task), true
}

// firstAllowedServableModel returns the lexicographically-first model in the org's
// enabled-models allowlist that this org can actually serve (has a route AND passes the
// tier/grant check), or "" if none is. Deterministic, so the same org+allowlist always
// resolves identically. Consulted ONLY as the allowlist's fallback of last resort — when
// the heuristic table offered no allowed model for the task — so `auto` still resolves to
// one of the org's OWN chosen models instead of dead-ending.
func firstAllowedServableModel(allow map[string]bool, orgId string, authUser *iam.User, cfg *ModelConfig) string {
	if len(allow) == 0 || cfg == nil {
		return ""
	}
	ids := make([]string, 0, len(allow))
	for id := range allow {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if resolveModelRouteForOrg(id, orgId) != nil && modelServable(id, orgId, authUser) {
			return id
		}
	}
	return ""
}

// applyRouterQualityBias tilts routing by the org's savings-vs-quality dial (bias in
// [0,1]: 0 = cheapest, 1 = best). It returns a (possibly narrowed) eligibility predicate
// and a (possibly tightened) SLO. At bias >= 1 (max quality) nothing changes. Below 1 it
// derives a per-request cost BUDGET = cheapest + bias·(priciest − cheapest) across the
// org's eligible models (injected blended $/1M price) and (a) drops models pricier than
// the budget from eligibility — so the heuristic ForTask picks the cheapest PREFERRED
// model that fits — and (b) tightens slo.MaxCost to that budget (per-1k) for the engine
// path. It can only TIGHTEN: an explicit X-Max-Cost or the resolved cost ceiling still
// wins when lower. An unpriced model (price ≤ 0) is never dropped and never bounds the
// range, so a missing price can neither exclude a model nor skew the dial.
func applyRouterQualityBias(base func(string) bool, slo router.Slo, price priceIndexFn, bias float64, cfg *ModelConfig) (func(string) bool, router.Slo) {
	if bias >= 1 || cfg == nil || base == nil || price == nil {
		return base, slo
	}
	var minP, maxP float64
	seen := false
	for _, m := range cfg.ListModels() {
		if !base(m.ID) {
			continue
		}
		p := price(m.ID)
		if p <= 0 {
			continue
		}
		if !seen || p < minP {
			minP = p
		}
		if !seen || p > maxP {
			maxP = p
		}
		seen = true
	}
	if !seen || maxP <= minP {
		return base, slo // one price point (or none priced): nothing to tilt
	}
	budget := minP + bias*(maxP-minP) // blended $/1M
	return capKnownToBudget(base, slo, price, budget)
}

// capKnownToBudget narrows an eligibility predicate to models whose blended $/1M price is
// within budgetPerMillion, and tightens slo.MaxCost to the same budget (per-1k) for the engine
// path. It only ever TIGHTENS: it never re-admits a model `base` already rejected, an UNPRICED
// model (price ≤ 0) is never dropped (a missing price can neither exclude a model nor be read as
// free), and slo.MaxCost is lowered only when the budget is lower (an explicit X-Max-Cost or an
// already-tighter ceiling still wins). It is the ONE cost-narrowing primitive shared by the
// savings-vs-quality dial and the free-tier flash cap. A non-positive budget or a nil base/price
// is a no-op, so a disabled cap can never dead-end routing.
func capKnownToBudget(base func(string) bool, slo router.Slo, price priceIndexFn, budgetPerMillion float64) (func(string) bool, router.Slo) {
	if base == nil || price == nil || budgetPerMillion <= 0 {
		return base, slo
	}
	eligible := func(id string) bool {
		if !base(id) {
			return false
		}
		p := price(id)
		return p <= 0 || p <= budgetPerMillion*(1+1e-9)
	}
	if per1k := router.PerMillionToPerThousand(budgetPerMillion); slo.MaxCost <= 0 || per1k < slo.MaxCost {
		slo.MaxCost = per1k
	}
	return eligible, slo
}

// DefaultFreeTierFlashCeilingPerMillion caps the FREE tier's `auto` routing to the flash pool.
// It is a BLENDED $/1M bound ((input+output)/2 — the blendedPriceForOrg unit). It sits
// deliberately ABOVE the synthesized default price (default_pricing 1/4 ⇒ blended 2.50), so a
// servable model with no explicit price (a zen champion, enso-flash) is treated as flash and
// never dropped, while EVERY premium model sits far above it (glm-5.2 ~6.3, gpt-5.x ~10, o3 ~25,
// opus/fable ~45) and is excluded with wide margin — as are the pricey non-premium ids
// (qwen3-coder ~3.6, kimi ~5, deepseek-reasoner ~5.5). The cheap pool (gpt-4o-mini ~0.38, the
// gemma/mistral/nemotron/llama pool ~0.1–0.35, deepseek-v3.2 ~0.69) is well within it. The
// family-tier gate already keeps free callers off the enso ladder above enso-flash; this ceiling
// is the complementary bound that keeps them off the do-ai premium models (not family SKUs).
const DefaultFreeTierFlashCeilingPerMillion = 3.00

// freeTierFlashCeilingPerMillion resolves the live flash ceiling — the
// ROUTER_FREE_TIER_CEILING_PER_MILLION env override or the built-in default. 0 (env set to 0)
// disables the free-tier cap entirely: the reversible kill switch, no rebuild.
func freeTierFlashCeilingPerMillion() float64 {
	return envFloat("ROUTER_FREE_TIER_CEILING_PER_MILLION", DefaultFreeTierFlashCeilingPerMillion)
}

// freeTierFlashCapActive reports whether the caller's commerce tier is CONFIDENTLY free — the
// only case the flash cap fires. It reuses the SAME tier source (familyTier) and subject rule
// (familyAccessSubject) the family-SKU gate uses, so it keys on the identical subject the balance
// read and usage debit do. Its fail-safe direction is deliberately that of familyTierAllowed, NOT
// the funding gate: an UNKNOWN tier (commerce unconfigured, a blip, or no subject → familyTier "")
// does NOT cap, so a paying caller is never degraded to the flash pool on a commerce hiccup. Only a
// confident "free" (enso rank 0) caps; trial/paid never do. A disabled ceiling (env 0) is false.
func freeTierFlashCapActive(orgId string, authUser *iam.User) bool {
	if freeTierFlashCeilingPerMillion() <= 0 {
		return false
	}
	name := familyTier(familyAccessSubject(orgId, authUser))
	if strings.TrimSpace(name) == "" {
		return false // unknown → do NOT cap (never degrade a paying caller on a blip)
	}
	return ensoTierRank(commerceTierToLadder(name)) == ensoTierRank("free")
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
	if v := c.Header("X-Max-Cost"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			slo.MaxCost = f
		}
	}
	if v := c.Header("X-Max-Latency-Ms"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			slo.MaxLatencyMs = n
		}
	}
	return slo
}
