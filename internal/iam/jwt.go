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
//
// The endpoint here is the STATED one, never the public default the read helpers
// fall back to. A trust anchor is the one thing that must not be guessed: with the
// default in play, a process configured with a certificate and no endpoint verified
// every signature against keys fetched from a host nobody named — a guess overriding
// a stated answer, on the security boundary — and carried an internet round trip
// inside every parse to do it.
func (c *Client) ParseJwtToken(token string) (*Claims, error) {
	t, err := jwt.ParseWithClaims(token, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		switch token.Method.Alg() {
		case jwt.SigningMethodES256.Alg(), jwt.SigningMethodES512.Alg(),
			jwt.SigningMethodRS256.Alg(), jwt.SigningMethodRS512.Alg():
			// Prefer JWKS (canonical, public keys) over any configured cert PEM.
			if endpoint := statedEndpoint(c.Endpoint); endpoint != "" {
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
			claims.typeMachine()
			return claims, nil
		}
	}
	return nil, err
}

// Machine is the User.Type a program carries — the word IAM's client_credentials
// grant states in the token, and the word this package writes wherever it has to
// answer the same question itself. Those two must agree: one principal typed two
// ways is one principal a reader has to know the provenance of. IAM also writes
// "service-account" on the rows behind API keys, and account.IsMachine reads both.
//
// Exported because a credential assembled in this process (the cloud-agent key)
// has to say the same word rather than a second copy of it.
const Machine = "application"

// typeMachine fills in what the token leaves unsaid: that its principal is a
// program.
//
// A client-credentials token carries no `type`. Measured on a live hanzo.id token,
// the whole payload is iss/sub/aud/exp/nbf/iat/jti, owner, organization, name,
// preferred_username, billing_account, azp, tokenType — and nothing that says
// machine. So account.IsMachine, which reads User.Type, answered "person" for every
// principal that authenticates by token, and the only credentials it ever
// recognised were the API keys, whose Type comes off the user row instead.
//
// The token does say it, in the one way this grant can: the client is its own
// subject. A person's token is minted FOR an application and names a person, so the
// two differ; a machine's token names the application itself, so the principal's
// name IS its audience. A human token cannot reach that equality, because a person
// is not an application.
//
// Stamped here, at the single parse, so every layer below reads the one field with
// the one predicate rather than each re-deriving the answer from claims. An explicit
// type from IAM is left exactly as it came — which is the case that matters now
// that the grant states the class itself, because this inference cannot see a
// SHARED app: IAM qualifies such an app's audience with its org, so `aud` and
// `name` no longer match and a shared app's machine reads here as a person. The
// inference remains for tokens minted before the claim shipped, and retires with
// the last of them.
func (c *Claims) typeMachine() {
	if c == nil || c.User.Type != "" || c.User.Name == "" {
		return
	}
	// A PRINCIPAL WITH MEMBERSHIPS IS A PERSON, and that is signed, so it outranks
	// the shape below.
	//
	// The reading this function performs — the client is its own subject, so its
	// name IS its audience — assumed a person could never reach that equality. A
	// person can: IAM's audience is the app's client id, its client ids are its app
	// names (`hanzo-cloud`), and its usernames are `^[a-z0-9][a-z0-9._-]{0,62}$`
	// with no reserved list, so signing up as `hanzo-cloud` makes name == aud on a
	// perfectly ordinary person's token. That token then types as a machine, and
	// account.Payer's machine branch pays from the ORG rather than the person —
	// which in the signup org is the difference between a wallet that starts at
	// zero and the platform pool.
	//
	// IAM already distinguishes the two, in a claim it signs and this package
	// already parses: it seeds every person's membership set with their home org
	// (store.MemberOrgRefs, "member" for a plain member, so never empty) and leaves
	// it nil for a client-credentials token, which has no memberships to state.
	// hanzoai/cloud reads exactly this to answer the same question
	// (authz.Claims.Machine is len(Orgs) == 0), so deferring to it here is not a
	// third rule — it is the two readers finally agreeing.
	//
	// Only the INFERENCE defers. A type IAM stated outright is still honored above,
	// so a machine that somehow carried memberships would keep its stated class.
	if len(c.Orgs) > 0 {
		return
	}
	for _, aud := range c.Audience {
		if aud == c.User.Name {
			c.User.Type = Machine
			return
		}
	}
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
