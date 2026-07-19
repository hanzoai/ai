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
	"sync"
	"time"

	"github.com/hanzoai/dbx"
)

// Auto-routing preference values for OrgSettings.AutoRouting and
// OrgSettings.DefaultSessionRouting. The empty string means "unset" — resolution
// falls through to the "*" global-default row and then the conf file. See
// resolveAutoModel and effectiveAutoRouting.
const (
	AutoRoutingUnset    = ""
	AutoRoutingEnabled  = "enabled"
	AutoRoutingDisabled = "disabled"
)

// GlobalDefaultOwner is the reserved OrgSettings.Owner for the platform-wide
// default row. Its writes are RequireSuperAdmin-gated exactly like any other
// org's; it is read as a fallback between a real org's row and the conf file. No
// real request ever resolves its org to "*" (GetOrg derives the org from
// the verified principal), so this owner is never mistaken for a tenant.
const GlobalDefaultOwner = "*"

// OrgSettings holds per-org overrides for platform features. One row per org
// (Owner is the sole primary key), mirroring the ModelRoute storage pattern.
type OrgSettings struct {
	Owner       string `db:"pk" json:"owner"` // org ID
	CreatedTime string `json:"createdTime"`
	UpdatedTime string `json:"updatedTime"`

	// AutoRouting overrides the global auto-routing flag for this org:
	// "" (unset) → "*" row then global flag decides, "enabled" → opt in even if
	// global off (when router config is present), "disabled" → opt out.
	AutoRouting string `json:"autoRouting"`

	// DefaultSessionRouting is the admin-settable default for whether new chat
	// sessions default to auto-routing in the client (console/chat/app/desktop
	// read it via /v1/get-routing-defaults). Same three-state as AutoRouting:
	// "" (unset) → "*" row then conf default (disabled), "enabled", "disabled".
	DefaultSessionRouting string `json:"defaultSessionRouting"`

	// RouterPrefer is this org's router preference table: task tag → ordered
	// model ids ("default" is the catch-all). nil/empty = unset → fall through
	// per task key to the "*" row then the conf router block. Persisted as a
	// JSON text column (JSONMap: database/sql rejects a bare map of slices).
	RouterPrefer JSONMap[[]string] `json:"routerPrefer"`

	// RouterCostCeiling caps the router's cost for this org in USD PER 1K TOKENS
	// (per-1k — not the $/1M pricing-table unit). 0 = unset → "*" row then conf. A
	// caller's X-Max-Cost header (also per-1k) still wins per request.
	RouterCostCeiling float64 `json:"routerCostCeiling"`

	// RouterStrategy pins which routing strategy leads for this org: "" (unset →
	// "*" row then the enso-leaning default), "enso" (delegate to the learned Enso
	// model), or "heuristic" (rules only — never call the engine). Lets an org opt
	// out of the learned router deterministically even when the engine is configured.
	RouterStrategy string `json:"routerStrategy"`

	// RouterOverrides are this org's deterministic task→model pins, applied BEFORE
	// any strategy: RouterOverrides["code"] forces a model for coding tasks,
	// RouterOverrides["default"] is the catch-all. An override naming a model the org
	// cannot serve is skipped, never honored blindly. nil/empty = none. Persisted as
	// a JSON text column (JSONMap).
	RouterOverrides JSONMap[string] `json:"routerOverrides"`

	// TrainingContribution is the org's opt-in for contributing its routing
	// events to the shared router-heads retrain (the Tier-2 base refresh in
	// universe/docs/architecture/personal-router-training.md). It gates ONLY
	// whether this org's privacy-preserving events (feature vectors + routed
	// model + reward — NEVER prompt text) join the cross-org base fit; per-org
	// heads always train on the org's own events regardless. Three-state, same
	// as AutoRouting: "" (unset) → treated as opted-OUT (privacy default), the
	// nightly base-refresh job includes only "enabled" orgs.
	TrainingContribution string `json:"trainingContribution"`

	// Judge* configure the LLM-as-a-judge dense-reward path (the Mean-Field Judge
	// Panel). They are PLATFORM-GLOBAL, not per-org: read ONLY from the "*"
	// GlobalDefaultOwner row (like the trainer's "*" RouterPrefer), live-tunable at
	// admin.hanzo.ai via /v1/update-org-settings — no env, no restart. NO secret
	// lives here: the judge presents the gateway's existing internal service bearer
	// (ROUTER_PROBE_TOKEN, env/KMS), never a DB value. Every field is fail-safe to
	// the built-in default when unset (GetCachedJudgeConfig):
	//   JudgeEnabled — three-state "" (unset → default ON) / "enabled" / "disabled".
	//   JudgeModels  — comma-separated served model ids; >1 ⇒ the MFJP panel. "" → the default panel.
	//   JudgeURL     — judge inference endpoint. "" → self; point at an attested TEE for confidential inference.
	//   JudgeSample  — fraction of eligible turns judged, 0<f<=1. 0 → the default rate.
	JudgeEnabled string  `json:"judgeEnabled"`
	JudgeModels  string  `json:"judgeModels"`
	JudgeURL     string  `json:"judgeUrl"`
	JudgeSample  float64 `json:"judgeSample"`

	// RouterMeanField* configure the congestion-aware mean-field routing LAYER
	// (controllers/router_meanfield.go). PLATFORM-GLOBAL like the Judge* knobs: read
	// ONLY from the "*" GlobalDefaultOwner row, live-tunable at admin.hanzo.ai via
	// /v1/update-org-settings — no env, no restart. The layer is a pure best-response
	// equilibrium that spreads `auto` load across near-equal-preference models instead
	// of stampeding the single champion. It is DISABLED by default so live routing is
	// byte-identical until an admin opts in (GetCachedMeanFieldConfig):
	//   RouterMeanFieldEnabled — three-state "" (unset → default OFF) / "enabled" / "disabled".
	//   RouterMeanFieldBeta    — congestion coefficient β >= 0 (0 → default). Higher ⇒ more load-spreading.
	RouterMeanFieldEnabled string  `json:"routerMeanFieldEnabled"`
	RouterMeanFieldBeta    float64 `json:"routerMeanFieldBeta"`
}

