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
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	iam "github.com/hanzoai/ai/internal/iam"

	"github.com/hanzoai/ai/object"
)

// This file completes the OpenAI-compatible surface alongside chat: the
// /v1/embeddings and /v1/rerank endpoints. They deliberately reuse the SAME
// authentication, model-routing, provider-resolution, upstream-endpoint, and
// billing machinery as ChatCompletions (see openai_api.go) — there is exactly
// one auth+routing policy in the gateway and these handlers ride it:
//
//	bearer token → authResolveProvider() → resolveEndpointForPath(provider, path)
//	             → proxy upstream → recordUsage()
//
// Embeddings and the native-provider branch of Rerank are thin JSON proxies.
// When no rerank-specific provider key is configured, Rerank falls back to a
// real bi-encoder ranking computed from the SAME embeddings path (cosine over
// the resolved embedding model) — a genuine ranked response with no extra key.

// Embeddings implements POST /v1/embeddings (OpenAI-compatible).
//
// Body: {"model": "...", "input": "..."|["...", ...], "encoding_format"?, "dimensions"?}
// It authenticates the caller, resolves the model to its upstream provider via
// the shared routing table, rewrites the user-facing model name to the upstream
// id, and proxies the request to the provider's /embeddings endpoint verbatim.
func (c *ApiController) Embeddings() {
	token, ok := c.bearerToken()
	if !ok {
		return
	}
	if isPublishableKey(token) {
		c.rejectPublishableKey()
		return
	}

	var head struct {
		Model string `json:"model"`
	}
	badReq := ""
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, &head); err != nil {
		badReq = fmt.Sprintf("Failed to parse request: %s", err.Error())
	} else if head.Model == "" {
		badReq = "embeddings request requires a \"model\" field"
	}
	if badReq != "" {
		// Authenticate before reporting the client error: an invalid credential is
		// 401 regardless of body validity (never a probe-able 200/400).
		if authErr := c.authenticate(token); authErr != nil {
			c.ResponseAuthError(authErr)
			return
		}
		c.ResponseErrorWithStatus(http.StatusBadRequest, badReq)
		return
	}

	startTime := time.Now().UTC()
	orgId := c.GetOrg()

	provider, authUser, upstreamModel, isPremium, _, err := c.authResolveProvider(token, head.Model, orgId)
	if err != nil {
		c.ResponseAuthError(err)
		return
	}
	if upstreamModel != "" {
		provider.SubType = upstreamModel
	} else if head.Model != "" {
		provider.SubType = head.Model
	}

	// Model families (Zen, Enso): forward to the family service, billed per token at
	// the discovered price (embeddings are token-metered, so this rides the same pipe
	// as chat, not the per-unit media pipe). The family owns the SKU→upstream mapping.
	if fam := familyForProviderType(provider.Type); fam != nil {
		var hold *budgetHold
		if authUser != nil {
			ledger := c.billingOrg(authUser)
			subject := authUser.PayerSubject(ledger)
			est := int64(1)
			if zm, ok := fam.lookup(head.Model); ok {
				est = zm.costCents(coarseTokenEstimate(c.Ctx.Input.RequestBody), 0)
			}
			var ok2 bool
			if hold, ok2 = reserveBudget(subject, est); !ok2 {
				c.ResponseAuthError(billingError("%s", object.InsufficientBalance(c.Ctx.Request.Host, ledger, "cost").Message))
				return
			}
			// The fail-safe every other reserving surface already has: whatever
			// way this request ends, the reservation is released. settle is
			// one-shot, so a real settle downstream still wins and this becomes a
			// no-op. Without it a refusal the pipe answers itself — a 400, or a
			// caller who hung up — leaves the cents held with nothing left
			// running that would ever release them.
			defer hold.settle(0)
		}
		refused := c.pipeToFamily(fam, "embeddings", "openai", head.Model, c.Ctx.Input.RequestBody, false, orgId, authUser, isPremium, hold, startTime)
		if refused == nil {
			return
		}
		// Embeddings have no alternate to move to — the chat pipeline's route
		// table does not describe them — so the honest answer is the refusal
		// itself, naming the vendor and the reason rather than forwarding an
		// upstream 402 that tells the customer THEY are out of money.
		c.recordRefusals(head.Model, refused, authUser, isPremium, false, uuid.NewString(), startTime)
		c.ResponseFailure(exhausted(head.Model, refused))
		return
	}

	// Translate the user-facing model to the upstream id, preserving every other
	// field (input, encoding_format, dimensions, user, …) for true passthrough.
	body, err := setJSONModel(c.Ctx.Input.RequestBody, provider.SubType)
	if err != nil {
		c.ResponseError(fmt.Sprintf("Failed to rewrite request: %s", err.Error()))
		return
	}

	c.proxyJSON(provider, "embeddings", body, head.Model, authUser, isPremium, startTime)
}

