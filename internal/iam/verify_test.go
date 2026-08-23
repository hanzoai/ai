// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package iam

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

// publishes stands up an IAM that publishes one key, and returns a client
// pointed at it. Every request this process trusts is trusted because a
// signature verified against what an address like this published.
func publishes(t *testing.T, public *rsa.PublicKey, kid string) *Client {
	t.Helper()
	doc := map[string]any{"keys": []map[string]string{{
		"kty": "RSA",
		"kid": kid,
		"n":   base64.RawURLEncoding.EncodeToString(public.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(public.E)).Bytes()),
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
	return NewClient(server.URL, "", "", "", "", "")
}

func signedWith(t *testing.T, key *rsa.PrivateKey, kid string, method jwt.SigningMethod, secret any) string {
	t.Helper()
	claims := &Claims{
		User: User{Owner: "acme", Name: "alice"},
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "alice",
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-time.Minute)),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	token := jwt.NewWithClaims(method, claims)
	if kid != "" {
		token.Header["kid"] = kid
	}
	if secret == nil {
		secret = key
	}
	signed, err := token.SignedString(secret)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

// A token is who it says it is only because a key we published signed it. These
// are the ways that can be false.
func TestATokenIsOnlyAsGoodAsWhoSignedIt(t *testing.T) {
	ours, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	client := publishes(t, &ours.PublicKey, "k1")

	t.Run("signed by the key we publish", func(t *testing.T) {
		claims, err := client.ParseJwtToken(signedWith(t, ours, "k1", jwt.SigningMethodRS256, nil))
		if err != nil {
			t.Fatalf("a token we signed was refused: %v", err)
		}
		if claims.Name != "alice" || claims.Owner != "acme" {
			t.Errorf("it resolved to %s/%s", claims.Owner, claims.Name)
		}
	})

	// Somebody else's key. A well-formed token naming whoever they like, and the
	// whole of what stops it is that we never published the key that signed it.
	t.Run("signed by a key we do not", func(t *testing.T) {
		if _, err := client.ParseJwtToken(signedWith(t, theirs, "k1", jwt.SigningMethodRS256, nil)); err == nil {
			t.Fatal("a token signed by another key was accepted")
		}
	})

	// Naming a key we do not publish is not a way to skip the check.
	t.Run("naming a key we do not publish", func(t *testing.T) {
		if _, err := client.ParseJwtToken(signedWith(t, ours, "nobody", jwt.SigningMethodRS256, nil)); err == nil {
			t.Fatal("a token naming an unpublished key was accepted")
		}
	})

	// The two classic ways to ask a verifier to stop verifying.
	t.Run("asking for no signature", func(t *testing.T) {
		token := jwt.NewWithClaims(jwt.SigningMethodNone, &Claims{User: User{Owner: "admin", Name: "z"}})
		token.Header["kid"] = "k1"
		unsigned, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.ParseJwtToken(unsigned); err == nil {
			t.Fatal("an unsigned token was accepted")
		}
	})

	// Algorithm confusion: sign with HMAC, using the PUBLIC key as the secret. A
	// verifier that picks its algorithm from the header verifies it.
	t.Run("asking for a symmetric algorithm", func(t *testing.T) {
		der, err := x509.MarshalPKIXPublicKey(&ours.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		pub := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
		confused := signedWith(t, nil, "k1", jwt.SigningMethodHS256, pub)
		if _, err := client.ParseJwtToken(confused); err == nil {
			t.Fatal("a token signed with the public key as an HMAC secret was accepted")
		}
	})

	t.Run("expired", func(t *testing.T) {
		claims := &Claims{
			User: User{Owner: "acme", Name: "alice"},
			RegisteredClaims: jwt.RegisteredClaims{
				IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour)),
			},
		}
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		token.Header["kid"] = "k1"
		stale, err := token.SignedString(ours)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.ParseJwtToken(stale); err == nil {
			t.Fatal("an expired token was accepted")
		}
	})

	t.Run("not a token at all", func(t *testing.T) {
		for _, s := range []string{"", "not-a-token", "a.b.c", strings.Repeat("x", 400)} {
			if _, err := client.ParseJwtToken(s); err == nil {
				t.Errorf("%q was accepted", s)
			}
		}
	})
}

// jwkToRSAPublicKey reconstructs the key from what IAM published, and a
// reconstruction that drifts is a signature that stops verifying.
func TestReadingAPublishedKey(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	back, err := jwkToRSAPublicKey(jwk{
		Kty: "RSA", Kid: "k1",
		N: base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
		E: base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
	})
	if err != nil {
		t.Fatal(err)
	}
	if back.N.Cmp(key.PublicKey.N) != 0 || back.E != key.PublicKey.E {
		t.Error("the key came back different from the one published")
	}

	// Something that is not a key says so.
	if _, err := jwkToRSAPublicKey(jwk{Kty: "RSA", N: "!!!not base64!!!", E: "AQAB"}); err == nil {
		t.Error("a malformed modulus was read without complaint")
	}
}

// jwksURL is the one address a key is fetched from.
func TestWhereKeysAreFetchedFrom(t *testing.T) {
	const want = "https://hanzo.id/v1/iam/.well-known/jwks"
	for _, endpoint := range []string{"https://hanzo.id", "https://hanzo.id/", "https://hanzo.id///"} {
		if got := jwksURL(endpoint); got != want {
			t.Errorf("jwksURL(%q) = %q, want %q", endpoint, got, want)
		}
	}
}
