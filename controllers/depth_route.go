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
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/router"
	"github.com/hanzoai/thinking"
	"github.com/zap-proto/zip"
)

// DepthDefault is the router.depth key a request the router decides starts at: the
// tier it is served at unless the router or Kai judges it needs more.
const DepthDefault = "default"

// The effort words a request states (chat `reasoning_effort`, Responses
// `reasoning.effort`), beside the depth words router.depth is keyed by. Auto, or no
// word at all, leaves the tier to the router; instant asks for no reasoning.
const (
	EffortAuto    = "auto"
	EffortInstant = "instant"
)

// effort folds an effort word onto the ladder router.depth is keyed by: auto for no
// word, instant for none, minimal and off, max for xhigh; any other word as written.
func effort(word string) string {
	switch w := strings.ToLower(strings.TrimSpace(word)); w {
	case "", DepthDefault, EffortAuto:
		return EffortAuto
	case "none", "minimal", "off", EffortInstant:
		return EffortInstant
	case "xhigh":
		return "max"
	default:
		return w
	}
}

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

// depthPick is what Enso's router decided for one request naming a router id: the row
// it read, the depth the request states and the key it read the row at, the tier
// there, whether the family serves that tier to every caller at a price (why, when it
// does not), and, for a request that left its effort to the router, the task the
// router read it as and what chose the tier.
type depthPick struct {
	model, depth, key, sku string
	row                    map[string]string
	served                 bool
	why                    string
	// auto: the request stated no effort, or auto, so the router chooses.
	auto bool
	// by is what chose the tier: "effort" (the word the request sent), "default"
	// (the row's start), "router" (its table for the task) or "kai" (its effort
	// decision).
	by   string
	task string
}

// pickDepth reads router.depth for model at the request's effort, before the router
// weighs it. ok is false when model is no router id: it has no row, or its row's tiers
// belong to no Hanzo family.
func pickDepth(model string, body []byte) (depthPick, bool) {
	row := depthRow(model)
	if !routerRow(row) {
		return depthPick{}, false
	}
	p := depthPick{model: model, depth: requestDepth(body), row: row}
	switch {
	case effort(p.depth) == EffortAuto:
		p.auto, p.key, p.by = true, DepthDefault, "default"
	case row[p.depth] != "":
		p.key, p.by = p.depth, "effort"
	case row[effort(p.depth)] != "":
		p.key, p.by = effort(p.depth), "effort"
	default:
		// A word the ladder does not hold is no effort the caller can be held to: the
		// router decides, as for none.
		p.auto, p.key, p.by = true, DepthDefault, "default"
	}
	p.sku = row[p.key]
	p.check()
	return p, true
}

// check settles whether the tier is one the family serves to every caller at a price.
func (p *depthPick) check() {
	p.served, p.why = false, ""
	if p.sku == "" {
		return
	}
	served, ok := familyLookupFresh(p.sku)
	if !ok || !served.priced() || served.gated() {
		p.why = p.sku + " is not a model open to every caller"
		return
	}
	p.served = true
}

// routerRow reports whether a depth row is a router's: some tier of it is a Hanzo
// family's SKU.
func routerRow(row map[string]string) bool {
	for _, sku := range row {
		if FamilyOf(sku) != "" {
			return true
		}
	}
	return false
}

// rank is each tier of a row by how deep it reads, shallow first.
func rank(row map[string]string) map[string]int {
	out := map[string]int{}
	for i, c := range tiers(row) {
		out[c.Model] = i
	}
	return out
}

// escalate lets the router move a request that left its effort to it onto a deeper
// tier, never a shallower one: the tier the router's table (prefer, org > "*" >
// conf) names first for the task router.Classify reads the request as, and the tier
// Kai's effort decision names when it answers at its floor. The deepest of those and
// the row's start serves, and what chose it is said (by). The decision is filed for
// the router's trainer as auto's are (routingEventSink).
func (p *depthPick) escalate(ctx context.Context, org, user, rid string, body []byte) {
	if !p.auto {
		return
	}
	at := rank(p.row)
	take := func(sku, by string) {
		if r, ok := at[sku]; ok && (p.sku == "" || r > at[p.sku]) {
			p.sku, p.by = sku, by
		}
	}
	ask := routerAsk(body)
	task := router.Classify(ask.req)
	p.task = string(task)
	if cfg := GetModelConfig(); cfg != nil {
		conf, _ := cfg.ConfRouterPolicy()
		pol := router.Policy{Prefer: effectiveRouterPrefer(org, conf)}
		if sku := pol.ForTask(task, func(id string) bool { _, ok := at[strings.ToLower(id)]; return ok }); sku != "" {
			take(strings.ToLower(sku), "router")
		}
	}
	if word, ok := kaiEffort(ctx, org, rid, ask, p.task); ok {
		take(p.row[effort(word)], "kai")
	}
	p.check()
	routingEventSink(object.RoutingEvent{
		Owner: org, User: user, RequestId: rid, Task: p.task,
		RequestedModel: strings.ToLower(strings.TrimSpace(p.model)), RoutedModel: p.sku, Source: "enso-" + p.by,
	})
}

// Depth is what Enso's router decided for a request naming a router id.
type Depth struct{ p depthPick }

