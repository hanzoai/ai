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

package object

import (
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

// A token IAM delegated for model calls is signed by a key the public JWKS never
// carries, so this module cannot verify it and must not try. It asks the host's
// verifier about the exact bytes; with none installed it refuses; and a token
// that is not confined never reaches that verifier.
func TestAConfinedTokenIsTheHostsToVerify(t *testing.T) {
	t.Cleanup(func() { SetConfinedVerifier(nil) })
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	mint := func(scope string) string {
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"iss": "https://hanzo.id", "sub": "uuid-alice", "aud": []string{"https://api.hanzo.ai"},
			"exp": time.Now().Add(time.Hour).Unix(), "scope": scope, "owner": "acme",
			"name": "alice", "preferred_username": "alice", "tokenType": "access-token",
			"orgs": []map[string]string{{"org": "acme", "role": "member"}},
		})
		tok.Header["kid"] = "cert-delegation"
		s, err := tok.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	delegated := mint("ai:inference")

	SetConfinedVerifier(nil)
	if _, err := ParseAndValidateJWT(delegated); !errors.Is(err, ErrJWTConfined) {
		t.Fatalf("no host verifier: %v, want ErrJWTConfined", err)
	}

	var asked []string
	refuse := errors.New("not this one")
	verdict := refuse
	SetConfinedVerifier(func(token string) error {
		asked = append(asked, token)
		return verdict
	})
	if _, err := ParseAndValidateJWT(delegated); !errors.Is(err, refuse) {
		t.Fatalf("host refused, module answered %v", err)
	}
	verdict = nil
	c, err := ParseAndValidateJWT(delegated)
	if err != nil {
		t.Fatalf("host verified, module refused: %v", err)
	}
	if c.User.Owner != "acme" || c.User.Name != "alice" || c.Subject != "uuid-alice" {
		t.Fatalf("claims = owner %q name %q sub %q, want alice in acme", c.User.Owner, c.User.Name, c.Subject)
	}
	if len(asked) != 2 || asked[0] != delegated || asked[1] != delegated {
		t.Fatalf("the host was asked about %d tokens, want the exact delegated bytes twice", len(asked))
	}

	// An ordinary token is this module's to verify, never the host's verifier's.
	asked = nil
	_, _ = ParseAndValidateJWT(mint("openid profile"))
	if len(asked) != 0 {
		t.Fatal("an ordinary token was handed to the confined verifier")
	}
}
