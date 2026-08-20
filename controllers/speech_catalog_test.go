package controllers

import "testing"

// speechSKUs is the speech lineup as the catalog sells it: the Zen name callers see,
// and the id the in-cluster speech service answers to.
var speechSKUs = map[string]string{
	"zen-scribe":      "whisper",
	"zen-scribe-mini": "whisper-small",
	"zen-voice-mini":  "kokoro",
}

// TestSpeechCatalog asserts the shipped conf/models.yaml — the catalog /v1/models
// actually renders — sells the speech models under their Zen names and keeps the
// upstream ids callable but unlisted.
//
// The static map in model_routes.go is only the fallback: listAvailableModels
// prefers the YAML whenever one is loaded, which is always in production. So the
// two have to agree, and this is what checks that they do.
func TestSpeechCatalog(t *testing.T) {
	useCatalog(t, "../conf/models.yaml")
	cfg := GetModelConfig()
	if cfg == nil {
		t.Fatal("model config nil after init")
	}

	listed := map[string]bool{}
	for _, m := range cfg.ListModels() {
		listed[m.ID] = true
	}

	for name, upstream := range speechSKUs {
		r := cfg.ResolveRoute(name)
		if r == nil {
			t.Errorf("%q did not resolve", name)
			continue
		}
		if r.providerName != "speech" {
			t.Errorf("%q provider = %q, want speech", name, r.providerName)
		}
		if r.upstreamModel != upstream {
			t.Errorf("%q upstream = %q, want %q", name, r.upstreamModel, upstream)
		}
		if !listed[name] {
			t.Errorf("%q is not in the /v1/models listing", name)
		}
		// The upstream id keeps working for callers already sending it, and stays
		// out of the catalog so we advertise one name per model.
		if cfg.ResolveRoute(upstream) == nil {
			t.Errorf("upstream id %q stopped resolving", upstream)
		}
		if listed[upstream] {
			t.Errorf("upstream id %q is advertised; only %q should be", upstream, name)
		}
	}

	// The static fallback has to sell the same names, or the catalog changes shape
	// the moment the YAML is missing.
	for name, upstream := range speechSKUs {
		route, ok := modelRoutes[name]
		if !ok {
			t.Errorf("static map is missing %q", name)
			continue
		}
		if route.upstreamModel != upstream {
			t.Errorf("static %q upstream = %q, want %q", name, route.upstreamModel, upstream)
		}
		if route.hidden {
			t.Errorf("static %q is hidden; it is the name we sell", name)
		}
		if r, ok := modelRoutes[upstream]; ok && !r.hidden {
			t.Errorf("static upstream id %q is advertised; it should be hidden", upstream)
		}
	}
}
