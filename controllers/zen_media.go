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

package controllers

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/account"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
	"github.com/hanzoai/decimal"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/money"
)

// The zen media plane on ai's side: forward an image/audio/rerank request to the
// zen service and bill it per unit at the discovered retail price. It mirrors the
// chat pipeToZen discipline (reserve → forward → settle from discovery) but meters
// per unit — images, calls — not per token. zen owns the SKU→upstream mapping,
// identity, and serving; ai authenticates, meters, and forwards.

// unitCostCents is the exact retail cost of `units` of a media SKU (images, calls)
// at the discovered headline per-unit price, in ai's cent ledger unit. Media prices
// are per unit, not per MTok — the same money/decimal path as costCents without the
// ÷1e6. A one-cent floor keeps a served call from ever billing zero.
func (m zenModel) unitCostCents(units int) int64 {
	usd := m.Base.In.Mul(decimal.New(int64(units), 0))
	cents := money.New(usd, money.USD).Minor().Int64()
	if cents <= 0 && units > 0 {
		cents = 1
	}
	return cents
}

// serveZenMedia reserves the per-unit cost, forwards the request to zen, and
// settles at the discovered price. It is the one Zen branch each media handler
// calls: apiPath is zen's endpoint ("images/generations", "audio/voice",
// "rerank"); units is the billable quantity (image count, else one call).
func (c *ApiController) serveZenMedia(apiPath, model string, rawBody []byte, units int, orgId string, authUser *iam.User, isPremium bool, start time.Time) {
	var hold *budgetHold
	if authUser != nil {
		if zm, ok := zenFam.lookup(model); ok {
			subject := account.Payer(account.Credential{Owner: authUser.Owner, Name: authUser.Name}).Subject()
			var ok2 bool
			if hold, ok2 = reserveBudget(subject, zm.unitCostCents(units)); !ok2 {
				c.ResponseAuthError(billingError("%s", object.InsufficientBalance(authUser.Owner, "cost").Message))
				return
			}
		}
	}
	defer hold.settle(0)
	c.pipeZenMedia(apiPath, model, rawBody, units, orgId, authUser, isPremium, hold, start)
}

// pipeZenMedia forwards rawBody to zen and relays the upstream bytes verbatim
// (image JSON, audio bytes) with the upstream content type, then settles the hold
// at the discovered per-unit price. Non-streaming — media is one buffered response.
func (c *ApiController) pipeZenMedia(apiPath, model string, rawBody []byte, units int, orgId string, authUser *iam.User, isPremium bool, hold *budgetHold, start time.Time) {
	prov := object.ZenProvider()
	if prov == nil {
		c.zenError("openai", "zen service is not configured", http.StatusServiceUnavailable)
		return
	}
	reqID := util.GenerateUUID()
	req, err := http.NewRequestWithContext(c.Ctx.Request.Context(), http.MethodPost, prov.ProviderUrl+"/v1/"+apiPath, bytes.NewReader(rawBody))
	if err != nil {
		c.zenError("openai", "build zen request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if a := c.Ctx.Request.Header.Get("Accept"); a != "" {
		req.Header.Set("Accept", a)
	}
	if prov.ClientSecret != "" {
		req.Header.Set("Authorization", "Bearer "+prov.ClientSecret)
	}
	// Tenant attribution: zen needs a billable tenant, and ai — which settles the
	// ledger — tells zen it fronts this call so zen meters without double-charging.
	if orgId != "" {
		req.Header.Set("X-Org-Id", orgId)
	} else if authUser != nil {
		req.Header.Set("X-Org-Id", authUser.Owner)
	}
	req.Header.Set("X-Hanzo-Fronted-By", "ai")

	resp, err := zenPipeClient.Do(req)
	if err != nil {
		c.recordZenMediaUsage(model, authUser, isPremium, reqID, units, start, hold, "error", err.Error())
		c.zenError("openai", "zen request failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	b, rErr := io.ReadAll(resp.Body)
	if rErr != nil {
		c.zenError("openai", "read zen response: "+rErr.Error(), http.StatusBadGateway)
		return
	}
	if resp.StatusCode != http.StatusOK {
		// A failed call is never charged: the deferred settle(0) releases the hold.
		c.zenError("openai", upstreamErrorMessage(b), resp.StatusCode)
		return
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	c.Ctx.Output.Header("Content-Type", ct)
	c.Ctx.Output.Body(b)
	c.recordZenMediaUsage(model, authUser, isPremium, reqID, units, start, hold, "success", "")
	c.EnableRender = false
}

// recordZenMediaUsage settles the budget hold at the discovered per-unit price and
// records the served (or failed) call for attribution. The discovered price is the
// billing source of truth; a model absent from the cache settles to zero (released).
func (c *ApiController) recordZenMediaUsage(model string, authUser *iam.User, isPremium bool, reqID string, units int, start time.Time, hold *budgetHold, status, errMsg string) {
	var cents int64
	if status == "success" {
		if zm, ok := zenFam.lookup(model); ok {
			cents = zm.unitCostCents(units)
		}
	}
	hold.settle(cents)
	if authUser == nil {
		return
	}
	rec := &usageRecord{
		Owner: authUser.Owner, User: authUser.Owner + "/" + authUser.Name, Organization: authUser.Owner,
		Model: model, Provider: "zen",
		Cost: float64(cents) / 100.0, Currency: "USD",
		Premium: isPremium, Status: status, ErrorMsg: errMsg,
		ClientIP: c.Ctx.Request.RemoteAddr, RequestID: reqID, Account: "hanzo",
	}
	recordUsage(rec)
	recordTrace(c.Ctx.Request.Context(), rec, start)
}

// AudioMedia serves the generative audio verbs — /v1/audio/voice (TTS), /music,
// /foley — that the Zen family serves natively. It resolves the SKU and, for a Zen
// model, forwards to zen's matching verb billed per call at the discovered price.
// These verbs are Zen-native; a non-Zen model is rejected.
func (c *ApiController) AudioMedia() {
	token, ok := c.bearerToken()
	if !ok {
		return
	}
	if isPublishableKey(token) {
		c.rejectPublishableKey()
		return
	}
	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, &req); err != nil || req.Model == "" {
		if authErr := c.authenticate(token); authErr != nil {
			c.ResponseAuthError(authErr)
			return
		}
		c.ResponseErrorWithStatus(http.StatusBadRequest, "audio request requires a \"model\" field")
		return
	}
	verb := ""
	switch p := c.Ctx.Request.URL.Path; {
	case strings.HasSuffix(p, "/voice"):
		verb = "voice"
	case strings.HasSuffix(p, "/music"):
		verb = "music"
	case strings.HasSuffix(p, "/foley"):
		verb = "foley"
	default:
		c.ResponseErrorWithStatus(http.StatusNotFound, "unknown audio verb")
		return
	}
	startTime := time.Now().UTC()
	orgId := c.GetOrg()
	provider, authUser, _, isPremium, _, err := c.authResolveProvider(token, req.Model, orgId)
	if err != nil {
		c.ResponseAuthError(err)
		return
	}
	if provider.Type != "Zen" {
		c.ResponseError("model \"" + req.Model + "\" does not serve the /v1/audio/" + verb + " endpoint")
		return
	}
	c.serveZenMedia("audio/"+verb, req.Model, c.Ctx.Input.RequestBody, 1, orgId, authUser, isPremium, startTime)
}
