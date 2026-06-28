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
	"bufio"
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
// key (?user=) and as the namespace selector (X-Hanzo-Org), matching the gate
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
	// (X-Hanzo-Org) is the org. Both must match the gate and the usage debit.
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
	req.Header.Set("X-Hanzo-Org", namespace)

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

// resolveOwnerFromOrigin maps a request's Origin/Referer hostname to an IAM
// org. Config lives in a single WIDGET_ORIGINS env/KMS value as a JSON object
// (or key=val,key=val) mapping hostname → owner:
//
//	WIDGET_ORIGINS={"lux.financial":"lux","hanzo.ai":"hanzo","zoo.ngo":"zoo"}
//
// or
//
//	WIDGET_ORIGINS=lux.financial=lux,hanzo.ai=hanzo,zoo.ngo=zoo
//
// Suffix matches so subdomains inherit (e.g., api.hanzo.ai → hanzo). Fallback
// is WIDGET_DEFAULT_OWNER env, then "hanzo".
func resolveOwnerFromOrigin(origin string) string {
	host := originHostname(origin)
	if host != "" {
		m := loadWidgetOrigins()
		if o, ok := m[host]; ok {
			return o
		}
		for suffix, o := range m {
			if strings.HasSuffix(host, "."+suffix) {
				return o
			}
		}
	}
	if v := os.Getenv("WIDGET_DEFAULT_OWNER"); v != "" {
		return v
	}
	return "hanzo"
}

func originHostname(origin string) string {
	if origin == "" {
		return ""
	}
	if i := strings.Index(origin, "://"); i >= 0 {
		origin = origin[i+3:]
	}
	if i := strings.IndexByte(origin, '/'); i >= 0 {
		origin = origin[:i]
	}
	if i := strings.LastIndexByte(origin, ':'); i >= 0 {
		origin = origin[:i]
	}
	return origin
}

