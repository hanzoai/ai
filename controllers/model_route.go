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

	"github.com/beego/beego/utils/pagination"
	"github.com/hanzoai/cloud/object"
	"github.com/hanzoai/cloud/util"
)

// GetModelRoutes
// @Title GetModelRoutes
// @Tag ModelRoute API
// @Description get model routes for an owner
// @Param owner query string true "The owner (org) of the model routes"
// @Success 200 {array} object.ModelRoute The Response object
// @router /get-model-routes [get]
func (c *ApiController) GetModelRoutes() {
	owner := c.Input().Get("owner")
	if owner == "" {
		owner = "admin"
	}

	limit := c.Input().Get("pageSize")
	page := c.Input().Get("p")
	field := c.Input().Get("field")
	value := c.Input().Get("value")
	sortField := c.Input().Get("sortField")
	sortOrder := c.Input().Get("sortOrder")

	if limit == "" || page == "" {
		routes, err := object.GetModelRoutes(owner)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}
		c.ResponseOk(routes)
	} else {
		limit := util.ParseInt(limit)
		count, err := object.GetModelRouteCount(owner, field, value)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		paginator := pagination.SetPaginator(c.Ctx, limit, count)
		routes, err := object.GetPaginationModelRoutes(owner, paginator.Offset(), limit, field, value, sortField, sortOrder)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		c.ResponseOk(routes, paginator.Nums())
	}
}

// GetModelRoute
// @Title GetModelRoute
// @Tag ModelRoute API
// @Description get a specific model route
// @Param owner query string true "The owner (org)"
// @Param modelName query string true "The model name"
// @Success 200 {object} object.ModelRoute The Response object
// @router /get-model-route [get]
func (c *ApiController) GetModelRoute() {
	owner := c.Input().Get("owner")
	modelName := c.Input().Get("modelName")

	if owner == "" || modelName == "" {
		c.ResponseError("owner and modelName are required")
		return
	}

	route, err := object.GetModelRoute(owner, modelName)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(route)
}

// AddModelRoute
// @Title AddModelRoute
// @Tag ModelRoute API
// @Description add a model route
// @Param body body object.ModelRoute true "The details of the model route"
// @Success 200 {object} controllers.Response The Response object
// @router /add-model-route [post]
func (c *ApiController) AddModelRoute() {
	var route object.ModelRoute
	err := json.Unmarshal(c.Ctx.Input.RequestBody, &route)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	if route.Owner == "" {
		route.Owner = "admin"
	}

	success, err := object.AddModelRoute(&route)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(success)
}

// UpdateModelRoute
// @Title UpdateModelRoute
// @Tag ModelRoute API
// @Description update a model route
// @Param owner query string true "The owner (org)"
// @Param modelName query string true "The model name"
// @Param body body object.ModelRoute true "The details of the model route"
// @Success 200 {object} controllers.Response The Response object
// @router /update-model-route [post]
func (c *ApiController) UpdateModelRoute() {
	owner := c.Input().Get("owner")
	modelName := c.Input().Get("modelName")

	var route object.ModelRoute
	err := json.Unmarshal(c.Ctx.Input.RequestBody, &route)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	success, err := object.UpdateModelRoute(owner, modelName, &route)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(success)
}

// DeleteModelRoute
// @Title DeleteModelRoute
// @Tag ModelRoute API
// @Description delete a model route
// @Param body body object.ModelRoute true "The details of the model route"
// @Success 200 {object} controllers.Response The Response object
// @router /delete-model-route [post]
func (c *ApiController) DeleteModelRoute() {
	var route object.ModelRoute
	err := json.Unmarshal(c.Ctx.Input.RequestBody, &route)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	success, err := object.DeleteModelRoute(&route)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(success)
}
