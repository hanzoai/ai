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
	if !c.RequireSuperAdmin() {
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
	if !c.RequireSuperAdmin() {
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
	if !c.RequireSuperAdmin() {
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
	if !c.RequireSuperAdmin() {
		return
	}
	owner := c.Input().Get("owner")
	if owner == "" {
		c.ResponseError("owner is required")
		return
	}

	// Upsert: the settings row keys only on owner, so if none exists yet, create it — an
	// admin write for an org that has never been configured must still take effect.
	existing, err := object.GetOrgSettings(owner)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	// PATCH-MERGE, not replace: unmarshal the body ONTO the existing row (or a fresh one
	// keyed by owner), so a field ABSENT from the body keeps its current value. dbx
	// Model(s).Update() writes ALL columns, so replacing the row would NULL every field the
	// body didn't restate — a partial write (e.g. just autoRouting from the admin toggle)
	// must never wipe the org's RouterPrefer/RouterEnabledModels/RouterQualityBias/… To
	// CLEAR a field, send it explicitly (e.g. {"routerPrefer":{}}).
	target := existing
	if target == nil {
		target = &object.OrgSettings{Owner: owner}
	}
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, target); err != nil {
		c.ResponseError(err.Error())
		return
	}
	target.Owner = owner

	var success bool
	if existing == nil {
		success, err = object.AddOrgSettings(target)
	} else {
		success, err = object.UpdateOrgSettings(owner, target)
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
	if !c.RequireSuperAdmin() {
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
