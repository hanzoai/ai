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
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/decimal"
)

// depthConfig loads a model table carrying the router.depth rows and the router's
// prefer table as universe declares them (values/hanzo/cloud.yaml), through the same
// file loader production uses.
func depthConfig(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models.yaml")
	const doc = `
version: 1
router:
  enabled: true
  depth:
    enso-auto: &enso
      default: enso-flash
      instant: enso-flash
      "off": enso-flash
      none: enso-flash
      minimal: enso-flash
      low: enso-flash
      medium: enso-pro
      high: enso-max
      xhigh: enso-ultra
      max: enso-ultra
    ENSO: *enso
  prefer:
    cheap_chat: [enso-flash]
    creative: [enso-flash]
    default: [enso-flash]
    vision: [enso-flash]
    code: [enso-pro]
    long_context: [enso-pro]
    reasoning: [enso-pro]
    math: [enso-max]
models: {}
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := globalModelConfig
	mc := &ModelConfig{}
	if err := mc.loadFromFile(path); err != nil {
		t.Fatal(err)
	}
	globalModelConfig = mc
	t.Cleanup(func() { globalModelConfig = prev })
}

// seedEnso stands in for enso discovery: the four priced tiers, and one priced SKU
// held behind a grant. The router ids are no SKUs and are not listed.
func seedEnso(t *testing.T) {
	t.Helper()
	price := func(in, out string, ctx int) zenTier {
		return zenTier{MaxCtx: ctx, In: decimal.MustParse(in), Out: decimal.MustParse(out)}
	}
	saved := ensoFam.byID
	ensoFam.byID = map[string]zenModel{
		"enso-flash":   {ID: "enso-flash", Base: price("0.33", "1.65", 1_000_000), Tiers: []zenTier{price("0.33", "1.65", 1_000_000)}},
		"enso-pro":     {ID: "enso-pro", Base: price("3.30", "16.50", 1_000_000), Tiers: []zenTier{price("3.30", "16.50", 1_000_000)}},
		"enso-max":     {ID: "enso-max", Base: price("4.30", "21.50", 1_000_000), Tiers: []zenTier{price("4.30", "21.50", 1_000_000)}},
		"enso-ultra":   {ID: "enso-ultra", Base: price("5.236", "26.18", 1_000_000), Tiers: []zenTier{price("5.236", "26.18", 1_000_000)}},
		"enso-preview": {ID: "enso-preview", Base: price("1", "1", 1_000_000), Tiers: []zenTier{price("1", "1", 1_000_000)}, Access: "waitlist"},
	}
	t.Cleanup(func() { ensoFam.byID = saved })
}

// noKai answers no Kai, and files the router's decisions where a test reads them.
func noKai(t *testing.T) *[]object.RoutingEvent {
	t.Helper()
	prevAsk, prevSink := askKai, routingEventSink
	askKai = func(context.Context, string, string, []byte) ([]byte, bool) { return nil, false }
	var got []object.RoutingEvent
	routingEventSink = func(e object.RoutingEvent) { got = append(got, e) }
	t.Cleanup(func() { askKai, routingEventSink = prevAsk, prevSink })
	return &got
}

// THE EFFORT LADDER, every value and alias, on enso-auto: a stated word names its
// tier, and a request that states none, or auto, or a word the ladder does not hold,
// is the router's; a trivial one stays at its start. Every dialect that states a
// depth reads the same.
func TestTheEffortLadderNamesEveryTier(t *testing.T) {
	depthConfig(t)
	seedEnso(t)
	noKai(t)
	for _, tc := range []struct {
		body, want, by string
	}{
		{`{"model":"enso-auto","messages":[{"role":"user","content":"hi"}]}`, "enso-flash", "default"},
		{`{"model":"enso-auto","reasoning_effort":"auto"}`, "enso-flash", "default"},
		{`{"model":"enso-auto","reasoning_effort":"instant"}`, "enso-flash", "effort"},
		{`{"model":"enso-auto","reasoning_effort":"none"}`, "enso-flash", "effort"},
		{`{"model":"enso-auto","reasoning_effort":"minimal"}`, "enso-flash", "effort"},
		{`{"model":"enso-auto","reasoning_effort":"low"}`, "enso-flash", "effort"},
		{`{"model":"enso-auto","reasoning_effort":"medium"}`, "enso-pro", "effort"},
		{`{"model":"enso-auto","reasoning_effort":"HIGH"}`, "enso-max", "effort"},
		{`{"model":"enso-auto","reasoning_effort":"max"}`, "enso-ultra", "effort"},
		{`{"model":"enso-auto","reasoning_effort":"xhigh"}`, "enso-ultra", "effort"},
		{`{"model":"enso-auto","reasoning":{"effort":"auto"}}`, "enso-flash", "default"},
		{`{"model":"enso-auto","reasoning":{"effort":"high"}}`, "enso-max", "effort"},
		{`{"model":"enso-auto","reasoning":{"effort":"none"}}`, "enso-flash", "effort"},
		{`{"model":"enso-auto","reasoning":{"enabled":false}}`, "enso-flash", "effort"},
		{`{"model":"enso-auto","reasoning":{"max_tokens":32000}}`, "enso-ultra", "effort"},
		{`{"model":"enso-auto","thinking":{"type":"enabled","budget_tokens":2048}}`, "enso-flash", "effort"},
		{`{"model":"enso-auto","thinking":{"type":"enabled","budget_tokens":20000}}`, "enso-ultra", "effort"},
		{`{"model":"enso-auto","thinking":{"type":"disabled"}}`, "enso-flash", "effort"},
		{`{"model":"enso-auto","reasoning_effort":"a word nobody defined"}`, "enso-flash", "default"},
		{`not json`, "enso-flash", "default"},
	} {
		d, ok := RouteDepth(context.Background(), "enso-auto", []byte(tc.body), "acme", "acme/alice", "r1")
		if !ok || d.Tier() != tc.want || d.p.by != tc.by {
			t.Errorf("%s -> (%q by %q, %v), want %q by %q", tc.body, d.Tier(), d.p.by, ok, tc.want, tc.by)
		}
	}
	if tier, floor, ok := DepthRoute("Enso", []byte(`{"reasoning_effort":"high"}`)); !ok || tier != "enso-max" || floor != "enso-flash" {
		t.Errorf("the old name routes like enso-auto, got (%q, %q, %v)", tier, floor, ok)
	}
}

// The router decides a request that left its effort to it: its table names the tier
// for the task it reads the request as, and the deeper of that and the start serves.
// A stated effort is never moved by the router. Every decision is filed for the
// router's trainer.
func TestTheRouterEscalatesOnlyWhatItJudgesNeedsMore(t *testing.T) {
	depthConfig(t)
	seedEnso(t)
	filed := noKai(t)
	for _, tc := range []struct {
		say, effort, want, by, task string
	}{
		{"Say hi in one word.", "", "enso-flash", "default", "cheap_chat"},
		{"Write a poem about the sea.", "", "enso-flash", "default", "creative"},
		{"Fix this bug: the function returns nil on an empty list.", "", "enso-pro", "router", "code"},
		{"Prove step by step that there are infinitely many primes p = 3 mod 4.", "", "enso-max", "router", "math"},
		{"Prove step by step that there are infinitely many primes p = 3 mod 4.", "low", "enso-flash", "effort", ""},
	} {
		msg, _ := json.Marshal(tc.say)
		body := `{"model":"enso-auto","messages":[{"role":"user","content":` + string(msg) + `}]`
		if tc.effort != "" {
			body += `,"reasoning_effort":"` + tc.effort + `"`
		}
		body += `}`
		d, _ := RouteDepth(context.Background(), "enso-auto", []byte(body), "acme", "acme/alice", "r1")
		if d.Tier() != tc.want || d.p.by != tc.by || d.p.task != tc.task {
			t.Errorf("%q effort %q -> %q by %q task %q, want %q by %q task %q", tc.say, tc.effort, d.Tier(), d.p.by, d.p.task, tc.want, tc.by, tc.task)
		}
		r := d.Routing("")
		if r.Chosen != tc.want || r.By != tc.by || r.Inputs.Policy != "funded" {
			t.Errorf("routing = %+v", r)
		}
	}
	if len(*filed) != 4 {
		t.Fatalf("filed %d decisions, want the four the router made", len(*filed))
	}
	if e := (*filed)[3]; e.RequestedModel != "enso-auto" || e.RoutedModel != "enso-max" || e.Task != "math" || e.Source != "enso-router" || e.Owner != "acme" {
		t.Errorf("filed %+v", e)
	}
}

// Kai is asked how much reasoning a request the router decides needs, as one choice
// over the ladder's words; its answer moves the request deeper only at its floor, and
// never shallower than the router's own decision.
func TestKaiEscalatesAtItsFloorAndNeverDown(t *testing.T) {
	depthConfig(t)
	seedEnso(t)
	noKai(t)
	var asked map[string]any
	answer := func(choice string, p float64) {
		askKai = func(_ context.Context, org, _ string, body []byte) ([]byte, bool) {
			_ = json.Unmarshal(body, &asked)
			b, _ := json.Marshal(map[string]any{"answers": map[string]any{"effort": map[string]any{"type": "choice", "choice": choice, "answer_confidence": p}}})
			return b, org == "acme"
		}
	}
	route := func(say string) *Depth {
		msg, _ := json.Marshal(say)
		d, _ := RouteDepth(context.Background(), "enso-auto", []byte(`{"model":"enso-auto","messages":[{"role":"user","content":`+string(msg)+`}],"tools":[{"type":"function"}]}`), "acme", "acme/alice", "r1")
		return d
	}

	answer("max", 0.95)
	if d := route("Plan the migration of our billing ledger."); d.Tier() != "enso-ultra" || d.p.by != "kai" {
		t.Errorf("Kai at 0.95 for max: %q by %q, want enso-ultra by kai", d.Tier(), d.p.by)
	}
	if asked["model"] != "kai" {
		t.Errorf("asked %v, want kai", asked["model"])
	}
	q := asked["questions"].(map[string]any)["effort"].(map[string]any)
	labels := q["criteria"].(map[string]any)
	for _, w := range []string{"instant", "low", "medium", "high", "max"} {
		if _, ok := labels[w]; !ok || q["type"] != "choice" {
			t.Errorf("the question %v lacks the ladder's word %q", q, w)
		}
	}
	state := asked["state"].(map[string]any)
	if state["request"] != "Plan the migration of our billing ledger." || state["tools"] != float64(1) || state["turns"] != float64(1) {
		t.Errorf("state = %v", state)
	}

	answer("max", 0.85)
	if d := route("Plan the migration of our billing ledger."); d.Tier() != "enso-flash" || d.p.by != "default" {
		t.Errorf("Kai under its floor moved the request: %q by %q", d.Tier(), d.p.by)
	}
	answer("low", 0.99)
	if d := route("Prove that the square root of two is irrational."); d.Tier() != "enso-max" || d.p.by != "router" {
		t.Errorf("Kai's low moved a math request off the router's tier: %q by %q", d.Tier(), d.p.by)
	}
	answer("deeper", 0.99)
	if d := route("Plan the migration of our billing ledger."); d.Tier() != "enso-flash" {
		t.Errorf("a label Kai was not asked for moved the request: %q", d.Tier())
	}
}

// A Kai that has not answered within its budget leaves the router's decision: the
// request is not held past kaiBudget.
func TestASlowKaiIsNotWaitedFor(t *testing.T) {
	depthConfig(t)
	seedEnso(t)
	noKai(t)
	askKai = func(ctx context.Context, _, _ string, _ []byte) ([]byte, bool) {
		<-ctx.Done()
		return nil, false
	}
	start := time.Now()
	d, _ := RouteDepth(context.Background(), "enso-auto", []byte(`{"messages":[{"role":"user","content":"Plan a trip."}]}`), "acme", "", "r1")
	if took := time.Since(start); d.Tier() != "enso-flash" || took > kaiBudget+300*time.Millisecond {
		t.Errorf("served %q after %s, want enso-flash within Kai's budget", d.Tier(), took)
	}
}

// Only a router id is routed, and only onto a tier the family serves at a price
// without a grant; a request no gate decided for is served at the row's start.
func TestDepthRouteRoutesOnlyARouterIdOntoAPaidTier(t *testing.T) {
	depthConfig(t)
	seedEnso(t)
	for _, model := range []string{"enso-flash", "enso-ultra", "zen6", "gpt-6-sol", ""} {
		if tier, floor, ok := DepthRoute(model, []byte(`{"reasoning_effort":"high"}`)); ok {
			t.Errorf("%q is no router id and was routed to (%q, %q)", model, tier, floor)
		}
		if got := unpaid(model); got != model {
			t.Errorf("unpaid(%q) = %q, want it unchanged", model, got)
		}
	}
	for _, model := range []string{"enso-auto", "ENSO"} {
		if got := unpaid(model); got != "enso-flash" {
			t.Errorf("unpaid(%q) = %q, want the row's start", model, got)
		}
	}
	for _, sku := range []string{"enso-gone", "enso-preview"} {
		globalModelConfig.mu.Lock()
		globalModelConfig.router.Depth["enso-auto"]["high"] = sku
		globalModelConfig.mu.Unlock()
		if tier, _, ok := DepthRoute("enso-auto", []byte(`{"reasoning_effort":"high"}`)); !ok || tier != "" {
			t.Errorf("a row naming %q: (%q, %v), want no tier", sku, tier, ok)
		}
	}
	globalModelConfig = nil
	if tier, floor, ok := DepthRoute("enso-auto", []byte(`{}`)); ok {
		t.Errorf("no model table routed to (%q, %q)", tier, floor)
	}
}

// WithModel changes the model and nothing else.
func TestWithModelChangesOnlyTheModel(t *testing.T) {
	in := []byte(`{"model":"enso-auto","stream":true,"messages":[{"role":"user","content":"a <b> & c"}],"reasoning_effort":"high","n":1}`)
	out, ok := WithModel(in, "enso-pro")
	if !ok {
		t.Fatal("a JSON object was refused")
	}
	var a, b map[string]any
	if err := json.Unmarshal(in, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &b); err != nil {
		t.Fatalf("rewritten body is not JSON: %s", out)
	}
	if b["model"] != "enso-pro" {
		t.Fatalf("model = %v", b["model"])
	}
	a["model"] = "enso-pro"
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Errorf("fields changed:\n%s\n%s", ja, jb)
	}
	for _, bad := range []string{``, `null`, `[]`, `"x"`, `not json`} {
		if _, ok := WithModel([]byte(bad), "enso-pro"); ok {
			t.Errorf("%q was rewritten", bad)
		}
	}
}

func TestDepthRoutingSaysWhatEnsoReadAndWhy(t *testing.T) {
	depthConfig(t)
	seedEnso(t)
	high := []byte(`{"model":"enso","reasoning_effort":"high"}`)

	r := DepthRouting("enso", high, "")
	if r == nil || r.Router != "enso" || r.Chosen != "enso-max" || r.By != "effort" {
		t.Fatalf("routing = %+v, want enso chose enso-max by effort", r)
	}
	if r.Inputs != (RoutingInputs{Depth: "high", Key: "high", Policy: "funded"}) {
		t.Fatalf("inputs = %+v", r.Inputs)
	}
	want := []Candidate{
		{Model: "enso-flash", Depths: []string{"instant", "off", "none", "minimal", "default", "low"}},
		{Model: "enso-pro", Depths: []string{"medium"}},
		{Model: "enso-max", Depths: []string{"high"}},
		{Model: "enso-ultra", Depths: []string{"xhigh", "max"}},
	}
	got, _ := json.Marshal(r.Candidates)
	exp, _ := json.Marshal(want)
	if string(got) != string(exp) {
		t.Fatalf("candidates = %s, want %s", got, exp)
	}
	if r.Reason != "High effort names enso-max, and your plan or balance pays for it." {
		t.Fatalf("reason = %q", r.Reason)
	}

	// The same request nothing pays the tier for is served on its no-cost rungs.
	r = DepthRouting("enso", high, "nothing pays for it (insufficient_balance)")
	if r.Chosen != "enso-max" || r.Inputs.Policy != "unfunded" ||
		r.Reason != "High effort names enso-max, but nothing pays for it (insufficient_balance), so enso-max serves at no charge on its no-cost rungs." {
		t.Fatalf("unfunded: %+v %q", r.Inputs, r.Reason)
	}

	// A request stating no effort is the router's, read at its start.
	r = DepthRouting("enso", []byte(`{"model":"enso"}`), "")
	if r.Inputs.Depth != "default" || r.Inputs.Key != "default" || r.Chosen != "enso-flash" || r.By != "default" {
		t.Fatalf("default: %+v chose %q by %q", r.Inputs, r.Chosen, r.By)
	}

	// A tier the family does not serve at a price is said so.
	globalModelConfig.mu.Lock()
	globalModelConfig.router.Depth["enso"]["max"] = "enso-preview"
	globalModelConfig.mu.Unlock()
	r = DepthRouting("enso", []byte(`{"reasoning_effort":"max"}`), "")
	if r.Inputs.Policy != "n/a" || r.Reason != "Max effort names enso-preview, but enso-preview is not a model open to every caller." {
		t.Fatalf("unserved tier: policy %q reason %q", r.Inputs.Policy, r.Reason)
	}

	for _, model := range []string{"enso-pro", "zen5"} {
		if DepthRouting(model, high, "") != nil {
			t.Fatalf("%s has no depth row: want no routing", model)
		}
	}
}

// /v1/models lists each router id: its start's row and price under its own id, the
// narrowest window of its tiers, and a description of where it routes. No free id is
// in the description, and a router whose start is not listed is not listed either.
func TestTheRouterIdsAreListed(t *testing.T) {
	depthConfig(t)
	seedEnso(t)
	models := []modelInfo{
		{ID: "enso-flash", Object: "model", OwnedBy: "hanzo", Premium: true, ContextWindow: 1_000_000, SupportsVision: true},
		{ID: "enso-max", Object: "model", OwnedBy: "hanzo", Premium: true, ContextWindow: 1_000_000},
		{ID: "enso-pro", Object: "model", OwnedBy: "hanzo", Premium: true, ContextWindow: 1_000_000},
		{ID: "enso-ultra", Object: "model", OwnedBy: "hanzo", Premium: true, ContextWindow: 1_000_000},
	}
	out := depthListings(globalModelConfig, models)
	var ids []string
	byID := map[string]modelInfo{}
	for _, m := range out {
		ids = append(ids, m.ID)
		byID[m.ID] = m
	}
	if got := strings.Join(ids, ","); got != "enso,enso-auto,enso-flash,enso-max,enso-pro,enso-ultra" {
		t.Fatalf("listed %s", got)
	}
	for _, id := range []string{"enso", "enso-auto"} {
		m := byID[id]
		if m.Name != "Enso" || !m.Premium || !m.SupportsVision {
			t.Errorf("%s = %+v, want enso-flash's row", id, m)
		}
		for _, says := range []string{"enso-max at high", "enso-ultra at xhigh, max", "starting at enso-flash"} {
			if !strings.Contains(m.Description, says) {
				t.Errorf("%s description %q does not say %q", id, m.Description, says)
			}
		}
		if strings.Contains(m.Description, "free") {
			t.Errorf("%s description names a free model: %q", id, m.Description)
		}
		if got := listedAs(id); got != "enso-flash" {
			t.Errorf("%s is priced as %q, want enso-flash", id, got)
		}
	}
	if out := depthListings(globalModelConfig, models[1:]); len(out) != 3 {
		t.Errorf("without enso-flash listed: %d rows", len(out))
	}
}
