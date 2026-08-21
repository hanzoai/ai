// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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

package object

// family_routing.go — the ONE place a served model-family call becomes a
// privacy-preserving RoutingEvent (source="family") + optional shadow A/B pick.
//
// It is shared, not duplicated: BOTH ai's proxy path (controllers.pipeToFamily, when
// ai fronts an out-of-process family like enso) AND cloud's embedded-zen meter
// (cloud/apps/zen.go commerceMeter, which serves the zen catalog in-process and never
// reaches ai's proxy) call this, so every family call — however it is carried — lands
// in the same ledger with the same join key. No prompt text is ever stored; the shadow
// call is the only thing that reads the text, exactly as the auto-route path does.

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/router"
)

// NormalizeRequestId trims the id and strips the response-object "chatcmpl-" prefix,
// so a caller may key on either the raw request id (as the usage ledger stores it) or
// the response `id` field verbatim — one stored form, both inputs. Both the family
// event write and the /v1/ai/feedback join go through here, so they key identically.
func NormalizeRequestId(s string) string {
	return strings.TrimPrefix(strings.TrimSpace(s), "chatcmpl-")
}

// FamilyRoutingInput is everything needed to record one served family call. The
// RoutedModel is the served upstream/arm (falls back to RequestedModel); ResponseId is
// the client-visible response id the client threads back to /v1/ai/feedback (the join
// key). The Shadow* fields drive the A/B pick and are NEVER stored — only the engine's
// derived features + counterfactual model persist.
type FamilyRoutingInput struct {
	Owner            string
	User             string
	RequestedModel   string // the family SKU (e.g. "zen5", "enso")
	RoutedModel      string // the served upstream/arm; "" falls back to RequestedModel
	ResponseId       string // client-visible response id — the reward-join key
	PromptTokens     int
	CompletionTokens int
	CostCents        int64
	LatencyMs        int64

	// Shadow A/B (optional): when RouterEndpoint is set AND ShadowText is non-empty,
	// ask the learned engine what IT would have picked for the same request features
	// and record ShadowModel + the engine's opaque feature vector. Zero user risk (the
	// call already served). ShadowText is used only for the engine call, never stored.
	RouterEndpoint     string
	ShadowText         string
	ShadowApproxTokens int
	ShadowHasMedia     bool
}

// RecordFamilyRouting writes the family RoutingEvent and, when configured, the shadow
// A/B pick. It is best-effort and blocking (the shadow call is hard-capped by
// router.DefaultTimeout); callers invoke it OFF the request hot path (a goroutine), the
// same fire-and-forget contract as the auto-route ledger writers. A nil DB or insert
// error is logged, never surfaced.
func RecordFamilyRouting(in FamilyRoutingInput) {
	joinID := NormalizeRequestId(in.ResponseId)
	if joinID == "" {
		joinID = uuid.NewString()
	}
	routed := strings.TrimSpace(in.RoutedModel)
	if routed == "" {
		routed = in.RequestedModel
	}
	ev := RoutingEvent{
		Owner:            in.Owner,
		User:             in.User,
		RequestId:        joinID,
		RequestedModel:   strings.ToLower(strings.TrimSpace(in.RequestedModel)),
		RoutedModel:      routed,
		Source:           "family",
		PromptTokens:     in.PromptTokens,
		CompletionTokens: in.CompletionTokens,
		CostCents:        in.CostCents,
		LatencyMs:        in.LatencyMs,
	}
	if ep := strings.TrimSpace(in.RouterEndpoint); ep != "" && strings.TrimSpace(in.ShadowText) != "" {
		cli := router.Client{Endpoint: ep, Timeout: router.DefaultTimeout}
		dec := cli.RouteDecision(context.Background(), router.Request{
			Text:         in.ShadowText,
			ApproxTokens: in.ShadowApproxTokens,
			HasMedia:     in.ShadowHasMedia,
		}, router.Slo{})
		ev.ShadowModel = dec.Model
		ev.Task = string(dec.Task)
		ev.Confidence = dec.Confidence
		if len(dec.Features) > 0 {
			if b, err := json.Marshal(dec.Features); err == nil {
				ev.Features = string(b)
			}
		}
	}
	if err := AddRoutingEvent(&ev); err != nil {
		log.Warning("family routing event persist failed: %v", err)
	}
}
