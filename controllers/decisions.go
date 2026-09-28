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
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	iam "github.com/hanzoai/ai/internal/iam"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/upstream"
)

// POST /v1/decisions is Hanzo Decision's public API, on the Decisions API wire: a
// request names a model, a state and typed questions about it, and the answer is
// one typed answer per question with calibrated probabilities.
//
// ai does not decide. The decision service (object.KaiProvider, KAI_URL) answers;
// ai authenticates on the one auth + routing policy (authResolveProvider), gates
// on the balance, forwards the body unchanged, returns the answer unchanged, and
// meters the call. The models are the routes to that service (conf/models.yaml,
// provider kai): kai, and the Jev ids the service forwards to OpenRouter. A
// decision bills its input tokens at the model's price (decisionCostNano).

// decisionsRequest is the body POST /v1/decisions reads. The handler reads only
// the model and forwards the rest verbatim; this is the shape it forwards.
type decisionsRequest struct {
	// Model is kai, typesafe/jev-1.13 or ~typesafe/jev-latest.
	Model string `json:"model"`
	// State is what the questions are about: a string, an object or an array.
	State json.RawMessage `json:"state"`
	// Questions are keyed by name; each answer comes back under the same name.
	Questions map[string]decisionsQuestion `json:"questions"`
	Provider  json.RawMessage              `json:"provider,omitempty"`
	SessionID string                       `json:"session_id,omitempty"`
	User      string                       `json:"user,omitempty"`
	Trace     json.RawMessage              `json:"trace,omitempty"`
}

// decisionsQuestion is one typed question.
type decisionsQuestion struct {
	// Type is choice (pick one label), noul (does the statement hold) or score
	// (pick a level, index 0 first).
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	// Criteria: for choice, {label: description} or [label, ...]; for noul,
	// {"true": description, "false": description}; for score, [level, ...].
	Criteria json.RawMessage `json:"criteria,omitempty"`
	// Labels rename a noul's sides: {"false": "...", "true": "..."}.
	Labels map[string]string `json:"labels,omitempty"`
}

// decisionsResponse is what POST /v1/decisions answers: the Decisions API
// response, and Hanzo's additions (routing, state_hash, latency_ms) that a
// Decisions client ignores.
type decisionsResponse struct {
	ID        string                     `json:"id"`
	Model     string                     `json:"model"`
	Provider  string                     `json:"provider"`
	Answers   map[string]decisionsAnswer `json:"answers"`
	Usage     decisionsUsage             `json:"usage"`
	Routing   *decisionsRouting          `json:"routing,omitempty"`
	StateHash string                     `json:"state_hash,omitempty"`
	LatencyMs float64                    `json:"latency_ms,omitempty"`
}

// decisionsAnswer is one question's answer. Type names which of noul, choice or
// score is set.
type decisionsAnswer struct {
	Type string `json:"type"`
	// Noul is P(true).
	Noul *float64 `json:"noul,omitempty"`
	// Choice is the chosen label.
	Choice string `json:"choice,omitempty"`
	// Score is the expected level, Σ i·p_i.
	Score *float64 `json:"score,omitempty"`
	// Confidence is (n·p_max − 1)/(n − 1): 0 when every option is equally likely.
	Confidence *float64 `json:"confidence,omitempty"`
	// Probabilities are keyed by label (choice) or by level index "0", "1", …
	// (score).
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	// Legend is a score's level descriptions, keyed "0", "1", ….
	Legend map[string]json.RawMessage `json:"legend,omitempty"`
	// AnswerConfidence is the calibrated probability of the reported answer.
	AnswerConfidence *float64         `json:"answer_confidence,omitempty"`
	Action           *decisionsAction `json:"action,omitempty"`
}

// decisionsAction is the act head's view of an answer.
type decisionsAction struct {
	// ActProbability is the probability of acting on the answer rather than
	// escalating it.
	ActProbability float64 `json:"act_probability"`
}

