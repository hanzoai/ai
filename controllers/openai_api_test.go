// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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

package controllers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestGetUserByAccessKeyUsesCanonicalPath locks the regression that broke
// API-key resolution: the lookup MUST hit IAM's canonical /v1/iam/users/get,
// never the legacy /api/get-user (which the @hanzo/id SPA ingress serves as
// HTML, producing "invalid character '<'" on JSON decode). It also asserts the
// resolved org owner is parsed back out.
func TestGetUserByAccessKeyUsesCanonicalPath(t *testing.T) {
	var gotPath, gotAccessKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAccessKey = r.URL.Query().Get("accessKey")
		w.Header().Set("Content-Type", "application/json")
		// Mirror the principal door's shape: status:ok + data:{owner,name}. It
		// kept the original envelope when it was re-homed, so unlike the record
		// routes it is still {status, data} and not the bare object.
		_, _ = w.Write([]byte(`{"status":"ok","msg":"","data":{"owner":"maxpower","name":"maxpower"}}`))
	}))
	defer srv.Close()

	os.Setenv("IAM_URL", srv.URL)
	defer os.Unsetenv("IAM_URL")
	// The resolver authenticates as a confidential app (client_secret_basic) and
	// fails closed without a credential — see TestGetUserByAccessKeyUsesClientSecretBasic.
	t.Setenv("IAM_CLIENT_ID", "hanzo-cloud")
	t.Setenv("IAM_CLIENT_SECRET", "s3cr3t")

	user, err := getUserByAccessKey("sk-canonical-test")
	if err != nil {
		t.Fatalf("getUserByAccessKey returned error: %v", err)
	}
	// Key resolution has its OWN door. It used to ride on the user read as
	// `get-user?accessKey=`, which reached an authentication boundary through a
	// CRUD verb whose target was a credential rather than the owner/name that a
	// read authorizes on. That verb is gone from IAM's router: asking any
	// address but this one resolves no key at all, and every gateway sk- fails
	// closed — which is why this is an equality and not a pattern.
	if gotPath != "/v1/iam/keys/principal" {
		t.Errorf("IAM path = %q, want /v1/iam/keys/principal", gotPath)
	}
	if strings.HasPrefix(gotPath, "/api/") {
		t.Errorf("IAM path %q uses the legacy /api/ alias — forbidden, breaks key resolution via SPA ingress", gotPath)
	}
	if gotAccessKey != "sk-canonical-test" {
		t.Errorf("accessKey query = %q, want sk-canonical-test", gotAccessKey)
	}
	if user == nil || user.Owner != "maxpower" {
		t.Errorf("resolved user owner = %+v, want owner=maxpower (the billing org)", user)
	}
}

// ── /v1/models is PUBLIC, and does not pretend otherwise ─────────────────────

// The catalogue answers every caller, including one carrying no credential at all.
//
// It used to hold a gate that authenticated NOBODY: it 401'd an absent credential and
// a malformed one, then admitted any string shaped like a key. Verified against
// production before this change: `Bearer sk-` + 36 zeroes returned 200, a 3.8-day
// expired JWT returned 200, and only `Bearer totally-bogus` and a missing header were
// refused. That is a shape check, not authentication — and because /v1/models is the
// natural "is my auth working?" probe, answering 200 to a dead credential sent people
// debugging the wrong system.
//
// A public endpoint must not APPEAR to validate. These cases pin that it no longer
// does: same status, same catalogue, whatever the header says.
func TestListModels_IsPublicAndNeverValidates(t *testing.T) {
	for _, tc := range []struct{ name, auth string }{
		{"no credential at all", ""},
		{"a key that was never minted", "Bearer sk-000000000000000000000000000000000000"},
		{"a syntactically broken token", "Bearer totally-bogus"},
		{"an expired JWT", "Bearer eyJhbGciOiJIUzI1NiJ9.eyJleHAiOjF9.c2ln"},
		{"not even a Bearer", "Basic dXNlcjpwYXNz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := presenting(visit(http.MethodGet, "/v1/models"), tc.auth)
			c.ListModels()
			if answered(c) != http.StatusOK {
				t.Fatalf("status = %d, want 200 — the catalogue is public: %s", answered(c), sent(c))
			}
			body := sent(c)
			if strings.Contains(body, "authentication_error") || strings.Contains(body, "unauthorized") {
				t.Fatalf("a public catalogue answered with an auth error: %s", body)
			}
			if !strings.Contains(body, `"data"`) {
				t.Fatalf("no catalogue in the response: %s", body)
			}
		})
	}
}

