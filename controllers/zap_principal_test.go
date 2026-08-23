// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"
)

// What a credential has to look like to name somebody, asserted in the one place
// that decides it.
//
// Five copies of this used to exist and differed on two points, so both are
// pinned here: the space around a credential is not part of it, and a value that
// declares itself an API key by its prefix is evaluated as one and only one —
// never handed to JWT validation after failing as a key.
func TestWhatACredentialHasToLookLikeToNameSomebody(t *testing.T) {
	iamd := withIAM(t)
	alice := &iam.User{Owner: "acme", Name: "alice"}
	key := iamd.asUser(t, alice)

	t.Run("a key names its person", func(t *testing.T) {
		if u := zapPrincipal(key); u == nil || u.Name != "alice" {
			t.Errorf("resolved %+v, want alice", u)
		}
	})

	t.Run("the space around it is not part of it", func(t *testing.T) {
		if u := zapPrincipal(key + "   "); u == nil || u.Name != "alice" {
			t.Errorf("a trailing space cost the credential its person: %+v", u)
		}
	})

	t.Run("nothing names nobody", func(t *testing.T) {
		for _, auth := range []string{"", "Bearer ", "Bearer    "} {
			if u := zapPrincipal(auth); u != nil {
				t.Errorf("%q resolved to %+v", auth, u)
			}
		}
	})

	t.Run("a key IAM does not know names nobody", func(t *testing.T) {
		if u := zapPrincipal("Bearer sk-not-a-key"); u != nil {
			t.Errorf("an unknown key resolved to %+v", u)
		}
	})

	// A value can be shaped like both — an sk- prefix and three long dot-separated
	// parts — and it is read as the key it claims to be. Falling through to JWT
	// validation after failing as a key would let a caller choose which of the two
	// credentials to be judged as.
	t.Run("a value shaped like both is judged as a key", func(t *testing.T) {
		both := "sk-aaaaaaaaaaaaaa.bbbbbbbbbbbbbb.cccccccccccccc"
		if !isIAMApiKey(both) || !isJwtToken(both) {
			t.Fatalf("fixture is not shaped like both: key=%v jwt=%v", isIAMApiKey(both), isJwtToken(both))
		}
		if u := zapPrincipal("Bearer " + both); u != nil {
			t.Errorf("resolved to %+v; an unknown key must name nobody", u)
		}
	})
}
