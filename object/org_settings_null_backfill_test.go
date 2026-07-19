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

// TestOrgSettingsNullScalarBackfill reproduces the live router-trainer regression:
// an OrgSettings row that predates the router_strategy / router_mean_field_enabled
// columns holds SQL NULL there (dbx.Sync's ALTER ADD COLUMN adds no default), so a
// full struct scan fails with "converting NULL to string is unsupported" — breaking
// GetOrgSettings (the trainer's deploy-prefer + consent-seed reads) and
// ListTrainingContributorOrgs (the contributor-list read). It proves backfillNullScalars
// repairs both to the zero value ("").
func TestOrgSettingsNullScalarBackfill(t *testing.T) {
	restore, err := UseMemoryDB("file:orgsettings_null_backfill_test?mode=memory&cache=shared", &OrgSettings{})
	if err != nil {
		t.Fatalf("UseMemoryDB: %v", err)
	}
	t.Cleanup(restore)
	invalidateOrgSettingsCache()

	// A pre-migration row: only the PK is set, so every other scalar column is SQL
	// NULL — byte-identical to a real row that existed before the columns were added.
	if _, err := adapter.db.NewQuery(`INSERT INTO org_settings (owner) VALUES ('acme')`).Execute(); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	// A contributing internal row (training_contribution set, the rest NULL) — this is
	// the shape that made the contributor-list read fail at router_mean_field_enabled.
	if _, err := adapter.db.NewQuery(
		`INSERT INTO org_settings (owner, training_contribution) VALUES ('hanzo', 'enabled')`,
	).Execute(); err != nil {
		t.Fatalf("seed contributor row: %v", err)
	}

	// RED: both reads fail exactly as the live pod logs, before the fix.
	if _, err := GetOrgSettings("acme"); err == nil {
		t.Fatal("GetOrgSettings: expected a NULL-scan error before backfill, got nil")
	}
	if _, err := ListTrainingContributorOrgs(); err == nil {
		t.Fatal("ListTrainingContributorOrgs: expected a NULL-scan error before backfill, got nil")
	}

	// Repair (the same call boot makes right after Sync).
	backfillNullScalars(adapter.db, "org_settings", &OrgSettings{})

	// GREEN: the single-row read succeeds and every scalar is the zero value.
	s, err := GetOrgSettings("acme")
	if err != nil {
		t.Fatalf("GetOrgSettings after backfill: %v", err)
	}
	if s == nil {
		t.Fatal("GetOrgSettings after backfill: expected a row, got nil")
	}
	if s.RouterStrategy != "" || s.RouterMeanFieldEnabled != "" || s.JudgeModels != "" ||
		s.RouterCostCeiling != 0 || s.RouterMeanFieldBeta != 0 || s.JudgeSample != 0 {
		t.Fatalf("after backfill, want all-zero scalars, got %+v", s)
	}

	// GREEN: the contributor-list read succeeds and returns the opted-in internal org.
	owners, err := ListTrainingContributorOrgs()
	if err != nil {
		t.Fatalf("ListTrainingContributorOrgs after backfill: %v", err)
	}
	if len(owners) != 1 || owners[0] != "hanzo" {
		t.Fatalf("contributor owners after backfill = %v, want [hanzo]", owners)
	}
}
