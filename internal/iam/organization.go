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

import "fmt"

// Organization is the subset of the IAM Organization model ai reads. It is a
// read-only response (GetOrganization), so a projection is correct: json
// unmarshal ignores the fields ai does not consume.
type Organization struct {
	Owner       string `json:"owner"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
}

// GetOrganization reads one organization.
//
// IAM serves no per-record route for organizations — only the listing and the
// older get-organization, which answers inside a {status, msg, data} envelope
// and reports a miss as 200 with a null data. So the envelope is decoded and an
// absent record is turned into an error here; read straight into Organization it
// would be a zero-valued success.
func (c *Client) GetOrganization(name string) (*Organization, error) {
	var envelope struct {
		Msg  string        `json:"msg"`
		Data *Organization `json:"data"`
	}
	if err := c.get("get-organization", map[string]string{"id": PlatformOwner + "/" + name}, &envelope); err != nil {
		return nil, err
	}
	if envelope.Data == nil {
		if envelope.Msg == "" {
			envelope.Msg = "organization not found"
		}
		return nil, fmt.Errorf("iam: get organization %q: %s", name, envelope.Msg)
	}
	return envelope.Data, nil
}

// GetOrganization uses the configured (or env-derived) client.
func GetOrganization(name string) (*Organization, error) {
	return ensureClient().GetOrganization(name)
}
