// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2025 The OpenAgent Authors. All Rights Reserved.
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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/hanzoai/account"

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/funding"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
	"github.com/hanzoai/go-openai"
)

// getUserBalance returns the current balance for a user by fetching from Commerce.
// Balance is mutable financial state (not identity) so it is never read from the
// JWT — always checked against the source of truth. Caching is handled by the
// router-level BalanceGate (routers/filter_balance.go); this controller-level
// call is a defense-in-depth backstop and does not maintain its own cache.
// The userId should be in "owner/name" format (e.g., "hanzo/alice").
// getUserBalance returns the available balance (in dollars) for an org. Billing
// is per-org: orgKey is the IAM org slug, used both as the balance destination
// key (?user=) and as the namespace selector (X-Org-Id), matching the gate
// and the per-org credit. Without the header, commerce's service-token path
// defaults to the "hanzo" namespace and a per-org credit is invisible.
func getUserBalance(subject, namespace string) (float64, error) {
	// Native path: a co-resident host (cloud) reads the subject's wallet balance
	// DIRECTLY from the in-process finance ledger — no HTTP. Cents → dollars.
	if r := object.BalanceReader(); r != nil {
		cents, err := r(context.Background(), subject, namespace, "usd")
		if err != nil {
			return 0, err
		}
		return float64(cents) / 100, nil
	}
	commerceEndpoint := conf.GetConfigString("commerceEndpoint")
	if commerceEndpoint == "" {
		return 0, fmt.Errorf("commerceEndpoint is not configured")
	}
	commerceEndpoint = strings.TrimRight(commerceEndpoint, "/")
	commerceToken := conf.GetConfigString("commerceToken")

	// Per global rule: /v1/ only, never /api/.
	// All commerce endpoints live under /v1/.
	// subject (?user=) is the per-user/per-org billing key; namespace
	// (X-Org-Id) is the org. Both must match the gate and the usage debit.
	reqURL := fmt.Sprintf("%s/v1/billing/balance?user=%s&currency=usd", commerceEndpoint, url.QueryEscape(subject))

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return 0, fmt.Errorf("Commerce request build failed: %w", err)
	}
	if commerceToken != "" {
		req.Header.Set("Authorization", "Bearer "+commerceToken)
	}
	// Scope the service-token call to this org's namespace.
	req.Header.Set("X-Org-Id", namespace)

	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("Commerce request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("Commerce returned status %d", resp.StatusCode)
	}

	var result struct {
		Available int64 `json:"available"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("failed to parse Commerce response: %w", err)
	}

	// Convert cents to dollars for backward compatibility with existing balance > 0 check
	balanceDollars := float64(result.Available) / 100.0

	return balanceDollars, nil
}

// isJwtToken checks if a token looks like a JWT (3 base64 segments separated by dots).
func isJwtToken(token string) bool {
	parts := strings.Split(token, ".")
	return len(parts) == 3 && len(parts[0]) > 10 && len(parts[1]) > 10
}

// isIAMApiKey checks if a token is an IAM-issued API key (sk- prefix) — the
// confidential half of the pair IAM mints, and the only one that authenticates.
func isIAMApiKey(token string) bool {
	return strings.HasPrefix(token, "sk-")
}

// isPublishableKey checks if a token is a publishable API key (pk- prefix).
// Publishable keys are safe for client-side use and can only access read-only endpoints.
func isPublishableKey(token string) bool {
	return strings.HasPrefix(token, "pk-")
}

// isWidgetKey checks if a token is a widget key (hz_ prefix).
// Widget keys provide restricted access for public-facing chat widgets
// on Hanzo properties (docs.hanzo.ai, hanzo.ai). They bypass balance
// checks but are limited to non-premium models with capped tokens.
func isWidgetKey(token string) bool {
	return strings.HasPrefix(token, "hz_")
}

// validateWidgetKey checks a widget key against KMS-stored valid keys.
// Resolution order: KMS secret "WIDGET_KEYS" (comma-separated list),
// then WIDGET_KEYS env var, then rejects. This replaces the former
// hardcoded "hz_widget_public" check.
func validateWidgetKey(token string) bool {
	// Try KMS first
	if keys, err := object.GetKMSSecret("WIDGET_KEYS"); err == nil && keys != "" {
		for _, k := range strings.Split(keys, ",") {
			if strings.TrimSpace(k) == token {
				return true
			}
		}
		return false
	}

	// Env var fallback (WIDGET_KEYS=hz_widget_public,hz_other_key)
	if keys := os.Getenv("WIDGET_KEYS"); keys != "" {
		for _, k := range strings.Split(keys, ",") {
			if strings.TrimSpace(k) == token {
				return true
			}
		}
		return false
	}

	// No keys configured — reject all widget tokens
	return false
}

// widgetMaxTokens caps the maximum tokens per widget request to control costs.
const widgetMaxTokens = 800

// Widget tenant resolution is bound to the widget KEY (see widgetKeyOwner in
// chat_retrieval.go), never the request Origin/Referer — a forgeable header must
// not select another tenant's data.

// widgetAllowedModels defines which models widget keys can access.
// Only cheap DO-AI models are allowed to keep costs minimal.
//
// A route priced at zero is reachable too, without being listed — see
// widgetMayServe. The list exists to bound what a page-embedded key can spend,
// and a route that spends nothing is already inside that bound.
var widgetAllowedModels = map[string]bool{
	"llama-3.1-8b":            true,
	"llama-3.3-70b":           true,
	"mistral-nemo":            true,
	"gpt-4o-mini":             true,
	"deepseek-r1-distill-70b": true,
	"claude-3-5-haiku":        true,
	"claude-haiku-4-5":        true,
}

// resolveProviderFromWidgetKey authenticates a widget key request.
// Widget keys skip balance checks but are restricted to non-premium models
// and have a token cap per request.
func resolveProviderFromWidgetKey(token string, requestedModel string, lang string) (*object.Provider, string, error) {
	// Validate the widget key against KMS-stored keys, with env var fallback.
	if !validateWidgetKey(token) {
		return nil, "", fmt.Errorf("invalid widget key")
	}

	// Look up the model in the routing table
	route := resolveModelRoute(requestedModel)
	if route == nil {
		return nil, "", fmt.Errorf(
			"model %q is not available for widget access",
			requestedModel,
		)
	}

	if !widgetMayServe(requestedModel) {
		return nil, "", fmt.Errorf(
			"model %q is not available for widget access. Allowed models: %s",
			requestedModel, widgetAllowedModelsList(),
		)
	}

	provider, err := object.GetModelProviderByName(route.providerName)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get provider %q: %s", route.providerName, err.Error())
	}
	if provider == nil {
		return nil, "", fmt.Errorf("provider %q not configured", route.providerName)
	}

	return provider, route.upstreamModel, nil
}

// widgetMayServe reports whether a key embedded in a public page may ask for this
// model: one of the cheap ids named above, or any route priced at zero.
//
// The list bounds what such a key can SPEND, and a route the vendor charges
// nothing for spends nothing — so admitting it keeps the bound rather than
// widening it, and a logged-out page can be answered without a wallet behind it.
// Read from the same price the ledger bills, and false for a model whose price
// cannot be read, so an unknown id is never admitted on a guess.
func widgetMayServe(model string) bool {
	return widgetAllowedModels[strings.ToLower(model)] || costsNothing(model, "")
}

// widgetAllowedModelsList returns a comma-separated list of widget-allowed models.
func widgetAllowedModelsList() string {
	models := make([]string, 0, len(widgetAllowedModels))
	for m := range widgetAllowedModels {
		models = append(models, m)
	}
	sort.Strings(models)
	return strings.Join(models, ", ")
}

// resolveProviderFromJwt validates a hanzo.id JWT token and returns the
// appropriate model provider for the requested model, plus the translated
// upstream model name.
//
// requested is the raw X-Org-Id the caller asked to act in ("" for none). This is
// the ONE auth path that can honor an org switch, because it is the one that
// holds the signed `orgs` claim proving membership — the ledger is resolved here,
// from those claims, rather than re-parsing the token downstream.
func resolveProviderFromJwt(token string, requested string, requestedModel string, lang string) (*object.Provider, *iam.User, string, error) {
	// Signature + issuer/audience validation (never raw iam.ParseJwtToken), so a
	// token minted for a foreign app/issuer cannot authenticate a paid request.
	claims, err := object.ParseAndValidateJWT(token)
	if err != nil {
		return nil, nil, "", authError("invalid access token: %s", err.Error())
	}

	user := &claims.User
	// An explicit org the signed claim does not cover is refused, not silently
	// billed to the caller's personal wallet. Unauthorized and nonexistent are one
	// answer so the header cannot be used to enumerate orgs.
	effective, orgErr := account.EffectiveOrg(user.Owner, claims.Orgs, requested)
	if orgErr != nil {
		return nil, nil, "", forbiddenError("organization %q is not available to this principal", requested)
	}
	ledger := account.LedgerOrg(effective, user.Owner, util.IsSuperAdmin(user))
	return resolveProviderForUser(user, ledger, requestedModel, lang)
}

// resolveProviderFromIAMKey validates an IAM API key (sk-{accessKey})
// and returns the model provider + user, same as JWT path.
//
// An IAM key carries no signed `orgs` claim, so it can never switch org: it bills
// the org that owns the key, which is its home org.
func resolveProviderFromIAMKey(apiKey string, requestedModel string, lang string) (*object.Provider, *iam.User, string, error) {
	// The whole token, prefix included, IS the accessKey IAM resolves.
	accessKey := apiKey

	user, err := getUserByAccessKey(accessKey)
	if err != nil {
		// IAM may return "password or code is incorrect" for service-account users
		// (cloud-agent, etc.) due to a known IAM deployment quirk where the
		// deployed binary handles certain user records differently. As a safe
		// fallback, we check the key against the CLOUD_AGENT_KEY KMS secret
		// (env CLOUD_AGENT_KEY as fallback). If it matches, we construct a
		// minimal user identity and let the Commerce balance check validate
		// the request as normal — so no billing bypass occurs.
		if fallbackUser := tryCloudAgentKeyFallback(apiKey); fallbackUser != nil {
			// Never log the API key (even masked) — owner/name identify the
			// fallback identity for debugging without leaking the credential.
			log.Warn("[iam-fallback] IAM returned %q; using cloud-agent fallback identity (owner=%s name=%s)",
				err.Error(), fallbackUser.Owner, fallbackUser.Name)
			return resolveProviderForUser(fallbackUser, fallbackUser.Owner, requestedModel, lang)
		}
		return nil, nil, "", authError("API key validation failed: %s", err.Error())
	}
	if user == nil {
		return nil, nil, "", authError("invalid API key")
	}

	return resolveProviderForUser(user, user.Owner, requestedModel, lang)
}

// tryCloudAgentKeyFallback checks whether apiKey matches the known cloud-agent
// service key stored in KMS (secret name "CLOUD_AGENT_KEY") with an env var
// fallback. Returns a minimal *iam.User on match, nil otherwise.
// This is intentionally narrow: only the exact key stored in KMS is accepted.
func tryCloudAgentKeyFallback(apiKey string) *iam.User {
	// Try KMS first
	var knownKey string
	if v, err := object.GetKMSSecret("CLOUD_AGENT_KEY"); err == nil && v != "" {
		knownKey = strings.TrimSpace(v)
	}
	// Env var fallback for local dev / bootstrap
	if knownKey == "" {
		knownKey = strings.TrimSpace(os.Getenv("CLOUD_AGENT_KEY"))
	}
	if knownKey == "" || apiKey != knownKey {
		return nil
	}
	// Typed, because this identity is assembled here rather than read from IAM and
	// nothing downstream can tell the difference. Left unsaid it read as a PERSON:
	// account.Payer's shape rule hands a person in the signup org a personal
	// wallet, and "hanzo" IS the signup org — so every call on this key addressed
	// hanzo/cloud-agent, a wallet no funding path can name, which reads $0 while
	// the org's balance sits one key away.
	return &iam.User{
		Owner: "hanzo",
		Name:  "cloud-agent",
		Type:  iam.Machine,
	}
}

// resolveProviderForUser is the shared logic for JWT and API key auth paths.
// Given a validated user, resolves the model route and provider.
//
// ledger is the org that PAYS for this request (account.LedgerOrg). It selects both
// the org's own BYOK provider and the wallet the balance gate reads, so a request
// billed to an org is served with that org's connected key — the two cannot name
// different tenants.
func resolveProviderForUser(user *iam.User, ledger string, requestedModel string, lang string) (*object.Provider, *iam.User, string, error) {
	// Look up the model in the static routing table. A valid caller asking for
	// an unknown model is a client error (400), not an auth failure.
	route := resolveModelRoute(requestedModel)
	if route == nil {
		return nil, user, "", modelError(
			"model %q is not available. Use GET /v1/models to list available models",
			requestedModel,
		)
	}

	// Fetch the provider entry that holds API keys/URLs for this upstream. Prefer
	// the org's OWN custom provider (BYOK) if it configured one, else the global
	// built-in provider on api.hanzo.ai. Returns a shallow copy, safe to mutate. A
	// missing/unconfigured provider is a server-side misconfiguration (500).
	provider, err := object.GetModelProviderByNameForOrg(ledger, route.providerName)
	if err != nil {
		return nil, user, "", serverError("failed to get provider %q: %s", route.providerName, err.Error())
	}
	if provider == nil {
		return nil, user, "", serverError("provider %q not configured in database", route.providerName)
	}

	// Prepaid-balance gate. Extracted into enforceBalanceGate so the provider-key
	// (sk-) path in authResolveProvider enforces the IDENTICAL policy — no auth
	// path can drift (M1).
	if gateErr := enforceBalanceGate(user, ledger, requestedModel); gateErr != nil {
		return nil, user, "", gateErr
	}

	return provider, user, route.upstreamModel, nil
}

// enforceBalanceGate applies the prepaid-balance policy to a resolved billing
// principal and stamps user.Balance. A positive prepaid balance is required for
// EVERY model — there is no implicit free allowance and no per-model tier: a
// subject's credit is only ever what it paid or was explicitly granted (commerce
// owns the one grant endpoint). It returns a typed billingError (402) when the
// balance is not positive, a serverError (500) when the balance cannot be verified
// (fail-closed — never grant on a lookup transport error), or nil to proceed.
//
// It is the SINGLE gate shared by the JWT/IAM path (resolveProviderForUser) and
// the provider-key (sk-) path (authResolveProvider), so no auth path can drift
// (M1). There is NO exempt path: every principal is gated on a positive prepaid
// balance. subject is the per-namespace billing account the gate read, the budget
// reservation, and the usage debit all key on.
//
// ledger is the org this request SPENDS FROM — account.LedgerOrg, via c.billingOrg or
// the claims the JWT resolver already holds. It is passed rather than derived
// from user.Owner so the gate reads the SAME wallet the debit writes: keying the
// gate on the selected org and the debit on the home org would check one balance
// and drain another. An empty ledger means the caller could not resolve one, and
// falls back to the home org — the behavior before the org switch existed.
func enforceBalanceGate(user *iam.User, ledger string, requestedModel string) error {
	if user == nil {
		return nil
	}
	if ledger == "" {
		ledger = user.Owner
	}
	orgKey := ledger // namespace (X-Org-Id): the org tenant whose ledger pays
	subject := user.PayerSubject(ledger)

	// A model priced at zero has nothing for this gate to refuse. Everything below
	// stops a call that cannot be paid for — the caller's wallet, and our own cash
	// behind it — and a route that bills zero on both sides spends neither. Refusing
	// one asks somebody to add funds for a call that will cost $0.00, which is how a
	// free plan came to be unable to reach the free routes we publish for it.
	if costsNothing(requestedModel, ledger) {
		return nil
	}

	// Cash circuit-breaker (internal/funding). Checked BEFORE the balance read
	// because it is a property of OUR bank account, not of the caller's wallet: a
	// caller with a perfectly good platform balance is exactly who spends our cash
	// once upstream promo credit is gone. Disarmed unless a ceiling is configured,
	// so this is a no-op until someone deliberately arms it.
	//
	// Being our bank account, it refuses with supplyError (503) and not with
	// billingError (402). The caller owes nothing, so 402 would send a funded org to
	// top up a balance that is already funded, and its code would put a billing
	// prompt in front of them. The figures stay on our side of the line for the same
	// reason the envelope keeps a buy price out of an answer: they are what we pay to
	// buy inference. The counter is what an operator reads.
	if cash := funding.Current(); cash.Refuse() {
		supplyRefused.WithLabelValues("hanzo", reasonCeiling).Inc()
		return supplyError(
			"paid inference is temporarily unavailable. Your balance is not affected — " +
				"nothing is owed and no action is needed. Retry shortly.",
		)
	}

	balance, err := getUserBalance(subject, orgKey)
	if err != nil {
		// Balance unverifiable (Commerce down, a rejected service token, or an
		// unset endpoint) → DENY. AI is prepaid: a broken or misconfigured billing
		// backend must never degrade to free, ungated inference. There is no
		// fail-open escape and no exempt principal.
		return serverError("failed to verify account balance: %s", err.Error())
	}
	if balance <= 0 {
		return billingError(
			"model %q requires a positive balance. Your current balance is $%.2f. "+
				"Add funds at https://hanzo.ai/billing",
			requestedModel, balance,
		)
	}

	user.Balance = balance
	return nil
}

// iamClientCreds resolves this service's confidential-app credentials, in order:
// env vars (IAM_CLIENT_ID/IAM_CLIENT_SECRET), KMS secrets, then the router config.
func iamClientCreds() (string, string) {
	clientId := conf.GetConfigString("IAM_CLIENT_ID")
	clientSecret := conf.GetConfigString("IAM_CLIENT_SECRET")

	// Try KMS if config values are empty or placeholders
	if clientId == "" {
		if v, err := object.GetKMSSecret("IAM_CLIENT_ID"); err == nil && v != "" {
			clientId = v
		}
	}
	if clientSecret == "" {
		if v, err := object.GetKMSSecret("IAM_CLIENT_SECRET"); err == nil && v != "" {
			clientSecret = v
		}
	}
	return clientId, clientSecret
}

// GetUserByAccessKey resolves an sk- IAM API key to its owning user via Hanzo
// IAM. Exported so the authz filter and the balance gate (package routers)
// resolve the key path to the same verified principal as the JWT path — ONE
// credential resolver, one tenant, one billing subject, one IAM transport.
func GetUserByAccessKey(accessKey string) (*iam.User, error) {
	return getUserByAccessKey(accessKey)
}

// getUserByAccessKey looks up a user by their IAM API key via Hanzo IAM.
func getUserByAccessKey(accessKey string) (*iam.User, error) {
	// Call IAM's get-user endpoint with accessKey query parameter
	iamEndpoint := conf.GetConfigString("IAM_URL")
	if iamEndpoint == "" {
		return nil, fmt.Errorf("IAM_URL is not configured")
	}
	iamEndpoint = strings.TrimRight(iamEndpoint, "/")

	// Per global rule: /v1/ only, never /api/. IAM serves at /v1/iam/get-user.
	// The legacy /api/get-user path is intercepted by the @hanzo/id SPA ingress
	// and returns HTML, which broke API-key resolution ("invalid character
	// '<'").
	reqURL := fmt.Sprintf("%s/v1/iam/get-user?accessKey=%s", iamEndpoint, url.QueryEscape(accessKey))

	// Auth is client_secret_basic (RFC 6749 §2.3.1) — the ONE transport IAM reads
	// to establish a confidential-APP principal. Key resolution is a
	// credential-disclosure boundary gated on `p.App != ""` holding CapKeyResolve,
	// and IAM derives p.App ONLY from Basic credentials: query params yield no
	// principal at all (401 "authentication required"), and a client_credentials
	// bearer resolves to a plain org-scoped principal with an EMPTY App, which the
	// gate refuses with "auth:Unauthorized operation". Basic is also why no token
	// cache is needed — there is no token.
	clientId, clientSecret := iamClientCreds()
	if clientId == "" || clientSecret == "" {
		return nil, fmt.Errorf("IAM client credentials are not configured")
	}

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("IAM request build failed: %w", err)
	}
	req.SetBasicAuth(clientId, clientSecret)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("IAM request failed: %w", err)
	}
	defer resp.Body.Close()

	// IAM states its reason in the BODY (code + msg), not the status line. This
	// used to return on a non-200 before reading it, so `key_unknown` — "the
	// entity does not exist" — reached the caller as a bare "IAM returned status
	// 400". Those read alike and mean opposite things: one says this key is not
	// there, the other says key validation itself is broken. Every gateway key in
	// the cluster once failed this way, and the bare status sent the search
	// toward a contract break for hours when IAM had named the cause in the first
	// reply. Decode first, judge second.
	var result struct {
		Status string    `json:"status"`
		Msg    string    `json:"msg"`
		Code   string    `json:"code"` // WHY, when IAM refused (iam store.KeyFailure)
		Data   *iam.User `json:"data"`
	}
	decodeErr := json.NewDecoder(resp.Body).Decode(&result)

	// A refusal IAM named is a refusal we can relay, whatever the status line.
	if decodeErr == nil && (result.Code != "" || result.Status != "ok") {
		return nil, keyRefusal(result.Code, result.Msg, accessKey)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("IAM returned status %d and named no reason", resp.StatusCode)
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("failed to parse IAM response: %w", decodeErr)
	}

	// ok-with-nobody. IAM said the request succeeded and named no user, which this
	// used to relay as (nil, nil) — no error, no principal — so the caller fell to
	// its bare "invalid API key" with nothing behind it. That message is the one
	// shape a holder cannot act on and an operator cannot trace: it looks identical
	// whether the key is wrong, the envelope changed, or IAM answered about someone
	// who no longer exists. A key that resolves to nobody IS unusable, so refuse it
	// — but say which of the two things happened, and keep the redacted prefix so
	// the line names the credential without disclosing it.
	if result.Data == nil {
		return nil, authError(
			"API key %s resolved to no user — IAM accepted the lookup and returned nothing. "+
				"The key may have been deleted, or its owner removed. Mint a new one at "+keysURL,
			keyHint(accessKey))
	}

	return result.Data, nil
}

// keysURL is where a holder mints a replacement key — the console page that serves
// it, spelled ONCE because every refusal below names it and three copies of an
// address drift the moment the page moves. It moved already: these messages sent
// people to cloud.hanzo.ai/keys, which answers 404 (cloud.hanzo.ai is the product
// site; the console is its own host, and its key surface is /api-keys). A refusal
// that names a cure the holder cannot reach is worse than one that names none.
const keysURL = "https://console.hanzo.ai/api-keys"

// keyRefusal turns IAM's refusal into something the holder can ACT on.
//
// IAM answers every unresolvable key with one sentence — "the entity does not exist"
// — and this function used to relay it verbatim, so a user whose key had simply been
// revoked was told their entity was gone and went looking for a deleted organization
// instead of minting a new key. The reason now rides beside that sentence as a code
// (iam internal/store/apikey.go), and each code has exactly one cure.
//
// The key is named by PREFIX only. A holder needs to know WHICH of their keys failed;
// nobody needs the rest of it, and this string reaches logs and error bodies.
func keyRefusal(code, msg, key string) error {
	switch code {
	case "key_unknown":
		return fmt.Errorf("API key %s is not recognized — it was revoked or replaced. "+
			"Mint a new one at "+keysURL, keyHint(key))
	case "key_wrong_door":
		return fmt.Errorf("API key %s is not a secret key. A publishable pk- key "+
			"identifies an org for ingest and cannot authenticate a request; "+
			"use your secret (sk-) key", keyHint(key))
	case "key_expired":
		return fmt.Errorf("API key %s has expired — mint a new one at "+
			keysURL, keyHint(key))
	case "key_not_publishable":
		return fmt.Errorf("API key %s is not a publishable key", keyHint(key))
	case "key_foreign_user", "key_dangling_user":
		// Not the holder's doing, and not something they can fix. Say only that it
		// was refused; the code carries the detail to whoever reads the logs.
		return fmt.Errorf("API key %s was refused (%s)", keyHint(key), code)
	}
	// An IAM that sent no code, or one this build does not know: relay what it said
	// rather than inventing a cause.
	return fmt.Errorf("IAM error: %s", msg)
}

// keyHint names a key by its prefix and nothing else — enough to tell WHICH key
// failed, useless to anyone who reads it. The ONE way a key is rendered outside its
// own use.
func keyHint(key string) string {
	key = strings.TrimSpace(key)
	const shown = 9 // "sk-" + 6
	if len(key) <= shown {
		return "…"
	}
	return key[:shown] + "…"
}

// ── Usage tracking ──────────────────────────────────────────────────────────

// usageRecord mirrors IAM's UsageRecord for JSON serialization.
type usageRecord struct {
	Owner            string  `json:"owner"`
	User             string  `json:"user"`
	Organization     string  `json:"organization"`
	Model            string  `json:"model"`
	Provider         string  `json:"provider"`
	PromptTokens     int     `json:"promptTokens"`
	CompletionTokens int     `json:"completionTokens"`
	TotalTokens      int     `json:"totalTokens"`
	CacheReadTokens  int     `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens int     `json:"cacheWriteTokens,omitempty"`
	Cost             float64 `json:"cost"`
	Currency         string  `json:"currency"`
	Premium          bool    `json:"premium"`
	Stream           bool    `json:"stream"`
	Status           string  `json:"status"`
	ErrorMsg         string  `json:"errorMsg"`
	ClientIP         string  `json:"clientIp"`
	RequestID        string  `json:"requestId"`

	// Requested is the model the caller ASKED for, set only when a different route
	// answered — today, when a vendor's account was spent and it served the request
	// from a route it charges nothing for. Empty on every ordinary call, so
	// non-empty IS the fallback flag, and the pair (Requested, Model) says both what
	// was wanted and what arrived.
	//
	// It exists because a downgrade nobody can see is a lie about what the user got:
	// the answer is real, it is just not the model they picked, and every reader of
	// this record — the ledger, the span, whoever is looking at a bad answer — needs
	// to be able to tell those two situations apart.
	Requested string `json:"requested,omitempty"`

	// Free states that the route that answered is priced at nothing BY THE VENDOR
	// — a spare route, the kind that keeps answering while an account is empty.
	//
	// It is a separate fact from Requested, and keeping them apart is the point:
	// one says the caller did not get what they asked for, the other says what the
	// answer cost. A price table asked about a SKU it has never seen answers with
	// its conservative default, which is the right guess for an unknown model and
	// exactly the wrong one here — it would bill a customer for the fallback they
	// were given because a vendor of ours ran out of money.
	Free bool `json:"free,omitempty"`

	// ImageCount is the number of images generated. Image models bill per image,
	// not per token: when > 0, recordUsage bills via imageCostCents instead of
	// the token-based cost. Placed last so the token-field alignment above is
	// unchanged.
	ImageCount int `json:"imageCount,omitempty"`

	// VideoCount is the number of videos generated. Like images, video models
	// bill per unit, not per token: when > 0, recordUsage bills via
	// videoCostCents. Mutually exclusive with ImageCount in practice (a call is
	// either an image or a video generation, never both).
	VideoCount int `json:"videoCount,omitempty"`

	// AudioSeconds is the DURATION of audio a speech-to-text call consumed, and
	// AudioChars the number of characters a text-to-speech call synthesized.
	// Audio bills per unit like images and video, but the unit differs by
	// DIRECTION — the market prices transcription per minute and synthesis per
	// million characters — so the two travel separately rather than collapsing
	// into one "audio units" field that means different things per row.
	//
	// A speech model that bills TOKENS instead (an ASR pipeline decoding through
	// an LM) needs no field here: it fills PromptTokens/CompletionTokens like any
	// other token-billed call. The record carries what was actually consumed.
	//
	// Both were previously discarded at the emit sites, which is why audio rows
	// billed 0: the quantity never reached the record, so the token math it fell
	// through to had nothing to multiply.
	AudioSeconds float64 `json:"audioSeconds,omitempty"`
	AudioChars   int     `json:"audioChars,omitempty"`

	// BYO marks a call executed against a customer-connected third-party account
	// (the caller's org supplied its own provider key via /v1/ai/connections),
	// rather than a Hanzo-served provider bought with Hanzo credits. When true the
	// customer already paid the upstream directly, so recordUsage bills only the
	// 1% platform fee (platformFeeCents) instead of the full token cost — the full
	// cost is still recorded for analytics. Set at the emit site via providerBYO.
	BYO bool `json:"byo,omitempty"`

	// FeeCents is the Hanzo platform surcharge stamped by recordUsage
	// (platformFeeCents(costCents, BYO)): the amount actually billed on a BYO
	// call, 0 for a Hanzo-credits call. Persisted to the warehouse fee_cents column.
	FeeCents int64 `json:"feeCents,omitempty"`

	// Account is the attribution label for the account that served the call:
	// "<owner>/<name>" for a BYO connection, "hanzo" for a Hanzo-served provider.
	Account string `json:"account,omitempty"`

	// Unpriced marks a token-billed call whose model had NO configured price in any
	// source (family/route/conf/static), so it billed at the conservative default
	// ($1/$4 per 1M) rather than a real rate. The debit is UNCHANGED — the flag just
	// makes the guess honest: the row is queryable and the o11y span carries
	// priced=false, instead of silently presenting an invented price as real. Set in
	// recordUsage; image/video calls (own per-unit pricing) are never flagged.
	Unpriced bool `json:"unpriced,omitempty"`

	// BilledNanoExact / CostNanoExact carry an EXACT nano-USD billed amount and
	// provider COGS when the biller knows them (zen's commerce Meter computes both
	// per served tier). Set, they are what the call billed and cost, and the table
	// recompute is not consulted.
	//
	// Pointers because exactness is PRESENCE, not positivity. A turn that billed
	// exactly nothing — a free tier, a zero-price model — knows its amount as
	// precisely as any other, and a plain int64 could not say so: 0 read as "unset"
	// sent it back to the table, which invents a price for a call that had none.
	BilledNanoExact *int64 `json:"billedNanoExact,omitempty"`
	CostNanoExact   *int64 `json:"costNanoExact,omitempty"`

	// ── o11y gen_ai span enrichment (all optional, best-effort) ──────────────
	// These carry the observation-of-record attribution the o11y span plane reads
	// (controllers/telemetry.go emitGenAISpan). They are omitempty so a record that
	// does not populate them emits no attribute — the span is honest, never a
	// fabricated value, and each dimension lights up as its producer is wired.

	// Session is the conversation/session id. Emitted as gen_ai.conversation.id +
	// session.id, which is what turns the o11y sessions view on for this org.
	Session string `json:"session,omitempty"`
	// TraceID is the gen_ai span's OWN trace id, stamped by emitGenAISpan.
	//
	// The span plane and the spend ledger answer different questions about one call
	// — what happened, and what it cost — and joining them needs an id BOTH sides
	// observed. Deriving one independently on each side would produce two ids that
	// merely describe the same request, which is not the same thing and cannot be
	// joined on. Empty when telemetry is off: there was no span, so there is no
	// trace, and saying so is better than inventing an id nothing else will carry.
	TraceID string `json:"traceId,omitempty"`
	// Environment is the caller's logical environment label (X-Environment). Emitted
	// as deployment.environment on the span so Observe narrows by environment instead
	// of defaulting to "default". Empty emits no attribute (honest, never fabricated).
	Environment string `json:"environment,omitempty"`
	// Project is the caller's org SUB-SCOPE (X-Project-Id). It is stamped on the
	// cloud_usage ledger row + the gen_ai span, so cost/tokens/latency narrow WITHIN
	// an org by project. Empty is the org's default project (whole-org view).
	// Populated once in recordTrace from the request context (WithGenAIAttribution).
	Project string `json:"project,omitempty"`
	// APIKeyHash is a NON-reversible ref (SHA-256 hex) of the caller credential —
	// never the plaintext key. Emitted on the gen_ai span (gen_ai.hanzo.api_key_hash)
	// so a span correlates to a key without the store ever holding a secret.
	// Populated in recordTrace from the request context.
	APIKeyHash string `json:"apiKeyHash,omitempty"`
	// ServedBy is where inference ran: "hanzo" (cloud), "byo-provider" (an
	// org-owned provider key), or "byo-gpu" (an org's own cluster). Empty defaults
	// to "hanzo" at emit — the cloud-served majority.
	ServedBy string `json:"servedBy,omitempty"`
	// ClusterID is the BYO-GPU cluster id when ServedBy == "byo-gpu".
	ClusterID string `json:"clusterId,omitempty"`
	// RoutePolicy is the enso route-policy decision that selected the model.
	RoutePolicy string `json:"routePolicy,omitempty"`
	// InputMessages / OutputMessages are the serialized prompt/completion. They are
	// PII and are emitted ONLY when O11Y_GENAI_CAPTURE_MESSAGES is enabled
	// (default off = redacted). Never logged, never billed — telemetry only.
	InputMessages  string `json:"inputMessages,omitempty"`
	OutputMessages string `json:"outputMessages,omitempty"`

	// Agent is the machine credential that authorized this call, "<org>/<name>",
	// when the caller was not a person. An IAM application is a route, not a
	// customer: it answers "which program placed the call" and never "whose spend
	// is this", so it is recorded HERE and User is left to the person the
	// application acts for. A row carrying an Agent and no User is a call nobody
	// owns — visible as such, rather than presented as the application's own spend.
	Agent string `json:"agent,omitempty"`

	// Origin is the host that answered — the hostname of the serving provider's
	// URL (object.Provider.Origin). Provider is our route label for the same call;
	// carrying both lets a row be asked whether the label still matches the address
	// the bytes went to. Empty when the serving provider declares no URL.
	Origin string `json:"-"`

	// Payer is the money address this call spends from — the account the balance
	// gate READ, carried here rather than re-derived, so the debit cannot land
	// somewhere else. Not serialized: an Account's fields are unexported by design
	// (it is money; an account whose owner disagrees with its key must not exist),
	// and the wire already carries the subject it resolves to.
	Payer account.Account `json:"-"`

	// Allowance is the subject whose free calls this one counts against, when the
	// request named a subject of its own. The public lane does: it counts a visitor,
	// and one shared subject would let a single caller empty every visitor's day.
	// Everyone else counts against their payer, so this is empty on an ordinary
	// call. Not serialized — it addresses the count, it does not describe the row.
	Allowance string `json:"-"`
}

