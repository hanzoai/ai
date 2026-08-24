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

package object

import (
	"testing"

	"github.com/hanzoai/ai/util"
)

// A message carries a store's NAME, and a name belongs to whoever chose it. Two
// organizations may each keep a store called "docs", so a report that asks by name
// alone counts both as one — and the number it hands one of them is partly the
// other's traffic.
func TestUsageCountsOneOrganizationsMessages(t *testing.T) {
	withStore(t)

	for _, m := range []*Message{
		{Owner: "admin", Name: "a1", Organization: "acme", Store: "docs", Text: "ours"},
		{Owner: "admin", Name: "a2", Organization: "acme", Store: "docs", Text: "ours"},
		{Owner: "admin", Name: "v1", Organization: "victim", Store: "docs", Text: "theirs"},
	} {
		m.CreatedTime = util.GetCurrentTime()
		if _, err := AddMessage(m); err != nil {
			t.Fatal(err)
		}
	}

	mine, err := GetGlobalMessagesByStoreName("acme", "docs")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range mine {
		if m.Organization != "acme" {
			t.Errorf("a report for acme carried %s/%s from %q", m.Owner, m.Name, m.Organization)
		}
	}
	if len(mine) != 2 {
		t.Errorf("acme's store holds %d messages, want its own 2", len(mine))
	}

	// The reserved org reads every organization's.
	all, err := GetGlobalMessagesByStoreName("", "docs")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Errorf("the reserved org saw %d messages, want all 3", len(all))
	}
}