// Rerank implements POST /v1/rerank (Cohere/Jina-compatible).
//
// Body: {"model": "...", "query": "...", "documents": ["...", ...]|[{"text":"..."}],
//
//	"top_n"?: int, "return_documents"?: bool}
//
// Response: {"object":"list","model":...,"results":[{"index","relevance_score","document"?}],"usage":{...}}
//
// Backend selection is provider-driven (one endpoint, one contract):
//   - If the model routes to a native rerank provider (Jina/Cohere/Voyage) the
//     request is proxied to that provider's /rerank endpoint.
//   - Otherwise scores are computed as a real bi-encoder ranking: embed the
//     query and documents through the resolved embedding model and rank by
//     cosine similarity. No rerank-specific key required.
func (c *ApiController) Rerank() {
	token, ok := c.bearerToken()
	if !ok {
		return
	}
	if isPublishableKey(token) {
		c.rejectPublishableKey()
		return
	}

	var raw struct {
		Model           string            `json:"model"`
		Query           string            `json:"query"`
		Documents       []json.RawMessage `json:"documents"`
		TopN            *int              `json:"top_n"`
		ReturnDocuments *bool             `json:"return_documents"`
	}
	badReq := ""
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, &raw); err != nil {
		badReq = fmt.Sprintf("Failed to parse request: %s", err.Error())
	} else if raw.Model == "" {
		badReq = "rerank request requires a \"model\" field"
	} else if raw.Query == "" {
		badReq = "rerank request requires a \"query\" field"
	} else if len(raw.Documents) == 0 {
		badReq = "rerank request requires a non-empty \"documents\" array"
	}
	if badReq != "" {
		// Authenticate before reporting the client error: an invalid credential is
		// 401 regardless of body validity (never a probe-able 200/400).
		if authErr := c.authenticate(token); authErr != nil {
			c.ResponseAuthError(authErr)
			return
		}
		c.ResponseErrorWithStatus(http.StatusBadRequest, badReq)
		return
	}

	docs := make([]string, len(raw.Documents))
	for i, d := range raw.Documents {
		docs[i] = coerceDocText(d)
	}

	startTime := time.Now().UTC()
	orgId := c.GetOrg()

	provider, authUser, upstreamModel, isPremium, _, err := c.authResolveProvider(token, raw.Model, orgId)
	if err != nil {
		c.ResponseAuthError(err)
		return
	}
	if upstreamModel != "" {
		provider.SubType = upstreamModel
	} else if raw.Model != "" {
		provider.SubType = raw.Model
	}

	// Zen family: forward to the zen service, billed per call at the discovered price.
	if provider.Type == "Zen" {
		c.serveZenMedia("rerank", raw.Model, c.Ctx.Input.RequestBody, 1, orgId, authUser, isPremium, startTime)
		return
	}

	// Native rerank provider → proxy the request straight through.
	if isNativeRerankProvider(provider.Type) {
		body, rerr := setJSONModel(c.Ctx.Input.RequestBody, provider.SubType)
		if rerr != nil {
			c.ResponseError(fmt.Sprintf("Failed to rewrite request: %s", rerr.Error()))
			return
		}
		c.proxyJSON(provider, "rerank", body, raw.Model, authUser, isPremium, startTime)
		return
	}

	// Bi-encoder rerank via the embeddings path: one query + N documents in a
	// single embeddings call, ranked by cosine similarity.
	texts := make([]string, 0, len(docs)+1)
	texts = append(texts, raw.Query)
	texts = append(texts, docs...)

	vecs, err := embedTexts(provider, provider.SubType, texts)
	if err != nil {
		c.ResponseError(fmt.Sprintf("rerank embedding failed: %s", err.Error()))
		return
	}
	if len(vecs) != len(texts) {
		c.ResponseError(fmt.Sprintf("rerank embedding returned %d vectors for %d inputs", len(vecs), len(texts)))
		return
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

	if authUser != nil {
		rec := &usageRecord{
			Owner:        c.billingOrg(authUser),
			Organization: authUser.Owner,
			Model:        raw.Model,
			Provider:     provider.Name,
			Origin:       provider.Origin(),
			Currency:     "USD",
			Premium:      isPremium,
			Status:       "success",
			ClientIP:     c.Ctx.Request.RemoteAddr,
			RequestID:    uuid.NewString(),
		}
		rec.bind(c.Ctx.Request.Context(), authUser)
		recordUsage(rec)
		recordTrace(c.Ctx.Request.Context(), rec, startTime)
	}

	c.jsonResponse(map[string]interface{}{
		"object":  "list",
		"model":   raw.Model,
		"results": out,
		"usage":   map[string]int{"total_tokens": 0},
	})
}

