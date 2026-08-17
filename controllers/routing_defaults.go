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
	"bufio"
	"encoding/json"
	"io"

	"github.com/hanzoai/ai/object"
)

// getRoutingEvents is the ledger source, indirected through a var so the export
// contract can be tested without a live DB. Production reads the DB.
var getRoutingEvents = object.GetRoutingEvents

// The routing-defaults read (GET /v1/router/defaults) and the training-data exports
// (GET /v1/router/{ledger,rewards}) are served ZAP-native — the ONE implementation —
// in controllers/zap_router-policy-stats.go (zapGetRoutingDefaultsHandler,
// zapExportRouting{Ledger,Rewards}Handler); the exports enforce the SAME super-admin-OR-
// ROUTER_ADMIN_TOKEN gate via zapRPSRouterAdminAuthorized. No controller twin.

// routingDefaults is the resolved-for-the-caller routing default surface read by
// console/chat/app/desktop. Both fields are resolved with org > "*" > conf
// precedence (see effectiveAutoRouting / effectiveSessionRouting).
type routingDefaults struct {
	AutoRoutingActive     bool `json:"auto_routing_active"`
	DefaultSessionRouting bool `json:"default_session_routing"`
}

// ExportMyRoutingData streams the CALLER'S OWN org routing events as JSONL. Org-
// admin gated and SELF-SCOPED — the org is forced to c.GetOrg(), never a query or
// body value — so a customer exports only its own content-free ledger. This is the
// customer-facing data-ownership read that pairs with the training opt-in; the
// super-admin, any-org ExportRoutingLedger above is the platform operator's export.
//
// @Title ExportMyRoutingData
// @Tag Router API
// @Description export the caller's own org routing events (content-free) as JSONL
// @router /export-my-routing-data [get]
func (c *ApiController) ExportMyRoutingData() {
	if !c.RequireAdmin() {
		return
	}
	org := c.GetOrg()
	if org == "" {
		c.ResponseError(c.T("auth:Please sign in first"))
		return
	}
	events, err := getRoutingEvents(org, c.Input().Get("since"))
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.SetHeader("Content-Type", "application/x-ndjson")
	_ = c.SendStreamWriter(func(w *bufio.Writer) {
		_ = writeRoutingLedgerJSONL(w, events)
	})
}

// DeleteMyRoutingData deletes ALL of the caller's OWN org routing events — the
// self-scoped right-to-be-forgotten. Org-admin gated and self-scoped (c.GetOrg()),
// so a caller can only ever delete its own data, never another tenant's. Completes
// the data-ownership story: content-free ledger + opt-in + self-export + self-delete.
//
// @Title DeleteMyRoutingData
// @Tag Router API
// @Description delete all of the caller's own org routing events (right to be forgotten)
// @router /delete-my-routing-data [post]
func (c *ApiController) DeleteMyRoutingData() {
	if !c.RequireAdmin() {
		return
	}
	org := c.GetOrg()
	if org == "" {
		c.ResponseError(c.T("auth:Please sign in first"))
		return
	}
	n, err := object.DeleteRoutingEvents(org)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(map[string]any{"deleted": n})
}

// writeRoutingLedgerJSONL streams events as JSONL (one per line). It is a pure
// function of its inputs so the export contract is unit-testable without a DB or
// router context. A write error stops the stream (a truncated download is better
// than a partial-then-error line).
func writeRoutingLedgerJSONL(w io.Writer, events []*object.RoutingEvent) error {
	enc := json.NewEncoder(w)
	for _, e := range events {
		line := ledgerLine{
			Id:               e.Id,
			CreatedAt:        e.CreatedTime,
			Org:              e.Owner,
			User:             e.User,
			RequestId:        e.RequestId,
			Task:             e.Task,
			RequestedModel:   e.RequestedModel,
			RoutedModel:      e.RoutedModel,
			Model:            e.RoutedModel, // build_dataset --ledger reads "model"
			Confidence:       e.Confidence,
			Source:           e.Source,
			ShadowModel:      e.ShadowModel,
			PromptTokens:     e.PromptTokens,
			CompletionTokens: e.CompletionTokens,
			CostCents:        e.CostCents,
			LatencyMs:        e.LatencyMs,
			Reward:           e.Reward,
			RewardedAt:       e.RewardedTime,
		}
		if e.Features != "" {
			line.Features = json.RawMessage(e.Features)
		}
		// Encoder writes a trailing newline per call → JSONL.
		if err := enc.Encode(line); err != nil {
			return err
		}
	}
	return nil
}

// ledgerLine is the JSONL export shape. Keys are snake_case to match the
// zen-router ledger contract; "model" aliases the routed model for
// build_dataset.py, while "routed_model"/"task"/"features" serve the reward-join
// / heads-fit stage. "shadow_model" is the engine's counterfactual pick for a
// family call (the A/B signal); "reward"/"rewarded_at" carry the joined outcome.
// No prompt text is ever emitted.
type ledgerLine struct {
	Id               string          `json:"id"`
	CreatedAt        string          `json:"created_at"`
	Org              string          `json:"org"`
	User             string          `json:"user"`
	RequestId        string          `json:"request_id"`
	Task             string          `json:"task"`
	RequestedModel   string          `json:"requested_model"`
	RoutedModel      string          `json:"routed_model"`
	Model            string          `json:"model"`
	Confidence       float64         `json:"confidence"`
	Source           string          `json:"source"`
	ShadowModel      string          `json:"shadow_model,omitempty"`
	PromptTokens     int             `json:"prompt_tokens,omitempty"`
	CompletionTokens int             `json:"completion_tokens,omitempty"`
	CostCents        int64           `json:"cost_cents,omitempty"`
	LatencyMs        int64           `json:"latency_ms,omitempty"`
	Reward           float64         `json:"reward,omitempty"`
	RewardedAt       string          `json:"rewarded_at,omitempty"`
	Features         json.RawMessage `json:"features,omitempty"`
}
