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
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	iamsdk "github.com/casdoor/casdoor-go-sdk/casdoorsdk"
	"github.com/hanzoai/cloud/conf"
	"github.com/hanzoai/cloud/model"
	"github.com/hanzoai/cloud/object"
	"github.com/hanzoai/cloud/util"
	"github.com/sashabaranov/go-openai"
)

// balanceEntry caches a user's balance from IAM to avoid per-request lookups.
type balanceEntry struct {
	balance   float64
	fetchedAt time.Time
}

var (
	balanceCache    = make(map[string]*balanceEntry)
	balanceCacheMu  sync.RWMutex
	balanceCacheTTL = 30 * time.Second
)

// getUserBalance returns the current balance for a user, fetching from IAM
// and caching briefly. Balance is mutable financial state (not identity) so
// it is never read from the JWT — always checked against the source of truth.
func getUserBalance(username string) (float64, error) {
	key := username

	balanceCacheMu.RLock()
	entry, ok := balanceCache[key]
	balanceCacheMu.RUnlock()

	if ok && time.Since(entry.fetchedAt) < balanceCacheTTL {
		return entry.balance, nil
	}

	user, err := iamsdk.GetUser(username)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch user from IAM: %s", err.Error())
	}
	if user == nil {
		return 0, fmt.Errorf("user %q not found in IAM", username)
	}

	balanceCacheMu.Lock()
	balanceCache[key] = &balanceEntry{balance: user.Balance, fetchedAt: time.Now()}
	balanceCacheMu.Unlock()

	return user.Balance, nil
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

	// Premium models require a positive balance.
	if route.premium {
		balance, err := getUserBalance(user.Name)
		if err != nil {
			return nil, user, "", fmt.Errorf("failed to verify account balance: %s", err.Error())
		}

		if balance <= 0 {
			return nil, user, "", fmt.Errorf(
				"model %q requires a paid plan. Your current balance is $%.2f. "+
					"Add funds at https://hanzo.ai/billing to access premium models, "+
					"or use a free-tier model instead",
				requestedModel, balance,
			)
		}

		user.Balance = balance
	}

	return provider, user, route.upstreamModel, nil
}

// getUserByAccessKey looks up a user by their IAM API key via the IAM HTTP API.
func getUserByAccessKey(accessKey string) (*iamsdk.User, error) {
	// Call IAM's get-user endpoint with accessKey query parameter
	iamEndpoint := conf.GetConfigString("iamEndpoint")
	if iamEndpoint == "" {
		iamEndpoint = conf.GetConfigString("casdoorEndpoint")
	}
	if iamEndpoint == "" {
		return nil, fmt.Errorf("IAM endpoint not configured")
	}
	iamEndpoint = strings.TrimRight(iamEndpoint, "/")

	url := fmt.Sprintf("%s/api/get-user?accessKey=%s", iamEndpoint, accessKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("IAM request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("IAM returned status %d", resp.StatusCode)
	}

	var result struct {
		Status string     `json:"status"`
		Msg    string     `json:"msg"`
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
	Cost             float64 `json:"cost"`
	Currency         string  `json:"currency"`
	Premium          bool    `json:"premium"`
	Stream           bool    `json:"stream"`
	Status           string  `json:"status"`
	ErrorMsg         string  `json:"errorMsg"`
	ClientIP         string  `json:"clientIp"`
	RequestID        string  `json:"requestId"`
}

// recordUsage sends a usage record to IAM asynchronously (fire-and-forget).
func recordUsage(record *usageRecord) {
	go func() {
		iamEndpoint := conf.GetConfigString("iamEndpoint")
		if iamEndpoint == "" {
			iamEndpoint = conf.GetConfigString("casdoorEndpoint")
		}
		if iamEndpoint == "" {
			return
		}
		iamEndpoint = strings.TrimRight(iamEndpoint, "/")

		body, err := json.Marshal(record)
		if err != nil {
			return
		}

		url := iamEndpoint + "/api/add-usage-record"
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			return // best-effort, don't block model serving
		}
		resp.Body.Close()
	}()
}

// ── API handlers ────────────────────────────────────────────────────────────

// ChatCompletions implements the OpenAI-compatible chat completions API
// @Title ChatCompletions
// @Tag OpenAI Compatible API
// @Description OpenAI compatible chat completions API. Accepts:
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

	if isIAMApiKey(token) {
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

	modelProvider, err := provider.GetModelProvider(c.GetAcceptLanguage())
	if err != nil {
		c.ResponseError(fmt.Sprintf("Failed to get model provider: %s", err.Error()))
		return
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

	// Call the model provider
	modelResult, err := modelProvider.QueryText(question, writer, history, "", knowledge, nil, c.GetAcceptLanguage())
	if err != nil {
		// Record failed usage
		if authUser != nil {
			recordUsage(&usageRecord{
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
			})
		}
		c.ResponseError(err.Error())
		return
	}

	// Record successful usage
	if authUser != nil {
		recordUsage(&usageRecord{
			Owner:            authUser.Owner,
			User:             authUser.Owner + "/" + authUser.Name,
			Organization:     authUser.Owner,
			Model:            request.Model,
			Provider:         provider.Name,
			PromptTokens:     modelResult.PromptTokenCount,
			CompletionTokens: modelResult.ResponseTokenCount,
			TotalTokens:      modelResult.TotalTokenCount,
			Currency:         "USD",
			Premium:          isPremium,
			Stream:           request.Stream,
			Status:           "success",
			ClientIP:         c.Ctx.Request.RemoteAddr,
			RequestID:        requestId,
		})
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
// @Title ListModels
// @Tag OpenAI Compatible API
// @Description Returns a list of all available models. No authentication required.
// @Success 200 {object} object
// @router /api/models [get]
func (c *ApiController) ListModels() {
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
