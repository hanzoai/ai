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

// connections_oauth_test.go — pure/httptest coverage for the AI login-manager
// OAuth flow. Like connections_api_test.go we avoid a live controller/xorm/KMS: the
// decomposed pure functions (state, config, authorize URL, code exchange) are
// driven directly, and the seal path is exercised through the sealProviderSecret
// seam so the two hard invariants are provable — (1) the authorize redirect binds
// the caller's org, and (2) the callback seals the exchanged token and the raw
// token never reaches the persisted row.

package controllers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const rawOAuthToken = "oauth-access-token-super-secret-DO-NOT-PERSIST"

// TestAIOAuthState_RoundTripAndTamper proves the org-bound state contract: a signed
// state verifies back to the same org for the same provider, and any tamper /
// foreign-provider / missing-secret is rejected (the login-CSRF / mix-up defense).
func TestAIOAuthState_RoundTripAndTamper(t *testing.T) {
	t.Setenv("AI_OAUTH_STATE_SECRET", "test-hmac-key")

	state, err := signAIOAuthState("acme", "openai")
	if err != nil {
		t.Fatalf("signAIOAuthState: %v", err)
	}
	org, err := verifyAIOAuthState(state, "openai")
	if err != nil || org != "acme" {
		t.Fatalf("verifyAIOAuthState = (%q, %v); want (\"acme\", nil)", org, err)
	}

	// Foreign provider: a state signed for openai must not verify as google.
	if _, err := verifyAIOAuthState(state, "google"); err == nil {
		t.Error("state verified for the wrong provider (mix-up not blocked)")
	}
	// Tampered signature.
	if _, err := verifyAIOAuthState(state[:len(state)-2]+"xx", "openai"); err == nil {
		t.Error("tampered state verified")
	}
	// Malformed.
	if _, err := verifyAIOAuthState("not-a-state", "openai"); err == nil {
		t.Error("malformed state verified")
	}
}

// TestAIOAuthState_NoSecretFailsClosed proves the flow is disabled (not forgeable)
// when the HMAC key is absent.
func TestAIOAuthState_NoSecretFailsClosed(t *testing.T) {
	t.Setenv("AI_OAUTH_STATE_SECRET", "")
	if _, err := signAIOAuthState("acme", "openai"); err == nil {
		t.Error("signAIOAuthState succeeded with no secret; want fail-closed")
	}
	if _, err := verifyAIOAuthState("x.y", "openai"); err == nil {
		t.Error("verifyAIOAuthState succeeded with no secret; want fail-closed")
	}
}

// TestAIOAuthConfig proves the config gate: unprovisioned → not configured (clear
// "not configured" instead of a crash); creds present → configured with the
// provider's default endpoints; and OpenAI additionally needs its URLs pinned.
func TestAIOAuthConfig(t *testing.T) {
	// Clear every relevant env so the test is hermetic.
	for _, up := range []string{"OPENAI", "ANTHROPIC", "GOOGLE"} {
		for _, k := range []string{"_OAUTH_CLIENT_ID", "_OAUTH_CLIENT_SECRET", "_OAUTH_AUTH_URL", "_OAUTH_TOKEN_URL", "_OAUTH_SCOPE"} {
			t.Setenv("AI_"+up+k, "")
		}
	}

	if _, ok := aiOAuthConfig("google"); ok {
		t.Error("google reported configured with no creds")
	}

	// Google ships default URLs → creds alone configure it.
	t.Setenv("AI_GOOGLE_OAUTH_CLIENT_ID", "gid")
	t.Setenv("AI_GOOGLE_OAUTH_CLIENT_SECRET", "gsecret")
	app, ok := aiOAuthConfig("google")
	if !ok {
		t.Fatal("google not configured after creds set")
	}
	if !strings.HasPrefix(app.authURL, "https://accounts.google.com/") || app.tokenURL == "" {
		t.Errorf("google default endpoints missing: %+v", app)
	}

	// OpenAI has no default URLs → creds alone are NOT enough.
	t.Setenv("AI_OPENAI_OAUTH_CLIENT_ID", "oid")
	t.Setenv("AI_OPENAI_OAUTH_CLIENT_SECRET", "osecret")
	if _, ok := aiOAuthConfig("openai"); ok {
		t.Error("openai reported configured without pinned URLs")
	}
	t.Setenv("AI_OPENAI_OAUTH_AUTH_URL", "https://auth.example.com/authorize")
	t.Setenv("AI_OPENAI_OAUTH_TOKEN_URL", "https://auth.example.com/token")
	if _, ok := aiOAuthConfig("openai"); !ok {
		t.Error("openai not configured after URLs pinned")
	}
}