// decisionsUsage is what a decision consumed. Cost, when present, is what the
// upstream charged for it in USD.
type decisionsUsage struct {
	InputTokens  int      `json:"input_tokens"`
	OutputTokens int      `json:"output_tokens"`
	Cost         *float64 `json:"cost,omitempty"`
}

// decisionsRouting says which checkpoint answered.
type decisionsRouting struct {
	Backend     string `json:"backend"`
	Checkpoint  string `json:"checkpoint"`
	Revision    string `json:"revision,omitempty"`
	Sha256      string `json:"sha256,omitempty"`
	Calibration string `json:"calibration,omitempty"`
	Device      string `json:"device,omitempty"`
	Upstream    string `json:"upstream,omitempty"`
	Reason      string `json:"reason"`
}

// decisionsFailure is the decision service's error body, {"error":{"code","message"}}.
// ai answers its own refusals in it, so a caller reads one shape whoever refused.
func decisionsFailure(code int, message string) []byte {
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"code": code, "message": message}})
	return b
}

// decisionRefusal is a status and a decisionsFailure body.
type decisionRefusal struct {
	status int
	body   []byte
}

func refuseDecision(code int, format string, args ...any) *decisionRefusal {
	return &decisionRefusal{status: code, body: decisionsFailure(code, fmt.Sprintf(format, args...))}
}

// decisionModels are the ids /v1/decisions serves: every route to the decision
// service, sorted — the list the service itself names when it refuses a model.
func decisionModels() []string {
	var ids []string
	if cfg := GetModelConfig(); cfg != nil {
		cfg.mu.RLock()
		for id, r := range cfg.routes {
			if r.providerName == object.KaiName {
				ids = append(ids, id)
			}
		}
		cfg.mu.RUnlock()
	} else {
		for id, r := range modelRoutes {
			if r.providerName == object.KaiName {
				ids = append(ids, id)
			}
		}
	}
	sort.Strings(ids)
	return ids
}

// decisionModel reads the model a decisions body names, and refuses — in the
// service's own words and shape — a body with none, or a model whose route does
// not go to the decision service. A model the service knows and does not publish
// is unknown here, so it is never forwarded.
func decisionModel(body []byte) (string, *decisionRefusal) {
	var head struct {
		Model *string `json:"model"`
	}
	if err := json.Unmarshal(body, &head); err != nil {
		return "", refuseDecision(http.StatusBadRequest, "body: %s", err.Error())
	}
	if head.Model == nil {
		return "", refuseDecision(http.StatusBadRequest, "the request needs a 'model'")
	}
	model := *head.Model
	if r := resolveModelRoute(model); r == nil || r.providerName != object.KaiName {
		return "", refuseDecision(http.StatusBadRequest, "unknown model %q; use one of %s", model, strings.Join(decisionModels(), ", "))
	}
	return model, nil
}

// decisionsClient carries every call to the decision service. A decision is one
// forward pass, or one upstream call for a forwarded model.
var decisionsClient = &http.Client{Timeout: 120 * time.Second}

