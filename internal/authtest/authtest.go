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

// Package authtest mints a credential a test can present.
//
// Identity here is a VERIFIED token and nothing else. GetSessionClaims parses and
// validates; there is no field to write a principal into and no store to seed. That
// is the property worth keeping — it is what stopped this service minting its own
// admins out of an MD5 and a memory map — so a test that needs an authenticated
// caller has to present a real credential rather than fabricate the answer.
//
// So this signs one. It installs a certificate the verifier will trust and signs
// against the matching key, and the code under test then authenticates exactly the
// way it does in production: the same parse, the same signature check, the same
// issuer policy. Nothing is bypassed and no seam is added to reach around.
//
// A test that fabricates an identity is not testing authorisation — it is testing
// an entry point of its own. This is the entry point everyone else uses.
//
// The key is generated once per process and never leaves it, so a token minted here
// is worth nothing anywhere else. It lives under internal/ and only tests import it.
package authtest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"

	iam "github.com/hanzoai/ai/internal/iam"
)

// issuer is the one this deployment expects by default (object.defaultJWTIssuer).
// A token carrying any other is refused by the issuer policy, which is the point of
// signing a real one.
const issuer = "https://hanzo.id"

var (
	once sync.Once
	key  *rsa.PrivateKey
	fail error
)

// install generates the key pair and points the verifier at its public half.
//
// The endpoint is left EMPTY on purpose: with no IAM stated, ParseJwtToken falls
// back to the configured certificate, which is the one thing a test can control.
// Given an endpoint it would prefer that JWKS and never consult this key — and it
// used to be given one anyway, because the read helpers' public default stood in
// for the endpoint nobody had stated. Every token minted here therefore went out
// to hanzo.id to be verified, and passed only because that host publishes nine
// keys and these tokens name no kid, so the lookup was ambiguous and the fallback
// ran. One key published there would have failed this whole package, and so would
// a machine that could not reach it.
func install() {
	key, fail = rsa.GenerateKey(rand.Reader, 2048)
	if fail != nil {
		return
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		fail = err
		return
	}
	iam.InitConfig("", "", "", string(pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: der,
	})), "admin", "ai")
}

// Signing is the key the verifier trusts, for a test that has to mint a credential
// this package's own happy path cannot: another brand's issuer, a window that has
// already closed, an algorithm nobody agreed to. Those claim sets are each a
// different attack and belong at the test that names them — what must not be
// duplicated is the INSTALL, because two installers race over one global and the
// loser signs against a certificate that is no longer there.
func Signing(t testing.TB) *rsa.PrivateKey {
	t.Helper()
	once.Do(install)
	if fail != nil {
		t.Fatalf("authtest: %v", fail)
	}
	return key
}

// Token is a bearer credential for u, signed so the service will accept it.
func Token(t testing.TB, u iam.User) string {
	t.Helper()
	once.Do(install)
	if fail != nil {
		t.Fatalf("authtest: %v", fail)
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodRS256, iam.Claims{
		User: u,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    issuer,
			Subject:   u.Owner + "/" + u.Name,
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}).SignedString(key)
	if err != nil {
		t.Fatalf("authtest: sign: %v", err)
	}
	return signed
}

// Bearer is Token as an Authorization header value.
func Bearer(t testing.TB, u iam.User) string {
	t.Helper()
	return "Bearer " + Token(t, u)
}
