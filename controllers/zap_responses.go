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

// Native ZAP handler for the OpenAI Responses API (POST /v1/responses).
//
// STRANGLER: this re-implements ApiController.Responses (openai_responses.go)
// as a pure ZAP handler against object/ + model/ + iam, mirroring
// zapChatHandler (zap_native.go) exactly — identity, org scoping, the ONE
// prepaid-balance gate, per-org provider selection, KMS secret resolution, and
// a single terminal meter. It NEVER wraps or drives the beego controller and
// never holds an http.ResponseWriter. The beego route in router.go stays live
// in parallel until Integrate flips the gateway/native dispatch to consult the
// registry below.
//
// REGISTRATION: this file declares NO shared registry symbol (so it compiles
// standalone in the strangler hybrid and never collides with a sibling group).
// It exposes its native-method and gateway-path tables as uniquely-named data
// (zapResponsesCloud / zapResponsesGateway) keyed to the ONE handler. Integrate
// ranges over these to wire registerCloud / registerGatewayPath into
// handleCloudService / handleGatewayHTTPRequest (falling back to the existing
// chat/models/balance cases) when it flips native dispatch on. No group edits a
// shared registration file.

package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hanzoai/ai/log"
	openai "github.com/hanzoai/go-openai"
	"github.com/luxfi/zap"

	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

// ── ZAP dispatch tables (group-local, collision-free) ───────────────────
//
// A type ALIAS (not a new named type) so no shared symbol is declared. Integrate
// ranges over the two tables below to registerCloud(method, h) /
// registerGatewayPath(path, h).

type zapResponsesHandlerFn = func(ctx context.Context, auth string, body []byte) (*zap.Message, error)

// init self-registers this group's dispatch tables into the canonical registry.
func init() {
	for method, h := range zapResponsesCloud {
		registerCloud(method, h)
	}
	for path, h := range zapResponsesGateway {
		registerGatewayPath(path, h)
	}
}

// zapResponsesCloud maps native cloud method → handler (MsgType 100).
var zapResponsesCloud = map[string]zapResponsesHandlerFn{
	"responses.create": zapResponsesHandler,
}

// zapResponsesGateway maps gateway path → handler (MsgType 200).
var zapResponsesGateway = map[string]zapResponsesHandlerFn{
	"/v1/responses": zapResponsesHandler,
}

// ── responses.create / POST /v1/responses ───────────────────────────────

func zapResponsesHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	if auth == "" {
		return object.BuildCloudResponse(401, nil, "authentication required")
	}
	token := trimBearer(auth)
	if isPublishableKey(token) {
		return object.BuildCloudResponse(403, nil, "Publishable keys cannot access /v1/responses. Use a secret key.")
	}

	// Codex compresses large Responses bodies with zstd. Decode env-agnostically
	// via the pure-Go decoder (Cloud binaries build with CGO_ENABLED=0).
	if responsesBodyIsZstd(body) {
		decoded, err := decodeResponsesZstd(body)
		if err != nil {
			return object.BuildCloudResponse(400, nil, "Failed to decompress zstd request: "+err.Error())
		}
		body = decoded
	}

	var request OpenAIResponsesRequest
	if err := json.Unmarshal(body, &request); err != nil {
		return object.BuildCloudResponse(400, nil, "Failed to parse Responses request: "+err.Error())
	}
	if request.Model == "" {
		return object.BuildCloudResponse(400, nil, "model is required")
	}

	// SAME conversion the HTTP path uses — one wire-shape translation.
	chatRequest, toolKinds, err := responsesToChatRequest(&request)
	if err != nil {
		return object.BuildCloudResponse(400, nil, err.Error())
	}

	// STEP 1/2 — identity + org scoping + per-org provider + upstream model.
	// The ONE auth seam (routes pk-/hk- IAM keys, JWTs, and sk- direct keys).
	provider, authUser, upstreamModel, err := zapResolveAuth(auth, chatRequest.Model)
	if err != nil {
		return object.BuildCloudResponse(401, nil, err.Error())
	}

	// STEP 4 — the ONE prepaid-balance gate, shared verbatim with the HTTP path.
	if gateErr := enforceBalanceGate(authUser, "", chatRequest.Model); gateErr != nil {
		return object.BuildCloudResponse(uint32(statusOf(gateErr)), nil, gateErr.Error())
	}
	isPremium := false
	if route := resolveModelRoute(chatRequest.Model); route != nil {
		isPremium = route.premium
	}

	// KMS secrets (env-first).
	if err := object.ResolveProviderSecret(provider); err != nil {
		log.Error("ZAP: KMS resolve %s: %v", provider.Name, err)
	}
	if upstreamModel != "" {
		provider.SubType = upstreamModel
	} else if chatRequest.Model != "" {
		provider.SubType = chatRequest.Model
	}

	modelProvider, err := provider.GetModelProvider("en")
	if err != nil {
		return object.BuildCloudResponse(502, nil, "provider init failed: "+err.Error())
	}

	// Flatten the converted chat messages into question + history, mirroring
	// zapChatHandler exactly (QueryText is text-only; tool/tool-output turns are
	// carried the same way the native chat handler carries them).
	question, systemPrompt, history := zapFlattenMessages(chatRequest.Messages)
	if question == "" {
		return object.BuildCloudResponse(400, nil, "input must contain at least one message")
	}
	if systemPrompt != "" {
		question = fmt.Sprintf("System: %s\n\nUser: %s", systemPrompt, question)
	}

	// STEP 5 — execute against the object/model layer, buffered (no HTTP writer).
	requestStartTime := time.Now().UTC()
	requestId := uuid.NewString()
	var buf bytes.Buffer

	modelResult, err := modelProvider.QueryText(question, &buf, history, "", nil, nil, "en")
	if err != nil {
		if authUser != nil {
			errRec := &usageRecord{
				Owner:        authUser.Owner,
				User:         authUser.Owner + "/" + authUser.Name,
				Organization: authUser.Owner,
				Model:        chatRequest.Model,
				Provider:     provider.Name,
				Premium:      isPremium,
				Stream:       request.Stream,
				Status:       "error",
				ErrorMsg:     err.Error(),
				RequestID:    requestId,
			}
			go recordUsage(errRec)
			recordTrace(context.Background(), errRec, requestStartTime)
		}
		return object.BuildCloudResponse(502, nil, "provider error: "+err.Error())
	}

	// STEP 6 — meter ONCE on the terminal path (the single meter shape).
	if authUser != nil {
		go func() {
			record := &usageRecord{
				Owner:            authUser.Owner,
				User:             authUser.Owner + "/" + authUser.Name,
				Organization:     authUser.Owner,
				Model:            chatRequest.Model,
				Provider:         provider.Name,
				PromptTokens:     modelResult.PromptTokenCount,
				CompletionTokens: modelResult.ResponseTokenCount,
				TotalTokens:      modelResult.TotalTokenCount,
				Currency:         "USD",
				Premium:          isPremium,
				Stream:           request.Stream,
				Status:           "success",
				RequestID:        requestId,
			}
			recordUsage(record)
			recordTrace(ctx, record, requestStartTime)
		}()
	}

	// STEP 7 — encode the SAME Responses wire shape the HTTP path returns.
	// Build the intermediate Chat Completions JSON first, then translate it via
	// the shared openai_responses.go translator (one translation, not a fork).
	chatResp := openai.ChatCompletionResponse{
		ID:      "chatcmpl-" + requestId,
		Object:  "chat.completion",
		Created: util.GetCurrentUnixTime(),
		Model:   chatRequest.Model,
		Choices: []openai.ChatCompletionChoice{
			{
				Index:        0,
				Message:      openai.ChatCompletionMessage{Role: "assistant", Content: buf.String()},
				FinishReason: openai.FinishReasonStop,
			},
		},
		Usage: openai.Usage{
			PromptTokens:     modelResult.PromptTokenCount,
			CompletionTokens: modelResult.ResponseTokenCount,
			TotalTokens:      modelResult.TotalTokenCount,
		},
	}
	chatBody, _ := json.Marshal(chatResp)

	if request.Stream {
		sse, err := responsesStreamBody(chatBody, &request, toolKinds)
		if err != nil {
			return object.BuildCloudResponse(502, nil, err.Error())
		}
		return object.BuildCloudResponse(200, sse, "")
	}

	out, err := openAIChatResponseToResponses(chatBody, &request, toolKinds)
	if err != nil {
		return object.BuildCloudResponse(502, nil, err.Error())
	}
	return object.BuildCloudResponse(200, out, "")
}

