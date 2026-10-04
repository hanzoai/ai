// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/hanzoai/ai/object"
)

// fieldsOR is an OpenRouter listing in its own dialect: a display name led by the
// vendor, a description, the input modalities, and the request parameters each SKU
// honours. jev-router is TypeSafe's; small:free is a free text route, which is what
// puts the platform's free id in the listing.
const fieldsOR = `{"data":[
 {"id":"anthropic/claude-sonnet-4","name":"Anthropic: Claude Sonnet 4",
  "description":"Claude Sonnet 4 balances coding and reasoning.","context_length":200000,
  "architecture":{"input_modalities":["image","text","file"],"output_modalities":["text"]},
  "supported_parameters":["include_reasoning","max_tokens","reasoning","tools","tool_choice"],
  "pricing":{"prompt":"0.000003","completion":"0.000015"}},
 {"id":"typesafe/jev-router","name":"TypeSafe: Jev Router","description":"Jev Router picks a model per request.",
  "context_length":1000000,
  "architecture":{"input_modalities":["audio","file","image","text","video"],"output_modalities":["text"]},
  "supported_parameters":["max_tokens","temperature"],
  "pricing":{"prompt":"0.000001","completion":"0.000002"}},
 {"id":"vendor/small:free","name":"Small (free)","context_length":65536,
  "architecture":{"input_modalities":["text"],"output_modalities":["text"]},
  "pricing":{"prompt":"0","completion":"0"}}
]}`

// fieldsZen and fieldsEnso are the Hanzo family wire: a mode names each SKU's kind,
// capabilities.vision its image input, and a description is decoded where the family
// sends one.
const fieldsZen = `{"data":[
 {"id":"zen5","owned_by":"zenlm","mode":"chat","context_window":262144,
  "description":"Zen 5, Hanzo's flagship.","pricing":{"input":"1","output":"4"}},
 {"id":"zen-vl","owned_by":"zenlm","mode":"chat","context_window":131072,
  "capabilities":{"vision":true},"pricing":{"input":"1","output":"4"}},
 {"id":"zen-embedding","owned_by":"zenlm","mode":"embedding","context_window":32768,
  "pricing":{"input":"0.01","output":"0.01"}},
 {"id":"zen-rerank","owned_by":"zenlm","mode":"rerank","context_window":8192,
  "pricing":{"input":"0.02","output":"0"}}
]}`

const fieldsEnso = `{"data":[
 {"id":"enso","owned_by":"hanzo","mode":"chat","context_window":1000000,"pricing":{"input":"4","output":"20"}}
]}`

// fieldsYAML is a config naming kai on the decision service, owned by Hanzo.
const fieldsYAML = `version: 1
models:
  kai:
    provider: kai
    upstream: kai
    owned_by: hanzo
    outputs: [decision]
    pricing: {input: 0.021, output: 0}
    description: Hanzo's decision model, which answers typed questions at /v1/decisions rather than a chat turn.
  bge-m3:
    provider: do-ai
    upstream: bge-m3
    pricing: {input: 0.02, output: 0}
`

