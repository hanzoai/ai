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

	c.ResponseOk(object.GetMaskedProviders(providers, user))
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

		providers = object.GetMaskedProviders(providers, user)
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

		paginator := util.NewPaginator(c.PageAsked(), limit, count)
		providers, err := object.GetPaginationProviders(owner, storeName, paginator.Offset(), limit, field, value, sortField, sortOrder)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		providers = object.GetMaskedProviders(providers, user)
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

	c.ResponseOk(object.GetMaskedProvider(provider, user))
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
	err := json.Unmarshal(c.Body(), &provider)
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

// sealPastedKey puts a raw provider key into KMS and leaves the row holding only
// the reference. It is the ONE seal, used by add and update on both transports —
// the controllers and their ZAP twins — because four call sites each
// deciding when a secret may touch the database is how three of them ended up
// deciding differently.
//
// `id` is the existing row's "owner/name", or "" when there is no row yet (add).
//
// It applies to the PLATFORM's own providers as much as a tenant's. The rule used
// to exempt owner=="admin", which put the highest-value keys we hold — the global
// upstream accounts every customer's traffic runs through — in the one place the
// rule was written to keep them out of.
//
// Without this a key pasted at admin.hanzo.ai was written to the database as
// PLAINTEXT — and then erased. Erased because ClientSecret is in the boot
// self-heal set (object/init.go): the seed rewrites it from the canonical table on
// every restart. So a raw value survives only until the next pod roll, and a
// reference minted under some OTHER name is overwritten too, orphaning the secret
// it points at. That is why this seals under the name the EXISTING reference
// already declares (kms://OPENROUTER_API_KEY -> "OPENROUTER_API_KEY") and returns
// the reference unchanged: the row keeps saying exactly what the seed will say, so
// the write is durable across restarts and the self-heal is a no-op, not a wipe.
//
// A row with no declared reference falls back to BYOK naming, which the seed does
// not manage and therefore does not clobber.
//
// Fails closed: with no KMS bound, StoreProviderSecret errors rather than let a
// raw key reach the database.
func sealPastedKey(id string, incoming *object.Provider) error {
	raw := incoming.ClientSecret
	if raw == "" || raw == object.SecretMask || strings.HasPrefix(raw, "kms://") {
		// Nothing pasted, already a reference, or the MASK. The admin API returns
		// ClientSecret as "***" (GetMaskedProvider), so an operator who opens a
		// provider and saves it without touching the key field posts the mask back.
		// object.UpdateProvider restores it from the row — but that runs AFTER this,
		// and only when the value is still the mask, so sealing here first would
		// write the literal "***" into KMS as the key and destroy the real one.
		// Every re-save of an untouched form would break the provider.
		return nil
	}

	name := ""
	if id != "" {
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
	err := json.Unmarshal(c.Body(), &provider)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	owner, ok := c.RequireSessionOwner()
	if !ok {
		return
	}
	provider.Owner = owner

	// No row yet, so no existing reference to seal under: "" id.
	if err := sealPastedKey("", &provider); err != nil {
		c.ResponseError(err.Error())
		return
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
	err := json.Unmarshal(c.Body(), &provider)
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
	err := json.Unmarshal(c.Body(), &provider)
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
