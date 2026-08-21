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
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hanzoai/account"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/log"
	openai "github.com/hanzoai/go-openai"
	"github.com/luxfi/zap"

	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

// InitZapHandlers registers native ZAP service handlers on the node. router is
// the fully-wrapped native router (routers.App) that serves every MsgType 200
// path the fast-path switch and the registry do not claim.
//
// The router is an ARGUMENT, not a later setter call, because the two are not
// separable: a gateway handler registered without a router answers 404 to the
// entire RESTful surface — silently, with the handler present and the node
// healthy. As a parameter the broken pairing is not expressible; the only way to
// reach that mode is to write nil, in one visible place.
func InitZapHandlers(router http.Handler) {
	node := object.GetZapNode()
	if node == nil {
		return
	}

	node.Handle(object.MsgTypeCloud, handleCloudService)
	node.Handle(object.MsgTypeHTTPRequest, gateway(target(router)))
	log.Info("ZAP: registered handlers (cloud=%d, gateway=%d)", object.MsgTypeCloud, object.MsgTypeHTTPRequest)
}

func handleCloudService(ctx context.Context, from string, msg *zap.Message) (resp *zap.Message, err error) {
	// controller parity: a handler panic (e.g. util.ParseInt on garbage input) must
	// surface as a 500 response, never escape the dispatch seam.
	defer func() {
		if r := recover(); r != nil {
			log.Error("zap cloud handler panic: %v", r)
			resp, err = object.BuildCloudResponse(500, nil, fmt.Sprintf("internal error: %v", r))
		}
	}()
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
	}
	// Migrated route-groups self-register their native-cloud methods into the
	// dispatch registry (zap_registry.go). Un-migrated methods are unknown here —
	// the caller reaches the full controller route table over the forward bridge / HTTP.
	if h, ok := lookupCloudHandler(method); ok {
		return h(ctx, auth, body)
	}
	return object.BuildCloudResponse(404, nil, "unknown method: "+method)
}

// ── Gateway HTTP-over-ZAP (MsgType 200) ─────────────────────────────────
//
// The gateway forwards HTTP requests as ZAP messages. We dispatch by path
// to the same handlers used by native cloud service, then return a gateway
// response (status + body + headers).

// gateway builds the MsgType 200 handler bound to the router that serves what the
// fast paths and the registry do not claim. Constructing it WITH the router —
// rather than registering it and installing the router from another line, in
// another file — is what makes the 404-to-everything mode unreachable by omission.
func gateway(router http.Handler) zap.Handler {
	return func(ctx context.Context, from string, msg *zap.Message) (resp *zap.Message, err error) {
		// controller parity: a handler panic must surface as a 500 response, never
		// escape the dispatch seam.
		defer func() {
			if r := recover(); r != nil {
				log.Error("zap gateway handler panic: %v", r)
				errBody, _ := json.Marshal(map[string]string{"error": fmt.Sprintf("internal error: %v", r)})
				resp, err = object.BuildGatewayResponse(500, errBody, nil)
			}
		}()
		root := msg.Root()
		method := root.Text(0)
		path := root.Text(8)
		query := root.Text(32)
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
		}
		// Migrated route-groups self-register their gateway path prefixes into the
		// dispatch registry (zap_registry.go). A miss is not a hole: the same request
		// served over the forward bridge (MsgTypeForward) reaches the full controller route
		// table, so un-migrated routes still serve (strangler fallback).
		if msg, handled, err := dispatchGateway(ctx, router, method, path, query, auth, body); handled {
			return msg, err
		}
		errBody, _ := json.Marshal(map[string]string{"error": "not found: " + path})
		return object.BuildGatewayResponse(404, errBody, nil)
	}
}

