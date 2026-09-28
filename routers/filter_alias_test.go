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

package routers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// A request naming an alias reaches everything after the filter naming the id the
// alias stands for, every other field as sent, on every path that names a model.
// Any other id passes byte for byte.
func TestAnAliasIsNamedAsTheIDItStandsFor(t *testing.T) {
	prev := canonical
	canonical = func(model string) (string, bool) {
		if strings.EqualFold(model, "hanzoai/enso") {
			return "hanzo/enso", true
		}
		return "", false
	}
	t.Cleanup(func() { canonical = prev })

	for _, path := range []string{"/v1/chat/completions", "/v1/messages", "/v1/responses", "/v1/decisions"} {
		p := ask(http.MethodPost, path).
			body([]byte(`{"model":"HanzoAI/Enso","stream":true,"messages":[{"role":"user","content":"hi"}]}`)).
			through(AliasFilter)
		var got struct {
			Model    string          `json:"model"`
			Stream   bool            `json:"stream"`
			Messages json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal([]byte(p.handed()), &got); err != nil || p.status() != http.StatusOK {
			t.Fatalf("%s: status %d, handed %q", path, p.status(), p.handed())
		}
		if got.Model != "hanzo/enso" || !got.Stream || string(got.Messages) != `[{"role":"user","content":"hi"}]` {
			t.Fatalf("%s: handed %s, want hanzo/enso and every other field as sent", path, p.handed())
		}
	}

	for _, sent := range []string{
		`{"model":"hanzo/enso","messages":[]}`,
		`{"model":"enso-auto","messages":[]}`,
		`{"messages":[]}`,
		`not json`,
	} {
		if p := ask(http.MethodPost, "/v1/chat/completions").body([]byte(sent)).through(AliasFilter); p.handed() != sent {
			t.Fatalf("%s was rewritten to %s", sent, p.handed())
		}
	}
}
