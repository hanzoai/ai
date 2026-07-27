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

// Native ZAP handler for the images group (POST /v1/images/generations).
//
// This is the pure-ZAP re-implementation of ApiController.ImagesGenerations
// (images_api.go): same identity, org scoping, per-org provider selection,
// balance reservation, per-image metering, and OpenAI images response — driven
// against object/ + model/ directly, with no http.ResponseWriter. It mirrors
// zapChatHandler (zap_native.go) exactly; it never wraps the beego controller.
//
// The beego route stays live in parallel (strangler). Integrate flips the
// gateway/native dispatch to consult the registry below, then deletes the beego
// line once this handler is proven.

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hanzoai/account"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/log"
	"github.com/luxfi/zap"

	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

// ── Group self-registration ───────────────────────────────────────────────
//
// The registry (registerCloud / registerGatewayPath + the lookups Integrate's
// dispatch consults) is the shared ONE-TIME infra defined in
// zap_embeddings-rerank.go. This group only self-registers from its own init().

func init() {
	registerCloud("images.generations", zapImagesHandler)
	registerGatewayPath("/v1/images", zapImagesHandler)
}

// ── images.generations / POST /v1/images/generations ─────────────────────

func zapImagesHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	if auth == "" {
		return object.BuildCloudResponse(401, nil, "authentication required")
	}

	// Decode into the SAME struct the HTTP path uses (images_api.go).
	var req imagesGenerationsRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return object.BuildCloudResponse(400, nil, "invalid request: "+err.Error())
	}
	if req.Model == "" {
		return object.BuildCloudResponse(400, nil, "images request requires a \"model\" field")
	}
	if req.Prompt == "" {
		return object.BuildCloudResponse(400, nil, "images request requires a \"prompt\" field")
	}

	// Auth → provider + user + upstream model. The IAM/JWT path resolves the
	// org's OWN provider (BYO seam) via GetModelProviderByNameForOrg(authUser.Owner).
	provider, authUser, upstreamModel, err := zapResolveAuth(auth, req.Model)
	if err != nil {
		return object.BuildCloudResponse(401, nil, err.Error())
	}
	if provider.Category != "Model" {
		return object.BuildCloudResponse(400, nil, fmt.Sprintf("Provider %s is not a model provider", provider.Name))
	}

	// Prepaid-balance gate — the ONE gate, shared verbatim with the HTTP path.
	if gateErr := enforceBalanceGate(authUser, "", req.Model); gateErr != nil {
		return object.BuildCloudResponse(uint32(statusOf(gateErr)), nil, gateErr.Error())
	}
	isPremium := false
	if route := resolveModelRoute(req.Model); route != nil {
		isPremium = route.premium
	}

	// KMS secrets (env-first).
	if err := object.ResolveProviderSecret(provider); err != nil {
		log.Error("ZAP: KMS resolve %s: %v", provider.Name, err)
	}

	// Upstream model on the provider (mirrors the HTTP path).
	if upstreamModel != "" {
		provider.SubType = upstreamModel
	} else if req.Model != "" {
		provider.SubType = req.Model
	}

	startTime := time.Now().UTC()

	// Clamp n to [1,10] BEFORE any cost math (mirrors images_api.go + doaiImageSubmit).
	n := req.N
	if n < 1 {
		n = 1
	}
	if n > 10 {
		n = 10
	}

	// Zen family: forward to the zen service, billed per image at the discovered
	// price. zen owns the SKU→upstream mapping; ai authenticates, meters, forwards.
	if provider.Type == "Zen" {
		return zapServeZenMedia("images/generations", req.Model, body, n, authUser, isPremium, startTime)
	}

	// ── Balance reservation ────────────────────────────────────────────
	// Reserve the exact per-image cost so concurrent image requests for one
	// subject can't double-spend. Settled with the ACTUAL produced-image cost.
	var hold *budgetHold
	if authUser != nil {
		subject := account.Payer(account.Credential{Owner: authUser.Owner, Name: authUser.Name}).Subject()
		var ok bool
		if hold, ok = reserveBudget(subject, imageCostCents(req.Model, n)); !ok {
			return object.BuildCloudResponse(402, nil, object.InsufficientBalance(authUser.Owner, "image cost").Message)
		}
	}
	defer hold.settle(0)

	// Generate the image(s) via the do-ai async image client.
	genCtx, cancel := context.WithTimeout(context.Background(), 130*time.Second)
	defer cancel()

	upstreamURL := imageUpstreamBase(provider)
	if upstreamURL == "" {
		return object.BuildCloudResponse(502, nil, "No upstream endpoint configured for provider: "+provider.Name)
	}

	result, err := model.GenerateImageForModel(genCtx, upstreamURL, provider.ClientSecret, model.ImageGenRequest{
		UpstreamModel: provider.SubType,
		Prompt:        req.Prompt,
		N:             n,
		Size:          req.Size,
	})
	if err != nil {
		zapRecordImageUsage(ctx, authUser, provider, req.Model, isPremium, 0, "error", err.Error(), startTime)
		return object.BuildCloudResponse(502, nil, "Image generation failed: "+err.Error())
	}

	// Bill for the images produced, capped at the requested n.
	billedCount := len(result.Images)
	if billedCount > n {
		billedCount = n
	}
	hold.settle(imageCostCents(req.Model, billedCount))

	zapRecordImageUsage(ctx, authUser, provider, req.Model, isPremium, billedCount, "success", "", startTime)

	data, _ := json.Marshal(map[string]interface{}{
		"created": time.Now().Unix(),
		"data":    imageResponseData(result),
	})
	return object.BuildCloudResponse(200, data, "")
}

// zapRecordImageUsage records a single image-generation usage event, mirroring
// recordImageUsage (images_api.go) — one meter, extended not forked. No client
// IP is available on the ZAP path.
func zapRecordImageUsage(ctx context.Context, authUser *iam.User, provider *object.Provider, userModel string, isPremium bool, imageCount int, status, errMsg string, startTime time.Time) {
	if authUser == nil {
		return
	}
	rec := &usageRecord{
		Owner:        authUser.Owner,
		User:         authUser.Owner + "/" + authUser.Name,
		Organization: authUser.Owner,
		Model:        userModel,
		Provider:     provider.Name,
		ImageCount:   imageCount,
		Currency:     "USD",
		Premium:      isPremium,
		Status:       status,
		ErrorMsg:     errMsg,
		RequestID:    util.GenerateUUID(),
	}
	recordUsage(rec)
	recordTrace(ctx, rec, startTime)
}

// The Zen media forward (zapServeZenMedia / zapRecordZenMediaUsage) is the ONE
// shared zen-media meter defined in zap_audio.go; the images group reuses it.
