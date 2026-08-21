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
	var permission *Permission
	if err := c.get("permissions/get", Ref{Owner: c.OrganizationName, Name: name}.query(), &permission); err != nil {
		return nil, err
	}
	return permission, nil
}

// GetPermissions lists all permissions in the client's organization.
func (c *Client) GetPermissions() ([]*Permission, error) {
	var page struct {
		Permissions []*Permission `json:"permissions"`
	}
	if err := c.get("permissions", map[string]string{"owner": c.OrganizationName}, &page); err != nil {
		return nil, err
	}
	return page.Permissions, nil
}

// writePermission posts permission to one of the permissions write routes. The
// record IS the request body — the retired surface also carried its key in an
// `?id=`, which the native routes read off the body instead.
//
// Success is the absence of a refusal. The old envelope reported it as the
// string "Affected" in `data`; the native writes answer with the stored record,
// so there is nothing left to compare against and a 2xx is the whole answer.
func (c *Client) writePermission(path string, permission *Permission) (bool, error) {
	permission.Owner = c.OrganizationName
	if err := c.post(path, nil, permission, nil); err != nil {
		return false, err
	}
	return true, nil
}

func (c *Client) AddPermission(permission *Permission) (bool, error) {
	return c.writePermission("permissions", permission)
}

func (c *Client) UpdatePermission(permission *Permission) (bool, error) {
	return c.writePermission("permissions/update", permission)
}

// DeletePermission removes a permission. Delete takes only the (owner, name)
// key, not the record: the route's input is the key alone, and posting a whole
// permission at it would rely on the extra fields being ignored.
func (c *Client) DeletePermission(permission *Permission) (bool, error) {
	ref := Ref{Owner: c.OrganizationName, Name: permission.Name}
	if err := c.post("permissions/delete", nil, ref, nil); err != nil {
		return false, err
	}
	return true, nil
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
