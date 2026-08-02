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
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"

	"github.com/golang-jwt/jwt/v4"
	"github.com/hanzoai/account"
)

// Claims is the verified access-token claim set. It embeds the User (so a token
// carries the subject's profile) plus the standard registered claims and the
// Hanzo-specific token/org/billing claims. Shape matches the IAM server so the
// /get-account response promotes every field identically.
type Claims struct {
	User
	AccessToken string `json:"accessToken"`
	jwt.RegisteredClaims
	TokenType        string `json:"tokenType"`
	RefreshTokenType string `json:"TokenType"`
	SigninMethod     string `json:"signinMethod"`
	// Orgs is the signed membership set. The type is account's, not a local
	// re-declaration: the JSON tags are a wire contract with IAM, and a copy of
	// them here is a copy that can drift one field at a time. If it did, every
	// membership would decode empty and every org switch would fail closed to
	// home — presenting as "the switcher does nothing".
	Orgs []account.OrgRef `json:"orgs,omitempty"`
}

// NOTE: `billing_account` is deliberately NOT declared here. It lives on the
// embedded User (see user.go), because WHO PAYS is a property of the identity,
// not of the token envelope that happened to carry it. Every spend site holds a
// *User — not a *Claims — so a field here is one only the auth layer can read,
// which is exactly how 27 of the 28 account.Payer call sites came to ignore it.
// Field promotion keeps `claims.BillingAccount` reading the same as before, and
// encoding/json still unmarshals the claim into the promoted field.
//
// Do not re-add it to Claims: an outer field SHADOWS the embedded one, so the
// claim would silently land here and leave User.BillingAccount empty — the
// failure would be invisible and would bill the wrong account.

// ParseJwtToken verifies a JWT's signature against the IAM server's published
// JWKS (proper OIDC) and returns its claims. RS256/RS512/ES256/ES512 only.
func ParseJwtToken(token string) (*Claims, error) { return ensureClient().ParseJwtToken(token) }

// ParseJwtToken verifies token against this client's IAM endpoint JWKS, falling
// back to a configured certificate PEM only when JWKS is unreachable.
func (c *Client) ParseJwtToken(token string) (*Claims, error) {
	t, err := jwt.ParseWithClaims(token, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		switch token.Method.Alg() {
		case jwt.SigningMethodES256.Alg(), jwt.SigningMethodES512.Alg(),
			jwt.SigningMethodRS256.Alg(), jwt.SigningMethodRS512.Alg():
			// Prefer JWKS (canonical, public keys) over any configured cert PEM.
			if endpoint := c.endpoint(); endpoint != "" {
				kid, _ := token.Header["kid"].(string)
				if pk, jwksErr := jwksPublicKey(endpoint, kid); jwksErr == nil {
					return pk, nil
				}
			}
			return publicKeyFromPEM([]byte(c.Certificate))
		default:
			return nil, fmt.Errorf("iam: unsupported signing method: %v", token.Header["alg"])
		}
	})
	if t != nil {
		if claims, ok := t.Claims.(*Claims); ok && t.Valid {
			return claims, nil
		}
	}
	return nil, err
}

func publicKeyFromPEM(pemBytes []byte) (interface{}, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("iam: not valid PEM")
	}
	if block.Type == "CERTIFICATE" {
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("iam: parse certificate: %w", err)
		}
		return cert.PublicKey, nil
	}
	return x509.ParsePKIXPublicKey(block.Bytes)
}
