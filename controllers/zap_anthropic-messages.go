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

// Native ZAP handlers for the Anthropic Messages route-group (strangler slice).
//
// Re-implements POST /v1/messages and POST /v1/messages/count_tokens against the
// shared object/ + iam layer, mirroring zap_native.go:zapChatHandler. It NEVER
// wraps the ApiController — same identity seam (zapResolveAuth/zapResolveUser),
// same per-org billing (reserveBudget → settle actual), same metering
// (recordUsage + recordTrace), same Anthropic error shapes.
//
// Transport parity: the ZAP frame carries ONE response message, so a `stream:true`
// request is served buffered (the whole Anthropic response in one body) — the same
// choice zapChatHandler makes. The `stream` flag is still carried on the usage
// record for metering parity.
//
// Registration convention (recipe): this file self-registers from its own init()
// — no edit to zap_native.go. handleCloudService and the gateway dispatch consult
// the shared registry (zap_registry.go) these registrations land in.

package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/log"
	openai "github.com/hanzoai/go-openai"
	"github.com/luxfi/zap"

	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/object"
)

// ── Registration (recipe per-group convention) ───────────────────────────────
//
// The registry primitives (registerCloud / registerGatewayPath + their maps and
// the lookups consulted by handleCloudService / the gateway dispatch) live ONCE
// in zap_registry.go. This group file only CALLS them from its own init(), so no
// two groups edit a shared registration file.

func init() { registerZapAnthropic() }

// registerZapAnthropic wires the Anthropic Messages group into the shared registry.
// Named so InitZapHandlers can also call it explicitly if init-order ever needs pinning.
func registerZapAnthropic() {
	registerCloud("messages", zapAnthropicMessagesCloud)
	registerCloud("messages.count_tokens", zapAnthropicCountTokensCloud)
	// Longest-prefix lookup ensures count_tokens is not shadowed by /v1/messages.
	registerGatewayPath("/v1/messages/count_tokens", zapAnthropicCountTokensGateway)
	registerGatewayPath("/v1/messages", zapAnthropicMessagesGateway)
}

// ── Transport adapters ───────────────────────────────────────────────────────
//
// The core logic returns (status, jsonBody, errText); the adapters encode it for
// their transport. The body is ALWAYS the Anthropic JSON (success: AnthropicResponse,
// error: AnthropicErrorBody) so a gateway client (Claude Code) sees the exact wire
// shape the HTTP path returns.

var anthropicJSONHeaders, _ = json.Marshal(map[string]string{"Content-Type": "application/json"})

func zapAnthropicMessagesCloud(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	status, respBody, errText := zapAnthropicMessages(ctx, auth, body)
	return object.BuildCloudResponse(uint32(status), respBody, errText)
}

func zapAnthropicMessagesGateway(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	status, respBody, _ := zapAnthropicMessages(ctx, auth, body)
	return object.BuildGatewayResponse(uint32(status), respBody, anthropicJSONHeaders)
}

func zapAnthropicCountTokensCloud(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	status, respBody, errText := zapAnthropicCountTokens(ctx, auth, body)
	return object.BuildCloudResponse(uint32(status), respBody, errText)
}

func zapAnthropicCountTokensGateway(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	status, respBody, _ := zapAnthropicCountTokens(ctx, auth, body)
	return object.BuildGatewayResponse(uint32(status), respBody, anthropicJSONHeaders)
}

// anthropicErr builds the (status, jsonBody, errText) triple for an Anthropic-shaped
// error. The JSON body is the AnthropicErrorBody the HTTP path emits; errText mirrors
// it into the cloud-transport error field.
func anthropicErr(errType, message string, status int) (int, []byte, string) {
	var body AnthropicErrorBody
	body.Type = "error"
	body.Error.Type = errType
	body.Error.Message = message
	out, _ := json.Marshal(body)
	return status, out, message
}

// ── POST /v1/messages ────────────────────────────────────────────────────────

