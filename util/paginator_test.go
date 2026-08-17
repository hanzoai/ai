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

package util

import (
	"testing"
)

// TestPaginatorOffsetAndNums pins the Offset and Nums the list handlers
// consume. The expected values were cross-checked against the historical
// paginator: pageCount = ceil(nums/per) with per defaulting to 10, the page
// is read from "p" and clamped into [1, pageCount], and Offset = (page-1)*per.
func TestPaginatorOffsetAndNums(t *testing.T) {
	cases := []struct {
		p          int
		per        int
		nums       int64
		wantOffset int
		wantNums   int64
	}{
		{0, 10, 0, 0, 0},
		{0, 10, 55, 0, 55}, // absent: page 1
		{1, 10, 55, 0, 55},
		{2, 10, 55, 10, 55},
		{6, 10, 55, 50, 55},   // last page: ceil(55/10) = 6
		{7, 10, 55, 50, 55},   // beyond last: clamp to 6
		{99, 10, 55, 50, 55},  // far beyond: clamp
		{0, 10, 55, 0, 55},    // non-positive: page 1
		{-3, 10, 55, 0, 55},   // negative: page 1
		{3, 25, 100, 50, 100}, // (3-1)*25
		{2, 0, 30, 10, 30},    // per <= 0 defaults to 10
		{3, 20, 0, 0, 0},      // zero count: page 1, offset 0
		{1, 10, 9, 0, 9},
		{2, 10, 20, 10, 20},
	}
	for _, c := range cases {
		p := NewPaginator(c.p, c.per, c.nums)
		if got := p.Offset(); got != c.wantOffset {
			t.Errorf("p=%d per=%d nums=%d: Offset = %d, want %d", c.p, c.per, c.nums, got, c.wantOffset)
		}
		if got := p.Nums(); got != c.wantNums {
			t.Errorf("p=%d per=%d nums=%d: Nums = %d, want %d", c.p, c.per, c.nums, got, c.wantNums)
		}
	}
}
