// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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

package iam

import (
	"encoding/json"
	"fmt"
)

// Permission mirrors the IAM server's Permission JSON model. ai marshals a
// client-supplied permission into this type and posts it back on
// add/update/delete, so the full field set is kept for a lossless round-trip
// (a slim struct would drop unknown fields and corrupt the stored permission).
type Permission struct {
	Owner       string `json:"owner"`
	Name        string `json:"name"`
	CreatedTime string `json:"createdTime"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`

	Users   []string `json:"users"`
	Groups  []string `json:"groups"`
	Roles   []string `json:"roles"`
	Domains []string `json:"domains"`

	Model        string   `json:"model"`
	Adapter      string   `json:"adapter"`
	ResourceType string   `json:"resourceType"`
	Resources    []string `json:"resources"`
	Actions      []string `json:"actions"`
	Effect       string   `json:"effect"`
	IsEnabled    bool     `json:"isEnabled"`

	Submitter   string `json:"submitter"`
	Approver    string `json:"approver"`
	ApproveTime string `json:"approveTime"`
	State       string `json:"state"`
}

// GetPermission fetches a permission by name within the client's organization.
func (c *Client) GetPermission(name string) (*Permission, error) {
	url := c.GetUrl("get-permission", map[string]string{
		"id": fmt.Sprintf("%s/%s", c.OrganizationName, name),
	})
	bytes, err := c.DoGetBytes(url)
	if err != nil {
		return nil, err
	}
	var permission *Permission
	if err = json.Unmarshal(bytes, &permission); err != nil {
		return nil, err
	}
	return permission, nil
}

// GetPermissions lists all permissions in the client's organization.
func (c *Client) GetPermissions() ([]*Permission, error) {
	url := c.GetUrl("get-permissions", map[string]string{"owner": c.OrganizationName})
	bytes, err := c.DoGetBytes(url)
	if err != nil {
		return nil, err
	}
	var permissions []*Permission
	if err = json.Unmarshal(bytes, &permissions); err != nil {
		return nil, err
	}
	return permissions, nil
}

func (c *Client) modifyPermission(action string, permission *Permission) (bool, error) {
	queryMap := map[string]string{
		"id": fmt.Sprintf("%s/%s", permission.Owner, permission.Name),
	}
	permission.Owner = c.OrganizationName
	postBytes, err := json.Marshal(permission)
	if err != nil {
		return false, err
	}
	resp, err := c.DoPost(action, queryMap, postBytes, false, false)
	if err != nil {
		return false, err
	}
	return resp.Data == "Affected", nil
}

func (c *Client) AddPermission(permission *Permission) (bool, error) {
	return c.modifyPermission("add-permission", permission)
}

func (c *Client) UpdatePermission(permission *Permission) (bool, error) {
	return c.modifyPermission("update-permission", permission)
}

func (c *Client) DeletePermission(permission *Permission) (bool, error) {
	return c.modifyPermission("delete-permission", permission)
}

// Package-level helpers.

func GetPermission(name string) (*Permission, error) { return ensureClient().GetPermission(name) }
func GetPermissions() ([]*Permission, error)         { return ensureClient().GetPermissions() }
func AddPermission(permission *Permission) (bool, error) {
	return ensureClient().AddPermission(permission)
}
func UpdatePermission(permission *Permission) (bool, error) {
	return ensureClient().UpdatePermission(permission)
}
func DeletePermission(permission *Permission) (bool, error) {
	return ensureClient().DeletePermission(permission)
}