// stubFields loads fieldsYAML and points every family at its stub catalog.
func stubFields(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(path, []byte(fieldsYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	useCatalog(t, path)
	withOpenRouter(t, fieldsOR)
	withFamily(t, zenFam, fieldsZen)
	withFamily(t, ensoFam, fieldsEnso)
}

// withFields lists fieldsYAML beside every family's stub catalog.
func withFields(t *testing.T) map[string]modelInfo {
	t.Helper()
	stubFields(t)
	return indexModels(listAvailableModels())
}

// An OpenRouter row lists what OpenRouter says of it: the display name without the
// vendor lead owned_by already carries, the description, the input modalities verbatim,
// and tools and reasoning read from the parameters it honours.
func TestOpenRouterRowCarriesItsCatalogFields(t *testing.T) {
	byID := withFields(t)

	c, ok := byID["anthropic/claude-sonnet-4"]
	if !ok {
		t.Fatal("anthropic/claude-sonnet-4 is not listed")
	}
	if c.Name != "Claude Sonnet 4" || c.Description != "Claude Sonnet 4 balances coding and reasoning." {
		t.Errorf("name %q description %q", c.Name, c.Description)
	}
	if !slices.Equal(c.Inputs, []string{"image", "text", "file"}) || !slices.Equal(c.Outputs, []string{"text"}) {
		t.Errorf("inputs %v outputs %v", c.Inputs, c.Outputs)
	}
	if !c.SupportsVision || !c.SupportsTools || !c.SupportsReasoning {
		t.Errorf("vision %v tools %v reasoning %v, want all three", c.SupportsVision, c.SupportsTools, c.SupportsReasoning)
	}

	// A SKU that honours neither parameter states neither: false is absence, never a no.
	j := byID["typesafe/jev-router"]
	if j.Name != "Jev Router" || j.SupportsTools || j.SupportsReasoning {
		t.Errorf("jev-router name %q tools %v reasoning %v", j.Name, j.SupportsTools, j.SupportsReasoning)
	}
	keys := jsonKeys(t, j)
	for _, k := range []string{"supports_tools", "supports_reasoning"} {
		if keys[k] {
			t.Errorf("jev-router publishes %q though its catalog states no such parameter", k)
		}
	}

	// A name with no vendor lead is kept whole.
	if got := byID["vendor/small:free"].Name; got != "Small (free)" {
		t.Errorf("a name with no lead lists as %q", got)
	}
}

// Every listed row carries its class, which is ClassOf's answer; Hanzo's families are
// named and a third-party model names none.
func TestListedRowsNameClassAndFamily(t *testing.T) {
	byID := withFields(t)

	for id, want := range map[string]struct{ class, family string }{
		"enso":                      {object.ClassOurs, "enso"},
		"zen5":                      {object.ClassOurs, "zen"},
		"kai":                       {object.ClassOurs, "kai"},
		"typesafe/jev-router":       {object.ClassPremium, ""},
		"anthropic/claude-sonnet-4": {object.ClassPremium, ""},
		"free":                      {object.ClassFree, "enso"},
		"bge-m3":                    {object.ClassPremium, ""},
	} {
		m, ok := byID[id]
		if !ok {
			t.Errorf("%s is not listed", id)
			continue
		}
		if m.Class != want.class || m.Family != want.family {
			t.Errorf("%s lists class %q family %q, want %q %q", id, m.Class, m.Family, want.class, want.family)
		}
	}
	for id, m := range byID {
		if m.Class == "" || m.Class != ClassOf(id) {
			t.Errorf("%s lists class %q, ClassOf says %q", id, m.Class, ClassOf(id))
		}
	}
	if jsonKeys(t, byID["anthropic/claude-sonnet-4"])["family"] {
		t.Error("a third-party model publishes a family")
	}
}

// lineage is one function over the id and its owner: kai is the decision service's
// route Hanzo owns, zoo whatever Zoo owns, and TypeSafe's Jev ids are no Hanzo family.
func TestLineageNamesHanzoFamilies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(path, []byte(fieldsYAML+`  typesafe/jev-1.13:
    provider: kai
    upstream: typesafe/jev-1.13
    owned_by: typesafe
`), 0o600); err != nil {
		t.Fatal(err)
	}
	useCatalog(t, path)
	for _, tc := range []struct{ id, owner, want string }{
		{"kai", "hanzo", "kai"},
		{"typesafe/jev-1.13", "typesafe", ""}, // on the decision service, owned by TypeSafe: not Kai, not ours
		{"~typesafe/jev-latest", "typesafe", ""},
		{"typesafe/jev-router", "typesafe", ""},
		{"zoo/eco-1", "zoo", "zoo"},
		{"eco-2", "ZooAI", "zoo"},
		{"bge-m3", "hanzo", ""}, // owned by Hanzo but not on the decision service
		{"anthropic/claude-sonnet-4", "anthropic", ""},
		{"free", "hanzo", "enso"},
	} {
		if got := lineage(tc.id, tc.owner); got != tc.want {
			t.Errorf("lineage(%q, %q) = %q, want %q", tc.id, tc.owner, got, tc.want)
		}
	}
}

// Jev is TypeSafe's model, so it is premium and in no Hanzo family, whether the decision
// service forwards it (typesafe/jev-1.13, ~typesafe/jev-latest) or OpenRouter lists it
// (typesafe/jev-router). Kai, which the same service serves and Hanzo owns, stays ours
// and in the kai family. Both Jev decision ids are listed: they are callable models.
func TestJevIsPremiumAndNoHanzoFamily(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(path, []byte(fieldsYAML+`  typesafe/jev-1.13:
    provider: kai
    upstream: typesafe/jev-1.13
    owned_by: typesafe
    outputs: [decision]
    pricing: {input: 0.042, output: 0}
  "~typesafe/jev-latest":
    provider: kai
    upstream: "~typesafe/jev-latest"
    owned_by: typesafe
    outputs: [decision]
    pricing: {input: 0.042, output: 0}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	useCatalog(t, path)
	withOpenRouter(t, fieldsOR)
	byID := indexModels(listAvailableModels())

	for id, want := range map[string]struct{ class, family string }{
		"typesafe/jev-1.13":    {object.ClassPremium, ""},
		"~typesafe/jev-latest": {object.ClassPremium, ""},
		"typesafe/jev-router":  {object.ClassPremium, ""},
		"kai":                  {object.ClassOurs, "kai"},
	} {
		m, ok := byID[id]
		if !ok {
			t.Errorf("%s is not listed", id)
			continue
		}
		if m.Class != want.class || m.Family != want.family {
			t.Errorf("%s lists class %q family %q, want %q %q", id, m.Class, m.Family, want.class, want.family)
		}
		if got := ClassOf(id); got != want.class {
			t.Errorf("ClassOf(%q) = %q, want %q", id, got, want.class)
		}
		if want.family == "" && jsonKeys(t, m)["family"] {
			t.Errorf("%s publishes a family", id)
		}
	}
	if p := byID["typesafe/jev-1.13"].Pricing; p == nil || p.InputPerMillion != 0.042 {
		t.Errorf("typesafe/jev-1.13 lists pricing %+v, want 0.042 per 1M input", p)
	}
	if p := byID["kai"].Pricing; p == nil || p.InputPerMillion != 0.021 {
		t.Errorf("kai lists pricing %+v, want 0.021 per 1M input", p)
	}
}

// A config row's `name` is its display name on the listing, the way its description
// is; a row that states none publishes none, and a reader shows the id.
func TestConfigRowCarriesItsName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(path, []byte(`version: 1
models:
  kai:
    provider: kai
    upstream: kai
    owned_by: hanzo
    name: Kai
    pricing: {input: 0.021, output: 0}
  typesafe/jev-1.13:
    provider: kai
    upstream: typesafe/jev-1.13
    owned_by: typesafe
    name: Jev 1.13
    pricing: {input: 0.042, output: 0}
  bge-m3:
    provider: do-ai
    upstream: bge-m3
    pricing: {input: 0.02, output: 0}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	useCatalog(t, path)
	byID := indexModels(listAvailableModels())
	for id, want := range map[string]string{"kai": "Kai", "typesafe/jev-1.13": "Jev 1.13", "bge-m3": ""} {
		if got := byID[id].Name; got != want {
			t.Errorf("%s lists name %q, want %q", id, got, want)
		}
	}
	if jsonKeys(t, byID["bge-m3"])["name"] {
		t.Error("bge-m3 publishes a name its row never stated")
	}
}

// A Hanzo family SKU lists its image input as the family states it, the kind of answer
// its mode names, and the description the family sends, absent where it sends none.
func TestFamilyRowCarriesVisionModeAndDescription(t *testing.T) {
	byID := withFields(t)

	if !byID["zen-vl"].SupportsVision {
		t.Error("zen-vl's capabilities.vision did not survive the merge")
	}
	if byID["zen5"].SupportsVision {
		t.Error("zen5 states no vision and lists one")
	}
	for id, want := range map[string][]string{
		"zen5":          {"text"},
		"zen-embedding": {"embeddings"},
		"zen-rerank":    {"rerank"},
		"enso":          {"text"},
	} {
		if got := byID[id].Outputs; !slices.Equal(got, want) {
			t.Errorf("%s outputs %v, want %v", id, got, want)
		}
	}
	if got := byID["zen5"].Description; got != "Zen 5, Hanzo's flagship." {
		t.Errorf("zen5 description %q", got)
	}
	if jsonKeys(t, byID["zen-vl"])["description"] {
		t.Error("zen-vl publishes a description its family never sent")
	}
	if got := byID["kai"].Description; got == "" {
		t.Error("kai's config row states a description the listing dropped")
	}
	// A family that states no mode states no outputs.
	if got := (zenWireModel{ID: "x"}).model().Outputs; got != nil {
		t.Errorf("a SKU with no mode lists outputs %v", got)
	}
}

// The static table, consulted when no config is loaded, carries class, family and the
// sentence each route of our own states.
func TestStaticRowsCarryClassFamilyAndDescription(t *testing.T) {
	prev := globalModelConfig
	t.Cleanup(func() { globalModelConfig = prev })
	globalModelConfig = nil

	byID := indexModels(listAvailableModels())
	for _, id := range []string{"kai", "zen-scribe", "zen-voice-mini"} {
		if byID[id].Description == "" {
			t.Errorf("static %s lists no description", id)
		}
	}
	if k := byID["kai"]; k.Class != object.ClassOurs || k.Family != "kai" {
		t.Errorf("static kai lists class %q family %q", k.Class, k.Family)
	}
	for id, m := range byID {
		if m.Class == "" {
			t.Errorf("static %s lists no class", id)
		}
	}
}

// The first build on a replica stores its slugs before it resolves a route. Class and
// family resolve one per row, and resolving an id no config names asks listedSlug,
// which builds the catalogue when no slugs are stored: in the other order the first
// build waits on its own lock for ever.
func TestFirstBuildDoesNotWaitOnItself(t *testing.T) {
	stubFields(t)
	slugs.Store(nil)
	listing.mu.Lock()
	listing.models = nil
	listing.mu.Unlock()

	done := make(chan []modelInfo, 1)
	go func() { done <- listAvailableModels() }()
	select {
	case models := <-done:
		if len(models) == 0 {
			t.Fatal("the first build listed nothing")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the first build is waiting on its own lock")
	}
}
