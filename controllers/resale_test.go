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

	"github.com/hanzoai/decimal"
)

// A stated cost of zero or less never credits the caller: a variable SKU bills its
// ceiling.
func TestResaleIgnoresNonPositiveCost(t *testing.T) {
	tier := zenTier{MaxCtx: 1 << 20, In: decimal.New(3, 0), Out: decimal.New(15, 0)}
	m := zenModel{Margin: decimal.New(12, 1), Variable: true, Base: tier, Tiers: []zenTier{tier}}
	ceiling := m.bill(nil, tokens{fresh: 1000, cached: 0, completion: 100})
	if ceiling <= 0 {
		t.Fatalf("ceiling = %d, want a positive charge", ceiling)
	}
	for _, c := range []int64{0, -600_000_000} {
		c := c
		if nano := m.bill(&c, tokens{fresh: 1000, cached: 0, completion: 100}); nano != ceiling {
			t.Fatalf("cost %d: nano=%d, want the ceiling %d", c, nano, ceiling)
		}
	}
}

// A SKU we resell bills its tokens at retail, and never less than what the vendor
// says the call cost us times the margin: a cache write or a search the token rates
// do not price is still paid for. A SKU we do not resell bills its tokens alone.
func TestAResoldCallNeverBillsBelowItsStatedCost(t *testing.T) {
	tier := zenTier{MaxCtx: 1 << 20, In: decimal.New(12, 2), Out: decimal.New(6, 1)} // $0.12 and $0.60 per MTok
	m := zenModel{Margin: decimal.New(12, 1), Base: tier, Tiers: []zenTier{tier}}
	retail := m.bill(nil, tokens{fresh: 1000, cached: 0, completion: 100}) // 1000 × 0.12e-6 + 100 × 0.6e-6 = $0.00018
	if retail != 180_000 {
		t.Fatalf("retail = %d nano, want 180000", retail)
	}
	under := int64(100_000) // the vendor charged $0.0001: retail covers it
	if got := m.bill(&under, tokens{fresh: 1000, cached: 0, completion: 100}); got != retail {
		t.Errorf("a cost retail covers billed %d, want retail %d", got, retail)
	}
	over := int64(200_000) // a cache write made it $0.0002: billed at 1.2 × that
	if got := m.bill(&over, tokens{fresh: 1000, cached: 0, completion: 100}); got != 240_000 {
		t.Errorf("a cost above retail billed %d, want 240000", got)
	}
	own := zenModel{Base: tier, Tiers: []zenTier{tier}}
	if got := own.bill(&over, tokens{fresh: 1000, cached: 0, completion: 100}); got != retail {
		t.Errorf("a SKU we do not resell billed %d, want its tokens %d", got, retail)
	}
}
