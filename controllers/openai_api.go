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
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	iamsdk "github.com/casdoor/casdoor-go-sdk/casdoorsdk"
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

// resolveProviderFromJwt validates a hanzo.id JWT token and returns the
// appropriate model provider for the requested model, plus the translated
// upstream model name.
//
// Architecture: JWT provides identity (who the user is). Balance is mutable
// financial state checked against IAM at request time — never from the JWT,
// since JWTs are valid for days and balance changes on every transaction.
//
// Models are resolved via the static routing table in model_routes.go.
// Each route specifies which DB provider holds the API key and the upstream
// model ID to forward to. Premium models require balance > 0.
func resolveProviderFromJwt(token string, requestedModel string, lang string) (*object.Provider, *iamsdk.User, string, error) {
	claims, err := iamsdk.ParseJwtToken(token)
	if err != nil {
		return nil, nil, "", fmt.Errorf("invalid hanzo.id token: %s", err.Error())
	}

	user := &claims.User

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

// ChatCompletions implements the OpenAI-compatible chat completions API
// @Title ChatCompletions
// @Tag OpenAI Compatible API
// @Description OpenAI compatible chat completions API. Accepts either a provider
// API key (sk-...) or a hanzo.id OAuth JWT token for authentication.
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

	if isJwtToken(token) {
		// Authenticate via hanzo.id JWT token and resolve model → provider.
		provider, authUser, upstreamModel, err = resolveProviderFromJwt(token, request.Model, c.GetAcceptLanguage())
		if err != nil {
			c.ResponseError(fmt.Sprintf("Authentication failed: %s", err.Error()))
			return
		}
		// Store user identity in request context for the record middleware
		// (AfterRecordMessage) to attribute usage to this user.
		if authUser != nil {
			userId := authUser.Owner + "/" + authUser.Name
			c.Ctx.Input.SetParam("recordUserId", userId)
		}
	} else {
		// Authenticate via provider API key (sk-...)
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

	// Set the upstream model name on the provider. For JWT auth, this is the
	// translated upstream model from the routing table. For API key auth,
	// fall back to the request model or provider's default.
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
		c.ResponseError(err.Error())
		return
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