// allowance answers whose free calls this one counts against: the subject the request
// named, or the payer. Free calls and money are bounded per subject, and this is the
// one expression that says which subject, so the two counters cannot drift apart.
func (r *usageRecord) allowance() string {
	if r.Allowance != "" {
		return r.Allowance
	}
	return r.payer().Subject()
}

// payer answers who pays for this call. ONE rule, and it prefers the answer the gate
// already computed over deriving a second one.
//
// The fallback is for a record with no authenticated principal behind it — the
// session-scoped and self-billing surfaces. It is account.PayerOf, which is what
// every debit used to do, and it is exactly why this field exists: PayerOf sees two
// strings, so it cannot see the signed billing_account claim naming a personal
// wallet, and it cannot see that a credential is a machine. Both are inputs to who
// pays. Missing them, it confidently answers the pre-claim default — a different
// account from the one the gate read a moment earlier, on the same request.
func (r *usageRecord) payer() account.Account {
	if !r.Payer.Zero() {
		return r.Payer
	}
	return account.PayerOf(r.Owner, r.User)
}

// bind ties this record to the credential that authorized the call: the money
// address it spends from, and the name its spend is attributed to. It is the ONE
// place a record learns its principal, so the two answers are read from one
// identity and cannot disagree.
//
// THE MONEY is unchanged and is the same single expression the balance gate
// resolves with (iam.User.Payer) — one identity, one rule, one address, so gate and
// debit cannot answer differently. Nil-safe: a surface with no principal leaves the
// field unset and falls back.
//
// THE NAME follows one rule: only a person may be named, because only a person can
// owe money.
//
//   - A person's credential names itself, and nothing on the request can move that.
//     Otherwise any caller could send a header and put their bill on a colleague.
//
//   - A machine credential names no person, because there is none behind it. It is
//     recorded as the Agent, and the User becomes whoever the identity boundary
//     authenticated before the machine placed the call (X-User-Id, qualified
//     "<org>/<name>"). A bare name is not an identity and is refused rather than
//     guessed at.
//
//   - A machine that names nobody leaves User empty and says so at once. An empty
//     column is a call a query can find; the application's own name in that column
//     reads as attributed and is not, which is how spend with no owner reaches an
//     invoice unnoticed.
//
// Naming a person never moves money: for a machine the payer resolves to the org
// whatever name the row carries, and a person's payer comes from their own
// credential. Attribution and settlement stay separate answers to separate questions.
func (r *usageRecord) bind(ctx context.Context, u *iam.User) {
	if r == nil || u == nil {
		return
	}
	r.Payer = u.Payer(r.Owner)
	// Whose free calls this one counts against is the same question as whose money
	// it spends, so it is answered here, from the same request, and not resolved a
	// second time somewhere downstream. Only the public lane puts a subject on the
	// request; every other caller counts against the payer just resolved.
	r.Allowance = visitorOf(ctx)

	self := u.Owner + "/" + u.Name
	// account.IsMachine is the ONE predicate for "is this credential a program",
	// shared with the payer rule above.
	if !account.IsMachine(u.Type) {
		r.User = self
		return
	}

	r.Agent = self
	if named := strings.TrimSpace(object.GenAIAttributionFromContext(ctx).User); strings.Contains(named, "/") {
		// …and the name must be SOMEONE ELSE. A caller reaching us through the
		// identity boundary never chooses this header: the boundary deletes what
		// arrived and rewrites it from the presenting credential's own claims, so a
		// machine that came that way is handed ITS OWN subject. Copying that into
		// the person column puts the application back where this whole rule exists
		// to keep it out of, one hop later and harder to see.
		//
		// The two spellings differ in the org half — a token's `sub` is qualified by
		// the registration's owner and the `owner` claim by the org it serves — so
		// the NAME is what identifies the principal across them.
		if _, name, _ := strings.Cut(named, "/"); !strings.EqualFold(name, u.Name) {
			r.User = named
			return
		}
	}
	r.User = ""
	warnUnownedOnce(self)
}

