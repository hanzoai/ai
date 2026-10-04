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
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/hanzoai/ai/controllers"
)

// The first call a customer makes through a generated SDK is a completion, and
// the client can only send one if the document says what the call takes. It
// said nothing, so every generated postChatCompletions took no body at all.
func TestTheModelCallsSayWhatTheyTake(t *testing.T) {
	doc := Document(built())
	paths, _ := doc["paths"].(map[string]any)
	comps, _ := doc["components"].(map[string]any)
	schemas, _ := comps["schemas"].(map[string]any)

	for _, c := range []struct {
		path   string
		fields []string
	}{
		{"/v1/chat/completions", []string{"model", "messages"}},
		{"/v1/messages", []string{"model", "messages", "max_tokens"}},
		{"/v1/embeddings", []string{"model", "input"}},
		{"/v1/decisions", []string{"model", "state", "questions"}},
		{"/v1/ai/feedback", []string{"request_id", "signal", "rating"}},
	} {
		item, _ := paths[c.path].(map[string]any)
		op, _ := item["post"].(map[string]any)
		body, _ := op["requestBody"].(map[string]any)
		if body["required"] != true {
			t.Errorf("POST %s: requestBody = %v, want a required body", c.path, op["requestBody"])
			continue
		}
		content, _ := body["content"].(map[string]any)
		js, _ := content["application/json"].(map[string]any)
		schema, _ := js["schema"].(map[string]any)
		name, _ := strings.CutPrefix(schema["$ref"].(string), "#/components/schemas/")
		shape, _ := schemas[name].(map[string]any)
		props, _ := shape["properties"].(map[string]any)
		for _, f := range c.fields {
			if _, ok := props[f]; !ok {
				t.Errorf("POST %s: body %s has no %q", c.path, name, f)
			}
		}
	}
}

// Every handler the takes table names must be one this package routes, for the
// reason TestAnswersNameRoutedHandlers gives: a renamed handler leaves its entry
// compiling and matching nothing.
func TestTakesNameRoutedHandlers(t *testing.T) {
	routed := map[string]bool{}
	for _, w := range wired {
		routed[w.Handler] = true
	}
	for name := range controllers.Takes() {
		if !routed[name] {
			t.Errorf("takes names %q, which no route reaches — renamed, or removed", name)
		}
	}
}

// json.RawMessage is JSON kept as written. It was published as a base64 string —
// its kind is []byte — so a message's content read as a shape no caller sends.
func TestARawMessageIsAnyJSON(t *testing.T) {
	if got := shape(reflect.TypeFor[json.RawMessage](), map[string]reflect.Type{}); len(got) != 0 {
		t.Errorf("json.RawMessage publishes %v, want {} (any JSON)", got)
	}
	named := map[string]reflect.Type{}
	msg := shape(reflect.TypeFor[controllers.AnthropicMessage](), named)
	props, _ := msg["properties"].(map[string]any)
	if content, _ := props["content"].(map[string]any); len(content) != 0 {
		t.Errorf("AnthropicMessage.content publishes %v, want {} — it is a string or an array of blocks", content)
	}
}

// A routed call is bounded by two headers and answers the model that served it in
// a third, and a client knows to send or read none of them unless the document
// says so on the operation. So every routed address publishes the bounds as
// header parameters and the served model as a header of its success, under the
// names the handler reads — and an address that does not route publishes neither.
func TestARoutedCallSaysItsBoundsAndTheModelThatServed(t *testing.T) {
	doc := Document(built())
	paths, _ := doc["paths"].(map[string]any)
	op := func(path string) map[string]any {
		item, _ := paths[path].(map[string]any)
		o, _ := item["post"].(map[string]any)
		return o
	}
	headers := func(o map[string]any) map[string]string {
		out := map[string]string{}
		params, _ := o["parameters"].([]any)
		for _, p := range params {
			m, _ := p.(map[string]any)
			if m["in"] != "header" {
				continue
			}
			s, _ := m["schema"].(map[string]any)
			out[m["name"].(string)], _ = s["type"].(string)
		}
		return out
	}
	for _, path := range []string{"/v1/chat/completions", "/v1/completions", "/v1/chat", "/v1/responses"} {
		o := op(path)
		if o == nil {
			t.Errorf("POST %s is not in the document", path)
			continue
		}
		h := headers(o)
		if h[controllers.MaxCostHeader] != "number" || h[controllers.MaxLatencyHeader] != "integer" {
			t.Errorf("POST %s: header parameters = %v, want %s number and %s integer",
				path, h, controllers.MaxCostHeader, controllers.MaxLatencyHeader)
		}
		responses, _ := o["responses"].(map[string]any)
		ok, _ := responses["200"].(map[string]any)
		said, _ := ok["headers"].(map[string]any)
		if _, has := said[controllers.RoutedModelHeader]; !has {
			t.Errorf("POST %s: 200 headers = %v, want %s", path, said, controllers.RoutedModelHeader)
		}
	}
	for _, path := range []string{"/v1/embeddings", "/v1/messages", "/v1/decisions"} {
		if h := headers(op(path)); h[controllers.MaxCostHeader] != "" {
			t.Errorf("POST %s does not route and publishes %s", path, controllers.MaxCostHeader)
		}
	}
}

// Every handler Routed names is one this package routes, for the reason
// TestAnswersNameRoutedHandlers gives.
func TestRoutedNamesRoutedHandlers(t *testing.T) {
	wiredHandlers := map[string]bool{}
	for _, w := range wired {
		wiredHandlers[w.Handler] = true
	}
	for name := range controllers.Routed() {
		if !wiredHandlers[name] {
			t.Errorf("Routed names %q, which no route reaches — renamed, or removed", name)
		}
	}
}
