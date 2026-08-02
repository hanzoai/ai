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

// Application is the subset of the IAM Application model ai reads: DisplayName,
// the signing cert name, and the OAuth redirect allowlist. Read-only response
// (GetApplication), so a projection is correct.
type Application struct {
	Owner        string   `json:"owner"`
	Name         string   `json:"name"`
	DisplayName  string   `json:"displayName"`
	Cert         string   `json:"cert"`
	RedirectUris []string `json:"redirectUris"`
}

// GetApplication fetches an application by name (owned by admin).
// PlatformOwner is the reserved org that holds cross-tenant configuration:
// applications, organizations and the signing certs applications point at. It is
// not a tenant and nothing customer-facing lives in it.
//
// It is a constant, and named, because these reads MUST agree. An application
// addressed in one partition and its certificate in another resolves an app and
// then fails to resolve the key that app's tokens are signed with — which reads
// as a missing cert rather than as a mis-addressed one.
const PlatformOwner = "admin"

func (c *Client) GetApplication(name string) (*Application, error) {
	url := c.GetUrl("get-application", map[string]string{
		"id": fmt.Sprintf("%s/%s", PlatformOwner, name),
	})
	bytes, err := c.DoGetBytes(url)
	if err != nil {
		return nil, err
	}
	var application *Application
	if err = json.Unmarshal(bytes, &application); err != nil {
		return nil, err
	}
	return application, nil
}

// GetApplication uses the configured (or env-derived) client.
func GetApplication(name string) (*Application, error) {
	return ensureClient().GetApplication(name)
}