// unownedWarned dedupes the unowned-spend report to once per agent, so a busy
// application logs one actionable line instead of one per request.
var unownedWarned sync.Map

// warnUnownedOnce reports spend that no person owns: an application bought
// inference and named nobody it was buying for, so the row bills the org with an
// empty user column. The fix is upstream — the caller sends X-User-Id with the
// person it authenticated — and this is the only moment the evidence still exists.
func warnUnownedOnce(agent string) {
	if _, seen := unownedWarned.LoadOrStore(agent, struct{}{}); seen {
		return
	}
	log.Error("usage: application %q is buying inference for nobody — its rows carry no user. Send X-User-Id with the person it authenticated, as \"<org>/<name>\".", agent)
}

// billingQueue is the singleton usage record delivery queue. Initialized by
// InitBillingQueue() in main.go. If nil (Commerce not configured), recordUsage
// is a no-op.
var billingQueue *util.BillingQueue

// InitBillingQueue creates the billing queue from app config. Must be called
// once during startup. Returns the queue so main.go can call Shutdown().
func InitBillingQueue() *util.BillingQueue {
	endpoint := conf.GetConfigString("commerceEndpoint")
	if endpoint == "" {
		return nil
	}
	endpoint = strings.TrimRight(endpoint, "/")
	token := conf.GetConfigString("commerceToken")

	billingQueue = util.NewBillingQueue(endpoint, token)
	return billingQueue
}

