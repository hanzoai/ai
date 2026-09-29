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

// Native ZAP handlers for POST /v1/decisions — the pure-ZAP twins of
// ApiController.Decisions (decisions.go). They resolve the principal as the HTTP
// path does and hand it to the same decide, so the reservation, the debit and every
// handle name the same org whichever door the call came through. The path stays
// live on routers.App, which also backs the gateway fallback.

package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/luxfi/zap"

	"github.com/hanzoai/account"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

func init() {
	registerCloud("decisions", func(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
		return decisionCloud(zapDecision(ctx, auth, body))
	})
	registerGatewayPath(decisionsPath, func(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
		return decisionGateway(zapDecision(ctx, auth, body))
	})
}

// decisionCloud is a reply on the cloud wire: status, body, and a refusal's words
// in the error slot. The cloud wire (MsgType 100) has no header slot, so the
// request id and a refusal's Retry-After do not cross it; they are never folded into
// the body, which is the service's own shape. A caller that needs them reaches the
// same handler over the gateway wire (MsgType 200) or HTTP.
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
func zapDecision(ctx context.Context, auth string, body []byte) decisionReply {
	rid := RequestID(gatewayHeader(ctx, "X-Request-Id"))
	token := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	if token == "" {
		return refused(rid, decline(http.StatusUnauthorized, "authentication required"))
	}
	if isPublishableKey(token) {
		return refused(rid, decline(http.StatusForbidden,
			"Publishable keys (pk-) can only access read-only endpoints. Use a secret key (sk-) for this endpoint."))
	}
	// The size bound first, then who is asking, and only then the body — as on HTTP.
	if n := len(body); n > decisionBodyBytes {
		return refused(rid, tooLong(n))
	}
	if err := vouched(token, "en"); err != nil {
		return refused(rid, decline(statusOf(err), err.Error()))
	}
	model, version, bad := readModel(body)
	if bad != nil {
		return refused(rid, bad)
	}
	user, ledger, premium, err := zapDecisionPrincipal(token, model, gatewayHeader(ctx, "X-Org-Id"))
	if err != nil {
		return refused(rid, decline(statusOf(err), err.Error()))
	}
	return decide(ctx, decisionCall{
		model: model, version: version, body: body,
		user: user, ledger: ledger, premium: premium,
		rid: rid, start: time.Now().UTC(), ctx: context.WithoutCancel(ctx),
	})
}

// zapDecisionPrincipal resolves who is asking, the org that pays, and runs the
// balance gate on it, the way the HTTP path does (authResolveProvider, billingOrg).
// A JWT pays from the org it asked to act in when its signed membership covers it,
// and is refused when it does not; an IAM key or a vendor key carries no membership
// and pays from its own org, a vendor key as the org that owns it.
func zapDecisionPrincipal(token, model, asked string) (*iam.User, string, bool, error) {
	premium := false
	if route := resolveModelRoute(model); route != nil {
		premium = route.premium
	}
	if isJwtToken(token) {
		_, user, _, err := resolveProviderFromJwt(token, asked, model, "en")
		if err != nil {
			return nil, "", false, err
		}
		claims, err := object.ParseAndValidateJWT(token)
		if err != nil {
			return nil, "", false, authError("invalid access token: %s", err.Error())
		}
		effective, err := account.EffectiveOrg(user.Owner, claims.Orgs, asked)
		if err != nil {
			return nil, "", false, forbiddenError("organization %q is not available to this principal", asked)
		}
		return user, account.LedgerOrg(effective, user.Owner, util.IsSuperAdmin(user)), premium, nil
	}
	provider, user, _, err := zapResolveAuth("Bearer "+token, model)
	if err != nil {
		return nil, "", false, err
	}
	if user == nil {
		if user, err = providerKeyBillingUser(provider); err != nil {
			return nil, "", false, err
		}
		if err := enforceBalanceGate(user, user.Owner, model); err != nil {
			return nil, "", false, err
		}
	}
	return user, user.Owner, premium, nil
}
