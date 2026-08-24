// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2025 The OpenAgent Authors. All Rights Reserved.
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

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

// GetAssets
// @Title GetAssets
// @Tag Asset API
// @Description get all assets
// @Param   pageSize     query    string  false        "The size of each page"
// @Param   p     query    string  false        "The number of the page"
// @Success 200 {object} object.Asset The Response object
// @router /get-assets [get]
func (c *ApiController) GetAssets() {
	owner, allowed := c.GetScopedOwner()
	if !allowed {
		return
	}
	limit := c.Input().Get("pageSize")
	page := c.Input().Get("p")
	field := c.Input().Get("field")
	value := c.Input().Get("value")
	sortField := c.Input().Get("sortField")
	sortOrder := c.Input().Get("sortOrder")

	if limit == "" || page == "" {
		assets, err := object.GetAssets(owner)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		assets = object.GetMaskedAssets(assets, true)
		c.ResponseOk(assets)
	} else {
		limit := util.ParseInt(limit)
		count, err := object.GetAssetCount(owner, field, value)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		paginator := util.NewPaginator(c.PageAsked(), limit, count)
		assets, err := object.GetPaginationAssets(owner, paginator.Offset(), limit, field, value, sortField, sortOrder)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		assets = object.GetMaskedAssets(assets, true)
		c.ResponseOk(assets, paginator.Nums())
	}
}

// GetAsset
// @Title GetAsset
// @Tag Asset API
// @Description get asset
// @Param   id     query    string  true        "The id ( owner/name ) of the asset"
// @Success 200 {object} object.Asset The Response object
// @router /get-asset [get]
func (c *ApiController) GetAsset() {
	id := c.Input().Get("id")

	asset, err := object.GetAsset(id)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	if asset != nil && !reachable(c, asset.Owner) {
		return
	}

	c.ResponseOk(object.GetMaskedAsset(asset, true))
}

// UpdateAsset
// @Title UpdateAsset
// @Tag Asset API
// @Description update asset
// @Param   id     query    string  true        "The id ( owner/name ) of the asset"
// @Param   body    body   object.Asset  true        "The details of the asset"
// @Success 200 {object} controllers.Response The Response object
// @router /update-asset [post]
func (c *ApiController) UpdateAsset() {
	caller, ok := c.RequireSignedInUser()
	if !ok {
		return
	}
	id := c.Input().Get("id")
	owner, _, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	if !reaches(caller, owner) {
		c.ResponseError(c.T("general:The asset does not exist"))
		return
	}

	var asset object.Asset
	if err = json.Unmarshal(c.Body(), &asset); err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(object.UpdateAsset(id, &asset))
}

// AddAsset
// @Title AddAsset
// @Tag Asset API
// @Description add an asset
// @Param   body    body   object.Asset  true        "The details of the asset"
// @Success 200 {object} controllers.Response The Response object
// @router /add-asset [post]
func (c *ApiController) AddAsset() {
	owner, allowed := c.GetScopedOwner()
	if !allowed {
		return
	}
	var asset object.Asset
	err := json.Unmarshal(c.Body(), &asset)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	// Filed into the caller's own organization, the one GetAssets reads back —
	// which is what the ZAP twin says too.
	asset.Owner = owner

	c.ResponseOk(object.AddAsset(&asset))
}

// DeleteAsset
// @Title DeleteAsset
// @Tag Asset API
// @Description delete an asset
// @Param   body    body   object.Asset  true        "The details of the asset"
// @Success 200 {object} controllers.Response The Response object
// @router /delete-asset [post]
func (c *ApiController) DeleteAsset() {
	caller, ok := c.RequireSignedInUser()
	if !ok {
		return
	}
	var asset object.Asset
	err := json.Unmarshal(c.Body(), &asset)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	if !reaches(caller, asset.Owner) {
		c.ResponseError(c.T("general:The asset does not exist"))
		return
	}

	c.ResponseOk(object.DeleteAsset(&asset))
}

// ScanAsset
// @Title ScanAsset
// @Tag Asset API
// @Description unified API for scanning assets (combines test-scan and start-scan functionality)
// @Param provider query string true "The provider ID (owner/name)"
// @Param scan query string false "The scan ID (owner/name) for saving results"
// @Param targetMode query string true "Target mode: 'Manual Input' or 'Asset'"
// @Param target query string false "Manual input target (IP address or network range)"
// @Param asset query string false "Asset ID (owner/name) for Asset mode"
// @Param command query string false "Scan command with optional %s placeholder for target"
// @Param saveToScan query string false "Whether to save results to scan object (true/false)"
// @Success 200 {object} controllers.Response The Response object
// @router /scan-asset [post]
func (c *ApiController) ScanAsset() {
	provider := c.Input().Get("provider")
	scan := c.Input().Get("scan")
	targetMode := c.Input().Get("targetMode")
	target := c.Input().Get("target")
	asset := c.Input().Get("asset")
	command := c.Input().Get("command")
	saveToScan := c.Input().Get("saveToScan") == "true"

	scanResult, err := object.ScanAsset(provider, scan, targetMode, target, asset, command, saveToScan, c.GetAcceptLanguage())
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(scanResult)
}

// ScanAssets
// @Title ScanAssets
// @Tag Asset API
// @Description scan assets from a cloud provider
// @Param   owner     query    string  true        "The owner"
// @Param   provider     query    string  true        "The provider name"
// @Success 200 {object} controllers.Response The Response object
// @router /scan-assets [post]
func (c *ApiController) ScanAssets() {
	owner, allowed := c.GetScopedOwner()
	if !allowed {
		return
	}
	provider := c.Input().Get("provider")

	success, err := object.ScanAssetsFromProvider(owner, provider)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(success)
}