// usageCostCents is the cost of a call in cents: the exact nano cost (usageCostNano
// — per unit for image, video and audio, per token otherwise) rounded to the nearest
// cent. ONE money, reported at two precisions, rather than a second arithmetic over
// the same inputs that can reach a different answer.
//
// It could, and did. The cents path rounded a float and then raised anything left at
// zero to a whole cent, so a call the ledger charged $0.0002 for was reported as $0.01
// — the same money, fifty times over, in the column every spend view reads. Measured
// on ten days of hanzo.cloud_usage: 867 of 1477 rows carried a cent that was invented
// rather than charged, and the totals differed by $8.03 on $34.69.
//
// A call that costs less than half a cent now reports zero cents, which is what it
// costs. Nothing is lost by saying so: cost_nano and billed_nano on the same row carry
// the exact amount, and they are what the debit and the invoice read.
func usageCostCents(record *usageRecord) int64 {
	return nanoToCents(usageCostNano(record))
}

// usageBilledCents is what Hanzo actually DEBITS the org ledger for a call, in
// cents, given its full provider cost: the full cost for a Hanzo-served call, or
// only the platform fee (platformFeeCents, ~1%) for a BYO call where the customer
// already paid the upstream with their own key. This is the ONE billed-amount of
// record: recordUsage debits it and emitGenAISpan reports it as
// _o11y.gen_ai.billed_cost, so Observe reconciles with the invoice — while
// _o11y.gen_ai.total_cost keeps the full provider cost for analytics.
func usageBilledCents(record *usageRecord, costCents int64) int64 {
	if record.BYO {
		return platformFeeCents(costCents, true)
	}
	return costCents
}

// recordUsage serializes a usage record and enqueues it for reliable delivery
// to Commerce. The queue handles retries with exponential backoff.
// Only successful API calls are recorded (error status is filtered here).
func recordUsage(record *usageRecord) {
	// Dense flywheel reward (HIP-510): score EVERY routed request's outcome and
	// attach it to its routing decision, so the bandit learns from request-volume
	// signal instead of sparse explicit thumbs. Runs BEFORE the success filter so an
	// errored request scores 0 (the arm is penalized for failing). Dark-by-default,
	// async, best-effort — never slows or fails the request.
	emitAutoRoutingReward(record)

	// Only record successful calls
	if record.Status != "success" {
		return
	}

	// Calculate cost. usageCostCents is the ONE cost of record — the same value the
	// o11y gen_ai span reports (emitGenAISpan), so the ledger debit and the span
	// can never disagree.
	costCents := usageCostCents(record)

	// Honesty flag: if the model has no configured price, the cost above was billed at
	// the conservative default. Mark the row (and warn once) rather than silently
	// presenting the invented rate as real — the debit itself is unchanged.
	if recordUnpriced(record) {
		record.Unpriced = true
		warnUnpricedOnce(record.Model)
	}

	// BYO: the customer paid the upstream directly with their own connected key,
	// so Hanzo bills ONLY the 1% platform fee — not the token cost. The full
	// costCents is still recorded (payload + warehouse) for analytics. On a
	// Hanzo-served call (byo=false) feeCents is 0 and the token cost is billed as
	// before. Stamp record.FeeCents so the warehouse writer persists the same value.
	feeCents := platformFeeCents(costCents, record.BYO)
	record.FeeCents = feeCents
	amount := usageBilledCents(record, costCents)

	// The debit MUST hit the same account the balance gate read and the starter
	// credit funded: the billing SUBJECT within the org NAMESPACE.
	//   namespace (X-Org-Id) = record.Owner (the org)
	//   subject   (?user=)   = the payer the emit site carried from the principal
	// For a personal-billing org that is "owner/name" (per-user); for a pooled org
	// it is the org slug. record.Owner is the IAM `owner`; fall back to deriving
	// owner+name from "owner/name" if Owner was not populated upstream.
	org := record.Owner
	if org == "" {
		if i := strings.IndexByte(record.User, '/'); i > 0 {
			org = record.User[:i]
		} else {
			org = record.User
		}
	}
	subject := record.payer().Subject()

	// WHAT THIS CALL SPENT: money, or one of the caller's free calls. A call that
	// debited nothing is exactly the call a wallet cannot bound, which is the whole
	// job of the plan allowance — so what the call BILLED decides it. usageFree reads
	// the same margin usageBilledUSD renders below, so the debit and the count cannot
	// hold different opinions about what this call was.
	//
	// IT IS COUNTED HERE BECAUSE HERE IS WHERE A CALL ANSWERED. The ceiling bounds
	// SPEND; spend is incurred when a model is reached; and this function has already
	// returned for anything that is not a success. A route that never resolved, a
	// vendor that timed out, a pod mid-roll — none of them reach this line, so none of
	// them costs a caller one of their calls. The gate upstream only READS the same
	// count, so a refusal at the ceiling does not raise it either.
	free := ""
	if usageFree(record) {
		free = record.allowance()
		// The public lane keeps its own count in this process so its ceiling holds
		// while the host is unreachable. Both counts rise at this one moment.
		//
		// A VISITOR ON THE RECORD IS WHAT SAYS THIS IS THAT LANE. Only the lane puts
		// one there, and nothing on the request can name one. The org would be the
		// obvious test and is the wrong one: it is resolved from X-Org-Id, which any
		// caller sets, so a bound that read it could be steered out of the lane it
		// bounds.
		if record.Allowance != "" {
			publicCount.count(record.Allowance, utcDay(time.Now()), publicChatDaily())
		}
	}

	// Native in-proc finance debit — the ONE money path when co-resident with the
	// finance ledger (hanzoai/cloud unified binary). The debit lands DIRECTLY on the
	// org's SQLite wallet, the SAME account the prepaid gate reads, so a funded
	// account both gates AND depletes. When the hook is installed we do NOT also
	// enqueue to Commerce — that would double-bill. Standalone ai (no hook) falls
	// through to the HTTP billing queue below.
	if rec := object.UsageRecorder(); rec != nil {
		if err := rec(context.Background(), object.UsageEvent{
			Subject:   subject,
			Namespace: org,
			USD:       usageBilledUSD(record), // EXACT atto-precise debit, never a floored cent
			Currency:  "usd",
			Model:     record.Model,
			Provider:  record.Provider,
			Allowance: free,
			RequestID: record.RequestID,
		}); err != nil {
			log.Error("billing: native usage record failed request_id=%s: %v", record.RequestID, err)
		}
		return
	}

	if billingQueue == nil {
		return
	}

	payload := map[string]interface{}{
		"user":             subject,
		"actor":            record.User,
		"currency":         "usd",
		"amount":           amount,
		"model":            record.Model,
		"provider":         record.Provider,
		"promptTokens":     record.PromptTokens,
		"completionTokens": record.CompletionTokens,
		"totalTokens":      record.TotalTokens,
		"cacheReadTokens":  record.CacheReadTokens,
		"cacheWriteTokens": record.CacheWriteTokens,
		"imageCount":       record.ImageCount,
		"videoCount":       record.VideoCount,
		"audioSeconds":     record.AudioSeconds,
		"audioChars":       record.AudioChars,
		"requestId":        record.RequestID,
		"premium":          record.Premium,
		"stream":           record.Stream,
		"status":           record.Status,
		"clientIp":         record.ClientIP,
		"byo":              record.BYO,
		"feeCents":         feeCents,
		"costCents":        costCents,
		"account":          record.Account,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		log.Error("billing: failed to marshal usage record request_id=%s: %v", record.RequestID, err)
		return
	}

	billingQueue.Enqueue(&util.BillingRecord{
		Body:      body,
		RequestID: record.RequestID,
		Org:       org,
		Model:     record.Model,
	})
}

// recordTrace persists an LLM/agent trace + usage record to hanzoai/datastore
// (native datastore OLAP) over native ZAP — the ONE internal telemetry path.
//
// The datastore (datastore) is reached directly via object.DatastoreExec
// (object/datastore.go) — the datastore image serves datastore on :8123/:9000,
// not a ZAP bridge. Fire-and-forget — failures are logged inside the writer,
// never block the request.
//
// Only the spend ledger (hanzo.cloud_usage) is written here — that is the ai
// module's table, read by GetCloudUsageOverview for the console Overview. The
// per-tenant trace ledger (canonical hanzo.observations / hanzo.traces) is owned
// and populated by the o11y/insights ingestion pipeline, not this module — one
// writer per table.
//
// Separately, emitGenAISpan ships one OpenTelemetry GenAI span per call to the
// o11y OTLP collector (opt-in via OTEL_EXPORTER_OTLP_ENDPOINT). That is the
// SOURCE the o11y pipeline ingests — this module never writes the observations
// table directly. The span emit is batched/async and a no-op when telemetry is
// off, so it never blocks the request.
func recordTrace(ctx context.Context, record *usageRecord, startTime time.Time) {
	// Enrich the record with the request-scoped observability attribution the
	// TenantContextFilter stashed on the context (project sub-scope + a
	// non-reversible credential ref). Doing it HERE — the ONE funnel every
	// completion surface passes through — stamps BOTH the warehouse row
	// (zapWriteUsage) and the gen_ai span (emitGenAISpan) from a single place,
	// rather than threading it through ~15 emit sites. A record that already set a
	// value (an explicit producer) is left untouched.
	if record != nil {
		attr := object.GenAIAttributionFromContext(ctx)
		// Org fallback: stamp the VERIFIED tenant org only when a producer set neither
		// Organization nor Owner, so real tenant traffic always carries its org (the
		// o11y llmobs views drop empty-org spans) without overriding an explicit value.
		if record.Organization == "" && record.Owner == "" {
			record.Organization = attr.Org
		}
		if record.Project == "" {
			record.Project = attr.Project
		}
		if record.Session == "" {
			record.Session = attr.Session
		}
		if record.Environment == "" {
			record.Environment = attr.Environment
		}
		if record.APIKeyHash == "" {
			record.APIKeyHash = attr.APIKeyHash
		}
	}
	// The span is emitted BEFORE the ledger write, because emitting it is what
	// decides the trace id and the row has to carry it. The write is the async half
	// (a goroutine, off the request path); the span emit is already batched and
	// non-blocking, so leading with it costs the caller nothing and is the only
	// order in which the two planes can share an id.
	emitGenAISpan(ctx, record, startTime)
	go zapWriteUsage(record, startTime)
	emitGenAIEvent(ctx, record, startTime)
}

// ── API handlers ────────────────────────────────────────────────────────────

// authenticate validates a bearer credential to a real principal WITHOUT routing
// a model — the authentication half of authResolveProvider. Handlers call it on a
// request-body error so an INVALID credential is rejected (401) regardless of
// body validity: a malformed (or field-incomplete) body from an unauthenticated
// caller must never return 200/400 that confirms the endpoint or lets it be
// probed. It is invoked only on the error path, so the happy path keeps a single
// validation (authResolveProvider). It mirrors authResolveProvider's auth
// branches EXACTLY and never grants on an unknown key (fail-secure).
func (c *ApiController) authenticate(token string) error {
	switch {
	case isWidgetKey(token):
		if !validateWidgetKey(token) {
			return authError("Widget authentication failed: invalid widget key")
		}
		return nil
	case isJwtToken(token):
		// Signature + issuer/audience validation (R3): a foreign-aud or
		// wrong-issuer token is rejected here, not just signature-checked.
		if _, err := object.ParseAndValidateJWT(token); err != nil {
			return authError("invalid access token: %s", err.Error())
		}
		return nil
	default:
		// A secret key — see authResolveProvider for why the STORE that owns it,
		// not its spelling, decides what it is.
		provider, err := object.GetProviderByProviderKey(token, c.GetAcceptLanguage())
		if err != nil {
			return authError("invalid API key: %s", err.Error())
		}
		if provider != nil {
			return nil
		}
		// getUserByAccessKey returns (nil, nil) for an unknown key (IAM 200 +
		// data:null), so check BOTH the error AND a nil user — exactly as
		// resolveProviderFromIAMKey does. Missing the nil-user case let an invalid
		// key fall through to a 400 parse error instead of a 401.
		user, err := getUserByAccessKey(token)
		if err != nil {
			// Same cloud-agent service-key fallback as resolveProviderFromIAMKey,
			// so a valid service key is not falsely rejected here.
			if tryCloudAgentKeyFallback(token) != nil {
				return nil
			}
			return authError("API key validation failed: %s", err.Error())
		}
		if user == nil {
			return authError("invalid API key")
		}
		return nil
	}
}

// providerKeyBillingUser derives the billing identity for a provider-key (sk-)
// caller: the org that OWNS the provider row the key belongs to (and therefore
// minted the key). The sk- key is a machine credential, so it bills the OWNER
// ORG — Type "application" marks it M2M (account.IsMachine), so account.Payer
// resolves it to the org account for every org, never a per-person wallet no one
// funds. A provider with no owner is
// unattributable: return an auth error so the caller refuses rather than spend
// the shared upstream key for free — the invariant is that every call spending
// the shared upstream key bills someone.
func providerKeyBillingUser(provider *object.Provider) (*iam.User, error) {
	if provider == nil || strings.TrimSpace(provider.Owner) == "" {
		return nil, authError("provider key is not attributable to a billable owner")
	}
	return &iam.User{Owner: provider.Owner, Type: "application"}, nil
}

