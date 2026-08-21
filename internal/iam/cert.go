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
//
// The read is a GET, and that is load-bearing rather than incidental: IAM
// decides whether a request is a READ from its method, so the same call shaped
// as a POST is weighed as a write and the self-read grant that lets an
// application fetch the one cert its own row names does not fire. That answers
// 403, not 404 — a refusal that reads like a permissions regression while the
// only thing wrong is the verb.
func (c *Client) GetCert(name string) (*Cert, error) {
	var cert *Cert
	if err := c.get("certs/get", Ref{Owner: PlatformOwner, Name: name}.query(), &cert); err != nil {
		return nil, err
	}
	return cert, nil
}

// GetCert uses the configured (or env-derived) client.
func GetCert(name string) (*Cert, error) { return ensureClient().GetCert(name) }