// loadWidgetOrigins parses WIDGET_ORIGINS (env or KMS) into a hostname→owner map.
// Accepts JSON object or comma-separated key=value.
func loadWidgetOrigins() map[string]string {
	raw := os.Getenv("WIDGET_ORIGINS")
	if raw == "" {
		if v, err := object.GetKMSSecret("WIDGET_ORIGINS"); err == nil {
			raw = v
		}
	}
	out := map[string]string{}
	if raw == "" {
		return out
	}
	if strings.HasPrefix(strings.TrimSpace(raw), "{") {
		_ = json.Unmarshal([]byte(raw), &out)
		return out
	}
	for _, part := range strings.Split(raw, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 {
			out[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}
	return out
}

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
	claims, err := iam.ParseJwtToken(token)
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

	// Fetch the provider entry that holds API keys/URLs for this upstream.
	// GetModelProviderByName returns a shallow copy, safe to mutate. A missing
	// or unconfigured provider is a server-side misconfiguration (500).
	provider, err := object.GetModelProviderByName(route.providerName)
	if err != nil {
		return nil, user, "", serverError("failed to get provider %q: %s", route.providerName, err.Error())
	}
	if provider == nil {
		return nil, user, "", serverError("provider %q not configured in database", route.providerName)
	}

	// Service accounts configured in BALANCE_EXEMPT_USERS skip balance checks.
	// This allows internal cloud agent pods to make LLM calls without Commerce setup.
	// The exempt list is matched on the per-user "owner/name" key (service
	// accounts are named individually), while billing itself is per-org.
	exemptUsers := os.Getenv("BALANCE_EXEMPT_USERS")
	userKey := user.Owner + "/" + user.Name
	orgKey := user.Owner // namespace (X-Hanzo-Org): the org tenant
	// subject is the billing account WITHIN the namespace: "owner/name" for a
	// personal-billing org (each member billed independently), the org slug for
	// a pooled org. The gate read, this backstop, and the usage debit all key
	// on this one subject.
	subject := object.BillingSubject(user.Owner, user.Name)
	isExempt := false
	if exemptUsers != "" {
		for _, u := range strings.Split(exemptUsers, ",") {
			t := strings.TrimSpace(u)
			if t == userKey || t == orgKey {
				isExempt = true
				break
			}
		}
	}

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

	// Calculate cost from per-model pricing table (cache-aware)
	costCents := calculateCostCentsWithCache(
		record.Model, record.PromptTokens, record.CompletionTokens,
		record.CacheReadTokens, record.CacheWriteTokens,
	)

	// The debit MUST hit the same account the balance gate reads and the starter
	// credit funded: the billing SUBJECT within the org NAMESPACE.
	//   namespace (X-Hanzo-Org) = record.Owner (the org)
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

// resolveConsoleKeys returns the console API key pair for the given org.
// Keys are fetched from KMS using the naming convention:
//   - console-pk-{org}  → public key
//   - console-sk-{org}  → secret key
//
// Falls back to global console-pk / console-sk secrets in KMS.
// KMS responses are cached for 5 minutes (kmsSecTTL in object/kms.go).
func resolveConsoleKeys(org string) (publicKey, secretKey string) {
	// Try per-org keys first: console-pk-{org}, console-sk-{org}
	if org != "" {
		pk, pkErr := object.GetKMSSecret("console-pk-" + org)
		sk, skErr := object.GetKMSSecret("console-sk-" + org)
		if pkErr == nil && skErr == nil && pk != "" && sk != "" {
			return pk, sk
		}
	}

	// Fall back to global console keys from KMS
	pk, pkErr := object.GetKMSSecret("console-pk")
	sk, skErr := object.GetKMSSecret("console-sk")
	if pkErr == nil && skErr == nil && pk != "" && sk != "" {
		return pk, sk
	}

	// Fall back to env vars (CONSOLE_PUBLIC_KEY / CONSOLE_SECRET_KEY)
	pk = os.Getenv("CONSOLE_PUBLIC_KEY")
	sk = os.Getenv("CONSOLE_SECRET_KEY")
	if pk != "" && sk != "" {
		return pk, sk
	}

	return "", ""
}

// recordTrace sends a trace+generation event to the console for observability.
// Traces are routed to per-org console projects using KMS secrets
// (console-pk-{org} / console-sk-{org}), enabling each org to see their own usage
// in console.hanzo.ai. This is fire-and-forget — failures are silently ignored.
func recordTrace(record *usageRecord, startTime time.Time) {
	// Write billing record to ClickHouse for invoice reconciliation.
	go zapWriteUsage(record, startTime)
	// Write observability trace to ClickHouse via native ZAP.
	go zapWriteTrace(record, startTime)

	go func() {
		// Resolve console endpoint from KMS, then Beego config, then env var
		consoleEndpoint, _ := object.GetKMSSecret("console-endpoint")
		if consoleEndpoint == "" {
			consoleEndpoint = conf.GetConfigString("consoleEndpoint")
		}
		if consoleEndpoint == "" {
			consoleEndpoint = os.Getenv("CONSOLE_HOST")
		}
		if consoleEndpoint == "" {
			return
		}
		consoleEndpoint = strings.TrimRight(consoleEndpoint, "/")

		// Resolve per-org or global console API keys
		org := record.Organization
		if org == "" {
			org = record.Owner
		}
		consoleApiKey, consoleSecretKeyVal := resolveConsoleKeys(org)
		if consoleApiKey == "" || consoleSecretKeyVal == "" {
			return
		}

		endTime := time.Now().UTC()
		traceId := util.GenerateUUID()
		genId := util.GenerateUUID()

		// Build tags: org, model, provider, source app
		tags := []string{record.Model, record.Provider}
		if org != "" {
			tags = append(tags, "org:"+org)
		}
		if record.User != "" {
			tags = append(tags, "user:"+record.User)
		}

		// Determine cost for the generation
		costCents := calculateCostCentsWithCache(
			record.Model, record.PromptTokens, record.CompletionTokens,
			record.CacheReadTokens, record.CacheWriteTokens,
		)

		// Build console ingestion batch with full org/user/cost context
		batch := map[string]interface{}{
			"batch": []map[string]interface{}{
				{
					"id":        util.GenerateUUID(),
					"type":      "trace-create",
					"timestamp": startTime.UTC().Format(time.RFC3339Nano),
					"body": map[string]interface{}{
						"id":        traceId,
						"name":      "chat-completion",
						"userId":    record.User,
						"sessionId": record.RequestID,
						"timestamp": startTime.UTC().Format(time.RFC3339Nano),
						"metadata": map[string]interface{}{
							"model":        record.Model,
							"provider":     record.Provider,
							"organization": org,
							"premium":      record.Premium,
							"stream":       record.Stream,
							"requestId":    record.RequestID,
							"clientIp":     record.ClientIP,
							"source":       "cloud-api",
						},
						"tags": tags,
					},
				},
				{
					"id":        util.GenerateUUID(),
					"type":      "generation-create",
					"timestamp": endTime.Format(time.RFC3339Nano),
					"body": map[string]interface{}{
						"id":                  genId,
						"traceId":             traceId,
						"name":                record.Model,
						"model":               record.Model,
						"startTime":           startTime.UTC().Format(time.RFC3339Nano),
						"endTime":             endTime.Format(time.RFC3339Nano),
						"completionStartTime": endTime.Format(time.RFC3339Nano),
						"level":               "DEFAULT",
						"statusMessage":       record.Status,
						"usage": map[string]interface{}{
							"input":  record.PromptTokens,
							"output": record.CompletionTokens,
							"total":  record.TotalTokens,
							"unit":   "TOKENS",
						},
						"costDetails": map[string]interface{}{
							"input":  float64(costCents) * float64(record.PromptTokens) / float64(max(record.TotalTokens, 1)),
							"output": float64(costCents) * float64(record.CompletionTokens) / float64(max(record.TotalTokens, 1)),
						},
						"metadata": map[string]interface{}{
							"provider":     record.Provider,
							"organization": org,
							"requestId":    record.RequestID,
							"costCents":    costCents,
						},
					},
				},
			},
			"metadata": map[string]interface{}{
				"sdk_name":    "cloud-api",
				"sdk_version": "1.0.0",
				"public_key":  consoleApiKey,
			},
		}

		body, err := json.Marshal(batch)
		if err != nil {
			return
		}

		url := consoleEndpoint + "/api/public/ingestion"
		client := &http.Client{Timeout: 5 * time.Second}
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		auth := base64.StdEncoding.EncodeToString([]byte(consoleApiKey + ":" + consoleSecretKeyVal))
		req.Header.Set("Authorization", "Basic "+auth)

		resp, err := client.Do(req)
		if err != nil {
			return
		}
		resp.Body.Close()
	}()
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
		if _, err := iam.ParseJwtToken(token); err != nil {
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

	// ── Tool-calling pass-through ──────────────────────────────────────
	// When the request includes tools/functions, the QueryText pipeline
	// cannot handle structured tool calls. Proxy the raw request directly
	// to the upstream provider's OpenAI-compatible endpoint so the LLM
	// receives tool definitions and can return tool_calls in the response.
	if len(request.Tools) > 0 || request.ToolChoice != nil {
		c.proxyToolRequest(provider, &request, requestStartTime, authUser, isPremium, orgId)
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
		retrievalOwner(authUser, token, c.Ctx.Request.Header.Get("Origin"), c.Ctx.Request.Header.Get("Referer")),
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
) {
	requestId := util.GenerateUUID()

	// Rewrite model to upstream model name
	request.Model = provider.SubType

	// For Claude/Anthropic providers, convert to Anthropic Messages API format
	if provider.Type == "Claude" {
		c.proxyToolRequestAnthropic(provider, request, requestStartTime, authUser, isPremium, orgId, requestId)
		return
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
		// Stream: copy SSE events directly
		c.Ctx.ResponseWriter.Header().Set("Content-Type", "text/event-stream")
		c.Ctx.ResponseWriter.Header().Set("Cache-Control", "no-cache")
		c.Ctx.ResponseWriter.Header().Set("Connection", "keep-alive")
		c.Ctx.ResponseWriter.WriteHeader(resp.StatusCode)

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)

		// Track the last seen chunk ID/model so we can fix bare usage chunks.
		var lastChunkID, lastChunkModel string

		for scanner.Scan() {
			line := scanner.Text()

			// Fix bare usage-only SSE chunks (missing id/object/choices) so
			// downstream OpenAI SDK clients can parse them correctly.
			if strings.HasPrefix(line, "data: {\"usage\"") && !strings.Contains(line, "\"choices\"") {
				raw := strings.TrimPrefix(line, "data: ")
				var usageChunk map[string]interface{}
				if json.Unmarshal([]byte(raw), &usageChunk) == nil {
					chunkID := lastChunkID
					if chunkID == "" {
						chunkID = "chatcmpl-" + requestId
					}
					chunkModel := lastChunkModel
					if chunkModel == "" {
						chunkModel = request.Model
					}
					usageChunk["id"] = chunkID
					usageChunk["object"] = "chat.completion.chunk"
					usageChunk["created"] = time.Now().Unix()
					usageChunk["model"] = chunkModel
					usageChunk["choices"] = []interface{}{}
					if fixed, err := json.Marshal(usageChunk); err == nil {
						line = "data: " + string(fixed)
					}
				}
			} else if strings.HasPrefix(line, "data: {") && strings.Contains(line, "\"id\"") {
				// Extract chunk ID/model for reuse in usage chunk
				var peek struct {
					ID    string `json:"id"`
					Model string `json:"model"`
				}
				if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &peek) == nil {
					if peek.ID != "" {
						lastChunkID = peek.ID
					}
					if peek.Model != "" {
						lastChunkModel = peek.Model
					}
				}
			}

			_, _ = fmt.Fprintf(c.Ctx.ResponseWriter, "%s\n", line)
			c.Ctx.ResponseWriter.Flush()
		}

		// Record usage (approximate — we don't parse SSE for token counts in streaming)
		if authUser != nil {
			successRecord := &usageRecord{
				Owner:        authUser.Owner,
				User:         authUser.Owner + "/" + authUser.Name,
				Organization: authUser.Owner,
				Model:        request.Model,
				Provider:     provider.Name,
				Currency:     "USD",
				Premium:      isPremium,
				Stream:       true,
				Status:       "success",
				ClientIP:     c.Ctx.Request.RemoteAddr,
				RequestID:    requestId,
			}
			recordUsage(successRecord)
			recordTrace(successRecord, requestStartTime)
		}
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

		if authUser != nil {
			successRecord := &usageRecord{
				Owner:            authUser.Owner,
				User:             authUser.Owner + "/" + authUser.Name,
				Organization:     authUser.Owner,
				Model:            request.Model,
				Provider:         provider.Name,
				PromptTokens:     upstreamResp.Usage.PromptTokens,
				CompletionTokens: upstreamResp.Usage.CompletionTokens,
				TotalTokens:      upstreamResp.Usage.TotalTokens,
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
	if request.Stream {
		anthropicReq["stream"] = true
	}

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

	if request.Stream {
		// For streaming, we need to convert Anthropic SSE to OpenAI SSE format
		// This is complex — for now, collect full response and send as non-stream
		// TODO: implement true SSE conversion for Anthropic streaming
	}

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

	jsonResponse, err := json.Marshal(openaiResp)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.Ctx.Output.Header("Content-Type", "application/json")
	c.Ctx.Output.Body(jsonResponse)
	c.EnableRender = false
}
