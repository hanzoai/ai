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
	"fmt"
	"sync"
	"time"

	"xorm.io/core"
)

type ModelRoute struct {
	Owner       string `xorm:"varchar(100) notnull pk" json:"owner"`     // org ID ("built-in" = global default)
	ModelName   string `xorm:"varchar(200) notnull pk" json:"modelName"` // e.g. "claude-sonnet-4-6"
	CreatedTime string `xorm:"varchar(100)" json:"createdTime"`
	UpdatedTime string `xorm:"varchar(100)" json:"updatedTime"`

	Provider    string `xorm:"varchar(200) notnull" json:"provider"`           // primary provider name
	Upstream    string `xorm:"varchar(200)" json:"upstream"`                   // upstream model name
	Fallback1   string `xorm:"varchar(200)" json:"fallback1Provider"`          // fallback provider 1
	Fallback1Up string `xorm:"varchar(200)" json:"fallback1Upstream"`          // fallback upstream 1
	Fallback2   string `xorm:"varchar(200)" json:"fallback2Provider"`          // fallback provider 2
	Fallback2Up string `xorm:"varchar(200)" json:"fallback2Upstream"`          // fallback upstream 2
	OwnedBy     string `xorm:"varchar(200)" json:"ownedBy"`                   // owned_by override for /api/models listing
	Premium     bool   `json:"premium"`                                        // requires paid balance
	Hidden      bool   `json:"hidden"`                                         // excluded from /api/models listing
	InputPrice  float64 `xorm:"DECIMAL(10, 4)" json:"inputPricePerMillion"`   // custom pricing (0 = use default)
	OutputPrice float64 `xorm:"DECIMAL(10, 4)" json:"outputPricePerMillion"`
	Enabled     bool   `json:"enabled"`
}

func (r *ModelRoute) GetId() string {
	return fmt.Sprintf("%s/%s", r.Owner, r.ModelName)
}

func GetModelRoutes(owner string) ([]*ModelRoute, error) {
	if adapter == nil || adapter.engine == nil {
		return nil, nil
	}
	routes := []*ModelRoute{}
	err := adapter.engine.Desc("created_time").Find(&routes, &ModelRoute{Owner: owner})
	if err != nil {
		return routes, err
	}
	return routes, nil
}

func GetModelRoute(owner string, modelName string) (*ModelRoute, error) {
	if adapter == nil || adapter.engine == nil {
		return nil, nil
	}
	route := ModelRoute{Owner: owner, ModelName: modelName}
	existed, err := adapter.engine.Get(&route)
	if err != nil {
		return &route, err
	}
	if existed {
		return &route, nil
	}
	return nil, nil
}

func GetModelRouteCount(owner, field, value string) (int64, error) {
	session := GetDbSession(owner, -1, -1, field, value, "", "")
	return session.Count(&ModelRoute{})
}

func GetPaginationModelRoutes(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*ModelRoute, error) {
	routes := []*ModelRoute{}
	session := GetDbSession(owner, offset, limit, field, value, sortField, sortOrder)
	err := session.Find(&routes)
	if err != nil {
		return routes, err
	}
	return routes, nil
}

func AddModelRoute(route *ModelRoute) (bool, error) {
	route.CreatedTime = time.Now().Format(time.RFC3339)
	route.UpdatedTime = route.CreatedTime
	affected, err := adapter.engine.Insert(route)
	if err != nil {
		return false, err
	}
	// Invalidate cache on write
	invalidateModelRouteCache()
	return affected != 0, nil
}

func UpdateModelRoute(owner string, modelName string, route *ModelRoute) (bool, error) {
	route.UpdatedTime = time.Now().Format(time.RFC3339)
	_, err := adapter.engine.ID(core.PK{owner, modelName}).AllCols().Update(route)
	if err != nil {
		return false, err
	}
	// Invalidate cache on write
	invalidateModelRouteCache()
	return true, nil
}

func DeleteModelRoute(route *ModelRoute) (bool, error) {
	affected, err := adapter.engine.ID(core.PK{route.Owner, route.ModelName}).Delete(&ModelRoute{})
	if err != nil {
		return false, err
	}
	// Invalidate cache on write
	invalidateModelRouteCache()
	return affected != 0, nil
}

// ── Cached resolution for hot path ──────────────────────────────────────

type modelRouteCacheEntry struct {
	routes    []*ModelRoute
	fetchedAt time.Time
}

var (
	modelRouteCache    = make(map[string]*modelRouteCacheEntry)
	modelRouteCacheMu  sync.RWMutex
	modelRouteCacheTTL = 60 * time.Second
)

func invalidateModelRouteCache() {
	modelRouteCacheMu.Lock()
	modelRouteCache = make(map[string]*modelRouteCacheEntry)
	modelRouteCacheMu.Unlock()
}

// GetCachedModelRoutes returns all model routes for an owner with 60s TTL caching.
func GetCachedModelRoutes(owner string) ([]*ModelRoute, error) {
	modelRouteCacheMu.RLock()
	entry, ok := modelRouteCache[owner]
	modelRouteCacheMu.RUnlock()

	if ok && time.Since(entry.fetchedAt) < modelRouteCacheTTL {
		return entry.routes, nil
	}

	routes, err := GetModelRoutes(owner)
	if err != nil {
		return nil, err
	}

	modelRouteCacheMu.Lock()
	modelRouteCache[owner] = &modelRouteCacheEntry{routes: routes, fetchedAt: time.Now()}
	modelRouteCacheMu.Unlock()

	return routes, nil
}

// ResolveModelRouteFromDB looks up a model route from the database.
// Resolution order: org-specific route -> global ("built-in") route.
// Returns nil if no DB route found (caller should fall back to YAML).
func ResolveModelRouteFromDB(modelName string, orgId string) (*ModelRoute, error) {
	// Try org-specific first
	if orgId != "" && orgId != "built-in" {
		routes, err := GetCachedModelRoutes(orgId)
		if err != nil {
			return nil, err
		}
		for _, r := range routes {
			if r.ModelName == modelName && r.Enabled {
				return r, nil
			}
		}
	}

	// Try global ("built-in") defaults
	routes, err := GetCachedModelRoutes("built-in")
	if err != nil {
		return nil, err
	}
	for _, r := range routes {
		if r.ModelName == modelName && r.Enabled {
			return r, nil
		}
	}

	return nil, nil
}
