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
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/ai/object"
)

// TestModelRoutingFailover_CatalogFidelityAndNoSilentSubstitution verifies that
// every advertised model in the family catalog resolves to its exact configured
// route ladder without silent model substitution, and disabled models are never
// exposed (#137).
func TestModelRoutingFailover_CatalogFidelityAndNoSilentSubstitution(t *testing.T) {
	orFees(t)
	ensoServiceAt(t)

	rs, err := ensoFam.route(context.Background())
	if err != nil {
		t.Fatalf("ensoFam.route failed: %v", err)
	}

	expectedModels := map[string]string{
		"enso-flash": "openai/gpt-6-luna",
		"enso-pro":   "openai/gpt-6.1-sol",
		"enso-max":   "claude-opus-5-5",
		"enso-ultra": "anthropic/claude-sonnet-5.5",
	}
	for modelID, primaryUpstream := range expectedModels {
		rungs, ok := rs[modelID]
		if !ok || len(rungs) == 0 {
			t.Fatalf("model %q missing from active routing table", modelID)
		}
		if rungs[0].upstream != primaryUpstream {
			t.Errorf("model %q primary upstream = %q, want %q (no silent substitution)", modelID, rungs[0].upstream, primaryUpstream)
		}
		if err := ensoFam.guard(context.Background(), modelID); err != nil {
			t.Errorf("guard rejected valid model %q: %v", modelID, err)
		}
	}

	if _, ok := rs["enso-gone"]; ok {
		t.Error("disabled model 'enso-gone' must never be present in active routing table")
	}
}

// TestModelRoutingFailover_MultiHopFailoverTrailAndMetadata verifies that when
// primary upstream providers fail with 429 rate limits or 5xx timeouts, the
// failover trail records every failed upstream arm and reason alongside the
// final serving arm's exact rate and metadata (#137).
func TestModelRoutingFailover_MultiHopFailoverTrailAndMetadata(t *testing.T) {
	orFees(t)
	ensoServiceAt(t)
	if _, err := ensoFam.route(context.Background()); err != nil {
		t.Fatal(err)
	}

	h := http.Header{}
	h.Set(servedHeader, "enso-pro")
	h.Set(armHeader, "anthropic/claude-sonnet-5.5")
	h.Set(providerHeader, "openrouter")
	h.Add(failoverHeader, "openai/gpt-6.1-sol (openrouter): upstream status 429 rate limit")
	h.Add(failoverHeader, "google/gemini-4-argon (openrouter): upstream status 503 timeout")

	rawTrail := trail(ensoFam, "enso-pro", servingOf(h), tokens{fresh: 50_000, completion: 2_000})
	var uses []armUse
	if err := json.Unmarshal([]byte(rawTrail), &uses); err != nil {
		t.Fatalf("unmarshal trail (%s) failed: %v", rawTrail, err)
	}
	if len(uses) != 3 {
		t.Fatalf("expected 3 arm entries (1 answered + 2 failed hops), got %d: %s", len(uses), rawTrail)
	}
	if !uses[0].Answered || uses[0].Upstream != "anthropic/claude-sonnet-5.5" {
		t.Errorf("uses[0] should be the answering fallback arm, got %+v", uses[0])
	}
	if uses[1].Answered || !strings.Contains(uses[1].Reason, "429") {
		t.Errorf("uses[1] should record 429 rate-limit failover, got %+v", uses[1])
	}
	if uses[2].Answered || !strings.Contains(uses[2].Reason, "503") {
		t.Errorf("uses[2] should record 503 timeout failover, got %+v", uses[2])
	}
}

// TestModelRoutingFailover_LedgerFailoverAndFallbackAccounting verifies that
// books() accurately separates primary successes, failover hops, and free-lane
// fallbacks without conflating error rows (#137).
func TestModelRoutingFailover_LedgerFailoverAndFallbackAccounting(t *testing.T) {
	orFees(t)
	now := time.Now().UTC()
	l := books([]object.Routed{
		{
			Model: "enso-pro", Served: "openai/gpt-6.1-sol", Vendor: "openrouter",
			Status: "success", Requests: 6, CostNano: 3_000_000, BilledNano: 9_000_000, KnownNano: 9_000_000,
		},
		{
			Model: "enso-pro", Served: "google/gemini-4-argon", Vendor: "openrouter",
			Status: "failover", Requests: 2,
		},
		{
			Model: "enso-pro", Served: "vendor/x:free", Vendor: "free",
			Status: "success", Requests: 2,
		},
	}, now)

	if l.Requests != 8 {
		t.Errorf("total served requests = %d, want 8", l.Requests)
	}
	if l.Failovers != 2 {
		t.Errorf("recorded failovers = %d, want 2", l.Failovers)
	}
	if l.Fallbacks != 2 {
		t.Errorf("recorded free-lane fallbacks = %d, want 2", l.Fallbacks)
	}
}
