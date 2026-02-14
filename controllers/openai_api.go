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

	iamsdk "github.com/casdoor/casdoor-go-sdk/casdoorsdk"
	"github.com/hanzoai/cloud/model"
	"github.com/hanzoai/cloud/object"
	"github.com/hanzoai/cloud/util"
	"github.com/sashabaranov/go-openai"
)

// isJwtToken checks if a token looks like a JWT (3 base64 segments separated by dots).
func isJwtToken(token string) bool {
	parts := strings.Split(token, ".")
	return len(parts) == 3 && len(parts[0]) > 10 && len(parts[1]) > 10
}

// resolveProviderFromJwt validates a hanzo.id JWT token and returns the
// appropriate model provider for the requested model. Free-tier users
// (Balance <= 0) are restricted to the default DigitalOcean-backed provider.
func resolveProviderFromJwt(token string, requestedModel string, lang string) (*object.Provider, *iamsdk.User, error) {
	claims, err := iamsdk.ParseJwtToken(token)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid hanzo.id token: %s", err.Error())
	}

	user := &claims.User

	// Route model name to the correct provider type
	providerType := modelToProviderType(requestedModel)

	// Look up the provider for this model
	var provider *object.Provider
	if providerType != "" {
		provider, err = object.GetModelProviderByType(providerType)
		if err != nil {
			return nil, user, fmt.Errorf("failed to get model provider: %s", err.Error())
		}
	}

	// Fall back to default provider if no specific match
	if provider == nil {
		provider, err = object.GetDefaultModelProvider()
		if err != nil {
			return nil, user, fmt.Errorf("failed to get default model provider: %s", err.Error())
		}
		if provider == nil {
			return nil, user, fmt.Errorf("no default model provider configured")
		}
	}

	// Enforce free-tier restrictions: users without prepaid balance
	// can only use the free-tier DigitalOcean provider.
	if !isDigitalOceanProvider(provider) && user.Balance <= 0 {
		// JWT tokens may not include the balance field; fetch from IAM API
		// to get the authoritative balance before rejecting the request.
		if user.Name != "" {
			iamUser, err := iamsdk.GetUser(user.Name)
			if err == nil && iamUser != nil {
				user.Balance = iamUser.Balance
			}
		}

		if user.Balance <= 0 {
			return nil, user, fmt.Errorf(
				"model %q requires a paid plan. Your current balance is $%.2f. "+
					"Add funds at https://hanzo.ai/billing to access premium models, "+
					"or use a free-tier model instead",
				requestedModel, user.Balance,
			)
		}
	}

	return provider, user, nil
}

// modelToProviderType maps a model name prefix to the provider type string
// used in the database. Returns empty string for unknown models (falls back
// to the default provider).
func modelToProviderType(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.HasPrefix(m, "claude-"):
		return "Claude"
	case strings.HasPrefix(m, "gpt-"), strings.HasPrefix(m, "o1-"), strings.HasPrefix(m, "o3-"), strings.HasPrefix(m, "o4-"):
		return "OpenAI"
	case strings.HasPrefix(m, "qwen"), strings.Contains(m, "fireworks"):
		return "Fireworks"
	case strings.HasPrefix(m, "gemini-"):
		return "Gemini"
	case strings.HasPrefix(m, "deepseek-"):
		return "DeepSeek"
	default:
		return ""
	}
}

// isDigitalOceanProvider checks if a provider is backed by DigitalOcean
// infrastructure (free-tier eligible).
func isDigitalOceanProvider(provider *object.Provider) bool {
	return provider.Type == "DigitalOcean"
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

	if isJwtToken(token) {
		// Authenticate via hanzo.id JWT token and resolve model → provider.
		// Free-tier users (Balance <= 0) are restricted to DigitalOcean models.
		provider, authUser, err = resolveProviderFromJwt(token, request.Model, c.GetAcceptLanguage())
		if err != nil {
			c.ResponseError(fmt.Sprintf("Authentication failed: %s", err.Error()))
			return
		}
		_ = authUser
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

	// Use the model from the request if provided, otherwise fall back to provider's subType
	if request.Model != "" {
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
