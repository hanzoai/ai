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
