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
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/hanzoai/ai/object"
)

// TestFilterEnabledModelNames verifies the public provider-flags feed lists ONLY
// enabled (State=="Active") Model providers, sorted, with no secret material.
func TestFilterEnabledModelNames(t *testing.T) {
	providers := []*object.Provider{
		{Name: "zen", Category: "Model", State: "Active", ClientSecret: "kms://DO_AI_API_KEY"},
		{Name: "do-ai", Category: "Model", State: "Active", ClientSecret: "kms://DO_AI_API_KEY"},
		{Name: "fireworks", Category: "Model", State: "Disabled", ClientSecret: "kms://FIREWORKS_API_KEY"},
		{Name: "openrouter", Category: "Model", State: "Disabled", ClientSecret: "kms://OPENROUTER_API_KEY"},
		{Name: "openai-direct", Category: "Model", State: "Disabled"},
		// Non-Model providers are excluded regardless of state.
		{Name: "provider-storage-built-in", Category: "Storage", State: "Active"},
		{Name: "dummy-embedding-provider", Category: "Embedding", State: "Active"},
	}

	got := filterEnabledModelNames(providers)
	want := []string{"do-ai", "zen"} // sorted, only Active Model providers

	if !reflect.DeepEqual(got, want) {
		t.Errorf("filterEnabledModelNames = %v, want %v", got, want)
	}
}

// TestProviderFlags_JSONShapeAndSecretFree locks the exact JSON contract the
// pricing sync consumes and guards that no secret leaks into it.
func TestProviderFlags_JSONShapeAndSecretFree(t *testing.T) {
	pf := providerFlags{EnabledProviders: []string{"do-ai", "zen"}}
	blob, err := json.Marshal(pf)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(blob)
	if s != `{"enabledProviders":["do-ai","zen"]}` {
		t.Errorf("provider-flags JSON = %s, want {\"enabledProviders\":[\"do-ai\",\"zen\"]}", s)
	}
	for _, forbidden := range []string{"kms://", "clientSecret", "providerKey", "sk-", "providerUrl"} {
		if strings.Contains(s, forbidden) {
			t.Errorf("provider-flags JSON leaked %q: %s", forbidden, s)
		}
	}
}

// TestProviderFlags_OpenRouterGateDefaultOff documents the DO-first default: with
// openrouter Disabled (the seed default), it is absent from the feed, so the
// pricing sync keeps OpenRouter models unpublished. Enabling it (State=Active)
// makes it appear — the toggle and the catalog are one control.
func TestProviderFlags_OpenRouterGateDefaultOff(t *testing.T) {
	seedDefaults := []*object.Provider{
		{Name: "do-ai", Category: "Model", State: "Active"},
		{Name: "zen", Category: "Model", State: "Active"},
		{Name: "fireworks", Category: "Model", State: "Disabled"},
		{Name: "openai-direct", Category: "Model", State: "Disabled"},
		{Name: "openrouter", Category: "Model", State: "Disabled"},
	}
	off := filterEnabledModelNames(seedDefaults)
	for _, n := range off {
		if n == "openrouter" {
			t.Fatal("openrouter must be OFF by default (DO-first)")
		}
	}

	// Flip openrouter on → it appears in the feed.
	seedDefaults[4].State = "Active"
	on := filterEnabledModelNames(seedDefaults)
	found := false
	for _, n := range on {
		if n == "openrouter" {
			found = true
		}
	}
	if !found {
		t.Error("openrouter enabled but not present in provider-flags feed")
	}
}
