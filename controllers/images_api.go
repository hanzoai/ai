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
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/object"
)

// This file completes the OpenAI-compatible surface alongside chat and
// embeddings: the /v1/images/generations endpoint. It rides the SAME
// authentication, model-routing, provider-resolution, balance-reservation, and
// usage-recording machinery as ChatCompletions and Embeddings (see
// openai_api.go / embeddings_api.go) — one auth+routing policy, one billing
// ledger. The only image-specific piece is the execution:
// model.GenerateImageForModel dispatches to the correct upstream shape — the
// fal-hosted diffusion models (zen3-image* / fal-ai/*) via DO's asynchronous
// image client, and the synchronous OpenAI shape (Stable Diffusion 3.5 Large,
// dall-e, gpt-image) via client.Images.Generate — and the response is the
// OpenAI images JSON shape {"created":<ts>,"data":[{"url":…}|{"b64_json":…}]}.

// imagesGenerationsRequest is the OpenAI /v1/images/generations request body.
// Only model + prompt are required; the rest are optional and passed through.
type imagesGenerationsRequest struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	N              int    `json:"n"`
	Size           string `json:"size"`
	ResponseFormat string `json:"response_format"`
}

// ImagesGenerations implements POST /v1/images/generations (OpenAI-compatible).
//
// Body: {"model": "...", "prompt": "...", "n"?: int, "size"?: "1024x1024",
//
//	"response_format"?: "url"|"b64_json"}
//
// It authenticates the caller, resolves the model to its upstream provider via
// the shared routing table (zen3-image* → do-ai fal diffusion), reserves the
// per-image budget, generates the image(s) through the do-ai async image
// client, records usage for billing, and returns the OpenAI images response.
// @Title ImagesGenerations
// @Tag OpenAI Compatible API
// @Description OpenAI compatible image generations API.
// @router /images/generations [post]
func (c *ApiController) ImagesGenerations() {
	token, ok := c.bearerToken()
	if !ok {
		return
	}
	if isPublishableKey(token) {
		c.rejectPublishableKey()
		return
	}

	var req imagesGenerationsRequest
	badReq := ""
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		badReq = fmt.Sprintf("Failed to parse request: %s", err.Error())
	} else if req.Model == "" {
		badReq = "images request requires a \"model\" field"
	} else if req.Prompt == "" {
		badReq = "images request requires a \"prompt\" field"
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

	provider, authUser, upstreamModel, isPremium, _, err := c.authResolveProvider(token, req.Model, orgId)
	if err != nil {
		c.ResponseAuthError(err)
		return
	}
	if provider.Category != "Model" {
		c.ResponseError(fmt.Sprintf("Provider %s is not a model provider", provider.Name))
		return
	}
	if upstreamModel != "" {
		provider.SubType = upstreamModel
	} else if req.Model != "" {
		provider.SubType = req.Model
	}

	// Zen family: forward to the zen service, billed per image at the discovered
	// price. zen owns the SKU→upstream mapping; ai authenticates, meters, forwards.
	if provider.Type == "Zen" {
		n := req.N
		if n < 1 {
			n = 1
		}
		if n > 10 {
			n = 10
		}
		c.serveZenMedia("images/generations", req.Model, c.Body(), n, orgId, authUser, isPremium, startTime)
		return
	}

	// Clamp n to [1,10] BEFORE any cost math so imageCostCents/reserveBudget are
	// never fed an unbounded n (mirrors the same clamp in doaiImageSubmit). This
	// makes the money-safety explicit rather than relying on int overflow.
	n := req.N
	if n < 1 {
		n = 1
	}
	if n > 10 {
		n = 10
	}

	// ── Balance reservation ────────────────────────────────────────────
	// Reserve the exact per-image cost so concurrent image requests for one
	// subject can't double-spend a balance the async debit hasn't applied yet.
	// Settled with the ACTUAL produced-image cost on completion (fail-safe
	// release deferred). Images are billed per image, so the reservation is the
	// image cost, not a token estimate.
	var hold *budgetHold
	if authUser != nil {
		ledger := c.billingOrg(authUser)
		subject := authUser.PayerSubject(ledger)
		var ok bool
		if hold, ok = reserveBudget(subject, imageCostCents(req.Model, n)); !ok {
			c.ResponseAuthError(billingError("%s", object.InsufficientBalance(c.Host(), ledger, "image cost").Message))
			return
		}
	}
	defer hold.settle(0)

	// Generate the image(s) via the do-ai async image client. The provider's
	// resolved key (never logged) and base URL drive submit → poll → retrieve.
	ctx, cancel := context.WithTimeout(context.Background(), 130*time.Second)
	defer cancel()

	upstreamURL := imageUpstreamBase(provider)
	if upstreamURL == "" {
		c.ResponseError("No upstream endpoint configured for provider: " + provider.Name)
		return
	}

	result, err := model.GenerateImageForModel(ctx, upstreamURL, provider.ClientSecret, model.ImageGenRequest{
		UpstreamModel: provider.SubType,
		Prompt:        req.Prompt,
		N:             n,
		Size:          req.Size,
	})
	if err != nil {
		c.recordImageUsage(authUser, provider, req.Model, isPremium, 0, "error", err.Error(), startTime)
		c.ResponseError(fmt.Sprintf("Image generation failed: %s", err.Error()))
		return
	}

	// Bill for the images produced, capped at the requested n. The reservation
	// covered n; capping here guarantees the settle can never exceed the hold
	// even if the upstream ever returned more images than requested — the user
	// is never charged beyond what they asked for.
	billedCount := len(result.Images)
	if billedCount > n {
		billedCount = n
	}
	hold.settle(imageCostCents(req.Model, billedCount))

	c.recordImageUsage(authUser, provider, req.Model, isPremium, billedCount, "success", "", startTime)

	c.jsonResponse(map[string]interface{}{
		"created": time.Now().Unix(),
		"data":    imageResponseData(result),
	})
}

