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

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/router"
)

func TestEffectiveRouterStrategy_OrgWinsOverGlobal(t *testing.T) {
	orig := orgRouterStrategyLookup
	defer func() { orgRouterStrategyLookup = orig }()
	orgRouterStrategyLookup = func(owner string) string {
		switch owner {
		case "acme":
			return "heuristic"
		case object.GlobalDefaultOwner:
			return "enso"
		}
		return ""
	}
	if got := effectiveRouterStrategy("acme"); got != router.StrategyHeuristic {
		t.Fatalf("org strategy should win: got %q", got)
	}
	if got := effectiveRouterStrategy("other"); got != router.StrategyEnso {
		t.Fatalf("global default should apply when the org is unset: got %q", got)
	}
}

func TestEffectiveRouterStrategy_UnsetEverywhere(t *testing.T) {
	orig := orgRouterStrategyLookup
	defer func() { orgRouterStrategyLookup = orig }()
	orgRouterStrategyLookup = func(string) string { return "" }
	if got := effectiveRouterStrategy("acme"); got != "" {
		t.Fatalf("unset at all levels must resolve to \"\" (the enso-leaning default): got %q", got)
	}
}

func TestEffectiveRouterOverrides_OrgOverridesGlobalPerKey(t *testing.T) {
	orig := orgRouterOverridesLookup
	defer func() { orgRouterOverridesLookup = orig }()
	orgRouterOverridesLookup = func(owner string) map[string]string {
		switch owner {
		case "acme":
			return map[string]string{"code": "acme-coder"}
		case object.GlobalDefaultOwner:
			return map[string]string{"code": "global-coder", "default": "global-default"}
		}
		return nil
	}
	got := effectiveRouterOverrides("acme")
	if got["code"] != "acme-coder" {
		t.Fatalf("org override should win for the code key: %v", got)
	}
	if got["default"] != "global-default" {
		t.Fatalf("a global-only key should carry through: %v", got)
	}
	if effectiveRouterOverrides("none") == nil {
		t.Fatalf("global-only overrides should still resolve for an org with none of its own")
	}
	// Folding must not mutate the cached global map: an org with none still sees
	// the unmodified global value.
	if effectiveRouterOverrides("")["code"] != "global-coder" {
		t.Fatalf("empty org should get the global overrides unmutated")
	}
}
