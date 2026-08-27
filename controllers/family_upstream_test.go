// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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
	"testing"

	"github.com/hanzoai/ai/model"
)

// A family's provider carries a type, and that type is an upstream something can
// open. pipeToFamily relays to the configured URL and never opens it, but a
// family's row is also reachable by name — GetModelProviderByName resolves one
// through familyProviderFns — and a type nothing answers to opens as absent.
//
// Zen, Enso and OpenRouter are the families; all three are served by relay, so
// all three are named.
func TestEveryFamilyTypeIsAnUpstreamThatOpens(t *testing.T) {
	if len(modelFamilies) == 0 {
		t.Fatal("no families — this check is reading nothing")
	}
	for _, fam := range modelFamilies {
		up := model.Upstream(fam.typ)
		made, err := model.Open(model.Spec{Upstream: up, Model: fam.name, URL: "http://127.0.0.1:1", Secret: "k"})
		if err != nil {
			t.Errorf("family %q is typed %q, which answered with an error: %v", fam.name, fam.typ, err)
			continue
		}
		if made == nil {
			t.Errorf("family %q is typed %q, which no upstream answers to", fam.name, fam.typ)
		}
	}
}
