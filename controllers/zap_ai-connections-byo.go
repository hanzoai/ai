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

// zap_ai-connections-byo.go — native ZAP twins of the AI login-manager
// (connections_api.go / connections_oauth.go / connections_usage.go).
//
// These re-implement the login-manager's org-scoped BYO-connection surface
// directly against object/ + iam, mirroring zapChatHandler: identity comes from
// the ZAP auth seam (never the body), the org is the resolved principal's Owner
// (never a client-supplied field), and every seal/no-leak invariant is preserved
// by REUSING the same pure functions the HTTP handlers use — buildConnectionProvider,
// connectionView, sealProviderSecret, persistConnection, unsealConnectionKey,
// providerUsageImporterFor. There is exactly one credential store and one seal path.
//
// Scope of this group (native cloud, MsgType 100):
//   ai.connections.list   ← GET  /v1/ai/connections        (GetAIConnections)
//   ai.connections.add    ← POST /v1/ai/connections        (AddAIConnection)
//   ai.connections.delete ← POST|DELETE /v1/ai/connections/:provider (DeleteAIConnection)
//   ai.connections.usage  ← GET  /v1/ai/connections/:provider/usage  (GetAIConnectionUsage)
// The native protocol carries no path segment, so :provider and the usage window
// travel in the request BODY.
//
// NOT migrated here — the OAuth pair ConnectAIProvider / CallbackAIProvider
// (GET .../authorize, GET .../callback). Those are browser-redirect (302) flows
// with no native-ZAP caller: authorize sends a browser to the provider and callback
// is hit BY the provider's redirect. They stay on beego (strangler) and can only be
// projected onto the gateway (MsgType 200) once Integrate settles the registry on a
// PATH-CARRYING gateway handler signature (the group's DELETE/usage/authorize/callback
// routes all need the request path + query, which the 3-arg gateway variant cannot
// supply). Until then registerGatewayPath is intentionally not called from this file.
//
// Registration convention (recipe): this group self-registers from its own file via
// init() → registerCloud. The registry primitives are the ONE shared home Integrate
// reconciles across the mid-flight groups; this file never redefines them and only
// depends on registerCloud, whose (method, func(ctx,auth,body)) shape every registry
// variant agrees on. Beego keeps serving these routes in parallel until Integrate
// flips native dispatch.

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/luxfi/zap"

	"github.com/hanzoai/ai/object"
)

// connGetProvider is the org-connection read seam. It defaults to object.GetProvider
// (the SAME read the HTTP handlers use) and is a package var only so the parity tests
// can drive the cores with a stubbed row instead of a live datastore — mirroring the
// sealProviderSecret var seam already established in connections_oauth.go.
var connGetProvider = object.GetProvider

func init() { registerZapAIConnections() }

// registerZapAIConnections binds the group's native cloud methods. Named so
// InitZapHandlers can also call it explicitly (no per-group switch arm).
func registerZapAIConnections() {
	registerCloud("ai.connections.list", zapConnectionsListCloud)
	registerCloud("ai.connections.add", zapConnectionsAddCloud)
	registerCloud("ai.connections.delete", zapConnectionsDeleteCloud)
	registerCloud("ai.connections.usage", zapConnectionsUsageCloud)
}

// ── identity / org scoping (the ONE auth seam) ──────────────────────────────

// zapConnectionOrg resolves the caller's org from the VERIFIED ZAP principal,
// never from the body. It reuses zapResolveUser (pk-/sk- IAM keys via
// getUserByAccessKey, JWTs via object.ParseAndValidateJWT) and takes the Owner half
// of "owner/name" — the exact tenant GetOrg defaults to for a non-admin, so a caller
// can only ever manage its OWN org's connections.
func zapConnectionOrg(auth string) (string, error) {
	if strings.TrimSpace(auth) == "" {
		return "", fmt.Errorf("authentication required")
	}
	id, err := zapResolveUser(auth)
	if err != nil {
		return "", err
	}
	owner := id
	if i := strings.IndexByte(id, '/'); i >= 0 {
		owner = id[:i]
	}
	if strings.TrimSpace(owner) == "" {
		return "", fmt.Errorf("authentication required")
	}
	return owner, nil
}

