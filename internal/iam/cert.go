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

// GetCert fetches a signing certificate by name from the PLATFORM partition,
// which is where certs live — the same partition GetApplication reads from.
//
// It used to qualify the id with c.OrganizationName, the caller's own tenant.
// That is the wrong owner and it was silent: the application read resolves
// admin/<app>, the application's `cert` field is a bare name, and the cert row
// is written owner=admin — so a deployment whose IAM_ORG was anything but
// "admin" asked for <tenant>/<cert>, got "the entity does not exist", and could
// not establish the key every bearer token is validated against. The store had
// the cert the whole time; only the question was addressed to the wrong tenant.
//
// Both reads now name the partition through one constant, so an application and
// its own certificate can no longer be looked up in two different places.
func (c *Client) GetCert(name string) (*Cert, error) {
	url := c.GetUrl("get-cert", map[string]string{
		"id": fmt.Sprintf("%s/%s", PlatformOwner, name),
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
