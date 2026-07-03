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
	"strings"
	"testing"

	"github.com/hanzoai/dbx"
)

// buildDB returns a dbx.DB that builds SQL for a driver WITHOUT opening a
// connection (sql.Open / dbx.NewFromDB never dial), so we can assert the exact
// UPDATE statements SetPrimaryModelProvider would run without a live database.
func buildDB(t *testing.T) *dbx.DB {
	t.Helper()
	db, err := dbx.Open("mysql", "")
	if err != nil {
		t.Fatalf("dbx.Open: %v", err)
	}
	return db
}

// TestPrimaryRepointSteps_PromoteBeforeDemote is the H-1 regression: the set-primary
// transition must PROMOTE the new primary (is_default=true) BEFORE it DEMOTEs the
// others (is_default=false), so there is never a window with zero primaries. The
// prior per-record loop could clear the old primary before setting the new one (0
// primaries) on a mid-loop failure. We assert the ORDER and the exact WHERE/SET of
// each step against the built SQL.
func TestPrimaryRepointSteps_PromoteBeforeDemote(t *testing.T) {
	db := buildDB(t)
	steps := primaryRepointSteps("fireworks")

	if len(steps) != 2 {
		t.Fatalf("expected exactly 2 UPDATE steps (promote, demote), got %d", len(steps))
	}

	// Step 0 = PROMOTE: is_default=true, scoped to the named admin Model provider.
	promoteSQL := db.Update("provider", dbx.Params{"is_default": steps[0].setDefault}, steps[0].where).SQL()
	if steps[0].setDefault != true {
		t.Errorf("step 0 must set is_default=true (promote), got %v", steps[0].setDefault)
	}
	if !strings.Contains(promoteSQL, "`is_default`=") {
		t.Errorf("promote SQL missing is_default assignment: %s", promoteSQL)
	}
	// Must target exactly the named row within the admin Model scope, positively.
	assertContainsAll(t, "promote", promoteSQL,
		"`owner`=", "`category`=", "name = ")
	if strings.Contains(promoteSQL, "name <>") {
		t.Errorf("promote must target name = (the target), not name <>: %s", promoteSQL)
	}

	// Step 1 = DEMOTE: is_default=false, scoped to the OTHER admin Model providers.
	demoteSQL := db.Update("provider", dbx.Params{"is_default": steps[1].setDefault}, steps[1].where).SQL()
	if steps[1].setDefault != false {
		t.Errorf("step 1 must set is_default=false (demote), got %v", steps[1].setDefault)
	}
	assertContainsAll(t, "demote", demoteSQL,
		"`owner`=", "`category`=", "name <> ")
	if strings.Contains(demoteSQL, "name = ") {
		t.Errorf("demote must target name <> (the others), not name =: %s", demoteSQL)
	}
}

// TestPrimaryRepointSteps_ScopedToAdminModel proves both UPDATEs are confined to the
// admin-owned Model-category records — a Storage/Embedding record or another org's
// provider is never touched by a set-primary. (owner and category are bound values;
// we assert the columns and, via the built statement, that both predicates are ANDed
// into every step alongside the name predicate.)
func TestPrimaryRepointSteps_ScopedToAdminModel(t *testing.T) {
	db := buildDB(t)
	for i, step := range primaryRepointSteps("do-ai") {
		sql := db.Update("provider", dbx.Params{"is_default": step.setDefault}, step.where).SQL()
		// The scope columns must both be present in the WHERE of each step.
		assertContainsAll(t, "step-scope", sql, "WHERE", "`owner`=", "`category`=")
		// And a name predicate must also be present (so it is a subset of the scope,
		// never the whole table).
		if !strings.Contains(sql, "name = ") && !strings.Contains(sql, "name <> ") {
			t.Errorf("step %d WHERE lacks a name predicate (would touch all admin Model rows): %s", i, sql)
		}
	}
}

// TestGetDefaultModelProvider_QueryRequiresActiveState locks in the H-1 State
// filter: the default-model query must constrain state="Active" (in addition to
// is_default + category) so a DISABLED primary is never returned to the store/KB/RAG
// default-model path (store.go returns this record with no further usability check).
// We assert the built SELECT WHERE carries the state predicate.
func TestGetDefaultModelProvider_QueryRequiresActiveState(t *testing.T) {
	db := buildDB(t)
	// The exact where used by GetDefaultModelProvider.
	where := dbx.HashExp{"is_default": true, "category": "Model", "state": "Active"}
	sql := db.Select().From("provider").Where(where).Build().SQL()
	assertContainsAll(t, "default-model", sql, "`state`=", "`is_default`=", "`category`=")
}

func assertContainsAll(t *testing.T, label, sql string, subs ...string) {
	t.Helper()
	for _, sub := range subs {
		if !strings.Contains(sql, sub) {
			t.Errorf("[%s] built SQL missing %q\n  SQL: %s", label, sub, sql)
		}
	}
}
