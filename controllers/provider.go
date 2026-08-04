// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2023 The OpenAgent Authors. All Rights Reserved.
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
	"regexp"
	"strings"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

// byokSecretNameSanitizer keeps a KMS secret name to a safe, collision-free
// charset so an org's custom-provider key is namespaced as byok_<org>_<provider>.
var byokSecretNameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9]+`)

func byokSecretName(owner, providerName string) string {
	clean := func(s string) string {
		return strings.Trim(byokSecretNameSanitizer.ReplaceAllString(s, "_"), "_")
	}
	return "byok_" + clean(owner) + "_" + clean(providerName)
}

// GetGlobalProviders
// @Title GetGlobalProviders
// @Tag Provider API
// @Description get global providers
// @Success 200 {array} object.Provider The Response object
// @router /get-global-providers [get]
func (c *ApiController) GetGlobalProviders() {
	user := c.GetSessionUser()
	providers, err := object.GetGlobalProviders()
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(object.GetMaskedProviders(providers, true, user))
}

// GetProviders
// @Title GetProviders
// @Tag Provider API
// @Description get providers
// @Success 200 {array} object.Provider The Response object
// @router /get-providers [get]
func (c *ApiController) GetProviders() {
	owner, ok := c.RequireSessionOwner()
	if !ok {
		return
	}
	limit := c.Input().Get("pageSize")
	page := c.Input().Get("p")
	field := c.Input().Get("field")
	value := c.Input().Get("value")
	sortField := c.Input().Get("sortField")
	sortOrder := c.Input().Get("sortOrder")
	user := c.GetSessionUser()
	storeName := c.Input().Get("store")

	// Apply store isolation based on user's Homepage field
	storeName, ok = c.EnforceStoreIsolation(storeName)
	if !ok {
		return
	}

	if limit == "" || page == "" {
		providers, err := object.GetProviders(owner)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		providers = object.GetMaskedProviders(providers, true, user)
		c.ResponseOk(providers)
	} else {
		if !c.RequireAdmin() {
			return
		}
		limit := util.ParseInt(limit)
		count, err := object.GetProviderCount(owner, storeName, field, value)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		paginator := util.NewPaginator(c.Ctx.Request, limit, count)
		providers, err := object.GetPaginationProviders(owner, storeName, paginator.Offset(), limit, field, value, sortField, sortOrder)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		providers = object.GetMaskedProviders(providers, true, user)
		c.ResponseOk(providers, paginator.Nums())
	}
}

// GetProvider
// @Title GetProvider
// @Tag Provider API
// @Description get provider
// @Param id query string true "The id of provider"
// @Success 200 {object} object.Provider The Response object
// @router /get-provider [get]
func (c *ApiController) GetProvider() {
	id := c.Input().Get("id")
	user := c.GetSessionUser()

	provider, err := object.GetProvider(id)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(object.GetMaskedProvider(provider, true, user))
}

// UpdateProvider
// @Title UpdateProvider
// @Tag Provider API
// @Description update provider
// @Param id query string true "The id (owner/name) of the provider"
// @Param body body object.Provider true "The details of the provider"
// @Success 200 {object} controllers.Response The Response object
// @router /update-provider [post]
func (c *ApiController) UpdateProvider() {
	if !c.RequireSuperAdmin() {
		return
	}
	id := c.Input().Get("id")

	var provider object.Provider
	err := json.Unmarshal(c.Ctx.Input.RequestBody, &provider)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	if err := sealPastedKey(id, &provider); err != nil {
		c.ResponseError(err.Error())
		return
	}

	success, err := object.UpdateProvider(id, &provider)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(success)
}

// sealPastedKey puts a key typed into the admin UI into KMS and leaves the row
// holding only the reference. Without it a provider key pasted at
// admin.hanzo.ai was written to the database as PLAINTEXT — and then erased.
//
// Erased because ClientSecret is in the boot self-heal set (object/init.go): the
// seed rewrites it from the canonical table on every restart. So a raw value
// survives only until the next pod roll, and a reference minted under some OTHER
// name is overwritten too, orphaning the secret it points at. That is why this
// seals under the name the EXISTING reference already declares
// (kms://OPENROUTER_API_KEY -> "OPENROUTER_API_KEY") and returns the reference
// unchanged: the row keeps saying exactly what the seed will say, so the write
// is durable across restarts and the self-heal is a no-op instead of a wipe.
//
// A row with no declared reference (a provider added by hand) falls back to the
// BYOK naming, which the seed does not manage and therefore does not clobber.
//
// Fails closed: with no KMS bound, StoreProviderSecret errors rather than let a
// raw key reach the database.
func sealPastedKey(id string, incoming *object.Provider) error {
	raw := incoming.ClientSecret
	if raw == "" || strings.HasPrefix(raw, "kms://") {
		return nil // nothing pasted, or already a reference
	}

	name := ""
	if existing, err := object.GetProvider(id); err == nil && existing != nil {
		if strings.HasPrefix(existing.ClientSecret, "kms://") {
			name = strings.TrimPrefix(existing.ClientSecret, "kms://")
		}
		if incoming.Owner == "" {
			incoming.Owner = existing.Owner
		}
		if incoming.Name == "" {
			incoming.Name = existing.Name
		}
	}
	if name == "" {
		name = byokSecretName(incoming.Owner, incoming.Name)
	}

	ref, err := object.StoreProviderSecret(name, raw)
	if err != nil {
		return err
	}
	incoming.ClientSecret = ref
	return nil
}

// AddProvider
// @Title AddProvider
// @Tag Provider API
// @Description add provider
// @Param body body object.Provider true "The details of the provider"
// @Success 200 {object} controllers.Response The Response object
// @router /add-provider [post]
func (c *ApiController) AddProvider() {
	var provider object.Provider
	err := json.Unmarshal(c.Ctx.Input.RequestBody, &provider)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	owner, ok := c.RequireSessionOwner()
	if !ok {
		return
	}
	provider.Owner = owner

	// BYOK: a tenant-supplied provider key must land in KMS, never the DB as
	// plaintext. Mint a kms:// ref for any org-owned RAW secret; fails closed when
	// KMS is unavailable (StoreProviderSecret errors rather than store plaintext).
	// An admin-owned global provider, or a value already a kms:// ref, is untouched.
	if owner != "admin" && provider.ClientSecret != "" && !strings.HasPrefix(provider.ClientSecret, "kms://") {
		ref, err := object.StoreProviderSecret(byokSecretName(owner, provider.Name), provider.ClientSecret)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}
		provider.ClientSecret = ref
	}

	success, err := object.AddProvider(&provider)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(success)
}

// DeleteProvider
// @Title DeleteProvider
// @Tag Provider API
// @Description delete provider
// @Param body body object.Provider true "The details of the provider"
// @Success 200 {object} controllers.Response The Response object
// @router /delete-provider [post]
func (c *ApiController) DeleteProvider() {
	if !c.RequireSuperAdmin() {
		return
	}
	var provider object.Provider
	err := json.Unmarshal(c.Ctx.Input.RequestBody, &provider)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	success, err := object.DeleteProvider(&provider)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(success)
}

// RefreshMcpTools
// @Title RefreshMcpTools
// @Tag Provider API
// @Description refresh Mcp tools
// @Param body body object.Provider true "The details of the provider"
// @Success 200 {object} controllers.Response The Response object
// @router /refresh-mcp-tools [post]
func (c *ApiController) RefreshMcpTools() {
	if !c.RequireSuperAdmin() {
		return
	}
	var provider object.Provider
	err := json.Unmarshal(c.Ctx.Input.RequestBody, &provider)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	err = object.RefreshMcpTools(&provider)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(&provider)
}
