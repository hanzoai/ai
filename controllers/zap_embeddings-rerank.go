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

// Native ZAP handlers for the embeddings/rerank route-group — the pure-ZAP
// twin of the ApiController.Embeddings / .Rerank methods
// (embeddings_api.go). These re-implement the SAME auth → provider-resolution →
// balance-gate → upstream → meter pipeline against object/ + model/ directly,
// with no http.ResponseWriter and no controller. POST /v1/embeddings and
// POST /v1/rerank stay live on routers.App, which also backs the gateway fallback.
//
// Registration convention (recipe): this group self-registers from its own
// file via init() → registerCloud / registerGatewayPath. The registry
// primitives (the two maps + register/lookup funcs) are the ONE shared home,
// zap_registry.go — this group reuses them, never redefines them.

package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/log"
	"github.com/luxfi/zap"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/upstream"
)

// ── Group self-registration ───────────────────────────────────────────────

func init() {
	registerCloud("embeddings", zapEmbeddingsHandler)
	registerCloud("rerank", zapRerankHandler)
	registerGatewayPath("/v1/embeddings", zapEmbeddingsHandler)
	registerGatewayPath("/v1/rerank", zapRerankHandler)
}

// ── embeddings ────────────────────────────────────────────────────────────

// zapEmbeddingsHandler is the native twin of ApiController.Embeddings. It
// authenticates the caller, resolves the model to its upstream provider via the
// shared routing table, enforces the ONE prepaid-balance gate, rewrites the
// user-facing model to the upstream id, proxies to the provider's /embeddings
// endpoint verbatim, and meters exactly once.
func zapEmbeddingsHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	if auth == "" {
		return object.BuildCloudResponse(401, nil, "authentication required")
	}

	var head struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &head); err != nil {
		return object.BuildCloudResponse(400, nil, "invalid request: "+err.Error())
	}
	if head.Model == "" {
		return object.BuildCloudResponse(400, nil, "embeddings request requires a \"model\" field")
	}

	provider, authUser, upstreamModel, err := zapResolveAuth(auth, head.Model)
	if err != nil {
		return object.BuildCloudResponse(401, nil, err.Error())
	}

	// The ONE prepaid-balance gate, shared verbatim with the HTTP path.
	if gateErr := enforceBalanceGate(authUser, "", head.Model); gateErr != nil {
		return object.BuildCloudResponse(uint32(statusOf(gateErr)), nil, gateErr.Error())
	}
	isPremium := false
	if route := resolveModelRoute(head.Model); route != nil {
		isPremium = route.premium
	}

	if err := object.ResolveProviderSecret(provider); err != nil {
		log.Error("ZAP: KMS resolve %s: %v", provider.Name, err)
	}
	if upstreamModel != "" {
		provider.SubType = upstreamModel
	} else {
		provider.SubType = head.Model
	}

	// Translate the user-facing model to the upstream id, preserving every other
	// field (input, encoding_format, dimensions, user, …) for true passthrough.
	outBody, err := setJSONModel(body, provider.SubType)
	if err != nil {
		return object.BuildCloudResponse(500, nil, "failed to rewrite request: "+err.Error())
	}

	startTime := time.Now().UTC()
	return zapProxyJSON(ctx, provider, "embeddings", outBody, head.Model, authUser, isPremium, startTime)
}

// ── rerank ────────────────────────────────────────────────────────────────

