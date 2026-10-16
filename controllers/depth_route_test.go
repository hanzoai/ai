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
	"strings"
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

// seedEnso stands in for enso discovery: the free SKU, three priced tiers, and one
// priced SKU held behind a grant. The router ids are no SKUs and are not listed.
func seedEnso(t *testing.T) {
	t.Helper()
	price := func(in, out string, ctx int) zenTier {
		return zenTier{MaxCtx: ctx, In: decimal.MustParse(in), Out: decimal.MustParse(out)}
	}
	free := zenTier{MaxCtx: 256_000}
	saved := ensoFam.byID
	ensoFam.byID = map[string]zenModel{
		"enso-free":    {ID: "enso-free", Base: free, Tiers: []zenTier{free}},
		"enso-flash":   {ID: "enso-flash", Base: price("0.33", "1.65", 900_000), Tiers: []zenTier{price("0.33", "1.65", 900_000)}, Funding: "prepaid"},
		"enso-pro":     {ID: "enso-pro", Base: price("4.40", "22.00", 272_000), Tiers: []zenTier{price("4.40", "22.00", 272_000)}, Funding: "prepaid"},
		"enso-ultra":   {ID: "enso-ultra", Base: price("5.50", "27.50", 1_000_000), Tiers: []zenTier{price("5.50", "27.50", 1_000_000)}, Funding: "prepaid"},
		"enso-preview": {ID: "enso-preview", Base: price("1", "1", 1_000_000), Tiers: []zenTier{price("1", "1", 1_000_000)}, Access: "waitlist"},
	}
	t.Cleanup(func() { ensoFam.byID = saved })
}

// The depth a request states picks the tier, in every dialect a caller sends it in,
// and a request that states none takes the default. Whatever the depth, the free SKU
// is the family's.
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
		tier, free, ok := DepthRoute("enso-auto", []byte(tc.body))
		if !ok || tier != tc.want || free != "enso-free" {
			t.Errorf("%s → (%q, %q, %v), want %q and enso-free", tc.body, tier, free, ok, tc.want)
		}
	}
	if tier, free, ok := DepthRoute("Enso", []byte(`{"reasoning_effort":"high"}`)); !ok || tier != "enso-pro" || free != "enso-free" {
		t.Errorf("the old name routes like enso-auto, got (%q, %q, %v)", tier, free, ok)
	}
}

