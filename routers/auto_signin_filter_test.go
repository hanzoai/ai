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

package routers

import "testing"

// TestIsOpaqueAPIKey_hkStillAccepted is a REGRESSION GUARD, not a formality.
//
// IAM stopped minting hk- in v1.33.9: a credential is now pk- (the publishable
// lookup handle) or sk- (the secret half), and the third prefix is gone. The
// tempting next step is to delete hk- here too and "finish the migration".
//
// That would be an outage. Keys issued before v1.33.9 are still live, and they
// are resolved by exact value (store.UserByAccessKey), never by prefix — so the
// only thing standing between an existing customer key and a silent 401 is that
// this predicate still recognises it as an opaque key rather than treating it as
// a legacy MD5 access token.
//
// Delete this test only when no hk- key remains in the store.
func TestIsOpaqueAPIKey_hkStillAccepted(t *testing.T) {
	for _, tok := range []string{
		"hk-2ff139c7-4dd5-4f23-9de1-df7b67331b6e", // pre-v1.33.9 shape
		"hk-live-deadbeef",                        // keys.Mint shape
	} {
		if !isOpaqueAPIKey(tok) {
			t.Fatalf("isOpaqueAPIKey(%q) = false; legacy hk- keys are still live and "+
				"must not fall through to the legacy MD5 access-token path", tok)
		}
	}
}

func TestIsOpaqueAPIKey(t *testing.T) {
	cases := []struct {
		token string
		want  bool
		why   string
	}{
		{"pk-live-abc123", true, "publishable half"},
		{"sk-live-abc123", true, "secret half"},
		{"sk-test-abc123", true, "test-env secret"},
		{"hk-live-abc123", true, "legacy, still accepted"},

		{"", false, "empty is not a key"},
		{"eyJhbGciOiJIUzI1NiJ9.e30.x", false, "JWT is handled by isJwtLike, not here"},
		{"0123456789abcdef0123456789abcdef", false, "legacy MD5 access token"},
		{"pk", false, "prefix without the hyphen is not a key"},
		{"Bearer sk-live-abc", false, "the scheme must already be stripped"},
		{"xpk-live-abc", false, "prefix must be at the START, not anywhere"},
	}
	for _, c := range cases {
		if got := isOpaqueAPIKey(c.token); got != c.want {
			t.Errorf("isOpaqueAPIKey(%q) = %v, want %v (%s)", c.token, got, c.want, c.why)
		}
	}
}