// authResolveProvider authenticates a bearer token and resolves the requested
// model to its upstream provider. It is the single auth + model-routing policy
// for every OpenAI-compatible surface (chat, embeddings, rerank): each handler
// calls it instead of re-implementing the widget/IAM/JWT/provider-key branches.
//
// Returns the resolved provider (with KMS-resolved secret), the billed user
// (nil for widget/provider-key auth), the upstream model id, whether the route
// is premium, and whether the caller used an anonymous widget key. Errors are
// returned pre-formatted for ResponseError.
func (c *ApiController) authResolveProvider(token, requestedModel, orgId string) (provider *object.Provider, authUser *iam.User, upstreamModel string, isPremium bool, isWidget bool, err error) {
	lang := c.GetAcceptLanguage()

	switch {
	case isRunKey(token):
		// Run key (hrun_...) — an autonomous run buying inference on the ledger of
		// the org that started it. It resolves to a machine principal and to NO
		// user, so it authenticates nobody and opens no other door; see run.go.
		// The org rides the TOKEN, never orgId, so a run cannot be pointed at
		// another tenant's balance by a header.
		provider, authUser, upstreamModel, err = resolveProviderFromRunKey(token, requestedModel, lang)
		if err != nil {
			err = wrapAuth(err)
			return
		}
		c.Ctx.Input.SetParam("recordUserId", authUser.Owner+"/run")
		return

	case isWidgetKey(token):
		// Widget key (hz_...) — restricted, token-capped model access (isWidget
		// caps MaxTokens to widgetMaxTokens downstream). It is NOT free: like an
		// sk- provider key, a widget call bills the OWNER ORG that minted the key
		// (widgetKeyOwner → WIDGET_KEY_OWNERS / WIDGET_DEFAULT_OWNER), so the
		// balance gate + budget reservation + usage debit all engage exactly as
		// for a normal principal. Type "application" marks it a machine, so
		// account.Payer resolves the billing subject to the org account. An
		// unattributable widget key (no owner mapping and no default owner) is a
		// config error: refuse rather than spend the shared upstream for free —
		// the same fail-secure invariant the sk- path enforces (every call that
		// spends the shared upstream bills someone).
		isWidget = true
		var widgetUpstream string
		provider, widgetUpstream, err = resolveProviderFromWidgetKey(token, requestedModel, lang)
		if err != nil {
			err = authError("Widget authentication failed: %s", err.Error())
			return
		}
		owner := widgetKeyOwner(token)
		if strings.TrimSpace(owner) == "" {
			err = authError("widget key is not attributable to a billable owner")
			return
		}
		authUser = &iam.User{Owner: owner, Type: "application"}
		upstreamModel = widgetUpstream
		c.Ctx.Input.SetParam("recordUserId", owner+"/widget")
		log.Info("Widget key access: owner=%s, model=%s, upstream=%s", owner, requestedModel, upstreamModel)
		return

	case isJwtToken(token):
		// hanzo.id JWT token — full model routing + billing. Same typed-status
		// contract as the IAM key path. The raw X-Org-Id goes in unvalidated: the
		// resolver holds the signed `orgs` claim and is the only place allowed to
		// decide whether the request may act — and pay — in that org.
		provider, authUser, upstreamModel, err = resolveProviderFromJwt(token, strings.TrimSpace(c.Ctx.Input.Header("X-Org-Id")), requestedModel, lang)
		if err != nil {
			err = wrapAuth(err)
			return
		}

	case isPublishableKey(token):
		// Publishable key (pk-...) — the read-only credential class. It reaches
		// only the surfaces that do not refuse it up front (embeddings; every
		// generative handler rejects pk- before calling here), and it resolves
		// and bills exactly as an IAM secret key: the org that minted the key
		// pays. This is the credential the cloud deployment documents for its
		// embed client — least privilege for a read-only endpoint.
		provider, authUser, upstreamModel, err = resolveProviderFromIAMKey(token, requestedModel, lang)
		if err != nil {
			err = wrapAuth(err)
			return
		}

	default:
		// A secret key, and the STORE that owns it decides what it is. Two
		// families share the sk- spelling: an upstream vendor key, which lives in
		// the provider table, and the key this estate mints, which lives in IAM.
		// Both lookups are exact, so neither can claim the other's key; the order
		// decides only who answers when NEITHER owns it, and IAM's refusal is the
		// one that names the cure ("mint a new one at" + keysURL) where
		// a provider miss can only say "invalid".
		provider, err = object.GetProviderByProviderKey(token, lang)
		if err != nil {
			err = authError("invalid API key: %s", err.Error())
			return
		}
		if provider == nil {
			// Not a vendor key — full model routing + billing off the IAM
			// principal. resolveProviderFromIAMKey returns a typed apiError (401
			// invalid key / 400 bad model / 402 balance / 500 misconfig); wrapAuth
			// preserves it and 401s any untyped error.
			provider, authUser, upstreamModel, err = resolveProviderFromIAMKey(token, requestedModel, lang)
			if err != nil {
				err = wrapAuth(err)
				return
			}
			break
		}
		// Attribute + bill this vendor-key call to the org that OWNS the provider row
		// (and thus minted this key). Without an authUser, BOTH reserveBudget and
		// recordUsage are skipped (they gate on authUser != nil) — so an sk- key
		// spending the SHARED upstream (do-ai) ran at ZERO cost and bypassed the
		// balance/premium gate. Resolve the billing owner from the key's OWN
		// provider row BEFORE any model-route swap below, so the debit lands on
		// the key minter, not the upstream route provider we merely call. An
		// unattributable provider (no owner) is refused (fail-secure).
		authUser, err = providerKeyBillingUser(provider)
		if err != nil {
			return
		}
		c.Ctx.Input.SetParam("recordUserId", authUser.Owner+"/provider-key")
		// Apply model routing for sk- keys too. If the route points to a
		// different provider than the one that owns the API key, switch to the
		// route's provider so zen/fireworks models work with any key.
		if route := resolveModelRouteForOrg(requestedModel, orgId); route != nil {
			upstreamModel = route.upstreamModel
			isPremium = route.premium
			if route.providerName != provider.Name {
				if routeProvider, routeErr := object.GetModelProviderByName(route.providerName); routeErr == nil && routeProvider != nil {
					provider = routeProvider
				}
			}
		}
		// M1: apply the SAME prepaid-balance gate the JWT/IAM paths enforce
		// (resolveProviderForUser), so an sk- provider key can never reach paid
		// upstreams without a positive balance. The billed owner is authUser (the
		// provider-row owner resolved just above), and a provider key carries no
		// membership claim — it always bills the org that minted it.
		if gateErr := enforceBalanceGate(authUser, authUser.Owner, requestedModel); gateErr != nil {
			err = gateErr
			return
		}
		return
	}

	// Shared post-resolution for IAM/JWT auth: record the billed user and the
	// premium flag from the route table.
	if authUser != nil {
		c.Ctx.Input.SetParam("recordUserId", authUser.Owner+"/"+authUser.Name)
	}
	if route := resolveModelRouteForOrg(requestedModel, orgId); route != nil {
		isPremium = route.premium
	}
	return
}

// ChatCompletions implements the OpenAI-compatible chat completions API
// @Title ChatCompletions
// @Tag OpenAI Compatible API
// @Description OpenAI compatible chat completions API. Accepts:
//   - Widget key (hz_...)   — restricted models, no balance check, token-capped
//   - IAM API key (sk-...)  — full model routing + billing
//   - hanzo.id JWT token    — full model routing + billing
//   - Provider API key      — direct provider access
//
// @Param   body    body    openai.ChatCompletionRequest  true    "The OpenAI chat request"
// @Success 200 {object} openai.ChatCompletionResponse
// @router /chat [post]
func (c *ApiController) ChatCompletions() { c.chatCompletions(callerBearer) }

// caller says which door a completion arrived at, and therefore what is taken from
// the request and what is decided for it. It is an unexported type with no decoded
// form, so no header, body or query can produce one: a request cannot elect its own
// door. callerPublic is reachable only from ChatCompletionsPublic, after that lane's
// ceiling has admitted the call.
type caller int

const (
	callerBearer caller = iota // a credential is presented and decides everything
	callerPublic               // no credential; the public lane decided everything
)

