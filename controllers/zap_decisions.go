// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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

// Native ZAP handler for POST /v1/decisions — the pure-ZAP twin of
// ApiController.Decisions (decisions.go), on the same pipeline as the rerank
// twin: auth → balance gate → forward unchanged → meter. POST /v1/decisions stays
// live on routers.App, which also backs the gateway fallback.

package controllers

import (
	"context"
	"net/http"
	"time"

	"github.com/luxfi/zap"

	"github.com/hanzoai/ai/object"
)

func init() {
	registerCloud("decisions", zapDecisionsHandler)
	registerGatewayPath("/v1/decisions", zapDecisionsHandler)
}

// zapDecisionsHandler is the native twin of ApiController.Decisions. The
// service's answer, and its refusals, come back as the body with the service's
// status; ai's own refusals of the body are in the service's error shape.
func zapDecisionsHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	if auth == "" {
		return object.BuildCloudResponse(401, nil, "authentication required")
	}
	model, bad := decisionModel(body)
	if bad != nil {
		return object.BuildCloudResponse(uint32(bad.status), bad.body, "")
	}

	_, authUser, _, err := zapResolveAuth(auth, model)
	if err != nil {
		return object.BuildCloudResponse(401, nil, err.Error())
	}
	// The ONE prepaid-balance gate, shared verbatim with the HTTP path.
	if gateErr := enforceBalanceGate(authUser, "", model); gateErr != nil {
		return object.BuildCloudResponse(uint32(statusOf(gateErr)), nil, gateErr.Error())
	}
	isPremium := false
	if route := resolveModelRoute(model); route != nil {
		isPremium = route.premium
	}

	startTime := time.Now().UTC()
	kai := object.KaiProvider()
	status, out, usage, fault := decide(ctx, kai, body)
	if fault != nil {
		return object.BuildCloudResponse(uint32(fault.status), fault.body, "")
	}
	if status == http.StatusOK && authUser != nil {
		rec := decisionRecord(ctx, authUser.Owner, authUser, model, kai, isPremium, usage)
		// One goroutine for both, in this order: recordUsage stamps the honesty
		// flag the span then reads.
		go func() {
			recordUsage(rec)
			recordTrace(ctx, rec, startTime)
		}()
	}
	return object.BuildCloudResponse(uint32(status), out, "")
}
