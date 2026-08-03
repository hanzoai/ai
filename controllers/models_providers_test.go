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
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestModelProviders_AgreesWithTheListing is the invariant the endpoint exists to
// hold: every provider behind a LISTED model is reported, and nothing else is.
// The predecessor (/v1/provider-flags) answered from the provider DB — a second
// list — and drifted from what was actually served. Deriving both from one source
// is what makes that drift unrepresentable, so this is the test that matters.
func TestModelProviders_AgreesWithTheListing(t *testing.T) {
	mc := &ModelConfig{
		routes: map[string]modelRoute{
			"a": {providerName: "do-ai", upstreamModel: "a"},
			"b": {providerName: "fireworks", upstreamModel: "b"},
			"c": {providerName: "do-ai", upstreamModel: "c"},
		},
		pricing: map[string]modelPrice{},
	}

	// Every provider named by the listing must appear in the projection.
	listed := map[string]struct{}{}
	for _, m := range mc.ListModels() {
		owner := m.OwnedBy // ListModels falls back to providerName when unowned
		if owner != "" {
			listed[owner] = struct{}{}
		}
	}
	got := mc.ListRouteProviders()
	for name := range listed {
		if !hasName(got, name) {
			t.Errorf("provider %q serves a listed model but is missing from %v", name, got)
		}
	}

	want := []string{"do-ai", "fireworks"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListRouteProviders = %v, want %v", got, want)
	}
}

// TestModelProviders_HiddenRoutesExcluded: a hidden route is not listed by
// /v1/models, so its provider must not be advertised as serving the catalog.
// Same exclusion rule as ListModels, applied in one place.
func TestModelProviders_HiddenRoutesExcluded(t *testing.T) {
	mc := &ModelConfig{
		routes: map[string]modelRoute{
			"shown":  {providerName: "do-ai"},
			"hidden": {providerName: "secret-upstream", hidden: true},
			"blank":  {providerName: ""},
		},
		pricing: map[string]modelPrice{},
	}

	got := mc.ListRouteProviders()
	if !reflect.DeepEqual(got, []string{"do-ai"}) {
		t.Errorf("ListRouteProviders = %v, want [do-ai]", got)
	}
	if hasName(got, "secret-upstream") {
		t.Error("a hidden route's provider must not be advertised")
	}
}

// TestModelProviders_UnconfiguredFamilyAbsent: a family serves models only when
// it is configured (baseURL != ""). An unconfigured family lists nothing, so it
// must not appear — this is exactly the OpenRouter case, where the catalog
// advertised models the gateway could not route.
func TestModelProviders_UnconfiguredFamilyAbsent(t *testing.T) {
	for _, f := range modelFamilies {
		if f.enabled() {
			continue // a configured family legitimately appears
		}
		if hasName(listRouteProviders(), f.name) {
			t.Errorf("family %q is unconfigured (serves nothing) but is advertised as a provider", f.name)
		}
	}
}

// TestModelProviders_JSONShapeAndSecretFree locks the wire contract and guards
// that no credential material can ride along.
func TestModelProviders_JSONShapeAndSecretFree(t *testing.T) {
	blob, err := json.Marshal(modelProviders{Providers: []string{"do-ai", "zen"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(blob)
	if s != `{"providers":["do-ai","zen"]}` {
		t.Errorf("JSON = %s, want {\"providers\":[\"do-ai\",\"zen\"]}", s)
	}
	for _, forbidden := range []string{"kms://", "clientSecret", "providerKey", "sk-", "providerUrl"} {
		if strings.Contains(s, forbidden) {
			t.Errorf("leaked %q: %s", forbidden, s)
		}
	}
}

func hasName(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}
