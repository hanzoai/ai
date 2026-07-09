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
//	Super-admin gated (see routers/authz_filter.go superAdminEndpoints).
//
// @Success 200 {array} controllers.adminProviderView The Response object
// @router /admin/providers [get]
func (c *ApiController) GetAdminProviders() {
	// Controller self-guard (defense in depth): the authz filter gates this route
	// on super admin, but we re-check here so a filter bypass can never expose the
	// provider list. Fail-closed 401/403.
	if !c.RequireSuperAdmin() {
		return
	}
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
	// Controller self-guard (defense in depth): disabling the primary provider
	// backs a platform-wide chat/embeddings/image DoS, so re-check super admin at
	// the controller even though the filter gates this route. Fail-closed 401/403.
	if !c.RequireSuperAdmin() {
		return
	}
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
	// Controller self-guard (defense in depth): repointing the primary changes
	// which upstream the whole catalog routes to, so re-check super admin here even
	// though the filter gates this route. Fail-closed 401/403.
	if !c.RequireSuperAdmin() {
		return
	}
	var req setPrimaryRequest
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, &req); err != nil {
		c.ResponseError(err.Error())
		return
	}

	// Validate the target first so an unknown name is a clean error and we never
	// clear the existing primary without a valid replacement.
	provider, err := getAdminModelProvider(req.Name)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	// A disabled provider must never become primary: GetDefaultModelProvider filters
	// on state="Active", so promoting a disabled one would make the default resolve
	// to nothing (breaking KB/RAG/store default-model answers). Reuse the ONE
	// "enabled/allowed-to-route" predicate (getAdminModelProvider already confirmed
	// Category=="Model"). Reject cleanly.
	if !object.ModelProviderUsable(provider) {
		c.ResponseError(c.T("provider:the provider must be enabled before it can be made primary"))
		return
	}

	// Atomic repoint (exactly-one-primary, no 0/2-primaries window): set the new
	// primary FIRST, then clear the others, so a mid-operation failure can never
	// leave zero primaries. Both are single set-wide UPDATE statements — never a
	// per-row loop. Also evicts the resolution cache so the change is immediate.
	if err := object.SetPrimaryModelProvider(req.Name); err != nil {
		c.ResponseError(err.Error())
		return
	}

	views, err := listAdminModelProviders()
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(views)
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