// TrainingContribution values for OrgSettings.TrainingContribution. The empty
// string is the privacy-safe default (opted OUT of the cross-org base refresh).
const (
	TrainingContributionUnset    = ""
	TrainingContributionEnabled  = "enabled"
	TrainingContributionDisabled = "disabled"
)

// JudgeEnabled three-state values for OrgSettings.JudgeEnabled — the same
// vocabulary as AutoRouting/TrainingContribution. Unset ("") resolves to the
// built-in default (JudgeEnabledDefault) in GetCachedJudgeConfig.
const (
	JudgeConfigUnset    = ""
	JudgeConfigEnabled  = "enabled"
	JudgeConfigDisabled = "disabled"
)

// Judge config defaults — the platform-global LLM-as-a-judge posture applied
// wherever the "*" GlobalDefaultOwner row leaves a field unset. On deploy, with NO
// admin action, the Mean-Field Judge Panel runs on ~10% of eligible (opted-in,
// non-EU-protected) traffic across a diverse, cheap, non-premium served panel;
// admin tunes or disables it live at admin.hanzo.ai.
const (
	// JudgeEnabledDefault arms the judge by default (opt-out for admins).
	JudgeEnabledDefault = true
	// JudgeModelsDefault is a DIVERSE panel — a cheap Anthropic haiku, a cheap OpenAI
	// mini, and a small open Meta model. Different model families decorrelate
	// systematic error (the MFJP diversity property), and all three serve via do-ai
	// with no balance gate, so the panel works out of the box.
	JudgeModelsDefault = "claude-3-5-haiku,gpt-4o-mini,llama-3.1-8b"
	// JudgeSampleDefault judges ~10% of eligible turns.
	JudgeSampleDefault = 0.1
)

// JudgeConfig is the resolved, platform-global judge configuration — the "*"
// GlobalDefaultOwner OrgSettings row with every unset field filled by its built-in
// default. NO secret: the bearer is resolved separately from env/KMS at call time.
type JudgeConfig struct {
	Enabled bool
	Models  string  // comma-separated panel of served model ids (>1 ⇒ MFJP)
	URL     string  // "" = self (the caller applies the self endpoint)
	Sample  float64 // (0,1] — fraction of eligible turns judged
}

