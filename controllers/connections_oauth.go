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

// connections_oauth.go — the OAuth half of the AI login-manager: connect a
// third-party AI ACCOUNT (OpenAI / Anthropic / Google) by signing in to the
// provider instead of pasting an API key. The token the provider returns is
// sealed into KMS and the org's provider row is upserted through the EXACT same
// path AddAIConnection (connections_api.go) uses for a BYOK key — sealProviderSecret
// then persistConnection — so a raw token never lands in the DB or a log line and a
// connection made either way is byte-identical in the store.
//
// SECURITY (the tenant + secret boundary), mirroring the KB connector this is
// modeled on (cloud/clients/knowledge/connectors.go):
//   - authorize resolves the org from the VERIFIED principal (requireConnectionOrg,
//     the session/JWT), binds it into a signed, expiring OAuth `state`, and hands
//     back the provider authorize URL. The org can only ever be the caller's own.
//   - callback recovers the org from the SIGNED state — NOT a header, NOT the
//     provider — so an attacker cannot bind their provider account to a victim org
//     nor steer a victim's callback to a foreign org (login-CSRF / mix-up defense).
//     That is also why the callback is reachable without a present credential
//     (routers/authz_filter.go): the state IS the authentication.
//   - the OAuth app client_id/secret are read from env (KMS-injected on the
//     deployment) per provider; an unprovisioned provider yields a clear
//     "not configured", never a crash.

package controllers

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/ai/object"
)

// sealProviderSecret is the ONE seal path shared by the BYOK (AddAIConnection) and
// OAuth (CallbackAIProvider) connect flows: it seals a tenant token into KMS and
// returns its "kms://…" reference. A package var so tests can substitute a
// capturing fake without a live KMS; it defaults to object.StoreProviderSecret,
// which FAILS CLOSED when KMS is unconfigured (never stores plaintext).
var sealProviderSecret = object.StoreProviderSecret

// oauthHTTP bounds the token-exchange round-trip so a hung provider endpoint can't
// wedge a callback.
var oauthHTTP = &http.Client{Timeout: 15 * time.Second}

// aiOAuthApp is a provider's OAuth application config, resolved from env at use.
// The client secret is KMS-injected and never logged.
type aiOAuthApp struct {
	clientID     string
	clientSecret string
	authURL      string
	tokenURL     string
	scope        string
}

// aiOAuthConfig returns the provider's OAuth app config and ok=true only when it is
// FULLY provisioned for this deployment (client_id + client_secret + authorize URL +
// token URL all present). Creds come from env (KMS-injected):
// AI_<PROVIDER>_OAUTH_CLIENT_ID / _CLIENT_SECRET. The authorize/token URLs and
// default scope are the providers' standard endpoints, each overridable via
// AI_<PROVIDER>_OAUTH_AUTH_URL / _TOKEN_URL / _SCOPE for a private OAuth app.
//
// Google and Anthropic ship known default URLs, so they need only the two creds.
// OpenAI has no stable public OAuth-for-API endpoint, so its URLs default empty and
// an operator must set AI_OPENAI_OAUTH_AUTH_URL / _TOKEN_URL as well — until then it
// is reported "not configured" rather than guessing an endpoint.
func aiOAuthConfig(provider string) (aiOAuthApp, bool) {
	up := strings.ToUpper(strings.TrimSpace(provider))
	app := aiOAuthApp{
		clientID:     strings.TrimSpace(os.Getenv("AI_" + up + "_OAUTH_CLIENT_ID")),
		clientSecret: strings.TrimSpace(os.Getenv("AI_" + up + "_OAUTH_CLIENT_SECRET")),
	}
	switch strings.ToLower(provider) {
	case "google":
		app.authURL = "https://accounts.google.com/o/oauth2/v2/auth"
		app.tokenURL = "https://oauth2.googleapis.com/token"
		app.scope = "https://www.googleapis.com/auth/generative-language.retriever"
	case "anthropic":
		app.authURL = "https://claude.ai/oauth/authorize"
		app.tokenURL = "https://console.anthropic.com/v1/oauth/token"
		app.scope = "org:create_api_key user:profile user:inference"
	case "openai":
		// No stable public OAuth-for-API endpoint — operator must pin the URLs.
		app.authURL = ""
		app.tokenURL = ""
		app.scope = "openid profile email"
	}
	// Per-provider overrides (private OAuth app / future endpoint changes).
	if v := strings.TrimSpace(os.Getenv("AI_" + up + "_OAUTH_AUTH_URL")); v != "" {
		app.authURL = v
	}
	if v := strings.TrimSpace(os.Getenv("AI_" + up + "_OAUTH_TOKEN_URL")); v != "" {
		app.tokenURL = v
	}
	if v := strings.TrimSpace(os.Getenv("AI_" + up + "_OAUTH_SCOPE")); v != "" {
		app.scope = v
	}
	configured := app.clientID != "" && app.clientSecret != "" && app.authURL != "" && app.tokenURL != ""
	return app, configured
}

