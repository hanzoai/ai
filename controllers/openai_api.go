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
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/beego/beego/logs"
	"github.com/hanzoai/cloud/conf"
	"github.com/hanzoai/cloud/model"
	"github.com/hanzoai/cloud/object"
	"github.com/hanzoai/cloud/util"
	iamsdk "github.com/hanzoid/go-sdk/casdoorsdk"
	"github.com/sashabaranov/go-openai"
)

// balanceEntry caches a user's balance from Commerce to avoid per-request lookups.
type balanceEntry struct {
	balance   float64
	fetchedAt time.Time
}

var (
	balanceCache    = make(map[string]*balanceEntry)
	balanceCacheMu  sync.RWMutex
	balanceCacheTTL = 30 * time.Second
)

// getUserBalance returns the current balance for a user, fetching from Commerce
// and caching briefly. Balance is mutable financial state (not identity) so
// it is never read from the JWT — always checked against the source of truth.
// The userId should be in "owner/name" format (e.g., "hanzo/alice").
func getUserBalance(userId string) (float64, error) {
	key := userId

	balanceCacheMu.RLock()
	entry, ok := balanceCache[key]
	balanceCacheMu.RUnlock()

	if ok && time.Since(entry.fetchedAt) < balanceCacheTTL {
		return entry.balance, nil
	}

	commerceEndpoint := conf.GetConfigString("commerceEndpoint")
	if commerceEndpoint == "" {
		return 0, fmt.Errorf("commerceEndpoint is not configured")
	}
	commerceEndpoint = strings.TrimRight(commerceEndpoint, "/")
	commerceToken := conf.GetConfigString("commerceToken")

	url := fmt.Sprintf("%s/api/v1/billing/balance?user=%s&currency=usd", commerceEndpoint, userId)

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("Commerce request build failed: %w", err)
	}
	if commerceToken != "" {
		req.Header.Set("Authorization", "Bearer "+commerceToken)
	}

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

	balanceCacheMu.Lock()
	balanceCache[key] = &balanceEntry{balance: balanceDollars, fetchedAt: time.Now()}
	balanceCacheMu.Unlock()

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

