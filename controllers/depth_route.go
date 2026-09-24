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
	"bytes"
	"encoding/json"
	"strings"

	"github.com/hanzoai/thinking"
)

// DepthDefault is the router.depth key for a request that states no reasoning depth.
const DepthDefault = "default"

// foldDepth lower-cases every id and depth key of a router.depth table, so a lookup
// reads the same row however the file or the caller spelled it.
func foldDepth(in map[string]map[string]string) map[string]map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]map[string]string, len(in))
	for id, row := range in {
		r := make(map[string]string, len(row))
		for depth, sku := range row {
			r[strings.ToLower(strings.TrimSpace(depth))] = strings.TrimSpace(sku)
		}
		out[strings.ToLower(strings.TrimSpace(id))] = r
	}
	return out
}

// DepthRoute names the priced SKU a request for model is served at when its caller
// funds it: the router.depth row for model, read at the request's reasoning depth.
//
// It answers only for a model the family serves at no price, and only with a SKU the
// family serves at a price and without a grant, so it can lift a free call onto the
// paid ladder and never the reverse. ok is false for every other request, which then
// runs as sent.
func DepthRoute(model string, body []byte) (string, bool) {
	cfg := GetModelConfig()
	if cfg == nil {
		return "", false
	}
	row := cfg.depthTable(model)
	if len(row) == 0 {
		return "", false
	}
	sku := row[requestDepth(body)]
	if sku == "" {
		sku = row[DepthDefault]
	}
	if sku == "" {
		return "", false
	}
	asked, ok := familyLookupFresh(model)
	if !ok || asked.priced() {
		return "", false
	}
	served, ok := familyLookupFresh(sku)
	if !ok || !served.priced() || served.gated() {
		return "", false
	}
	return served.ID, true
}

// requestDepth is the reasoning depth a request states, as a router.depth key: the
// effort word it sends (reasoning_effort, or reasoning.effort), else the depth its
// token budget folds to (thinking.budget_tokens, or reasoning.max_tokens), "off" for
// reasoning turned off, and DepthDefault when it states none.
func requestDepth(body []byte) string {
	var req struct {
		ReasoningEffort string `json:"reasoning_effort"`
		Reasoning       *struct {
			Effort    string `json:"effort"`
			MaxTokens int    `json:"max_tokens"`
			Enabled   *bool  `json:"enabled"`
		} `json:"reasoning"`
		Thinking *struct {
			Type         string `json:"type"`
			BudgetTokens int    `json:"budget_tokens"`
		} `json:"thinking"`
	}
	if json.Unmarshal(body, &req) != nil {
		return DepthDefault
	}
	if e := strings.ToLower(strings.TrimSpace(req.ReasoningEffort)); e != "" {
		return e
	}
	if r := req.Reasoning; r != nil {
		switch {
		case strings.TrimSpace(r.Effort) != "":
			return strings.ToLower(strings.TrimSpace(r.Effort))
		case r.Enabled != nil && !*r.Enabled:
			return "off"
		case r.MaxTokens > 0:
			return depthName(thinking.Budget(r.MaxTokens))
		}
	}
	if t := req.Thinking; t != nil {
		switch strings.ToLower(strings.TrimSpace(t.Type)) {
		case "disabled":
			return "off"
		case "enabled":
			return depthName(thinking.Budget(t.BudgetTokens))
		}
	}
	return DepthDefault
}

// depthName is the effort word for a depth.
func depthName(d thinking.Depth) string {
	switch d {
	case thinking.Low:
		return "low"
	case thinking.Mid:
		return "medium"
	case thinking.High:
		return "high"
	case thinking.Max:
		return "max"
	default:
		return "off"
	}
}

// WithModel returns a JSON request body naming model in place of the one it named,
// every other field as sent. ok is false for a body that is not a JSON object.
func WithModel(body []byte, model string) ([]byte, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || fields == nil {
		return nil, false
	}
	name, err := json.Marshal(model)
	if err != nil {
		return nil, false
	}
	fields["model"] = name
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if enc.Encode(fields) != nil {
		return nil, false
	}
	return bytes.TrimRight(out.Bytes(), "\n"), true
}