// ── Shared helpers ────────────────────────────────────────────────────────

// bearerToken extracts the "Bearer <token>" credential or writes the standard
// auth error and returns ok=false.
func (c *ApiController) bearerToken() (string, bool) {
	authHeader := c.Ctx.Request.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		c.ResponseErrorWithStatus(401, c.T("openai:Invalid API key format. Expected 'Bearer API_KEY'"))
		return "", false
	}
	return strings.TrimPrefix(authHeader, "Bearer "), true
}

// rejectPublishableKey writes the 403 that read-only publishable (pk-) keys get
// when they hit a privileged endpoint.
func (c *ApiController) rejectPublishableKey() {
	c.Ctx.Output.SetStatus(403)
	c.Ctx.Output.Header("Content-Type", "application/json")
	c.Ctx.Output.Body([]byte(`{"error":{"message":"Publishable keys (pk-) can only access read-only endpoints (/v1/models, /health). Use a secret key (sk-) for this endpoint.","type":"auth_error","code":403}}`))
	c.EnableRender = false
}

// jsonResponse writes v as a 200 JSON body and disables the router's auto-render.
func (c *ApiController) jsonResponse(v interface{}) {
	b, err := json.Marshal(v)
	if err != nil {
		c.ResponseError(fmt.Sprintf("Failed to encode response: %s", err.Error()))
		return
	}
	c.Ctx.Output.Header("Content-Type", "application/json")
	c.Ctx.ResponseWriter.WriteHeader(http.StatusOK)
	c.Ctx.Output.Body(b)
	c.EnableRender = false
}

// proxyJSON forwards a pre-marshalled JSON body to the provider's apiPath
// endpoint (e.g. "embeddings", "rerank") and returns the upstream response
// verbatim, recording usage for billing. It is the non-streaming twin of
// proxyToolRequest, used by Embeddings and the native branch of Rerank.
func (c *ApiController) proxyJSON(provider *object.Provider, apiPath string, body []byte, userModel string, authUser *iam.User, isPremium bool, startTime time.Time) {
	requestId := uuid.NewString()

	upstreamURL, apiKey, authHeader := resolveEndpointForPath(provider, apiPath)
	if upstreamURL == "" {
		c.ResponseError("No upstream endpoint configured for provider: " + provider.Name)
		return
	}

	req, err := http.NewRequest(http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		c.ResponseError(fmt.Sprintf("Failed to create upstream request: %s", err.Error()))
		return
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
		if authUser != nil {
			errRecord := &usageRecord{
				Owner:     c.billingOrg(authUser),
				Model:     userModel,
				Provider:  provider.Name,
				Origin:    provider.Origin(),
				Premium:   isPremium,
				Status:    "error",
				ErrorMsg:  err.Error(),
				ClientIP:  c.Ctx.Request.RemoteAddr,
				RequestID: requestId,
			}
			errRecord.bind(c.Ctx.Request.Context(), authUser)
			recordUsage(errRecord)
			recordTrace(c.Ctx.Request.Context(), errRecord, startTime)
		}
		c.ResponseError(fmt.Sprintf("Upstream request failed: %s", err.Error()))
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		c.ResponseError(fmt.Sprintf("Failed to read upstream response: %s", err.Error()))
		return
	}

	// The same rule the family path above already states: a vendor whose account
	// with us is spent must not be forwarded as the caller's own 402.
	if resp.StatusCode != http.StatusOK {
		c.ResponseFailure(relay(userModel, provider.Name, resp.StatusCode, respBody))
		return
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
			Owner:        c.billingOrg(authUser),
			Organization: authUser.Owner,
			Model:        userModel,
			Provider:     provider.Name,
			Origin:       provider.Origin(),
			PromptTokens: upstreamResp.Usage.PromptTokens,
			TotalTokens:  upstreamResp.Usage.TotalTokens,
			Currency:     "USD",
			Premium:      isPremium,
			Status:       status,
			ClientIP:     c.Ctx.Request.RemoteAddr,
			RequestID:    requestId,
		}
		rec.bind(c.Ctx.Request.Context(), authUser)
		recordUsage(rec)
		recordTrace(c.Ctx.Request.Context(), rec, startTime)
	}

	for k, vals := range resp.Header {
		for _, v := range vals {
			c.Ctx.ResponseWriter.Header().Add(k, v)
		}
	}
	c.Ctx.ResponseWriter.WriteHeader(resp.StatusCode)
	c.Ctx.Output.Body(respBody)
	c.EnableRender = false
}