// RouteDepth resolves a request naming a router id (router.depth: enso, enso-auto,
// hanzo/enso) to one tier: the one the effort it states names, or, for one that states
// none or auto, the one the router chooses (escalate). org, user and rid are the
// caller's, for the router's table and its ledger. ok is false for any other model.
func RouteDepth(ctx context.Context, model string, body []byte, org, user, rid string) (*Depth, bool) {
	p, ok := pickDepth(model, body)
	if !ok {
		return nil, false
	}
	p.escalate(ctx, org, user, rid, body)
	return &Depth{p: p}, true
}

// Tier is the SKU the request is served at, "" when its row names none the family
// serves to every caller at a price.
func (d *Depth) Tier() string {
	if d == nil || !d.p.served {
		return ""
	}
	return d.p.sku
}

// Routing is the decision as the answer publishes it. unfunded is why nothing pays
// for the tier, "" when something does.
func (d *Depth) Routing(unfunded string) *Routing {
	if d == nil {
		return nil
	}
	return d.p.routing(unfunded)
}

// DepthRoute resolves a router id at the effort body states, the router not asked:
// tier is the SKU it names ("" when its row names none the family serves to every
// caller at a price) and floor the row's start, the tier a request that states no
// effort is quoted at. ok is false for any other model.
func DepthRoute(model string, body []byte) (tier, floor string, ok bool) {
	p, ok := pickDepth(model, body)
	if !ok {
		return "", "", false
	}
	if p.served {
		tier = p.sku
	}
	return tier, p.row[DepthDefault], true
}

// unpaid is model as a request no gate decided who pays for is served: a router id is
// its row's start, served on the tier's no-cost rungs (no gate marked it otherwise);
// any other model is itself.
func unpaid(model string) string {
	if row := depthRow(model); routerRow(row) && row[DepthDefault] != "" {
		return row[DepthDefault]
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
	// By is what chose the tier: "effort" (the request's word), "default" (the
	// router's start), "router" (its table for the task) or "kai".
	By string `json:"by,omitempty"`
}

// Candidate is one model a router could have chosen, and the depths that read it.
type Candidate struct {
	Model  string   `json:"model"`
	Depths []string `json:"depths"`
}

// RoutingInputs are what Enso decides on: the reasoning depth the request states, the
// table key that depth was read at, the task the router read the request as (for a
// request that left its effort to the router), and whether the tier is paid for
// ("funded"; "unfunded", served on its no-cost rungs; or "n/a" where no tier is in
// question).
type RoutingInputs struct {
	Depth  string `json:"depth"`
	Key    string `json:"key"`
	Task   string `json:"task,omitempty"`
	Policy string `json:"policy"`
}

// depthOrder is the order a table's rows are read in: shallow to deep. A key the order
// does not name follows, by name.
var depthOrder = []string{EffortInstant, "off", "none", "minimal", DepthDefault, "low", "medium", "high", "xhigh", "max"}

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

// DepthRouting is what Enso's router reads for a request naming model at the effort
// it states, the router not asked; nil when model is no router id. why is "" when
// something pays for the tier, and otherwise why nothing does, so the tier is served
// on its no-cost rungs.
func DepthRouting(model string, body []byte, why string) *Routing {
	p, ok := pickDepth(model, body)
	if !ok {
		return nil
	}
	return p.routing(why)
}

// routing is the decision as the answer publishes it.
func (p depthPick) routing(why string) *Routing {
	r := &Routing{Router: "enso", Chosen: p.sku, Candidates: tiers(p.row), By: p.by,
		Inputs: RoutingInputs{Depth: p.depth, Key: p.key, Task: p.task, Policy: "n/a"}}
	read := fmt.Sprintf("%s effort", p.depth)
	if p.auto {
		read = "the router"
		if p.task != "" {
			read = fmt.Sprintf("the router, reading a %s request,", p.task)
		}
	} else if p.key != p.depth {
		read = fmt.Sprintf("%s effort (read at %s)", p.depth, p.key)
	}
	switch {
	case p.sku == "":
		r.Reason = fmt.Sprintf("The depth table names no model for %s.", read)
	case !p.served:
		r.Reason = fmt.Sprintf("%s names %s, but %s.", read, p.sku, p.why)
	case why == "":
		r.Inputs.Policy = "funded"
		r.Reason = fmt.Sprintf("%s names %s, and your plan or balance pays for it.", read, p.sku)
	default:
		r.Inputs.Policy = "unfunded"
		r.Reason = fmt.Sprintf("%s names %s, but %s, so %s serves at no charge on its no-cost rungs.", read, p.sku, why, p.sku)
	}
	if p.by == "kai" {
		r.Reason += " Kai judged the request needs it."
	}
	r.Reason = strings.ToUpper(r.Reason[:1]) + r.Reason[1:]
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
	info, ok := listed(row[DepthDefault])
	if !routerRow(row) || !ok {
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
	info.Description = fmt.Sprintf("Routes each request to one tier: %s. With no effort, or auto, the router "+
		"chooses, starting at %s and going deeper only when it or Kai judges the request needs it. "+
		"It bills at the price of the tier that answers; the price shown is the router's start. "+
		"When nothing pays for a tier it answers on that tier's no-cost rungs, at no charge, and says so.",
		strings.Join(said, "; "), row[DepthDefault])
	return info, true
}

// listedAs is the id a listed row is priced by: a router id's default tier, any other
// id itself.
func listedAs(id string) string {
	if row := depthRow(id); routerRow(row) && row[DepthDefault] != "" {
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
