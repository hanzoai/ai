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
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/hanzoai/account"
	"github.com/hanzoai/decimal"

	"github.com/hanzoai/ai/address"
	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/funding"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/upstream"
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

// resolveProviderFromJwt validates a hanzo.id JWT token and returns the
// appropriate model provider for the requested model, plus the translated
// upstream model name.
//
// requested is the raw X-Org-Id the caller asked to act in ("" for none). This is
// the ONE auth path that can honor an org switch, because it is the one that
// holds the signed `orgs` claim proving membership — the ledger is resolved here,
// from those claims, rather than re-parsing the token downstream.
func resolveProviderFromJwt(ctx context.Context, token string, requested string, requestedModel string, lang string) (*object.Provider, *iam.User, string, error) {
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
	return resolveProviderForUser(ctx, user, ledger, requestedModel, lang)
}

// resolveProviderFromIAMKey validates an IAM API key (sk-{accessKey})
// and returns the model provider + user, same as JWT path.
//
// An IAM key carries no signed `orgs` claim, so it can never switch org: it bills
// the org that owns the key, which is its home org.
func resolveProviderFromIAMKey(ctx context.Context, apiKey string, requestedModel string, lang string) (*object.Provider, *iam.User, string, error) {
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
			return resolveProviderForUser(ctx, fallbackUser, fallbackUser.Owner, requestedModel, lang)
		}
		return nil, nil, "", authError("API key validation failed: %s", err.Error())
	}
	if user == nil {
		return nil, nil, "", authError("invalid API key")
	}

	return resolveProviderForUser(ctx, user, user.Owner, requestedModel, lang)
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
	// A secret is compared in constant time, as the two other places in this module
	// that compare one already do: an ordinary != stops at the first differing
	// byte, and how long it took is a measurement a caller can make.
	if knownKey == "" || subtle.ConstantTimeCompare([]byte(apiKey), []byte(knownKey)) != 1 {
		return nil
	}
	// This identity is assembled HERE rather than read from IAM, so it must state
	// what IAM would have stated for it. Left unsaid it read as a PERSON:
	// account.Payer's shape rule hands a person in the signup org a personal
	// wallet, and "hanzo" IS the signup org — so every call on this key addressed
	// hanzo/cloud-agent, a wallet no funding path can name, which reads $0 while
	// the org's balance sits one key away.
	//
	// The LEDGER is named outright and the type carries only attribution. In this
	// org an org account is the platform's own balance, so which account pays is a
	// statement the assembling code makes on the strength of the KMS secret it just
	// verified — not something inferred from a class, which is a thing rows can
	// also carry. This is the same reason the provider-key and widget identities
	// name theirs.
	return &iam.User{
		Owner:          account.SignupOrg,
		Name:           "cloud-agent",
		Type:           iam.Machine, // attribution: no person behind this call
		BillingAccount: account.Org(account.SignupOrg).String(),
	}
}

