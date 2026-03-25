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

// Native ZAP service handlers. Pure ZAP binary protocol, no HTTP.
//
// Clients connect directly to cloud-api:9651. No gateway, no proxy,
// no sidecars. ZAP-to-ZAP end-to-end.
//
// Message type 100 (native cloud):
//   Request:  method(0:Text) + auth(8:Text) + body(16:Bytes)
//   Response: status(0:Uint32) + body(4:Bytes) + error(12:Text)
//
// Message type 200 (gateway → cloud HTTP-over-ZAP):
//   Request:  method(0:Text) + path(8:Text) + headers(16:Bytes) + body(24:Bytes) + query(32:Text)
//   Response: status(0:Uint32) + body(4:Bytes) + headers(12:Bytes)

package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/beego/beego/logs"
	iamsdk "github.com/hanzoid/go-sdk/casdoorsdk"
	"github.com/luxfi/zap"
	openai "github.com/sashabaranov/go-openai"

	"github.com/hanzoai/cloud/model"
	"github.com/hanzoai/cloud/object"
	"github.com/hanzoai/cloud/util"
)

// InitZapHandlers registers native ZAP service handlers on the node.
func InitZapHandlers() {
	node := object.GetZapNode()
	if node == nil {
		return
	}

	node.Handle(object.MsgTypeCloud, handleCloudService)
	node.Handle(object.MsgTypeHTTPRequest, handleGatewayHTTPRequest)
	logs.Info("ZAP: registered handlers (cloud=%d, gateway=%d)", object.MsgTypeCloud, object.MsgTypeHTTPRequest)
}

func handleCloudService(ctx context.Context, from string, msg *zap.Message) (*zap.Message, error) {
	root := msg.Root()
	method := root.Text(object.CloudReqMethod)
	auth := root.Text(object.CloudReqAuth)
	body := root.Bytes(object.CloudReqBody)

	switch method {
	case "models.list":
		// R-04: require auth for model listing
		if auth == "" {
			return object.BuildCloudResponse(401, nil, "authentication required")
		}
		return zapListModelsHandler()
	case "balance":
		return zapBalanceHandler(auth, body)
	case "chat.completions", "chat.messages":
		return zapChatHandler(ctx, auth, body)
	default:
		return object.BuildCloudResponse(404, nil, "unknown method: "+method)
	}
}

// ── Gateway HTTP-over-ZAP (MsgType 200) ─────────────────────────────────
//
// The gateway forwards HTTP requests as ZAP messages. We dispatch by path
// to the same handlers used by native cloud service, then return a gateway
// response (status + body + headers).

func handleGatewayHTTPRequest(ctx context.Context, from string, msg *zap.Message) (*zap.Message, error) {
	root := msg.Root()
	path := root.Text(8)
	body := root.Bytes(24)

	// Extract auth from headers JSON: {"Authorization":"Bearer xxx", ...}
	auth := extractAuthFromHeaders(root.Bytes(16))

	switch {
	case path == "/api/chat/completions" || path == "/v1/chat/completions":
		return zapChatHandler(ctx, auth, body)
	case path == "/api/models" || path == "/v1/models":
		// R-04: require auth for model listing
		if auth == "" {
			errBody, _ := json.Marshal(map[string]interface{}{
				"error": map[string]string{
					"message": "Authentication required. Provide a Bearer token.",
					"type":    "authentication_error",
					"code":    "unauthorized",
				},
			})
			return object.BuildGatewayResponse(401, errBody, nil)
		}
		return zapListModelsHandler()
	case strings.HasPrefix(path, "/api/balance") || strings.HasPrefix(path, "/v1/balance"):
		return zapBalanceHandler(auth, body)
	default:
		errBody, _ := json.Marshal(map[string]string{"error": "not found: " + path})
		return object.BuildGatewayResponse(404, errBody, nil)
	}
}

// extractAuthFromHeaders parses the Authorization header from a JSON-encoded
// headers map sent by the gateway.
func extractAuthFromHeaders(headersJSON []byte) string {
	if len(headersJSON) == 0 {
		return ""
	}
	var headers map[string]string
	if err := json.Unmarshal(headersJSON, &headers); err != nil {
		return ""
	}
	if auth, ok := headers["Authorization"]; ok {
		return auth
	}
	if auth, ok := headers["authorization"]; ok {
		return auth
	}
	return ""
}

// ── ZAP trace writer (datastore → ClickHouse) ──────────────────────────
//
// Writes observability traces directly to ClickHouse via native ZAP binary.
// Bypasses the Console HTTP ingestion endpoint when datastore is connected.