// ---- OAuth state (org-bound, signed, expiring) ----

// aiOAuthStateSecret is the HMAC key for the OAuth state, from env (KMS-injected).
// Absent, the connect flow is disabled (fail closed) rather than issuing forgeable
// state.
func aiOAuthStateSecret() []byte { return []byte(os.Getenv("AI_OAUTH_STATE_SECRET")) }

// signAIOAuthState mints an opaque state binding (org, provider) with a short
// expiry, authenticated by HMAC-SHA256. The callback verifies the MAC + expiry and
// recovers the org from the SIGNED payload — so the org a callback acts on is the
// one THIS server bound at authorize time, never anything the client or provider
// sends.
func signAIOAuthState(org, provider string) (string, error) {
	secret := aiOAuthStateSecret()
	if len(secret) == 0 {
		return "", fmt.Errorf("AI_OAUTH_STATE_SECRET not set")
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	exp := time.Now().Add(10 * time.Minute).Unix()
	payload := fmt.Sprintf("%s|%s|%s|%d", org, provider, hex.EncodeToString(nonce), exp)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	sig := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(sig), nil
}

// verifyAIOAuthState checks the MAC + expiry and returns the (org) the state was
// signed for, rejecting a tampered, expired, or foreign-provider state. The
// callback then refuses, so an attacker cannot forge a state that binds their token
// to a victim org.
func verifyAIOAuthState(state, provider string) (string, error) {
	secret := aiOAuthStateSecret()
	if len(secret) == 0 {
		return "", fmt.Errorf("AI_OAUTH_STATE_SECRET not set")
	}
	parts := strings.SplitN(state, ".", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("malformed state")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", fmt.Errorf("malformed state payload")
	}
	gotSig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("malformed state sig")
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	if subtle.ConstantTimeCompare(gotSig, mac.Sum(nil)) != 1 {
		return "", fmt.Errorf("state signature mismatch")
	}
	fields := strings.Split(string(payload), "|")
	if len(fields) != 4 {
		return "", fmt.Errorf("malformed state fields")
	}
	if fields[1] != provider {
		return "", fmt.Errorf("state provider mismatch")
	}
	exp, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", fmt.Errorf("state expired")
	}
	if fields[0] == "" {
		return "", fmt.Errorf("state missing org")
	}
	return fields[0], nil
}

// ---- URLs ----

// aiOAuthRedirectURI is the deployment's own OAuth redirect URI for a provider,
// derived from a fixed, server-known origin (AI_OAUTH_REDIRECT_BASE, else the
// request origin) — never client-influenced. Providers must have this registered.
func aiOAuthRedirectURI(origin, provider string) string {
	base := strings.TrimSpace(os.Getenv("AI_OAUTH_REDIRECT_BASE"))
	if base == "" {
		base = origin
	}
	return strings.TrimRight(base, "/") + "/v1/ai/connections/" + provider + "/callback"
}