// chatCompletions is the one completion pipeline. Both doors run it; they differ only
// in who the caller is, which is a value passed in rather than a fact re-derived here.
func (c *ApiController) chatCompletions(from caller) {
	var token string
	if from == callerBearer {
		// Extract Bearer token
		authHeader := c.Ctx.Request.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			c.ResponseErrorWithStatus(401, c.T("openai:Invalid API key format. Expected 'Bearer API_KEY'"))
			return
		}

		token = strings.TrimPrefix(authHeader, "Bearer ")

		// Publishable keys (pk-) cannot access completions — reject early
		if isPublishableKey(token) {
			c.Ctx.Output.SetStatus(403)
			c.Ctx.Output.Header("Content-Type", "application/json")
			c.Ctx.Output.Body([]byte(`{"error":{"message":"Publishable keys (pk-) can only access read-only endpoints (/v1/models, /v1/embeddings, /health). Use a secret key (sk-) for completions.","type":"auth_error","code":403}}`))
			c.EnableRender = false
			return
		}
	}

	// Track timing for observability
	requestStartTime := time.Now().UTC()

	// Parse request body. Authenticate BEFORE reporting a parse error so an
	// invalid credential is 401 regardless of body validity — a malformed body
	// from an unauthenticated caller must not return 200. A valid credential with
	// a bad body gets 400 (not 200).
	var request openai.ChatCompletionRequest
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, &request); err != nil {
		if from == callerBearer {
			if authErr := c.authenticate(token); authErr != nil {
				c.ResponseAuthError(authErr)
				return
			}
		}
		c.ResponseErrorWithStatus(http.StatusBadRequest, fmt.Sprintf("Failed to parse request: %s", err.Error()))
		return
	}

	// Resolve org context for per-org model routing and pricing.
	orgId := c.GetOrg()
	if from == callerPublic {
		// THE PUBLIC LANE READS NEITHER THE MODEL NOR THE ORG FROM THE REQUEST. Both
		// are overwritten before anything downstream can consult them, so naming a
		// paid model or another tenant's org is not refused — it is unrepresentable.
		// The assignment is here rather than in the lane because this is the last
		// point at which the body could still be read.
		request.Model, orgId = freeID, publicOrg
	}

	// A PUBLIC CALL ACTS UNDER NO AMBIENT IDENTITY. The widget lives on our own
	// pages, so a visitor may well be carrying a session for this estate; every
	// helper that reads one would then attribute an anonymous call to whoever is
	// signed in — the routing ledger would record their name, and the wallet
	// resolver would try to switch them onto an org and answer "nobody to bill".
	// The lane admitted a stranger, so a stranger is who the rest of this serves.
	routingUser := c.routingUserId()
	principal := c.principalUser()
	if from == callerPublic {
		routingUser, principal = "", nil
	}

	// `auto`/`zen-router` resolution (below) calls the router engine — an internal
	// HTTP request carrying the caller's prompt — and records a RoutingEvent, BEFORE
	// authResolveProvider authenticates further down. Authenticate FIRST so an
	// UNAUTHENTICATED caller can never drive that internal machinery: auth precedes
	// every side effect. authenticate is the SAME credential check authResolveProvider
	// runs (a strict subset — credential only, no model/balance), read-only, so it
	// never rejects a request the resolver would accept. Concrete models skip this
	// (resolveAutoModel is a no-op for them), so the dominant path pays nothing.
	if isAutoModel(request.Model) {
		if authErr := c.authenticate(token); authErr != nil {
			c.ResponseAuthError(authErr)
			return
		}
	}

	// One request id, generated once here — it is the response id (`chatcmpl-<id>`),
	// the usage-ledger request_id, AND the routing-event join key, so a later reward
	// (POST /v1/feedback) can be tied back to THIS decision. Generated
	// before routing so resolveAutoModel can stamp it on the RoutingEvent.
	requestId := uuid.NewString()

	// Virtual `auto`/`zen-router` model → resolve to a concrete servable model id
	// BEFORE any provider/pricing/billing resolution, so the ENTIRE existing path
	// (auth+routing, ModelRoute fallbacks, zen identity, balance reserve/settle,
	// usage record, response `model` echo) bills and reports the model that
	// actually served. The transparency header lets callers see the routed choice.
	// routedTask + routingRecorded carry the routing decision to the post-response
	// judge hook at the tail of this handler (the LLM-as-a-judge dense reward): set
	// in whichever branch records a RoutingEvent, so the judge scores only requests
	// that are actually in the routing ledger.
	var routedTask string
	var routingRecorded bool
	if routed, task, ok := resolveAutoModel(request.Model, orgId, routingUser, requestId, principal, &request, c.sloFromHeaders()); ok {
		request.Model = routed
		routedTask, routingRecorded = task, true
		c.Ctx.ResponseWriter.Header().Set(RoutedModelHeader, routed)
		// Fold this request's TASK into the region's task-mix for the live-traffic
		// globe — geo from the edge headers only (NO IP), aggregates only. Best-effort.
		object.GlobalTraffic.RecordTask(
			c.Ctx.Request.Header.Get("CF-IPCountry"),
			c.Ctx.Request.Header.Get("CF-Region-Code"),
			task,
		)
	} else if !isAutoModel(request.Model) {
		// NON-auto: record the caller's explicit model selection so EVERY request is
		// a rateable, trainable data point — up/down feedback for all models, and the
		// ledger captures which model served which task even when the caller picked.
		task := recordExplicitRouting(request.Model, orgId, routingUser, requestId, &request)
		routedTask, routingRecorded = task, true
		object.GlobalTraffic.RecordTask(
			c.Ctx.Request.Header.Get("CF-IPCountry"),
			c.Ctx.Request.Header.Get("CF-Region-Code"),
			task,
		)
	}

	// Authenticate the bearer token and resolve the requested model to its
	// upstream provider, premium flag, and (for IAM/JWT auth) the billed user.
	// This is the ONE auth+routing policy, shared with /v1/embeddings and
	// /v1/rerank — see authResolveProvider.
	var (
		provider      *object.Provider
		authUser      *iam.User
		upstreamModel string
		isPremium     bool
		isWidget      bool
		err           error
	)
	if from == callerPublic {
		// No credential to authenticate. The lane already settled who is asking and
		// what they may run; this settles only who runs it.
		provider, authUser, upstreamModel, err = c.resolveProviderForPublic()
	} else {
		provider, authUser, upstreamModel, isPremium, isWidget, err = c.authResolveProvider(token, request.Model, orgId)
	}
	if err != nil {
		c.ResponseAuthError(err)
		return
	}
	// The ledger this request spends from — the org the switcher selected when the
	// signed membership claim proves it, else the caller's home org. Resolved ONCE
	// here so the reservation below and every usage record at the tail of this
	// handler key on the SAME wallet the gate inside authResolveProvider just read.
	ledger := c.billingOrg(authUser)
	if isWidget || from == callerPublic {
		// Cap max_tokens for a caller nobody can be billed for — the widget's key
		// holder and the public lane's visitor are the same case.
		if request.MaxTokens == 0 || request.MaxTokens > widgetMaxTokens {
			request.MaxTokens = widgetMaxTokens
		}
	}

	if provider.Category != "Model" {
		c.ResponseError(fmt.Sprintf("Provider %s is not a model provider", provider.Name))
		return
	}

	// Set the upstream model name on the provider. For JWT/IAM key auth, this
	// is the translated upstream model from the routing table. For provider
	// API key auth, fall back to the request model or provider's default.
	if upstreamModel != "" {
		provider.SubType = upstreamModel
	} else if request.Model != "" {
		provider.SubType = request.Model
	}

	// ── Balance reservation ────────────────────────────────────────────
	// Hold an upper-bound budget for this request so concurrent requests for the
	// same subject can't double-spend a balance the async debit hasn't applied
	// yet. The router gate is coarse (balance>0); this enforces
	// balance >= estimated cost and reserves it atomically. It is settled with
	// the ACTUAL cost when the request completes (deferred fail-safe release).
	var hold *budgetHold
	if authUser != nil {
		subject := authUser.PayerSubject(ledger)
		// Clamp the upstream completion ceiling BEFORE reserving so the proxied
		// (tool/stream) upstream can never emit more than we reserve — the actual
		// settle can never exceed the hold (R1b). reserveCompletionTokens also
		// covers the QueryText pipeline's fixed cap, which ignores max_tokens.
		request.MaxTokens = clampMaxTokens(request.MaxTokens)
		est := estimateRequestCostCents(request.Model, estimatePromptTokens(&request), request.MaxTokens)
		var ok bool
		if hold, ok = reserveBudget(subject, est); !ok {
			c.ResponseAuthError(billingError("%s", object.InsufficientBalance(c.Ctx.Request.Host, ledger, "request cost").Message))
			return
		}
	}
	defer hold.settle(0)

	// ── Model families (Zen, Enso) ─────────────────────
	// A family model is served by its family service, which owns identity, reasoning,
	// the 1M ladder, vision, the fan-out, and the upstream. ai forwards verbatim and
	// meters the result; it holds no family routing of its own (hip-00NN).
	//
	// A family is one provider among several. When it refuses for a reason of its
	// own — its account is empty, it is rate limited, it is down — it writes
	// nothing and hands back the reason, and the request carries on to the
	// route's declared alternates below. That is the difference between a vendor
	// running out of money and the product going dark.
	var familyRefused []attempt
	if fam := familyForProviderType(provider.Type); fam != nil {
		familyRefused = c.pipeToFamily(fam, "chat/completions", "openai", request.Model, c.Ctx.Input.RequestBody, request.Stream, orgId, authUser, isPremium, hold, requestStartTime)
		if familyRefused == nil {
			return
		}
		c.recordRefusals(request.Model, familyRefused, authUser, isPremium, request.Stream, requestId, requestStartTime)
	}

	// ── Tool-calling pass-through ──────────────────────────────────────
	// When the request includes tools/functions, the QueryText pipeline
	// cannot handle structured tool calls. Proxy the raw request directly
	// to the upstream provider's OpenAI-compatible endpoint so the LLM
	// receives tool definitions and can return tool_calls in the response.
	//
	// A tool request the family refused stops here. The pipeline below is
	// text-only, so cascading it to an alternate would answer a tool call with
	// prose — an answer shaped wrongly is worse than an honest refusal, and the
	// refusal names the vendor and the reason.
	if len(request.Tools) > 0 || request.ToolChoice != nil {
		if familyRefused != nil {
			c.ResponseFailure(exhausted(request.Model, familyRefused))
			return
		}
		c.proxyToolRequest(provider, &request, requestStartTime, authUser, isPremium, orgId, hold)
		return
	}

	// Multimodal (vision): the QueryText pipeline below is text-only and would drop
	// image parts. Forward multimodal requests verbatim to the upstream (the same path
	// tool-calls take), so vision-capable models actually receive the images.
	//
	// Same stop as tool calls, for the same reason: cascading a request whose
	// images the pipeline would silently discard produces an answer about
	// nothing.
	if requestHasMedia(&request) {
		if familyRefused != nil {
			c.ResponseFailure(exhausted(request.Model, familyRefused))
			return
		}
		c.proxyToolRequest(provider, &request, requestStartTime, authUser, isPremium, orgId, hold)
		return
	}

	// Extract messages content
	var question string
	var systemPrompt string
	history := []*model.RawMessage{}

	for _, msg := range request.Messages {
		// Extract text from Content or MultiContent (array-style content parts)
		text := msg.Content
		if text == "" && len(msg.MultiContent) > 0 {
			var parts []string
			for _, part := range msg.MultiContent {
				if part.Type == openai.ChatMessagePartTypeText && part.Text != "" {
					parts = append(parts, part.Text)
				}
			}
			text = strings.Join(parts, "\n")
		}
		switch msg.Role {
		case "system":
			systemPrompt = text
		case "user":
			question = text
		case "assistant":
			history = append(history, &model.RawMessage{
				Author: "AI",
				Text:   text,
			})
		}
	}

	if question == "" {
		c.ResponseError(c.T("openai:No user message found in the request"))
		return
	}

	// Combine system prompt with user question if available
	if systemPrompt != "" {
		question = fmt.Sprintf("System: %s\n\nUser: %s", systemPrompt, question)
	}

	// Setup for streaming if enabled
	if request.Stream {
		c.Ctx.ResponseWriter.Header().Set("Content-Type", "text/event-stream")
		c.Ctx.ResponseWriter.Header().Set("Cache-Control", "no-cache")
		c.Ctx.ResponseWriter.Header().Set("Connection", "keep-alive")
	}

	// Create custom writer for OpenAI format
	writer := &OpenAIWriter{
		Response:     *c.Ctx.ResponseWriter,
		Buffer:       []byte{},
		RequestID:    requestId,
		Stream:       request.Stream,
		Cleaner:      *NewCleaner(6),
		Model:        request.Model,
		IncludeUsage: request.StreamOptions != nil && request.StreamOptions.IncludeUsage,
	}

	// Optional RAG: unified retrieval path shared with the old /chat-docs route.
	// Enabled when any of the following is true:
	//   - Request header `X-Retrieval: 1` or body field `retrieval=true`
	//   - Header `X-Retrieval-Store` specifies a store
	//   - Auth is a widget key AND WIDGET_RETRIEVAL=1 (auto-RAG for public widgets)
	knowledge := c.retrieveKnowledgeIfEnabled(
		question,
		retrievalOwner(authUser, token),
		c.retrievalStore(),
		c.GetAcceptLanguage(),
	)

	// Resolve the route for failover (may have fallback providers), sized to the
	// prompt: if this prompt is bigger than the provider we would normally use
	// can hold, we route it to one that can rather than refusing it. See
	// routeForPrompt — a too-large prompt is a fact about the PROVIDER, not the
	// model, and the fallback chain already knows who else serves it.
	promptTokens, _ := model.GetTokenSize(request.Model, question)
	route := routeForPrompt(request.Model, orgId, promptTokens)

	// Call the model provider, cascading to the route's alternates on a refusal
	// that is about the vendor rather than about this request.
	//
	// Every routed model takes this path, not just the ones with fallbacks
	// declared. A single-provider model has no alternate to move to, but it
	// still earns the rest: its vendor gets demoted when it refuses, the refusal
	// is recorded where someone reads it, and "everyone refused" comes back as
	// an honest 503 naming the reason instead of an upstream 402 telling the
	// customer THEY are out of money.
	var modelResult *model.ModelResult
	var actualProvider served
	var tried []attempt

	if route != nil {
		modelResult, actualProvider, tried, err = ask{
			ctx:       c.Ctx.Request.Context(),
			route:     route,
			org:       ledger,
			model:     request.Model,
			primary:   provider,
			question:  question,
			history:   history,
			knowledge: knowledge,
			lang:      c.GetAcceptLanguage(),
			writer:    writer,
			sent:      func() bool { return writer.StreamSent },
			prior:     familyRefused,
		}.serve()
	} else {
		// Model absent from the route table: call the provider auth resolved.
		var modelProvider model.ModelProvider
		modelProvider, err = provider.GetModelProvider(c.GetAcceptLanguage())
		if err != nil {
			c.ResponseError(fmt.Sprintf("Failed to get model provider: %s", err.Error()))
			return
		}
		modelResult, err = modelProvider.QueryText(question, writer, history, "", knowledge, nil, c.GetAcceptLanguage())
		actualProvider = served{provider.Name, provider.Origin(), provider}
	}

	// Every vendor that refused goes in the ledger, whether or not one of them
	// eventually served. A failover nobody can see leaves the empty account
	// empty. The family's own refusal is already recorded above, so skip it here.
	if n := len(familyRefused); len(tried) > n {
		c.recordRefusals(request.Model, tried[n:], authUser, isPremium, request.Stream, requestId, requestStartTime)
	}

	if err != nil {
		// Record failed usage
		if authUser != nil {
			errRecord := &usageRecord{
				Owner:     ledger,
				Model:     request.Model,
				Provider:  actualProvider.name,
				Origin:    actualProvider.origin,
				Premium:   isPremium,
				Stream:    request.Stream,
				Status:    "error",
				ErrorMsg:  err.Error(),
				ClientIP:  c.Ctx.Request.RemoteAddr,
				RequestID: requestId,
			}
			errRecord.bind(c.Ctx.Request.Context(), authUser)
			errRecord.BYO, errRecord.Account = providerBYO(provider, authUser)
			recordUsage(errRecord)
			recordTrace(c.Ctx.Request.Context(), errRecord, requestStartTime)
		}
		c.ResponseError(err.Error())
		return
	}

	// Record successful usage (actualProvider reflects which provider served the request)
	if authUser != nil {
		successRecord := &usageRecord{
			Owner:            ledger,
			Organization:     authUser.Owner,
			Model:            request.Model,
			Provider:         actualProvider.name,
			Origin:           actualProvider.origin,
			PromptTokens:     modelResult.PromptTokenCount,
			CacheReadTokens:  modelResult.CacheReadTokenCount,
			CacheWriteTokens: modelResult.CacheWriteTokenCount,
			CompletionTokens: modelResult.ResponseTokenCount,
			TotalTokens:      modelResult.TotalTokenCount,
			Currency:         "USD",
			Premium:          isPremium,
			Stream:           request.Stream,
			Status:           "success",
			ClientIP:         c.Ctx.Request.RemoteAddr,
			RequestID:        requestId,
		}
		successRecord.bind(c.Ctx.Request.Context(), authUser)
		// Whether this call was "bring your own key" is a property of the row
		// that SPENT a credential, not of the row auth resolved before failover
		// moved the request. Reading the latter is how a call served on the
		// platform's key gets billed as BYO — 1% instead of the token cost, with
		// the platform eating the upstream.
		successRecord.BYO, successRecord.Account = providerBYO(actualProvider.row, authUser)
		recordUsage(successRecord)
		recordTrace(c.Ctx.Request.Context(), successRecord, requestStartTime)
		// Settle the reservation with the ACTUAL cost (this works identically for
		// streaming and non-streaming non-tool responses — both have real token
		// counts here from the QueryText pipeline).
		hold.settle(calculateCostCentsWithCache(request.Model, modelResult.PromptTokenCount, modelResult.ResponseTokenCount, 0, 0))
	}

	// Handle response based on streaming mode
	if !request.Stream {
		answer := writer.MessageString()

		response := openai.ChatCompletionResponse{
			ID:      "chatcmpl-" + requestId,
			Object:  "chat.completion",
			Created: util.GetCurrentUnixTime(),
			Model:   request.Model,
			Choices: []openai.ChatCompletionChoice{
				{
					Index: 0,
					Message: openai.ChatCompletionMessage{
						Role:    "assistant",
						Content: answer,
					},
					FinishReason: openai.FinishReasonStop,
				},
			},
			Usage: openai.Usage{
				PromptTokens:     modelResult.PromptTokenCount,
				CompletionTokens: modelResult.ResponseTokenCount,
				TotalTokens:      modelResult.TotalTokenCount,
			},
		}

		jsonResponse, err := json.Marshal(response)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		c.Ctx.Output.Header("Content-Type", "application/json")
		c.Ctx.Output.Body(jsonResponse)
	} else {
		err = writer.Close(
			modelResult.PromptTokenCount,
			modelResult.ResponseTokenCount,
			modelResult.TotalTokenCount,
		)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}
	}
	// LLM-as-a-judge: score THIS served (prompt, response) into a dense quality
	// reward for the enso router. Placed AFTER the reply is fully written (both
	// stream and non-stream), so it never adds latency; the hook only spawns a
	// goroutine after cheap gates, and is a no-op unless ROUTER_JUDGE_ENABLED. The
	// prompt/response are passed transiently — never persisted (see router_judge.go).
	if routingRecorded {
		judgeRoutedResponse(c.Ctx.Request.UserAgent(), orgId, requestId, c.Ctx.Request.Header.Get("CF-IPCountry"), request.Model, routedTask, question, writer.MessageString())
	}
	c.EnableRender = false
}

