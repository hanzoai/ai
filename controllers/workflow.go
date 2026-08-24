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
)

// GetGlobalWorkflows
// @Title GetGlobalWorkflows
// @Tag Workflow API
// @Description get global workflows
// @Success 200 {array} object.Workflow The Response object
// @router /get-global-workflows [get]
func (c *ApiController) GetGlobalWorkflows() {
	workflows, err := object.GetGlobalWorkflows()
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(object.GetMaskedWorkflows(workflows, true))
}

// GetWorkflows
// @Title GetWorkflows
// @Tag Workflow API
// @Description get workflows
// @Param owner query string true "The owner of workflow"
// @Success 200 {array} object.Workflow The Response object
// @router /get-workflows [get]
func (c *ApiController) GetWorkflows() {
	listed(c, table[object.Workflow]{all: object.GetWorkflows, mask: object.GetMaskedWorkflows,
		count: object.GetWorkflowCount, page: object.GetPaginationWorkflows})
}

// GetWorkflow
// @Title GetWorkflow
// @Tag Workflow API
// @Description get workflow
// @Param id query string true "The id (owner/name) of workflow"
// @Success 200 {object} object.Workflow The Response object
// @router /get-workflow [get]
func (c *ApiController) GetWorkflow() {
	id := c.Input().Get("id")

	workflow, err := object.GetWorkflow(id)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(object.GetMaskedWorkflow(workflow, true))
}

// UpdateWorkflow
// @Title UpdateWorkflow
// @Tag Workflow API
// @Description update workflow
// @Param id query string true "The id (owner/name) of the workflow"
// @Param body body object.Workflow true "The details of the workflow"
// @Success 200 {object} controllers.Response The Response object
// @router /update-workflow [post]
func (c *ApiController) UpdateWorkflow() {
	owner, allowed := c.GetScopedOwner()
	if !allowed {
		return
	}
	id := c.Input().Get("id")

	var workflow object.Workflow
	err := json.Unmarshal(c.Body(), &workflow)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	// Whose row this is, not what the body said — the same answer this table's
	// listing uses.
	workflow.Owner = owner

	success, err := object.UpdateWorkflow(id, &workflow, c.GetAcceptLanguage())
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(success)
}

// AddWorkflow
// @Title AddWorkflow
// @Tag Workflow API
// @Description add workflow
// @Param body body object.Workflow true "The details of the workflow"
// @Success 200 {object} controllers.Response The Response object
// @router /add-workflow [post]
func (c *ApiController) AddWorkflow() {
	owner, allowed := c.GetScopedOwner()
	if !allowed {
		return
	}
	var workflow object.Workflow
	err := json.Unmarshal(c.Body(), &workflow)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	// Whose row this is, not what the body said — the same answer this table's
	// listing uses.
	workflow.Owner = owner

	success, err := object.AddWorkflow(&workflow, c.GetAcceptLanguage())
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(success)
}

// DeleteWorkflow
// @Title DeleteWorkflow
// @Tag Workflow API
// @Description delete workflow
// @Param body body object.Workflow true "The details of the workflow"
// @Success 200 {object} controllers.Response The Response object
// @router /delete-workflow [post]
func (c *ApiController) DeleteWorkflow() { stored(c, c.GetScopedOwner, object.DeleteWorkflow) }
