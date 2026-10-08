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

// A stated cost of zero or less never credits the caller: it bills the ceiling.
func TestResaleIgnoresNonPositiveCost(t *testing.T) {
	tier := zenTier{MaxCtx: 1 << 20, In: decimal.New(3, 0), Out: decimal.New(15, 0)}
	m := zenModel{Margin: decimal.New(12, -1), Base: tier, Tiers: []zenTier{tier}}
	ceiling := m.resale(nil, 1000, 0, 100)
	if ceiling <= 0 {
		t.Fatalf("ceiling = %d, want a positive charge", ceiling)
	}
	for _, c := range []int64{0, -600_000_000} {
		c := c
		if nano := m.resale(&c, 1000, 0, 100); nano != ceiling {
			t.Fatalf("cost %d: nano=%d, want the ceiling %d", c, nano, ceiling)
		}
	}
}