// ListModels returns the list of available models from the routing table.
//
// PUBLIC BY DESIGN, AND IT DOES NOT AUTHENTICATE — that is the whole contract, so it
// is stated here rather than left to be inferred. The catalogue is the same for
// everyone (listAvailableModels takes no principal), docs.hanzo.ai fetches it from
// the browser, and every policy layer around it already says so out loud: the authz
// filter lists "models" as public, filter_balance refuses to gate it (a 402 here was
// a console-wide outage), the rate limiter excludes it, and cloud's spend.Reachable
// carries /v1/models/ as "the model catalog the shell reads for discovery".
//
// SO THE Authorization HEADER IS NOT AN ADMISSION CHECK HERE. It is read for ONE
// thing — annotating gated SKUs with the caller's own access standing — and
// annotation degrades to nothing when there is no verified principal.
//
// It used to hold a "require authentication" gate that authenticated nobody: it
// rejected an ABSENT credential and a MALFORMED one, then accepted any string that
// merely looked like a key. `Bearer sk-` followed by 36 zeroes returned 200 in
// production; so did a JWT three days expired. It was a shape check wearing an auth
// check's clothes, and its cost was diagnostic: /v1/models is the natural "is my auth
// working?" probe, and answering 200 to a dead credential sent people debugging the
// wrong system. A public endpoint must not appear to validate. Either check the
// credential or ignore it — this one ignores it, deliberately and visibly.
//
// Removing that gate discloses nothing new: the catalogue was already reachable by
// anyone willing to type three characters, so there is no confidentiality delta, only
// an honesty one.
//
// @Title ListModels
// @Tag OpenAI Compatible API
// @Description Returns a list of all available models. Public — no authentication.
// @Success 200 {object} object
// @router /models [get]
func (c *ApiController) ListModels() {
	// Gated (limited-preview) SKUs carry the caller's access standing
	// (waitlist|requested|granted), so the client can show "request access" vs
	// "granted" without a second call.
	//
	// The ONLY use of the credential on this route, and it is best-effort:
	// principalUser returns nil unless the request carries a VERIFIED principal (a
	// session, a signature-checked JWT, or a key IAM resolved). So an absent, expired
	// or forged credential simply yields the un-annotated public catalogue — never a
	// 401, and never another caller's standing.
	jsonResponse, err := modelListing(c.principalUser())
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.Ctx.Output.Header("Content-Type", "application/json")
	c.Ctx.Output.Body(jsonResponse)
	c.EnableRender = false
}

// proxyToolRequest forwards an OpenAI chat completion request that contains
// tool definitions directly to the upstream provider, bypassing the QueryText
// pipeline which cannot handle structured tool calls. The raw upstream response
// (including tool_calls) is streamed back to the client.
func (c *ApiController) proxyToolRequest(
	provider *object.Provider,
	request *openai.ChatCompletionRequest,
	requestStartTime time.Time,
	authUser *iam.User,
	isPremium bool,
	orgId string,
	hold *budgetHold,
) {
	requestId := uuid.NewString()

	// The wallet this request spends from — the same value ChatCompletions gated
	// and reserved on, re-derived from the same credential rather than threaded,
	// so the two can never be given different arguments.
	ledger := c.billingOrg(authUser)

	// The answer leaves through our door (envelope.go): our id, the SKU the caller
	// asked for, the seller. The SKU has to be read BEFORE the line below, which
	// replaces it with the upstream's own name for the model — that name is what the
	// upstream needs and what this path used to hand back to the caller.
	mk := &mark{id: "chatcmpl-" + requestId, model: request.Model, seller: seller(provider, authUser)}

	// Rewrite model to upstream model name
	request.Model = provider.SubType

	// For Claude/Anthropic providers, convert to Anthropic Messages API format
	if provider.Type == "Claude" {
		c.proxyToolRequestAnthropic(provider, request, mk.model, requestStartTime, authUser, isPremium, orgId, requestId, hold)
		return
	}

	// On the streaming path FORCE the upstream to emit a final usage chunk
	// (stream_options.include_usage) so streamed tool calls are billed for their
	// real token counts. Without this the streamed response carried no usage and
	// was debited as $0 — any funded key + a dummy tool + stream:true = free
	// premium inference. Remember whether the CLIENT requested usage so the
	// injected usage-only chunk can be suppressed if it did not.
	clientWantsUsage := true
	if request.Stream {
		clientWantsUsage = request.StreamOptions != nil && request.StreamOptions.IncludeUsage
		request.StreamOptions = &openai.StreamOptions{IncludeUsage: true}
	}

	// Determine upstream endpoint and auth
	upstreamURL, apiKey, authHeader := resolveUpstreamEndpoint(provider)
	if upstreamURL == "" {
		c.ResponseError("No upstream endpoint configured for provider: " + provider.Name)
		return
	}

	// Marshal the full request (tools included) for OpenAI-compatible providers
	body, err := json.Marshal(request)
	if err != nil {
		c.ResponseError(fmt.Sprintf("Failed to marshal request: %s", err.Error()))
		return
	}

	// Build upstream HTTP request
	req, err := http.NewRequest(http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		c.ResponseError(fmt.Sprintf("Failed to create upstream request: %s", err.Error()))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	} else if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		if authUser != nil {
			errRecord := &usageRecord{
				Owner:     ledger,
				Model:     request.Model,
				Provider:  provider.Name,
				Origin:    provider.Origin(),
				Premium:   isPremium,
				Stream:    request.Stream,
				Status:    "error",
				ErrorMsg:  err.Error(),
				ClientIP:  c.Ctx.Request.RemoteAddr,
				RequestID: requestId,
			}
			errRecord.bind(c.Ctx.Request.Context(), authUser)
			errRecord.BYO, errRecord.Account = providerBYO(provider, authUser)
			recordUsage(errRecord)
			recordTrace(c.Ctx.Request.Context(), errRecord, requestStartTime)
		}
		c.ResponseError(fmt.Sprintf("Upstream request failed: %s", err.Error()))
		return
	}
	defer resp.Body.Close()

	// ONE status decision, ahead of the split and ahead of every billing line
	// below. Whether the caller asked for a stream does not change whose refusal
	// this is, and nothing has been written yet either way — for a stream this is
	// the last moment that is true, which is why the decision lives here rather
	// than down in the relay.
	//
	// It also stands between a refusal and the billing below, which reads usage off
	// the body and settles. An error body carries no usage, so anything reaching
	// that code tokenizes the refusal itself and charges the caller for the round
	// trip that turned them away — recorded as a success, since nothing down there
	// looks at a status.
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		c.ResponseFailure(relay(mk.model, provider.Name, resp.StatusCode, b))
		return
	}

	// Copy upstream response headers
	for k, vals := range resp.Header {
		for _, v := range vals {
			c.Ctx.ResponseWriter.Header().Add(k, v)
		}
	}

	if request.Stream {
		// Stream: copy SSE events while capturing token usage for billing.
		c.Ctx.ResponseWriter.Header().Set("Content-Type", "text/event-stream")
		c.Ctx.ResponseWriter.Header().Set("Cache-Control", "no-cache")
		c.Ctx.ResponseWriter.Header().Set("Connection", "keep-alive")
		c.Ctx.ResponseWriter.WriteHeader(http.StatusOK)

		// Copy the SSE stream to the client while capturing token usage (and the
		// output text for a tokenizer fallback). This is the billing-critical core
		// of the streaming tool path — see streamCaptureUsage. A reasoning-inlining
		// upstream (DeepSeek) also gets its leading <think></think> block stripped
		// from the forwarded content; every other upstream streams unchanged.
		var strip *model.ReasoningStripper
		if model.InlinesReasoning(request.Model) {
			strip = &model.ReasoningStripper{}
		}
		capPrompt, capCompletion, capTotal, completionText := streamCaptureUsage(
			resp.Body, c.Ctx.ResponseWriter, c.Ctx.ResponseWriter.Flush,
			clientWantsUsage, strip, mk,
		)

		// Settle billing with the REAL token usage — captured from the forced
		// usage chunk, or tokenized as a fallback so a successful streamed
		// response is never billed as zero.
		prompt, completion, total := capPrompt, capCompletion, capTotal
		if completion == 0 && total == 0 {
			if pt, err := model.OpenaiNumTokensFromMessages(request.Messages, request.Model); err == nil {
				prompt = pt
			}
			completion, _ = model.GetTokenSize(request.Model, completionText)
		}
		if total == 0 {
			total = prompt + completion
		}
		actualCents := calculateCostCentsWithCache(request.Model, prompt, completion, 0, 0)
		if authUser != nil {
			successRecord := &usageRecord{
				Owner:            ledger,
				Organization:     authUser.Owner,
				Model:            request.Model,
				Provider:         provider.Name,
				Origin:           originOf(provider, mk),
				CostNanoExact:    mk.cogs(),
				PromptTokens:     prompt,
				CompletionTokens: completion,
				TotalTokens:      total,
				Currency:         "USD",
				Premium:          isPremium,
				Stream:           true,
				Status:           "success",
				ClientIP:         c.Ctx.Request.RemoteAddr,
				RequestID:        requestId,
			}
			successRecord.bind(c.Ctx.Request.Context(), authUser)
			successRecord.BYO, successRecord.Account = providerBYO(provider, authUser)
			recordUsage(successRecord)
			recordTrace(c.Ctx.Request.Context(), successRecord, requestStartTime)
		}
		hold.settle(actualCents)
	} else {
		// Non-streaming: read full response, extract token counts, forward
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			c.ResponseError(fmt.Sprintf("Failed to read upstream response: %s", err.Error()))
			return
		}

		// What goes out is ours (envelope.go); what came in is what the billing below
		// reads. Stamping is a disclosure decision, never a pricing one, so the two
		// bodies stay separate — and it happens here so the usage row can record the
		// upstream the stamp just took out of the answer.
		out := mk.stamp(respBody)

		// Try to extract usage for billing
		var upstreamResp struct {
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			} `json:"usage"`
		}
		_ = json.Unmarshal(respBody, &upstreamResp)

		prompt := upstreamResp.Usage.PromptTokens
		completion := upstreamResp.Usage.CompletionTokens
		total := upstreamResp.Usage.TotalTokens
		if completion == 0 && total == 0 {
			// Upstream returned no usage — tokenize so a successful tool response
			// is never billed as zero.
			if pt, err := model.OpenaiNumTokensFromMessages(request.Messages, request.Model); err == nil {
				prompt = pt
			}
			completion, _ = model.GetTokenSize(request.Model, string(respBody))
		}
		if total == 0 {
			total = prompt + completion
		}
		actualCents := calculateCostCentsWithCache(request.Model, prompt, completion, 0, 0)

		if authUser != nil {
			successRecord := &usageRecord{
				Owner:            ledger,
				Organization:     authUser.Owner,
				Model:            request.Model,
				Provider:         provider.Name,
				Origin:           originOf(provider, mk),
				CostNanoExact:    mk.cogs(),
				PromptTokens:     prompt,
				CompletionTokens: completion,
				TotalTokens:      total,
				Currency:         "USD",
				Premium:          isPremium,
				Stream:           false,
				Status:           "success",
				ClientIP:         c.Ctx.Request.RemoteAddr,
				RequestID:        requestId,
			}
			successRecord.bind(c.Ctx.Request.Context(), authUser)
			successRecord.BYO, successRecord.Account = providerBYO(provider, authUser)
			recordUsage(successRecord)
			recordTrace(c.Ctx.Request.Context(), successRecord, requestStartTime)
		}
		hold.settle(actualCents)

		// Strip a reasoning-inlining upstream's leading <think></think> block from
		// the forwarded body (billing above already tokenized the original).
		if model.InlinesReasoning(request.Model) {
			out = stripReasoningBody(out)
		}
		c.Ctx.ResponseWriter.WriteHeader(http.StatusOK)
		c.Ctx.Output.Body(out)
	}
	c.EnableRender = false
}

