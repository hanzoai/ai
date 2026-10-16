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
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/hanzoai/thinking"
	"github.com/zap-proto/zip"
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

// depthPick is what router.depth says for one request naming a router id: the row it
// read, the depth the request states and the key it was read at, the tier there,
// whether the family serves that tier to every caller at a price (why, when it does
// not), and the family's free SKU, which answers whatever nothing pays a tier for.
type depthPick struct {
	model, depth, key, sku, free string
	row                          map[string]string
	served                       bool
	why                          string
}

// pickDepth reads router.depth for model at the request's reasoning depth. ok is false
// when model is no router id: it has no row, or its row's tiers belong to no family
// with a free SKU.
func pickDepth(model string, body []byte) (depthPick, bool) {
	cfg := GetModelConfig()
	if cfg == nil {
		return depthPick{}, false
	}
	row := cfg.depthTable(model)
	free := freeOf(row)
	if free == "" {
		return depthPick{}, false
	}
	p := depthPick{model: model, depth: requestDepth(body), row: row, free: free}
	p.key = p.depth
	if p.sku = row[p.key]; p.sku == "" {
		p.key, p.sku = DepthDefault, row[DepthDefault]
	}
	if p.sku == "" {
		return p, true
	}
	served, ok := familyLookupFresh(p.sku)
	if !ok || !served.priced() || served.gated() {
		p.why = p.sku + " is not a paid model open to every caller"
		return p, true
	}
	p.served = true
	return p, true
}

// freeOf is the free SKU of the family a depth row's tiers belong to, "" when no tier
// names a family that has one.
func freeOf(row map[string]string) string {
	for _, sku := range slices.Sorted(maps.Values(row)) {
		name := FamilyOf(sku)
		for _, f := range modelFamilies {
			if name != "" && f.name == name && f.freeName != "" {
				return f.freeName
			}
		}
	}
	return ""
}

// DepthRoute resolves a request naming a router id, a router.depth row (enso,
// enso-auto, hanzo/enso): tier is the paid SKU its reasoning depth names, "" when the
// row names none the family serves to every caller at a price; free is the family's
// free SKU, which answers whatever nothing pays tier for. A router id is no SKU, so
// the request is served as one or the other, never as sent. ok is false for any other
// model.
func DepthRoute(model string, body []byte) (tier, free string, ok bool) {
	p, ok := pickDepth(model, body)
	if !ok {
		return "", "", false
	}
	if p.served {
		tier = p.sku
	}
	return tier, p.free, true
}

// unpaid is model as a request no gate decided who pays for is served: a router id
// is its family's free SKU, since nothing was asked to pay for a tier; any other model
// is itself.
func unpaid(model string) string {
	if free := freeOf(depthRow(model)); free != "" {
		return free
	}
	return model
}

// depthRow is the router.depth row for model, nil when it has none or no table is
// loaded.
func depthRow(model string) map[string]string {
	cfg := GetModelConfig()
	if cfg == nil {
		return nil
	}
	return cfg.depthTable(model)
}

// Routing is what a router decided for one request, published beside the answer as
// its `routing` object and on its trace as gen_ai.hanzo.routing. Every field is one
// the router read or wrote; what it does not weigh (a score, an expected cost or
// latency, the context size) is not reported.
type Routing struct {
	// Router is the router that decided: "enso".
	Router string `json:"router"`
	// Chosen is the model the request is served at.
	Chosen string `json:"chosen"`
	// Candidates are the models the router chose among: each model its table names,
	// with the depths that read it, shallow to deep.
	Candidates []Candidate `json:"candidates"`
	// Inputs are what it decided on.
	Inputs RoutingInputs `json:"inputs"`
	// Reason says, in a sentence, how the inputs led to Chosen.
	Reason string `json:"reason"`
}

// Candidate is one model a router could have chosen, and the depths that read it.
type Candidate struct {
	Model  string   `json:"model"`
	Depths []string `json:"depths"`
}

// RoutingInputs are what Enso decides on: the reasoning depth the request states, the
// table key that depth was read at, and whether the tier at that depth is paid for
// ("funded", "unfunded", or "n/a" where no paid tier is in question).
type RoutingInputs struct {
	Depth  string `json:"depth"`
	Key    string `json:"key"`
	Policy string `json:"policy"`
}

// depthOrder is the order a table's rows are read in: shallow to deep. A key the order
// does not name follows, by name.
var depthOrder = []string{"off", "none", "minimal", "low", "medium", DepthDefault, "high", "xhigh", "max"}

