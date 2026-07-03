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

package controllers

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/hanzoai/ai/object"
)

// adminProviderView is the clean management projection of an object.Provider for
// the provider-admin dashboard (console frontend contract — field names are
// load-bearing, do not rename). It NEVER carries secret material: clientSecret,
// providerKey, userKey, signKey and every other provider field are intentionally
// absent from this struct. keyPresent (a boolean) is the ONLY signal about the
// key. Adding a field here must never re-introduce a secret.
type adminProviderView struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Type        string `json:"type"`
	Enabled     bool   `json:"enabled"`     // State == "Active"
	Primary     bool   `json:"primary"`     // IsDefault
	KeyPresent  bool   `json:"keyPresent"`  // ClientSecret resolves to non-empty (never the value)
	ModelCount  int    `json:"modelCount"`  // # model keys routing to this provider
	ProviderURL string `json:"providerUrl"` // upstream base URL (not a secret)
}

// toAdminProviderView projects one admin-owned Model provider into the management
// view, resolving keyPresent as a boolean only (never the key) and attaching the
// model count from the routing table. counts is precomputed once per request via
// modelCountByProvider() so a list projection is a single map read per provider.
func toAdminProviderView(p *object.Provider, counts map[string]int) adminProviderView {
	return adminProviderView{
		Name:        p.Name,
		DisplayName: p.DisplayName,
		Type:        p.Type,
		Enabled:     p.State == "Active",
		Primary:     p.IsDefault,
		KeyPresent:  object.ProviderKeyPresent(p),
		ModelCount:  counts[p.Name],
		ProviderURL: p.ProviderUrl,
	}
}

// listAdminModelProviders fetches the admin-owned Model-category providers and
// projects them into the management view, sorted by name for a stable UI. This
// reads the SAME object.Provider records as the generic CRUD store — one source
// of truth — and never forks it.
func listAdminModelProviders() ([]adminProviderView, error) {
	providers, err := object.GetProviders("admin")
	if err != nil {
		return nil, err
	}
	counts := modelCountByProvider()
	views := make([]adminProviderView, 0, len(providers))
	for _, p := range providers {
		if p.Category != "Model" {
			continue
		}
		views = append(views, toAdminProviderView(p, counts))
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
	return views, nil
}

// getAdminModelProvider fetches a single admin-owned provider by name and
// validates it is an addressable Model-category provider. Returns a clean error
// otherwise (unknown name, or a non-Model provider that this admin surface does
// not manage). It returns the RAW record (with the kms:// ref intact) so callers
// that persist a toggle never accidentally write a masked "***" or resolved key.
func getAdminModelProvider(name string) (*object.Provider, error) {
	if name == "" {
		return nil, fmt.Errorf("provider name is required")
	}
	provider, err := object.GetProvider("admin/" + name)
	if err != nil {
		return nil, err
	}
	if provider == nil {
		return nil, fmt.Errorf("provider %q not found", name)
	}
	if provider.Category != "Model" {
		return nil, fmt.Errorf("provider %q is not a model provider", name)
	}
	return provider, nil
}

// GetAdminProviders
// @Title GetAdminProviders
// @Tag Provider Admin API
// @Description List admin-owned Model providers as a clean management view
//
//	(enabled/primary/keyPresent/modelCount). Never returns secret material.
//	Global-admin gated (see routers/authz_filter.go globalAdminEndpoints).
//
// @Success 200 {array} controllers.adminProviderView The Response object
// @router /admin/providers [get]
func (c *ApiController) GetAdminProviders() {
	views, err := listAdminModelProviders()
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(views)
}

// toggleProviderRequest is the body of POST /v1/admin/providers/toggle.
type toggleProviderRequest struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// ToggleAdminProvider
// @Title ToggleAdminProvider
// @Tag Provider Admin API
// @Description Enable/disable an admin-owned Model provider (sets State to
//
//	"Active"/"Disabled") via the SAME store the generic CRUD uses. Persists
//	across restarts (init.go never clobbers State on an existing record).
//	Returns the updated management view for that provider.
//
// @Param body body controllers.toggleProviderRequest true "provider name + enabled flag"
// @Success 200 {object} controllers.adminProviderView The Response object
// @router /admin/providers/toggle [post]
func (c *ApiController) ToggleAdminProvider() {
	var req toggleProviderRequest
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, &req); err != nil {
		c.ResponseError(err.Error())
		return
	}

	provider, err := getAdminModelProvider(req.Name)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	provider.State = stateForEnabled(req.Enabled)
	if _, err := object.UpdateProvider(provider.GetId(), provider); err != nil {
		c.ResponseError(err.Error())
		return
	}
	// Evict the hot-path resolution cache so the enable/disable takes effect
	// immediately instead of after the 60s TTL — a disabled provider stops routing
	// (GetModelProviderByName → nil) on the very next request.
	object.InvalidateProviderNameCache(provider.Name)

	// Re-read so the returned view reflects exactly what was persisted (and picks
	// up keyPresent/modelCount consistently).
	updated, err := getAdminModelProvider(req.Name)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(toAdminProviderView(updated, modelCountByProvider()))
}