// resolveUpstreamEndpoint returns the chat completions URL, API key, and
// optional full Authorization header for the given provider. It is a thin
// alias over resolveEndpointForPath so chat, embeddings, and rerank all share
// exactly one per-provider endpoint map.
func resolveUpstreamEndpoint(provider *object.Provider) (url string, apiKey string, authHeader string) {
	return resolveEndpointForPath(provider, "chat/completions")
}

// resolveEndpointForPath returns the upstream URL, API key, and optional full
// Authorization header for the given provider and OpenAI-style API path
// (e.g. "chat/completions", "embeddings", "rerank"). This is the single place
// that knows each provider's base URL and auth scheme; every OpenAI-compatible
// surface is built by varying apiPath only — no per-endpoint copy of provider
// routing exists.
func resolveEndpointForPath(provider *object.Provider, apiPath string) (url string, apiKey string, authHeader string) {
	apiKey = provider.ClientSecret

	switch provider.Type {
	case "OpenAI":
		baseURL := provider.ProviderUrl
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		baseURL = strings.TrimRight(baseURL, "/")
		if !strings.HasSuffix(baseURL, "/v1") {
			baseURL += "/v1"
		}
		return baseURL + "/" + apiPath, apiKey, ""

	case "Fireworks":
		return "https://api.fireworks.ai/inference/v1/" + apiPath, apiKey, ""

	case "Grok":
		return "https://api.x.ai/v1/" + apiPath, apiKey, ""

	case "OpenRouter":
		return "https://openrouter.ai/api/v1/" + apiPath, apiKey, ""

	case "Moonshot":
		return "https://api.moonshot.cn/v1/" + apiPath, apiKey, ""

	case "Gemini":
		// Gemini exposes an OpenAI-compatible surface under /v1beta/openai.
		return "https://generativelanguage.googleapis.com/v1beta/openai/" + apiPath, apiKey, ""

	case "Jina":
		// Jina AI: OpenAI-compatible /v1/embeddings and a native /v1/rerank.
		return "https://api.jina.ai/v1/" + apiPath, apiKey, ""

	case "Cohere":
		// Cohere v1 exposes /v1/embeddings and /v1/rerank.
		return "https://api.cohere.com/v1/" + apiPath, apiKey, ""

	case "Azure":
		baseURL := strings.TrimRight(provider.ProviderUrl, "/")
		apiVersion := provider.ApiVersion
		if apiVersion == "" {
			apiVersion = "2024-02-01"
		}
		return fmt.Sprintf("%s/openai/deployments/%s/%s?api-version=%s",
			baseURL, provider.SubType, apiPath, apiVersion), "", "api-key " + apiKey

	case "Local", "Ollama", "DigitalOcean":
		// Local/compatible providers with custom URLs.
		baseURL := strings.TrimRight(provider.ProviderUrl, "/")
		if baseURL == "" {
			return "", "", ""
		}
		if strings.HasSuffix(baseURL, "/v1") {
			return baseURL + "/" + apiPath, apiKey, ""
		}
		return baseURL + "/v1/" + apiPath, apiKey, ""

	default:
		// Any other OpenAI-compatible provider with a custom URL.
		if provider.ProviderUrl != "" {
			baseURL := strings.TrimRight(provider.ProviderUrl, "/")
			return baseURL + "/" + apiPath, apiKey, ""
		}
		return "", "", ""
	}
}

// proxyToolRequestAnthropic handles tool-calling requests for Claude/Anthropic
// providers by converting the OpenAI format to Anthropic Messages API format
// and converting the response back.
// sku is the model the CALLER named. request.Model already carries the upstream's
// own id by the time this runs, and a refusal must name the SKU the caller can act
// on rather than disclosing the id we buy under.
func (c *ApiController) proxyToolRequestAnthropic(
	provider *object.Provider,
	request *openai.ChatCompletionRequest,
	sku string,
	requestStartTime time.Time,
	authUser *iam.User,
	isPremium bool,
	orgId string,
	requestId string,
	hold *budgetHold,
) {
	// See proxyToolRequest: the same wallet ChatCompletions gated and reserved on.
	ledger := c.billingOrg(authUser)

	apiKey := provider.ClientSecret
	baseURL := provider.ProviderUrl
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	baseURL = strings.TrimRight(baseURL, "/")

	// Convert OpenAI messages to Anthropic format
	var systemPrompt string
	anthropicMessages := []map[string]interface{}{}

	for _, msg := range request.Messages {
		if msg.Role == "system" {
			systemPrompt = msg.Content
			continue
		}

		anthropicMsg := map[string]interface{}{
			"role": msg.Role,
		}

		if msg.Role == "tool" {
			// Tool result message
			anthropicMsg["role"] = "user"
			anthropicMsg["content"] = []map[string]interface{}{
				{
					"type":        "tool_result",
					"tool_use_id": msg.ToolCallID,
					"content":     msg.Content,
				},
			}
		} else if len(msg.ToolCalls) > 0 {
			// Assistant message with tool calls
			content := []map[string]interface{}{}
			if msg.Content != "" {
				content = append(content, map[string]interface{}{
					"type": "text",
					"text": msg.Content,
				})
			}
			for _, tc := range msg.ToolCalls {
				var inputObj interface{}
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &inputObj)
				if inputObj == nil {
					inputObj = map[string]interface{}{}
				}
				content = append(content, map[string]interface{}{
					"type":  "tool_use",
					"id":    tc.ID,
					"name":  tc.Function.Name,
					"input": inputObj,
				})
			}
			anthropicMsg["content"] = content
		} else if len(msg.MultiContent) > 0 {
			content := []map[string]interface{}{}
			for _, part := range msg.MultiContent {
				if part.Type == openai.ChatMessagePartTypeText {
					content = append(content, map[string]interface{}{
						"type": "text",
						"text": part.Text,
					})
				}
			}
			anthropicMsg["content"] = content
		} else {
			anthropicMsg["content"] = msg.Content
		}

		anthropicMessages = append(anthropicMessages, anthropicMsg)
	}

	// Convert OpenAI tools to Anthropic tool format
	anthropicTools := []map[string]interface{}{}
	for _, tool := range request.Tools {
		if tool.Type == openai.ToolTypeFunction {
			anthropicTool := map[string]interface{}{
				"name":        tool.Function.Name,
				"description": tool.Function.Description,
			}
			if tool.Function.Parameters != nil {
				var params interface{}
				raw, _ := json.Marshal(tool.Function.Parameters)
				_ = json.Unmarshal(raw, &params)
				anthropicTool["input_schema"] = params
			} else {
				anthropicTool["input_schema"] = map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				}
			}
			anthropicTools = append(anthropicTools, anthropicTool)
		}
	}

	// Build Anthropic request
	anthropicReq := map[string]interface{}{
		"model":      request.Model,
		"messages":   anthropicMessages,
		"max_tokens": 4096,
		"tools":      anthropicTools,
	}
	if systemPrompt != "" {
		anthropicReq["system"] = systemPrompt
	}
	if request.MaxTokens > 0 {
		anthropicReq["max_tokens"] = request.MaxTokens
	}
	if request.Temperature > 0 {
		anthropicReq["temperature"] = request.Temperature
	}
	// Always fetch the FULL (non-streamed) Anthropic response so the body is
	// parseable JSON carrying token usage for billing. If the client requested
	// streaming, the converted result is re-emitted as SSE below. Previously
	// stream=true made io.ReadAll+json.Unmarshal fail on the SSE body, so streamed
	// tool calls errored AND were never billed.

	body, err := json.Marshal(anthropicReq)
	if err != nil {
		c.ResponseError(fmt.Sprintf("Failed to marshal Anthropic request: %s", err.Error()))
		return
	}

	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		c.ResponseError(fmt.Sprintf("Failed to create Anthropic request: %s", err.Error()))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		c.ResponseError(fmt.Sprintf("Anthropic request failed: %s", err.Error()))
		return
	}
	defer resp.Body.Close()

	// Read full Anthropic response
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		c.ResponseError(fmt.Sprintf("Failed to read Anthropic response: %s", err.Error()))
		return
	}

	if resp.StatusCode != http.StatusOK {
		c.ResponseFailure(relay(sku, provider.Name, resp.StatusCode, respBody))
		return
	}

	// Parse Anthropic response
	var anthropicResp struct {
		ID      string `json:"id"`
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text,omitempty"`
			ID    string          `json:"id,omitempty"`
			Name  string          `json:"name,omitempty"`
			Input json.RawMessage `json:"input,omitempty"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &anthropicResp); err != nil {
		c.ResponseError(fmt.Sprintf("Failed to parse Anthropic response: %s", err.Error()))
		return
	}

	// Convert Anthropic response to OpenAI format
	var contentText string
	var toolCalls []openai.ToolCall
	toolCallIdx := 0

	for _, block := range anthropicResp.Content {
		switch block.Type {
		case "text":
			contentText += block.Text
		case "tool_use":
			tc := openai.ToolCall{
				Index: &toolCallIdx,
				ID:    block.ID,
				Type:  openai.ToolTypeFunction,
				Function: openai.FunctionCall{
					Name:      block.Name,
					Arguments: string(block.Input),
				},
			}
			toolCalls = append(toolCalls, tc)
			toolCallIdx++
		}
	}

	finishReason := openai.FinishReasonStop
	if anthropicResp.StopReason == "tool_use" {
		finishReason = openai.FinishReasonToolCalls
	}

	openaiResp := openai.ChatCompletionResponse{
		ID:      "chatcmpl-" + requestId,
		Object:  "chat.completion",
		Created: util.GetCurrentUnixTime(),
		Model:   request.Model,
		Choices: []openai.ChatCompletionChoice{
			{
				Index: 0,
				Message: openai.ChatCompletionMessage{
					Role:      "assistant",
					Content:   contentText,
					ToolCalls: toolCalls,
				},
				FinishReason: finishReason,
			},
		},
		Usage: openai.Usage{
			PromptTokens:     anthropicResp.Usage.InputTokens,
			CompletionTokens: anthropicResp.Usage.OutputTokens,
			TotalTokens:      anthropicResp.Usage.InputTokens + anthropicResp.Usage.OutputTokens,
		},
	}

	// Record usage
	if authUser != nil {
		successRecord := &usageRecord{
			Owner:            ledger,
			Organization:     authUser.Owner,
			Model:            request.Model,
			Provider:         provider.Name,
			Origin:           provider.Origin(),
			PromptTokens:     anthropicResp.Usage.InputTokens,
			CompletionTokens: anthropicResp.Usage.OutputTokens,
			TotalTokens:      anthropicResp.Usage.InputTokens + anthropicResp.Usage.OutputTokens,
			Currency:         "USD",
			Premium:          isPremium,
			Stream:           false,
			Status:           "success",
			ClientIP:         c.Ctx.Request.RemoteAddr,
			RequestID:        requestId,
		}
		successRecord.bind(c.Ctx.Request.Context(), authUser)
		successRecord.BYO, successRecord.Account = providerBYO(provider, authUser)
		recordUsage(successRecord)
		recordTrace(c.Ctx.Request.Context(), successRecord, requestStartTime)
	}
	hold.settle(calculateCostCentsWithCache(request.Model, anthropicResp.Usage.InputTokens, anthropicResp.Usage.OutputTokens, 0, 0))

	jsonResponse, err := json.Marshal(openaiResp)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	if request.Stream {
		// Client asked for streaming: emit the converted completion as a single
		// SSE chunk followed by [DONE], so OpenAI SDK clients consuming a stream
		// still work while billing used the real (full-response) token usage.
		c.Ctx.ResponseWriter.Header().Set("Content-Type", "text/event-stream")
		c.Ctx.ResponseWriter.Header().Set("Cache-Control", "no-cache")
		c.Ctx.ResponseWriter.Header().Set("Connection", "keep-alive")
		chunk := map[string]interface{}{
			"id":      openaiResp.ID,
			"object":  "chat.completion.chunk",
			"created": openaiResp.Created,
			"model":   openaiResp.Model,
			"choices": []map[string]interface{}{{
				"index": 0,
				"delta": map[string]interface{}{
					"role":       "assistant",
					"content":    contentText,
					"tool_calls": toolCalls,
				},
				"finish_reason": finishReason,
			}},
		}
		if chunkJSON, mErr := json.Marshal(chunk); mErr == nil {
			_, _ = fmt.Fprintf(c.Ctx.ResponseWriter, "data: %s\n\n", string(chunkJSON))
		}
		_, _ = fmt.Fprint(c.Ctx.ResponseWriter, "data: [DONE]\n\n")
		c.Ctx.ResponseWriter.Flush()
		c.EnableRender = false
		return
	}

	c.Ctx.Output.Header("Content-Type", "application/json")
	c.Ctx.Output.Body(jsonResponse)
	c.EnableRender = false
}