// resolveProviderForUser is the shared logic for JWT and API key auth paths.
// Given a validated user, resolves the model route and provider.
//
// ledger is the org that PAYS for this request (account.LedgerOrg). It selects both
// the org's own BYOK provider and the wallet the balance gate reads, so a request
// billed to an org is served with that org's connected key — the two cannot name
// different tenants.
func resolveProviderForUser(ctx context.Context, user *iam.User, ledger string, requestedModel string, lang string) (*object.Provider, *iam.User, string, error) {
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
	if gateErr := enforceBalanceGate(ctx, user, ledger, requestedModel); gateErr != nil {
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
func enforceBalanceGate(ctx context.Context, user *iam.User, ledger string, requestedModel string) error {
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
	// Nor does a request the plan or a free cap pays for: no wallet is asked about it.
	if covered(ctx) {
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

// PublishableOrg resolves a publishable pk- to the org that holds it. Exported so
// the router's tenant resolver answers a page key the same way the controller
// does — ONE credential resolver, one tenant, one IAM transport, exactly as
// GetUserByAccessKey is exported for the secret half.
func PublishableOrg(accessKey string) (string, error) { return publishableOrg(accessKey) }

// The floor a key clears before this process will ask IAM about it. IAM mints
// "{pk|sk}-{live|test}-{random}" (iam internal/keys.Mint), so anything outside
// this alphabet and length is not a key this estate ever issued and needs no round
// trip to refuse. The floor is deliberately wider than the mint: it exists to drop
// junk, not to re-implement the key format, and a key whose shape is plausible is
// still decided by IAM.
//
// The two halves carry their own prefix rather than sharing one pattern, so the
// publishable resolver still refuses a secret locally and the secret resolver still
// refuses a publishable — each answers about its own kind of key, and asking the
// wrong one is a question this process can settle without IAM.
const keyBody = `-[A-Za-z0-9_-]{8,128}$`

var (
	publishableKeyShape = regexp.MustCompile(`^pk` + keyBody)
	secretKeyShape      = regexp.MustCompile(`^sk` + keyBody)
)

const (
	// How long a resolved org stays fresh, and how long a refusal is remembered.
	// The refusal is shorter because a key can become valid — it was just minted,
	// or IAM was briefly unreachable — and nothing should stay denied for a full
	// positive lifetime over a blip.
	publishableOrgTTL  = 5 * time.Minute
	publishableDenyTTL = 30 * time.Second

	// A bound on distinct keys remembered at once. The keys arriving here are
	// whatever the internet sends, so the map must not grow with them.
	publishableCacheMax = 8192
)

// publishableAnswer is what the endpoint said about one key, refusals included.
type publishableAnswer struct {
	org      string
	err      error
	answered time.Time
}

var (
	publishableMu    sync.RWMutex
	publishableCache = map[string]publishableAnswer{}
)

// publishableOrg is resolveOrgFromPublishableKey with a memory, and it is what
// every caller should use.
//
// REMEMBERING THE REFUSALS IS THE POINT. A publishable key ships in the source of
// a page, so the keys presented here are whatever anyone cares to send. Asking IAM
// about each one turns a public endpoint into a load generator aimed at the single
// service every other credential path also depends on — and the way that failure
// shows up is a pk- request resolving no org, which is the one answer an attacker
// would like to induce. Two things stop it: a shape check that costs nothing, and
// a remembered "no" that costs a map read.
//
// It is also what makes the two resolvers AGREE. A single request can ask for this
// org more than once — the tenant resolver and the model resolver both do — and two
// live round trips can answer differently when IAM is flaky, leaving one request
// scoped to one org and billed to another. One answer per key, shared.
func publishableOrg(accessKey string) (string, error) {
	if !publishableKeyShape.MatchString(accessKey) {
		return "", authError("invalid publishable key")
	}
	publishableMu.RLock()
	a, ok := publishableCache[accessKey]
	publishableMu.RUnlock()
	if ok && time.Since(a.answered) <= publishableFresh(a) {
		return a.org, a.err
	}
	org, err := resolveOrgFromPublishableKey(accessKey)
	publishableRemember(accessKey, publishableAnswer{org: org, err: err, answered: time.Now()})
	return org, err
}

// publishableFresh is how long THIS answer stands.
func publishableFresh(a publishableAnswer) time.Duration {
	if a.err != nil {
		return publishableDenyTTL
	}
	return publishableOrgTTL
}

// publishableRemember stores an answer, dropping what has expired when the map
// reaches its bound and starting over if that was not enough.
//
// STARTING OVER IS INDUCIBLE, and saying otherwise would overstate what this is.
// Refusals are remembered too, so enough shape-valid keys — none of which IAM has
// to recognise — fill the bound while still fresh and force the reset, which costs
// every live key a round trip. The shape floor drops junk for free, but past it an
// attacker still buys one IAM call per distinct key. This is a latency and
// duplicate-work optimisation; the ceiling on what reaches IAM is the rate limiter,
// not this map.
func publishableRemember(accessKey string, a publishableAnswer) {
	publishableMu.Lock()
	defer publishableMu.Unlock()
	if len(publishableCache) >= publishableCacheMax {
		for k, v := range publishableCache {
			if time.Since(v.answered) > publishableFresh(v) {
				delete(publishableCache, k)
			}
		}
		if len(publishableCache) >= publishableCacheMax {
			publishableCache = map[string]publishableAnswer{}
		}
	}
	publishableCache[accessKey] = a
}

// resolveOrgFromPublishableKey resolves a publishable pk- to the ORG that holds
// it, via IAM's publishable endpoint (/v1/iam/keys/org) — the exact dual of
// getUserByAccessKey, which answers only for secret keys and refuses a pk- as
// key_wrong_door. Same confidential Basic transport, same envelope, same typed
// refusals; the answer is an org and never a person.
func resolveOrgFromPublishableKey(accessKey string) (string, error) {
	iamEndpoint := conf.GetConfigString("IAM_URL")
	if iamEndpoint == "" {
		return "", fmt.Errorf("IAM_URL is not configured")
	}
	iamEndpoint = strings.TrimRight(iamEndpoint, "/")
	reqURL := fmt.Sprintf("%s/v1/iam/keys/org?accessKey=%s", iamEndpoint, url.QueryEscape(accessKey))
	clientId, clientSecret := iamClientCreds()
	if clientId == "" || clientSecret == "" {
		return "", fmt.Errorf("IAM client credentials are not configured")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("IAM request build failed: %w", err)
	}
	req.SetBasicAuth(clientId, clientSecret)
	resp, err := client.Do(req)
	if err != nil {
		// The key travels in the query string, so a transport failure carries it:
		// *url.Error prints the whole URL. Redacted before it becomes an error value,
		// because from here it is logged and wrapped further.
		return "", fmt.Errorf("IAM request failed: %s", object.RedactKeys(err.Error()))
	}
	defer resp.Body.Close()
	var result struct {
		Status string `json:"status"`
		Msg    string `json:"msg"`
		Code   string `json:"code"`
		Data   struct {
			Org string `json:"org"`
		} `json:"data"`
	}
	decodeErr := json.NewDecoder(resp.Body).Decode(&result)
	if decodeErr == nil && (result.Code != "" || result.Status != "ok") {
		return "", keyRefusal(result.Code, result.Msg, accessKey)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("IAM returned status %d and named no reason", resp.StatusCode)
	}
	if decodeErr != nil {
		return "", fmt.Errorf("failed to parse IAM response: %w", decodeErr)
	}
	if result.Data.Org == "" {
		return "", authError("publishable key resolved to no org")
	}
	return result.Data.Org, nil
}

// getUserByAccessKey looks up a user by their IAM API key via Hanzo IAM.
func getUserByAccessKey(accessKey string) (*iam.User, error) {
	// The dual of the floor publishableOrg applies to a pk-, and it is here rather
	// than at any one call site because this is the ONE place a secret key is
	// resolved: the controller, the balance gate and the authz filter all arrive
	// through it, and a floor at one door is a floor nobody else stands behind.
	//
	// It refuses in IAM's OWN words. A holder who presents a retired spelling, a
	// typo, or another vendor's key needs the one sentence that ends the problem —
	// mint a new key — and which side of the wire decided that is no business of
	// theirs. Saying it here also leaves nothing to probe: a shape this estate never
	// mints and a key it minted and revoked answer identically, so the floor cannot
	// be used to learn the format.
	if !secretKeyShape.MatchString(accessKey) {
		return nil, keyRefusal("key_unknown", "", accessKey)
	}

	iamEndpoint := conf.GetConfigString("IAM_URL")
	if iamEndpoint == "" {
		return nil, fmt.Errorf("IAM_URL is not configured")
	}
	iamEndpoint = strings.TrimRight(iamEndpoint, "/")
	data, err := answerFor(iamEndpoint, accessKey, func() (json.RawMessage, bool, error) {
		return askForKey(iamEndpoint, accessKey)
	})
	if err != nil {
		return nil, err
	}
	var u iam.User
	if err := json.Unmarshal(data, &u); err != nil {
		return nil, fmt.Errorf("failed to parse IAM response: %w", err)
	}
	return &u, nil
}

// askForKey asks IAM which principal a secret key speaks for: the principal's JSON,
// or IAM's refusal. final says the answer is about the key, so it may be held; a
// fault in reaching or reading IAM is not.
func askForKey(iamEndpoint, accessKey string) (json.RawMessage, bool, error) {
	// Per global rule: /v1/ only, never /api/. A path under /api/ is intercepted
	// by the @hanzo/id SPA ingress and returns HTML, which broke API-key
	// resolution once ("invalid character '<'").
	//
	// This is the SECRET-key endpoint, and it is its own route rather than a CRUD
	// read. Resolving a key used to ride on the user read as
	// `get-user?accessKey=` — an authentication boundary reached through a verb
	// whose target was a credential rather than the owner/name that read
	// authorizes on. It resolves an sk- to the full principal; the publishable
	// pk- has a separate endpoint (resolveOrgFromPublishableKey) that yields an org
	// and never a person, so the browser-safe disclosure and this one are never
	// behind a single authorization decision.
	//
	// The segments name things — a key, its principal — and the method says the
	// verb. IAM refuses a new verb-noun address outright; the one sibling that
	// still reads as one (resolve-key, below) is a frozen spelling live
	// consumers hard-code, and that list only ever shrinks.
	reqURL := fmt.Sprintf("%s/v1/iam/keys/principal?accessKey=%s", iamEndpoint, url.QueryEscape(accessKey))

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
		return nil, false, fmt.Errorf("IAM client credentials are not configured")
	}

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, false, fmt.Errorf("IAM request build failed: %w", err)
	}
	req.SetBasicAuth(clientId, clientSecret)
	resp, err := client.Do(req)
	if err != nil {
		// The key travels in the query string, so a transport failure carries it:
		// *url.Error prints the whole URL. Redacted before it becomes an error value,
		// because from here it is logged and wrapped further.
		return nil, false, fmt.Errorf("IAM request failed: %s", object.RedactKeys(err.Error()))
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
		Status string          `json:"status"`
		Msg    string          `json:"msg"`
		Code   string          `json:"code"` // WHY, when IAM refused (iam store.KeyFailure)
		Data   json.RawMessage `json:"data"`
	}
	decodeErr := json.NewDecoder(resp.Body).Decode(&result)

	// A refusal IAM named is a refusal we can relay, whatever the status line. It is
	// about the key only when it carries a code.
	if decodeErr == nil && (result.Code != "" || result.Status != "ok") {
		return nil, result.Code != "", keyRefusal(result.Code, result.Msg, accessKey)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("IAM returned status %d and named no reason", resp.StatusCode)
	}
	if decodeErr != nil {
		return nil, false, fmt.Errorf("failed to parse IAM response: %w", decodeErr)
	}

	// ok-with-nobody. IAM said the request succeeded and named no user, which this
	// used to relay as (nil, nil) — no error, no principal — so the caller fell to
	// its bare "invalid API key" with nothing behind it. That message is the one
	// shape a holder cannot act on and an operator cannot trace: it looks identical
	// whether the key is wrong, the envelope changed, or IAM answered about someone
	// who no longer exists. A key that resolves to nobody IS unusable, so refuse it
	// — but say which of the two things happened, and keep the redacted prefix so
	// the line names the credential without disclosing it.
	if len(result.Data) == 0 || string(result.Data) == "null" {
		return nil, true, authError(
			"API key %s resolved to no user — IAM accepted the lookup and returned nothing. "+
				"The key may have been deleted, or its owner removed. Mint a new one at "+KeysURL(""),
			keyHint(accessKey))
	}
	var u iam.User
	if err := json.Unmarshal(result.Data, &u); err != nil {
		return nil, false, fmt.Errorf("failed to parse IAM response: %w", err)
	}
	return result.Data, true, nil
}

// KeysURL is where a holder mints a key: the API-keys page on the console of the
// brand host belongs to, "" for a brand that runs none. It is spelled ONCE because
// every refusal below names it and three copies of an address drift the moment the
// page moves. It moved already: these messages sent people to cloud.hanzo.ai/keys,
// which answers 404 (cloud.hanzo.ai is the product site; the console is its own
// host, and its key surface is /api-keys). A refusal that names a cure the holder
// cannot reach is worse than one that names none. The key refusals below hold no
// request and pass "", the default brand.
func KeysURL(host string) string {
	if c := brandDefs[brandFromHost(host)].console; c != "" {
		return "https://" + c + "/api-keys"
	}
	return ""
}

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
		// IT SAYS WHAT IS KNOWN, WHICH IS THAT THE KEY DOES NOT RESOLVE.
		//
		// This code carries no history. It is the same answer for a key that was
		// revoked, one that was replaced, and one that never resolved from the
		// moment it was issued — and the third really happens: a service account's
		// credential was written to a column nothing reads, so it answered this
		// code for its entire life (iam internal/serviceaccounts). Saying "it was
		// revoked or replaced" turned that into a hunt for a revocation nobody
		// performed, on keys that had never worked once.
		//
		// The cure is the same either way, so the sentence keeps it and drops the
		// story it cannot support.
		return fmt.Errorf("API key %s does not resolve — mint a new one at "+
			KeysURL(""), keyHint(key))
	case "key_wrong_door":
		return fmt.Errorf("API key %s is not a secret key. A publishable pk- key "+
			"identifies an org for ingest and cannot authenticate a request; "+
			"use your secret (sk-) key", keyHint(key))
	case "key_expired":
		return fmt.Errorf("API key %s has expired — mint a new one at "+
			KeysURL(""), keyHint(key))
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
	Owner        string `json:"owner"`
	User         string `json:"user"`
	Organization string `json:"organization"`
	Model        string `json:"model"`
	Provider     string `json:"provider"`
	// PromptTokens is EVERY input token on every path; CacheReadTokens and
	// CacheWriteTokens are parts of it, read from and written to the upstream's
	// prompt cache, each billed at its own rate (tokenNanoAt).
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
	// ref names this record's debit for the ledger (object.UsageEvent.Ref): minted
	// the first time the record is filed and kept, so filing it again — a retry after
	// a lost answer — is the same debit.
	ref string
	// ClientRequestID is the X-Request-Id the caller was answered under, where the
	// surface answers with one. It is what the row's request_id column shows, so a
	// caller's id finds their bill; RequestID stays the row's own, minted here, and is
	// never a value a caller chose.
	ClientRequestID string `json:"clientRequestId,omitempty"`

	// plan is the grant of the plan that covers this call (Cover), nil when none
	// does: the call debits no wallet and counts against no free allowance. planNanos
	// is what its paid upstream cost, settled against the plan's budget (planCost).
	plan      *object.LimitGrant
	planNanos int64
	// cash says the host's grant lets only the wallet's cash pay for this call: the
	// model's policy refuses granted credit (object.UsageEvent.Cash).
	cash bool
	// key is the API key the call arrived on (object.UsageEvent.Key), set by bind.
	key string
	// seat is the call's seat on the paid lane (lane.go), set by bind: what it spent
	// settles it, against the platform's paid-lane day and its org's share.
	seat *seat

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

	// DecisionCount is the number of decisions answered (POST /v1/decisions), one
	// per call. When > 0, usageCostNano prices the call by decisionCostNano: its
	// input tokens at the model's price.
	DecisionCount int `json:"decisionCount,omitempty"`

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
	// Routing is the router's whole decision (`Routing`, encoded) when one routed this
	// request; emitted on the span as gen_ai.hanzo.routing. Never persisted.
	Routing string `json:"-"`
	// Served is the arm that generated the answer when a family names one
	// (X-Hanzo-Served) — the model behind an adaptive SKU. Emitted as
	// gen_ai.response.model; empty leaves response.model the SKU.
	Served string `json:"served,omitempty"`
	// Vendor is the provider that ran that arm ("digitalocean", "openrouter").
	// Emitted as gen_ai.provider.name, ahead of Provider, which is our route label.
	Vendor string `json:"vendor,omitempty"`
	// Failover is the chain of arms that failed before the one that answered, as
	// the family reported it. Empty when the first arm answered.
	Failover string `json:"failover,omitempty"`
	// ReasoningTokens is the part of CompletionTokens the model spent reasoning.
	ReasoningTokens int `json:"reasoningTokens,omitempty"`
	// First is the time from the start of the call to the first token of the
	// answer. Zero when not measured.
	First time.Duration `json:"-"`
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

	// Visitor is the lanes a public-lane call is counted on, narrowest first. Only
	// the public lane puts one on the request (withVisitor); every other call is
	// empty here. Not serialized — it addresses the lane's count, it does not
	// describe the row.
	Visitor []address.Bucket `json:"-"`
}

// reached answers whether a vendor was committed to this call — the question the money
// turns on, since a vendor that ran a request invoices us for it whatever came back.
//
// Provider is where that fact already lives. Every emit site fills it from the row that
// SERVED or REFUSED the request, and leaves it empty when the request died before any
// vendor was chosen: an unresolvable route, a candidate list that ran out, a caller who
// hung up. `served` says the same thing from the other side — a failed attempt carries
// a name and nothing else.
//
// IT IS A NAME, NOT A RECEIPT. It says a vendor was committed to this request, not that
// the vendor billed us for it — a 400 the upstream rejected on sight usually costs us
// nothing. That is the honest limit of what a record knows here, and the error it makes
// is the harmless one: a zero-cost row filed for a vendor that charged nothing, rather
// than no row at all for one that did.
func (r *usageRecord) reached() bool { return r.Provider != "" }

// answered reports that a model produced a result for this call. Only the public
// lane's own count asks it — see recordUsage.
func (r *usageRecord) answered() bool { return r.Status == "success" }

// spent reports the tokens a call produced, for a call that then failed.
//
// reached() says a vendor ran the request; this says how much of it it ran. Without
// both, a failure files a row naming a vendor and a quantity of zero, which prices a
// half-generated answer at nothing and leaves us holding the invoice for it.
//
// The pipeline's own count is authoritative whenever it filled one. Otherwise the
// prompt was already counted before the call went out — routeForPrompt sizes the route
// with it — and the partial answer is whatever reached the writer before the stream
// broke. Neither number is invented: one was measured on the way out, the other is the
// text the vendor actually sent.
func spent(res *model.ModelResult, name string, prompt int, partial string, err error) (int, int) {
	if res != nil && res.TotalTokenCount > 0 {
		return res.PromptTokenCount, res.ResponseTokenCount
	}
	completion, _ := model.GetTokenSize(name, partial)
	if completion == 0 && !ran(err) {
		// Nothing came back and nobody ran it: the vendor reported no meter of its
		// own, no text reached the caller, and the refusal is one that arrives at
		// the edge. The prompt was measured on the way out — routeForPrompt sizes
		// the route with it — but measuring a prompt is not a vendor processing it,
		// and there is no invoice behind this to pass on.
		//
		// It is the same reasoning recordUsage states for a call that never left,
		// applied to the quantity rather than the row. Left unsaid, the charge
		// scales with the prompt, so the larger the request the more it costs to be
		// turned away.
		return 0, 0
	}
	return prompt, completion
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
	// The API key the call arrived on, as the boundary named it: what the host
	// counts the key's own spend against.
	r.key = object.GenAIAttributionFromContext(ctx).Key
	r.seat = seatOf(ctx)
	// The public lane's visitor, read from the request the lane alone writes it on.
	r.Visitor = visitorOf(ctx)
	// Who the host said pays: a covered call settles against its grant and debits no
	// wallet; one sent to the wallet carries whether only cash may pay.
	if g := grantFrom(ctx); g.Covered() {
		if r.plan == nil {
			r.plan = g
		}
	} else if g != nil {
		r.cash = g.Cash
	}

	self := u.Owner + "/" + u.Name
	// account.IsMachine is the ONE predicate for "is this credential a program",
	// shared with the payer rule above.
	if !account.IsMachine(u.Type) {
		r.User = self
		return
	}

	r.Agent = self
	if p := person(ctx, u); p != "" {
		r.User = p
		return
	}
	r.User = ""
	warnUnownedOnce(self)
}

// person is the person a machine credential's call is for: whoever the identity
// boundary authenticated before the machine placed it (X-User-Id, "<org>/<name>").
// "" for a person's own credential, which names itself, and for a machine that names
// nobody else.
func person(ctx context.Context, u *iam.User) string {
	if u == nil || !account.IsMachine(u.Type) {
		return ""
	}
	named := strings.TrimSpace(object.GenAIAttributionFromContext(ctx).User)
	// …and the name must be SOMEONE ELSE. A caller reaching us through the identity
	// boundary never chooses this header: the boundary deletes what arrived and
	// rewrites it from the presenting credential's own claims, so a machine that
	// came that way is handed ITS OWN subject. Copying that into the person column
	// puts the application back where this whole rule exists to keep it out of, one
	// hop later and harder to see.
	//
	// The two spellings differ in the org half — a token's `sub` is qualified by the
	// registration's owner and the `owner` claim by the org it serves — so the NAME
	// is what identifies the principal across them.
	if _, name, ok := strings.Cut(named, "/"); ok && !strings.EqualFold(name, u.Name) {
		return named
	}
	return ""
}

// tenant is the org a model family is told a call is for (X-Org-Id): the customer a
// program in this process stated (object.Caller), else the org of the person a
// machine credential acts for (person), else the org the request bills, else the
// caller's own. A sibling's call over the plane is the platform working for that
// person's org, and the family keeps its per-org share by it. Money is not moved by
// it: ai settles the call, and the family is told so (X-Hanzo-Fronted-By).
func tenant(ctx context.Context, org string, u *iam.User) string {
	if who, ok := object.CallerOf(ctx); ok {
		return who.Org
	}
	if o, _, _ := strings.Cut(person(ctx, u), "/"); o != "" {
		return o
	}
	if org != "" {
		return org
	}
	if u != nil {
		return u.Owner
	}
	return ""
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

// shownID is the request id the row files under in its request_id column: the one
// the caller was answered under when there was one, else the row's own.
func (r *usageRecord) shownID() string {
	if r.ClientRequestID != "" {
		return r.ClientRequestID
	}
	return r.RequestID
}

// paidBy names who pays for a recorded call when it is not the wallet: the plan
// that covered it, or the platform for a free model it absorbs. "" is the wallet.
func paidBy(record *usageRecord) string {
	switch {
	case record.plan != nil && record.plan.Pays == object.PaysCaller:
		return object.PaysCaller
	case record.plan != nil:
		return object.PaysPlan
	case usageFree(record):
		return "hanzo"
	}
	return ""
}

// nanoUSD renders a nano-dollar cost as exact decimal USD, "" when unknown.
func nanoUSD(nano *int64) string {
	if nano == nil {
		return ""
	}
	return decimal.New(*nano, 9).String()
}

// usageTimeout bounds one debit's hand-off to the ledger. The native recorder
// answers once the debit is durably posted, which is milliseconds; a ledger that
// has not answered in this long has failed, and the failure is logged with the
// request id instead of holding a goroutine — and, on a path that files the debit
// before its reply, the caller — for as long as the ledger stays silent. It is kept
// well inside a pod's shutdown budget, so a debit filed after its reply can be tried
// again before the process goes (settleAfter).
var usageTimeout = 5 * time.Second

// recordUsage files what a call spent, and returns the native recorder's refusal
// when the debit did not land. It is the ONE chokepoint between a request and
// the money: an exact debit on the org's wallet where this build is co-resident with
// the ledger, or the same amount on its way to Commerce where it is not.
//
// A CALL THAT REACHED A VENDOR IS BILLED, ANSWER OR NO ANSWER. The rule here used to be
// "success only", which reads as fairness and is not one: by the time a call fails the
// request has been sent, the vendor has run it, and we have been invoiced for the tokens
// it processed. Filing nothing does not undo any of that — it only removes the line that
// says it happened, and leaves the spend with us.
//
// THE TRADE IS DELIBERATE AND IT FALLS ON THE CALLER. A request they broke costs them
// what it cost us to try. Keeping our side answering is our job and stays our job; what
// a caller does to their own request is theirs. The alternative prices a failing request
// at zero, which is the one property an unbounded workload must never have — the cheapest
// call in the catalogue would be the one that breaks, and whoever sends the most of them
// would pay the least.
//
// WHAT IT COST IS THE SAME ONE COST. usageCostCents / usageBilledUSD are asked here for a
// failure exactly as for a success, so the row and its o11y gen_ai span carry the
// identical number and cannot disagree. A failure that came back with no tokens therefore
// bills nothing — not because failure is free, but because that is what the meter reads,
// and reading it is the whole point.
func recordUsage(record *usageRecord) error {
	// Dense flywheel reward (HIP-510): score EVERY routed request's outcome and
	// attach it to its routing decision, so the bandit learns from request-volume
	// signal instead of sparse explicit thumbs. An errored request scores 0 (the arm
	// is penalized for failing). Dark-by-default, async, best-effort — never slows or
	// fails the request.
	emitAutoRoutingReward(record)

	// NOTHING WAS SPENT ON A CALL THAT NEVER LEFT, so nothing is filed for one. There is
	// no invoice behind it and therefore no amount, and an amount is not a thing to
	// invent because a row would look tidier with one. The call is still visible — the
	// emit sites write the warehouse row and the span for it either way; what it does
	// not get is a place in the money.
	if !record.reached() {
		if record.plan != nil {
			record.plan.Settle(0)
		}
		return nil
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

	// A call the caller's plan covers settles against the plan: what it spent on
	// paid upstream, against the budget the plan held for it. It debits no wallet and
	// counts against no free allowance — the plan counted it when it was admitted.
	if record.plan != nil {
		record.plan.Settle(planUse(record))
	}
	// A call on the paid lane settles its seat at what it spent, whoever paid: the
	// hold taken at admission is given back and the spend counted against the
	// platform's day and its org's share. After the plan's settle, so the payer's next
	// call reads the host's figure with this one in it before this hold is gone.
	record.seat.settle(laneSpend(record))
	// The public lane keeps its own count in this process, by the visitor's address
	// and the site it shares, so its ceiling holds while the host is unreachable. It
	// rises on an answer; the host's count was taken when the lane admitted the call.
	//
	// A VISITOR ON THE RECORD IS WHAT SAYS THIS IS THAT LANE. Only the lane puts one
	// there, and nothing on the request can name one. The org would be the obvious
	// test and is the wrong one: it is resolved from X-Org-Id, which any caller sets,
	// so a bound that read it could be steered out of the lane it bounds.
	if record.plan == nil && record.answered() && len(record.Visitor) > 0 && usageFree(record) {
		publicCount.serve(record.Visitor, utcDay(time.Now()), publicChatDaily())
	}

	// Native in-proc finance debit — the ONE money path when co-resident with the
	// finance ledger (hanzoai/cloud unified binary). The debit lands DIRECTLY on the
	// org's SQLite wallet, the SAME account the prepaid gate reads, so a funded
	// account both gates AND depletes. When the hook is installed we do NOT also
	// enqueue to Commerce — that would double-bill. Standalone ai (no hook) falls
	// through to the HTTP billing queue below.
	if rec := object.UsageRecorder(); rec != nil {
		ctx, cancel := context.WithTimeout(context.Background(), usageTimeout)
		defer cancel()
		if record.ref == "" {
			record.ref = uuid.NewString()
		}
		if err := rec(ctx, object.UsageEvent{
			Subject:   subject,
			Namespace: org,
			USD:       usageBilledUSD(record), // EXACT atto-precise debit, never a floored cent
			Currency:  "usd",
			Model:     record.Model,
			Provider:  record.Provider,
			Actor:     record.User,
			Plan:      record.plan != nil,
			Cash:      record.cash,
			RequestID: record.RequestID,
			Ref:       record.ref,
			Class:     ClassOf(record.Model),
			Units:     int64(record.TotalTokens),
			// The COGS the row and the span read (usageMargin): a family's or a
			// vendor's own statement where one was made, else the rate table's.
			CostUSD: nanoUSD(usageMargin(record).CostNano),
			PaidBy:  paidBy(record),
			Key:     record.key,
		}); err != nil {
			log.Error("billing: native usage record failed request_id=%s: %v", record.RequestID, err)
			return err
		}
		return nil
	}

	if billingQueue == nil {
		return nil
	}

	// The SAME facts the native event carries, in the shape Commerce reads them. Money
	// alone: a free call's count was taken at admission (object.SpentFunc), so no
	// writer carries one, and what a call spent does not depend on which writer a
	// build has.
	payload := map[string]any{
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
		"decisionCount":    record.DecisionCount,
		"audioSeconds":     record.AudioSeconds,
		"audioChars":       record.AudioChars,
		"requestId":        record.RequestID,
		"clientRequestId":  record.ClientRequestID,
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
		return nil
	}

	billingQueue.Enqueue(&util.BillingRecord{
		Body:      body,
		RequestID: record.RequestID,
		Org:       org,
		Model:     record.Model,
	})
	return nil
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
// validation (authResolveProvider).
//
// It is a strict SUBSET of authResolveProvider's branches, and the omissions are
// the point: a pk- never reaches here (every generative endpoint refuses it before
// the body is read) and a run key authenticates only against the run table, so
// neither is admitted by this check. A branch missing here can only REFUSE a
// request the resolver would have taken — never admit one it would have refused —
// which is the direction a credential check is allowed to be wrong in.
func (c *ApiController) authenticate(token string) error {
	return authenticateToken(token, c.GetAcceptLanguage())
}

// authenticateToken is authenticate with the request's language passed in, for a
// caller that holds no controller (a ZAP handler).
func authenticateToken(token, lang string) error {
	switch {
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
		provider, err := object.GetProviderByProviderKey(token, lang)
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
// ORG — never a per-person wallet no one funds. A provider with no owner is
// unattributable: return an auth error so the caller refuses rather than spend
// the shared upstream key for free — the invariant is that every call spending
// the shared upstream key bills someone.
//
// It NAMES that ledger rather than implying it through a class. This identity is
// synthesized HERE, from a provider row this process just read — there is no
// person and no token — so the org is a fact the calling code holds, and it says
// so in the field that carries such statements. Marking it "application" and
// letting the payer infer the org worked everywhere except the one org where it
// mattered: in the signup org an org account is the platform's own balance, so an
// inferred class was the difference between billing a tenant and spending Hanzo's
// money. A stated ledger cannot be confused with an asserted class.
func providerKeyBillingUser(provider *object.Provider) (*iam.User, error) {
	if provider == nil || strings.TrimSpace(provider.Owner) == "" {
		return nil, authError("provider key is not attributable to a billable owner")
	}
	owner := strings.TrimSpace(provider.Owner)
	return &iam.User{
		Owner:          owner,
		Type:           iam.Machine, // attribution: this call has no person behind it
		BillingAccount: account.Org(owner).String(),
	}, nil
}

// authResolveProvider authenticates a bearer token and resolves the requested
// model to its upstream provider. It is the single auth + model-routing policy
// for every OpenAI-compatible surface (chat, embeddings, rerank): each handler
// calls it instead of re-implementing the IAM/JWT/provider-key branches.
//
// Returns the resolved provider (with KMS-resolved secret), the billed user
// (nil for provider-key auth), the upstream model id, and whether the route is
// premium. Errors are returned pre-formatted for ResponseError.
func (c *ApiController) authResolveProvider(token, requestedModel, orgId string) (provider *object.Provider, authUser *iam.User, upstreamModel string, isPremium bool, err error) {
	lang := c.GetAcceptLanguage()
	ctx := c.Context()

	switch {
	case isRunKey(token):
		// Run key (hrun_...) — an autonomous run buying inference on the ledger of
		// the org that started it. It resolves to a machine principal and to NO
		// user, so it authenticates nobody and opens no other surface; see run.go.
		// The org rides the TOKEN, never orgId, so a run cannot be pointed at
		// another tenant's balance by a header.
		provider, authUser, upstreamModel, err = resolveProviderFromRunKey(ctx, token, requestedModel, lang)
		if err != nil {
			err = wrapAuth(err)
			return
		}
		c.Locals("recordUserId", authUser.Owner+"/run")
		return

	case isJwtToken(token):
		// hanzo.id JWT token — full model routing + billing. Same typed-status
		// contract as the IAM key path. The raw X-Org-Id goes in unvalidated: the
		// resolver holds the signed `orgs` claim and is the only place allowed to
		// decide whether the request may act — and pay — in that org.
		provider, authUser, upstreamModel, err = resolveProviderFromJwt(ctx, token, strings.TrimSpace(c.Header("X-Org-Id")), requestedModel, lang)
		if err != nil {
			err = wrapAuth(err)
			return
		}

	case isPublishableKey(token):
		// Publishable key (pk-...) — the read-only credential class. It reaches
		// only the surfaces that do not refuse it up front (embeddings; every
		// generative handler rejects pk- before calling here). IAM's publishable
		// endpoint answers with the ORG that holds the key and never a person, so
		// the call bills that org as a machine — the same shape as a provider
		// key. This is the credential the cloud deployment documents for its
		// embed client: least privilege for a read-only endpoint.
		org, kerr := publishableOrg(token)
		if kerr != nil {
			err = wrapAuth(kerr)
			return
		}
		machine := &iam.User{
			Owner:          org,
			Type:           iam.Machine, // attribution: no person is behind a page key
			BillingAccount: account.Org(org).String(),
		}
		provider, authUser, upstreamModel, err = resolveProviderForUser(ctx, machine, org, requestedModel, lang)
		if err != nil {
			err = wrapAuth(err)
			return
		}
		c.Locals("recordUserId", org+"/publishable")

	default:
		// A secret key, and the STORE that owns it decides what it is. Two
		// families share the sk- spelling: an upstream vendor key, which lives in
		// the provider table, and the key this estate mints, which lives in IAM.
		// Both lookups are exact, so neither can claim the other's key; the order
		// decides only who answers when NEITHER owns it, and IAM's refusal is the
		// one that names the cure ("mint a new one at" + KeysURL) where
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
			provider, authUser, upstreamModel, err = resolveProviderFromIAMKey(ctx, token, requestedModel, lang)
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
		c.Locals("recordUserId", authUser.Owner+"/provider-key")
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
		if gateErr := enforceBalanceGate(ctx, authUser, authUser.Owner, requestedModel); gateErr != nil {
			err = gateErr
			return
		}
		return
	}

	// Shared post-resolution for IAM/JWT auth: record the billed user and the
	// premium flag from the route table.
	if authUser != nil {
		c.Locals("recordUserId", authUser.Owner+"/"+authUser.Name)
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
//   - IAM API key (sk-...)  — full model routing + billing
//   - hanzo.id JWT token    — full model routing + billing
//   - Provider API key      — direct provider access
//
// @Param   body    body    openai.ChatCompletionRequest  true    "The OpenAI chat request"
// @Success 200 {object} openai.ChatCompletionResponse
// @router /chat [post]
// A completion renders itself to the client. A sink, when a caller supplies one,
// renders it somewhere else instead: /v1/responses reads this same completion in a
// second dialect, and used to do it by swapping the writer out of the request
// context and reading what came back.
//
// Two functions, because a stream and a whole body are two different things and
// collapsing them is what made the old version need a status machine. `wrap`
// decorates the stream's destination as it is produced; `body` takes the
// non-streaming answer entire.
type sink struct {
	wrap func(io.Writer) io.Writer
	body func([]byte) error
}

// ChatCompletions implements the OpenAI-compatible chat completions API
// @Title ChatCompletions
// @Tag OpenAI Compatible API
// @Description OpenAI compatible chat completions API. Accepts:
//   - IAM API key (sk-...)  — full model routing + billing
//   - hanzo.id JWT token    — full model routing + billing
//   - Provider API key      — direct provider access
//
// @Param   body    body    openai.ChatCompletionRequest  true    "The OpenAI chat request"
// @Success 200 {object} openai.ChatCompletionResponse
// @router /chat [post]
func (c *ApiController) ChatCompletions() { c.chatCompletions(callerBearer, nil) }

// caller says which address a completion arrived at, and therefore what is taken from
// the request and what is decided for it. It is an unexported type with no decoded
// form, so no header, body or query can produce one: the route decides it and the
// request cannot. callerPublic is reachable only from ChatCompletionsPublic, after
// that lane's ceiling has admitted the call.
type caller int

const (
	callerBearer caller = iota // a credential is presented and decides everything
	callerPublic               // no credential; the public lane decided everything
)

// chatCompletions is the one completion pipeline. Both entry points run it; they
// differ only in who the caller is, which is a value passed in rather than a fact
// re-derived here.
func (c *ApiController) chatCompletions(from caller, to *sink) {
	c.answer = to
	var token string
	if from == callerBearer {
		// Extract Bearer token
		authHeader := c.Header("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			c.ResponseErrorWithStatus(401, c.T("openai:Invalid API key format. Expected 'Bearer API_KEY'"))
			return
		}

		token = strings.TrimPrefix(authHeader, "Bearer ")

		// Publishable keys (pk-) cannot access completions — reject early
		if isPublishableKey(token) {
			c.rejectPublishableKey()
			return
		}
	}

	// Track timing for observability
	requestStartTime := time.Now().UTC()

	// Parse request body. Authenticate BEFORE reporting a parse error so an
	// invalid credential is 401 regardless of body validity — a malformed body
	// from an unauthenticated caller must not return 200. A valid credential with
	// a bad body gets 400 (not 200).
	var parsed chatRequest
	refusal := ""
	if err := json.Unmarshal(c.Body(), &parsed); err != nil {
		refusal = fmt.Sprintf("Failed to parse request: %s", parseProblem(err))
	} else if why := casefolded(c.Body()); why != "" {
		refusal = why
	} else if f := unpricedField(c.Body()); f != "" {
		refusal = fmt.Sprintf("%q is not accepted: it would buy something this model's price does not cover. Remove it and send the request again.", f)
	} else if parsed.N > 1 {
		// n completions cost n times one, and the hold covers one. Priced by n, a
		// request would be admitted on a balance a single answer fits and settle far
		// past it; one answer per request is the price that was quoted.
		refusal = fmt.Sprintf("n=%d is not accepted: one request is one completion here.", parsed.N)
	}
	if refusal != "" {
		if from == callerBearer {
			if authErr := c.authenticate(token); authErr != nil {
				c.ResponseAuthError(authErr)
				return
			}
		}
		c.ResponseErrorWithStatus(http.StatusBadRequest, refusal)
		return
	}
	request := parsed.ChatCompletionRequest

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
	strict := c.strictAsked()
	if isAutoModel(request.Model) {
		if authErr := c.authenticate(token); authErr != nil {
			c.ResponseAuthError(authErr)
			return
		}
		if strict {
			c.refuseStrict("route_auto", "auto-routing chooses the model; name one")
			return
		}
	}
	// RouteAuto has usually rewritten "auto" to its choice before this handler runs,
	// so the body alone no longer says the model was chosen for the caller.
	if _, routed := c.Locals(autoRoutedKey).(autoRouted); routed && strict {
		if authErr := c.authenticate(token); authErr != nil {
			c.ResponseAuthError(authErr)
			return
		}
		c.refuseStrict("route_auto", "auto-routing chose the model; name one")
		return
	}

	// One request id, generated once here — it is the response id (`chatcmpl-<id>`),
	// the usage-ledger request_id, AND the routing-event join key, so a later reward
	// (POST /v1/ai/feedback) can be tied back to THIS decision. Generated
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
	if r, ok := c.Locals(autoRoutedKey).(autoRouted); ok && r.routed == request.Model && from == callerBearer {
		// RouteAuto resolved `auto` ahead of the balance gate and rewrote the request
		// to name the SKU; the gate priced that SKU and the RoutingEvent is already
		// recorded under r.requestId. Keep that id so the response and the ledger join
		// the decision, and record nothing twice.
		requestId = r.requestId
		routedTask, routingRecorded = r.task, true
		object.GlobalTraffic.RecordTask(
			Country(c.Ctx),
			"",
			r.task,
		)
	} else if routed, task, ok := resolveAutoModel(request.Model, orgId, routingUser, requestId, principal, &request, c.sloFromHeaders()); ok {
		request.Model = routed
		routedTask, routingRecorded = task, true
		c.SetHeader(RoutedModelHeader, routed)
		// Fold this request's TASK into the region's task-mix for the live-traffic
		// globe — geo from the edge headers only (NO IP), aggregates only. Best-effort.
		object.GlobalTraffic.RecordTask(
			Country(c.Ctx),
			"",
			task,
		)
	} else if !isAutoModel(request.Model) {
		// NON-auto: record the caller's explicit model selection so EVERY request is
		// a rateable, trainable data point — up/down feedback for all models, and the
		// ledger captures which model served which task even when the caller picked.
		task := recordExplicitRouting(request.Model, orgId, routingUser, requestId, &request)
		routedTask, routingRecorded = task, true
		object.GlobalTraffic.RecordTask(
			Country(c.Ctx),
			"",
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
		err           error
	)
	if from == callerPublic {
		// No credential to authenticate. The lane already settled who is asking and
		// what they may run; this settles only who runs it.
		provider, authUser, upstreamModel, err = c.resolveProviderForPublic()
	} else {
		provider, authUser, upstreamModel, isPremium, err = c.authResolveProvider(token, request.Model, orgId)
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
	if from == callerPublic {
		// Cap max_tokens for a caller nobody can be billed for: the public lane
		// serves a stranger, so the answer is bounded instead.
		if request.MaxTokens == 0 || request.MaxTokens > publicMaxTokens {
			request.MaxTokens = publicMaxTokens
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
	// wide is how many providers this request may be offered to AT ONCE. It is
	// decided with the reservation and never after: what a race costs is N
	// completions, so the hold either covers them or the request is not raced.
	wide := 1
	// The prompt as sent, every message of it. The reservation prices it and the
	// relay sizes the route by it, so it is measured once.
	measured := estimatePromptTokens(&request)
	if authUser != nil {
		subject := authUser.PayerSubject(ledger)
		// Clamp the upstream completion ceiling BEFORE reserving so the relayed
		// upstream can never emit more than we reserve — the actual settle can
		// never exceed the hold (R1b). reserveCompletionTokens also covers the
		// QueryText pipeline's fixed cap, which ignores max_tokens.
		// The ceiling a caller named under either key is the one the hold covers; one
		// named only as max_completion_tokens was otherwise lowered to the floor.
		if request.MaxTokens == 0 {
			request.MaxTokens = request.MaxCompletionTokens
		}
		// On the paid lane the ceiling is never above the one its seat was sized for.
		request.MaxTokens = laneTokens(c.Context(), clampMaxTokens(request.MaxTokens))
		// The vendor bills the whole body — tools, schemas and call arguments as well
		// as message text — so the hold is never priced on less than its size.
		est := estimateRequestCostCents(request.Model, max(measured, len(c.Body())/4), request.MaxTokens)
		var ok bool
		if hold, wide, ok = c.widthFor(authUser, subject, est); !ok {
			c.ResponseAuthError(billingError("%s", object.InsufficientBalance(c.Host(), ledger, "request cost").Message))
			return
		}
	}
	// Released on the way out — unless a relayed stream took it. That answer settles
	// from inside its own writer, which runs after this handler has returned, and a
	// release here would win the one-shot settle before the answer's cost is known.
	defer func() { hold.settle(0) }()

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
	if fam := familyForProviderType(provider.Type); fam != nil && strict {
		c.refuseStrict("translation", "this model is not served in a way that keeps the request as sent")
		return
	}
	if fam := familyForProviderType(provider.Type); fam != nil {
		// The free tier is served under freeDoor, so it answers as a model of ours.
		sku := request.Model
		if fam == freeFamily() && strings.EqualFold(strings.TrimSpace(sku), freeID) {
			fam, sku = freeDoor()
		}
		familyRefused = c.pipeToFamily(fam, "chat/completions", "openai", sku, c.Body(), request.Stream, clampMaxTokens(request.MaxTokens), orgId, authUser, isPremium, hold, requestStartTime)
		if familyRefused == nil {
			if request.Stream {
				hold = nil // a family's stream settles its own hold, from its writer
			}
			return
		}
		recordRefusals(c.takeSnapshot(authUser), request.Model, familyRefused, authUser, isPremium, request.Stream, requestId, requestStartTime)
	}

	// ── Tools and images ───────────────────────────────────────────────
	// A tool call or an image the family refused stops here. What follows a family
	// is its route's tail — the resale copy of the model and the free text floor —
	// and neither is chosen for tools or vision: an alternate could answer the call
	// without the part that made it one, and an answer shaped wrongly is worse than
	// an honest refusal that names the vendor and the reason.
	//
	// With the paid lane off, only that floor is asked, so the same holds.
	tooled := len(request.Tools) > 0 || request.ToolChoice != nil || len(request.Functions) > 0 || request.FunctionCall != nil
	media := requestHasMedia(&request)
	if tooled || media {
		if familyRefused != nil {
			c.ResponseFailure(exhausted(request.Model, familyRefused))
			return
		}
		if shut(c.Context(), provider) {
			c.ResponseFailure(laneOff(c.Context(), request.Model))
			return
		}
	}

	// ── The relay ──────────────────────────────────────────────────────
	// A route that speaks OpenAI's dialect is sent the caller's own request —
	// every message, every parameter, tools and images alike — and fails over
	// along the route exactly as the text pipeline does (forward.go).
	if relays(provider) {
		question := lastUserText(&request)
		// The router judge scores a text answer against the turn that asked for it,
		// as it did before; a tool call or an image is not what it scores. Every
		// value it reads is taken now, because it runs where a stream is finished.
		var score func(string)
		if routingRecorded && !tooled && !media {
			agent, country, org := strings.Clone(c.Header("User-Agent")), strings.Clone(Country(c.Ctx)), strings.Clone(orgId)
			sku, task := request.Model, routedTask
			score = func(answer string) {
				judgeRoutedResponse(agent, org, requestId, country, sku, task, question, answer)
			}
		}
		// A strict request goes where its route is declared to go. Sized to the prompt,
		// routeForPrompt may move it to another row serving another model, which the
		// digests, taken without the model, would not show.
		route := routeForPrompt(request.Model, orgId, measured)
		if strict {
			route = resolveModelRouteForOrg(request.Model, orgId)
		}
		if c.forward(pass{
			req:     &request,
			body:    c.Body(),
			primary: provider,
			route:   route,
			prior:   familyRefused,
			knowledge: c.retrieveKnowledgeIfEnabled(
				question, retrievalOwner(authUser), c.retrievalStore(), c.GetAcceptLanguage()),
			prompt:  measured,
			user:    authUser,
			premium: isPremium,
			hold:    hold,
			id:      requestId,
			start:   requestStartTime,
			judge:   score,
			strict:  strict,
		}) {
			hold = nil
		}
		return
	}

	// Anthropic-type rows and rows with no OpenAI-compatible address keep the older
	// path until the relay speaks their dialect: a tool call or an image converted
	// for Anthropic, and plain text through the QueryText pipeline below.
	//
	// Neither carries a response format, so one the caller named is refused rather than
	// ignored: an answer that skipped the schema would read as one that followed it.
	// These rows move to the Messages translator when it lands, and this goes with them.
	if strict {
		c.refuseStrict("translation", "this model is not served in a way that keeps the request as sent")
		return
	}
	var format struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(parsed.ResponseFormat, &format) == nil && format.Type != "" && format.Type != "text" {
		c.ResponseFailure(modelError("response_format %q is not supported for %s on this route; send the request without it", format.Type, request.Model))
		return
	}
	if tooled || media {
		if model.Upstream(provider.Type) != model.Anthropic {
			c.ResponseError("No upstream endpoint configured for provider: " + provider.Name)
			return
		}
		// The body is built from the upstream's own id; the envelope, the usage row
		// and the price read the SKU.
		sku := request.Model
		request.Model = provider.SubType
		c.proxyToolRequestAnthropic(provider, &request, sku, requestStartTime, authUser, isPremium, orgId, requestId, hold)
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

	// The OpenAI-shaped view of the stream. Its destination is bound below, where
	// the body is produced.
	writer := &OpenAIWriter{
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
	knowledge := c.retrieveKnowledgeIfEnabled(
		question,
		retrievalOwner(authUser),
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
	// EVERY BYTE OF THE BODY IS WRITTEN IN HERE. A fasthttp response is produced
	// inside a stream callback rather than into a writer the handler holds, so the
	// completion, its bookkeeping and its final chunk all have to run within one
	// call. They run in the order they already ran — this is a wrap, not a
	// reordering.
	//
	// `answered` carries what a bare `return` used to. Those returns are now
	// returns from a closure, which would fall through to the judge below instead
	// of skipping it. (Not `served` — that name is a type in this package.)
	// Built HERE, not inside complete: it reads the caller's IP and holds it, and
	// a provider beaten in a race is billed long after this handler is done with
	// the request that named it.
	billRaced := c.billRaced(request.Model, authUser, isPremium, request.Stream, requestId, requestStartTime)

	// What the body reads from the request, read now. A streamed body is produced
	// after this handler returns and fiber has released the request, so complete
	// reads this and never the controller (base.go snapshot).
	snap := c.takeSnapshot(authUser)

	// complete produces the whole answer into out and returns what went wrong, if
	// anything. It renders nothing itself: the caller knows whether a status can
	// still be sent.
	//
	// It settles `own`, never `hold`. A streamed answer finishes after this handler
	// has returned, and the handler's deferred release would win the one settle first
	// and leave the ledger blind to what the stream cost; so a stream takes the hold
	// from the handler below, and complete must still be holding it when it does.
	own := hold
	answered := false
	complete := func(out io.Writer) error {
		writer.out = out
		var modelResult *model.ModelResult
		var actualProvider served
		var tried []attempt

		// Fast mode, when the reservation covered it. Nil is the cascade, which
		// is what every ordinary request gets.
		var race *fan
		if wide > 1 {
			race = &fan{
				n:     wide,
				to:    out,
				fork:  func(o io.Writer) io.Writer { return writer.fork(o) },
				adopt: writer.adopt,
				bill:  billRaced,
			}
		}

		if route != nil {
			modelResult, actualProvider, tried, err = ask{
				ctx:       snap.ctx,
				route:     route,
				org:       ledger,
				model:     request.Model,
				primary:   provider,
				question:  question,
				history:   history,
				knowledge: knowledge,
				lang:      snap.lang,
				writer:    writer,
				prompt:    promptTokens,
				fan:       race,
				sent:      func() bool { return writer.StreamSent },
				prior:     familyRefused,
			}.serve()
		} else {
			// Model absent from the route table: call the provider auth resolved.
			var modelProvider model.ModelProvider
			modelProvider, err = provider.GetModelProvider(snap.lang)
			if err != nil {
				return serverError("Failed to get model provider: %s", err.Error())
			}
			modelResult, err = modelProvider.QueryText(question, writer, history, "", knowledge, nil, snap.lang)
			actualProvider = served{provider.Name, provider.Origin(), provider}
		}

		// Every vendor that refused goes in the ledger, whether or not one of them
		// eventually served. A failover nobody can see leaves the empty account
		// empty. The family's own refusal is already recorded above, so skip it here.
		if n := len(familyRefused); len(tried) > n {
			recordRefusals(snap, request.Model, tried[n:], authUser, isPremium, request.Stream, requestId, requestStartTime)
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
					ClientIP:  snap.ip,
					RequestID: requestId,
				}
				errRecord.PromptTokens, errRecord.CompletionTokens =
					spent(modelResult, request.Model, promptTokens, writer.MessageString(), err)
				errRecord.TotalTokens = errRecord.PromptTokens + errRecord.CompletionTokens
				errRecord.bind(snap.ctx, authUser)
				errRecord.BYO, errRecord.Account = providerBYO(provider, authUser)
				recordUsage(errRecord)
				recordTrace(snap.ctx, errRecord, requestStartTime)
			}
			return err
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
				ClientIP:         snap.ip,
				RequestID:        requestId,
			}
			successRecord.bind(snap.ctx, authUser)
			// Whether this call was "bring your own key" is a property of the row
			// that SPENT a credential, not of the row auth resolved before failover
			// moved the request. Reading the latter is how a call served on the
			// platform's key gets billed as BYO — 1% instead of the token cost, with
			// the platform eating the upstream.
			successRecord.BYO, successRecord.Account = providerBYO(actualProvider.row, authUser)
			recordUsage(successRecord)
			recordTrace(snap.ctx, successRecord, requestStartTime)
			// Settle the reservation with the ACTUAL cost (this works identically for
			// streaming and non-streaming non-tool responses — both have real token
			// counts here from the QueryText pipeline).
			own.settle(calculateCostCentsWithCache(request.Model, modelResult.PromptTokenCount, modelResult.ResponseTokenCount, modelResult.CacheReadTokenCount, modelResult.CacheWriteTokenCount))
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
				return serverError("%s", err.Error())
			}

			// Only the non-streaming answer is written here, and it runs inside the
			// handler, where the request is still ours.
			c.answerBody(jsonResponse)
		} else {
			err = writer.Close(
				modelResult.PromptTokenCount,
				modelResult.ResponseTokenCount,
				modelResult.TotalTokenCount,
			)
			if err != nil {
				return err
			}
		}
		answered = true
		return nil
	}

	// LLM-as-a-judge: score THIS served (prompt, response) into a dense quality reward
	// for the enso router. It reads the ANSWER, so it runs where the answer is finished
	// — which for a stream is inside the callback, not after it: the callback has not
	// run when SendStreamWriter returns, so out here MessageString() is still empty and
	// every streamed turn would be scored on nothing. It never adds latency either way;
	// the hook only spawns a goroutine after cheap gates, and is a no-op unless
	// ROUTER_JUDGE_ENABLED. The prompt and response are passed transiently — never
	// persisted (see router_judge.go).
	agent, country := c.Header("User-Agent"), Country(c.Ctx)
	judge := func() {
		if answered && routingRecorded {
			judgeRoutedResponse(agent, orgId, requestId, country, request.Model, routedTask, question, writer.MessageString())
		}
	}

	if request.Stream {
		// THE FIRST BYTE DECIDES THE STATUS. Handing zip the stream commits a 200, so
		// the answer is produced into `unsent` first and this handler waits for its first
		// byte. A request every provider refused before a byte was written is answered
		// with the status that says so; one that has begun is handed to the stream with
		// what it already said, and a failure after that goes out as an error event.
		out := newUnsent()
		var failed error
		done := make(chan struct{})
		hold = nil // the stream settles it: complete with the cost, or at zero if it failed
		go func() {
			defer close(done)
			defer own.settle(0)
			failed = complete(out)
			judge()
		}()
		select {
		case <-out.first:
		case <-done:
		}
		if !out.opened() && failed != nil {
			c.ResponseModelFailure(failed)
			return
		}
		// The headers go on before the callback: the first chunk commits the status
		// line, and nothing can be added to it afterwards.
		c.SetHeader("Content-Type", "text/event-stream")
		c.SetHeader("Cache-Control", "no-cache")
		c.SetHeader("Connection", "keep-alive")
		_ = c.SendStreamWriter(func(bw *bufio.Writer) {
			attached := out.attach(bw)
			<-done
			if failed != nil && attached == nil {
				streamOpenAIError(bw, failed)
			}
		})
		return
	}

	// Nothing is streamed: OpenAIWriter accumulates into MessageBuf and the one JSON
	// body is written by complete itself, so there is no stream to name.
	if err := complete(io.Discard); err != nil {
		c.ResponseModelFailure(err)
	}
	judge()
}

// ListModels returns the list of available models from the routing table.
//
// PUBLIC BY DESIGN, AND IT DOES NOT AUTHENTICATE — that is the whole contract, so it
// is stated here rather than left to be inferred. The catalogue is the same for
// everyone (listAvailableModels takes no principal), docs.hanzo.ai fetches it from
// the browser, and every policy layer around it says so: the authz filter lists
// "models" as public, filter_balance does not gate it, the rate limiter excludes it,
// and cloud's spend.Reachable carries /v1/models/ as "the model catalog the shell
// reads for discovery".
//
// SO THE Authorization HEADER IS NOT AN ADMISSION CHECK HERE. It is read for ONE
// thing — annotating gated SKUs with the caller's own access standing — and
// annotation degrades to nothing when there is no verified principal.
//
// A credential that is presented is verified, never merely shape-checked: a caller
// using /v1/models to ask "is my key working?" gets an honest answer, because a key
// that does not verify annotates nothing rather than reading as accepted.
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

	c.SetHeader("Content-Type", "application/json")
	c.Bytes(http.StatusOK, jsonResponse)
}

// proxyToolRequestAnthropic handles tool-calling requests for Claude/Anthropic
// providers by converting the OpenAI format to Anthropic Messages API format
// and converting the response back.
// sku is the model the CALLER named. request.Model already carries the upstream's
// own id by the time this runs, so it is what the outbound Anthropic body is built
// from and nothing else: the envelope, the usage row and the price all read sku,
// which is the model this call was sold as.
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
	// The same wallet ChatCompletions gated and reserved on, re-derived from the same
	// credential rather than threaded, so the two cannot be given different arguments.
	ledger := c.billingOrg(authUser)

	baseURL := provider.ProviderUrl
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	baseURL = strings.TrimRight(baseURL, "/")

	// Convert OpenAI messages to Anthropic format
	var systemPrompt string
	anthropicMessages := []map[string]any{}

	for _, msg := range request.Messages {
		if msg.Role == "system" {
			systemPrompt = msg.Content
			continue
		}

		anthropicMsg := map[string]any{
			"role": msg.Role,
		}

		if msg.Role == "tool" {
			// Tool result message
			anthropicMsg["role"] = "user"
			anthropicMsg["content"] = []map[string]any{
				{
					"type":        "tool_result",
					"tool_use_id": msg.ToolCallID,
					"content":     msg.Content,
				},
			}
		} else if len(msg.ToolCalls) > 0 {
			// Assistant message with tool calls
			content := []map[string]any{}
			if msg.Content != "" {
				content = append(content, map[string]any{
					"type": "text",
					"text": msg.Content,
				})
			}
			for _, tc := range msg.ToolCalls {
				var inputObj any
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &inputObj)
				if inputObj == nil {
					inputObj = map[string]any{}
				}
				content = append(content, map[string]any{
					"type":  "tool_use",
					"id":    tc.ID,
					"name":  tc.Function.Name,
					"input": inputObj,
				})
			}
			anthropicMsg["content"] = content
		} else if len(msg.MultiContent) > 0 {
			content := []map[string]any{}
			for _, part := range msg.MultiContent {
				if part.Type == openai.ChatMessagePartTypeText {
					content = append(content, map[string]any{
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
	anthropicTools := []map[string]any{}
	for _, tool := range request.Tools {
		if tool.Type == openai.ToolTypeFunction {
			anthropicTool := map[string]any{
				"name":        tool.Function.Name,
				"description": tool.Function.Description,
			}
			if tool.Function.Parameters != nil {
				var params any
				raw, _ := json.Marshal(tool.Function.Parameters)
				_ = json.Unmarshal(raw, &params)
				anthropicTool["input_schema"] = params
			} else {
				anthropicTool["input_schema"] = map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				}
			}
			anthropicTools = append(anthropicTools, anthropicTool)
		}
	}

	// Build Anthropic request
	anthropicReq := map[string]any{
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
	upstream.Authorize(req, provider)
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
		StopReason string         `json:"stop_reason"`
		Usage      AnthropicUsage `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &anthropicResp); err != nil {
		c.ResponseError(fmt.Sprintf("Failed to parse Anthropic response: %s", err.Error()))
		return
	}
	// The prompt as every record counts it: read fresh, read from the cache and
	// written to it, each part billed at its own rate.
	used := anthropicResp.Usage.billed(sku)

	// Convert Anthropic response to OpenAI format
	var contentText strings.Builder
	var toolCalls []openai.ToolCall
	toolCallIdx := 0

	for _, block := range anthropicResp.Content {
		switch block.Type {
		case "text":
			contentText.WriteString(block.Text)
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
		Model:   sku,
		Choices: []openai.ChatCompletionChoice{
			{
				Index: 0,
				Message: openai.ChatCompletionMessage{
					Role:      "assistant",
					Content:   contentText.String(),
					ToolCalls: toolCalls,
				},
				FinishReason: finishReason,
			},
		},
		Usage: openai.Usage{
			PromptTokens:        used.prompt(),
			CompletionTokens:    used.out,
			TotalTokens:         used.prompt() + used.out,
			PromptTokensDetails: &openai.PromptTokensDetails{CachedTokens: used.read},
		},
	}

	// Record usage
	if authUser != nil {
		successRecord := &usageRecord{
			Owner:            ledger,
			Organization:     authUser.Owner,
			Model:            sku,
			Provider:         provider.Name,
			Origin:           provider.Origin(),
			PromptTokens:     used.prompt(),
			CacheReadTokens:  used.read,
			CacheWriteTokens: used.write,
			CompletionTokens: used.out,
			TotalTokens:      used.prompt() + used.out,
			Currency:         "USD",
			Premium:          isPremium,
			Stream:           false,
			Status:           "success",
			ClientIP:         c.Fiber().IP(),
			RequestID:        requestId,
		}
		successRecord.bind(c.Context(), authUser)
		successRecord.BYO, successRecord.Account = providerBYO(provider, authUser)
		recordUsage(successRecord)
		recordTrace(c.Context(), successRecord, requestStartTime)
	}
	// The same model the usage row above is priced from. recordUsage debits what
	// usageCostCents makes of record.Model, so a hold settled against a different
	// model would hold one number and bill another.
	hold.settle(calculateCostCentsWithCache(sku, used.prompt(), used.out, used.read, used.write))

	jsonResponse, err := json.Marshal(openaiResp)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	if request.Stream {
		// Client asked for streaming: emit the converted completion as a single
		// SSE chunk followed by [DONE], so OpenAI SDK clients consuming a stream
		// still work while billing used the real (full-response) token usage.
		c.SetHeader("Content-Type", "text/event-stream")
		c.SetHeader("Cache-Control", "no-cache")
		c.SetHeader("Connection", "keep-alive")
		chunk := map[string]any{
			"id":      openaiResp.ID,
			"object":  "chat.completion.chunk",
			"created": openaiResp.Created,
			"model":   openaiResp.Model,
			"choices": []map[string]any{{
				"index": 0,
				"delta": map[string]any{
					"role":       "assistant",
					"content":    contentText.String(),
					"tool_calls": toolCalls,
				},
				"finish_reason": finishReason,
			}},
		}
		// The stream owns the connection from here: zip hands the handler a writer
		// and flushes each frame as it is written, so a chunk reaches the client
		// when it is produced rather than when the handler returns.
		_ = c.SendStreamWriter(func(w *bufio.Writer) {
			if chunkJSON, mErr := json.Marshal(chunk); mErr == nil {
				_, _ = fmt.Fprintf(w, "data: %s\n\n", string(chunkJSON))
			}
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		})
		return
	}

	c.answerBody(jsonResponse)
}

// planUse is what a covered call used of its plan, in nano-dollars: its list price,
// what the caller would have paid; for a model sold at zero, what its paid upstream
// cost us (planNanos), since its list price says nothing about what it spent.
func planUse(record *usageRecord) int64 {
	if billed := usageMargin(record).BilledNano; billed > 0 {
		return billed
	}
	return record.planNanos
}
