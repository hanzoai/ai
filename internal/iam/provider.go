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

// Provider is the subset of the IAM Provider model ai reads. ai filters the
// provider list by Category (e.g. "Storage"). Read-only response
// (GetProviders), so a projection is correct.
type Provider struct {
	Owner       string `json:"owner"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Category    string `json:"category"`
	Type        string `json:"type"`
}

// GetProviders lists all providers in the client's organization.
//
// The list arrives NAMED — {"providers":[…]} — where the retired surface put a
// bare array in an envelope's `data`. Decoding the new body as an array is not
// an error, it is an empty one: the caller filters a nil slice by Category and
// concludes the organization has no storage provider configured.
func (c *Client) GetProviders() ([]*Provider, error) {
	var page struct {
		Providers []*Provider `json:"providers"`
	}
	if err := c.get("providers", map[string]string{"owner": c.OrganizationName}, &page); err != nil {
		return nil, err
	}
	return page.Providers, nil
}

// GetProviders uses the configured (or env-derived) client.
func GetProviders() ([]*Provider, error) { return ensureClient().GetProviders() }