func zapWriteTrace(record *usageRecord, startTime time.Time) {
	if !object.DatastoreEnabled() {
		return
	}

	endTime := time.Now().UTC()
	traceID := util.GenerateUUID()
	genID := util.GenerateUUID()

	org := record.Organization
	if org == "" {
		org = record.Owner
	}

	costCents := calculateCostCentsWithCache(
		record.Model, record.PromptTokens, record.CompletionTokens,
		record.CacheReadTokens, record.CacheWriteTokens,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Insert trace
	err := object.ZapDatastoreExec(ctx,
		`INSERT INTO hanzo.observations (id, trace_id, name, start_time, end_time, type, model, prompt_tokens, completion_tokens, total_tokens, input_cost, output_cost, total_cost, metadata, tags, user_id, session_id, level, status_message, version) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		genID, traceID, "chat-completion",
		startTime.UTC(), endTime,
		"GENERATION", record.Model,
		record.PromptTokens, record.CompletionTokens, record.TotalTokens,
		float64(costCents)*float64(record.PromptTokens)/float64(max(record.TotalTokens, 1)),
		float64(costCents)*float64(record.CompletionTokens)/float64(max(record.TotalTokens, 1)),
		float64(costCents),
		fmt.Sprintf(`{"provider":"%s","organization":"%s","requestId":"%s","premium":%v,"stream":%v}`,
			record.Provider, org, record.RequestID, record.Premium, record.Stream),
		fmt.Sprintf(`["%s","%s","org:%s"]`, record.Model, record.Provider, org),
		record.User, record.RequestID,
		"DEFAULT", record.Status, "cloud-api",
	)
	if err != nil {
		logs.Warn("ZAP: trace write failed: %v", err)
	}
}

// ── ZAP billing record writer (datastore → ClickHouse) ──────────────────
//
// Writes billing/usage records to hanzo.cloud_usage for invoice reconciliation.
// Both Commerce and Console can query this table for unified billing views.

var usageTableCreated bool

func zapEnsureUsageTable() {
	if usageTableCreated {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := object.ZapDatastoreExec(ctx, `
		CREATE TABLE IF NOT EXISTS hanzo.cloud_usage (
			id String,
			timestamp DateTime,
			owner String,
			user_id String,
			organization String,
			model String,
			provider String,
			request_id String,
			prompt_tokens UInt32,
			completion_tokens UInt32,
			total_tokens UInt32,
			cache_read_tokens UInt32,
			cache_write_tokens UInt32,
			cost_cents UInt64,
			currency String,
			status String,
			error_msg String,
			is_premium UInt8,
			is_stream UInt8,
			client_ip String
		) ENGINE = MergeTree()
		ORDER BY (timestamp, organization, user_id)
		TTL timestamp + INTERVAL 2 YEAR
	`)
	if err != nil {
		logs.Warn("ZAP: failed to create cloud_usage table: %v", err)
		return
	}
	usageTableCreated = true
}

func zapWriteUsage(record *usageRecord, startTime time.Time) {
	if !object.DatastoreEnabled() {
		return
	}

	zapEnsureUsageTable()

	org := record.Organization
	if org == "" {
		org = record.Owner
	}

	costCents := calculateCostCentsWithCache(
		record.Model, record.PromptTokens, record.CompletionTokens,
		record.CacheReadTokens, record.CacheWriteTokens,
	)

	premium := uint8(0)
	if record.Premium {
		premium = 1
	}
	stream := uint8(0)
	if record.Stream {
		stream = 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := object.ZapDatastoreExec(ctx,
		`INSERT INTO hanzo.cloud_usage (id, timestamp, owner, user_id, organization, model, provider, request_id, prompt_tokens, completion_tokens, total_tokens, cache_read_tokens, cache_write_tokens, cost_cents, currency, status, error_msg, is_premium, is_stream, client_ip) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.RequestID, startTime.UTC(),
		record.Owner, record.User, org,
		record.Model, record.Provider, record.RequestID,
		record.PromptTokens, record.CompletionTokens, record.TotalTokens,
		record.CacheReadTokens, record.CacheWriteTokens,
		costCents, "usd",
		record.Status, record.ErrorMsg,
		premium, stream, record.ClientIP,
	)
	if err != nil {
		logs.Warn("ZAP: usage write failed: %v", err)
	}
}

// ── models.list ─────────────────────────────────────────────────────────

func zapListModelsHandler() (*zap.Message, error) {
	models := listAvailableModels()
	data, _ := json.Marshal(map[string]interface{}{
		"object": "list",
		"data":   models,
	})
	return object.BuildCloudResponse(200, data, "")
}

// ── balance ─────────────────────────────────────────────────────────────

func zapBalanceHandler(auth string, body []byte) (*zap.Message, error) {
	userId, err := zapResolveUser(auth)
	if err != nil {
		return object.BuildCloudResponse(401, nil, err.Error())
	}

	if len(body) > 0 {
		var params struct {
			User string `json:"user"`
		}
		if json.Unmarshal(body, &params) == nil && params.User != "" {
			userId = params.User
		}
	}

	balance, err := getUserBalance(userId)
	if err != nil {
		return object.BuildCloudResponse(500, nil, "balance query failed: "+err.Error())
	}

	data, _ := json.Marshal(map[string]interface{}{
		"user":      userId,
		"balance":   balance,
		"currency":  "usd",
		"available": balance,
	})
	return object.BuildCloudResponse(200, data, "")
}

// ── chat.completions / chat.messages ────────────────────────────────────

func zapChatHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	if auth == "" {
		return object.BuildCloudResponse(401, nil, "auth token required")
	}

	var request openai.ChatCompletionRequest
	if err := json.Unmarshal(body, &request); err != nil {
		return object.BuildCloudResponse(400, nil, "invalid request: "+err.Error())
	}

	// Auth → provider + user + upstream model.
	provider, authUser, upstreamModel, err := zapResolveAuth(auth, request.Model)
	if err != nil {
		return object.BuildCloudResponse(401, nil, err.Error())
	}

	// Balance gate for premium models.
	isPremium := false
	if route := resolveModelRoute(request.Model); route != nil {
		isPremium = route.premium
		if route.premium && authUser != nil {
			userId := authUser.Owner + "/" + authUser.Name
			balance, balErr := getUserBalance(userId)
			if balErr != nil || balance <= 0 {
				return object.BuildCloudResponse(402, nil, "insufficient balance for premium model")
			}
		}
	}

	// KMS secrets.
	if err := object.ResolveProviderSecret(provider); err != nil {
		logs.Error("ZAP: KMS resolve %s: %v", provider.Name, err)
	}

	// Set upstream model on provider.
	if upstreamModel != "" {
		provider.SubType = upstreamModel
	} else if request.Model != "" {
		provider.SubType = request.Model
	}

	modelProvider, err := provider.GetModelProvider("en")
	if err != nil {
		return object.BuildCloudResponse(502, nil, "provider init failed: "+err.Error())
	}

	// Inject Zen identity for zen-branded models.
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

	// Extract question + history from messages.
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
		return object.BuildCloudResponse(400, nil, "no user message found")
	}

	if systemPrompt != "" {
		question = fmt.Sprintf("System: %s\n\nUser: %s", systemPrompt, question)
	}

	// Call the model provider. Use a buffer — no HTTP writer.
	requestStartTime := time.Now().UTC()
	requestId := util.GenerateUUID()
	var buf bytes.Buffer

	modelResult, err := modelProvider.QueryText(question, &buf, history, "", nil, nil, "en")
	if err != nil {
		if authUser != nil {
			go recordUsage(&usageRecord{
				User:      authUser.Owner + "/" + authUser.Name,
				Model:     request.Model,
				Provider:  provider.Name,
				Premium:   isPremium,
				Stream:    false,
				Status:    "error",
				ErrorMsg:  err.Error(),
				RequestID: requestId,
			})
		}
		return object.BuildCloudResponse(502, nil, "provider error: "+err.Error())
	}

	// Build response.
	answer := buf.String()
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
	data, _ := json.Marshal(response)

	// Record billing.
	if authUser != nil {
		go func() {
			record := &usageRecord{
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
				Stream:           false,
				Status:           "success",
				RequestID:        requestId,
			}
			recordUsage(record)
			recordTrace(record, requestStartTime)
		}()
	}

	return object.BuildCloudResponse(200, data, "")
}