// dispatchGateway routes an HTTP-over-ZAP request to a migrated group's native
// handler, reporting whether any group claimed the path. It walks one ordered
// chain of candidates: the HTTP-shaped routes (method/path/query aware) longest
// prefix first, then the body-only routes, then the native router. A candidate
// that returns errDecline says the path is inside its prefix but not one it
// serves, so the walk continues — that is what keeps a broad prefix like
// "/v1/models/" from swallowing a sibling ("/v1/models/providers") that another
// registration owns. handled == false means nothing claimed the path and no
// router was supplied.
//
// router is the last candidate in that chain, and it is whatever the caller can
// honestly fall back to: the transport handler passes routers.App; a caller that
// is ALREADY inside routers.App passes nil, because falling back there would
// re-enter itself.
func dispatchGateway(ctx context.Context, router http.Handler, method, path, query, auth string, body []byte) (*zap.Message, bool, error) {
	for _, h := range lookupGatewayRoutes(path) {
		msg, err := h(ctx, method, path, query, auth, body)
		if errors.Is(err, errDecline) {
			continue
		}
		return msg, true, err
	}
	if h, ok := lookupGatewayHandler(path); ok {
		msg, err := h(ctx, auth, body)
		return msg, true, err
	}
	// No registry entry owns this path — serve it through the native router,
	// which resolves method + path parameters and runs every filter. See
	// zap_gateway_fallback.go for why a second router is not taught to do that.
	return serveGatewayViaRouter(ctx, router, method, path, query, auth, body)
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
			log.Warn("ZAP: ensure cloud_usage table: %v", err)
		}
		cancel()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := object.DatastoreExec(ctx, object.CloudUsageInsert, cloudUsageValues(record, startTime)...); err != nil {
		log.Warn("ZAP: usage write failed: %v", err)
	}
}

// cloudUsageValues renders a record as one warehouse row, in
// object.CloudUsageColumns order. Grouped to match that list line for line so the
// alignment can be checked by eye; the count is pinned by test, which is what
// catches a column added in the middle — the change that shifts every value after
// it into its neighbour's field while still compiling and still writing rows.
//
// It derives the money itself rather than reading recordUsage's stamps: that path
// early-returns when the billing queue is off, and this warehouse write is
// independent of it. Same inputs, same functions, same answers.
func cloudUsageValues(record *usageRecord, startTime time.Time) []any {
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
	// An unpriced turn has no computable margin (usageMargin leaves it nil), and the
	// column is Int64. Write 0, which contributes nothing to a margin sum, rather
	// than the fabricated loss the invented COGS would produce — the `unpriced` flag
	// on this same row is what says the money here is a guess.
	marginNano := int64(0)
	if m.MarginNano != nil {
		marginNano = *m.MarginNano
	}
	// cost_nano is Int64, so a cost nobody knows writes 0 and says so in the column
	// beside it. Without that flag the row is indistinguishable from a call that
	// genuinely cost nothing, and a SUM over the column quietly under-reports what the
	// business spent — which is the whole question the column exists to answer.
	costNano := int64(0)
	uncosted := uint8(1)
	if m.CostNano != nil {
		costNano, uncosted = *m.CostNano, 0
	}
	// Honest "priced?" flag — self-contained (this writer runs independent of
	// recordUsage's stamp): 1 when the model billed at the conservative default.
	unpriced := uint8(0)
	if recordUnpriced(record) {
		unpriced = 1
	}

	return []any{
		record.RequestID, startTime.UTC(),
		record.Owner, record.User, org, record.Project,
		record.Model, record.Requested, record.Provider, record.Origin, record.Agent, record.APIKeyHash,
		// The conversation this call belongs to. The span has always carried it; the
		// ledger has not, so "which session spent the money" was a join against a
		// store that holds no money.
		record.Session, record.TraceID,
		record.RequestID,
		record.PromptTokens, record.CompletionTokens, record.TotalTokens,
		record.CacheReadTokens, record.CacheWriteTokens,
		costCents, "usd",
		record.Status, record.ErrorMsg,
		premium, stream, record.ClientIP,
		byo, feeCents, record.Account,
		costNano, m.BilledNano, marginNano, unpriced, uncosted,
	}
}

