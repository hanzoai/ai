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
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	iam "github.com/hanzoai/ai/internal/iam"

	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/upstream"
)

// POST /v1/decisions is Hanzo Decision's public API, on the Decisions API wire: a
// request names a model, a state and typed questions about it, and the answer is
// one typed answer per question with calibrated probabilities. POST /v1/systemone
// is the same call on Jev's wire, for a Jev client moved to Kai by its base URL:
// Jev's request and answer shapes, and refusals in FastAPI's {"detail": ...}.
//
// ai does not decide. The decision service (object.KaiProvider, KAI_URL) answers
// both paths; ai authenticates on the one auth + routing policy, reserves the
// call's price against the org that pays, names every handle by that org, forwards
// the body under the id the model's route names upstream, answers, and files the
// debit once the answer has gone. The models are the routes to that service
// (conf/models.yaml, provider kai): kai, and the Jev ids the service forwards to
// OpenRouter, which /v1/decisions serves and /v1/systemone does not. A decision
// bills its input tokens at the model's price (decisionCostNano).

const (
	decisionsPath = "/v1/decisions"
	systemonePath = "/v1/systemone"
)

// decisionContent is Content on the decision wire: a string, an object or an
// array, kept as written. It and the three types like it below marshal as
// json.RawMessage does — they are that type under a name that states its schema.
type decisionContent json.RawMessage

// Schema is Content as JSON Schema states it.
func (decisionContent) Schema() map[string]any {
	return map[string]any{"anyOf": []any{
		map[string]any{"type": "string"},
		map[string]any{"type": "object"},
		map[string]any{"type": "array"},
	}}
}

func (c decisionContent) MarshalJSON() ([]byte, error) { return json.RawMessage(c).MarshalJSON() }
func (c *decisionContent) UnmarshalJSON(b []byte) error {
	return (*json.RawMessage)(c).UnmarshalJSON(b)
}

// decisionOption is what a choice says about one label: Content, or null for a
// label that speaks for itself.
type decisionOption json.RawMessage

// Schema is Content or null.
func (decisionOption) Schema() map[string]any {
	return map[string]any{"anyOf": []any{
		map[string]any{"type": "string"},
		map[string]any{"type": "object"},
		map[string]any{"type": "array"},
		map[string]any{"type": "null"},
	}}
}

func (c decisionOption) MarshalJSON() ([]byte, error)  { return json.RawMessage(c).MarshalJSON() }
func (c *decisionOption) UnmarshalJSON(b []byte) error { return (*json.RawMessage)(c).UnmarshalJSON(b) }

