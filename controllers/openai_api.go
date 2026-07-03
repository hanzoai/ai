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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/beego/beego/logs"
	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
	iam "github.com/hanzoai/iam"
	"github.com/sashabaranov/go-openai"
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

// isIAMApiKey checks if a token is an IAM-issued API key (hk- prefix).
func isIAMApiKey(token string) bool {
	return strings.HasPrefix(token, "hk-")
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

	// Widget keys can only access explicitly allowed models
	if !widgetAllowedModels[strings.ToLower(requestedModel)] {
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
func resolveProviderFromJwt(token string, requestedModel string, lang string) (*object.Provider, *iam.User, string, error) {
	// Signature + issuer/audience validation (never raw iam.ParseJwtToken), so a
	// token minted for a foreign app/issuer cannot authenticate a paid request.
	claims, err := object.ParseAndValidateJWT(token)
	if err != nil {
		return nil, nil, "", authError("invalid hanzo.id token: %s", err.Error())
	}

	user := &claims.User
	return resolveProviderForUser(user, requestedModel, lang)
}

// resolveProviderFromIAMKey validates an IAM API key (hk-{accessKey})
// and returns the model provider + user, same as JWT path.
func resolveProviderFromIAMKey(apiKey string, requestedModel string, lang string) (*object.Provider, *iam.User, string, error) {
	// IAM API key format: hk-{uuid}
	// Look up user by accessKey via IAM API
	accessKey := apiKey // the full token including hk- prefix is the accessKey

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
			logs.Warn("[iam-fallback] IAM returned %q; using cloud-agent fallback identity (owner=%s name=%s)",
				err.Error(), fallbackUser.Owner, fallbackUser.Name)
			return resolveProviderForUser(fallbackUser, requestedModel, lang)
		}
		return nil, nil, "", authError("API key validation failed: %s", err.Error())
	}
	if user == nil {
		return nil, nil, "", authError("invalid API key")
	}

	return resolveProviderForUser(user, requestedModel, lang)
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
	return &iam.User{
		Owner: "hanzo",
		Name:  "cloud-agent",
	}
}

// resolveProviderForUser is the shared logic for JWT and API key auth paths.
// Given a validated user, resolves the model route and provider.
func resolveProviderForUser(user *iam.User, requestedModel string, lang string) (*object.Provider, *iam.User, string, error) {
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
	provider, err := object.GetModelProviderByNameForOrg(user.Owner, route.providerName)
	if err != nil {
		return nil, user, "", serverError("failed to get provider %q: %s", route.providerName, err.Error())
	}
	if provider == nil {
		return nil, user, "", serverError("provider %q not configured in database", route.providerName)
	}

	// Service accounts configured in BALANCE_EXEMPT_USERS skip balance checks.
	// This allows internal cloud agent pods to make LLM calls without Commerce
	// setup. Exemption matches the per-user "owner/name" key (service accounts are
	// named individually) OR a bare org — via object.BalanceExempt, the SAME shared
	// definition the router gate uses, so the two can never drift in granularity.
	orgKey := user.Owner // namespace (X-Org-Id): the org tenant
	// subject is the billing account WITHIN the namespace: "owner/name" for a
	// personal-billing org (each member billed independently), the org slug for
	// a pooled org. The gate read, this backstop, and the usage debit all key
	// on this one subject.
	subject := object.BillingSubjectForPrincipal(user.Owner, user.Name, user.Type)
	isExempt := object.BalanceExempt().Matches(user.Owner, user.Name)

	if !isExempt {
		// All models require prepaid balance. New accounts receive a $5 starter
		// credit that works only for non-premium (DO-AI) models.
		// Premium models (Fireworks, OpenAI Direct, Zen) require the user to
		// have added funds beyond the starter credit. Balance is per-subject.
		balance, err := getUserBalance(subject, orgKey)
		if err != nil {
			// Fail closed (server-side: cannot verify funds) — never grant on a
			// balance-lookup transport error.
			return nil, user, "", serverError("failed to verify account balance: %s", err.Error())
		}

		if balance <= 0 {
			return nil, user, "", billingError(
				"model %q requires a positive balance. Your current balance is $%.2f. "+
					"Add funds at https://hanzo.ai/billing",
				requestedModel, balance,
			)
		}
	}

	// Premium models require funds beyond the starter credit.
	// A balance <= StarterCreditDollars means the user only has free credit.
	if !isExempt {
		balance, _ := getUserBalance(subject, orgKey)
		starterCredit := StarterCreditDollars
		if cfg := GetModelConfig(); cfg != nil {
			starterCredit = cfg.StarterCreditDollars()
		}
		if route.premium && balance <= starterCredit {
			return nil, user, "", billingError(
				"model %q is a premium model requiring a paid balance. "+
					"Your current balance ($%.2f) is from the starter credit. "+
					"Add funds at https://hanzo.ai/billing to access premium models",
				requestedModel, balance,
			)
		}
	}

	if !isExempt {
		bal, _ := getUserBalance(subject, orgKey)
		user.Balance = bal
	}

	return provider, user, route.upstreamModel, nil
}