// ── models.list ─────────────────────────────────────────────────────────

func zapListModelsHandler() (*zap.Message, error) {
	data, err := modelListing(nil)
	if err != nil {
		return nil, err
	}
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
	// = account.Payer(account.Credential{Owner: owner, Name: name}).Subject() (per-user for a personal-billing org).
	namespace := userId
	name := ""
	if i := strings.IndexByte(userId, '/'); i >= 0 {
		namespace = userId[:i]
		name = userId[i+1:]
	}
	subject := account.Payer(account.Credential{Owner: namespace, Name: name}).Subject()

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

	// Prepaid-balance gate — the ONE gate, shared verbatim with the HTTP path
	// (enforceBalanceGate): a strictly positive balance is required for ANY model,
	// premium or not. $0 → 402; an unverifiable balance → 500 (fail-closed). This
	// closes the old ZAP-only hole where non-premium models ran ungated at $0.
	if gateErr := enforceBalanceGate(authUser, "", request.Model); gateErr != nil {
		return object.BuildCloudResponse(uint32(statusOf(gateErr)), nil, gateErr.Error())
	}
	isPremium := false
	if route := resolveModelRoute(request.Model); route != nil {
		isPremium = route.premium
	}

	// KMS secrets.
	if err := object.ResolveProviderSecret(provider); err != nil {
		log.Error("ZAP: KMS resolve %s: %v", provider.Name, err)
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
	requestId := uuid.NewString()
	var buf bytes.Buffer

	modelResult, err := modelProvider.QueryText(question, &buf, history, "", nil, nil, "en")
	if err != nil {
		if authUser != nil {
			errRec := &usageRecord{
				Owner:        authUser.Owner,
				Organization: authUser.Owner,
				Model:        request.Model,
				Provider:     provider.Name,
				Origin:       provider.Origin(),
				Premium:      isPremium,
				Stream:       false,
				Status:       "error",
				ErrorMsg:     err.Error(),
				RequestID:    requestId,
			}
			errRec.bind(context.Background(), authUser)
			// One goroutine for both, in this order, exactly as the success path
			// below. They share the record — recordUsage stamps the honesty flag
			// the span then reads — so they are sequenced rather than raced.
			go func() {
				recordUsage(errRec)
				recordTrace(context.Background(), errRec, requestStartTime)
			}()
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
				Organization:     authUser.Owner,
				Model:            request.Model,
				Provider:         provider.Name,
				Origin:           provider.Origin(),
				PromptTokens:     modelResult.PromptTokenCount,
				CacheReadTokens:  modelResult.CacheReadTokenCount,
				CacheWriteTokens: modelResult.CacheWriteTokenCount,
				CompletionTokens: modelResult.ResponseTokenCount,
				TotalTokens:      modelResult.TotalTokenCount,
				Currency:         "USD",
				Premium:          isPremium,
				Stream:           false,
				Status:           "success",
				RequestID:        requestId,
			}
			record.bind(ctx, authUser)
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

	if isJwtToken(token) {
		return resolveProviderFromJwt(token, "", requestModel, "en")
	}

	// A secret key: the STORE that owns it decides what it is, exactly as
	// ApiController.authResolveProvider does it. A vendor key lives in the
	// provider table; anything else answers to IAM, whose refusal names the cure.
	provider, err := object.GetProviderByProviderKey(token, "en")
	if err != nil {
		return nil, nil, "", fmt.Errorf("invalid auth token")
	}
	if provider == nil {
		return resolveProviderFromIAMKey(token, requestModel, "en")
	}

	upstreamModel := ""
	if route := resolveModelRoute(requestModel); route != nil {
		upstreamModel = route.upstreamModel
	}
	return provider, nil, upstreamModel, nil
}
