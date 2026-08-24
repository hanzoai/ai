// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2024 The OpenAgent Authors. All Rights Reserved.
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
	"net/http"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

// GetConnections
// @Title GetConnections
// @Tag Connection API
// @Description get all connections
// @Param   pageSize     query    string  true        "The size of each page"
// @Param   p     query    string  true        "The number of the page"
// @Success 200 {object} object.Connection The Response object
// @router /get-connections [get]
func (c *ApiController) GetConnections() {
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
	status := c.Input().Get("status")

	if limit == "" || page == "" {
		connections, err := object.GetConnections(owner)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		c.ResponseOk(connections)
	} else {
		limit := util.ParseInt(limit)

		count, err := object.GetConnectionCount(owner, status, field, value)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		paginator := util.NewPaginator(c.PageAsked(), limit, count)
		connections, err := object.GetPaginationConnections(owner, status, paginator.Offset(), limit, field, value, sortField, sortOrder)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		c.ResponseOk(connections, paginator.Nums())
	}
}

// GetConnection
// @Title GetConnection
// @Tag Connection API
// @Description get connection
// @Param   id     query    string  true        "The id of connection"
// @Success 200 {object} object.Connection
// @router /get-connection [get]
func (c *ApiController) GetConnection() {
	id := c.Input().Get("id")

	connection, err := object.GetConnection(id)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(connection)
}

// DeleteConnection
// @Title DeleteConnection
// @Tag Connection API
// @Description delete connection
// @Param   id     query    string  true        "The id of connection"
// @Success 200 {object} Response
// @router /delete-connection [post]
func (c *ApiController) DeleteConnection() {
	caller, ok := c.RequireSignedInUser()
	if !ok {
		return
	}
	var body object.Connection
	err := json.Unmarshal(c.Body(), &body)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	connection, err := connectionFor(caller, util.GetIdFromOwnerAndName(body.Owner, body.Name))
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	affected, err := object.DeleteConnection(connection)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.JSON(http.StatusOK, wrapActionResponse(affected))
}

// UpdateConnection
// @Title UpdateConnection
// @Tag Connection API
// @Description update connection
// @Param   id     query    string  true        "The id of connection"
// @Param   body    body   object.Connection true "The connection object"
// @Success 200 {object} Response
// @router /update-connection [post]
func (c *ApiController) UpdateConnection() {
	caller, ok := c.RequireSignedInUser()
	if !ok {
		return
	}
	id := c.Input().Get("id")
	if _, err := connectionFor(caller, id); err != nil {
		c.ResponseError(err.Error())
		return
	}

	var connection object.Connection
	err := json.Unmarshal(c.Body(), &connection)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.JSON(http.StatusOK, wrapActionResponse(object.UpdateConnection(id, &connection)))
}

// AddConnection
// @Title AddConnection
// @Tag Connection API
// @Description add connection
// @Param   body    body   object.Connection true "The connection object"
// @Success 200 {object} Response
// @router /add-connection [post]
// connectionFor resolves the connection an id names, for a caller entitled to act
// on it. A connection is a session on a machine, so it is one their organization
// reaches — and one out of reach answers as one that is not there.
func connectionFor(user *iam.User, id string) (*object.Connection, error) {
	connection, err := object.GetConnection(id)
	if err != nil {
		return nil, err
	}
	if connection == nil || !reaches(user, connection.Owner) {
		return nil, fmt.Errorf("the connection: %s does not exist", id)
	}
	return connection, nil
}

func (c *ApiController) AddConnection() {
	caller, ok := c.RequireSignedInUser()
	if !ok {
		return
	}
	var connection object.Connection
	err := json.Unmarshal(c.Body(), &connection)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	// A connection belongs to the node it reaches, which is what CreateConnection
	// says too. Taking the owner off the body instead lets a connection name one
	// organization while pointing at another's node — and a tunnel is opened by
	// asking whether the CONNECTION is yours.
	node, err := object.GetNode(connection.Node)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	if node == nil || !reaches(caller, node.Owner) {
		c.ResponseError(c.T("general:The node does not exist"))
		return
	}
	connection.Owner = node.Owner

	c.JSON(http.StatusOK, wrapActionResponse(object.AddConnection(&connection)))
}

// StartConnection
// @Title StartConnection
// @Tag Connection API
// @Description start connection
// @Param   id     query    string  true        "The id of connection"
// @Success 200 {object} Response
// @router /start-connection [post]
func (c *ApiController) StartConnection() {
	caller, ok := c.RequireSignedInUser()
	if !ok {
		return
	}
	connectionId := c.Input().Get("id")
	if _, err := connectionFor(caller, connectionId); err != nil {
		c.ResponseError(err.Error())
		return
	}

	connection := &object.Connection{
		Status:    object.Connected,
		StartTime: util.GetCurrentTime(),
	}

	_, err := object.UpdateConnection(connectionId, connection, []string{"status", "start_time"}...)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk()
}

// StopConnection
// @Title StopConnection
// @Tag Connection API
// @Description stop connection
// @Param   id     query    string  true        "The id of connection"
// @Success 200 {object} Response
// @router /stop-connection [post]
func (c *ApiController) StopConnection() {
	connectionId := c.Input().Get("id")

	err := object.CloseConnection(connectionId, ForcedDisconnect, "The administrator forcibly closes the session")
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk()
}