// iamAuthQuery returns the clientId/clientSecret query string for IAM API auth.
// Credentials are resolved in order: env vars (IAM_CLIENT_ID/IAM_CLIENT_SECRET),
// KMS secrets, then Beego config (for local dev).
func iamAuthQuery() string {
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

	if clientId != "" && clientSecret != "" {
		return "&clientId=" + url.QueryEscape(clientId) + "&clientSecret=" + url.QueryEscape(clientSecret)
	}
	return ""
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
	// and returns HTML, which broke hk- API-key resolution ("invalid character
	// '<'"). iamAuthQuery() appends clientId/clientSecret so the IAM
	// AutoSigninFilter authenticates this service call (the accessKey lookup
	// requires an authenticated caller).
	reqURL := fmt.Sprintf("%s/v1/iam/get-user?accessKey=%s%s", iamEndpoint, url.QueryEscape(accessKey), iamAuthQuery())

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("IAM request build failed: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("IAM request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("IAM returned status %d", resp.StatusCode)
	}

	var result struct {
		Status string    `json:"status"`
		Msg    string    `json:"msg"`
		Data   *iam.User `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse IAM response: %w", err)
	}

	if result.Status != "ok" {
		return nil, fmt.Errorf("IAM error: %s", result.Msg)
	}

	return result.Data, nil
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

// recordUsage serializes a usage record and enqueues it for reliable delivery
// to Commerce. The queue handles retries with exponential backoff.
// Only successful API calls are recorded (error status is filtered here).
func recordUsage(record *usageRecord) {
	if billingQueue == nil {
		return
	}

	// Only record successful calls
	if record.Status != "success" {
		return
	}

	// Calculate cost. Image and video generations bill per unit (token counts
	// are 0 on those paths); everything else bills from the cache-aware token
	// table. A record carries at most one of ImageCount/VideoCount.
	var costCents int64
	switch {
	case record.VideoCount > 0:
		costCents = videoCostCents(record.Model, record.VideoCount)
	case record.ImageCount > 0:
		costCents = imageCostCents(record.Model, record.ImageCount)
	default:
		costCents = calculateCostCentsWithCache(
			record.Model, record.PromptTokens, record.CompletionTokens,
			record.CacheReadTokens, record.CacheWriteTokens,
		)
	}

	// The debit MUST hit the same account the balance gate reads and the starter
	// credit funded: the billing SUBJECT within the org NAMESPACE.
	//   namespace (X-Org-Id) = record.Owner (the org)
	//   subject   (?user=)      = object.BillingSubject(owner, name)
	// For a personal-billing org that is "owner/name" (per-user); for a pooled
	// org it is the org slug. record.Owner is the IAM `owner`; fall back to
	// deriving owner+name from "owner/name" if Owner was not populated upstream.
	org := record.Owner
	if org == "" {
		if i := strings.IndexByte(record.User, '/'); i > 0 {
			org = record.User[:i]
		} else {
			org = record.User
		}
	}
	subject := object.BillingSubjectFromUserKey(record.Owner, record.User)

	payload := map[string]interface{}{
		"user":             subject,
		"actor":            record.User,
		"currency":         "usd",
		"amount":           costCents,
		"model":            record.Model,
		"provider":         record.Provider,
		"promptTokens":     record.PromptTokens,
		"completionTokens": record.CompletionTokens,
		"totalTokens":      record.TotalTokens,
		"cacheReadTokens":  record.CacheReadTokens,
		"cacheWriteTokens": record.CacheWriteTokens,
		"imageCount":       record.ImageCount,
		"videoCount":       record.VideoCount,
		"requestId":        record.RequestID,
		"premium":          record.Premium,
		"stream":           record.Stream,
		"status":           record.Status,
		"clientIp":         record.ClientIP,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		logs.Error("billing: failed to marshal usage record request_id=%s: %v", record.RequestID, err)
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
// (native ClickHouse OLAP) over native ZAP — the ONE internal telemetry path.
//
// The datastore (ClickHouse) is reached directly via object.DatastoreExec
// (object/datastore.go) — the datastore image serves ClickHouse on :8123/:9000,
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
func recordTrace(record *usageRecord, startTime time.Time) {
	go zapWriteUsage(record, startTime)
	emitGenAISpan(record, startTime)
}

// ── API handlers ────────────────────────────────────────────────────────────

// authenticate validates a bearer credential to a real principal WITHOUT routing
// a model — the authentication half of authResolveProvider. Handlers call it on a
// request-body error so an INVALID credential is rejected (401) regardless of
// body validity: a malformed (or field-incomplete) body from an unauthenticated
// caller must never return 200/400 that confirms the endpoint or lets it be
// probed. It is invoked only on the error path, so the happy path keeps a single
// validation (authResolveProvider). It mirrors authResolveProvider's four auth
// branches EXACTLY and never grants on an unknown key (fail-secure).
func (c *ApiController) authenticate(token string) error {
	switch {
	case isWidgetKey(token):
		if !validateWidgetKey(token) {
			return authError("Widget authentication failed: invalid widget key")
		}
		return nil
	case isIAMApiKey(token):
		// getUserByAccessKey returns (nil, nil) for an unknown key (IAM 200 +
		// data:null), so check BOTH the error AND a nil user — exactly as
		// resolveProviderFromIAMKey does. Missing the nil-user case let an invalid
		// hk- key fall through to a 400 parse error instead of a 401.
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
	case isJwtToken(token):
		// Signature + issuer/audience validation (R3): a foreign-aud or
		// wrong-issuer token is rejected here, not just signature-checked.
		if _, err := object.ParseAndValidateJWT(token); err != nil {
			return authError("invalid hanzo.id token: %s", err.Error())
		}
		return nil
	default:
		// Provider API key (sk-...). An unresolvable key is a 401.
		provider, err := object.GetProviderByProviderKey(token, c.GetAcceptLanguage())
		if err != nil {
			return authError("invalid API key: %s", err.Error())
		}
		if provider == nil {
			return authError("invalid API key")
		}
		return nil
	}
}

// providerKeyBillingUser derives the billing identity for a provider-key (sk-)
// caller: the org that OWNS the provider row the key belongs to (and therefore
// minted the key). The sk- key is a machine credential, so it bills the OWNER
// ORG — Name is empty so object.BillingSubject collapses to the org ledger for
// BOTH personal-billing and pooled orgs, and Type "application" marks it M2M,
// mirroring BillingSubjectForPrincipal's carve-out. A provider with no owner is
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
	case isWidgetKey(token):
		// Widget key (hz_...) — restricted model access, no balance check.
		isWidget = true
		var widgetUpstream string
		provider, widgetUpstream, err = resolveProviderFromWidgetKey(token, requestedModel, lang)
		if err != nil {
			err = authError("Widget authentication failed: %s", err.Error())
			return
		}
		upstreamModel = widgetUpstream
		c.Ctx.Input.SetParam("recordUserId", "widget/anonymous")
		logs.Info("Widget key access: model=%s, upstream=%s", requestedModel, upstreamModel)
		return

	case isIAMApiKey(token):
		// IAM API key (hk-...) — full model routing + billing. resolveProviderFromIAMKey
		// returns a typed apiError (401 invalid key / 400 bad model / 402 balance /
		// 500 misconfig); wrapAuth preserves it and 401s any untyped error.
		provider, authUser, upstreamModel, err = resolveProviderFromIAMKey(token, requestedModel, lang)
		if err != nil {
			err = wrapAuth(err)
			return
		}

	case isJwtToken(token):
		// hanzo.id JWT token — full model routing + billing. Same typed-status
		// contract as the IAM key path.
		provider, authUser, upstreamModel, err = resolveProviderFromJwt(token, requestedModel, lang)
		if err != nil {
			err = wrapAuth(err)
			return
		}

	default:
		// Provider API key (sk-...) — direct provider access. An unresolvable key
		// is an auth failure (401).
		provider, err = object.GetProviderByProviderKey(token, lang)
		if err != nil {
			err = authError("invalid API key: %s", err.Error())
			return
		}
		if provider == nil {
			err = authError("invalid API key")
			return
		}
		// Attribute + bill this sk- call to the org that OWNS the provider row
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
//   - IAM API key (hk-...)  — full model routing + billing
//   - hanzo.id JWT token    — full model routing + billing
//   - Provider API key      — direct provider access
//
// @Param   body    body    openai.ChatCompletionRequest  true    "The OpenAI chat request"
// @Success 200 {object} openai.ChatCompletionResponse
// @router /chat [post]
func (c *ApiController) ChatCompletions() {
	// Extract Bearer token
	authHeader := c.Ctx.Request.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		c.ResponseErrorWithStatus(401, c.T("openai:Invalid API key format. Expected 'Bearer API_KEY'"))
		return
	}

	token := strings.TrimPrefix(authHeader, "Bearer ")

	// Publishable keys (pk-) cannot access completions — reject early
	if isPublishableKey(token) {
		c.Ctx.Output.SetStatus(403)
		c.Ctx.Output.Header("Content-Type", "application/json")
		c.Ctx.Output.Body([]byte(`{"error":{"message":"Publishable keys (pk-) can only access read-only endpoints (/api/models, /health). Use a secret key (sk-) for completions.","type":"auth_error","code":403}}`))
		c.EnableRender = false
		return
	}

	// Track timing for observability
	requestStartTime := time.Now().UTC()

	// Parse request body. Authenticate BEFORE reporting a parse error so an
	// invalid credential is 401 regardless of body validity — a malformed body
	// from an unauthenticated caller must not return 200. A valid credential with
	// a bad body gets 400 (not 200).
	var request openai.ChatCompletionRequest
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, &request); err != nil {
		if authErr := c.authenticate(token); authErr != nil {
			c.ResponseAuthError(authErr)
			return
		}
		c.ResponseErrorWithStatus(http.StatusBadRequest, fmt.Sprintf("Failed to parse request: %s", err.Error()))
		return
	}

	// Resolve org context for per-org model routing and pricing.
	orgId := c.GetEffectiveOrg()

	// Authenticate the bearer token and resolve the requested model to its
	// upstream provider, premium flag, and (for IAM/JWT auth) the billed user.
	// This is the ONE auth+routing policy, shared with /v1/embeddings and
	// /v1/rerank — see authResolveProvider.
	provider, authUser, upstreamModel, isPremium, isWidget, err := c.authResolveProvider(token, request.Model, orgId)
	if err != nil {
		c.ResponseAuthError(err)
		return
	}
	if isWidget {
		// Cap max_tokens for anonymous widget requests.
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
		subject := object.BillingSubject(authUser.Owner, authUser.Name)
		// Clamp the upstream completion ceiling BEFORE reserving so the proxied
		// (tool/stream) upstream can never emit more than we reserve — the actual
		// settle can never exceed the hold (R1b). reserveCompletionTokens also
		// covers the QueryText pipeline's fixed cap, which ignores max_tokens.
		request.MaxTokens = clampMaxTokens(request.MaxTokens)
		est := estimateRequestCostCents(request.Model, estimatePromptTokens(&request), request.MaxTokens)
		var ok bool
		if hold, ok = reserveBudget(subject, est); !ok {
			c.ResponseAuthError(billingError("Insufficient balance for the estimated request cost. Add credits at console.hanzo.ai"))
			return
		}
	}
	defer hold.settle(0)

	// ── Tool-calling pass-through ──────────────────────────────────────
	// When the request includes tools/functions, the QueryText pipeline
	// cannot handle structured tool calls. Proxy the raw request directly
	// to the upstream provider's OpenAI-compatible endpoint so the LLM
	// receives tool definitions and can return tool_calls in the response.
	if len(request.Tools) > 0 || request.ToolChoice != nil {
		c.proxyToolRequest(provider, &request, requestStartTime, authUser, isPremium, orgId, hold)
		return
	}

	// Inject Zen identity prompt for zen-branded models
	if zenPrompt := zenIdentityPrompt(request.Model); zenPrompt != "" {
		hasSystem := len(request.Messages) > 0 && request.Messages[0].Role == "system"
		if hasSystem {
			request.Messages[0].Content = zenPrompt + "\n\n" + request.Messages[0].Content
		} else {
			request.Messages = append([]openai.ChatCompletionMessage{{
				Role:    "system",
				Content: zenPrompt,
			}}, request.Messages...)
		}
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
	requestId := util.GenerateUUID()
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
		c.Ctx.Request.Header.Get("X-Retrieval-Store"),
		c.GetAcceptLanguage(),
	)

	// Resolve the route for failover (may have fallback providers)
	route := resolveModelRouteForOrg(request.Model, orgId)

	// Call the model provider with failover support
	var modelResult *model.ModelResult
	var actualProvider string

	if route != nil && len(route.fallbacks) > 0 {
		modelResult, actualProvider, err = failoverQueryText(
			route, question, writer, history, knowledge,
			c.GetAcceptLanguage(),
			func() bool { return writer.StreamSent },
		)
	} else {
		// No fallbacks configured — direct call (original path)
		var modelProvider model.ModelProvider
		modelProvider, err = provider.GetModelProvider(c.GetAcceptLanguage())
		if err != nil {
			c.ResponseError(fmt.Sprintf("Failed to get model provider: %s", err.Error()))
			return
		}
		modelResult, err = modelProvider.QueryText(question, writer, history, "", knowledge, nil, c.GetAcceptLanguage())
		actualProvider = provider.Name
	}

	if err != nil {
		// Record failed usage
		if authUser != nil {
			errRecord := &usageRecord{
				Owner:     authUser.Owner,
				User:      authUser.Owner + "/" + authUser.Name,
				Model:     request.Model,
				Provider:  actualProvider,
				Premium:   isPremium,
				Stream:    request.Stream,
				Status:    "error",
				ErrorMsg:  err.Error(),
				ClientIP:  c.Ctx.Request.RemoteAddr,
				RequestID: requestId,
			}
			recordUsage(errRecord)
			recordTrace(errRecord, requestStartTime)
		}
		c.ResponseError(err.Error())
		return
	}

	// Record successful usage (actualProvider reflects which provider served the request)
	if authUser != nil {
		successRecord := &usageRecord{
			Owner:            authUser.Owner,
			User:             authUser.Owner + "/" + authUser.Name,
			Organization:     authUser.Owner,
			Model:            request.Model,
			Provider:         actualProvider,
			PromptTokens:     modelResult.PromptTokenCount,
			CompletionTokens: modelResult.ResponseTokenCount,
			TotalTokens:      modelResult.TotalTokenCount,
			Currency:         "USD",
			Premium:          isPremium,
			Stream:           request.Stream,
			Status:           "success",
			ClientIP:         c.Ctx.Request.RemoteAddr,
			RequestID:        requestId,
		}
		recordUsage(successRecord)
		recordTrace(successRecord, requestStartTime)
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
	c.EnableRender = false
}

// ListModels returns the list of available models from the routing table.
// Requires a valid Bearer token (JWT, hk-, pk-, sk-, or hz_ key).
// @Title ListModels
// @Tag OpenAI Compatible API
// @Description Returns a list of all available models. Requires authentication.
// @Param Authorization header string true "Bearer token"
// @Success 200 {object} object
// @Failure 401 {object} object "Unauthorized"
// @router /models [get]
func (c *ApiController) ListModels() {
	// R-04 fix: require authentication for model listing.
	// Accept any valid token type (JWT, IAM key, publishable key, widget key).
	authHeader := c.Ctx.Request.Header.Get("Authorization")
	token := ""
	if strings.HasPrefix(authHeader, "Bearer ") {
		token = strings.TrimPrefix(authHeader, "Bearer ")
	}
	hasSession := c.GetSessionUsername() != ""
	if token == "" && !hasSession {
		c.Ctx.Output.Header("Content-Type", "application/json")
		c.Ctx.ResponseWriter.WriteHeader(401)
		c.Ctx.Output.Body([]byte(`{"error":{"message":"Authentication required. Provide a Bearer token.","type":"authentication_error","code":"unauthorized"}}`))
		c.EnableRender = false
		return
	}

	// R-RED-03: Validate token format — reject obviously invalid bearer values.
	// Accepted prefixes: hk- (IAM key), sk- (secret key), pk- (publishable key),
	// hz_ (Hanzo token). JWTs must have 3 base64url-encoded parts.
	if token != "" {
		isKnownPrefix := strings.HasPrefix(token, "hk-") ||
			strings.HasPrefix(token, "sk-") ||
			strings.HasPrefix(token, "pk-") ||
			strings.HasPrefix(token, "hz_")
		isValidJWT := false
		if !isKnownPrefix {
			// JWT must have exactly 3 dot-separated parts, each valid base64url
			parts := strings.Split(token, ".")
			if len(parts) == 3 {
				isValidJWT = true
				for _, part := range parts {
					if len(part) == 0 {
						isValidJWT = false
						break
					}
					if _, err := base64.RawURLEncoding.DecodeString(part); err != nil {
						isValidJWT = false
						break
					}
				}
			}
		}
		if !isKnownPrefix && !isValidJWT {
			c.Ctx.Output.Header("Content-Type", "application/json")
			c.Ctx.ResponseWriter.WriteHeader(401)
			c.Ctx.Output.Body([]byte(`{"error":{"message":"Invalid token format.","type":"authentication_error","code":"unauthorized"}}`))
			c.EnableRender = false
			return
		}
	}

	models := listAvailableModels()

	response := map[string]interface{}{
		"object": "list",
		"data":   models,
	}

	jsonResponse, err := json.Marshal(response)
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
	requestId := util.GenerateUUID()

	// Rewrite model to upstream model name
	request.Model = provider.SubType

	// For Claude/Anthropic providers, convert to Anthropic Messages API format
	if provider.Type == "Claude" {
		c.proxyToolRequestAnthropic(provider, request, requestStartTime, authUser, isPremium, orgId, requestId, hold)
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
				Owner:     authUser.Owner,
				User:      authUser.Owner + "/" + authUser.Name,
				Model:     request.Model,
				Provider:  provider.Name,
				Premium:   isPremium,
				Stream:    request.Stream,
				Status:    "error",
				ErrorMsg:  err.Error(),
				ClientIP:  c.Ctx.Request.RemoteAddr,
				RequestID: requestId,
			}
			recordUsage(errRecord)
			recordTrace(errRecord, requestStartTime)
		}
		c.ResponseError(fmt.Sprintf("Upstream request failed: %s", err.Error()))
		return
	}
	defer resp.Body.Close()

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
		c.Ctx.ResponseWriter.WriteHeader(resp.StatusCode)

		// Copy the SSE stream to the client while capturing token usage (and the
		// output text for a tokenizer fallback). This is the billing-critical core
		// of the streaming tool path — see streamCaptureUsage.
		capPrompt, capCompletion, capTotal, completionText := streamCaptureUsage(
			resp.Body, c.Ctx.ResponseWriter, c.Ctx.ResponseWriter.Flush,
			clientWantsUsage, requestId, request.Model,
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
				Owner:            authUser.Owner,
				User:             authUser.Owner + "/" + authUser.Name,
				Organization:     authUser.Owner,
				Model:            request.Model,
				Provider:         provider.Name,
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
			recordUsage(successRecord)
			recordTrace(successRecord, requestStartTime)
		}
		hold.settle(actualCents)
	} else {
		// Non-streaming: read full response, extract token counts, forward
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			c.ResponseError(fmt.Sprintf("Failed to read upstream response: %s", err.Error()))
			return
		}

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
				Owner:            authUser.Owner,
				User:             authUser.Owner + "/" + authUser.Name,
				Organization:     authUser.Owner,
				Model:            request.Model,
				Provider:         provider.Name,
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
			recordUsage(successRecord)
			recordTrace(successRecord, requestStartTime)
		}
		hold.settle(actualCents)

		c.Ctx.ResponseWriter.WriteHeader(resp.StatusCode)
		c.Ctx.Output.Body(respBody)
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
func (c *ApiController) proxyToolRequestAnthropic(
	provider *object.Provider,
	request *openai.ChatCompletionRequest,
	requestStartTime time.Time,
	authUser *iam.User,
	isPremium bool,
	orgId string,
	requestId string,
	hold *budgetHold,
) {
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
		logs.Error("[proxyToolRequest] Anthropic error %d: %s", resp.StatusCode, string(respBody))
		c.Ctx.ResponseWriter.WriteHeader(resp.StatusCode)
		c.Ctx.Output.Body(respBody)
		c.EnableRender = false
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
			Owner:            authUser.Owner,
			User:             authUser.Owner + "/" + authUser.Name,
			Organization:     authUser.Owner,
			Model:            request.Model,
			Provider:         provider.Name,
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
		recordUsage(successRecord)
		recordTrace(successRecord, requestStartTime)
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