// imageResponseData projects the generated images into the OpenAI images data
// array: each element carries "url" or "b64_json" per what the upstream
// returned (fal returns url).
func imageResponseData(result *model.ImageGenResult) []map[string]string {
	data := make([]map[string]string, 0, len(result.Images))
	for _, im := range result.Images {
		entry := map[string]string{}
		if im.URL != "" {
			entry["url"] = im.URL
		}
		if im.B64JSON != "" {
			entry["b64_json"] = im.B64JSON
		}
		data = append(data, entry)
	}
	return data
}

// imageUpstreamBase returns the provider's base URL for the async image API
// (the do-ai provider's ProviderUrl, e.g. https://inference.do-ai.run/v1). It
// reuses endpoint so provider URL handling lives in exactly one place; the async
// client appends /async-invoke itself, so the empty path yields the clean base
// with the provider's /v1 normalization applied.
func imageUpstreamBase(provider *object.Provider) string {
	base := endpoint(provider, "")
	// endpoint returns "<base>/" for the empty path; trim the trailing slash so
	// the async client's "/async-invoke" join is clean.
	for len(base) > 0 && base[len(base)-1] == '/' {
		base = base[:len(base)-1]
	}
	return base
}

// recordImageUsage records a single image-generation usage event for billing +
// observability, mirroring the embeddings/rerank recording. imageCount drives
// the per-image cost; only successful calls are billed (recordUsage filters
// error status), but the trace is emitted either way.
func (c *ApiController) recordImageUsage(authUser *iam.User, provider *object.Provider, userModel string, isPremium bool, imageCount int, status, errMsg string, startTime time.Time) {
	if authUser == nil {
		return
	}
	rec := &usageRecord{
		Owner:        c.billingOrg(authUser),
		Organization: authUser.Owner,
		Model:        userModel,
		Provider:     provider.Name,
		Origin:       provider.Origin(),
		ImageCount:   imageCount,
		Currency:     "USD",
		Premium:      isPremium,
		Status:       status,
		ErrorMsg:     errMsg,
		ClientIP:     c.Fiber().IP(),
		RequestID:    uuid.NewString(),
	}
	rec.bind(c.Context(), authUser)
	recordUsage(rec)
	recordTrace(c.Context(), rec, startTime)
}