// decisionsRequest is the body POST /v1/decisions reads. The handler reads the
// model, names the handle, and forwards the rest verbatim; this is the shape it
// forwards. A request over a handle carries neither state nor questions: they are
// the ones observed.
type decisionsRequest struct {
	// Model is kai, typesafe/jev-1.13 or ~typesafe/jev-latest.
	Model string `json:"model" validate:"required"`
	// State is what the questions are about: a string, an object or an array.
	State decisionContent `json:"state,omitempty"`
	// Questions are keyed by name; each answer comes back under the same name.
	Questions map[string]decisionsQuestion `json:"questions,omitempty" validate:"min=1,max=100"`
	// Observe holds the state under this id, for later decisions over it.
	Observe string `json:"observe,omitempty"`
	// Handle decides over the state observed under this id, by this org.
	Handle    string          `json:"handle,omitempty"`
	Provider  json.RawMessage `json:"provider,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	User      string          `json:"user,omitempty"`
	Trace     json.RawMessage `json:"trace,omitempty"`
}

// decisionsQuestion is one typed question: a noul, a choice or a score, named by
// its type.
type decisionsQuestion struct{}

// Variants are the three kinds of question.
func (decisionsQuestion) Variants() (string, []any) {
	return "type", []any{decisionsNoul{}, decisionsChoice{}, decisionsScore{}}
}

// decisionsNoul asks whether a statement holds.
type decisionsNoul struct {
	Type         string          `json:"type" validate:"required" enum:"noul"`
	Instructions decisionContent `json:"instructions,omitempty"`
	Criteria     *decisionSides  `json:"criteria,omitempty"`
	// Labels rename the sides: {"false": "...", "true": "..."}.
	Labels map[string]string `json:"labels,omitempty"`
}

// decisionSides say what counts as true and what counts as false.
type decisionSides struct {
	True  decisionContent `json:"true,omitempty"`
	False decisionContent `json:"false,omitempty"`
}

// decisionsChoice picks one label. The labels are bounded by the request's token
// budget, not by a count.
type decisionsChoice struct {
	Type         string                    `json:"type" validate:"required" enum:"choice"`
	Instructions decisionContent           `json:"instructions,omitempty"`
	Criteria     map[string]decisionOption `json:"criteria" validate:"required,min=2"`
}

// decisionsScore picks a level; level i scores i.
type decisionsScore struct {
	Type         string            `json:"type" validate:"required" enum:"score"`
	Instructions decisionContent   `json:"instructions,omitempty"`
	Criteria     []decisionContent `json:"criteria" validate:"required,min=1"`
}

// decisionsResponse is what POST /v1/decisions answers: the Decisions API
// response, and Hanzo's additions (routing, state_hash, latency_ms) that a
// Decisions client ignores.
type decisionsResponse struct {
	ID        string                     `json:"id" validate:"required"`
	Model     string                     `json:"model" validate:"required"`
	Provider  string                     `json:"provider" validate:"required"`
	Answers   map[string]decisionsAnswer `json:"answers" validate:"required"`
	Usage     decisionsUsage             `json:"usage" validate:"required"`
	Routing   *decisionsRouting          `json:"routing" validate:"required"`
	StateHash string                     `json:"state_hash" validate:"required"`
	LatencyMs float64                    `json:"latency_ms" validate:"required"`
}

// decisionsAnswer is one question's answer. Type names which of noul, choice or
// score is set.
type decisionsAnswer struct {
	Type string `json:"type" validate:"required" enum:"noul,choice,score"`
	// Noul is P(true).
	Noul *float64 `json:"noul,omitempty"`
	// Choice is the chosen label.
	Choice string `json:"choice,omitempty"`
	// Score is the expected level, Σ i·p_i.
	Score *float64 `json:"score,omitempty"`
	// Confidence is |2p − 1| for a noul, (n·p_max − 1)/(n − 1) otherwise: 0 when
	// every option is equally likely.
	Confidence *float64 `json:"confidence,omitempty"`
	// Probabilities are keyed by label (choice) or by level index "0", "1", …
	// (score).
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	// Legend is a score's level descriptions, keyed "0", "1", ….
	Legend map[string]decisionContent `json:"legend,omitempty"`
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

// decisionsUsage is what a decision consumed. InputTokens is the billed count: the
// request's text counted once in Kai's tokenizer — the state once, each question's
// instructions and options once; a decision over a handle bills the state once,
// when it is observed. Cost, when present, is what the upstream charged in USD.
type decisionsUsage struct {
	InputTokens  int      `json:"input_tokens" validate:"required"`
	OutputTokens int      `json:"output_tokens" validate:"required"`
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

// decisionsRefused is a refusal on /v1/decisions, the service's error body.
type decisionsRefused struct {
	Error decisionsReason `json:"error" validate:"required"`
}

// decisionsReason is what refused and why. Code is the status, or a name such as
// state_too_long where the refusal has one.
type decisionsReason struct {
	Code    decisionCode `json:"code" validate:"required"`
	Message string       `json:"message" validate:"required"`
}

// decisionCode is a refusal's code: its status, or its name.
type decisionCode json.RawMessage

// Schema is an integer or a string.
func (decisionCode) Schema() map[string]any {
	return map[string]any{"anyOf": []any{map[string]any{"type": "integer"}, map[string]any{"type": "string"}}}
}

func (c decisionCode) MarshalJSON() ([]byte, error)  { return json.RawMessage(c).MarshalJSON() }
func (c *decisionCode) UnmarshalJSON(b []byte) error { return (*json.RawMessage)(c).UnmarshalJSON(b) }

// systemoneRequest is the body POST /v1/systemone reads: Jev's request.
type systemoneRequest struct {
	// Model is kai, or its versioned id.
	Model     string                       `json:"model" validate:"required"`
	State     decisionContent              `json:"state" validate:"required"`
	Questions map[string]systemoneQuestion `json:"questions" validate:"required,min=1,max=100"`
}

// systemoneQuestion is one of Jev's three questions.
type systemoneQuestion struct{}

// Variants are the three kinds of question, at Jev's limits.
func (systemoneQuestion) Variants() (string, []any) {
	return "type", []any{systemoneNoul{}, systemoneChoice{}, systemoneScore{}}
}

// systemoneNoul asks whether a statement holds.
type systemoneNoul struct {
	Type         string          `json:"type" validate:"required" enum:"noul"`
	Instructions decisionContent `json:"instructions,omitempty"`
	Criteria     *decisionSides  `json:"criteria,omitempty"`
}

// systemoneChoice picks one of 2 to 255 labels.
type systemoneChoice struct {
	Type         string                    `json:"type" validate:"required" enum:"choice"`
	Instructions decisionContent           `json:"instructions,omitempty"`
	Criteria     map[string]decisionOption `json:"criteria" validate:"required,min=2,max=255"`
}

// systemoneScore picks one of 1 to 10 levels; level i scores i.
type systemoneScore struct {
	Type         string            `json:"type" validate:"required" enum:"score"`
	Instructions decisionContent   `json:"instructions,omitempty"`
	Criteria     []decisionContent `json:"criteria" validate:"required,min=1,max=10"`
}

// systemoneResponse is Jev's answer, and nothing beside it.
type systemoneResponse struct {
	// Model is the versioned id of the model that answered.
	Model   string                     `json:"model" validate:"required"`
	Answers map[string]systemoneAnswer `json:"answers" validate:"required"`
	Usage   systemoneUsage             `json:"usage" validate:"required"`
}

// systemoneAnswer is one of Jev's answers. Type names which of noul, choice or
// score is set.
type systemoneAnswer struct {
	Type   string   `json:"type" validate:"required" enum:"noul,choice,score"`
	Noul   *float64 `json:"noul,omitempty"`
	Choice string   `json:"choice,omitempty"`
	Score  *float64 `json:"score,omitempty"`
	// Confidence is (n·p_max − 1)/(n − 1) for a choice, and Jev's
	// 1 − E|i − mode| / MAD(uniform) for a score.
	Confidence    *float64                   `json:"confidence,omitempty"`
	Legend        map[string]decisionContent `json:"legend,omitempty"`
	Probabilities map[string]float64         `json:"probabilities,omitempty"`
}

// systemoneUsage is the billed count, as decisionsUsage counts it.
type systemoneUsage struct {
	InputTokens  int `json:"input_tokens" validate:"required"`
	OutputTokens int `json:"output_tokens" validate:"required"`
}

// systemoneRefused is a refusal on /v1/systemone: FastAPI's {"detail": "..."}.
type systemoneRefused struct {
	Detail string `json:"detail" validate:"required"`
}

// systemoneInvalid is FastAPI's 422: each field that failed, where, and why.
type systemoneInvalid struct {
	Detail []systemoneViolation `json:"detail" validate:"required"`
}

// systemoneViolation is one failed field.
type systemoneViolation struct {
	Loc  []decisionPlace `json:"loc" validate:"required"`
	Msg  string          `json:"msg" validate:"required"`
	Type string          `json:"type" validate:"required"`
}

// decisionPlace is one step of a location: a field name or an index.
type decisionPlace json.RawMessage

// Schema is a string or an integer.
func (decisionPlace) Schema() map[string]any {
	return map[string]any{"anyOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "integer"}}}
}

func (c decisionPlace) MarshalJSON() ([]byte, error)  { return json.RawMessage(c).MarshalJSON() }
func (c *decisionPlace) UnmarshalJSON(b []byte) error { return (*json.RawMessage)(c).UnmarshalJSON(b) }

// decisionsFailure is the decision service's error body, {"error":{"code","message"}}.
// ai answers its own refusals on /v1/decisions in it, so a caller reads one shape
// whoever refused.
func decisionsFailure(code int, message string) []byte {
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"code": code, "message": message}})
	return b
}

// systemoneFailure is FastAPI's error body, {"detail": "..."}: ai's refusals on
// /v1/systemone.
func systemoneFailure(message string) []byte {
	b, _ := json.Marshal(map[string]string{"detail": message})
	return b
}

// decisionRefusal is a status and a body in the path's own words.
type decisionRefusal struct {
	status int
	body   []byte
}

// refuseDecision is ai's refusal on /v1/decisions.
func refuseDecision(code int, format string, args ...any) *decisionRefusal {
	return &decisionRefusal{status: code, body: decisionsFailure(code, fmt.Sprintf(format, args...))}
}

// decline is ai's refusal on path, in that path's words.
func decline(path string, code int, message string) *decisionRefusal {
	if path == systemonePath {
		return &decisionRefusal{status: code, body: systemoneFailure(message)}
	}
	return &decisionRefusal{status: code, body: decisionsFailure(code, message)}
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

// kaiUpstream reports whether a route reaches Kai itself, in the service's own
// spelling: kai, or kai@<revision>. The Jev routes reach Jev.
func kaiUpstream(up string) bool {
	return up == "kai" || strings.HasPrefix(up, "kai@")
}

// decisionModel reads the model a body names on path, and refuses — in that path's
// words — a body with none, or a model the path does not serve. /v1/decisions
// serves every route to the decision service. /v1/systemone serves Kai alone: a Jev
// id is never mapped to Kai, so every Jev spelling is unknown there. A model the
// service knows and does not publish is unknown on both, so it is never forwarded.
func decisionModel(path string, body []byte) (string, *decisionRefusal) {
	var head struct {
		Model *string `json:"model"`
	}
	if err := json.Unmarshal(body, &head); err != nil {
		return "", decline(path, http.StatusBadRequest, "body: "+err.Error())
	}
	if head.Model == nil {
		if path == systemonePath {
			b, _ := json.Marshal(systemoneInvalid{Detail: []systemoneViolation{{
				Loc: []decisionPlace{decisionPlace(`"body"`), decisionPlace(`"model"`)}, Msg: "Field required", Type: "missing",
			}}})
			return "", &decisionRefusal{status: http.StatusUnprocessableEntity, body: b}
		}
		return "", refuseDecision(http.StatusBadRequest, "the request needs a 'model'")
	}
	model := *head.Model
	r := resolveModelRoute(model)
	if path == systemonePath {
		if r == nil || r.providerName != object.KaiName || !kaiUpstream(r.upstreamModel) {
			return "", decline(path, http.StatusBadRequest, "Unknown model: "+model)
		}
		return model, nil
	}
	if r == nil || r.providerName != object.KaiName {
		return "", refuseDecision(http.StatusBadRequest, "unknown model %q; use one of %s", model, strings.Join(decisionModels(), ", "))
	}
	return model, nil
}

// A handle belongs to the org that observed it. The service holds observed states
// by id, so ai names every id it forwards by the org that pays for the call —
// observe and handle become <org>/<id> — and an id one org chose can only ever
// reach that org's states. The prefix is ai's, never the caller's: it is taken off
// anything the service says back.

// handles are the ids a body names, as the caller wrote them.
type handles struct{ observe, handle string }

// scope names the body's handles by org. A body naming none goes out byte for
// byte; a handle that is not a string is refused.
func scope(path string, body []byte, org string) ([]byte, handles, *decisionRefusal) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || fields == nil {
		return body, handles{}, nil
	}
	var h handles
	named := false
	for key, id := range map[string]*string{"observe": &h.observe, "handle": &h.handle} {
		raw, ok := fields[key]
		if !ok || string(raw) == "null" {
			continue
		}
		if err := json.Unmarshal(raw, id); err != nil {
			return nil, handles{}, decline(path, http.StatusBadRequest, fmt.Sprintf("'%s' must be a string", key))
		}
		fields[key], _ = json.Marshal(org + "/" + *id)
		named = true
	}
	if !named {
		return body, handles{}, nil
	}
	// An org with a slash in its name would make <org>/<id> ambiguous. IAM names
	// never carry one; a principal whose does is refused rather than guessed at.
	if strings.Contains(org, "/") {
		return nil, handles{}, decline(path, http.StatusForbidden, "this organization cannot hold a handle")
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if enc.Encode(fields) != nil {
		return nil, handles{}, decline(path, http.StatusBadRequest, "body: not a JSON object")
	}
	return bytes.TrimRight(out.Bytes(), "\n"), h, nil
}

// unscope takes the org back off everything the service said about the body's
// handles: an id echoed on the answer, and every mention in a refusal. An answer
// that mentions none goes back byte for byte.
func unscope(status int, body []byte, org string, h handles) []byte {
	if h.observe == "" && h.handle == "" {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return body
	}
	prefix := org + "/"
	changed := false
	var walk func(any) any
	walk = func(x any) any {
		switch t := x.(type) {
		case string:
			if status >= http.StatusBadRequest && strings.Contains(t, prefix) {
				changed = true
				return strings.ReplaceAll(t, prefix, "")
			}
		case []any:
			for i := range t {
				t[i] = walk(t[i])
			}
		case map[string]any:
			for k := range t {
				t[k] = walk(t[k])
			}
		}
		return x
	}
	v = walk(v)
	if m, ok := v.(map[string]any); ok {
		for _, key := range []string{"observe", "handle"} {
			if s, ok := m[key].(string); ok && strings.HasPrefix(s, prefix) {
				m[key] = strings.TrimPrefix(s, prefix)
				changed = true
			}
		}
	}
	if !changed {
		return body
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if enc.Encode(v) != nil {
		return body
	}
	return bytes.TrimRight(out.Bytes(), "\n")
}

// decisionsClient carries every call to the decision service. A decision is one
// forward pass, or one upstream call for a forwarded model.
var decisionsClient = &http.Client{Timeout: 120 * time.Second}

// Relayed are the service's headers a caller is owed: the request id, and how long
// to wait before asking again.
var relayed = []string{"X-Request-Id", "Retry-After", "Retry-After-Ms"}

// decided is the service's reply to a body as sent, or ai's refusal to send it.
type decided struct {
	status int
	body   []byte
	header map[string]string
	usage  decisionsUsage
	sha256 string // routing.sha256 of an answer Kai gave in process
	fault  *decisionRefusal
	exp    time.Time
}

// consult sends body to path on the decision service, naming model by the id its
// route names upstream, and returns what the service said: a 200 on /v1/decisions
// names the model asked for, with the usage that answer reports. A refusal is ai's
// own: the service is not configured, or could not be reached.
func consult(ctx context.Context, kai *object.Provider, path, model, org, rid string, body []byte) decided {
	if kai == nil {
		return decided{fault: decline(path, http.StatusServiceUnavailable, "the decision service is not configured")}
	}
	up := model
	if r := resolveModelRoute(model); r != nil && r.upstreamModel != "" {
		up = r.upstreamModel
	}
	if up != model {
		if b, ok := WithModel(body, up); ok {
			body = b
		}
	}
	d := recall(ctx, kai, path, up, org, rid, body)
	if d.status == http.StatusOK && path == decisionsPath && up != model {
		if named, ok := WithModel(d.body, model); ok {
			d.body = named
		}
	}
	return d
}

// send posts body to path on the decision service, under the caller's request id,
// and reads its reply.
func send(ctx context.Context, kai *object.Provider, path, rid string, body []byte) decided {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(kai.ProviderUrl, "/")+path, bytes.NewReader(body))
	if err != nil {
		return decided{fault: decline(path, http.StatusInternalServerError, "build decision request: "+err.Error())}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-Id", rid)
	upstream.Authorize(req, kai)
	resp, err := decisionsClient.Do(req)
	if err != nil {
		return decided{fault: decline(path, http.StatusBadGateway, "decision service: "+err.Error())}
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return decided{fault: decline(path, http.StatusBadGateway, "decision service: "+err.Error())}
	}
	d := decided{status: resp.StatusCode, body: b, header: map[string]string{}}
	for _, k := range relayed {
		if v := resp.Header.Get(k); v != "" {
			d.header[k] = v
		}
	}
	if resp.StatusCode == http.StatusOK {
		var answer struct {
			Usage   decisionsUsage    `json:"usage"`
			Routing *decisionsRouting `json:"routing"`
		}
		_ = json.Unmarshal(b, &answer)
		d.usage = answer.Usage
		if r := answer.Routing; r != nil && r.Backend == "kai" {
			d.sha256 = r.Sha256
		}
	}
	return d
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

// decisionCall is one decision a principal asked for, resolved: the path and the
// model, the body as written, who is asking, and the org that pays — the one org
// the reservation, the debit and every handle name.
type decisionCall struct {
	path    string
	model   string
	body    []byte
	user    *iam.User
	ledger  string
	premium bool
	host    string
	ip      string
	rid     string
	start   time.Time
	// ctx outlives the reply, for the debit filed after it.
	ctx context.Context
}

// decisionReply is what a decision path answers: a status, a body, and the headers
// every answer on the path carries.
type decisionReply struct {
	status int
	body   []byte
	header map[string]string
}

// refused is a refusal as a reply on path, under request id rid.
func refused(path, rid string, r *decisionRefusal) decisionReply {
	header := map[string]string{}
	body, _ := Restate(path, r.status, r.body, header, rid)
	return decisionReply{status: r.status, body: body, header: header}
}

// decide answers one resolved decision. The price is reserved against the paying
// org before anything is sent, the in-process ledger is settled before the reply,
// and the debit — the call to the books — is filed once the reply has gone.
func decide(ctx context.Context, d decisionCall) decisionReply {
	if d.user == nil || d.ledger == "" {
		return refused(d.path, d.rid, decline(d.path, http.StatusForbidden, "no organization pays for this call"))
	}
	body, h, bad := scope(d.path, d.body, d.ledger)
	if bad != nil {
		return refused(d.path, d.rid, bad)
	}

	// Reserve the call's price, as the rerank media pipe does. Whatever way this
	// ends, the hold is released; a served call settles it at the price first.
	hold, admitted := reserveBudget(d.user.PayerSubject(d.ledger), decisionCostCents(d.model, 1))
	if !admitted {
		return refused(d.path, d.rid, decline(d.path, http.StatusPaymentRequired, object.InsufficientBalance(d.host, d.ledger, "cost").Message))
	}
	defer hold.settle(0)

	kai := object.KaiProvider()
	got := consult(ctx, kai, d.path, d.model, d.ledger, d.rid, body)
	if got.fault != nil {
		return refused(d.path, d.rid, got.fault)
	}
	out := unscope(got.status, got.body, d.ledger, h)
	if got.status == http.StatusOK {
		hold.settle(nanoToCents(decisionCostNano(d.model, got.usage.InputTokens, 1)))
		rec := decisionRecord(d.ctx, d.ledger, d.user, d.model, kai, d.premium, got.usage)
		rec.ClientIP = d.ip
		settleAfter(d.ctx, rec, d.start)
	}
	header := make(map[string]string, len(got.header))
	for k, v := range got.header {
		header[k] = v
	}
	out, _ = Restate(d.path, got.status, out, header, d.rid)
	return decisionReply{status: got.status, body: out, header: header}
}

// settling is every debit filed after its reply that has not reached the ledger.
var settling struct {
	mu   sync.Mutex
	n    int
	idle chan struct{} // closed when n falls to zero
}

// settleAfter files a served call's debit off the request path, so the reply never
// waits on the books. Every filing is counted until it lands, and Settled waits on
// the count: a debit is not lost to a process that stops.
func settleAfter(ctx context.Context, rec *usageRecord, start time.Time) {
	settling.mu.Lock()
	if settling.n == 0 {
		settling.idle = make(chan struct{})
	}
	settling.n++
	settling.mu.Unlock()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error("billing: debit filed after its reply panicked request_id=%s: %v", rec.RequestID, r)
			}
			settling.mu.Lock()
			settling.n--
			if settling.n == 0 {
				close(settling.idle)
			}
			settling.mu.Unlock()
		}()
		// One goroutine for both, in this order: recordUsage stamps the honesty
		// flag the span then reads.
		recordUsage(rec)
		recordTrace(ctx, rec, start)
	}()
}

// Settled waits until every debit filed after its reply has been handed to the
// ledger, or ctx ends. Shutdown calls it before the process exits.
func Settled(ctx context.Context) error {
	settling.mu.Lock()
	idle := settling.idle
	n := settling.n
	settling.mu.Unlock()
	if n == 0 {
		return nil
	}
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// DecisionPath reports whether path is one of the two decision paths, whose
// answers Restate words.
func DecisionPath(path string) bool {
	p := strings.ToLower(path)
	return p == decisionsPath || p == systemonePath
}

// RequestID is the id a decision answers under: the caller's own, when it is one a
// header can carry back, else a fresh one.
func RequestID(inbound string) string {
	if n := len(inbound); n > 0 && n <= 128 {
		ok := true
		for i := 0; i < n; i++ {
			if c := inbound[i]; c < 0x21 || c > 0x7e {
				ok = false
				break
			}
		}
		if ok {
			return inbound
		}
	}
	return uuid.NewString()
}

// pause is how long a refusal asks a caller to wait when nobody said: a balance is
// read again after its cache lifetime, and a full queue drains in a second.
func pause(status int) (time.Duration, bool) {
	switch status {
	case http.StatusPaymentRequired:
		return object.BalanceLedgerTTL, true
	case http.StatusTooManyRequests, 529:
		return time.Second, true
	}
	return 0, false
}

// Restate is how a decision path says an answer, whoever wrote it: a refusal in the
// path's own error shape — {"error":{"code","message"}} on /v1/decisions,
// {"detail": ...} on /v1/systemone — a 402, 429 or 529 in both Retry-After and
// Retry-After-Ms, and every answer under X-Request-Id, rid when nobody set one.
// header holds the answer's headers by canonical name and is completed in place;
// changed reports whether body was reworded.
func Restate(path string, status int, body []byte, header map[string]string, rid string) (_ []byte, changed bool) {
	if header["X-Request-Id"] == "" {
		header["X-Request-Id"] = rid
	}
	if d, ok := pause(status); ok {
		sec, ms := header["Retry-After"], header["Retry-After-Ms"]
		switch {
		case sec == "" && ms == "":
			header["Retry-After"] = strconv.FormatInt(int64(d/time.Second), 10)
			header["Retry-After-Ms"] = strconv.FormatInt(d.Milliseconds(), 10)
		case ms == "":
			if s, err := strconv.ParseFloat(sec, 64); err == nil && s >= 0 {
				header["Retry-After-Ms"] = strconv.FormatInt(int64(s*1000), 10)
			}
		case sec == "":
			if m, err := strconv.ParseFloat(ms, 64); err == nil && m >= 0 {
				header["Retry-After"] = strconv.FormatInt(int64((m+999)/1000), 10)
			}
		}
	}
	if status < http.StatusBadRequest {
		return body, false
	}
	var v map[string]any
	_ = json.Unmarshal(body, &v)
	if strings.ToLower(path) == systemonePath {
		if _, ok := v["detail"]; ok {
			return body, false
		}
		return systemoneFailure(wording(v, body, status)), true
	}
	if e, ok := v["error"].(map[string]any); ok {
		if _, ok := e["message"].(string); ok {
			return body, false
		}
	}
	return decisionsFailure(status, wording(v, body, status)), true
}

// wording is what a refusal says, read from whichever shape wrote it.
func wording(v map[string]any, body []byte, status int) string {
	if e, ok := v["error"].(map[string]any); ok {
		if s, ok := e["message"].(string); ok && s != "" {
			return s
		}
	}
	for _, k := range []string{"error", "detail", "msg", "message"} {
		if s, ok := v[k].(string); ok && s != "" {
			return s
		}
	}
	if v == nil {
		if s := strings.TrimSpace(string(body)); s != "" && len(s) <= 512 {
			return s
		}
	}
	return http.StatusText(status)
}

// remint gives an answer that was not produced for this request an id of its own,
// so no two calls share one.
func remint(body []byte) []byte {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || fields == nil {
		return body
	}
	if _, ok := fields["id"]; !ok {
		return body
	}
	var r [16]byte
	_, _ = rand.Read(r[:])
	fields["id"], _ = json.Marshal("dec_" + hex.EncodeToString(r[:]))
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if enc.Encode(fields) != nil {
		return body
	}
	return bytes.TrimRight(out.Bytes(), "\n")
}

// Decisions implements POST /v1/decisions (the Decisions API).
//
// Body: {"model": "kai", "state": "..."|{...}|[...], "questions": {"<name>":
// {"type": "choice"|"noul"|"score", "instructions": ..., "criteria": ...}}}.
// model is required; state and questions are required unless the request names a
// handle, which carries neither. instructions is optional and any JSON. A choice
// names at least 2 labels and a score at least 1 level, bounded by the token
// budget rather than a count; questions holds 1 to 100.
//
// observe holds the state under an id, and a later request naming that id as its
// handle decides over it again. A handle belongs to the org that observed it: no
// other org's request can name it.
//
// Response: {"id","model","provider","answers":{"<name>":{"type",...}},
// "usage":{"input_tokens","output_tokens"},"routing","state_hash","latency_ms"}.
// usage.input_tokens is the billed count — the request's text counted once, the
// state once and each question's instructions and options once; a decision over a
// handle bills the state once, when it was observed.
//
// Refusals are {"error":{"code","message"}}: 400 malformed JSON or unknown model,
// 401 no valid credential, 402 insufficient balance, 403 a key kind that may not
// call this (pk-), 422 an invalid question or a state beyond the checkpoint's reach
// (code state_too_long), 429 rate limited or queue full, 502 the service failed,
// 503 the model is known and not served, 529 overloaded. 402, 429 and 529 carry
// Retry-After and Retry-After-Ms, and every answer carries X-Request-Id. Billed on
// the answer's input tokens at the model's price.
func (c *ApiController) Decisions() { c.decision(decisionsPath) }

// Systemone implements POST /v1/systemone, the Jev-compatible spelling of POST
// /v1/decisions for a Jev client pointed at Hanzo: the same service, auth and
// billing, on Jev's wire.
//
// Body: {"model": "kai", "state": ..., "questions": {...}}, all three required:
// 1 to 100 questions, a choice of 2 to 255 labels, a score of 1 to 10 levels.
// Only kai and its versioned id are served; every Jev id is 400 {"detail":
// "Unknown model: <id>"}.
//
// Response: {"model","answers","usage":{"input_tokens","output_tokens"}}, Jev's
// shape and nothing beside it; model is the versioned id that answered.
//
// Refusals are FastAPI's: 422 {"detail":[{"loc","msg","type"}]} for a field that
// failed, and {"detail": "..."} for everything else — 400, 401, 402, 403, 429,
// 502, 503 and 529. 402, 429 and 529 carry Retry-After and Retry-After-Ms, and
// every answer carries X-Request-Id.
func (c *ApiController) Systemone() { c.decision(systemonePath) }

// decision answers a decision path over HTTP: authenticate, resolve who pays, and
// decide.
func (c *ApiController) decision(path string) {
	rid := RequestID(c.Header("X-Request-Id"))
	token, ok := strings.CutPrefix(c.Header("Authorization"), "Bearer ")
	if !ok || strings.TrimSpace(token) == "" {
		c.decisionReply(refused(path, rid, decline(path, http.StatusUnauthorized, "a Bearer credential is required")))
		return
	}
	if isPublishableKey(token) {
		c.decisionReply(refused(path, rid, decline(path, http.StatusForbidden,
			"Publishable keys (pk-) can only access read-only endpoints. Use a secret key (sk-) for this endpoint.")))
		return
	}

	model, bad := decisionModel(path, c.Body())
	if bad != nil {
		// Authenticate before reporting the client error: an invalid credential is
		// 401 regardless of body validity (never a probe-able 400).
		if authErr := c.authenticate(token); authErr != nil {
			c.decisionReply(refused(path, rid, decline(path, statusOf(authErr), authErr.Error())))
			return
		}
		c.decisionReply(refused(path, rid, bad))
		return
	}

	start := time.Now().UTC()
	_, authUser, _, isPremium, err := c.authResolveProvider(token, model, c.GetOrg())
	if err != nil {
		c.decisionReply(refused(path, rid, decline(path, statusOf(err), err.Error())))
		return
	}
	c.decisionReply(decide(c.Context(), decisionCall{
		path: path, model: model, body: c.Body(),
		user: authUser, ledger: c.billingOrg(authUser), premium: isPremium,
		host: c.Host(), ip: strings.Clone(c.Fiber().IP()), rid: rid, start: start,
		ctx: context.WithoutCancel(c.Context()),
	}))
}

// decisionReply writes a decision path's reply and disables the router's
// auto-render.
func (c *ApiController) decisionReply(r decisionReply) {
	for k, v := range r.header {
		c.SetHeader(k, v)
	}
	c.SetHeader("Content-Type", "application/json")
	c.Bytes(r.status, r.body)
}