// setJSONModel returns raw with its top-level "model" field replaced by model,
// preserving all other fields. Used to translate the user-facing model name to
// the upstream provider's model id before proxying.
func setJSONModel(raw []byte, model string) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	mv, err := json.Marshal(model)
	if err != nil {
		return nil, err
	}
	m["model"] = mv
	return json.Marshal(m)
}

// coerceDocText extracts the document text from a rerank "documents" element,
// which may be a bare JSON string or an object with a "text" field.
func coerceDocText(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var obj struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.Text != "" {
		return obj.Text
	}
	return strings.TrimSpace(string(raw))
}

// isNativeRerankProvider reports whether a provider type exposes a first-class
// /rerank endpoint that we proxy directly (rather than synthesizing scores).
func isNativeRerankProvider(providerType string) bool {
	switch providerType {
	case "Jina", "Cohere", "Voyage":
		return true
	default:
		return false
	}
}

// embedTexts calls the provider's /embeddings endpoint once for all texts and
// returns the vectors ordered by their input index.
func embedTexts(provider *object.Provider, upstreamModel string, texts []string) ([][]float64, error) {
	reqBody, err := json.Marshal(map[string]interface{}{
		"model": upstreamModel,
		"input": texts,
	})
	if err != nil {
		return nil, err
	}

	upstreamURL, apiKey, authHeader := resolveEndpointForPath(provider, "embeddings")
	if upstreamURL == "" {
		return nil, fmt.Errorf("no embeddings endpoint configured for provider %s", provider.Name)
	}

	req, err := http.NewRequest(http.MethodPost, upstreamURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	} else if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("embeddings upstream returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var er struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &er); err != nil {
		return nil, err
	}
	if len(er.Data) != len(texts) {
		return nil, fmt.Errorf("embeddings upstream returned %d vectors for %d inputs", len(er.Data), len(texts))
	}

	out := make([][]float64, len(texts))
	for _, d := range er.Data {
		if d.Index >= 0 && d.Index < len(out) {
			out[d.Index] = d.Embedding
		}
	}
	// Guard against a provider that omits/duplicates indices.
	for i := range out {
		if out[i] == nil {
			return nil, fmt.Errorf("embeddings upstream missing vector for input %d", i)
		}
	}
	return out, nil
}

// rankResult is one scored document, sorted by descending relevance.
type rankResult struct {
	Index int
	Score float64
}

// rankByCosine ranks each document vector against the query vector by cosine
// similarity, returning results sorted by descending score (stable for ties).
func rankByCosine(query []float64, documents [][]float64) []rankResult {
	results := make([]rankResult, len(documents))
	for i, d := range documents {
		results[i] = rankResult{Index: i, Score: cosineSimilarity(query, d)}
	}
	sort.SliceStable(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})
	return results
}

// cosineSimilarity returns the cosine similarity of two equal-length vectors,
// or 0 when undefined (mismatched length or a zero vector).
func cosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
