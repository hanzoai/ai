// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package object

import "testing"

// TestModelRouteNullPricedBackfill is the failure adding Priced actually caused,
// which is not the one it was added for.
//
// dbx.Sync grows a table with ALTER TABLE ADD COLUMN and no default, so every route
// that existed before the column holds SQL NULL there — and database/sql refuses
// NULL into a bool, failing the WHOLE row read. Measured against a live store: an
// update of any pre-existing route answered
//
//	sql: Scan error on column index 18, name "priced": couldn't convert <nil> into type bool
//
// so the column that was meant to let a route say "this costs nothing" instead made
// every route already written unreadable. This pins the repair boot performs.
func TestModelRouteNullPricedBackfill(t *testing.T) {
	restore, err := UseMemoryDB("file:modelroute_priced_backfill_test?mode=memory&cache=shared", &ModelRoute{})
	if err != nil {
		t.Fatalf("UseMemoryDB: %v", err)
	}
	t.Cleanup(restore)

	// Byte-identical to a row that predates the column: PK set, everything else NULL.
	if _, err := adapter.db.NewQuery(
		`INSERT INTO model_route (owner, model_name) VALUES ('built-in', 'legacy')`,
	).Execute(); err != nil {
		t.Fatalf("seed pre-column row: %v", err)
	}

	// RED: the read fails exactly as it did against the live store.
	if _, err := GetModelRoute("built-in", "legacy"); err == nil {
		t.Fatal("expected a NULL-scan error before backfill, got nil")
	}

	// Repair — the same call boot makes right after Sync.
	backfillNullScalars(adapter.db, "model_route", &ModelRoute{})

	// GREEN: the row reads, and an unstated price is false, never free.
	r, err := GetModelRoute("built-in", "legacy")
	if err != nil {
		t.Fatalf("GetModelRoute after backfill: %v", err)
	}
	if r == nil {
		t.Fatal("route vanished")
	}
	if r.Priced {
		t.Fatal("a NULL priced column repaired to true — an unstated route would serve free")
	}
}