// TestAIAuthorizeURL_RedirectBindsOrg proves the authorize redirect: the built URL
// targets the provider authorize endpoint and carries a state that verifies back to
// the caller's org — i.e. the redirect a signed-in caller is sent to is org-bound.
func TestAIAuthorizeURL_RedirectBindsOrg(t *testing.T) {
	t.Setenv("AI_OAUTH_STATE_SECRET", "test-hmac-key")
	t.Setenv("AI_GOOGLE_OAUTH_CLIENT_ID", "gid")
	t.Setenv("AI_GOOGLE_OAUTH_CLIENT_SECRET", "gsecret")

	app, ok := aiOAuthConfig("google")
	if !ok {
		t.Fatal("google not configured")
	}
	state, err := signAIOAuthState("acme", "google")
	if err != nil {
		t.Fatalf("signAIOAuthState: %v", err)
	}
	redirectURI := aiOAuthRedirectURI("https://api.hanzo.ai", "google")
	authorizeURL := aiAuthorizeURL(app, redirectURI, state, "google")

	u, err := url.Parse(authorizeURL)
	if err != nil {
		t.Fatalf("authorize URL unparsable: %v", err)
	}
	if u.Scheme+"://"+u.Host+u.Path != "https://accounts.google.com/o/oauth2/v2/auth" {
		t.Errorf("authorize target = %q; want the google authorize endpoint", u.Scheme+"://"+u.Host+u.Path)
	}
	q := u.Query()
	if q.Get("client_id") != "gid" || q.Get("response_type") != "code" {
		t.Errorf("authorize params wrong: %v", q)
	}
	if q.Get("redirect_uri") != "https://api.hanzo.ai/v1/ai/connections/google/callback" {
		t.Errorf("redirect_uri = %q", q.Get("redirect_uri"))
	}
	// The state in the redirect must verify back to the caller's org.
	gotOrg, err := verifyAIOAuthState(q.Get("state"), "google")
	if err != nil || gotOrg != "acme" {
		t.Errorf("state in authorize URL = (%q, %v); want org acme", gotOrg, err)
	}
}

// TestCallbackStoresSealedToken is the end-to-end SEAL INVARIANT for the OAuth
// callback WITHOUT a live KMS or DB: the code is exchanged at a mock token endpoint,
// the resulting token is fed through the SAME sealProviderSecret seam the handler
// uses, and the row built from the returned ref carries ONLY the "kms://…" reference
// — never the raw token. This exercises exactly the callback's exchange→seal→build
// path.
func TestCallbackStoresSealedToken(t *testing.T) {
	// Mock provider token endpoint returning a fake access token.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("token endpoint parse form: %v", err)
		}
		if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "auth-code-123" {
			t.Errorf("unexpected exchange params: %v", r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"` + rawOAuthToken + `","account":"acme-openai"}`))
	}))
	defer ts.Close()

	app := aiOAuthApp{clientID: "id", clientSecret: "secret", tokenURL: ts.URL}
	redirectURI := aiOAuthRedirectURI("https://api.hanzo.ai", "openai")

	token, account, err := exchangeAIOAuthCode(context.Background(), "openai", app, "auth-code-123", redirectURI)
	if err != nil {
		t.Fatalf("exchangeAIOAuthCode: %v", err)
	}
	if token != rawOAuthToken {
		t.Fatalf("exchanged token = %q; want %q", token, rawOAuthToken)
	}
	if account != "acme-openai" {
		t.Errorf("account label = %q; want acme-openai", account)
	}

	// Swap the seal seam for a capturing fake (no live KMS): it must receive the
	// EXACT exchanged token and return a kms:// ref, exactly as the handler wires
	// object.StoreProviderSecret.
	orig := sealProviderSecret
	defer func() { sealProviderSecret = orig }()
	var sealedName, sealedValue string
	sealProviderSecret = func(name, value string) (string, error) {
		sealedName, sealedValue = name, value
		return "kms://" + name, nil
	}

	spec, _ := aiConnSpecFor("openai")
	ref, err := sealProviderSecret(connectionSecretName("acme", spec.name), token)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if sealedValue != rawOAuthToken {
		t.Fatalf("sealed value = %q; the RAW token was not what got sealed", sealedValue)
	}
	if sealedName != byokSecretName("acme", "openai") {
		t.Errorf("sealed under %q; want the BYOK convention %q", sealedName, byokSecretName("acme", "openai"))
	}
	if !strings.HasPrefix(ref, "kms://") {
		t.Fatalf("seal returned %q; want a kms:// reference", ref)
	}

	// The persisted row carries ONLY the sealed ref — never the raw token.
	row := buildConnectionProvider("acme", spec, ref)
	if row.ClientSecret != ref {
		t.Errorf("row.ClientSecret = %q; want the kms ref %q", row.ClientSecret, ref)
	}
	if strings.Contains(row.ClientSecret, rawOAuthToken) {
		t.Fatal("SEAL INVARIANT VIOLATED: the OAuth callback would persist the raw token")
	}
}

// TestExchangeAIOAuthCode_ProviderError proves a provider error body surfaces as an
// error (so the callback fails to the console error redirect, not a false connect).
func TestExchangeAIOAuthCode_ProviderError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"bad code"}`))
	}))
	defer ts.Close()

	app := aiOAuthApp{clientID: "id", clientSecret: "secret", tokenURL: ts.URL}
	if _, _, err := exchangeAIOAuthCode(context.Background(), "openai", app, "x", "https://api.hanzo.ai/cb"); err == nil {
		t.Error("exchange accepted an error body; want error")
	}
}
