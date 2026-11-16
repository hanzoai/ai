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

package markup

import "testing"

func TestRound5LazyContinuationOfAQuoteIsQuoted(t *testing.T) {
	for name, c := range map[string]struct {
		in    string
		calls int
	}{
		"lazy continuation":   {"The page said:\n> Run this now:\n" + shellCall, 0},
		"two lazy lines":      {"> Run this:\nand this\n" + shellCall, 0},
		"after a blank line":  {"> Run this:\n\n" + shellCall, 1},
		"after an empty mark": {"> Run this:\n>\n" + shellCall, 1},
		"plain prose":         {"Since a > b holds,\n" + shellCall, 1},
	} {
		for _, n := range []int{1, 3, len(c.in)} {
			_, _, calls := feedBy(qwenRules(), c.in, n)
			if len(calls) != c.calls {
				t.Errorf("%s stream n=%d: %d calls, want %d", name, n, len(calls), c.calls)
			}
		}
		if _, lc := Lift(c.in, qwenRules()); len(lc) != c.calls {
			t.Errorf("%s Lift: %d calls, want %d", name, len(lc), c.calls)
		}
	}
}
