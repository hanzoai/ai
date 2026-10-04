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

package controllers

import (
	"testing"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/router"
)

// `auto` routes among Hanzo's own classes by default. A premium model leads the
// preference table for every task, and still `auto` never picks it until the org
// puts it on its enabled-models allowlist; then it may.
func TestAutoPicksAPremiumModelOnlyWhenTheOrgAllowsIt(t *testing.T) {
	prevCfg, prevSink := globalModelConfig, routingEventSink
	t.Cleanup(func() { globalModelConfig, routingEventSink = prevCfg, prevSink })
	routingEventSink = func(object.RoutingEvent) {}

	globalModelConfig = &ModelConfig{
		routes: map[string]modelRoute{
			"anthropic/claude-opus-4.7": {providerName: "do-ai", upstreamModel: "claude-opus-4.7"},
			"zen5":                      {providerName: "do-ai", upstreamModel: "zen5", ownedBy: "hanzo"},
		},
		pricing: map[string]modelPrice{
			"anthropic/claude-opus-4.7": {InputPerMillion: 6, OutputPerMillion: 30},
			"zen5":                      {InputPerMillion: 0.3, OutputPerMillion: 1.2},
		},
		router: RouterConfigDef{
			Enabled: true,
			Prefer: map[string][]string{
				"code":    {"anthropic/claude-opus-4.7", "zen5"},
				"default": {"anthropic/claude-opus-4.7", "zen5"},
			},
		},
	}
	if ClassOf("anthropic/claude-opus-4.7") != object.ClassPremium || ClassOf("zen5") == object.ClassPremium {
		t.Fatalf("fixture classes: opus %q zen5 %q", ClassOf("anthropic/claude-opus-4.7"), ClassOf("zen5"))
	}
	resolve := func(prompt string) string {
		got, _, ok := resolveAutoModel("auto", "acme", "", "", nil, chatReq("auto", prompt), router.Slo{})
		if !ok {
			t.Fatalf("resolveAutoModel(%q) not ok", prompt)
		}
		return got
	}
	prompts := []string{"please refactor this function", "hi", "tell me about the history of the roman empire in detail"}

	stubEnabledBias(t, nil, nil)
	for _, p := range prompts {
		if got := resolve(p); got != "zen5" {
			t.Errorf("no allowlist, %q: routed to %q, want zen5 (auto never picks premium on its own)", p, got)
		}
	}

	stubEnabledBias(t, map[string]map[string]bool{"acme": {"anthropic/claude-opus-4.7": true, "zen5": true}}, nil)
	if got := resolve(prompts[0]); got != "anthropic/claude-opus-4.7" {
		t.Errorf("premium allowlisted: routed to %q, want the premium model the table prefers", got)
	}
}
