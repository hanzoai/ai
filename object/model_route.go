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
	"fmt"
	"sync"
	"time"

	"github.com/hanzoai/dbx"
)

type ModelRoute struct {
	Owner       string  `db:"pk" json:"owner"`     // org ID ("built-in" = global default)
	ModelName   string  `db:"pk" json:"modelName"` // e.g. "claude-sonnet-4-6"
	CreatedTime string  `json:"createdTime"`
	UpdatedTime string  `json:"updatedTime"`
	Provider    string  `json:"provider"`             // primary provider name
	Upstream    string  `json:"upstream"`             // upstream model name
	Fallback1   string  `json:"fallback1Provider"`    // fallback provider 1
	Fallback1Up string  `json:"fallback1Upstream"`    // fallback upstream 1
	Fallback2   string  `json:"fallback2Provider"`    // fallback provider 2
	Fallback2Up string  `json:"fallback2Upstream"`    // fallback upstream 2
	OwnedBy     string  `json:"ownedBy"`              // owned_by override for /api/models listing
	Premium     bool    `json:"premium"`              // requires paid balance
	Hidden      bool    `json:"hidden"`               // excluded from /api/models listing
	InputPrice  float64 `json:"inputPricePerMillion"` // custom pricing (0 = use default)
	OutputPrice float64 `json:"outputPricePerMillion"`
	Enabled     bool    `json:"enabled"`
}

func (r *ModelRoute) GetId() string {
	return fmt.Sprintf("%s/%s", r.Owner, r.ModelName)
}

func GetModelRoutes(owner string) ([]*ModelRoute, error) {
	if adapter == nil || adapter.db == nil {
		return nil, nil
	}
	routes := []*ModelRoute{}
	err := findAll(adapter.db, "model_route", &routes, dbx.HashExp{"owner": owner}, "created_time DESC")
	if err != nil {
		return routes, err
	}
	return routes, nil
}

func GetModelRoute(owner string, modelName string) (*ModelRoute, error) {
	if adapter == nil || adapter.db == nil {
		return nil, nil
	}
	route := ModelRoute{Owner: owner, ModelName: modelName}
	existed, err := getOne(adapter.db, "model_route", &route, dbx.HashExp{"owner": owner, "model_name": modelName})
	if err != nil {
		return &route, err
	}
	if existed {
		return &route, nil
	}
	return nil, nil
}

func GetModelRouteCount(owner, field, value string) (int64, error) {
	session := GetDbQuery(owner, -1, -1, field, value, "", "")
	return queryCount(session, "model_route")
}

func GetPaginationModelRoutes(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*ModelRoute, error) {
	routes := []*ModelRoute{}
	session := GetDbQuery(owner, offset, limit, field, value, sortField, sortOrder)
	err := queryFind(session, "model_route", &routes)
	if err != nil {
		return routes, err
	}
	return routes, nil
}

func AddModelRoute(route *ModelRoute) (bool, error) {
	route.CreatedTime = time.Now().Format(time.RFC3339)
	route.UpdatedTime = route.CreatedTime
	err := insertRow(adapter.db, route)
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	// Invalidate cache on write
	invalidateModelRouteCache()
	return affected != 0, nil
}

func UpdateModelRoute(owner string, modelName string, route *ModelRoute) (bool, error) {
	route.UpdatedTime = time.Now().Format(time.RFC3339)
	route.Owner = owner
	route.ModelName = modelName
	err := adapter.db.Model(route).Update()
	if err != nil {
		return false, err
	}
	// Invalidate cache on write
	invalidateModelRouteCache()
	return true, nil
}

func DeleteModelRoute(route *ModelRoute) (bool, error) {
	affected, err := deleteByPK(adapter.db, "model_route", pk2(route.Owner, route.ModelName))
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
