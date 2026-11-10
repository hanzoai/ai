// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"context"
	"encoding/json"
	"time"

	"github.com/hanzoai/ai/object"
	"github.com/luxfi/zap"
)

func init() {
	registerCloud("threed.generate", zapThreeDGenerateHandler)
	registerGatewayPath("/v1/3d/generations", zapThreeDGenerateHandler)
}

func zapThreeDGenerateHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	if auth == "" {
		return object.BuildCloudResponse(401, nil, "authentication required")
	}
	var req threeDGenerationsRequest
	if err := json.Unmarshal(body, &req); err != nil || req.Model == "" || req.Prompt == "" {
		return object.BuildCloudResponse(400, nil, "3d request requires model and prompt")
	}
	provider, authUser, _, err := zapResolveAuth(auth, req.Model)
	if err != nil {
		return object.BuildCloudResponse(401, nil, err.Error())
	}
	if provider.Type != "Zen" {
		return object.BuildCloudResponse(400, nil, "model \""+req.Model+"\" does not serve the /v1/3d/generations endpoint")
	}
	isPremium := false
	if route := resolveModelRoute(req.Model); route != nil {
		isPremium = route.premium
	}
	return zapServeZenMedia("3d/generations", req.Model, body, 1, authUser, isPremium, time.Now().UTC())
}
