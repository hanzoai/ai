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

package router

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestPerThousandPerMillionConversion pins the $/1M ↔ $/1k relationship the whole cost
// stack depends on: a $/1M rate is exactly 1000× the $/1k rate for the same tokens.
func TestPerThousandPerMillionConversion(t *testing.T) {
	cases := []struct{ perMillion, perThousand float64 }{
		{3.0, 0.003},
		{10.0, 0.010},
		{0.5, 0.0005},
		{0, 0},
	}
	for _, tc := range cases {
		if got := PerMillionToPerThousand(tc.perMillion); math.Abs(got-tc.perThousand) > 1e-12 {
			t.Errorf("PerMillionToPerThousand(%v) = %v, want %v", tc.perMillion, got, tc.perThousand)
		}
		if got := PerThousandToPerMillion(tc.perThousand); math.Abs(got-tc.perMillion) > 1e-9 {
			t.Errorf("PerThousandToPerMillion(%v) = %v, want %v", tc.perThousand, got, tc.perMillion)
		}
	}
}

// TestSloMaxCostForwardedVerbatim proves the enso SLO forward uses the SAME unit end to
// end: the per-1k cost ceiling placed on Slo.MaxCost reaches the engine's /route body
// unchanged — no accidental ×1000 or ÷1000 in the forward. A $3/1M price is expressed in
// the ceiling's per-1k unit via PerMillionToPerThousand and must arrive as exactly that.
func TestSloMaxCostForwardedVerbatim(t *testing.T) {
	ceiling := PerMillionToPerThousand(3.0) // 0.003 $/1k — a $3/1M price in ceiling units

	var got engineRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode engine request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(engineResponse{Model: "zen4", Task: "reasoning", Confidence: 1})
	}))
	defer srv.Close()

	c := Client{Endpoint: srv.URL, Policy: testPolicy()}
	c.Route(context.Background(), Request{Text: "hi", ApproxTokens: 2}, Slo{MaxCost: ceiling})

	// The engine receives the ceiling verbatim (JSON float round-trips exactly).
	if got.Slo.MaxCost != ceiling {
		t.Fatalf("engine max_cost = %v, want %v verbatim (per-1k, no unit change in the forward)", got.Slo.MaxCost, ceiling)
	}
	if math.Abs(got.Slo.MaxCost-0.003) > 1e-12 {
		t.Fatalf("forwarded max_cost = %v, want 0.003 (a $3/1M price in per-1k units)", got.Slo.MaxCost)
	}
}