// aiAuthorizeURL builds the provider authorize URL carrying the org-bound signed
// state. It is pure so the exact redirect target (client_id, redirect_uri, scope,
// state, response_type) is unit-testable without a live handler.
func aiAuthorizeURL(app aiOAuthApp, redirectURI, state, provider string) string {
	q := url.Values{}
	q.Set("client_id", app.clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", app.scope)
	q.Set("state", state)
	q.Set("response_type", "code")
	if provider == "google" {
		// Ask Google for a refresh token so the connection survives token expiry.
		q.Set("access_type", "offline")
		q.Set("prompt", "consent")
	}
	return app.authURL + "?" + q.Encode()
}

// aiOAuthSuccessBase is the console page the callback returns the browser to after
// a connect. AI_OAUTH_SUCCESS_REDIRECT pins it; otherwise the request origin root.
func aiOAuthSuccessBase(origin string) string {
	if v := strings.TrimSpace(os.Getenv("AI_OAUTH_SUCCESS_REDIRECT")); v != "" {
		return v
	}
	return origin
}

// ---- code exchange ----

// exchangeAIOAuthCode swaps an authorization code for an access token at the
// provider's token endpoint and derives a non-secret display `account`. Returns the
// token (kept secret by the caller → KMS) and the account label. Hand-rolled over
// net/http (like the KB connector) so JSON and account derivation stay explicit.
func exchangeAIOAuthCode(ctx context.Context, provider string, app aiOAuthApp, code, redirectURI string) (token, account string, err error) {
	form := url.Values{}
	form.Set("client_id", app.clientID)
	form.Set("client_secret", app.clientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("grant_type", "authorization_code")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, app.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := oauthHTTP.Do(req)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode/100 != 2 {
		return "", "", fmt.Errorf("token endpoint status %d", resp.StatusCode)
	}

	var j struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
		Account     string `json:"account"`
		Email       string `json:"email"`
	}
	if err := json.Unmarshal(body, &j); err != nil {
		return "", "", fmt.Errorf("decode token response: %w", err)
	}
	if j.Error != "" {
		return "", "", fmt.Errorf("provider error: %s", j.Error)
	}
	if j.AccessToken == "" {
		return "", "", fmt.Errorf("no access token in response")
	}
	account = j.Account
	if account == "" {
		account = j.Email
	}
	return j.AccessToken, account, nil
}

// persistConnection upserts the org's provider row from a SEALED secret reference
// (kms://…). It is the ONE row-upsert shared by the BYOK and OAuth connect flows: on
// reconnect it preserves the row's minted ProviderKey and original CreatedTime and
// refreshes the sealed ref + reactivates. ref MUST be a kms:// reference (the caller
// gets it from sealProviderSecret) — buildConnectionProvider copies it verbatim into
// ClientSecret, so a raw key/token can never reach the row.
func persistConnection(org string, spec aiConnSpec, ref string) error {
	row := buildConnectionProvider(org, spec, ref)
	id := org + "/" + spec.name
	existing, err := object.GetProvider(id)
	if err != nil {
		return err
	}
	if existing != nil {
		row.ProviderKey = existing.ProviderKey
		row.CreatedTime = existing.CreatedTime
		if _, err := object.UpdateProvider(id, row); err != nil {
			return err
		}
	} else if _, err := object.AddProvider(row); err != nil {
		return err
	}
	object.InvalidateProviderNameCache(spec.name)
	return nil
}

// ---- handlers ----

// ConnectAIProvider begins an OAuth connection for the caller's org: it binds the
// org into a signed state and sends the caller to the provider's authorize URL. By
// default it 302-redirects (a top-level browser "connect your login" click); a
// SPA/BFF that needs to drive the redirect itself passes ?format=json and gets
// {authorizeUrl} in the standard envelope. The org is the VERIFIED principal, so
// only the caller's own connection can result.
// @Title ConnectAIProvider
// @Tag AI Connections API
// @Description begin an OAuth connection to a third-party AI account (login manager)
// @Param provider path string true "provider slug (openai|anthropic|google)"
// @Success 302 {string} string redirect to the provider authorize URL
// @router /ai/connections/:provider/authorize [get]
func (c *ApiController) ConnectAIProvider() {
	org, ok := c.requireConnectionOrg()
	if !ok {
		return
	}
	provider := strings.ToLower(c.Ctx.Input.Param(":provider"))
	spec, ok := aiConnSpecFor(provider)
	if !ok {
		c.ResponseError(c.T("openai:provider must be one of") + ": " + aiConnProviderList())
		return
	}
	app, configured := aiOAuthConfig(spec.name)
	if !configured {
		c.ResponseErrorWithStatus(http.StatusServiceUnavailable,
			fmt.Sprintf("%s login is not configured on this deployment", spec.label))
		return
	}
	state, err := signAIOAuthState(org, spec.name)
	if err != nil {
		c.ResponseErrorWithStatus(http.StatusServiceUnavailable, "connector state unavailable")
		return
	}
	redirectURI := aiOAuthRedirectURI(getOriginFromHost(c.Ctx.Request.Host), spec.name)
	authorizeURL := aiAuthorizeURL(app, redirectURI, state, spec.name)

	if c.Query("format") == "json" {
		c.ResponseOk(map[string]string{"authorizeUrl": authorizeURL})
		return
	}
	c.Ctx.Redirect(http.StatusFound, authorizeURL)
}

// CallbackAIProvider completes OAuth: the org is recovered from the SIGNED state
// (not a header), the code is exchanged for a token, the token is SEALED into KMS
// (never the row/logs) through the same path as a BYOK key, and the org's provider
// row is upserted to "connected". The browser is then redirected back to the console
// with ?ai_connected=<provider> (or ?ai_connect_error=<provider> on failure). Because
// the org comes from the state THIS server signed, an attacker cannot land their
// token in a victim org — which is why this endpoint is state-authenticated rather
// than credential-gated.
// @Title CallbackAIProvider
// @Tag AI Connections API
// @Description OAuth callback that seals the provider token and connects the account
// @Param provider path string true "provider slug (openai|anthropic|google)"
// @Success 302 {string} string redirect back to the console
// @router /ai/connections/:provider/callback [get]
func (c *ApiController) CallbackAIProvider() {
	origin := getOriginFromHost(c.Ctx.Request.Host)
	successBase := aiOAuthSuccessBase(origin)

	provider := strings.ToLower(c.Ctx.Input.Param(":provider"))
	spec, ok := aiConnSpecFor(provider)
	if !ok {
		c.ResponseError(c.T("openai:provider must be one of") + ": " + aiConnProviderList())
		return
	}
	fail := func(reason string) {
		c.Ctx.Redirect(http.StatusFound, successBase+"?ai_connect_error="+url.QueryEscape(spec.name)+"&reason="+url.QueryEscape(reason))
	}

	if errParam := c.Query("error"); errParam != "" {
		fail("authorization denied: " + errParam)
		return
	}
	code := c.Query("code")
	state := c.Query("state")
	if code == "" || state == "" {
		fail("missing code or state")
		return
	}
	org, err := verifyAIOAuthState(state, spec.name)
	if err != nil {
		// A bad state is an attack or a stale link — refuse, do not guess an org.
		fail("invalid oauth state")
		return
	}
	app, configured := aiOAuthConfig(spec.name)
	if !configured {
		fail(spec.label + " login is not configured")
		return
	}

	token, _, err := exchangeAIOAuthCode(c.Context(), spec.name, app, code, aiOAuthRedirectURI(origin, spec.name))
	if err != nil {
		// Never log the code/token; the provider error class is enough to triage.
		fail("token exchange failed")
		return
	}

	// Seal the token FIRST — fails closed (sealProviderSecret refuses plaintext when
	// KMS is unconfigured), so the row is never written with a raw token.
	ref, err := sealProviderSecret(connectionSecretName(org, spec.name), token)
	if err != nil {
		fail("secret store unavailable")
		return
	}
	if err := persistConnection(org, spec, ref); err != nil {
		fail("could not record connection")
		return
	}

	c.Ctx.Redirect(http.StatusFound, successBase+"?ai_connected="+url.QueryEscape(spec.name))
}