// GetCachedJudgeConfig resolves the live judge config from the 60s-cached "*"
// GlobalDefaultOwner row, applying the built-in default for every unset field.
// Fail-safe: on any missing row or read error it returns the defaults (judge ON
// with the default panel), so a lookup blip never silently disables quality
// rewards. Read cheaply on the judge's refresh tick — admin.hanzo.ai edits to the
// "*" row take effect within one cache/refresh window, no restart.
func GetCachedJudgeConfig() JudgeConfig {
	out := JudgeConfig{Enabled: JudgeEnabledDefault, Models: JudgeModelsDefault, Sample: JudgeSampleDefault}
	s, err := GetCachedOrgSettings(GlobalDefaultOwner)
	if err != nil || s == nil {
		return out
	}
	switch s.JudgeEnabled {
	case JudgeConfigEnabled:
		out.Enabled = true
	case JudgeConfigDisabled:
		out.Enabled = false
	}
	if m := strings.TrimSpace(s.JudgeModels); m != "" {
		out.Models = m
	}
	if u := strings.TrimSpace(s.JudgeURL); u != "" {
		out.URL = u
	}
	if s.JudgeSample > 0 && s.JudgeSample <= 1 {
		out.Sample = s.JudgeSample
	}
	return out
}

// RouterMeanFieldEnabled three-state values — the same vocabulary as
// AutoRouting/JudgeEnabled. Unset ("") resolves to the built-in default (OFF).
const (
	MeanFieldConfigUnset    = ""
	MeanFieldConfigEnabled  = "enabled"
	MeanFieldConfigDisabled = "disabled"
)

// Mean-field routing defaults — the congestion-aware layer is OFF by default, so a
// fresh deploy (and any lookup blip) routes EXACTLY as it does today until an admin
// enables it at admin.hanzo.ai. Beta is the congestion coefficient applied to a
// model's live load share once enabled; the default is a gentle spread.
const (
	MeanFieldEnabledDefault = false
	MeanFieldBetaDefault    = 0.15
)

// MeanFieldConfig is the resolved, platform-global congestion-routing config — the
// "*" GlobalDefaultOwner OrgSettings row with every unset field filled by its
// built-in default (DISABLED).
type MeanFieldConfig struct {
	Enabled bool
	Beta    float64
}

// GetCachedMeanFieldConfig resolves the live mean-field routing config from the
// 60s-cached "*" GlobalDefaultOwner row, applying the built-in default (OFF) for
// every unset field. Fail-safe: on any missing row or read error it returns the
// default (disabled), so a lookup blip NEVER silently flips congestion routing on —
// live routing stays byte-identical to today unless an admin explicitly enables it.
func GetCachedMeanFieldConfig() MeanFieldConfig {
	out := MeanFieldConfig{Enabled: MeanFieldEnabledDefault, Beta: MeanFieldBetaDefault}
	s, err := GetCachedOrgSettings(GlobalDefaultOwner)
	if err != nil || s == nil {
		return out
	}
	switch s.RouterMeanFieldEnabled {
	case MeanFieldConfigEnabled:
		out.Enabled = true
	case MeanFieldConfigDisabled:
		out.Enabled = false
	}
	if s.RouterMeanFieldBeta > 0 {
		out.Beta = s.RouterMeanFieldBeta
	}
	return out
}

func GetOrgSettingsList(owner string) ([]*OrgSettings, error) {
	if adapter == nil || adapter.db == nil {
		return nil, nil
	}
	settings := []*OrgSettings{}
	err := findAll(adapter.db, "org_settings", &settings, dbx.HashExp{"owner": owner}, "created_time DESC")
	if err != nil {
		return settings, err
	}
	return settings, nil
}

func GetOrgSettings(owner string) (*OrgSettings, error) {
	if adapter == nil || adapter.db == nil {
		return nil, nil
	}
	s := OrgSettings{Owner: owner}
	existed, err := getOne(adapter.db, "org_settings", &s, dbx.HashExp{"owner": owner})
	if err != nil {
		return &s, err
	}
	if existed {
		return &s, nil
	}
	return nil, nil
}

func AddOrgSettings(s *OrgSettings) (bool, error) {
	s.CreatedTime = time.Now().Format(time.RFC3339)
	s.UpdatedTime = s.CreatedTime
	err := insertRow(adapter.db, s)
	if err != nil {
		return false, err
	}
	invalidateOrgSettingsCache()
	return true, nil
}

func UpdateOrgSettings(owner string, s *OrgSettings) (bool, error) {
	s.UpdatedTime = time.Now().Format(time.RFC3339)
	s.Owner = owner
	err := adapter.db.Model(s).Update()
	if err != nil {
		return false, err
	}
	invalidateOrgSettingsCache()
	return true, nil
}

