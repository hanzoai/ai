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
// Clients connect directly to cloud-api:9999. No gateway, no proxy,
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

	"github.com/hanzoai/beego/logs"
	iam "github.com/hanzoai/iam"
	"github.com/luxfi/zap"
	openai "github.com/sashabaranov/go-openai"

	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
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
	case path == "/v1/chat" || path == "/v1/chat/completions" || path == "/v1/completions":
		return zapChatHandler(ctx, auth, body)
	case path == "/v1/models":
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
	case strings.HasPrefix(path, "/v1/balance"):
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

// The per-tenant trace ledger (canonical hanzo.observations / hanzo.traces, the
// OTel GenAI observations tables) is owned and populated by the o11y/insights
// ingestion pipeline, NOT this module. This module writes only the spend ledger
// (hanzo.cloud_usage, above). One writer per table — the ai module does not write
// a second, incompatible observations shape into the o11y-owned table.

// ── Datastore billing record writer (direct datastore client) ──────────
//
// Writes billing/usage records to hanzo.cloud_usage for invoice reconciliation
// via the direct datastore client (object/datastore.go) — NOT a ZAP peer (see the
// "WHY NOT ZAP" note atop object/datastore.go). Both Commerce and Console query
// this table for unified billing views.

func zapWriteUsage(record *usageRecord, startTime time.Time) {
	if !object.DatastoreEnabled() {
		return
	}

	// Schema lives once in object.cloudUsageTableDDL; the read side ensures it too.
	{
		ensureCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := object.EnsureCloudUsageTable(ensureCtx); err != nil {
			logs.Warn("ZAP: ensure cloud_usage table: %v", err)
		}
		cancel()
	}

	org := record.Organization
	if org == "" {
		org = record.Owner
	}

	// Cost authority = usageCostCents (the SAME function the debit uses): it prices
	// image/video per-unit (ImageCount/VideoCount) and text per-token, so this
	// analytics/reconciliation ledger's cost_cents matches what the customer was
	// billed. The prior token-only calc recorded image/video spend as $0 (those
	// rows carry no tokens) — under-reporting that revenue in console2 Overview,
	// the admin god-view, and spend-by-model.
	costCents := usageCostCents(record)

	premium := uint8(0)
	if record.Premium {
		premium = 1
	}
	stream := uint8(0)
	if record.Stream {
		stream = 1
	}
	byo := uint8(0)
	if record.BYO {
		byo = 1
	}
	// The platform fee is a pure function of (costCents, BYO) — derive it here from
	// the cost this writer already computed rather than depending on recordUsage's
	// stamp (recordUsage may early-return without stamping when the billing queue
	// is off, while this warehouse path is independent). Same value as record.FeeCents.
	feeCents := platformFeeCents(costCents, record.BYO)

	// Nano-USD margin ledger: cost_nano = provider COGS, billed_nano = org debit,
	// margin_nano = billed − cost. Derived here (self-contained, like feeCents) from
	// the ONE usageMargin so the warehouse row matches the debit and the o11y span.
	m := usageMargin(record)
	// Honest "priced?" flag — self-contained (this writer runs independent of
	// recordUsage's stamp): 1 when the model billed at the conservative default.
	unpriced := uint8(0)
	if recordUnpriced(record) {
		unpriced = 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := object.DatastoreExec(
		ctx,
		`INSERT INTO hanzo.cloud_usage (id, timestamp, owner, user_id, organization, model, provider, request_id, prompt_tokens, completion_tokens, total_tokens, cache_read_tokens, cache_write_tokens, cost_cents, currency, status, error_msg, is_premium, is_stream, client_ip, byo, fee_cents, account, cost_nano, billed_nano, margin_nano, unpriced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.RequestID, startTime.UTC(),
		record.Owner, record.User, org,
		record.Model, record.Provider, record.RequestID,
		record.PromptTokens, record.CompletionTokens, record.TotalTokens,
		record.CacheReadTokens, record.CacheWriteTokens,
		costCents, "usd",
		record.Status, record.ErrorMsg,
		premium, stream, record.ClientIP,
		byo, feeCents, record.Account,
		m.CostNano, m.BilledNano, m.MarginNano, unpriced,
	)
	if err != nil {
		logs.Warn("ZAP: usage write failed: %v", err)
	}
}

// ── models.list ─────────────────────────────────────────────────────────

func zapListModelsHandler() (*zap.Message, error) {
	models := listAvailableModels()
	data, _ := json.Marshal(modelListEnvelope(models))
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

	// The balance lives under the billing SUBJECT within the org NAMESPACE.
	// userId is "owner/name" (or a bare org); namespace = the org part, subject
	// = object.BillingSubject(owner, name) (per-user for a personal-billing org).
	namespace := userId
	name := ""
	if i := strings.IndexByte(userId, '/'); i >= 0 {
		namespace = userId[:i]
		name = userId[i+1:]
	}
	subject := object.BillingSubject(namespace, name)

	balance, err := getUserBalance(subject, namespace)
	if err != nil {
		return object.BuildCloudResponse(500, nil, "balance query failed: "+err.Error())
	}

	data, _ := json.Marshal(map[string]interface{}{
		"user":      subject,
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
			// Check the billing SUBJECT within the org NAMESPACE (per-user for a
			// personal-billing org), matching the gate and usage debit.
			balance, balErr := getUserBalance(object.BillingSubject(authUser.Owner, authUser.Name), authUser.Owner)
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
			recordTrace(ctx, record, requestStartTime)
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
		// signature + iss/aud validation (R3), never raw iam.ParseJwtToken
		claims, err := object.ParseAndValidateJWT(token)
		if err == nil && claims != nil {
			return claims.Owner + "/" + claims.Name, nil
		}
	}

	return "", fmt.Errorf("unsupported auth type")
}

func zapResolveAuth(auth string, requestModel string) (*object.Provider, *iam.User, string, error) {
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
