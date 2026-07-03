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
	"sort"

	"github.com/hanzoai/ai/object"
)

// providerFlags is the secret-free response of GET /v1/provider-flags. It carries
// ONLY the names of enabled Model providers — no keys, no URLs, no config — so it
// is safe to be served unauthenticated. The pricing catalog sync
// (hanzoai/pricing/src/sync.mjs) reads this to decide which providers' models to
// publish, replacing the standalone ENABLE_OPENROUTER env: the provider State
// toggle and the catalog become ONE control.
//
// Contract for the pricing agent:
//
//	GET https://api.hanzo.ai/v1/provider-flags   (also on the cloud ingress)
//	200 {"status":"ok","data":{"enabledProviders":["do-ai","zen"]}}
//
// enabledProviders is a sorted, de-duplicated list of admin-owned Model provider
// names whose State == "Active". To gate OpenRouter, sync.mjs checks whether
// "openrouter" is in this list instead of reading its own env flag.
type providerFlags struct {
	EnabledProviders []string `json:"enabledProviders"`
}

// enabledModelProviderNames returns the sorted names of admin-owned Model
// providers that are currently enabled (State == "Active"). Secret-free by
// construction — it projects only the Name field.
func enabledModelProviderNames() ([]string, error) {
	providers, err := object.GetProviders("admin")
	if err != nil {
		return nil, err
	}
	return filterEnabledModelNames(providers), nil
}

// filterEnabledModelNames is the pure projection: sorted, secret-free names of
// enabled (State=="Active") Model providers. DB-free so it is unit-tested
// directly.
func filterEnabledModelNames(providers []*object.Provider) []string {
	names := make([]string, 0, len(providers))
	for _, p := range providers {
		if p.Category == "Model" && p.State == "Active" {
			names = append(names, p.Name)
		}
	}
	sort.Strings(names)
	return names
}

// GetProviderFlags
// @Title GetProviderFlags
// @Tag Provider API
// @Description Public, secret-free list of enabled Model provider NAMES. Used by
//
//	the pricing catalog sync to gate which providers' models are published
//	(replaces the standalone ENABLE_OPENROUTER env — one control). Safe to be
//	unauthenticated: no keys, URLs, or config are returned.
//
// @Success 200 {object} controllers.providerFlags The Response object
// @router /provider-flags [get]
func (c *ApiController) GetProviderFlags() {
	names, err := enabledModelProviderNames()
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(providerFlags{EnabledProviders: names})
}
