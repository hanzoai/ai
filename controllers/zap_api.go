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
)

// ── ZAP-over-HTTP dispatch ──────────────────────────────────────────────
//
// Single endpoint that speaks the ZAP JSON envelope protocol.
// ZAP is the DEFAULT protocol for all Hanzo services. HTTP/JSON REST
// endpoints remain as a fallback for legacy clients.
//
//	POST /zap
//
// Request:  {"method":"chat.completions","id":"req-1","params":{...}}
// Response: {"id":"req-1","result":{...}} or SSE stream
//
// Methods:
//   chat.completions  → OpenAI-compatible chat completion
//   chat.messages     → Anthropic-compatible messages
//   models.list       → List available models
//   billing.balance   → Get user balance

// zapRequest is the ZAP JSON envelope.
type zapRequest struct {
	Method string          `json:"method"`
	ID     string          `json:"id"`
	Params json.RawMessage `json:"params"`
}

// zapResponse is the ZAP JSON response envelope.
type zapResponse struct {
	ID     string      `json:"id"`
	Result interface{} `json:"result,omitempty"`
	Error  *zapError   `json:"error,omitempty"`
}

// zapError follows JSON-RPC 2.0 error conventions.
type zapError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ZapDispatch is the unified ZAP endpoint.
// @Title ZapDispatch
// @Tag ZAP Protocol
// @Description ZAP-over-HTTP dispatch endpoint. Routes to chat, messages, models, and billing.
// @Param   body    body    zapRequest  true    "ZAP request envelope"
// @Success 200 {object} zapResponse
// @router /zap [post]
func (c *ApiController) ZapDispatch() {
	var req zapRequest
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, &req); err != nil {
		c.respondZapError(req.ID, -32700, "parse error: "+err.Error())
		return
	}

	if req.Method == "" {
		c.respondZapError(req.ID, -32600, "method is required")
		return
	}

	switch req.Method {
	case "chat.completions":
		c.zapChatCompletions(req)
	case "chat.messages":
		c.zapMessages(req)
	case "models.list":
		c.zapListModels(req)
	case "billing.balance":
		c.zapBalance(req)
	default:
		c.respondZapError(req.ID, -32601, "method not found: "+req.Method)
	}
}

// ── ZAP method: chat.completions ────────────────────────────────────────

func (c *ApiController) zapChatCompletions(req zapRequest) {
	// Re-inject the params as request body and delegate to existing handler.
	// The ChatCompletions handler reads from c.Ctx.Input.RequestBody.
	c.Ctx.Input.RequestBody = req.Params
	c.ChatCompletions()
}

// ── ZAP method: chat.messages (Anthropic) ───────────────────────────────

func (c *ApiController) zapMessages(req zapRequest) {
	c.Ctx.Input.RequestBody = req.Params
	c.AnthropicMessages()
}

// ── ZAP method: models.list ─────────────────────────────────────────────

type zapModelEntry struct {
	ID       string `json:"id"`
	Object   string `json:"object"`
	OwnedBy  string `json:"owned_by"`
	Premium  bool   `json:"premium,omitempty"`
	Upstream string `json:"upstream,omitempty"`
}

func (c *ApiController) zapListModels(req zapRequest) {
	if cfg := GetModelConfig(); cfg != nil {
		models := cfg.ListModelsWithUpstream()
		c.respondZap(req.ID, map[string]interface{}{
			"object": "list",
			"data":   models,
		})
		return
	}

	// Static fallback
	models := make([]zapModelEntry, 0)

	for name, route := range modelRoutes {
		models = append(models, zapModelEntry{
			ID:       name,
			Object:   "model",
			OwnedBy:  route.providerName,
			Premium:  route.premium,
			Upstream: route.upstreamModel,
		})
	}

	c.respondZap(req.ID, map[string]interface{}{
		"object": "list",
		"data":   models,
	})
}

// ── ZAP method: billing.balance ─────────────────────────────────────────

type zapBalanceParams struct {
	User     string `json:"user"`
	Currency string `json:"currency"`
}

func (c *ApiController) zapBalance(req zapRequest) {
	var params zapBalanceParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		c.respondZapError(req.ID, -32602, "invalid params: "+err.Error())
		return
	}

	// If no user specified, try to get from auth
	userId := params.User
	if userId == "" {
		token := c.Ctx.Request.Header.Get("Authorization")
		if strings.HasPrefix(token, "Bearer ") {
			token = strings.TrimPrefix(token, "Bearer ")
		}
		if token == "" {
			token = c.Ctx.Request.Header.Get("x-api-key")
		}
		if token != "" && isIAMApiKey(token) {
			if user, err := getUserByAccessKey(token); err == nil && user != nil {
				userId = user.Owner + "/" + user.Name
			}
		}
	}

	if userId == "" {
		c.respondZapError(req.ID, -32602, "user is required (provide in params or authenticate)")
		return
	}

	balance, err := getUserBalance(userId)
	if err != nil {
		c.respondZapError(req.ID, -32000, fmt.Sprintf("balance query failed: %s", err.Error()))
		return
	}

	c.respondZap(req.ID, map[string]interface{}{
		"user":      userId,
		"balance":   balance,
		"currency":  "usd",
		"available": balance,
	})
}

// ── Response helpers ────────────────────────────────────────────────────

func (c *ApiController) respondZap(id string, result interface{}) {
	resp := zapResponse{ID: id, Result: result}
	jsonData, err := json.Marshal(resp)
	if err != nil {
		c.respondZapError(id, -32603, "internal error: "+err.Error())
		return
	}
	c.Ctx.Output.Header("Content-Type", "application/json")
	c.Ctx.Output.Body(jsonData)
	c.EnableRender = false
}

func (c *ApiController) respondZapError(id string, code int, message string) {
	resp := zapResponse{
		ID:    id,
		Error: &zapError{Code: code, Message: message},
	}
	jsonData, _ := json.Marshal(resp)
	c.Ctx.Output.Header("Content-Type", "application/json")
	c.Ctx.ResponseWriter.WriteHeader(200) // ZAP errors are app-level, not HTTP-level
	c.Ctx.Output.Body(jsonData)
	c.EnableRender = false
}
