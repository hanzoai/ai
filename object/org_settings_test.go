// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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

import (
	"strings"
	"testing"
	"time"
)

// TestOrgSettingsCacheInvalidationOnWrite proves the hot-path cache contract the
// auto-routing doctrine relies on: GetCachedOrgAutoRouting caches a row for the
// 60s TTL, and every write helper (Add/Update/Delete) invalidates that cache so an
// admin change to the settings row takes effect AT ONCE — not after the TTL. It
// runs against the canonical embedded SQLite store, no CI DB needed.
func TestOrgSettingsCacheInvalidationOnWrite(t *testing.T) {
	restore, err := UseMemoryDB("file:orgsettings_cache_test?mode=memory&cache=shared", &OrgSettings{})
	if err != nil {
		t.Fatalf("UseMemoryDB: %v", err)
	}
	t.Cleanup(restore)
	// The org-settings cache is process-wide; clear any entry a prior test left so
	// this test starts from a cold cache.
	invalidateOrgSettingsCache()

	const org = "hanzo"

	// 1. Add the row enabled; a cached read reflects it and populates the cache.
	if _, err := AddOrgSettings(&OrgSettings{Owner: org, AutoRouting: AutoRoutingEnabled}); err != nil {
		t.Fatalf("AddOrgSettings: %v", err)
	}
	if got := GetCachedOrgAutoRouting(org); got != AutoRoutingEnabled {
		t.Fatalf("after add: cached auto-routing = %q, want %q", got, AutoRoutingEnabled)
	}

	// 2. A raw DB mutation that bypasses the write helpers must NOT be visible
	// through the cache yet — proving the cache is real (else step 3 proves nothing).
	raw := &OrgSettings{Owner: org, AutoRouting: AutoRoutingDisabled, UpdatedTime: time.Now().Format(time.RFC3339)}
	if err := adapter.db.Model(raw).Update(); err != nil {
		t.Fatalf("raw update: %v", err)
	}
	if got := GetCachedOrgAutoRouting(org); got != AutoRoutingEnabled {
		t.Fatalf("cached read saw an un-invalidated raw write: %q, want stale %q", got, AutoRoutingEnabled)
	}

	// 3. UpdateOrgSettings invalidates the cache, so the new value is visible at
	// once. This is the invalidation-on-write contract.
	if _, err := UpdateOrgSettings(org, &OrgSettings{Owner: org, AutoRouting: AutoRoutingDisabled}); err != nil {
		t.Fatalf("UpdateOrgSettings: %v", err)
	}
	if got := GetCachedOrgAutoRouting(org); got != AutoRoutingDisabled {
		t.Fatalf("after update: cached auto-routing = %q, want %q (cache not invalidated on write)", got, AutoRoutingDisabled)
	}

	// 4. Delete also invalidates: the row is gone, so the read falls back to unset.
	if _, err := DeleteOrgSettings(&OrgSettings{Owner: org}); err != nil {
		t.Fatalf("DeleteOrgSettings: %v", err)
	}
	if got := GetCachedOrgAutoRouting(org); got != AutoRoutingUnset {
		t.Fatalf("after delete: cached auto-routing = %q, want %q", got, AutoRoutingUnset)
	}
}

// TestGlobalDefaultOwnerRowResolves proves the "*" GlobalDefaultOwner row — the
// single source of truth for the platform-wide routing default — is read back
// through the same cached accessor the hot path uses.
func TestGlobalDefaultOwnerRowResolves(t *testing.T) {
	restore, err := UseMemoryDB("file:orgsettings_global_test?mode=memory&cache=shared", &OrgSettings{})
	if err != nil {
		t.Fatalf("UseMemoryDB: %v", err)
	}
	t.Cleanup(restore)
	invalidateOrgSettingsCache()

	// No row yet → unset (the deprecated env / conf flag decides downstream).
	if got := GetCachedOrgAutoRouting(GlobalDefaultOwner); got != AutoRoutingUnset {
		t.Fatalf("no global row: cached auto-routing = %q, want %q", got, AutoRoutingUnset)
	}

	if _, err := AddOrgSettings(&OrgSettings{Owner: GlobalDefaultOwner, AutoRouting: AutoRoutingEnabled}); err != nil {
		t.Fatalf("AddOrgSettings(*): %v", err)
	}
	if got := GetCachedOrgAutoRouting(GlobalDefaultOwner); got != AutoRoutingEnabled {
		t.Fatalf("global row enabled: cached auto-routing = %q, want %q", got, AutoRoutingEnabled)
	}
}

// TestGetCachedJudgeConfig proves the platform-global judge config: with NO "*"
// row it resolves the built-in defaults (judge ON, the 3-model diverse panel,
// ~10% sample), and a "*" GlobalDefaultOwner row live-overrides each field — the
// DB-backed dynamic knob admin.hanzo.ai flips, no env, no restart.
func TestGetCachedJudgeConfig(t *testing.T) {
	restore, err := UseMemoryDB("file:orgsettings_judge_test?mode=memory&cache=shared", &OrgSettings{})
	if err != nil {
		t.Fatalf("UseMemoryDB: %v", err)
	}
	t.Cleanup(restore)
	invalidateOrgSettingsCache()

	// No "*" row → defaults: ON, the default diverse panel, default sample.
	def := GetCachedJudgeConfig()
	if !def.Enabled || def.Models != JudgeModelsDefault || def.Sample != JudgeSampleDefault {
		t.Fatalf("default judge config = %+v, want enabled/%q/%v", def, JudgeModelsDefault, JudgeSampleDefault)
	}
	if def.URL != "" {
		t.Fatalf("default judge URL = %q, want empty (self)", def.URL)
	}
	// The default panel is a diverse committee (>1 model ⇒ MFJP).
	if got := len(splitJudgeModelsForTest(def.Models)); got < 2 {
		t.Fatalf("default panel size = %d, want >=2 (MFJP)", got)
	}

	// A "*" row live-overrides every field — the admin.hanzo.ai knob.
	row := &OrgSettings{
		Owner:        GlobalDefaultOwner,
		JudgeEnabled: JudgeConfigDisabled,
		JudgeModels:  "m1,m2",
		JudgeURL:     "https://tee.hanzo.ai",
		JudgeSample:  0.25,
	}
	if _, err := AddOrgSettings(row); err != nil {
		t.Fatalf("AddOrgSettings(*): %v", err)
	}
	got := GetCachedJudgeConfig()
	if got.Enabled {
		t.Fatalf("row judgeEnabled=disabled: Enabled = true, want false")
	}
	if got.Models != "m1,m2" || got.URL != "https://tee.hanzo.ai" || got.Sample != 0.25 {
		t.Fatalf("row override = %+v, want m1,m2 / tee / 0.25", got)
	}

	// Flip back to enabled; unset model/url/sample fall through to defaults.
	row.JudgeEnabled = JudgeConfigEnabled
	row.JudgeModels, row.JudgeURL, row.JudgeSample = "", "", 0
	if _, err := UpdateOrgSettings(GlobalDefaultOwner, row); err != nil {
		t.Fatalf("UpdateOrgSettings(*): %v", err)
	}
	got = GetCachedJudgeConfig()
	if !got.Enabled || got.Models != JudgeModelsDefault || got.Sample != JudgeSampleDefault || got.URL != "" {
		t.Fatalf("enabled + unset fields = %+v, want defaults", got)
	}
}

// splitJudgeModelsForTest is a tiny comma splitter local to this test — the object
// package must not import the controllers' splitModels, so the panel-size assertion
// stays self-contained.
func splitJudgeModelsForTest(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
