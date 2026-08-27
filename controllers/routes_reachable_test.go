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
	"sort"
	"testing"

	"github.com/hanzoai/ai/object"
)

// A provider the routes send models to is one a deployment can reach.
//
// A Model provider at State Disabled resolves to nil, so a route naming one
// answers "provider not configured in database" — the model is in the catalog and
// cannot be served. Seventeen routes named fireworks that way and five named
// openai-direct, which is a catalog claiming models for a year of boots and a
// seed quietly declining to serve them.
func TestEveryRoutedProviderIsOneADeploymentCanReach(t *testing.T) {
	seeded := object.SeededModelProviders()

	named := map[string]int{}
	for _, route := range modelRoutes {
		if route.providerName != "" {
			named[route.providerName]++
		}
	}
	if len(named) == 0 {
		t.Fatal("no route names a provider — this check is reading nothing")
	}

	names := make([]string, 0, len(named))
	for n := range named {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		p, ok := seeded[name]
		if !ok {
			// A model FAMILY is addressed by deployment config and is deliberately
			// not seeded; pruneFamilySeeds deletes rows that name one.
			continue
		}
		if p.State != "Active" {
			t.Errorf("%d route(s) name provider %q, which the seed ships %q — those models cannot be served",
				named[name], name, p.State)
		}
	}
}
