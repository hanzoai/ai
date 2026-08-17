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
// The tests that used to fabricate an identity were not testing authorisation —
// they were testing a door. This is the door everyone else uses.
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
// The endpoint is left EMPTY on purpose: with no IAM to ask, ParseJwtToken falls
// back to the configured certificate, which is the one thing a test can control.
// Given an endpoint it would prefer that JWKS and never consult this key.
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
