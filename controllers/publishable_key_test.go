// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"net/http"
	"net/http/httptest"
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

// The publishable resolver asks IAM at ONE address, and it is not a verb.
//
// This is the dual of TestGetUserByAccessKeyUsesCanonicalPath. Key resolution
// used to ride on a verb-noun address; IAM answers 410 there now, and the two
// kinds of key were split onto endpoints of their own — a secret names a
// principal, a publishable names only an org. Asking anywhere else resolves no
// key at all, and a pk- that resolves to nothing bills nobody, so this is an
// equality and not a pattern.
func TestResolveOrgFromPublishableKeyUsesCanonicalPath(t *testing.T) {
	var gotPath, gotAccessKey, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAccessKey = r.URL.Query().Get("accessKey")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","msg":"","data":{"org":"acme","scope":"ingest"}}`))
	}))
	defer srv.Close()

	t.Setenv("IAM_URL", srv.URL)
	t.Setenv("IAM_CLIENT_ID", "hanzo-cloud")
	t.Setenv("IAM_CLIENT_SECRET", "s3cr3t")

	org, err := resolveOrgFromPublishableKey("pk-canonical-test")
	if err != nil {
		t.Fatalf("resolveOrgFromPublishableKey: %v", err)
	}
	if gotPath != "/v1/iam/keys/org" {
		t.Errorf("IAM path = %q, want /v1/iam/keys/org", gotPath)
	}
	if strings.HasPrefix(gotPath, "/api/") {
		t.Errorf("IAM path %q uses the legacy /api/ alias", gotPath)
	}
	if gotAccessKey != "pk-canonical-test" {
		t.Errorf("accessKey query = %q, want pk-canonical-test", gotAccessKey)
	}
	// Resolving a key is a credential boundary IAM opens only to a confidential
	// app, and it reads that from Basic. A bearer resolves to a principal holding
	// no such capability, so the answer would be a refusal rather than an org.
	if !strings.HasPrefix(gotAuth, "Basic ") {
		t.Errorf("Authorization = %q, want client_secret_basic", gotAuth)
	}
	if org != "acme" {
		t.Errorf("org = %q, want acme", org)
	}
}

// An IAM that cannot answer must not resolve to an org — including the second
// time it is asked.
//
// A pk- names a tenant, so when IAM cannot confirm WHICH, the honest answer is
// none: GetOrg returns "" and the request is refused rather than scoped to the
// deployment's own org, which would bill a customer's traffic to the platform.
// The answer is CACHED, so the refusal has to be remembered AS a refusal — a
// cached ("", nil) would turn one IAM hiccup into a lasting empty org that every
// later request reads as an answer.
func TestAPublishableKeyRefusedByIAMNeverResolvesToAnOrg(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"status":"error","msg":"server_error"}`))
	}))
	defer srv.Close()

	t.Setenv("IAM_URL", srv.URL)
	t.Setenv("IAM_CLIENT_ID", "hanzo-cloud")
	t.Setenv("IAM_CLIENT_SECRET", "s3cr3t")

	const key = "pk-refused-by-iam-0001"
	for _, when := range []string{"first ask", "from the cache"} {
		org, err := publishableOrg(key)
		if err == nil {
			t.Fatalf("%s: a refused key resolved to %q", when, org)
		}
		if org != "" {
			t.Errorf("%s: refusal carried org %q, want none", when, org)
		}
	}
}

// A shape that is not a publishable key never reaches IAM at all, so a public
// endpoint cannot be used to aim traffic at the one service every other
// credential path also depends on.
func TestAKeyOfTheWrongShapeNeverReachesIAM(t *testing.T) {
	asked := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","data":{"org":"acme"}}`))
	}))
	defer srv.Close()

	t.Setenv("IAM_URL", srv.URL)
	t.Setenv("IAM_CLIENT_ID", "hanzo-cloud")
	t.Setenv("IAM_CLIENT_SECRET", "s3cr3t")

	for _, bad := range []string{"", "sk-0123456789abcdef", "not-a-key", "pk-"} {
		if org, err := publishableOrg(bad); err == nil {
			t.Errorf("%q resolved to org %q", bad, org)
		}
	}
	if asked {
		t.Error("a malformed key was sent to IAM")
	}
}

// A shape that is not a secret key never reaches IAM either — the dual of
// TestAKeyOfTheWrongShapeNeverReachesIAM, at the resolver every generative call
// arrives through.
//
// A secret key rides on the endpoints the whole internet can reach, so whatever is
// sent lands here. IAM mints "sk-{live|test}-{random}", and a string outside that
// alphabet and length is not a key this estate ever issued: it is settled in this
// process rather than turned into a lookup aimed at the one service every other
// credential path also depends on.
func TestASecretOfTheWrongShapeNeverReachesIAM(t *testing.T) {
	asked := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","data":{"owner":"acme","name":"alice"}}`))
	}))
	defer srv.Close()

	t.Setenv("IAM_URL", srv.URL)
	t.Setenv("IAM_CLIENT_ID", "hanzo-cloud")
	t.Setenv("IAM_CLIENT_SECRET", "s3cr3t")

	bad := []string{
		"",
		"sk-",
		"sk-short",                          // inside the floor's length
		"not-a-key",                         // no prefix at all
		"pk-live-0123456789abcdef01234567",  // the other half's key, at this door
		"sk-live-0123456789abcdef 01234567", // a space is not in the alphabet
		"sk-live-" + strings.Repeat("a", 200),
	}
	for _, key := range bad {
		if u, err := getUserByAccessKey(key); err == nil {
			t.Errorf("%q resolved to %+v", key, u)
		}
	}
	if asked {
		t.Error("a malformed key was sent to IAM")
	}
}

// AND THE FLOOR PASSES WHAT IAM MINTS.
//
// A floor that refused everything would satisfy the test above while resolving no
// key at all, so the shape IAM actually issues is asked about here and has to reach
// the endpoint. keys.Mint spells it "sk-{live|test}-{16 random bytes, hex}".
func TestAMintedSecretReachesIAM(t *testing.T) {
	asked := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","data":{"owner":"acme","name":"alice"}}`))
	}))
	defer srv.Close()

	t.Setenv("IAM_URL", srv.URL)
	t.Setenv("IAM_CLIENT_ID", "hanzo-cloud")
	t.Setenv("IAM_CLIENT_SECRET", "s3cr3t")

	for _, env := range []string{"live", "test"} {
		key := "sk-" + env + "-" + strings.Repeat("0f", 16)
		u, err := getUserByAccessKey(key)
		if err != nil {
			t.Fatalf("%q: %v", key, err)
		}
		if u == nil || u.Owner != "acme" {
			t.Fatalf("%q resolved to %+v, want acme/alice", key, u)
		}
	}
	if !asked {
		t.Error("a minted key never reached IAM")
	}
}