// ── helpers ──────────────────────────────────────────────────────────────

func trimBearer(auth string) string {
	const prefix = "Bearer "
	if len(auth) >= len(prefix) && auth[:len(prefix)] == prefix {
		return auth[len(prefix):]
	}
	return auth
}

// responsesBodyIsZstd reports whether body starts with the zstd magic number
// (0xFD2FB528, little-endian). The native ZAP path has no Content-Encoding
// header, so we sniff the frame the way the HTTP path trusts the header.
func responsesBodyIsZstd(body []byte) bool {
	return len(body) >= 4 && body[0] == 0x28 && body[1] == 0xB5 && body[2] == 0x2F && body[3] == 0xFD
}

// zapFlattenMessages collapses chat messages into (question, systemPrompt,
// history) exactly as zapChatHandler does: the last user turn is the question,
// assistant turns become AI history, the system turn is prepended.
func zapFlattenMessages(messages []openai.ChatCompletionMessage) (question, systemPrompt string, history []*model.RawMessage) {
	history = []*model.RawMessage{}
	for _, msg := range messages {
		switch msg.Role {
		case "system":
			systemPrompt = msg.Content
		case "user":
			question = msg.Content
		case "assistant":
			history = append(history, &model.RawMessage{Author: "AI", Text: msg.Content})
		}
	}
	return question, systemPrompt, history
}

// responsesStreamBody renders the complete Responses SSE event sequence for a
// buffered (non-streaming-upstream) completion. QueryText yields the full text
// + token counts in one shot, so we drive the SAME responsesStreamTranslator
// the HTTP bridge uses — created → output_item/content_part → output_text
// deltas → done → completed — into a byte buffer instead of a live writer.
func responsesStreamBody(chatBody []byte, request *OpenAIResponsesRequest, toolKinds map[string]string) ([]byte, error) {
	var out bytes.Buffer
	emit := func(event string, data interface{}) error {
		b, err := json.Marshal(data)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(&out, "event: %s\ndata: %s\n\n", event, b)
		return err
	}

	var resp struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string                           `json:"id"`
					Function struct{ Name, Arguments string } `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(chatBody, &resp); err != nil {
		return nil, fmt.Errorf("invalid upstream chat response: %w", err)
	}

	t := newResponsesStreamTranslator(emit, request, toolKinds)
	t.promptTokens = resp.Usage.PromptTokens
	t.completionTokens = resp.Usage.CompletionTokens
	t.totalTokens = resp.Usage.TotalTokens
	if err := t.start(); err != nil {
		return nil, err
	}
	if len(resp.Choices) > 0 {
		msg := resp.Choices[0].Message
		if msg.Content != "" {
			if err := t.handleChunk(streamChunkForContent(request.Model, msg.Content)); err != nil {
				return nil, err
			}
		}
		for i, tc := range msg.ToolCalls {
			if err := t.handleChunk(streamChunkForToolCall(request.Model, i, tc.ID, tc.Function.Name, tc.Function.Arguments)); err != nil {
				return nil, err
			}
		}
	}
	if err := t.finish(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// streamChunkForContent builds a single-delta chunk carrying the full text.
// Marshaling through JSON avoids repeating openaiStreamChunk's anonymous
// nested struct literal.
func streamChunkForContent(modelID, content string) *openaiStreamChunk {
	b, _ := json.Marshal(map[string]interface{}{
		"model":   modelID,
		"choices": []map[string]interface{}{{"delta": map[string]interface{}{"content": content}}},
	})
	var c openaiStreamChunk
	_ = json.Unmarshal(b, &c)
	return &c
}

// streamChunkForToolCall builds a single tool-call delta chunk.
func streamChunkForToolCall(modelID string, index int, id, name, arguments string) *openaiStreamChunk {
	b, _ := json.Marshal(map[string]interface{}{
		"model": modelID,
		"choices": []map[string]interface{}{{"delta": map[string]interface{}{
			"tool_calls": []map[string]interface{}{{
				"index":    index,
				"id":       id,
				"function": map[string]string{"name": name, "arguments": arguments},
			}},
		}}},
	})
	var c openaiStreamChunk
	_ = json.Unmarshal(b, &c)
	return &c
}