func zapAnthropicMessages(ctx context.Context, auth string, reqBody []byte) (int, []byte, string) {
	// STEP 1 — identity: the ONE auth seam. `auth` is the raw credential (optionally
	// "Bearer …"); x-api-key parity for the gateway path is a follow-on in
	// extractAuthFromHeaders (zap_native.go), not a second seam here.
	token := strings.TrimPrefix(auth, "Bearer ")
	if token == "" {
		return anthropicErr("authentication_error", "Missing API key. Provide x-api-key header or Authorization: Bearer header.", 401)
	}
	// pk- publishable keys can never reach messages (parity with the router path).
	if isPublishableKey(token) {
		return anthropicErr("auth_error", "Publishable keys (pk-) can only access read-only endpoints. Use a secret key (sk-) for messages.", 403)
	}

	// STEP 3 — decode + validate. Authenticate BEFORE reporting a client body error
	// so an invalid credential is 401 regardless of body validity (no probe-able 400).
	var request AnthropicRequest
	badReq := ""
	if err := json.Unmarshal(reqBody, &request); err != nil {
		badReq = fmt.Sprintf("Failed to parse request: %s", err.Error())
	} else if request.Model == "" {
		badReq = "model is required"
	} else if request.MaxTokens <= 0 {
		badReq = "max_tokens is required and must be > 0"
	} else if len(request.Messages) == 0 {
		badReq = "messages must contain at least one message"
	}
	if badReq != "" {
		if _, authErr := zapResolveUser(auth); authErr != nil {
			return anthropicErr("authentication_error", authErr.Error(), 401)
		}
		return anthropicErr("invalid_request_error", badReq, 400)
	}

	requestStartTime := time.Now().UTC()

	// STEP 1/2 — auth → provider + user + upstream model, with per-org provider
	// selection (BYO seam) resolved from authUser.Owner inside zapResolveAuth.
	provider, authUser, upstreamModel, err := zapResolveAuth(auth, request.Model)
	if err != nil {
		st := statusOf(err)
		return anthropicErr(anthropicErrorTypeForStatus(st), err.Error(), st)
	}
	isPremium := false
	if route := resolveModelRoute(request.Model); route != nil {
		isPremium = route.premium
	}

	if provider.Category != "Model" {
		return anthropicErr("invalid_request_error", fmt.Sprintf("Provider %s is not a model provider", provider.Name), 400)
	}

	// Set upstream model on the provider.
	if upstreamModel != "" {
		provider.SubType = upstreamModel
	} else if request.Model != "" {
		provider.SubType = request.Model
	}

	// STEP 4 — policy gate: budget reservation, shared with the HTTP path. The
	// prepaid balance gate already ran inside zapResolveAuth (enforceBalanceGate).
	request.MaxTokens = clampMaxTokens(request.MaxTokens)
	var hold *budgetHold
	if authUser != nil {
		subject := authUser.PayerSubject("")
		est := estimateRequestCostCents(request.Model, len(request.Messages)*500, request.MaxTokens)
		var ok bool
		if hold, ok = reserveBudget(subject, est); !ok {
			return anthropicErr("billing_error", object.InsufficientBalance(zapBrandHost, authUser.Owner, "request cost").Message, http.StatusPaymentRequired)
		}
	}
	defer hold.settle(0)

	// STEP 4 — KMS secrets (env-first).
	if err := object.ResolveProviderSecret(provider); err != nil {
		log.Error("ZAP anthropic: KMS resolve %s: %v", provider.Name, err)
	}

	// ── Tool-calling proxy ────────────────────────────────────────────────
	// A request carrying tools cannot go through QueryText (no tool_use blocks);
	// translate + proxy to the upstream, buffered.
	if len(request.Tools) > 0 {
		return zapAnthropicToolRequest(ctx, provider, &request, requestStartTime, authUser, isPremium, hold)
	}

	// ── Core path: QueryText into a buffer (mirrors zapChatHandler) ────────
	oaiMessages := make([]openai.ChatCompletionMessage, 0, len(request.Messages)+1)
	if sysText := request.SystemText(); sysText != "" {
		oaiMessages = append(oaiMessages, openai.ChatCompletionMessage{Role: "system", Content: sysText})
	}
	for _, msg := range request.Messages {
		oaiMessages = append(oaiMessages, openai.ChatCompletionMessage{Role: msg.Role, Content: msg.ContentText()})
	}

	var question string
	var systemPrompt string
	history := []*model.RawMessage{}
	for _, msg := range oaiMessages {
		switch msg.Role {
		case "system":
			systemPrompt = msg.Content
		case "user":
			question = msg.Content
		case "assistant":
			history = append(history, &model.RawMessage{Author: "AI", Text: msg.Content})
		}
	}
	if question == "" {
		return anthropicErr("invalid_request_error", "No user message found in the request", 400)
	}
	if systemPrompt != "" {
		question = fmt.Sprintf("System: %s\n\nUser: %s", systemPrompt, question)
	}

	requestId := uuid.NewString()

	// STEP 5 — execute against object/ + model/ directly (buffer, no HTTP writer).
	modelProvider, err := provider.GetModelProvider("en")
	if err != nil {
		return anthropicErr("api_error", "Failed to get model provider: "+err.Error(), 500)
	}
	var buf bytes.Buffer
	knowledge := []*model.RawMessage{}
	modelResult, err := modelProvider.QueryText(question, &buf, history, "", knowledge, nil, "en")
	if err != nil {
		// STEP 6 — meter the error path too (parity with zapChatHandler/HTTP).
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
				RequestID: requestId,
			}
			errRecord.stampPayer(authUser)
			errRecord.BYO, errRecord.Account = providerBYO(provider, authUser)
			recordUsage(errRecord)
			recordTrace(ctx, errRecord, requestStartTime)
		}
		st := statusForModelError(err)
		return anthropicErr(anthropicErrorTypeForStatus(st), err.Error(), st)
	}

	// STEP 6 — meter the success terminal ONCE + settle the hold with actual cost.
	if authUser != nil {
		successRecord := &usageRecord{
			Owner:            authUser.Owner,
			User:             authUser.Owner + "/" + authUser.Name,
			Organization:     authUser.Owner,
			Model:            request.Model,
			Provider:         provider.Name,
			PromptTokens:     modelResult.PromptTokenCount,
			CacheReadTokens:  modelResult.CacheReadTokenCount,
			CacheWriteTokens: modelResult.CacheWriteTokenCount,
			CompletionTokens: modelResult.ResponseTokenCount,
			TotalTokens:      modelResult.TotalTokenCount,
			Currency:         "USD",
			Premium:          isPremium,
			Stream:           request.Stream,
			Status:           "success",
			RequestID:        requestId,
		}
		successRecord.stampPayer(authUser)
		successRecord.BYO, successRecord.Account = providerBYO(provider, authUser)
		recordUsage(successRecord)
		recordTrace(ctx, successRecord, requestStartTime)
		hold.settle(calculateCostCentsWithCache(request.Model, modelResult.PromptTokenCount, modelResult.ResponseTokenCount, 0, 0))
	}

	// STEP 7 — encode the SAME response struct the HTTP path returns.
	response := AnthropicResponse{
		ID:   "msg_" + requestId,
		Type: "message",
		Role: "assistant",
		Content: []AnthropicContentBlock{
			{Type: "text", Text: buf.String()},
		},
		Model:      request.Model,
		StopReason: "end_turn",
		Usage: AnthropicUsage{
			InputTokens:  modelResult.PromptTokenCount,
			OutputTokens: modelResult.ResponseTokenCount,
		},
	}
	out, err := json.Marshal(response)
	if err != nil {
		return anthropicErr("api_error", err.Error(), 500)
	}
	return 200, out, ""
}

