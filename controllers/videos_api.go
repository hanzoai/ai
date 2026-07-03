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

	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
	iam "github.com/hanzoai/iam"
)

// This file completes the OpenAI-compatible surface alongside chat, embeddings,
// and images: the /v1/videos/generations endpoint (text-to-video). It rides the
// SAME authentication, model-routing, provider-resolution, balance-reservation,
// and usage-recording machinery as ImagesGenerations — one auth+routing policy,
// one billing ledger. The only video-specific piece is the execution:
// model.GenerateVideoDOAI drives DO's Sora-style async /v1/videos API (create →
// poll → download) for wan2-2-t2v-a14b (and the zen3-video brand family that
// routes to it), and the response is the OpenAI images-shaped JSON
// {"created":<ts>,"data":[{"b64_json":…}]} — the video bytes are delivered
// base64 because DO serves them ONLY behind a key-authenticated /content
// endpoint, so the do-ai key never leaves the server.

// videosGenerationsRequest is the /v1/videos/generations request body. Only
// model + prompt are required; the rest are optional and passed through.
type videosGenerationsRequest struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	N              int    `json:"n"`
	Size           string `json:"size"`
	Seconds        int    `json:"seconds"`
	ResponseFormat string `json:"response_format"`
}

// VideosGenerations implements POST /v1/videos/generations (OpenAI-style).
//
// Body: {"model": "...", "prompt": "...", "n"?: int, "size"?: "1280x720",
//
//	"seconds"?: int, "response_format"?: "b64_json"}
//
// It authenticates the caller, resolves the model to its upstream provider via
// the shared routing table (zen3-video* / wan2-2-t2v-a14b → do-ai), reserves the
// per-video budget, generates the video(s) through the do-ai async video client,
// records usage for billing, and returns the OpenAI-style videos response.
// @Title VideosGenerations
// @Tag OpenAI Compatible API
// @Description OpenAI compatible text-to-video generations API.
// @router /videos/generations [post]
func (c *ApiController) VideosGenerations() {
	token, ok := c.bearerToken()
	if !ok {
		return
	}
	if isPublishableKey(token) {
		c.rejectPublishableKey()
		return
	}

	var req videosGenerationsRequest
	badReq := ""
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, &req); err != nil {
		badReq = fmt.Sprintf("Failed to parse request: %s", err.Error())
	} else if req.Model == "" {
		badReq = "videos request requires a \"model\" field"
	} else if req.Prompt == "" {
		badReq = "videos request requires a \"prompt\" field"
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
	orgId := c.GetEffectiveOrg()

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

	// Clamp n to [1, videoMaxN] BEFORE any cost math so videoCostCents/reserveBudget
	// are never fed an unbounded n (mirrors the same clamp in model.GenerateVideoDOAI).
	// The ceiling is deliberately lower than images (videos are minutes-long and
	// expensive), making the money-safety explicit rather than relying on int
	// overflow.
	const videoMaxN = 4
	n := req.N
	if n < 1 {
		n = 1
	}
	if n > videoMaxN {
		n = videoMaxN
	}

	// ── Balance reservation ────────────────────────────────────────────
	// Reserve the exact per-video cost so concurrent video requests for one
	// subject can't double-spend a balance the async debit hasn't applied yet.
	// Settled with the ACTUAL produced-video cost on completion (fail-safe
	// release deferred). Videos are billed per video, so the reservation is the
	// video cost, not a token estimate.
	var hold *budgetHold
	if authUser != nil {
		subject := object.BillingSubject(authUser.Owner, authUser.Name)
		var ok bool
		if hold, ok = reserveBudget(subject, videoCostCents(req.Model, n)); !ok {
			c.ResponseAuthError(billingError("Insufficient balance for the estimated video cost. Add credits at console.hanzo.ai"))
			return
		}
	}
	defer hold.settle(0)

	// Generate the video(s) via the do-ai async video client. The provider's
	// resolved key (never logged) and base URL drive create → poll → download.
	// Video generation is minutes-long, so the ctx ceiling sits just above the
	// client's own doaiVideoMaxWait (300s) per produced video.
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(n)*310*time.Second)
	defer cancel()

	upstreamURL := videoUpstreamBase(provider)
	if upstreamURL == "" {
		c.ResponseError("No upstream endpoint configured for provider: " + provider.Name)
		return
	}

	result, err := model.GenerateVideoDOAI(ctx, upstreamURL, provider.ClientSecret, model.VideoGenRequest{
		UpstreamModel: provider.SubType,
		Prompt:        req.Prompt,
		N:             n,
		Size:          req.Size,
		Seconds:       req.Seconds,
	})
	if err != nil {
		c.recordVideoUsage(authUser, provider, req.Model, isPremium, 0, "error", err.Error(), startTime)
		c.ResponseError(fmt.Sprintf("Video generation failed: %s", err.Error()))
		return
	}

	// Bill for the videos produced, capped at the requested n. The reservation
	// covered n; capping here guarantees the settle can never exceed the hold
	// even if the upstream ever returned more videos than requested — the user is
	// never charged beyond what they asked for.
	billedCount := len(result.Videos)
	if billedCount > n {
		billedCount = n
	}
	hold.settle(videoCostCents(req.Model, billedCount))

	c.recordVideoUsage(authUser, provider, req.Model, isPremium, billedCount, "success", "", startTime)

	c.jsonResponse(map[string]interface{}{
		"created": time.Now().Unix(),
		"data":    videoResponseData(result),
	})
}

