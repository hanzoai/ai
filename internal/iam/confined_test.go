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
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

func signed(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = "cert-delegation"
	s, err := tok.SignedString(k)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func delegatedClaims(scope string) jwt.MapClaims {
	return jwt.MapClaims{
		"iss": "https://hanzo.id", "sub": "uuid-alice", "aud": []string{"https://api.hanzo.ai"},
		"exp": time.Now().Add(time.Hour).Unix(), "scope": scope, "owner": "acme",
		"name": "alice", "preferred_username": "alice", "tokenType": "access-token",
		"orgs": []map[string]string{{"org": "acme", "role": "member"}},
		"act":  map[string]string{"sub": "admin/hanzo-cloud"},
	}
}

// Confined chooses a verifier from the unverified scope, by the word alone.
func TestConfinedReadsTheScope(t *testing.T) {
	for scope, want := range map[string]bool{
		Inference:               true,
		"openid " + Inference:   true,
		"openid profile":        false,
		"ai:inference-extended": false,
		"":                      false,
	} {
		if got := Confined(signed(t, delegatedClaims(scope))); got != want {
			t.Errorf("Confined(scope %q) = %v, want %v", scope, got, want)
		}
	}
	for _, junk := range []string{"", "a.b", "a.!!!.c", "a.e30.c"} {
		if Confined(junk) {
			t.Errorf("Confined(%q) = true", junk)
		}
	}
}

// ClaimsOf reads a verified token the way ParseJwtToken reads one: the person,
// their home org from the signed membership set, and no machine class.
func TestClaimsOfReadsWhatParseJwtTokenReads(t *testing.T) {
	c, err := ClaimsOf(signed(t, delegatedClaims(Inference)))
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject != "uuid-alice" || c.User.Owner != "acme" || c.User.Name != "alice" || c.User.Type != "" ||
		len(c.Orgs) != 1 || c.Orgs[0].Org != "acme" {
		t.Fatalf("claims = sub %q owner %q name %q type %q orgs %+v", c.Subject, c.User.Owner, c.User.Name, c.User.Type, c.Orgs)
	}
	nobody := delegatedClaims(Inference)
	delete(nobody, "sub")
	if _, err := ClaimsOf(signed(t, nobody)); err == nil {
		t.Fatal("a token naming nobody was read as somebody")
	}
}
