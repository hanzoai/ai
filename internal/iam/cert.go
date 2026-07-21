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

// Cert is the subset of the IAM Cert model ai reads. ai consumes the PEM in
// Certificate (to verify token signatures when JWKS is unavailable).
type Cert struct {
	Owner           string `json:"owner"`
	Name            string `json:"name"`
	DisplayName     string `json:"displayName"`
	Scope           string `json:"scope"`
	Type            string `json:"type"`
	CryptoAlgorithm string `json:"cryptoAlgorithm"`
	Certificate     string `json:"certificate"`
}

// GetCert fetches a certificate by name within the client's organization.
func (c *Client) GetCert(name string) (*Cert, error) {
	url := c.GetUrl("get-cert", map[string]string{
		"id": fmt.Sprintf("%s/%s", c.OrganizationName, name),
	})
	bytes, err := c.DoGetBytes(url)
	if err != nil {
		return nil, err
	}
	var cert *Cert
	if err = json.Unmarshal(bytes, &cert); err != nil {
		return nil, err
	}
	return cert, nil
}

// GetCert uses the configured (or env-derived) client.
func GetCert(name string) (*Cert, error) { return ensureClient().GetCert(name) }
