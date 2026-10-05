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
	"slices"
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

// depthPick is what router.depth says for one request: the row it read, the depth the
// request states and the key it was read at, the SKU there, and whether that SKU may
// lift the request (why, when it may not).
type depthPick struct {
	model, depth, key, sku string
	row                    map[string]string
	lift                   bool
	why                    string
}

// pickDepth reads router.depth for model at the request's reasoning depth. ok is false
// when model has no row: it is not depth-routed.
func pickDepth(model string, body []byte) (depthPick, bool) {
	cfg := GetModelConfig()
	if cfg == nil {
		return depthPick{}, false
	}
	row := cfg.depthTable(model)
	if len(row) == 0 {
		return depthPick{}, false
	}
	p := depthPick{model: model, depth: requestDepth(body), row: row}
	p.key = p.depth
	if p.sku = row[p.key]; p.sku == "" {
		p.key, p.sku = DepthDefault, row[DepthDefault]
	}
	if p.sku == "" {
		p.why = "names no model for this depth"
		return p, true
	}
	asked, ok := familyLookupFresh(model)
	if !ok || asked.priced() {
		p.why = model + " is not a free id the table lifts"
		return p, true
	}
	served, ok := familyLookupFresh(p.sku)
	if !ok || !served.priced() || served.gated() {
		p.why = p.sku + " is not a paid model open to every caller"
		return p, true
	}
	p.lift = true
	return p, true
}

// DepthRoute names the priced SKU a request for model is served at when its caller
// funds it: the router.depth row for model, read at the request's reasoning depth.
//
// It answers only for a model the family serves at no price, and only with a SKU the
// family serves at a price and without a grant, so it can lift a free call onto the
// paid ladder and never the reverse. ok is false for every other request, which then
// runs as sent.
func DepthRoute(model string, body []byte) (string, bool) {
	p, ok := pickDepth(model, body)
	if !ok || !p.lift {
		return "", false
	}
	return p.sku, true
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
// table key that depth was read at, and whether the caller's plan or credit pays for
// the paid model at that depth ("funded", "unfunded", or "n/a" where no paid model is
// in question).
type RoutingInputs struct {
	Depth  string `json:"depth"`
	Key    string `json:"key"`
	Policy string `json:"policy"`
}

// depthOrder is the order a table's rows are read in: shallow to deep. A key the order
// does not name follows, by name.
var depthOrder = []string{"off", "none", "minimal", "low", "medium", DepthDefault, "high", "xhigh", "max"}

// DepthRouting is the routing Enso did for a request naming model: nil when model is
// not depth-routed. funded says whether the caller's plan or credit pays for the SKU
// the table names, which is the gate's answer and only asked when that SKU may lift.
func DepthRouting(model string, body []byte, funded bool) *Routing {
	p, ok := pickDepth(model, body)
	if !ok {
		return nil
	}
	r := &Routing{Router: "enso", Chosen: model, Inputs: RoutingInputs{Depth: p.depth, Key: p.key, Policy: "n/a"}}
	keys := slices.Clone(depthOrder)
	rest := make([]string, 0, len(p.row))
	for d := range p.row {
		if !slices.Contains(depthOrder, d) {
			rest = append(rest, d)
		}
	}
	slices.Sort(rest)
	at := map[string]int{}
	for _, d := range append(keys, rest...) {
		sku := p.row[d]
		if sku == "" {
			continue
		}
		i, ok := at[sku]
		if !ok {
			i = len(r.Candidates)
			at[sku] = i
			r.Candidates = append(r.Candidates, Candidate{Model: sku})
		}
		r.Candidates[i].Depths = append(r.Candidates[i].Depths, d)
	}
	read := fmt.Sprintf("%s reasoning", p.depth)
	if p.key != p.depth {
		read = fmt.Sprintf("%s reasoning (read at %s)", p.depth, p.key)
	}
	switch {
	case p.sku == "":
		r.Reason = fmt.Sprintf("The depth table %s, so %s serves as asked.", p.why, model)
	case !p.lift:
		r.Reason = fmt.Sprintf("%s names %s, but %s, so %s serves as asked.", read, p.sku, p.why, model)
	case funded:
		r.Chosen, r.Inputs.Policy = p.sku, "funded"
		r.Reason = fmt.Sprintf("%s names %s, and your plan or credit pays for it.", read, p.sku)
	default:
		r.Inputs.Policy = "unfunded"
		r.Reason = fmt.Sprintf("%s names %s, which your plan and credit do not pay for, so %s serves.", read, p.sku, model)
	}
	return r
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