// setPrimaryRequest is the body of POST /v1/admin/providers/primary.
type setPrimaryRequest struct {
	Name string `json:"name"`
}

// SetPrimaryAdminProvider
// @Title SetPrimaryAdminProvider
// @Tag Provider Admin API
// @Description Make one admin-owned Model provider the primary (IsDefault=true)
//
//	and clear IsDefault on all other Model providers, so EXACTLY ONE primary
//	exists. Returns the full updated management list.
//
// @Param body body controllers.setPrimaryRequest true "provider name"
// @Success 200 {array} controllers.adminProviderView The Response object
// @router /admin/providers/primary [post]
func (c *ApiController) SetPrimaryAdminProvider() {
	var req setPrimaryRequest
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, &req); err != nil {
		c.ResponseError(err.Error())
		return
	}

	// Validate the target first so an unknown name is a clean error and we never
	// clear the existing primary without a valid replacement.
	if _, err := getAdminModelProvider(req.Name); err != nil {
		c.ResponseError(err.Error())
		return
	}

	providers, err := object.GetProviders("admin")
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	// Exactly-one-primary: compute which Model providers must change, then persist
	// only those (via the same store — one source of truth). Each provider here is
	// the RAW record (kms:// ref intact), safe to persist without masking.
	for _, p := range providersToRepointPrimary(providers, req.Name) {
		if _, err := object.UpdateProvider(p.GetId(), p); err != nil {
			c.ResponseError(err.Error())
			return
		}
		// Immediate effect: drop the changed provider from the resolution cache so
		// IsDefault (primary) reads are consistent on the next request.
		object.InvalidateProviderNameCache(p.Name)
	}

	views, err := listAdminModelProviders()
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(views)
}

// providersToRepointPrimary computes the exactly-one-primary transition: among
// the Model-category providers, the one named `primaryName` must have
// IsDefault=true and every other must have IsDefault=false. It returns ONLY the
// records whose IsDefault flag actually changes (with the flag already flipped),
// so the caller persists the minimal set. Pure and DB-free — the core invariant
// is unit-tested here without touching the store. Non-Model providers are left
// untouched.
func providersToRepointPrimary(providers []*object.Provider, primaryName string) []*object.Provider {
	changed := make([]*object.Provider, 0, len(providers))
	for _, p := range providers {
		if p.Category != "Model" {
			continue
		}
		want := p.Name == primaryName
		if p.IsDefault == want {
			continue
		}
		p.IsDefault = want
		changed = append(changed, p)
	}
	return changed
}

// stateForEnabled maps the boolean toggle to the canonical State string. "Active"
// is the single enabled sentinel the whole codebase checks (see
// object.ModelProviderUsable, GetDefaultKubernetesProvider, etc.); "Disabled" is
// the paused state.
func stateForEnabled(enabled bool) string {
	if enabled {
		return "Active"
	}
	return "Disabled"
}
