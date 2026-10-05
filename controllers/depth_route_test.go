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
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/decimal"
)

// depthConfig loads a model table carrying the router.depth rows of the enso
// family's default ids, the shape universe declares, through the same file loader
// production uses.
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
      "off": enso-flash
      none: enso-flash
      minimal: enso-flash
      low: enso-flash
      medium: enso-flash
      high: enso-pro
      xhigh: enso-ultra
      max: enso-ultra
    ENSO: *enso
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

// seedEnso stands in for enso discovery: two free ids, three priced rungs, and one
// priced SKU held behind a grant.
func seedEnso(t *testing.T) {
	t.Helper()
	price := func(in, out string) zenTier {
		return zenTier{MaxCtx: 1_000_000, In: decimal.MustParse(in), Out: decimal.MustParse(out)}
	}
	free := zenTier{MaxCtx: 256_000}
	saved := ensoFam.byID
	ensoFam.byID = map[string]zenModel{
		"enso-auto":    {ID: "enso-auto", Base: free, Tiers: []zenTier{free}},
		"enso":         {ID: "enso", Base: free, Tiers: []zenTier{free}},
		"enso-free":    {ID: "enso-free", Base: free, Tiers: []zenTier{free}},
		"enso-flash":   {ID: "enso-flash", Base: price("0.42", "1.50"), Tiers: []zenTier{price("0.42", "1.50")}, Funding: "prepaid"},
		"enso-pro":     {ID: "enso-pro", Base: price("4.20", "13.20"), Tiers: []zenTier{price("4.20", "13.20")}, Funding: "prepaid"},
		"enso-ultra":   {ID: "enso-ultra", Base: price("6.00", "30.00"), Tiers: []zenTier{price("6.00", "30.00")}, Funding: "prepaid"},
		"enso-preview": {ID: "enso-preview", Base: price("1", "1"), Tiers: []zenTier{price("1", "1")}, Access: "waitlist"},
	}
	t.Cleanup(func() { ensoFam.byID = saved })
}

// The depth a request states picks the row, in every dialect a caller sends it in,
// and a request that states none takes the default.
func TestDepthRouteReadsTheDepthEveryDialectStates(t *testing.T) {
	depthConfig(t)
	seedEnso(t)
	for _, tc := range []struct {
		body string
		want string
	}{
		{`{"model":"enso-auto","messages":[]}`, "enso-flash"},
		{`{"model":"enso-auto","reasoning_effort":"minimal"}`, "enso-flash"},
		{`{"model":"enso-auto","reasoning_effort":"HIGH"}`, "enso-pro"},
		{`{"model":"enso-auto","reasoning_effort":"xhigh"}`, "enso-ultra"},
		{`{"model":"enso-auto","reasoning":{"effort":"max"}}`, "enso-ultra"},
		{`{"model":"enso-auto","reasoning":{"effort":"none"}}`, "enso-flash"},
		{`{"model":"enso-auto","reasoning":{"enabled":false}}`, "enso-flash"},
		{`{"model":"enso-auto","reasoning":{"max_tokens":32000}}`, "enso-ultra"},
		{`{"model":"enso-auto","thinking":{"type":"enabled","budget_tokens":2048}}`, "enso-flash"},
		{`{"model":"enso-auto","thinking":{"type":"enabled","budget_tokens":20000}}`, "enso-ultra"},
		{`{"model":"enso-auto","thinking":{"type":"disabled"}}`, "enso-flash"},
		{`{"model":"enso-auto","reasoning_effort":"a word nobody defined"}`, "enso-flash"},
		{`not json`, "enso-flash"},
	} {
		got, ok := DepthRoute("enso-auto", []byte(tc.body))
		if !ok || got != tc.want {
			t.Errorf("%s → (%q, %v), want %q", tc.body, got, ok, tc.want)
		}
	}
	if got, ok := DepthRoute("Enso", []byte(`{"reasoning_effort":"high"}`)); !ok || got != "enso-pro" {
		t.Errorf("the old name routes like enso-auto, got (%q, %v)", got, ok)
	}
}

