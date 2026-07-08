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

	"github.com/hanzoai/ai/object"
)

// GetOrgSettingsList
// @Title GetOrgSettingsList
// @Tag OrgSettings API
// @Description get per-org feature settings for an owner
// @Param owner query string true "The owner (org) of the settings"
// @Success 200 {array} object.OrgSettings The Response object
// @router /get-org-settings-list [get]
func (c *ApiController) GetOrgSettingsList() {
	if !c.RequireGlobalAdmin() {
		return
	}
	owner := c.Input().Get("owner")
	if owner == "" {
		owner = "admin"
	}

	settings, err := object.GetOrgSettingsList(owner)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(settings)
}

// GetOrgSettings
// @Title GetOrgSettings
// @Tag OrgSettings API
// @Description get the settings row for a specific org
// @Param owner query string true "The owner (org)"
// @Success 200 {object} object.OrgSettings The Response object
// @router /get-org-settings [get]
func (c *ApiController) GetOrgSettings() {
	if !c.RequireGlobalAdmin() {
		return
	}
	owner := c.Input().Get("owner")
	if owner == "" {
		c.ResponseError("owner is required")
		return
	}

	settings, err := object.GetOrgSettings(owner)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(settings)
}

// AddOrgSettings
// @Title AddOrgSettings
// @Tag OrgSettings API
// @Description add a per-org settings row
// @Param body body object.OrgSettings true "The org settings"
// @Success 200 {object} controllers.Response The Response object
// @router /add-org-settings [post]
func (c *ApiController) AddOrgSettings() {
	if !c.RequireGlobalAdmin() {
		return
	}
	var settings object.OrgSettings
	err := json.Unmarshal(c.Ctx.Input.RequestBody, &settings)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	if settings.Owner == "" {
		c.ResponseError("owner is required")
		return
	}

	success, err := object.AddOrgSettings(&settings)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(success)
}

// UpdateOrgSettings
// @Title UpdateOrgSettings
// @Tag OrgSettings API
// @Description update (upsert) a per-org settings row
// @Param owner query string true "The owner (org)"
// @Param body body object.OrgSettings true "The org settings"
// @Success 200 {object} controllers.Response The Response object
// @router /update-org-settings [post]
func (c *ApiController) UpdateOrgSettings() {
	if !c.RequireGlobalAdmin() {
		return
	}
	owner := c.Input().Get("owner")
	if owner == "" {
		c.ResponseError("owner is required")
		return
	}

	var settings object.OrgSettings
	err := json.Unmarshal(c.Ctx.Input.RequestBody, &settings)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	// Upsert: the settings row keys only on owner, so if none exists yet, create
	// it — an admin PUT for an org that has never been configured must still take
	// effect (one settings row per org, created on first write).
	existing, err := object.GetOrgSettings(owner)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	var success bool
	if existing == nil {
		settings.Owner = owner
		success, err = object.AddOrgSettings(&settings)
	} else {
		success, err = object.UpdateOrgSettings(owner, &settings)
	}
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(success)
}

// DeleteOrgSettings
// @Title DeleteOrgSettings
// @Tag OrgSettings API
// @Description delete a per-org settings row (reverts to global defaults)
// @Param body body object.OrgSettings true "The org settings"
// @Success 200 {object} controllers.Response The Response object
// @router /delete-org-settings [post]
func (c *ApiController) DeleteOrgSettings() {
	if !c.RequireGlobalAdmin() {
		return
	}
	var settings object.OrgSettings
	err := json.Unmarshal(c.Ctx.Input.RequestBody, &settings)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	success, err := object.DeleteOrgSettings(&settings)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(success)
}