// ── Auth helpers ────────────────────────────────────────────────────────

func zapResolveUser(auth string) (string, error) {
	if auth == "" {
		return "", fmt.Errorf("auth token required")
	}
	token := strings.TrimPrefix(auth, "Bearer ")

	if isIAMApiKey(token) {
		user, err := getUserByAccessKey(token)
		if err != nil {
			return "", fmt.Errorf("invalid API key: %w", err)
		}
		if user != nil {
			return user.Owner + "/" + user.Name, nil
		}
	}

	if isJwtToken(token) {
		claims, err := iamsdk.ParseJwtToken(token)
		if err == nil && claims != nil {
			return claims.Owner + "/" + claims.Name, nil
		}
	}

	return "", fmt.Errorf("unsupported auth type")
}

func zapResolveAuth(auth string, requestModel string) (*object.Provider, *iamsdk.User, string, error) {
	token := strings.TrimPrefix(auth, "Bearer ")

	if isIAMApiKey(token) {
		return resolveProviderFromIAMKey(token, requestModel, "en")
	}
	if isJwtToken(token) {
		return resolveProviderFromJwt(token, requestModel, "en")
	}

	// Direct provider key (sk-...).
	provider, err := object.GetProviderByProviderKey(token, "en")
	if err != nil || provider == nil {
		return nil, nil, "", fmt.Errorf("invalid auth token")
	}

	upstreamModel := ""
	if route := resolveModelRoute(requestModel); route != nil {
		upstreamModel = route.upstreamModel
	}
	return provider, nil, upstreamModel, nil
}