// videoResponseData projects the generated videos into the OpenAI images-shaped
// data array: each element carries "b64_json" (the base64 MP4 the do-ai /content
// endpoint returned) and, when present, its "mime_type" and any hosted "url".
func videoResponseData(result *model.VideoGenResult) []map[string]string {
	data := make([]map[string]string, 0, len(result.Videos))
	for _, v := range result.Videos {
		entry := map[string]string{}
		if v.URL != "" {
			entry["url"] = v.URL
		}
		if v.B64JSON != "" {
			entry["b64_json"] = v.B64JSON
		}
		if v.MimeType != "" {
			entry["mime_type"] = v.MimeType
		}
		data = append(data, entry)
	}
	return data
}

// videoUpstreamBase returns the provider's base URL for the async video API (the
// do-ai provider's ProviderUrl, e.g. https://inference.do-ai.run/v1). It reuses
// resolveEndpointForPath so provider URL handling lives in exactly one place; the
// async client appends /videos itself, so the "" apiPath yields the clean base
// with the provider's /v1 normalization applied.
func videoUpstreamBase(provider *object.Provider) string {
	base, _, _ := resolveEndpointForPath(provider, "")
	// resolveEndpointForPath returns "<base>/" for the empty path; trim the
	// trailing slash so the async client's "/videos" join is clean.
	for len(base) > 0 && base[len(base)-1] == '/' {
		base = base[:len(base)-1]
	}
	return base
}

// recordVideoUsage records a single video-generation usage event for billing +
// observability, mirroring recordImageUsage. videoCount drives the per-video
// cost; only successful calls are billed (recordUsage filters error status), but
// the trace is emitted either way.
func (c *ApiController) recordVideoUsage(authUser *iam.User, provider *object.Provider, userModel string, isPremium bool, videoCount int, status, errMsg string, startTime time.Time) {
	if authUser == nil {
		return
	}
	rec := &usageRecord{
		Owner:        authUser.Owner,
		User:         authUser.Owner + "/" + authUser.Name,
		Organization: authUser.Owner,
		Model:        userModel,
		Provider:     provider.Name,
		VideoCount:   videoCount,
		Currency:     "USD",
		Premium:      isPremium,
		Status:       status,
		ErrorMsg:     errMsg,
		ClientIP:     c.Ctx.Request.RemoteAddr,
		RequestID:    util.GenerateUUID(),
	}
	recordUsage(rec)
	recordTrace(rec, startTime)
}