// tiers are the models a depth row names, each with the depths that read it,
// shallow to deep.
func tiers(row map[string]string) []Candidate {
	keys := slices.Clone(depthOrder)
	rest := make([]string, 0, len(row))
	for d := range row {
		if !slices.Contains(depthOrder, d) {
			rest = append(rest, d)
		}
	}
	slices.Sort(rest)
	var out []Candidate
	at := map[string]int{}
	for _, d := range append(keys, rest...) {
		sku := row[d]
		if sku == "" {
			continue
		}
		i, ok := at[sku]
		if !ok {
			i = len(out)
			at[sku] = i
			out = append(out, Candidate{Model: sku})
		}
		out[i].Depths = append(out[i].Depths, d)
	}
	return out
}

// DepthRouting is what Enso's router did for a request naming model, nil when model
// is no router id. why is "" when the tier the request's depth names answers it, and
// otherwise why nothing pays for that tier, so the family's free SKU answers instead.
func DepthRouting(model string, body []byte, why string) *Routing {
	p, ok := pickDepth(model, body)
	if !ok {
		return nil
	}
	r := &Routing{Router: "enso", Chosen: p.free, Candidates: tiers(p.row),
		Inputs: RoutingInputs{Depth: p.depth, Key: p.key, Policy: "n/a"}}
	read := fmt.Sprintf("%s reasoning", p.depth)
	if p.key != p.depth {
		read = fmt.Sprintf("%s reasoning (read at %s)", p.depth, p.key)
	}
	switch {
	case p.sku == "":
		r.Reason = fmt.Sprintf("The depth table names no model for %s, so %s serves.", read, p.free)
	case !p.served:
		r.Reason = fmt.Sprintf("%s names %s, but %s, so %s serves.", read, p.sku, p.why, p.free)
	case why == "":
		r.Chosen, r.Inputs.Policy = p.sku, "funded"
		r.Reason = fmt.Sprintf("%s names %s, and your plan or balance pays for it.", read, p.sku)
	default:
		r.Inputs.Policy = "unfunded"
		r.Reason = fmt.Sprintf("%s names %s, but %s, so %s serves at no charge.", read, p.sku, why, p.free)
	}
	return r
}

// depthListings lists each router id router.depth names (depthListing) in place of
// any row of that id, sorted by id as the catalogue is.
func depthListings(cfg *ModelConfig, models []modelInfo) []modelInfo {
	at := make(map[string]int, len(models))
	for i, m := range models {
		at[strings.ToLower(m.ID)] = i
	}
	listed := func(id string) (modelInfo, bool) {
		i, ok := at[strings.ToLower(id)]
		if !ok {
			return modelInfo{}, false
		}
		return models[i], true
	}
	for _, id := range cfg.routers() {
		info, ok := depthListing(id, cfg.depthTable(id), listed)
		if !ok {
			continue
		}
		if i, ok := at[id]; ok {
			models[i] = info
			continue
		}
		at[id] = len(models)
		models = append(models, info)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models
}

// depthListing is router id's row in /v1/models: the listed row of the tier a request
// stating no depth is served at, under the router's id, holding the narrowest window
// of its tiers (the prompt every tier it may answer on can hold), and saying what it
// routes to. ok is false when its row names no family with a free SKU or its default
// tier is not listed.
func depthListing(id string, row map[string]string, listed func(string) (modelInfo, bool)) (modelInfo, bool) {
	free := freeOf(row)
	info, ok := listed(row[DepthDefault])
	if free == "" || !ok {
		return modelInfo{}, false
	}
	var said []string
	for _, c := range tiers(row) {
		said = append(said, fmt.Sprintf("%s at %s", c.Model, strings.Join(c.Depths, ", ")))
		if t, ok := listed(c.Model); ok && t.ContextWindow > 0 && t.ContextWindow < info.ContextWindow {
			info.ContextWindow = t.ContextWindow
		}
	}
	info.ID, info.Name, info.Access = id, "Enso", nil
	info.Description = fmt.Sprintf("Routes each request to one tier by the reasoning depth it asks for: %s. "+
		"It bills at the price of the tier that answers; the price shown is a request's that states no depth. "+
		"When nothing pays for that tier it answers on %s, at no charge, and says so.", strings.Join(said, "; "), free)
	return info, true
}

// listedAs is the id a listed row is priced by: a router id's default tier, any other
// id itself.
func listedAs(id string) string {
	if row := depthRow(id); freeOf(row) != "" && row[DepthDefault] != "" {
		return row[DepthDefault]
	}
	return id
}

// routingKey is where the gate leaves a request's routing for its answer.
type routingKey struct{}

// SetRouting leaves r for the answer to this request to publish.
func SetRouting(c *zip.Ctx, r *Routing) { c.Locals(routingKey{}, r) }

// routingOf is the routing the gate left for this request, encoded; nil when none.
func routingOf(c *zip.Ctx) json.RawMessage {
	r, ok := c.Locals(routingKey{}).(*Routing)
	if !ok || r == nil {
		return nil
	}
	out, err := json.Marshal(r)
	if err != nil {
		return nil
	}
	return out
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