// widgetMaxTokens caps the maximum tokens per widget request to control costs.
const widgetMaxTokens = 800

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
	// Validate the widget key. For now, accept any hz_ prefixed key.
	// In production, these will be validated against KMS-stored keys.
	if token != "hz_widget_public" {
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
func resolveProviderFromJwt(token string, requestedModel string, lang string) (*object.Provider, *iamsdk.User, string, error) {
	claims, err := iamsdk.ParseJwtToken(token)
	if err != nil {
		return nil, nil, "", fmt.Errorf("invalid hanzo.id token: %s", err.Error())
	}

	user := &claims.User
	return resolveProviderForUser(user, requestedModel, lang)
}

// resolveProviderFromIAMKey validates an IAM API key (hk-{accessKey})
// and returns the model provider + user, same as JWT path.
func resolveProviderFromIAMKey(apiKey string, requestedModel string, lang string) (*object.Provider, *iamsdk.User, string, error) {
	// IAM API key format: hk-{uuid}
	// Look up user by accessKey via IAM API
	accessKey := apiKey // the full token including hk- prefix is the accessKey

	user, err := getUserByAccessKey(accessKey)
	if err != nil {
		return nil, nil, "", fmt.Errorf("API key validation failed: %s", err.Error())
	}
	if user == nil {
		return nil, nil, "", fmt.Errorf("invalid API key")
	}

	return resolveProviderForUser(user, requestedModel, lang)
}

// resolveProviderForUser is the shared logic for JWT and API key auth paths.
// Given a validated user, resolves the model route and provider.
func resolveProviderForUser(user *iamsdk.User, requestedModel string, lang string) (*object.Provider, *iamsdk.User, string, error) {
	// Look up the model in the static routing table.
	route := resolveModelRoute(requestedModel)
	if route == nil {
		return nil, user, "", fmt.Errorf(
			"model %q is not available. Use GET /api/models to list available models",
			requestedModel,
		)
	}

	// Fetch the provider entry that holds API keys/URLs for this upstream.
	// GetModelProviderByName returns a shallow copy, safe to mutate.
	provider, err := object.GetModelProviderByName(route.providerName)
	if err != nil {
		return nil, user, "", fmt.Errorf("failed to get provider %q: %s", route.providerName, err.Error())
	}
	if provider == nil {
		return nil, user, "", fmt.Errorf("provider %q not configured in database", route.providerName)
	}

	// All models require prepaid balance. New accounts receive a $5 starter
	// credit that works only for non-premium (DO-AI) models.
	// Premium models (Fireworks, OpenAI Direct, Zen) require the user to
	// have added funds beyond the starter credit.
	balance, err := getUserBalance(user.Owner + "/" + user.Name)
	if err != nil {
		return nil, user, "", fmt.Errorf("failed to verify account balance: %s", err.Error())
	}

	if balance <= 0 {
		return nil, user, "", fmt.Errorf(
			"model %q requires a positive balance. Your current balance is $%.2f. "+
				"Add funds at https://hanzo.ai/billing",
			requestedModel, balance,
		)
	}

	// Premium models require funds beyond the starter credit.
	// A balance <= StarterCreditDollars means the user only has free credit.
	starterCredit := StarterCreditDollars
	if cfg := GetModelConfig(); cfg != nil {
		starterCredit = cfg.StarterCreditDollars()
	}
	if route.premium && balance <= starterCredit {
		return nil, user, "", fmt.Errorf(
			"model %q is a premium model requiring a paid balance. "+
				"Your current balance ($%.2f) is from the starter credit. "+
				"Add funds at https://hanzo.ai/billing to access premium models",
			requestedModel, balance,
		)
	}

	user.Balance = balance

	return provider, user, route.upstreamModel, nil
}

// iamAuthQuery returns the clientId/clientSecret query string for IAM API auth.
func iamAuthQuery() string {
	clientId := conf.GetConfigString("clientId")
	clientSecret := conf.GetConfigString("clientSecret")
	if clientId != "" && clientSecret != "" {
		return "&clientId=" + clientId + "&clientSecret=" + clientSecret
	}
	return ""
}

// getUserByAccessKey looks up a user by their IAM API key via Hanzo IAM.
func getUserByAccessKey(accessKey string) (*iamsdk.User, error) {
	// Call IAM's get-user endpoint with accessKey query parameter
	iamEndpoint := conf.GetConfigString("iamEndpoint")
	if iamEndpoint == "" {
		return nil, fmt.Errorf("iamEndpoint is not configured")
	}
	iamEndpoint = strings.TrimRight(iamEndpoint, "/")

	url := fmt.Sprintf("%s/api/get-user?accessKey=%s%s", iamEndpoint, accessKey, iamAuthQuery())

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodGet, url, nil)
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
		Status string       `json:"status"`
		Msg    string       `json:"msg"`
		Data   *iamsdk.User `json:"data"`
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

	payload := map[string]interface{}{
		"user":             record.User,
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
		User:      record.User,
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
// @router /api/chat/completions [post]
func (c *ApiController) ChatCompletions() {
	// Extract Bearer token
	authHeader := c.Ctx.Request.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		c.ResponseError(c.T("openai:Invalid API key format. Expected 'Bearer API_KEY'"))
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

	// Parse request body
	var request openai.ChatCompletionRequest
	err := json.Unmarshal(c.Ctx.Input.RequestBody, &request)
	if err != nil {
		c.ResponseError(fmt.Sprintf("Failed to parse request: %s", err.Error()))
		return
	}

	var provider *object.Provider
	var authUser *iamsdk.User
	var upstreamModel string
	var isPremium bool

	if isWidgetKey(token) {
		// Authenticate via widget key (hz_...) — restricted model access, no balance check
		var widgetUpstream string
		provider, widgetUpstream, err = resolveProviderFromWidgetKey(token, request.Model, c.GetAcceptLanguage())
		if err != nil {
			c.ResponseError(fmt.Sprintf("Widget authentication failed: %s", err.Error()))
			return
		}
		upstreamModel = widgetUpstream
		// Cap max_tokens for widget requests
		if request.MaxTokens == 0 || request.MaxTokens > widgetMaxTokens {
			request.MaxTokens = widgetMaxTokens
		}
		// Track as anonymous widget usage
		c.Ctx.Input.SetParam("recordUserId", "widget/anonymous")
		logs.Info("Widget key access: model=%s, upstream=%s", request.Model, upstreamModel)
	} else if isIAMApiKey(token) {
		// Authenticate via IAM API key (hk-...) — full model routing
		provider, authUser, upstreamModel, err = resolveProviderFromIAMKey(token, request.Model, c.GetAcceptLanguage())
		if err != nil {
			c.ResponseError(fmt.Sprintf("Authentication failed: %s", err.Error()))
			return
		}
		if authUser != nil {
			userId := authUser.Owner + "/" + authUser.Name
			c.Ctx.Input.SetParam("recordUserId", userId)
		}
		if route := resolveModelRoute(request.Model); route != nil {
			isPremium = route.premium
		}
	} else if isJwtToken(token) {
		// Authenticate via hanzo.id JWT token — full model routing
		provider, authUser, upstreamModel, err = resolveProviderFromJwt(token, request.Model, c.GetAcceptLanguage())
		if err != nil {
			c.ResponseError(fmt.Sprintf("Authentication failed: %s", err.Error()))
			return
		}
		if authUser != nil {
			userId := authUser.Owner + "/" + authUser.Name
			c.Ctx.Input.SetParam("recordUserId", userId)
		}
		if route := resolveModelRoute(request.Model); route != nil {
			isPremium = route.premium
		}
	} else {
		// Authenticate via provider API key (sk-...) — direct provider access
		provider, err = object.GetProviderByProviderKey(token, c.GetAcceptLanguage())
		if err != nil {
			c.ResponseError(fmt.Sprintf("Authentication failed: %s", err.Error()))
			return
		}
		if provider == nil {
			c.ResponseError("Authentication failed: invalid API key")
			return
		}
		// Apply model routing for sk- keys too. If the route points to a
		// different provider than the one that owns the API key, switch to
		// the route's provider so zen/fireworks models work with any key.
		if route := resolveModelRoute(request.Model); route != nil {
			upstreamModel = route.upstreamModel
			isPremium = route.premium
			if route.providerName != provider.Name {
				routeProvider, routeErr := object.GetModelProviderByName(route.providerName)
				if routeErr == nil && routeProvider != nil {
					provider = routeProvider
				}
			}
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
		switch msg.Role {
		case "system":
			systemPrompt = msg.Content
		case "user":
			question = msg.Content
		case "assistant":
			history = append(history, &model.RawMessage{
				Author: "AI",
				Text:   msg.Content,
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
		Response:  *c.Ctx.ResponseWriter,
		Buffer:    []byte{},
		RequestID: requestId,
		Stream:    request.Stream,
		Cleaner:   *NewCleaner(6),
		Model:     request.Model,
	}

	knowledge := []*model.RawMessage{}

	// Resolve the route for failover (may have fallback providers)
	route := resolveModelRoute(request.Model)

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
// @router /api/models [get]
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
	// hz_ (Hanzo token). JWTs are identified by containing at least two dots.
	if token != "" {
		validFormat := strings.HasPrefix(token, "hk-") ||
			strings.HasPrefix(token, "sk-") ||
			strings.HasPrefix(token, "pk-") ||
			strings.HasPrefix(token, "hz_") ||
			strings.Count(token, ".") >= 2 // JWT: header.payload.signature
		if !validFormat {
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