// ── a refusal a holder can act on ────────────────────────────────────────────

// IAM answers every unresolvable key with "the entity does not exist". Relaying that
// verbatim told a user whose key had been REVOKED that their entity was gone, sending
// them after a deleted organization instead of minting a new key. Each reason now has
// exactly one cure — and none of them echoes the credential.
func TestKeyRefusal_NamesTheCauseAndTheCure(t *testing.T) {
	const key = "sk-902abd8e-dead-beef-cafe-000000000000"

	for _, tc := range []struct {
		code  string
		wants []string
	}{
		// The cure, and NOT a cause: this code is the same answer for a revoked
		// key, a replaced one, and one that never resolved at all, so a sentence
		// naming any of them would be a guess the resolver cannot support.
		{"key_unknown", []string{"does not resolve", "mint a new one"}},
		{"key_wrong_door", []string{"not a secret key", "sk-"}},
		{"key_expired", []string{"expired", "mint a new one"}},
		{"key_not_publishable", []string{"not a publishable key"}},
		{"key_foreign_user", []string{"refused", "key_foreign_user"}},
	} {
		err := keyRefusal(tc.code, "the entity does not exist", key)
		if err == nil {
			t.Fatalf("%s: no error", tc.code)
		}
		got := err.Error()
		for _, want := range tc.wants {
			// Case-insensitive: these assert the SUBSTANCE of the advice, not its
			// sentence position.
			if !strings.Contains(strings.ToLower(got), strings.ToLower(want)) {
				t.Errorf("%s: %q does not mention %q", tc.code, got, want)
			}
		}
		// The generic sentence must be GONE, not merely decorated.
		if strings.Contains(got, "the entity does not exist") {
			t.Errorf("%s: still relays IAM's generic sentence: %q", tc.code, got)
		}
		// NEVER the credential — only its prefix.
		if strings.Contains(got, key) || strings.Contains(got, "dead-beef") {
			t.Fatalf("%s: SECRET LEAK — the refusal echoed the key: %q", tc.code, got)
		}
		if !strings.Contains(got, "sk-902abd…") {
			t.Errorf("%s: %q does not name WHICH key failed", tc.code, got)
		}
	}

	// An IAM that sends no code (older build) still relays what it said, rather than
	// this build inventing a cause it cannot know.
	if got := keyRefusal("", "the entity does not exist", key).Error(); got != "IAM error: the entity does not exist" {
		t.Errorf("no-code refusal = %q, want the relayed IAM message", got)
	}
}

