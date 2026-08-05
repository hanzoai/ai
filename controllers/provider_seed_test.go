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
	"strings"
	"testing"

	"github.com/hanzoai/ai/object"
)

// TestSeededFamilyProvidersDoNotCarryV1 guards the defect that silently took the
// entire zen family off /v1/models.
//
// A model FAMILY appends its own /v1: discovery does base+"/v1/models" and
// pipeToFamily does ProviderUrl+"/v1/"+apiPath. So a seeded row whose
// ProviderUrl ALREADY carries /v1 sends every call to .../v1/v1/... and 404s.
// The direct-relay providers are the exact opposite — nothing appends for them,
// so they MUST carry /v1. That inversion is why this is easy to get wrong twice,
// and why it is asserted here rather than remembered: openrouter hit it and was
// caught, zen hit it and shipped broken for weeks.
//
// The failure is invisible at runtime. The family just serves nothing, and the
// only symptom is a once-a-minute "catalog refresh failed: status 404" warning.
func TestSeededFamilyProvidersDoNotCarryV1(t *testing.T) {
	seeded := object.SeededModelProviders()
	for _, name := range object.FamilyProviderNames() {
		url, isSeeded := seeded[name]
		if !isSeeded {
			continue
		}
		if strings.HasSuffix(strings.TrimRight(url, "/"), "/v1") {
			t.Errorf("seeded family provider %q has ProviderUrl %q ending in /v1; "+
				"the family paths append /v1 themselves, so every call would 404",
				name, url)
		}
	}
}

// TestZenIsNotSeeded pins the decision that zen has NO seed row at all.
//
// zen's address is deployment config (ZEN_URL → the in-cluster zen service),
// exactly like its sibling enso — which was never seeded and never broke. A row
// is read by familyProvider as an operator OVERRIDE and wins over that config,
// so seeding one hijacks the family. Dropping the /v1 would NOT be enough: it
// would merely aim zen at DigitalOcean's catalog instead of the service that
// actually serves zen SKUs.
func TestZenIsNotSeeded(t *testing.T) {
	if url, ok := object.SeededModelProviders()["zen"]; ok {
		t.Fatalf("zen must not be seeded: its address is ZEN_URL and a seeded row "+
			"overrides it (got ProviderUrl %q)", url)
	}
}
