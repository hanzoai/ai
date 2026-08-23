// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	iam "github.com/hanzoai/ai/internal/iam"
)

// A browser or a first-party client signs in and carries a token IAM signed. It
// is verified here — signature against IAM's published keys, then issuer and
// audience — so a test that wants to be a signed-in person has to present one.
//
// withSignIn stands up that half of IAM: a key pair, the JWKS document the
// verifier fetches, and the issuer and audience the policy accepts. It returns a
// function that mints a credential for whoever you name.
//
// The alternative — reaching past the check to plant a user — would test the
// handler against an identity the real one could never produce, and would keep
// passing if the verification were removed altogether.
func withSignIn(t *testing.T) func(*iam.User) string {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const kid = "test-key"
	doc := map[string]any{"keys": []map[string]string{{
		"kty": "RSA",
		"kid": kid,
		"n":   base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
	}}}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/iam/.well-known/jwks" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(server.Close)

	issuer := server.URL
	// IAM_ENDPOINT is the trust anchor the verifier fetches keys from, and it is
	// deliberately never guessed — unset, verification falls through to a
	// certificate PEM and reports "not valid PEM", which reads like a key problem
	// and is a missing address.
	t.Setenv("IAM_URL", server.URL)
	t.Setenv("IAM_ENDPOINT", server.URL)
	t.Setenv("JWT_ISSUER", issuer)
	t.Setenv("IAM_AUDIENCE", "hanzo-test")

	return func(user *iam.User) string {
		t.Helper()
		claims := &iam.Claims{
			User: *user,
			RegisteredClaims: jwt.RegisteredClaims{
				Issuer:    issuer,
				Audience:  jwt.ClaimStrings{"hanzo-test"},
				Subject:   user.Name,
				IssuedAt:  jwt.NewNumericDate(time.Now().Add(-time.Minute)),
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			},
		}
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		token.Header["kid"] = kid
		signed, err := token.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return "Bearer " + signed
	}
}

// The harness has to produce a credential the real verification accepts, or every
// test built on it is testing nothing.
func TestTheSignInHarnessProducesARealCredential(t *testing.T) {
	signIn := withSignIn(t)
	credential := signIn(&iam.User{Owner: "acme", Name: "alice", IsAdmin: true})

	c := as(visit("GET", "/v1/ai/whoami"), credential)
	user := c.GetSessionUser()
	if user == nil {
		t.Fatal("a credential the harness minted was not accepted")
	}
	if user.Owner != "acme" || user.Name != "alice" || !user.IsAdmin {
		t.Errorf("it resolved to %+v", user)
	}

	// And something it did not mint is refused.
	if c := as(visit("GET", "/x"), "Bearer not-a-token"); c.GetSessionUser() != nil {
		t.Error("a credential nobody signed was accepted")
	}
	if visit("GET", "/x").GetSessionUser() != nil {
		t.Error("a request carrying nothing resolved to somebody")
	}
}