// decide forwards body unchanged to the decision service and returns the
// service's status and bytes unchanged, with the usage a 200 answer reports. A
// refusal is ai's own: the service is not configured, or could not be reached.
func decide(ctx context.Context, kai *object.Provider, body []byte) (int, []byte, decisionsUsage, *decisionRefusal) {
	if kai == nil {
		return 0, nil, decisionsUsage{}, refuseDecision(http.StatusServiceUnavailable, "the decision service is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(kai.ProviderUrl, "/")+"/v1/decisions", bytes.NewReader(body))
	if err != nil {
		return 0, nil, decisionsUsage{}, refuseDecision(http.StatusInternalServerError, "build decision request: %s", err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	upstream.Authorize(req, kai)
	resp, err := decisionsClient.Do(req)
	if err != nil {
		return 0, nil, decisionsUsage{}, refuseDecision(http.StatusBadGateway, "decision service: %s", err.Error())
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, decisionsUsage{}, refuseDecision(http.StatusBadGateway, "decision service: %s", err.Error())
	}
	var answer decisionsResponse
	if resp.StatusCode == http.StatusOK {
		_ = json.Unmarshal(b, &answer)
	}
	return resp.StatusCode, b, answer.Usage, nil
}

// decisionRecord is the row a served decision files: one call at the per-call
// price, the tokens the answer reports, and — when the answer states it — what the
// call cost upstream, as the exact COGS.
func decisionRecord(ctx context.Context, ledger string, authUser *iam.User, model string, kai *object.Provider, isPremium bool, u decisionsUsage) *usageRecord {
	rec := &usageRecord{
		Owner:            ledger,
		Organization:     authUser.Owner,
		Model:            model,
		Provider:         kai.Name,
		Origin:           kai.Origin(),
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.InputTokens + u.OutputTokens,
		DecisionCount:    1,
		Currency:         "USD",
		Premium:          isPremium,
		Status:           "success",
		RequestID:        uuid.NewString(),
		Account:          "hanzo",
	}
	if u.Cost != nil {
		cost := usdToNano(*u.Cost)
		rec.CostNanoExact = &cost
	}
	rec.bind(ctx, authUser)
	return rec
}

// Decisions implements POST /v1/decisions (the Decisions API).
//
// Body: {"model": "kai", "state": "..."|{...}|[...], "questions": {"<name>":
// {"type": "choice"|"noul"|"score", "instructions": "...", "criteria": ...}}}
//
// Response: {"id","model","provider","answers":{"<name>":{"type",...}},
// "usage":{"input_tokens","output_tokens"}}
//
// The body goes to the decision service unchanged and its answer comes back
// unchanged, errors included ({"error":{"code","message"}}). An unknown model is
// refused here in that shape, without a call. Billed per call, like /v1/rerank.
func (c *ApiController) Decisions() {
	token, ok := c.bearerToken()
	if !ok {
		return
	}
	if isPublishableKey(token) {
		c.rejectPublishableKey()
		return
	}

	model, bad := decisionModel(c.Body())
	if bad != nil {
		// Authenticate before reporting the client error: an invalid credential is
		// 401 regardless of body validity (never a probe-able 400).
		if authErr := c.authenticate(token); authErr != nil {
			c.ResponseAuthError(authErr)
			return
		}
		c.decisionsReply(bad.status, bad.body)
		return
	}

	startTime := time.Now().UTC()
	_, authUser, _, isPremium, err := c.authResolveProvider(token, model, c.GetOrg())
	if err != nil {
		c.ResponseAuthError(err)
		return
	}

	// Reserve the call's price, as the rerank media pipe does. Whatever way this
	// ends, the hold is released; a served call settles it at the price first.
	var hold *budgetHold
	ledger := c.billingOrg(authUser)
	if authUser != nil {
		var admitted bool
		if hold, admitted = reserveBudget(authUser.PayerSubject(ledger), decisionCostCents(model, 1)); !admitted {
			c.ResponseAuthError(billingError("%s", object.InsufficientBalance(c.Host(), ledger, "cost").Message))
			return
		}
	}
	defer hold.settle(0)

	kai := object.KaiProvider()
	status, body, usage, fault := decide(c.Context(), kai, c.Body())
	if fault != nil {
		c.decisionsReply(fault.status, fault.body)
		return
	}
	if status == http.StatusOK && authUser != nil {
		hold.settle(decisionCostCents(model, 1))
		rec := decisionRecord(c.Context(), ledger, authUser, model, kai, isPremium, usage)
		rec.ClientIP = c.Fiber().IP()
		recordUsage(rec)
		recordTrace(c.Context(), rec, startTime)
	}
	c.decisionsReply(status, body)
}

// decisionsReply writes a decisions body with its status and disables the
// router's auto-render.
func (c *ApiController) decisionsReply(status int, body []byte) {
	c.SetHeader("Content-Type", "application/json")
	c.Bytes(status, body)
}
