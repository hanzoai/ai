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
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

// GetPrompts
// @Title GetPrompts
// @Tag Prompt API
// @Description get prompts for the authenticated org
// @Param owner query string false "The owner (org) of the prompts; admins may target another org"
// @Success 200 {array} object.Prompt The Response object
// @router /get-prompts [get]
func (c *ApiController) GetPrompts() {
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
		prompts, err := object.GetPrompts(owner)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}
		c.ResponseOk(prompts)
	} else {
		limit := util.ParseInt(limit)
		count, err := object.GetPromptCount(owner, field, value)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		paginator := pagination.SetPaginator(c.Ctx, limit, count)
		prompts, err := object.GetPaginationPrompts(owner, paginator.Offset(), limit, field, value, sortField, sortOrder)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		c.ResponseOk(prompts, paginator.Nums())
	}
}

// GetPrompt
// @Title GetPrompt
// @Tag Prompt API
// @Description get prompt
// @Param id query string true "The id (owner/name) of the prompt"
// @Success 200 {object} object.Prompt The Response object
// @router /get-prompt [get]
func (c *ApiController) GetPrompt() {
	owner, allowed := c.GetScopedOwner()
	if !allowed {
		return
	}
	id := c.Input().Get("id")

	prompt, err := object.GetPrompt(id)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	// Tenant isolation: a prompt that exists but belongs to another org is not
	// disclosed — same posture as the rest of the org-scoped surface.
	if prompt != nil && prompt.Owner != owner {
		c.ResponseError(c.T("auth:Unauthorized operation"))
		return
	}

	c.ResponseOk(prompt)
}

// UpdatePrompt
// @Title UpdatePrompt
// @Tag Prompt API
// @Description update prompt
// @Param id   query string        true "The id (owner/name) of the prompt"
// @Param body body  object.Prompt true "The details of the prompt"
// @Success 200 {object} controllers.Response The Response object
// @router /update-prompt [post]
func (c *ApiController) UpdatePrompt() {
	owner, allowed := c.GetScopedOwner()
	if !allowed {
		return
	}
	id := c.Input().Get("id")

	var prompt object.Prompt
	err := json.Unmarshal(c.Ctx.Input.RequestBody, &prompt)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	existing, err := object.GetPrompt(id)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	if existing == nil || existing.Owner != owner {
		c.ResponseError(c.T("auth:Unauthorized operation"))
		return
	}

	success, err := object.UpdatePrompt(id, &prompt)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(success)
}

// AddPrompt
// @Title AddPrompt
// @Tag Prompt API
// @Description add prompt
// @Param body body object.Prompt true "The details of the prompt"
// @Success 200 {object} controllers.Response The Response object
// @router /add-prompt [post]
func (c *ApiController) AddPrompt() {
	owner, allowed := c.GetScopedOwner()
	if !allowed {
		return
	}

	var prompt object.Prompt
	err := json.Unmarshal(c.Ctx.Input.RequestBody, &prompt)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	// Owner is bound to the authenticated org, never trusted from the body.
	prompt.Owner = owner

	success, err := object.AddPrompt(&prompt)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(success)
}

// DeletePrompt
// @Title DeletePrompt
// @Tag Prompt API
// @Description delete prompt
// @Param body body object.Prompt true "The details of the prompt"
// @Success 200 {object} controllers.Response The Response object
// @router /delete-prompt [post]
func (c *ApiController) DeletePrompt() {
	owner, allowed := c.GetScopedOwner()
	if !allowed {
		return
	}

	var prompt object.Prompt
	err := json.Unmarshal(c.Ctx.Input.RequestBody, &prompt)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	// Bind to the authenticated org so a tenant can only delete its own prompts.
	prompt.Owner = owner

	success, err := object.DeletePrompt(&prompt)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(success)
}
