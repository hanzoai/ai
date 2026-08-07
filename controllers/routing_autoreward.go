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
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
)

// ── Dense auto-reward — the flywheel's implicit signal (HIP-510) ─────────────
//
// The learned router was reward-STARVED: ~request-volume decisions/day but only a
// handful of explicit rewards, so every training cycle "kept incumbent" and no arm
// was ever promoted. This scores the outcome of EVERY routed request and attaches
// it as the bandit's reward, keyed by the same request id the routing decision
// carries — turning thousands of daily requests into training signal instead of a
// few thumbs.
//
// reward = quality × cost-efficiency ∈ [0,1]:
//   - QUALITY is the cheap implicit proxy the request plane already knows: the call
//     SUCCEEDED (2xx, no error) and produced non-empty output. (A richer explicit
//     signal — thumbs — or an LLM judge overrides this floor later; see
//     AttachRoutingReward vs AttachRoutingRewardIfUnset.)
//   - COST-EFFICIENCY normalizes the arm's per-1k-token price against a reference,
//     so among arms that succeed the CHEAPER one keeps more reward. The router thus
//     converges to the cheapest arm that still succeeds, not the cheapest garbage
//     (a failing arm scores 0 regardless of price).
//
// Dark-by-default: a no-op unless ROUTER_AUTOREWARD_ENABLED, so it ships dark and
// enables per-deployment — same safety posture as the trainer. Best-effort and
// async so scoring never slows or fails a request.
const (
	autoRewardDefaultLambda        = 0.5 // cost weight in reward = q·(1 − λ·normCost)
	autoRewardDefaultRefCentsPer1k = 2.0 // reference arm price ≈ $20 / 1M tokens
)

// emitAutoRoutingReward scores the request and attaches the implicit reward to its
// routing event (if one exists) without clobbering an explicit reward. Called once
// from recordUsage — the single choke point every served request crosses — BEFORE
// its success filter, so an errored request scores 0 and still trains the bandit.
func emitAutoRoutingReward(r *usageRecord) {
	if r == nil || !envTrue("ROUTER_AUTOREWARD_ENABLED") {
		return
	}
	if r.RequestID == "" || r.Owner == "" {
		return
	}
	reward := implicitRoutingReward(r)
	go func() {
		if _, err := object.AttachRoutingRewardIfUnset(r.Owner, r.RequestID, reward); err != nil {
			log.Warning("auto-reward: attach failed request_id=%s: %v", r.RequestID, err)
		}
	}()
}

// implicitRoutingReward folds the quality proxy and the cost axis into one [0,1]
// bandit reward. A failed request is 0 (no cost credit for garbage); a successful
// one is discounted by how expensive its arm was.
func implicitRoutingReward(r *usageRecord) float64 {
	q := implicitQuality(r)
	if q <= 0 {
		return 0
	}
	lambda := envFloat("ROUTER_AUTOREWARD_LAMBDA", autoRewardDefaultLambda)
	return clamp01(q * (1 - lambda*normRoutingCost(r)))
}

// implicitQuality is the per-request quality the gateway can observe with zero extra
// work: 1.0 = succeeded with output, 0.2 = 2xx but empty (weak), 0 = errored.
func implicitQuality(r *usageRecord) float64 {
	if r.Status != "success" {
		return 0 // errored / refused / timed out — this arm failed the request
	}
	if r.CompletionTokens <= 0 && r.ImageCount == 0 && r.VideoCount == 0 && !recordIsAudio(r) {
		return 0.2 // 2xx with no output — a weak, near-empty success
	}
	return 1.0
}

// normRoutingCost is the arm's per-1k-token cost normalized to [0,1] against a
// reference rate. Per-1k (not per-request) so it compares ARMS, not request sizes.
// Per-unit calls (image/video, no token cost) contribute no cost penalty.
func normRoutingCost(r *usageRecord) float64 {
	tokens := r.TotalTokens
	if tokens <= 0 {
		tokens = r.PromptTokens + r.CompletionTokens
	}
	if tokens <= 0 {
		return 0
	}
	centsPer1k := float64(usageCostCents(r)) / float64(tokens) * 1000.0
	ref := envFloat("ROUTER_AUTOREWARD_COST_REF_CENTS_PER_1K", autoRewardDefaultRefCentsPer1k)
	if ref <= 0 {
		return 0
	}
	return clamp01(centsPer1k / ref)
}
