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

package routers

import (
	"reflect"
	"strings"
	"testing"
)

// A resource that does not say what it holds publishes an untyped bag.
//
// This is the property the whole file exists for: `data` used to be one sentence
// for every operation, so every generated client handed the caller an `any`. A
// new row that forgets `shape` would silently rejoin that world, and the document
// would still look full.
func TestEveryResourceSaysWhatItHolds(t *testing.T) {
	for _, r := range resources {
		if r.shape == nil {
			t.Errorf("%s/%s has no shape: its operations can only publish the bare envelope", r.ns, r.path)
		}
	}
}

// The collection answers a list of the resource, the member answers one.
//
// Asserted through Document rather than by calling the builders, because what
// matters is what a CLIENT reads — a shape threaded correctly into items() and
// then dropped on the way out would pass a narrower test.
func TestCollectionListsWhatTheMemberReturns(t *testing.T) {
	doc := Document()
	paths, _ := doc["paths"].(map[string]any)

	for _, r := range resources {
		if r.shape == nil || r.noList {
			continue
		}
		name := component(reflect.TypeOf(r.shape))
		want := "#/components/schemas/" + name

		data := dataSchema(t, paths, openAPIPath(r.collection()), "get")
		if data == nil {
			t.Errorf("GET %s does not narrow data", r.collection())
			continue
		}
		if data["type"] != "array" {
			t.Errorf("GET %s answers a %v, not a list", r.collection(), data["type"])
			continue
		}
		items, _ := data["items"].(map[string]any)
		if items["$ref"] != want {
			t.Errorf("GET %s lists %v, want %s", r.collection(), items["$ref"], want)
		}

		if !r.noRead {
			one := dataSchema(t, paths, openAPIPath(r.member()), "get")
			if one == nil || one["$ref"] != want {
				t.Errorf("GET %s answers %v, want %s", r.member(), one, want)
			}
		}
	}
}

// Every $ref the document writes must resolve to a schema it carries.
//
// A dangling $ref is the exact failure the deleted hand-authored spec left
// behind: operations pointing at component definitions that no longer existed,
// which every generator resolves to "unknown" without complaining.
func TestEveryReferenceResolves(t *testing.T) {
	doc := Document()
	comps, _ := doc["components"].(map[string]any)
	schemas, _ := comps["schemas"].(map[string]any)

	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if r, ok := x["$ref"].(string); ok {
				const p = "#/components/schemas/"
				name, found := strings.CutPrefix(r, p)
				if !found {
					t.Errorf("$ref %q does not point into this document", r)
				} else if _, ok := schemas[name]; !ok {
					t.Errorf("$ref %q resolves to nothing", r)
				}
			}
			for _, e := range x {
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(doc)
}

// dataSchema is the `data` an operation narrows the envelope to, or nil when it
// has not narrowed it.
func dataSchema(t *testing.T, paths map[string]any, path, verb string) map[string]any {
	t.Helper()
	item, _ := paths[path].(map[string]any)
	op, _ := item[verb].(map[string]any)
	resp, _ := op["responses"].(map[string]any)
	ok200, _ := resp["200"].(map[string]any)
	content, _ := ok200["content"].(map[string]any)
	js, _ := content["application/json"].(map[string]any)
	schema, _ := js["schema"].(map[string]any)
	all, _ := schema["allOf"].([]any)
	if len(all) != 2 {
		return nil
	}
	narrowed, _ := all[1].(map[string]any)
	props, _ := narrowed["properties"].(map[string]any)
	data, _ := props["data"].(map[string]any)
	return data
}
