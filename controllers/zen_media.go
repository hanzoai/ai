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
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/upstream"
	"github.com/luxfi/zap"
)

// The zen media plane on ai's side: forward an image/audio/video/rerank request to
// the zen service and bill it per unit at the discovered retail price. It mirrors the
// chat pipeToFamily discipline (reserve → forward → settle from discovery) but meters
// per unit — images, calls — not per token. zen owns the SKU→upstream mapping,
// identity, and serving; ai authenticates, meters, and forwards.

// media is what one media call relayed to zen answered: the status and the bytes to
// write, or the reason it could not be served. short is a wallet that could not cover
// the call's price, which the caller words in its own transport.
type media struct {
	status int
	ct     string
	body   []byte
	msg    string
	short  bool
}

// zenMedia relays one buffered media call to the zen family and bills it at the
// discovered per-unit price: reserve the price, forward, settle exactly what was
// served. It is the ONE media relay — the HTTP handlers (serveZenMedia) and their ZAP
// twins answer with what it returns. apiPath is zen's endpoint ("images/generations",
// "audio/voice", "rerank", …); units the billable quantity (image count, else one
// call); org the tenant zen is told (X-Org-Id); ctx bounds the upstream call and w
// is the request's record.
func zenMedia(ctx context.Context, w whence, apiPath, model string, raw []byte, units int, org, accept string, u *iam.User, premium bool, start time.Time) media {
	var hold *budgetHold
	if u != nil {
		if zm, ok := zenFam.lookup(model); ok {
			var ok2 bool
			if hold, ok2 = reserveFor(w.ctx, u.PayerSubject(w.ledger), centsUp(zm.unitNano(units))); !ok2 {
				return media{status: http.StatusPaymentRequired, short: true}
			}
		}
	}
	defer hold.settle(0)

	prov := object.ZenProvider()
	if prov == nil {
		return media{status: http.StatusServiceUnavailable, msg: "zen service is not configured"}
	}
	reqID := uuid.NewString()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, prov.ProviderUrl+"/v1/"+apiPath, bytes.NewReader(raw))
	if err != nil {
		return media{status: http.StatusInternalServerError, msg: "build zen request: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	upstream.Authorize(req, prov)
	// Tenant attribution: zen needs a billable tenant, and ai — which settles the
	// ledger — tells zen it fronts this call so zen meters without double-charging.
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	req.Header.Set("X-Hanzo-Fronted-By", "ai")

	resp, err := zenPipeClient.Do(req)
	if err != nil {
		recordMedia(w, model, u, premium, reqID, units, serving{}, start, hold, "error", err.Error())
		return media{status: http.StatusBadGateway, msg: "zen request failed: " + err.Error()}
	}
	defer resp.Body.Close()
	b, rErr := io.ReadAll(resp.Body)
	if rErr != nil {
		return media{status: http.StatusBadGateway, msg: "read zen response: " + rErr.Error()}
	}
	sv := servingOf(resp.Header)
	if resp.StatusCode != http.StatusOK {
		// A failed call is never charged: the deferred settle(0) releases the hold.
		// The arms zen asked before it gave up are on the books all the same.
		failed(w, zenFam, model, sv.failover, u, premium, false, reqID, start)
		return media{status: resp.StatusCode, msg: upstreamErrorMessage(b)}
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	recordMedia(w, model, u, premium, reqID, units, sv, start, hold, "success", "")
	return media{status: http.StatusOK, ct: ct, body: b}
}

// recordMedia settles the hold at the discovered per-unit price, exactly — a rerank
// call worth a tenth of a cent bills a tenth of a cent — and records the served (or
// failed) call. A rung zen says bills nothing (X-Hanzo-Free) bills nothing; a model
// absent from the catalog settles to zero (released). The unit price is the charge,
// carried as the exact billed amount every reader of the money asks (usageCostNano);
// what the call cost upstream is zen's own statement (cogsHeader).
func recordMedia(w whence, model string, u *iam.User, premium bool, reqID string, units int, sv serving, start time.Time, hold *budgetHold, status, errMsg string) {
	var nano int64
	if status == "success" && !sv.free {
		if zm, ok := zenFam.lookup(model); ok {
			nano = zm.unitNano(units)
		}
	}
	hold.settleNano(nano)
	if u == nil {
		return
	}
	rec := &usageRecord{
		Owner: w.ledger, Organization: u.Owner,
		Model: model, Provider: "zen", Free: sv.free,
		Served: sv.arm, Vendor: sv.vendor, Failover: sv.failover,
		Cost: float64(nano) / 1e9, Currency: "USD",
		Premium: premium, Status: status, ErrorMsg: errMsg,
		ClientIP: w.ip, RequestID: reqID, Account: "hanzo",
		TotalTokens: units, BilledNanoExact: &nano, CostNanoExact: sv.cogs,
	}
	rec.bind(w.ctx, u)
	recordUsage(rec)
	recordTrace(w.ctx, rec, start)
	if status == "success" {
		failed(w, zenFam, model, sv.failover, u, premium, false, reqID, start)
	}
}

// serveZenMedia is the one Zen branch each media handler calls: zenMedia for this
// request, written back as the HTTP answer.
func (c *ApiController) serveZenMedia(apiPath, model string, rawBody []byte, units int, orgId string, authUser *iam.User, isPremium bool, start time.Time) {
	w := whence{ledger: c.billingOrg(authUser), ip: c.Fiber().IP(), ctx: c.Context()}
	m := zenMedia(c.Context(), w, apiPath, model, rawBody, units, tenant(c.Context(), orgId, authUser), c.Header("Accept"), authUser, isPremium, start)
	switch {
	case m.short:
		c.ResponseAuthError(billingError("%s", object.InsufficientBalance(c.Host(), w.ledger, "cost").Message))
	case m.status != http.StatusOK:
		c.zenError("openai", m.msg, m.status)
	default:
		c.SetHeader("Content-Type", m.ct)
		_ = c.Bytes(http.StatusOK, m.body)
	}
}

// zapServeZenMedia is serveZenMedia over ZAP: no writer to hold, the answer is one
// cloud response.
func zapServeZenMedia(apiPath, mdl string, rawBody []byte, units int, authUser *iam.User, isPremium bool, start time.Time) (*zap.Message, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 130*time.Second)
	defer cancel()
	w := whence{ctx: context.Background()}
	org := ""
	if authUser != nil {
		w.ledger, org = authUser.Owner, authUser.Owner
	}
	m := zenMedia(ctx, w, apiPath, mdl, rawBody, units, org, "", authUser, isPremium, start)
	switch {
	case m.short:
		return object.BuildCloudResponse(402, nil, object.InsufficientBalance(zapBrandHost, w.ledger, "cost").Message)
	case m.status != http.StatusOK:
		return object.BuildCloudResponse(uint32(m.status), nil, m.msg)
	}
	return object.BuildCloudResponse(200, m.body, "")
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
	if err := json.Unmarshal(c.Body(), &req); err != nil || req.Model == "" {
		if authErr := c.authenticate(token); authErr != nil {
			c.ResponseAuthError(authErr)
			return
		}
		c.ResponseErrorWithStatus(http.StatusBadRequest, "audio request requires a \"model\" field")
		return
	}
	verb := ""
	switch p := c.Path(); {
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
	provider, authUser, _, isPremium, err := c.authResolveProvider(token, req.Model, orgId)
	if err != nil {
		c.ResponseAuthError(err)
		return
	}
	if provider.Type != "Zen" {
		c.ResponseError("model \"" + req.Model + "\" does not serve the /v1/audio/" + verb + " endpoint")
		return
	}
	c.serveZenMedia("audio/"+verb, req.Model, c.Body(), 1, orgId, authUser, isPremium, startTime)
}
