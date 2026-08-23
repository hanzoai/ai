// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"strings"
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"
)

// A publishable key names an ORG and never a person.
//
// That is the whole reason a pk- is safe to put in a browser: it says which org
// an ingest belongs to and carries no authority to act as anybody. The endpoint
// that answers it is a different one from the secret key's, and each refuses the
// other's key — so a pk- cannot authenticate a request even if a caller sends it
// where an sk- belongs.
func TestAPublishableKeyNamesAnOrgAndNeverAPerson(t *testing.T) {
	iamd := withIAM(t)
	key := iamd.asOrg(t, "acme")

	org, err := resolveOrgFromPublishableKey(key)
	if err != nil {
		t.Fatalf("resolveOrgFromPublishableKey: %v", err)
	}
	if org != "acme" {
		t.Errorf("org = %q, want acme", org)
	}

	// The same key at the secret endpoint names nobody, and says why.
	if u := zapPrincipal("Bearer " + key); u != nil {
		t.Errorf("a publishable key resolved to a person: %+v", u)
	}
}

// And a secret key is refused at the publishable endpoint, so the two are not
// interchangeable in either direction.
func TestASecretKeyIsRefusedAtThePublishableDoor(t *testing.T) {
	iamd := withIAM(t)
	secret := strings.TrimPrefix(iamd.asUser(t, &iam.User{Owner: "acme", Name: "alice"}), "Bearer ")

	org, err := resolveOrgFromPublishableKey(secret)
	if err == nil {
		t.Fatalf("a secret key resolved to org %q at the publishable endpoint", org)
	}
	// The refusal says which key it is and what to use instead, because being
	// told "does not resolve" for a key that resolves perfectly well at the other
	// endpoint sends somebody to mint a replacement they do not need.
	if !strings.Contains(err.Error(), "publishable") {
		t.Errorf("refusal = %q, want it to name the two kinds of key", err)
	}
}

// A key IAM does not know is refused, and an unconfigured IAM refuses too rather
// than resolving to an empty org — a caller must never end up acting for "".
func TestAnUnknownKeyAndAnAbsentIAMBothRefuse(t *testing.T) {
	iamd := withIAM(t)
	if org, err := resolveOrgFromPublishableKey("pk-never-issued"); err == nil {
		t.Errorf("an unknown key resolved to %q", org)
	}
	_ = iamd

	t.Setenv("IAM_URL", "")
	if org, err := resolveOrgFromPublishableKey("pk-anything"); err == nil {
		t.Errorf("with no IAM configured the key resolved to %q", org)
	}
}

// What a refused key is TOLD. Each code IAM can answer has its own sentence, and
// they differ because the cure differs.
func TestARefusedKeyIsToldWhichRefusalItIs(t *testing.T) {
	for _, tc := range []struct {
		code string
		says []string
	}{
		{"key_unknown", []string{"does not resolve", "mint a new one"}},
		{"key_wrong_door", []string{"not a secret key", "publishable", "sk-"}},
		{"key_expired", []string{"expired", "mint a new one"}},
	} {
		t.Run(tc.code, func(t *testing.T) {
			err := keyRefusal(tc.code, "", "sk-abcdefghijklmnop")
			if err == nil {
				t.Fatalf("%s produced no refusal", tc.code)
			}
			for _, want := range tc.says {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal for %s = %q, want it to mention %q", tc.code, err, want)
				}
			}
		})
	}

	// A code this does not know must still refuse, and must not borrow another
	// code's sentence — being told to mint a new key for a fault that is not the
	// key's is a wasted rotation.
	other := keyRefusal("something_new", "IAM said something else", "sk-abcdefghijklmnop")
	if other == nil {
		t.Fatal("an unrecognised code produced no refusal")
	}
	if strings.Contains(other.Error(), "mint a new one") {
		t.Errorf("an unrecognised code borrowed the unknown-key sentence: %q", other)
	}
}

// Which credential a request is read as, when it carries two.
//
// The header wins and the cookie is the fallback, and that order is the point: a
// cookie outlives the tab it was set in, so a request that states a credential
// explicitly must not be answered as whoever the browser last signed in as.
func TestAnExplicitCredentialBeatsTheCookie(t *testing.T) {
	for _, tc := range []struct{ what, header, cookie, want string }{
		{"the header alone", "Bearer sk-header", "", "sk-header"},
		{"the cookie alone", "", "cookie-token", "cookie-token"},
		{"both, and the header wins", "Bearer sk-header", "cookie-token", "sk-header"},
		{"neither", "", "", ""},
		{"a header that is not a bearer falls to the cookie", "Basic abc", "cookie-token", "cookie-token"},
		{"and to nothing when there is no cookie", "Basic abc", "", ""},
	} {
		t.Run(tc.what, func(t *testing.T) {
			if got := bearerToken(tc.header, tc.cookie); got != tc.want {
				t.Errorf("bearerToken(%q, %q) = %q, want %q", tc.header, tc.cookie, got, tc.want)
			}
		})
	}
}
