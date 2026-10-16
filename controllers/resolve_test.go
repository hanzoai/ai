// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/decimal"
	"gopkg.in/yaml.v3"
)

// A model its maker sells is reached on the maker's key first and through
// OpenRouter after; a maker with no key listing it leaves OpenRouter the first path a
// key serves.
func TestAModelIsReachedOnADirectKeyBeforeOpenRouter(t *testing.T) {
	fam := resale(t, &accounts{})
	fam.byID[claudeSKU] = zenModel{ID: claudeSKU, CostIn: decimal.MustParse("1"), CostOut: decimal.MustParse("5"), Base: fam.byID[claudeSKU].Base, Tiers: fam.byID[claudeSKU].Tiers}
	fees(t, map[string]string{"openrouter": "5.5", "hanzo": "5.5"})
	makerAt(t, &maker{}, "a1")

	got := Paths(claudeSKU, "")
	if len(got) != 2 || got[0] != (Path{Provider: "anthropic", Kind: direct, Keyed: true}) || got[1].Provider != "openrouter" || got[1].Kind != reseller {
		t.Fatalf("paths = %+v, want the maker's key, then OpenRouter", got)
	}

	makerAt(t, &maker{}) // no key
	got = Paths(claudeSKU, "")
	if len(got) != 2 || got[0].Keyed || !got[1].Keyed || got[1].Provider != "openrouter" {
		t.Fatalf("paths with no maker key = %+v, want OpenRouter the first keyed", got)
	}
}

// A route row's paths are its providers as a call asks them, then OpenRouter's copy
// of its model; the row lists its direct provider first.
func TestARouteRowIsAskedAsItsPathsSay(t *testing.T) {
	withRoute(t, "probe-direct", "anthropic", "do-ai")
	got := Paths("probe-direct", "")
	if len(got) != 2 || got[0].Provider != "anthropic" || got[0].Kind != direct || got[1].Provider != "do-ai" || got[1].Kind != digitalocean {
		t.Errorf("paths = %+v, want anthropic then do-ai", got)
	}
	cs := candidates("acme", resolveModelRouteForOrg("probe-direct", "acme"), nil, false)
	if len(cs) != len(got) {
		t.Fatalf("a call asks %+v, paths say %+v", cs, got)
	}
	for i := range cs {
		if cs[i].provider != got[i].Provider {
			t.Errorf("a call asks %+v, paths say %+v", cs, got)
		}
	}
	if Paths("no-such-model", "") != nil {
		t.Error("a model nothing routes has a path")
	}
}

// Every route the repository ships asks a direct provider before DigitalOcean: a
// call walks a row as declared, so the order is the row's to state.
func TestEveryShippedRouteAsksADirectKeyBeforeDigitalOcean(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "conf", "models.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var file ModelConfigFile
	if err := yaml.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	order := map[string]int{direct: 0, digitalocean: 1}
	for id, def := range file.Models {
		if def.AliasOf != "" || def.PricingOnly {
			continue
		}
		last := -1
		for _, p := range append([]string{def.Provider}, providersOf(def.Fallbacks)...) {
			k := order[kindOfRow(p)]
			if k < last {
				t.Errorf("%s asks %s after DigitalOcean", id, p)
			}
			last = max(last, k)
		}
	}
}

func providersOf(fbs []FallbackDef) []string {
	var out []string
	for _, f := range fbs {
		out = append(out, f.Provider)
	}
	return out
}

// withRoute loads a model config holding one route row: model id on provider, and
// each of also as a fallback in order.
func withRoute(t *testing.T, id, provider string, also ...string) {
	t.Helper()
	prev := globalModelConfig
	t.Cleanup(func() { globalModelConfig = prev })
	def := ModelDef{Provider: provider, Upstream: id}
	for _, p := range also {
		def.Fallbacks = append(def.Fallbacks, FallbackDef{Provider: p, Upstream: id})
	}
	mc := &ModelConfig{routes: map[string]modelRoute{}, pricing: map[string]modelPrice{}, stopCh: make(chan struct{})}
	if err := mc.applyConfig(&ModelConfigFile{Models: map[string]ModelDef{id: def}}); err != nil {
		t.Fatal(err)
	}
	globalModelConfig = mc
}
