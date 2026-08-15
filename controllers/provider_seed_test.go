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
	"testing"

	"github.com/hanzoai/ai/object"
)

// A model FAMILY is never a seeded provider row, and this is the one rule that
// keeps it so.
//
// familyProvider reads a row of the family's name as an operator OVERRIDE that
// wins over configuration: it supplies the address AND the on/off. So a seeded
// row does not configure a family, it TAKES it — and both seeds that existed show
// a different way that goes wrong. zen was seeded at a base already carrying /v1
// while the family paths append their own, so every refresh asked
// .../v1/v1/models and no zen SKU was listed or served. openrouter was seeded
// State "Disabled", which familyProvider answers with nil no matter what
// OPENROUTER_URL says, so its whole catalog stayed dark. Sibling enso — same
// serving binary, same family shape, never seeded — never had either problem.
//
// Both failures are invisible at runtime: the family simply serves nothing. So the
// rule is asserted here rather than remembered, and it is the general one, because
// each time it was written as a special case the next family found a new way to
// break.
func TestNoFamilyIsSeeded(t *testing.T) {
	seeded := object.SeededModelProviders()
	for _, name := range object.FamilyProviderNames() {
		if row, ok := seeded[name]; ok {
			t.Errorf("family %q is seeded as a provider row (ProviderUrl %q); "+
				"a family is addressed by configuration, and a row of its name overrides that",
				name, row.ProviderUrl)
		}
	}
}