// ── shared cores (org-scoped, transport-agnostic) ───────────────────────────
//
// connOutcome is the one small result the cores return so the native-cloud wrapper
// (and any future gateway wrapper) encode identically. A non-empty errMsg is the
// error text; otherwise payload is the JSON success body.

type connOutcome struct {
	status  int
	payload interface{}
	errMsg  string
}

// connListCore lists the org's connectable accounts and whether each is connected.
// Never surfaces a key or a kms:// reference — connectionView is the no-leak seam.
func connListCore(org string) connOutcome {
	out := make([]aiConnResponse, 0, len(aiConnOrder))
	for _, p := range aiConnOrder {
		spec := aiConnSpecs[p]
		row, err := connGetProvider(org + "/" + spec.name)
		if err != nil {
			return connOutcome{status: http.StatusInternalServerError, errMsg: err.Error()}
		}
		out = append(out, connectionView(p, row))
	}
	return connOutcome{status: http.StatusOK, payload: out}
}

// connAddCore seals the supplied key into KMS and upserts the org's provider row via
// the SAME sealProviderSecret + persistConnection path the BYOK and OAuth flows use.
// The raw key is sealed BEFORE the row is built (fail-closed) and never persisted or
// echoed — connAddCore returns metadata only.
func connAddCore(org, provider, apiKey string) connOutcome {
	spec, ok := aiConnSpecFor(provider)
	if !ok {
		return connOutcome{status: http.StatusBadRequest, errMsg: "provider must be one of: " + aiConnProviderList()}
	}
	if strings.TrimSpace(apiKey) == "" {
		return connOutcome{status: http.StatusBadRequest, errMsg: "apiKey is required"}
	}
	ref, err := sealProviderSecret(connectionSecretName(org, spec.name), apiKey)
	if err != nil {
		return connOutcome{status: http.StatusServiceUnavailable, errMsg: "secret store unavailable, cannot store key"}
	}
	if err := persistConnection(org, spec, ref); err != nil {
		return connOutcome{status: http.StatusInternalServerError, errMsg: err.Error()}
	}
	return connOutcome{status: http.StatusOK, payload: aiConnResponse{Provider: spec.name, Connected: true, AccountLabel: org + "/" + spec.name}}
}

// connDeleteCore disconnects a connection: deactivate the org row so resolution
// reverts to Hanzo credits, then best-effort tombstone the sealed secret. Idempotent —
// a missing row is already disconnected.
func connDeleteCore(org, provider string) connOutcome {
	spec, ok := aiConnSpecFor(provider)
	if !ok {
		return connOutcome{status: http.StatusBadRequest, errMsg: "provider must be one of: " + aiConnProviderList()}
	}
	id := org + "/" + spec.name
	existing, err := connGetProvider(id)
	if err != nil {
		return connOutcome{status: http.StatusInternalServerError, errMsg: err.Error()}
	}
	if existing == nil {
		return connOutcome{status: http.StatusOK, payload: aiConnResponse{Provider: spec.name, Connected: false}}
	}
	existing.State = "Disabled"
	if _, err := object.UpdateProvider(id, existing); err != nil {
		return connOutcome{status: http.StatusInternalServerError, errMsg: err.Error()}
	}
	// The deactivation above is the authoritative disconnect; a KMS tombstone failure
	// must not fail the request.
	_, _ = object.StoreProviderSecret(connectionSecretName(org, spec.name), "")
	object.InvalidateProviderNameCache(spec.name)
	return connOutcome{status: http.StatusOK, payload: aiConnResponse{Provider: spec.name, Connected: false}}
}