// Only a router id is routed, and only onto a tier the family serves at a price
// without a grant: a row naming anything else keeps the router on its free SKU, since
// the id itself is never served.
func TestDepthRouteRoutesOnlyARouterIdOntoAPaidTier(t *testing.T) {
	depthConfig(t)
	seedEnso(t)
	for _, model := range []string{"enso-free", "enso-flash", "enso-ultra", "zen-free", "gpt-6-sol", ""} {
		if tier, free, ok := DepthRoute(model, []byte(`{"reasoning_effort":"high"}`)); ok {
			t.Errorf("%q is no router id and was routed to (%q, %q)", model, tier, free)
		}
		if got := unpaid(model); got != model {
			t.Errorf("unpaid(%q) = %q, want it unchanged", model, got)
		}
	}
	for _, model := range []string{"enso-auto", "ENSO"} {
		if got := unpaid(model); got != "enso-free" {
			t.Errorf("unpaid(%q) = %q, want enso-free", model, got)
		}
	}

	// A row naming a SKU discovery does not serve, a free SKU, or one behind a grant.
	for _, sku := range []string{"enso-gone", "enso-free", "enso-preview"} {
		globalModelConfig.mu.Lock()
		globalModelConfig.router.Depth["enso-auto"]["high"] = sku
		globalModelConfig.mu.Unlock()
		if tier, free, ok := DepthRoute("enso-auto", []byte(`{"reasoning_effort":"high"}`)); !ok || tier != "" || free != "enso-free" {
			t.Errorf("a row naming %q: (%q, %q, %v), want no tier and enso-free", sku, tier, free, ok)
		}
	}

	globalModelConfig = nil
	if tier, free, ok := DepthRoute("enso-auto", []byte(`{}`)); ok {
		t.Errorf("no model table routed to (%q, %q)", tier, free)
	}
	if got := unpaid("enso-auto"); got != "enso-auto" {
		t.Errorf("no model table: unpaid = %q", got)
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
	if r == nil {
		t.Fatal("enso is a router id: want a routing")
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
	if r.Reason != "high reasoning names enso-pro, and your plan or balance pays for it." {
		t.Fatalf("reason = %q", r.Reason)
	}

	// The same request nothing pays the tier for is served on the free SKU, and says why.
	r = DepthRouting("enso", high, "nothing pays for it (insufficient_balance)")
	if r.Chosen != "enso-free" || r.Inputs.Policy != "unfunded" {
		t.Fatalf("unfunded: chose %q policy %q", r.Chosen, r.Inputs.Policy)
	}
	if r.Reason != "high reasoning names enso-pro, but nothing pays for it (insufficient_balance), so enso-free serves at no charge." {
		t.Fatalf("unfunded reason = %q", r.Reason)
	}

	// A request stating no depth is read at the default row.
	r = DepthRouting("enso", []byte(`{"model":"enso"}`), "")
	if r.Inputs.Depth != "default" || r.Inputs.Key != "default" || r.Chosen != "enso-flash" {
		t.Fatalf("default: %+v chose %q", r.Inputs, r.Chosen)
	}

	// An unknown depth is read at the default row, and the reason says where it was read.
	r = DepthRouting("enso", []byte(`{"model":"enso","reasoning_effort":"deep"}`), "")
	if r.Inputs.Key != "default" || r.Reason != "deep reasoning (read at default) names enso-flash, and your plan or balance pays for it." {
		t.Fatalf("unknown depth: %+v %q", r.Inputs, r.Reason)
	}

	// A tier the family does not serve at a price leaves the free SKU, whoever pays.
	globalModelConfig.mu.Lock()
	globalModelConfig.router.Depth["enso"]["max"] = "enso-preview"
	globalModelConfig.mu.Unlock()
	r = DepthRouting("enso", []byte(`{"reasoning_effort":"max"}`), "")
	if r.Chosen != "enso-free" || r.Inputs.Policy != "n/a" ||
		r.Reason != "max reasoning names enso-preview, but enso-preview is not a paid model open to every caller, so enso-free serves." {
		t.Fatalf("unserved tier: chose %q policy %q reason %q", r.Chosen, r.Inputs.Policy, r.Reason)
	}

	// A model with no row is not routed by Enso.
	for _, model := range []string{"enso-pro", "enso-free", "zen5"} {
		if DepthRouting(model, high, "") != nil {
			t.Fatalf("%s has no depth row: want no routing", model)
		}
	}
}

// /v1/models lists each router id: its default tier's row and price under its own id,
// the narrowest window of its tiers, and a description of where it routes. A router
// whose default tier is not listed is not listed either.
func TestTheRouterIdsAreListed(t *testing.T) {
	depthConfig(t)
	seedEnso(t)
	models := []modelInfo{
		{ID: "enso-flash", Object: "model", OwnedBy: "hanzo", Premium: true, ContextWindow: 900_000, SupportsVision: true},
		{ID: "enso-free", Object: "model", OwnedBy: "hanzo", ContextWindow: 256_000},
		{ID: "enso-pro", Object: "model", OwnedBy: "hanzo", Premium: true, ContextWindow: 272_000},
		{ID: "enso-ultra", Object: "model", OwnedBy: "hanzo", Premium: true, ContextWindow: 1_000_000},
	}
	out := depthListings(globalModelConfig, models)
	var ids []string
	byID := map[string]modelInfo{}
	for _, m := range out {
		ids = append(ids, m.ID)
		byID[m.ID] = m
	}
	if got := strings.Join(ids, ","); got != "enso,enso-auto,enso-flash,enso-free,enso-pro,enso-ultra" {
		t.Fatalf("listed %s", got)
	}
	for _, id := range []string{"enso", "enso-auto"} {
		m := byID[id]
		if m.Name != "Enso" || !m.Premium || !m.SupportsVision || m.ContextWindow != 272_000 {
			t.Errorf("%s = %+v, want enso-flash's row at the narrowest tier window", id, m)
		}
		for _, says := range []string{"enso-flash at off, none, minimal, low, medium, default", "enso-pro at high", "enso-ultra at xhigh, max", "enso-free, at no charge"} {
			if !strings.Contains(m.Description, says) {
				t.Errorf("%s description %q does not say %q", id, m.Description, says)
			}
		}
		if got := listedAs(id); got != "enso-flash" {
			t.Errorf("%s is priced as %q, want enso-flash", id, got)
		}
	}
	if got := listedAs("enso-pro"); got != "enso-pro" {
		t.Errorf("a tier is priced as %q", got)
	}

	// No default tier listed: no router row.
	if out := depthListings(globalModelConfig, models[1:]); len(out) != 3 {
		t.Errorf("without enso-flash listed: %d rows", len(out))
	}
}
