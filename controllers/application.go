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
	"fmt"

	"github.com/hanzoai/ai/cluster"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

// GetApplications
// @Title GetApplications
// @Tag Application API
// @Description get applications
// @Param owner query string true "The owner of applications"
// @Success 200 {array} object.Application The Response object
// @router /get-applications [get]
func (c *ApiController) GetApplications() {
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
		applications, err := object.GetApplications(owner)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}
		cluster.Describe(applications, c.GetAcceptLanguage())
		c.ResponseOk(applications)
	} else {
		limit := util.ParseInt(limit)
		count, err := object.GetApplicationCount(owner, field, value)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		paginator := util.NewPaginator(c.PageAsked(), limit, count)
		applications, err := object.GetPaginationApplications(owner, paginator.Offset(), limit, field, value, sortField, sortOrder)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		cluster.Describe(applications, c.GetAcceptLanguage())
		c.ResponseOk(applications, paginator.Nums())
	}
}

// GetApplication
// @Title GetApplication
// @Tag Application API
// @Description get application
// @Param id query string true "The id of application"
// @Success 200 {object} object.Application The Response object
// @router /get-application [get]
// applicationFor resolves the application an id names, for a caller entitled to
// act on it.
//
// An application is a manifest and a namespace to apply it in, so acting on
// somebody else's is creating objects in their cluster space. One the caller's
// organization does not reach answers the same way as one that is not there.
func applicationFor(user *iam.User, id string) (*object.Application, error) {
	application, err := object.GetApplication(id)
	if err != nil {
		return nil, err
	}
	if application == nil || !reaches(user, application.Owner) {
		return nil, fmt.Errorf("the application: %s does not exist", id)
	}
	return application, nil
}

func (c *ApiController) GetApplication() {
	caller, ok := c.RequireSignedInUser()
	if !ok {
		return
	}
	id := c.Input().Get("id")

	res, err := object.GetApplication(id)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	// The listing beside this one scopes to the caller's organization; addressing
	// one application by id has to answer the same way, all the more so because the
	// description attached below reads the live namespace — its services, its
	// addresses, and which credentials each deployment expects.
	if res == nil || !reaches(caller, res.Owner) {
		c.ResponseError(c.T("general:The application does not exist"))
		return
	}

	cluster.Describe([]*object.Application{res}, c.GetAcceptLanguage())
	c.ResponseOk(res)
}

// UpdateApplication
// @Title UpdateApplication
// @Tag Application API
// @Description update application
// @Param id query string true "The id (owner/name) of the application"
// @Param body body object.Application true "The details of the application"
// @Success 200 {object} controllers.Response The Response object
// @router /update-application [post]
func (c *ApiController) UpdateApplication() {
	caller, ok := c.RequireSignedInUser()
	if !ok {
		return
	}
	id := c.Input().Get("id")
	if _, err := applicationFor(caller, id); err != nil {
		c.ResponseError(err.Error())
		return
	}

	var application object.Application
	err := json.Unmarshal(c.Body(), &application)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	application.Manifest, err = cluster.Manifest(&application, c.GetAcceptLanguage())
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	success, err := object.UpdateApplication(id, &application)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(success)
}

// AddApplication
// @Title AddApplication
// @Tag Application API
// @Description add application
// @Param body body object.Application true "The details of the application"
// @Success 200 {object} controllers.Response The Response object
// @router /add-application [post]
func (c *ApiController) AddApplication() {
	owner, allowed := c.GetScopedOwner()
	if !allowed {
		return
	}
	var application object.Application
	err := json.Unmarshal(c.Body(), &application)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	// Filed into the caller's own organization — the one GetApplications reads
	// back — and the template below is looked up under it.
	application.Owner = owner

	if application.Template == "" {
		c.ResponseError(c.T("application:Missing required parameters"))
		return
	}

	// Verify template exists
	template, err := object.GetTemplate(util.GetIdFromOwnerAndName(application.Owner, application.Template))
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	if template == nil {
		c.ResponseError(c.T("application:The Template not found"))
		return
	}

	success, err := object.AddApplication(&application)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(success)
}

// DeleteApplication
// @Title DeleteApplication
// @Tag Application API
// @Description delete application
// @Param body body object.Application true "The details of the application"
// @Success 200 {object} controllers.Response The Response object
// @router /delete-application [post]
func (c *ApiController) DeleteApplication() {
	caller, ok := c.RequireSignedInUser()
	if !ok {
		return
	}
	var body object.Application
	err := json.Unmarshal(c.Body(), &body)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	// The stored row, not the body's copy: what is torn down is the namespace this
	// application was deployed in, and the body does not decide that.
	application, err := applicationFor(caller, util.GetIdFromOwnerAndName(body.Owner, body.Name))
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	// Best-effort teardown of what the record deployed, then drop the record. An
	// org with no Kubernetes provider still deletes its applications.
	_, _ = cluster.Undeploy(application.Owner, application.Name, application.Namespace, c.GetAcceptLanguage())

	success, err := object.DeleteApplication(application)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(success)
}

// DeployApplication
// @Title DeployApplication
// @Tag Application API
// @Description deploy application synchronously
// @Param body body object.Application true "The details of the application"
// @Success 200 {object} controllers.Response The Response object
// @router /deploy-application [post]
func (c *ApiController) DeployApplication() {
	caller, ok := c.RequireSignedInUser()
	if !ok {
		return
	}
	id := c.Input().Get("id")

	var application object.Application
	err := json.Unmarshal(c.Body(), &application)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	if _, err := applicationFor(caller, id); err != nil {
		c.ResponseError(err.Error())
		return
	}

	application.Manifest, err = cluster.Manifest(&application, c.GetAcceptLanguage())
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	success, err := object.UpdateApplication(id, &application)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	if !success {
		c.ResponseError(c.T("application:Failed to update application"))
		return
	}

	// Deploy the application synchronously and wait for completion
	success, err = cluster.DeploySync(&application, c.GetAcceptLanguage())
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	updatedApplication, err := object.GetApplication(id)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(updatedApplication)
}

// UndeployApplication
// @Title UndeployApplication
// @Tag Application API
// @Description undeploy application synchronously
// @Param body body object.Application true "The details of the application"
// @Success 200 {object} controllers.Response The Response object
// @router /undeploy-application [post]
func (c *ApiController) UndeployApplication() {
	caller, ok := c.RequireSignedInUser()
	if !ok {
		return
	}
	id := c.Input().Get("id")

	application, err := applicationFor(caller, id)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	// Undeploy the application synchronously and wait for completion
	success, err := cluster.UndeploySync(owner, name, application.Namespace, c.GetAcceptLanguage())
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(success)
}
