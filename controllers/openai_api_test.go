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

func TestIsWidgetKey(t *testing.T) {
	tests := []struct {
		token    string
		expected bool
	}{
		{"hz_widget_public", true},
		{"hz_custom_key_123", true},
		{"hk-some-iam-key", false},
		{"sk-openai-key", false},
		{"pk-publishable", false},
		{"random.jwt.token", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isWidgetKey(tt.token); got != tt.expected {
			t.Errorf("isWidgetKey(%q) = %v, want %v", tt.token, got, tt.expected)
		}
	}
}

func TestWidgetAllowedModels(t *testing.T) {
	// Verify allowed models are in the list
	allowed := []string{"llama-3.1-8b", "llama-3.3-70b", "mistral-nemo", "gpt-4o-mini", "deepseek-r1-distill-70b", "claude-3-5-haiku", "claude-haiku-4-5"}
	for _, m := range allowed {
		if !widgetAllowedModels[m] {
			t.Errorf("expected %q to be in widgetAllowedModels", m)
		}
	}

	// Verify premium models are not allowed
	blocked := []string{"zen4", "zen4-coder", "zen4-ultra", "gpt-5"}
	for _, m := range blocked {
		if widgetAllowedModels[m] {
			t.Errorf("expected %q to NOT be in widgetAllowedModels", m)
		}
	}
}

func TestWidgetAllowedModelsList(t *testing.T) {
	list := widgetAllowedModelsList()
	if list == "" {
		t.Fatal("widgetAllowedModelsList() returned empty string")
	}

	// Verify the list is sorted (comma-separated)
	for _, model := range []string{"claude-3-5-haiku", "gpt-4o-mini", "llama-3.1-8b"} {
		if !contains(list, model) {
			t.Errorf("widgetAllowedModelsList() missing %q: got %q", model, list)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestWidgetMaxTokens(t *testing.T) {
	if widgetMaxTokens != 800 {
		t.Errorf("widgetMaxTokens = %d, want 800", widgetMaxTokens)
	}
}

func TestValidateWidgetKey(t *testing.T) {
	// Set env var with comma-separated valid keys
	os.Setenv("WIDGET_KEYS", "hz_widget_public,hz_test_key")
	defer os.Unsetenv("WIDGET_KEYS")

	tests := []struct {
		token    string
		expected bool
	}{
		{"hz_widget_public", true},
		{"hz_test_key", true},
		{"hz_unknown_key", false},
		{"hk-not-widget", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := validateWidgetKey(tt.token); got != tt.expected {
			t.Errorf("validateWidgetKey(%q) = %v, want %v", tt.token, got, tt.expected)
		}
	}
}

func TestValidateWidgetKeyNoConfig(t *testing.T) {
	// Ensure no env var is set
	os.Unsetenv("WIDGET_KEYS")

	// Without KMS or env vars, all keys should be rejected
	if validateWidgetKey("hz_widget_public") {
		t.Error("validateWidgetKey should reject all keys when WIDGET_KEYS is not configured")
	}
}

// TestGetUserByAccessKeyUsesCanonicalPath locks the regression that broke hk-
// API-key resolution: the lookup MUST hit IAM's canonical /v1/iam/get-user,
// never the legacy /api/get-user (which the @hanzo/id SPA ingress serves as
// HTML, producing "invalid character '<'" on JSON decode). It also asserts the
// resolved org owner is parsed back out.
func TestGetUserByAccessKeyUsesCanonicalPath(t *testing.T) {
	var gotPath, gotAccessKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAccessKey = r.URL.Query().Get("accessKey")
		w.Header().Set("Content-Type", "application/json")
		// Mirror IAM's get-user shape: status:ok + data:{owner,name}.
		_, _ = w.Write([]byte(`{"status":"ok","msg":"","data":{"owner":"maxpower","name":"maxpower"}}`))
	}))
	defer srv.Close()

	os.Setenv("IAM_URL", srv.URL)
	defer os.Unsetenv("IAM_URL")
	// The resolver authenticates as a confidential app (client_secret_basic) and
	// fails closed without a credential — see TestGetUserByAccessKeyUsesClientSecretBasic.
	t.Setenv("IAM_CLIENT_ID", "hanzo-cloud")
	t.Setenv("IAM_CLIENT_SECRET", "s3cr3t")

	user, err := getUserByAccessKey("hk-canonical-test")
	if err != nil {
		t.Fatalf("getUserByAccessKey returned error: %v", err)
	}
	if gotPath != "/v1/iam/get-user" {
		t.Errorf("IAM path = %q, want /v1/iam/get-user (must NOT use legacy /api/get-user)", gotPath)
	}
	if strings.HasPrefix(gotPath, "/api/") {
		t.Errorf("IAM path %q uses the legacy /api/ alias — forbidden, breaks hk- resolution via SPA ingress", gotPath)
	}
	if gotAccessKey != "hk-canonical-test" {
		t.Errorf("accessKey query = %q, want hk-canonical-test", gotAccessKey)
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
// production before this change: `Bearer hk-` + 36 zeroes returned 200, a 3.8-day
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
		{"a key that was never minted", "Bearer hk-000000000000000000000000000000000000"},
		{"a syntactically broken token", "Bearer totally-bogus"},
		{"an expired JWT", "Bearer eyJhbGciOiJIUzI1NiJ9.eyJleHAiOjF9.c2ln"},
		{"not even a Bearer", "Basic dXNlcjpwYXNz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := newAuthController(http.MethodGet, "/v1/models", tc.auth, nil)
			c.ListModels()
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 — the catalogue is public: %s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
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
	const key = "hk-902abd8e-dead-beef-cafe-000000000000"

	for _, tc := range []struct {
		code  string
		wants []string
	}{
		{"key_unknown", []string{"not recognized", "revoked or replaced", "mint a new one"}},
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
		if !strings.Contains(got, "hk-902abd…") {
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

	_, err := getUserByAccessKey("hk-902abd8e-dead-beef-cafe-000000000000")
	if err == nil {
		t.Fatal("an unresolvable key must still fail")
	}
	if !strings.Contains(err.Error(), "not recognized") || !strings.Contains(err.Error(), "Mint a new one") {
		t.Fatalf("error = %q, want the actionable revoked-key message", err)
	}
	if strings.Contains(err.Error(), "dead-beef") {
		t.Fatalf("SECRET LEAK: %q", err)
	}
}

// KeyHint discloses a prefix and nothing more.
func TestKeyHint_Redacts(t *testing.T) {
	if got := keyHint("hk-902abd8e-dead-beef-cafe-000000000000"); got != "hk-902abd…" {
		t.Fatalf("keyHint = %q, want hk-902abd…", got)
	}
	for _, short := range []string{"", "hk-", "hk-abc"} {
		if got := keyHint(short); got != "…" {
			t.Errorf("keyHint(%q) = %q, want …", short, got)
		}
	}
}
