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

// emptyRouterConfig has routing globally off AND no config to route with (no
// prefer table, no endpoint). A per-org "enabled" override must NOT activate
// routing here — there is nothing to route with.
func emptyRouterConfig() *ModelConfig {
	return &ModelConfig{
		routes:  map[string]modelRoute{},
		pricing: map[string]modelPrice{},
		prompts: map[string]string{},
		router:  RouterConfigDef{Enabled: false},
	}
}

// TestAutoRoutingPrecedenceMatrix proves the full precedence table: the global
// router.enabled flag × the org's OrgSettings preference ("", "enabled",
// "disabled") decide whether an `auto` request is routed. orgAutoRoutingLookup is
// stubbed so the matrix is exercised without a live DB.
func TestAutoRoutingPrecedenceMatrix(t *testing.T) {
	prevCfg := globalModelConfig
	prevLookup := orgAutoRoutingLookup
	defer func() {
		globalModelConfig = prevCfg
		orgAutoRoutingLookup = prevLookup
	}()

	const org = "acme"

	cases := []struct {
		name       string
		globalOn   bool
		orgPref    string
		wantRouted bool
	}{
		{"global-on/org-unset → route (global decides)", true, object.AutoRoutingUnset, true},
		{"global-on/org-enabled → route", true, object.AutoRoutingEnabled, true},
		{"global-on/org-disabled → opt out", true, object.AutoRoutingDisabled, false},
		{"global-off/org-unset → no route (global decides)", false, object.AutoRoutingUnset, false},
		{"global-off/org-enabled → opt in (config present)", false, object.AutoRoutingEnabled, true},
		{"global-off/org-disabled → no route", false, object.AutoRoutingDisabled, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			globalModelConfig = routerTestConfig(tc.globalOn)
			orgAutoRoutingLookup = func(owner string) string {
				if owner == org {
					return tc.orgPref
				}
				return object.AutoRoutingUnset
			}

			// "please refactor this function" classifies to code → zen4-coder.
			got, ok := resolveAutoModel("auto", org, "", chatReq("auto", "please refactor this function"), router.Slo{})
			if ok != tc.wantRouted {
				t.Fatalf("resolveAutoModel routed=%v, want %v", ok, tc.wantRouted)
			}
			if tc.wantRouted && got != "zen4-coder" {
				t.Errorf("resolveAutoModel = %q, want zen4-coder", got)
			}
			if !tc.wantRouted && got != "" {
				t.Errorf("resolveAutoModel returned %q with routed=false, want empty", got)
			}
		})
	}
}

// TestAutoRoutingOrgEnabledNeedsConfig proves the guard on the per-org opt-in:
// "enabled" only activates routing when router config is actually present. With
// the global flag off AND an empty router config, "enabled" must not route.
func TestAutoRoutingOrgEnabledNeedsConfig(t *testing.T) {
	prevCfg := globalModelConfig
	prevLookup := orgAutoRoutingLookup
	defer func() {
		globalModelConfig = prevCfg
		orgAutoRoutingLookup = prevLookup
	}()

	globalModelConfig = emptyRouterConfig()
	orgAutoRoutingLookup = func(string) string { return object.AutoRoutingEnabled }

	if _, ok := resolveAutoModel("auto", "acme", "", chatReq("auto", "hi"), router.Slo{}); ok {
		t.Error("resolveAutoModel routed with org-enabled but empty router config, want not-routed")
	}
}

// TestAutoRoutingActiveUnit exercises the decision in isolation across the matrix,
// independent of the classifier, so a routing-logic regression is pinpointed here
// rather than only surfacing through resolveAutoModel.
func TestAutoRoutingActiveUnit(t *testing.T) {
	on := routerTestConfig(true)
	off := routerTestConfig(false)
	empty := emptyRouterConfig()

	cases := []struct {
		cfg  *ModelConfig
		pref string
		want bool
	}{
		{on, object.AutoRoutingUnset, true},
		{on, object.AutoRoutingEnabled, true},
		{on, object.AutoRoutingDisabled, false},
		{off, object.AutoRoutingUnset, false},
		{off, object.AutoRoutingEnabled, true},
		{off, object.AutoRoutingDisabled, false},
		{empty, object.AutoRoutingEnabled, false},
		{empty, object.AutoRoutingUnset, false},
	}
	for _, tc := range cases {
		if got := tc.cfg.AutoRoutingActive(tc.pref); got != tc.want {
			t.Errorf("AutoRoutingActive(pref=%q) = %v, want %v", tc.pref, got, tc.want)
		}
	}
}