// zapAnthropicToolRequest re-implements the tool-calling proxy against the shared
// translation layer (anthropic_translate.go) + plain net/http, buffered for the
// single-message ZAP transport. It reuses the EXACT translation functions the HTTP
// path uses — no forked logic — and forces a non-streaming upstream call so the
// response is clean JSON to translate (Anthropic→OpenAI request, OpenAI→Anthropic
// response). Billing lands exactly once via the shared recordUsage/recordTrace.
func zapAnthropicToolRequest(
	ctx context.Context,
	provider *object.Provider,
	request *AnthropicRequest,
	requestStartTime time.Time,
	authUser *iam.User,
	isPremium bool,
	hold *budgetHold,
) (int, []byte, string) {
	requestId := uuid.NewString()

	// Native Anthropic upstream: forward the raw request verbatim (non-stream).
	if provider.Type == "Claude" || provider.Type == "Anthropic" {
		fwd := *request
		fwd.Stream = false
		apiKey := provider.ClientSecret
		baseURL := strings.TrimRight(provider.ProviderUrl, "/")
		if baseURL == "" {
			baseURL = "https://api.anthropic.com"
		}
		body, err := json.Marshal(&fwd)
		if err != nil {
			return anthropicErr("api_error", "Failed to marshal request: "+err.Error(), 500)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/messages", bytes.NewReader(body))
		if err != nil {
			return anthropicErr("api_error", "Failed to build upstream request: "+err.Error(), 500)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")

		client := &http.Client{Timeout: 120 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			zapMeterAnthropic(ctx, provider, authUser, request.Model, isPremium, request.Stream, requestId, 0, 0, requestStartTime, hold, "error", err.Error())
			return anthropicErr("api_error", "Upstream request failed: "+err.Error(), 502)
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return anthropicErr(anthropicErrorTypeForStatus(resp.StatusCode), upstreamErrorMessage(respBody), resp.StatusCode)
		}
		var usage struct {
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		_ = json.Unmarshal(respBody, &usage)
		zapMeterAnthropic(ctx, provider, authUser, request.Model, isPremium, request.Stream, requestId,
			usage.Usage.InputTokens, usage.Usage.OutputTokens, requestStartTime, hold, "success", "")
		return resp.StatusCode, respBody, ""
	}

	// Non-native (DO-AI / OpenAI-compat / Local): full Anthropic→OpenAI translation,
	// non-streaming upstream, OpenAI→Anthropic response translation.
	oaiReq := &openai.ChatCompletionRequest{
		Model:      provider.SubType,
		Messages:   anthropicToOpenAIMessages(request),
		Tools:      anthropicToolsToOpenAI(request.Tools),
		ToolChoice: anthropicToolChoiceToOpenAI(request.ToolChoice),
		MaxTokens:  request.MaxTokens,
		Stream:     false,
	}
	if request.Temperature > 0 {
		oaiReq.Temperature = request.Temperature
	}
	vocab := thinkingVocabularyForUpstream(provider.SubType)
	if re := anthropicThinkingToReasoningEffort(request.Thinking, vocab); re != "" {
		oaiReq.ReasoningEffort = re
	}

	upstreamURL, apiKey, authHeader := resolveUpstreamEndpoint(provider)
	if upstreamURL == "" {
		return anthropicErr("api_error", "No upstream endpoint configured for provider: "+provider.Name, 500)
	}
	body, err := json.Marshal(oaiReq)
	if err != nil {
		return anthropicErr("api_error", "Failed to marshal request: "+err.Error(), 500)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		return anthropicErr("api_error", "Failed to build upstream request: "+err.Error(), 500)
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
		zapMeterAnthropic(ctx, provider, authUser, request.Model, isPremium, request.Stream, requestId, 0, 0, requestStartTime, hold, "error", err.Error())
		return anthropicErr("api_error", "Upstream request failed: "+err.Error(), 502)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return anthropicErr("api_error", "Failed to read upstream response: "+err.Error(), 502)
	}
	if resp.StatusCode != http.StatusOK {
		return anthropicErr(anthropicErrorTypeForStatus(resp.StatusCode), upstreamErrorMessage(respBody), resp.StatusCode)
	}
	antResp, prompt, completion := openAIResponseToAnthropic(respBody, request.Model, requestId)
	out, err := json.Marshal(antResp)
	if err != nil {
		return anthropicErr("api_error", err.Error(), 500)
	}
	zapMeterAnthropic(ctx, provider, authUser, request.Model, isPremium, request.Stream, requestId, prompt, completion, requestStartTime, hold, "success", "")
	return 200, out, ""
}

// zapMeterAnthropic is the ONE meter for the ZAP Anthropic tool paths: it records
// usage + trace and, on success, settles the budget hold with the actual token
// cost. Extends the shared usageRecord shape (byo/fee via providerBYO) — never a
// second meter.
func zapMeterAnthropic(
	ctx context.Context, provider *object.Provider, authUser *iam.User, modelName string,
	isPremium, stream bool, requestId string, prompt, completion int,
	requestStartTime time.Time, hold *budgetHold, status, errMsg string,
) {
	if authUser != nil {
		rec := &usageRecord{
			Owner:            authUser.Owner,
			User:             authUser.Owner + "/" + authUser.Name,
			Organization:     authUser.Owner,
			Model:            modelName,
			Provider:         provider.Name,
			PromptTokens:     prompt,
			CompletionTokens: completion,
			TotalTokens:      prompt + completion,
			Currency:         "USD",
			Premium:          isPremium,
			Stream:           stream,
			Status:           status,
			ErrorMsg:         errMsg,
			RequestID:        requestId,
		}
		rec.stampPayer(authUser)
		rec.BYO, rec.Account = providerBYO(provider, authUser)
		recordUsage(rec)
		recordTrace(ctx, rec, requestStartTime)
	}
	if status == "success" {
		hold.settle(calculateCostCentsWithCache(modelName, prompt, completion, 0, 0))
	}
}

// ── POST /v1/messages/count_tokens ───────────────────────────────────────────

func zapAnthropicCountTokens(ctx context.Context, auth string, reqBody []byte) (int, []byte, string) {
	_ = ctx
	token := strings.TrimPrefix(auth, "Bearer ")
	if token == "" {
		return anthropicErr("authentication_error", "Missing API key. Provide x-api-key header or Authorization: Bearer header.", 401)
	}
	if isPublishableKey(token) {
		return anthropicErr("auth_error", "Publishable keys (pk-) can only access read-only endpoints. Use a secret key (sk-) for messages.", 403)
	}

	var request AnthropicRequest
	parseErr := json.Unmarshal(reqBody, &request)
	// Authenticate before reporting a body error (parity with the HTTP path).
	if _, authErr := zapResolveUser(auth); authErr != nil {
		return anthropicErr("authentication_error", authErr.Error(), 401)
	}
	if parseErr != nil {
		return anthropicErr("invalid_request_error", "Failed to parse request: "+parseErr.Error(), 400)
	}
	if request.Model == "" {
		return anthropicErr("invalid_request_error", "model is required", 400)
	}

	msgs := anthropicToOpenAIMessages(&request)
	n, err := model.OpenaiNumTokensFromMessages(msgs, request.Model)
	if err != nil || n <= 0 {
		// Coarse character-based fallback when the tokenizer can't handle the model.
		chars := 0
		for _, m := range msgs {
			chars += len(m.Content)
		}
		n = chars / 4
	}
	// Tool schemas contribute input tokens too.
	if len(request.Tools) > 0 {
		if tb, e := json.Marshal(anthropicToolsToOpenAI(request.Tools)); e == nil {
			if tn, e2 := model.GetTokenSize(request.Model, string(tb)); e2 == nil {
				n += tn
			}
		}
	}

	out, _ := json.Marshal(map[string]interface{}{"input_tokens": n})
	return 200, out, ""
}