// zapRerankHandler is the native twin of ApiController.Rerank. Native rerank
// providers (Jina/Cohere/Voyage) are proxied straight through; every other
// provider gets a real bi-encoder ranking computed from the SAME embeddings
// path (cosine over the resolved embedding model) — no rerank-specific key.
func zapRerankHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	if auth == "" {
		return object.BuildCloudResponse(401, nil, "authentication required")
	}

	var raw struct {
		Model           string            `json:"model"`
		Query           string            `json:"query"`
		Documents       []json.RawMessage `json:"documents"`
		TopN            *int              `json:"top_n"`
		ReturnDocuments *bool             `json:"return_documents"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return object.BuildCloudResponse(400, nil, "invalid request: "+err.Error())
	}
	if raw.Model == "" {
		return object.BuildCloudResponse(400, nil, "rerank request requires a \"model\" field")
	}
	if raw.Query == "" {
		return object.BuildCloudResponse(400, nil, "rerank request requires a \"query\" field")
	}
	if len(raw.Documents) == 0 {
		return object.BuildCloudResponse(400, nil, "rerank request requires a non-empty \"documents\" array")
	}

	docs := make([]string, len(raw.Documents))
	for i, d := range raw.Documents {
		docs[i] = coerceDocText(d)
	}

	provider, authUser, upstreamModel, err := zapResolveAuth(auth, raw.Model)
	if err != nil {
		return object.BuildCloudResponse(401, nil, err.Error())
	}

	if gateErr := enforceBalanceGate(authUser, "", raw.Model); gateErr != nil {
		return object.BuildCloudResponse(uint32(statusOf(gateErr)), nil, gateErr.Error())
	}
	isPremium := false
	if route := resolveModelRoute(raw.Model); route != nil {
		isPremium = route.premium
	}

	if err := object.ResolveProviderSecret(provider); err != nil {
		log.Error("ZAP: KMS resolve %s: %v", provider.Name, err)
	}
	if upstreamModel != "" {
		provider.SubType = upstreamModel
	} else {
		provider.SubType = raw.Model
	}

	startTime := time.Now().UTC()

	// Native rerank provider → proxy the request straight through.
	if isNativeRerankProvider(provider.Type) {
		outBody, rerr := setJSONModel(body, provider.SubType)
		if rerr != nil {
			return object.BuildCloudResponse(500, nil, "failed to rewrite request: "+rerr.Error())
		}
		return zapProxyJSON(ctx, provider, "rerank", outBody, raw.Model, authUser, isPremium, startTime)
	}

	// Bi-encoder rerank via the embeddings path: one query + N documents in a
	// single embeddings call, ranked by cosine similarity.
	texts := make([]string, 0, len(docs)+1)
	texts = append(texts, raw.Query)
	texts = append(texts, docs...)

	vecs, err := embedTexts(provider, provider.SubType, texts)
	if err != nil {
		return object.BuildCloudResponse(502, nil, "rerank embedding failed: "+err.Error())
	}
	if len(vecs) != len(texts) {
		return object.BuildCloudResponse(502, nil, fmt.Sprintf("rerank embedding returned %d vectors for %d inputs", len(vecs), len(texts)))
	}

	results := rankByCosine(vecs[0], vecs[1:])
	topN := len(results)
	if raw.TopN != nil && *raw.TopN >= 0 && *raw.TopN < topN {
		topN = *raw.TopN
	}
	returnDocs := raw.ReturnDocuments != nil && *raw.ReturnDocuments

	out := make([]map[string]interface{}, 0, topN)
	for _, r := range results[:topN] {
		entry := map[string]interface{}{
			"index":           r.Index,
			"relevance_score": r.Score,
		}
		if returnDocs {
			entry["document"] = map[string]string{"text": docs[r.Index]}
		}
		out = append(out, entry)
	}

	// Meter once on the terminal path — the ONE usageRecord shape.
	if authUser != nil {
		rec := &usageRecord{
			Owner:        authUser.Owner,
			Organization: authUser.Owner,
			Model:        raw.Model,
			Provider:     provider.Name,
			Origin:       provider.Origin(),
			Currency:     "USD",
			Premium:      isPremium,
			Status:       "success",
			RequestID:    uuid.NewString(),
		}
		rec.bind(ctx, authUser)
		// One goroutine for both, in this order. They share the record — recordUsage
		// stamps the honesty flag the span then reads — so they are sequenced rather
		// than raced.
		go func() {
			recordUsage(rec)
			recordTrace(ctx, rec, startTime)
		}()
	}

	data, _ := json.Marshal(map[string]interface{}{
		"object":  "list",
		"model":   raw.Model,
		"results": out,
		"usage":   map[string]int{"total_tokens": 0},
	})
	return object.BuildCloudResponse(200, data, "")
}

// ── shared proxy (pure ZAP twin of ApiController.proxyJSON) ────────────────

// zapProxyJSON forwards a pre-marshalled JSON body to the provider's apiPath
// endpoint (e.g. "embeddings", "rerank"), returns the upstream response
// verbatim as a cloud response, and meters the call exactly once (success or
// error). It is the no-http-writer twin of ApiController.proxyJSON.
func zapProxyJSON(ctx context.Context, provider *object.Provider, apiPath string, body []byte, userModel string, authUser *iam.User, isPremium bool, startTime time.Time) (*zap.Message, error) {
	requestId := uuid.NewString()

	upstreamURL := upstream.Endpoint(provider, apiPath)
	if upstreamURL == "" {
		return object.BuildCloudResponse(502, nil, "no upstream endpoint configured for provider: "+provider.Name)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		return object.BuildCloudResponse(500, nil, "failed to create upstream request: "+err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	upstream.Authorize(req, provider)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		if authUser != nil {
			errRec := &usageRecord{
				Owner:        authUser.Owner,
				Organization: authUser.Owner,
				Model:        userModel,
				Provider:     provider.Name,
				Origin:       provider.Origin(),
				Premium:      isPremium,
				Status:       "error",
				ErrorMsg:     err.Error(),
				RequestID:    requestId,
			}
			errRec.bind(context.Background(), authUser)
			go func() {
				recordUsage(errRec)
				recordTrace(context.Background(), errRec, startTime)
			}()
		}
		return object.BuildCloudResponse(502, nil, "upstream request failed: "+err.Error())
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return object.BuildCloudResponse(502, nil, "failed to read upstream response: "+err.Error())
	}

	// Extract usage (prompt/total tokens) for billing when present.
	var upstreamResp struct {
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(respBody, &upstreamResp)

	if authUser != nil {
		status := "success"
		if resp.StatusCode >= 400 {
			status = "error"
		}
		rec := &usageRecord{
			Owner:        authUser.Owner,
			Organization: authUser.Owner,
			Model:        userModel,
			Provider:     provider.Name,
			Origin:       provider.Origin(),
			PromptTokens: upstreamResp.Usage.PromptTokens,
			TotalTokens:  upstreamResp.Usage.TotalTokens,
			Currency:     "USD",
			Premium:      isPremium,
			Status:       status,
			RequestID:    requestId,
		}
		rec.bind(ctx, authUser)
		go func() {
			recordUsage(rec)
			recordTrace(ctx, rec, startTime)
		}()
	}

	// The HTTP twin's rule, on the twin surface: the request went upstream with its
	// model rewritten, so the answer names the model the caller asked for.
	if out, err := setJSONModel(respBody, userModel); err == nil {
		respBody = out
	}
	return object.BuildCloudResponse(uint32(resp.StatusCode), respBody, "")
}
