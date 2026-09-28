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

// Native ZAP handlers for POST /v1/decisions and POST /v1/systemone — the pure-ZAP
// twins of ApiController.Decisions and ApiController.Systemone (decisions.go). Both
// resolve the principal as the HTTP path does and hand it to the same decide, so the
// reservation, the debit and every handle name the same org whichever door the call
// came through. The paths stay live on routers.App, which also backs the gateway
// fallback.

package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/luxfi/zap"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
)

func init() {
	registerCloud("decisions", func(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
		return decisionCloud(zapDecision(ctx, decisionsPath, auth, body))
	})
	registerCloud("systemone", func(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
		return decisionCloud(zapDecision(ctx, systemonePath, auth, body))
	})
	registerGatewayPath(decisionsPath, func(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
		return decisionGateway(zapDecision(ctx, decisionsPath, auth, body))
	})
	registerGatewayPath(systemonePath, func(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
		return decisionGateway(zapDecision(ctx, systemonePath, auth, body))
	})
}

// decisionCloud is a reply on the cloud wire: status, body, and a refusal's words
// in the error slot.
func decisionCloud(r decisionReply) (*zap.Message, error) {
	said := ""
	if r.status >= http.StatusBadRequest {
		var v map[string]any
		_ = json.Unmarshal(r.body, &v)
		said = wording(v, r.body, r.status)
	}
	return object.BuildCloudResponse(uint32(r.status), r.body, said)
}

// decisionGateway is a reply on the gateway wire, whose third slot carries the
// headers: the request id and, on a refusal that asks for it, the wait.
func decisionGateway(r decisionReply) (*zap.Message, error) {
	header := map[string]string{"Content-Type": "application/json"}
	for k, v := range r.header {
		header[k] = v
	}
	h, _ := json.Marshal(header)
	return object.BuildGatewayResponse(uint32(r.status), r.body, h)
}

// zapDecision is the native twin of ApiController.decision. A ZAP call names no
// org to switch to, so it pays from the principal's own.
func zapDecision(ctx context.Context, path, auth string, body []byte) decisionReply {
	rid := RequestID("")
	token := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	if token == "" {
		return refused(path, rid, decline(path, http.StatusUnauthorized, "authentication required"))
	}
	if isPublishableKey(token) {
		return refused(path, rid, decline(path, http.StatusForbidden,
			"Publishable keys (pk-) can only access read-only endpoints. Use a secret key (sk-) for this endpoint."))
	}
	model, bad := decisionModel(path, body)

	// Authentication comes first, as on HTTP: an invalid credential is 401 whatever
	// the body says. A body naming no model this path serves is resolved as kai, and
	// once the credential holds, the body's own error is the answer.
	asked := model
	if bad != nil {
		asked = "kai"
	}
	user, premium, err := zapDecisionPrincipal(token, asked)
	if err != nil && (bad == nil || statusOf(err) == http.StatusUnauthorized || statusOf(err) == http.StatusForbidden) {
		return refused(path, rid, decline(path, statusOf(err), err.Error()))
	}
	if bad != nil {
		return refused(path, rid, bad)
	}
	var ledger string
	if user != nil {
		ledger = user.Owner
	}
	return decide(ctx, decisionCall{
		path: path, model: model, body: body,
		user: user, ledger: ledger, premium: premium,
		rid: rid, start: time.Now().UTC(), ctx: context.WithoutCancel(ctx),
	})
}

// zapDecisionPrincipal resolves who is asking and runs the balance gate on the org
// that pays, the way authResolveProvider does for HTTP: an IAM key or a JWT through
// the resolver, whose gate reads the principal's own org, and a vendor key as the
// org that owns it, gated the same way.
func zapDecisionPrincipal(token, model string) (*iam.User, bool, error) {
	provider, user, _, err := zapResolveAuth("Bearer "+token, model)
	if err != nil {
		return nil, false, err
	}
	if user == nil {
		if user, err = providerKeyBillingUser(provider); err != nil {
			return nil, false, err
		}
		if err := enforceBalanceGate(user, user.Owner, model); err != nil {
			return nil, false, err
		}
	}
	premium := false
	if route := resolveModelRoute(model); route != nil {
		premium = route.premium
	}
	return user, premium, nil
}
