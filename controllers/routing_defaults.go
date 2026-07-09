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
	"encoding/json"
	"io"

	"github.com/hanzoai/ai/object"
)

// getRoutingEvents is the ledger source, indirected through a var so the export
// contract can be tested without a live DB. Production reads the DB.
var getRoutingEvents = object.GetRoutingEvents

// routingDefaults is the resolved-for-the-caller routing default surface read by
// console/chat/app/desktop. Both fields are resolved with org > "*" > conf
// precedence (see effectiveAutoRouting / effectiveSessionRouting).
type routingDefaults struct {
	AutoRoutingActive     bool `json:"auto_routing_active"`
	DefaultSessionRouting bool `json:"default_session_routing"`
}

// GetRoutingDefaults returns the routing defaults resolved for the CALLER's org.
// Unlike the org-settings CRUD it is NOT admin-gated — every authenticated user
// may read it — but it exposes only booleans, never provider or topology config.
// Resolution is cached exactly like org settings (60s TTL, via the shared
// OrgSettings cache the effective* helpers read).
//
// @Title GetRoutingDefaults
// @Tag Router API
// @Description get the auto-routing / default-session-routing defaults resolved for the caller's org
// @Success 200 {object} controllers.routingDefaults The Response object
// @router /get-routing-defaults [get]
func (c *ApiController) GetRoutingDefaults() {
	org := c.GetEffectiveOrg()

	autoActive := false
	if cfg := GetModelConfig(); cfg != nil {
		autoActive = cfg.AutoRoutingActive(effectiveAutoRouting(org))
	}

	c.ResponseOk(routingDefaults{
		AutoRoutingActive:     autoActive,
		DefaultSessionRouting: effectiveSessionRouting(org) == object.AutoRoutingEnabled,
	})
}

// ExportRoutingLedger streams the privacy-preserving routing-decision ledger as
// JSONL (one event per line) for router training. RequireSuperAdmin — it is a
// platform-wide export.
//
// The line schema matches the keys zen-router training/build_dataset.py --ledger
// reads (model, task) and adds the ledger's native fields plus the engine
// feature vector. It deliberately carries NO prompt text: build_dataset's
// prompt-keyed SFT join therefore skips these rows (its `if row.get("prompt")`
// guard). This export instead feeds the reward-join / heads-fit training stage,
// which keys on the feature vector + routed model, not prompt text — the privacy
// design in universe/docs/architecture/personal-router-training.md.
//
// Filters: ?org= (owner) and ?since= (RFC3339, created_time >= since).
//
// @Title ExportRoutingLedger
// @Tag Router API
// @Description stream the routing-decision ledger as JSONL for training (super admin only)
// @Param org query string false "filter to one org"
// @Param since query string false "only events at/after this RFC3339 timestamp"
// @Success 200 {string} string "JSONL, one routing event per line"
// @router /export-routing-ledger [get]
func (c *ApiController) ExportRoutingLedger() {
	if !c.RequireSuperAdmin() {
		return
	}

	org := c.Input().Get("org")
	since := c.Input().Get("since")

	events, err := getRoutingEvents(org, since)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.Ctx.Output.Header("Content-Type", "application/x-ndjson")
	_ = writeRoutingLedgerJSONL(c.Ctx.ResponseWriter, events)
	c.EnableRender = false
}

// writeRoutingLedgerJSONL streams events as JSONL (one per line). It is a pure
// function of its inputs so the export contract is unit-testable without a DB or
// beego context. A write error stops the stream (a truncated download is better
// than a partial-then-error line).
func writeRoutingLedgerJSONL(w io.Writer, events []*object.RoutingEvent) error {
	enc := json.NewEncoder(w)
	for _, e := range events {
		line := ledgerLine{
			Id:             e.Id,
			CreatedAt:      e.CreatedTime,
			Org:            e.Owner,
			User:           e.User,
			Task:           e.Task,
			RequestedModel: e.RequestedModel,
			RoutedModel:    e.RoutedModel,
			Model:          e.RoutedModel, // build_dataset --ledger reads "model"
			Confidence:     e.Confidence,
			Source:         e.Source,
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
// / heads-fit stage. No prompt text is ever emitted.
type ledgerLine struct {
	Id             string          `json:"id"`
	CreatedAt      string          `json:"created_at"`
	Org            string          `json:"org"`
	User           string          `json:"user"`
	Task           string          `json:"task"`
	RequestedModel string          `json:"requested_model"`
	RoutedModel    string          `json:"routed_model"`
	Model          string          `json:"model"`
	Confidence     float64         `json:"confidence"`
	Source         string          `json:"source"`
	Features       json.RawMessage `json:"features,omitempty"`
}