// The reason reaches keyRefusal from IAM's envelope — the field cloud and ai both
// used to drop on the floor.
func TestGetUserByAccessKey_SurfacesTheRefusalCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"error","msg":"the entity does not exist","code":"key_unknown"}`))
	}))
	defer srv.Close()

	t.Setenv("IAM_URL", srv.URL)
	t.Setenv("IAM_CLIENT_ID", "hanzo-cloud")
	t.Setenv("IAM_CLIENT_SECRET", "s3cr3t")

	_, err := getUserByAccessKey("sk-902abd8e-dead-beef-cafe-000000000000")
	if err == nil {
		t.Fatal("an unresolvable key must still fail")
	}
	if !strings.Contains(err.Error(), "does not resolve") || !strings.Contains(err.Error(), "mint a new one") {
		t.Fatalf("error = %q, want the actionable key_unknown message", err)
	}
	if strings.Contains(err.Error(), "dead-beef") {
		t.Fatalf("SECRET LEAK: %q", err)
	}
}

// A key wearing a prefix this estate does not mint gets the SAME answer as one that
// was revoked, and that is the point.
//
// Two shapes exist: pk-, which identifies an org and authenticates nobody, and sk-,
// which authenticates. Everything else — a retired prefix, a typo, a key from another
// vendor — answers to no live credential, so IAM refuses it as key_unknown rather than
// inventing a family for each spelling. What the holder needs is not a taxonomy of how
// their string is wrong; it is the one sentence that ends the problem: mint a new key.
//
// This pins that the retired hk- spelling gets exactly that sentence, and NOT the bare
// "IAM error: the entity does not exist" relay, which sent people looking for a deleted
// organization.
func TestGetUserByAccessKey_RetiredPrefixGetsTheActionableRefusal(t *testing.T) {
	const key = "hk-2ff139c7-4dd5-4f23-9de1-df7b67331b6e"

	var gotAccessKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccessKey = r.URL.Query().Get("accessKey")
		w.Header().Set("Content-Type", "application/json")
		// What IAM answers for any shape it does not mint (store.UserByAccessKey).
		_, _ = w.Write([]byte(`{"status":"error","msg":"the entity does not exist","code":"key_unknown"}`))
	}))
	defer srv.Close()

	t.Setenv("IAM_URL", srv.URL)
	t.Setenv("IAM_CLIENT_ID", "hanzo-cloud")
	t.Setenv("IAM_CLIENT_SECRET", "s3cr3t")

	user, err := getUserByAccessKey(key)
	if err == nil || user != nil {
		t.Fatalf("getUserByAccessKey(%q) = (%v, %v); a retired prefix must never resolve", keyHint(key), user, err)
	}
	if gotAccessKey != key {
		t.Fatalf("IAM was asked for %q, want the presented key — the refusal must come from IAM, not a local shape test", gotAccessKey)
	}

	got := err.Error()
	for _, want := range []string{"does not resolve", "mint a new one"} {
		if !strings.Contains(got, want) {
			t.Errorf("refusal %q does not say %q — the holder is left without the cure", got, want)
		}
	}
	// The generic relay is what this replaced; seeing it again means the code fell
	// through to the unknown-code branch instead of naming the cause.
	if strings.Contains(got, "IAM error:") || strings.Contains(got, "the entity does not exist") {
		t.Errorf("refusal %q is the generic IAM relay, not an actionable message", got)
	}
	// Only the prefix, never the key.
	if strings.Contains(got, key) || strings.Contains(got, "2ff139c7") {
		t.Fatalf("SECRET LEAK — the refusal echoed the key: %q", got)
	}
}

// KeyHint discloses a prefix and nothing more.
func TestKeyHint_Redacts(t *testing.T) {
	if got := keyHint("sk-902abd8e-dead-beef-cafe-000000000000"); got != "sk-902abd…" {
		t.Fatalf("keyHint = %q, want sk-902abd…", got)
	}
	for _, short := range []string{"", "sk-", "sk-abc"} {
		if got := keyHint(short); got != "…" {
			t.Errorf("keyHint(%q) = %q, want …", short, got)
		}
	}
}

// IAM can answer "ok" and name nobody. That used to return (nil, nil) — no error
// and no principal — so the caller fell through to a bare "invalid API key" with
// nothing behind it: the one message a holder cannot act on and an operator
// cannot trace, identical whether the key was deleted, the envelope changed, or
// the owner was removed. A key that resolves to nobody is unusable, so it must be
// refused with a reason and the redacted prefix that names which credential.
func TestOkWithNoUserIsAnActionableRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","msg":"","data":null}`))
	}))
	defer srv.Close()

	os.Setenv("IAM_URL", srv.URL)
	defer os.Unsetenv("IAM_URL")
	t.Setenv("IAM_CLIENT_ID", "hanzo-cloud")
	t.Setenv("IAM_CLIENT_SECRET", "s3cr3t")

	user, err := getUserByAccessKey("sk-live-abcdef0123456789")
	if user != nil {
		t.Fatalf("user = %+v, want nil", user)
	}
	if err == nil {
		t.Fatal("err = nil; ok-with-no-user must refuse, not return a nil user with no error")
	}
	msg := err.Error()
	if !strings.Contains(msg, keysURL) {
		t.Errorf("refusal does not say what to do: %q", msg)
	}
	if !strings.Contains(msg, "resolved to no user") {
		t.Errorf("refusal does not say what happened: %q", msg)
	}
	// The credential is named by prefix, never in full.
	if strings.Contains(msg, "abcdef0123456789") {
		t.Errorf("refusal leaked the key: %q", msg)
	}
}

// Every key refusal names a cure, and the cure has to be a page that answers. These
// messages used to send holders to cloud.hanzo.ai/keys — measured 404, because
// cloud.hanzo.ai is the product site and the console is its own host with the key
// surface at /api-keys. One spelling, checked here, so the three refusals that quote
// it cannot drift apart again.
func TestKeysURL_isTheConsoleKeyPage(t *testing.T) {
	if keysURL != "https://console.hanzo.ai/api-keys" {
		t.Fatalf("keysURL = %q, which is not the console's key page", keysURL)
	}
	for _, code := range []string{"key_unknown", "key_expired"} {
		msg := keyRefusal(code, "the entity does not exist", "sk-live-abcdef123456").Error()
		if !strings.Contains(msg, keysURL) {
			t.Errorf("%s refusal does not name where to mint a key: %q", code, msg)
		}
	}
}