// connUsageCore imports the org's usage for a connected account. The key is unsealed
// SERVER-SIDE on a COPY of the row (unsealConnectionKey) and never returned; an
// unconnected account, an unreadable key, a missing importer, or a scope-denied
// provider all degrade to an HONEST 200 ProviderUsage with connected/available flags +
// a note — never a fabricated figure. A genuine upstream transport failure is a 502.
func connUsageCore(ctx context.Context, org, provider, fromRaw, toRaw string) connOutcome {
	spec, ok := aiConnSpecFor(provider)
	if !ok {
		return connOutcome{status: http.StatusBadRequest, errMsg: "provider must be one of: " + aiConnProviderList()}
	}
	from, to := resolveProviderUsageWindow(fromRaw, toRaw, time.Now())
	out := ProviderUsage{
		Provider: spec.name,
		Currency: "usd",
		Start:    from.UTC().Format(time.RFC3339),
		End:      to.UTC().Format(time.RFC3339),
		Interval: "day",
	}

	row, err := connGetProvider(org + "/" + spec.name)
	if err != nil {
		return connOutcome{status: http.StatusInternalServerError, errMsg: err.Error()}
	}
	if !object.ModelProviderUsable(row) || !object.ProviderKeyPresent(row) {
		out.Note = "Connect your " + spec.label + " account to import its usage."
		return connOutcome{status: http.StatusOK, payload: out}
	}
	out.Connected = true

	key, err := unsealConnectionKey(row)
	if err != nil {
		return connOutcome{status: http.StatusServiceUnavailable, errMsg: "could not access the connected key"}
	}
	if strings.TrimSpace(key) == "" {
		out.Available = false
		out.Note = "The connected key can't be read on this deployment yet."
		return connOutcome{status: http.StatusOK, payload: out}
	}

	imp := providerUsageImporterFor(spec.name)
	if imp == nil {
		out.Available = false
		out.Note = "Usage import for " + spec.label + " is coming soon."
		return connOutcome{status: http.StatusOK, payload: out}
	}

	ictx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	res, err := imp.importUsage(ictx, key, from, to)
	if err != nil {
		return connOutcome{status: http.StatusBadGateway, errMsg: err.Error()}
	}
	out.Available = res.Available
	out.Note = res.Note
	if res.Currency != "" {
		out.Currency = res.Currency
	}
	out.Totals = res.Totals
	out.Series = res.Series
	out.ByModel = res.ByModel
	return connOutcome{status: http.StatusOK, payload: out}
}

// cloudOutcome encodes a core result as a native cloud (MsgType 100) response, exactly
// like zapChatHandler: an error is (status, nil, errMsg); success is (status, json, "").
func cloudOutcome(o connOutcome) (*zap.Message, error) {
	if o.errMsg != "" {
		return object.BuildCloudResponse(uint32(o.status), nil, o.errMsg)
	}
	data, _ := json.Marshal(o.payload)
	return object.BuildCloudResponse(uint32(o.status), data, "")
}

// ── native cloud handlers (MsgType 100) ─────────────────────────────────────

func zapConnectionsListCloud(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	org, err := zapConnectionOrg(auth)
	if err != nil {
		return object.BuildCloudResponse(http.StatusUnauthorized, nil, err.Error())
	}
	return cloudOutcome(connListCore(org))
}

func zapConnectionsAddCloud(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	org, err := zapConnectionOrg(auth)
	if err != nil {
		return object.BuildCloudResponse(http.StatusUnauthorized, nil, err.Error())
	}
	var req struct {
		Provider string `json:"provider"`
		APIKey   string `json:"apiKey"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return object.BuildCloudResponse(http.StatusBadRequest, nil, "invalid request: "+err.Error())
	}
	return cloudOutcome(connAddCore(org, req.Provider, req.APIKey))
}

func zapConnectionsDeleteCloud(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	org, err := zapConnectionOrg(auth)
	if err != nil {
		return object.BuildCloudResponse(http.StatusUnauthorized, nil, err.Error())
	}
	var req struct {
		Provider string `json:"provider"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return object.BuildCloudResponse(http.StatusBadRequest, nil, "invalid request: "+err.Error())
		}
	}
	return cloudOutcome(connDeleteCore(org, req.Provider))
}

func zapConnectionsUsageCloud(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	org, err := zapConnectionOrg(auth)
	if err != nil {
		return object.BuildCloudResponse(http.StatusUnauthorized, nil, err.Error())
	}
	var req struct {
		Provider string `json:"provider"`
		From     string `json:"from"`
		To       string `json:"to"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return object.BuildCloudResponse(http.StatusBadRequest, nil, "invalid request: "+err.Error())
		}
	}
	return cloudOutcome(connUsageCore(ctx, org, req.Provider, req.From, req.To))
}