func DeleteOrgSettings(s *OrgSettings) (bool, error) {
	affected, err := deleteByPK(adapter.db, "org_settings", dbx.HashExp{"owner": s.Owner})
	if err != nil {
		return false, err
	}
	invalidateOrgSettingsCache()
	return affected != 0, nil
}

// ── Cached resolution for hot path ──────────────────────────────────────
// Mirrors the ModelRoute cache: org settings are read on every chat request
// that carries a virtual `auto` model, so a short-TTL cache keeps that path off
// the DB while still picking up admin changes within one TTL window.
type orgSettingsCacheEntry struct {
	settings  *OrgSettings // nil = no row for this owner
	fetchedAt time.Time
}

var (
	orgSettingsCache    = make(map[string]*orgSettingsCacheEntry)
	orgSettingsCacheMu  sync.RWMutex
	orgSettingsCacheTTL = 60 * time.Second
)

func invalidateOrgSettingsCache() {
	orgSettingsCacheMu.Lock()
	orgSettingsCache = make(map[string]*orgSettingsCacheEntry)
	orgSettingsCacheMu.Unlock()
}

// GetCachedOrgSettings returns an org's settings row (nil if none) with 60s TTL
// caching.
func GetCachedOrgSettings(owner string) (*OrgSettings, error) {
	orgSettingsCacheMu.RLock()
	entry, ok := orgSettingsCache[owner]
	orgSettingsCacheMu.RUnlock()
	if ok && time.Since(entry.fetchedAt) < orgSettingsCacheTTL {
		return entry.settings, nil
	}
	s, err := GetOrgSettings(owner)
	if err != nil {
		return nil, err
	}
	orgSettingsCacheMu.Lock()
	orgSettingsCache[owner] = &orgSettingsCacheEntry{settings: s, fetchedAt: time.Now()}
	orgSettingsCacheMu.Unlock()
	return s, nil
}

// GetCachedOrgAutoRouting returns the org's auto-routing preference ("",
// "enabled", "disabled"). Falls back to unset ("") on any missing row or error,
// so a lookup failure never changes routing behavior from the global default.
func GetCachedOrgAutoRouting(owner string) string {
	if owner == "" {
		return AutoRoutingUnset
	}
	s, err := GetCachedOrgSettings(owner)
	if err != nil || s == nil {
		return AutoRoutingUnset
	}
	return s.AutoRouting
}

// GetCachedOrgSessionRouting returns the org's default-session-routing preference
// ("", "enabled", "disabled") from the 60s-cached settings row. Falls back to
// unset ("") on any missing row or error, so a lookup failure is fail-safe (the
// caller then folds in the "*" row and the conf default).
func GetCachedOrgSessionRouting(owner string) string {
	if owner == "" {
		return AutoRoutingUnset
	}
	s, err := GetCachedOrgSettings(owner)
	if err != nil || s == nil {
		return AutoRoutingUnset
	}
	return s.DefaultSessionRouting
}

// GetCachedOrgTrainingContribution returns the org's training-contribution opt-in
// ("", "enabled", "disabled") from the 60s-cached settings row. Falls back to
// unset ("") — the privacy-safe opted-OUT default — on any missing row or error.
func GetCachedOrgTrainingContribution(owner string) string {
	if owner == "" {
		return TrainingContributionUnset
	}
	s, err := GetCachedOrgSettings(owner)
	if err != nil || s == nil {
		return TrainingContributionUnset
	}
	return s.TrainingContribution
}

// ListTrainingContributorOrgs returns the owners of every org that has explicitly
// opted IN to the cross-org base refresh (TrainingContribution == "enabled"). The
// nightly base-refresh job uses this to filter the shared fit to consenting orgs;
// an org that never set the flag (unset) is excluded. The "*" global-default row
// is never a contributor.
func ListTrainingContributorOrgs() ([]string, error) {
	if adapter == nil || adapter.db == nil {
		return nil, nil
	}
	rows := []*OrgSettings{}
	err := findAll(adapter.db, "org_settings", &rows, dbx.HashExp{"training_contribution": TrainingContributionEnabled}, "owner ASC")
	if err != nil {
		return nil, err
	}
	owners := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.Owner != GlobalDefaultOwner {
			owners = append(owners, r.Owner)
		}
	}
	return owners, nil
}