// Only a free id with a row is lifted, and only onto a priced SKU the family serves
// without a grant. Everything else runs as sent.
func TestDepthRouteLiftsOnlyAFreeIdOntoAPricedSKU(t *testing.T) {
	depthConfig(t)
	seedEnso(t)
	for _, model := range []string{"enso-free", "enso-flash", "enso-ultra", "zen-free", "gpt-6-sol", ""} {
		if got, ok := DepthRoute(model, []byte(`{"reasoning_effort":"high"}`)); ok {
			t.Errorf("%q has no depth row and was routed to %q", model, got)
		}
	}

	// A row naming a SKU discovery does not serve, a free SKU, or one behind a grant.
	for _, sku := range []string{"enso-gone", "enso-free", "enso-preview"} {
		globalModelConfig.mu.Lock()
		globalModelConfig.router.Depth["enso-auto"]["high"] = sku
		globalModelConfig.mu.Unlock()
		if got, ok := DepthRoute("enso-auto", []byte(`{"reasoning_effort":"high"}`)); ok {
			t.Errorf("a row naming %q routed to %q", sku, got)
		}
	}

	// A requested id the family prices is already on the paid ladder.
	ensoFam.byID["enso-auto"] = ensoFam.byID["enso-flash"]
	if got, ok := DepthRoute("enso-auto", []byte(`{}`)); ok {
		t.Errorf("a priced requested id was re-routed to %q", got)
	}

	globalModelConfig = nil
	if got, ok := DepthRoute("enso-auto", []byte(`{}`)); ok {
		t.Errorf("no model table routed to %q", got)
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

	r := DepthRouting("enso", high, true)
	if r == nil {
		t.Fatal("enso is depth-routed: want a routing")
	}
	if r.Router != "enso" || r.Chosen != "enso-pro" {
		t.Fatalf("router %q chose %q, want enso chose enso-pro", r.Router, r.Chosen)
	}
	if r.Inputs != (RoutingInputs{Depth: "high", Key: "high", Policy: "funded"}) {
		t.Fatalf("inputs = %+v", r.Inputs)
	}
	want := []Candidate{
		{Model: "enso-flash", Depths: []string{"off", "none", "minimal", "low", "medium", "default"}},
		{Model: "enso-pro", Depths: []string{"high"}},
		{Model: "enso-ultra", Depths: []string{"xhigh", "max"}},
	}
	got, _ := json.Marshal(r.Candidates)
	exp, _ := json.Marshal(want)
	if string(got) != string(exp) {
		t.Fatalf("candidates = %s, want %s", got, exp)
	}
	if r.Reason != "high reasoning names enso-pro, and your plan or credit pays for it." {
		t.Fatalf("reason = %q", r.Reason)
	}

	// The same request from a caller nothing pays for keeps the free id, and says so.
	r = DepthRouting("enso", high, false)
	if r.Chosen != "enso" || r.Inputs.Policy != "unfunded" {
		t.Fatalf("unfunded: chose %q policy %q", r.Chosen, r.Inputs.Policy)
	}
	if r.Reason != "high reasoning names enso-pro, which your plan and credit do not pay for, so enso serves." {
		t.Fatalf("unfunded reason = %q", r.Reason)
	}

	// A request stating no depth is read at the default row.
	r = DepthRouting("enso", []byte(`{"model":"enso"}`), true)
	if r.Inputs.Depth != "default" || r.Inputs.Key != "default" || r.Chosen != "enso-flash" {
		t.Fatalf("default: %+v chose %q", r.Inputs, r.Chosen)
	}

	// An unknown depth is read at the default row, and the reason says where it was read.
	r = DepthRouting("enso", []byte(`{"model":"enso","reasoning_effort":"deep"}`), true)
	if r.Inputs.Key != "default" || r.Reason != "deep reasoning (read at default) names enso-flash, and your plan or credit pays for it." {
		t.Fatalf("unknown depth: %+v %q", r.Inputs, r.Reason)
	}

	// A model with no row is not routed by Enso.
	for _, model := range []string{"enso-pro", "zen5"} {
		if DepthRouting(model, high, true) != nil {
			t.Fatalf("%s has no depth row: want no routing", model)
		}
	}
}

func TestDepthRoutingNeverLiftsAPricedId(t *testing.T) {
	depthConfig(t)
	seedEnso(t)
	priced := ensoFam.byID["enso-auto"]
	priced.Base = zenTier{MaxCtx: 1_000_000, In: ensoFam.byID["enso-pro"].Base.In, Out: ensoFam.byID["enso-pro"].Base.Out}
	priced.Tiers = []zenTier{priced.Base}
	ensoFam.byID["enso-auto"] = priced
	r := DepthRouting("enso-auto", []byte(`{"reasoning_effort":"max"}`), true)
	if r.Chosen != "enso-auto" || r.Inputs.Policy != "n/a" {
		t.Fatalf("priced id: chose %q policy %q", r.Chosen, r.Inputs.Policy)
	}
	if r.Reason != "max reasoning names enso-ultra, but enso-auto is not a free id the table lifts, so enso-auto serves as asked." {
		t.Fatalf("reason = %q", r.Reason)
	}
}
