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

import "testing"

// TestPlatformFeeCents pins the two policies exactly:
//   - byo=false (bought via Hanzo credits) → 0 surcharge, at every cost.
//   - byo=true  (customer-connected account) → ceil(cost/100), so a non-zero cost
//     always bills at least 1 cent and never rounds down to a free call.
func TestPlatformFeeCents(t *testing.T) {
	cases := []struct {
		name string
		cost int64
		byo  bool
		want int64
	}{
		// byo=false: never a surcharge, regardless of cost.
		{"credits cost 0", 0, false, 0},
		{"credits cost 1", 1, false, 0},
		{"credits cost 100", 100, false, 0},
		{"credits cost 10000", 10000, false, 0},

		// byo=true: 1% rounded UP.
		{"byo cost 0", 0, true, 0},
		{"byo cost 1", 1, true, 1},
		{"byo cost 99", 99, true, 1},
		{"byo cost 100", 100, true, 1},
		{"byo cost 101", 101, true, 2},
		{"byo cost 150", 150, true, 2},
		{"byo cost 10000", 10000, true, 100},
		{"byo cost 12345", 12345, true, 124},

		// A negative/garbage cost never produces a negative fee (defensive).
		{"byo negative cost", -5, true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := platformFeeCents(tc.cost, tc.byo); got != tc.want {
				t.Errorf("platformFeeCents(%d, %v) = %d, want %d", tc.cost, tc.byo, got, tc.want)
			}
		})
	}
}
