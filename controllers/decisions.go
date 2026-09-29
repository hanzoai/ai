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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/google/uuid"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zlib"
	"github.com/klauspost/compress/zstd"
	"github.com/valyala/fasthttp"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"

	iam "github.com/hanzoai/ai/internal/iam"

	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/upstream"
)

// POST /v1/decisions is Hanzo Decision's public API, on the Decisions API wire: a
// request names a model, a state and typed questions about it, and the answer is
// one typed answer per question with calibrated probabilities.
//
// ai does not decide. The decision service (object.KaiProvider, KAI_URL) answers;
// ai authenticates on the one auth + routing policy, reserves the call's price
// against the org that pays, names every handle by that org, forwards the body
// under the id the model's route names upstream, answers, and files the debit once
// the answer has gone. The models are the routes to that service
// (conf/models.yaml, provider kai): kai, and the Jev ids the service forwards to
// OpenRouter. A decision bills its input tokens at the model's price
// (decisionCostNano): Kai at $0.021 per million, Jev at its list price, $0.042.

const (
	decisionsPath = "/v1/decisions"
	nanoPerCent   = 10_000_000
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
	// Model is kai, kai-<12 hex> (Kai's versioned id), typesafe/jev-1.13 or
	// ~typesafe/jev-latest.
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

// decisionsFailure is the decision service's error body, {"error":{"code","message"}}.
// ai answers its own refusals on /v1/decisions in it, so a caller reads one shape
// whoever refused.
func decisionsFailure(code int, message string) []byte {
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"code": code, "message": message}})
	return b
}

// decisionRefusal is a status and a body in the service's error shape.
type decisionRefusal struct {
	status int
	body   []byte
}

// decline is ai's refusal on /v1/decisions.
func decline(code int, message string) *decisionRefusal {
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

// DecisionFree reports whether a model /v1/decisions serves costs org nothing.
// The balance gate refuses an empty wallet on it before decoding a body
// only when none does, so a free decision route stays reachable at $0.
func DecisionFree(org string) bool {
	for _, id := range decisionModels() {
		if costsNothing(id, org) {
			return true
		}
	}
	return false
}

// kaiUpstream reports whether a route reaches Kai itself, in the service's own
// spelling: kai, or kai@<revision>. The Jev routes reach Jev.
func kaiUpstream(up string) bool {
	return up == "kai" || strings.HasPrefix(up, "kai@")
}

// kaiVersion reports whether id is Kai's versioned id: kai- and the first 12
// lowercase hex digits of the served weights' sha256.
func kaiVersion(id string) bool {
	hex, ok := strings.CutPrefix(id, "kai-")
	if !ok || len(hex) != 12 {
		return false
	}
	for _, c := range hex {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// jevNamed reports whether an id names Jev.
func jevNamed(id string) bool {
	return strings.Contains(strings.ToLower(id), "jev")
}

// decisionBodyBytes is the largest decision body the gateway forwards, and it is the
// decision service's own BODY_BYTES (hanzoai/decision): the service derives it from
// its ceilings — 128k tokens of state, 100 questions, options bounded by the token
// budget — and the two change together, so a caller meets one bound, refused with
// one code, whichever of the two refuses. The socket admits more (ai.App's
// BodyLimit), so a body past this reaches the handler and is refused in the
// service's shape rather than by the transport.
const decisionBodyBytes = 16 << 20

// tooLong refuses a body past decisionBodyBytes: 422 request_too_long. size is the
// body's length, or 0 when the transport stopped reading it.
func tooLong(size int) *decisionRefusal {
	msg := fmt.Sprintf("the request body is over the limit of %d bytes", decisionBodyBytes)
	if size > 0 {
		msg = fmt.Sprintf("the request body is %d bytes; the limit is %d", size, decisionBodyBytes)
	}
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"code": "request_too_long", "message": msg}})
	return &decisionRefusal{status: http.StatusUnprocessableEntity, body: b}
}

// DecisionBody reads a decision body as sent, or decoded from its one Content-Encoding,
// neither past decisionBodyBytes, and leaves the request holding it with no coding.
func DecisionBody(c *zip.Ctx) ([]byte, error) {
	req := c.Fiber().Request()
	if len(req.Body()) > decisionBodyBytes {
		return nil, fasthttp.ErrBodyTooLarge
	}
	coding := strings.ToLower(strings.TrimSpace(string(req.Header.ContentEncoding())))
	if coding == "" || coding == "identity" {
		return req.Body(), nil
	}
	body, err := decode(coding, req.Body(), decisionBodyBytes)
	switch {
	case errors.Is(err, errCoding):
		return nil, fiber.NewError(http.StatusUnsupportedMediaType, fmt.Sprintf("Content-Encoding %q is not gzip, deflate, br or zstd", coding))
	case errors.Is(err, fasthttp.ErrBodyTooLarge):
		return nil, err
	case err != nil:
		return nil, fiber.NewError(http.StatusBadRequest, "body: not valid "+coding)
	}
	req.SetBodyRaw(body)
	req.Header.Del(fiber.HeaderContentEncoding)
	return body, nil
}

// errCoding is a Content-Encoding decode does not read.
var errCoding = errors.New("unsupported Content-Encoding")

// decode is body decoded from coding by decoders configured here, never past limit
// bytes: every coding's output is read to limit+1 and refused there, and a zstd
// frame whose header declares a window past limit is refused at the header, before
// the window is allocated. fasthttp's pooled decoders run at the libraries'
// defaults, which grant a zstd frame any window up to 512 MiB on its header's word.
// Brotli's window is at most 16 MiB by its format; this reader does not accept the
// large-window extension. Past the bound is fasthttp.ErrBodyTooLarge, a coding it
// does not read is errCoding, and any other error is a body that is not valid in
// its coding.
func decode(coding string, body []byte, limit int) ([]byte, error) {
	src := bytes.NewReader(body)
	var (
		r   io.Reader
		err error
	)
	switch coding {
	case "gzip", "x-gzip":
		r, err = gzip.NewReader(src)
	case "deflate":
		r, err = zlib.NewReader(src)
	case "br":
		r = brotli.NewReader(src)
	case "zstd":
		var d *zstd.Decoder
		d, err = zstd.NewReader(src,
			zstd.WithDecoderConcurrency(1),
			zstd.WithDecoderMaxWindow(uint64(limit)),
			zstd.WithDecoderMaxMemory(uint64(limit)))
		if err == nil {
			defer d.Close()
			r = d
		}
	default:
		return nil, errCoding
	}
	var out []byte
	if err == nil {
		out, err = io.ReadAll(io.LimitReader(r, int64(limit)+1))
	}
	switch {
	case errors.Is(err, zstd.ErrWindowSizeExceeded), errors.Is(err, zstd.ErrDecoderSizeExceeded):
		return nil, fasthttp.ErrBodyTooLarge
	case err != nil:
		return nil, err
	case len(out) > limit:
		return nil, fasthttp.ErrBodyTooLarge
	}
	return out, nil
}

// Refusing is /v1/decisions' answer to an error a layer RETURNED rather than
// wrote — a refusal the framework raised reading the request included: its status
// and sentence in the service's shape, a body over the limit as request_too_long,
// and anything that chose no status as a 500 that says nothing of what failed.
func Refusing(err error, rid string) (int, []byte, map[string]string) {
	status, msg := http.StatusInternalServerError, "internal error"
	var he *zip.HTTPError
	var fe *fiber.Error
	switch {
	case errors.Is(err, fasthttp.ErrBodyTooLarge):
		status = http.StatusRequestEntityTooLarge
	case errors.As(err, &he) && he.Status != 0:
		status, msg = he.Status, he.Msg
	case errors.As(err, &fe):
		status, msg = fe.Code, fe.Message
	default:
		log.Error("decisions: failed request_id=%s: %v", rid, err)
	}
	r := decline(status, msg)
	if status == http.StatusRequestEntityTooLarge {
		r = tooLong(0)
	}
	out := refused(rid, r)
	return out.status, out.body, out.header
}

// decisionKeys are the fields a decision body carries at its top level, as the
// service spells them.
var decisionKeys = map[string]bool{
	"model": true, "state": true, "questions": true, "observe": true, "handle": true,
	"provider": true, "session_id": true, "user": true, "trace": true,
}

// fieldsOf reads a decision body's top level exactly as written, and refuses a body
// the service could read differently from the gateway: a field it does not know —
// in any spelling, a case variant of one it does included — or a field given twice.
// What the gateway prices, gates and scopes is what the service reads, key for key,
// and no re-encoding downstream can fold a repeat into one value.
//
// It costs one pass over at most one more key than decisionKeys holds: an unknown or
// repeated key ends the read the moment it is seen, before its value is decoded.
func fieldsOf(body []byte) (map[string]json.RawMessage, *decisionRefusal) {
	bad := func(msg string) *decisionRefusal { return decline(http.StatusBadRequest, "body: "+msg) }
	dec := json.NewDecoder(bytes.NewReader(body))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, bad("not a JSON object")
	}
	fields := make(map[string]json.RawMessage, len(decisionKeys))
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, bad(err.Error())
		}
		key, _ := t.(string)
		if !decisionKeys[key] {
			for k := range decisionKeys {
				if strings.EqualFold(k, key) {
					return nil, bad(fmt.Sprintf("%q is spelled %q", k, key))
				}
			}
			return nil, bad(fmt.Sprintf("unknown field %q", key))
		}
		if _, again := fields[key]; again {
			return nil, bad(fmt.Sprintf("%q is given twice", key))
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, bad(err.Error())
		}
		fields[key] = v
	}
	if _, err := dec.Token(); err != nil {
		return nil, bad(err.Error())
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, bad("more than one JSON value")
	}
	return fields, nil
}

// vouched checks that token is one this service accepts before anything reads
// the body it came with: the body is the caller's to spend our time on only once we
// know who the caller is. A run key answers to the run table; every other credential
// as authenticate does.
func vouched(token, lang string) error {
	if isRunKey(token) {
		if _, ok := resolveRun(token); !ok {
			return authError("invalid or expired run key")
		}
		return nil
	}
	return authenticateToken(token, lang)
}

// readModel is how a handler reads a decision body's model: decisionModel, named so
// a test can count the reads a refused credential never reaches.
var readModel = decisionModel

// decisionModel reads the model a body names, and refuses a body with none, or a
// model /v1/decisions does not serve. It serves every route to the decision
// service: Kai, and Jev under OpenRouter's vendor ids, which reach Jev itself. An
// id that names Jev is never answered by Kai, so a route that would send one there
// is unknown, and so is every bare Jev spelling, which has no route. A model the
// service knows and does not publish is unknown, so it is never forwarded. The
// model comes back as its route's id, the one it is priced and filed under,
// whatever case or alias it was asked by.
func decisionModel(body []byte) (model, version string, _ *decisionRefusal) {
	fields, bad := fieldsOf(body)
	if bad != nil {
		return "", "", bad
	}
	raw, named := fields["model"]
	if named && json.Unmarshal(raw, &model) != nil {
		return "", "", decline(http.StatusBadRequest, "'model' must be a string")
	}
	if !named {
		return "", "", decline(http.StatusBadRequest, "the request needs a 'model'")
	}
	if id, ok := Canonical(model); ok {
		model = id
	}
	r := resolveModelRoute(model)
	// Kai's versioned id names the weights it is asked of: it is Kai, priced and
	// filed as kai, and the service is sent the id as asked, to check it names the
	// weights it serves. Anything else spelled kai-… that no route names is unknown.
	if r == nil && strings.HasPrefix(strings.ToLower(model), "kai-") {
		if k := resolveModelRoute("kai"); k != nil && k.providerName == object.KaiName && kaiUpstream(k.upstreamModel) && kaiVersion(model) {
			return "kai", model, nil
		}
	}
	if r == nil || r.providerName != object.KaiName || (jevNamed(model) && kaiUpstream(r.upstreamModel)) {
		return "", "", decline(http.StatusBadRequest, fmt.Sprintf("unknown model %q; use one of %s", model, strings.Join(decisionModels(), ", ")))
	}
	return strings.ToLower(model), "", nil
}

// A handle belongs to the org that observed it. The service holds observed states
// by id, so ai names every id it forwards by the org that pays for the call —
// observe and handle become <org>/<id> — and an id one org chose can only ever
// reach that org's states. The prefix is ai's, never the caller's: it is taken off
// anything the service says back.

// handles are the ids a body names, as the caller wrote them, and an observe's
// questions as coarse tokens: what a decision over it bills.
type handles struct {
	observe, handle string
	asked           int
}

// handleIDBytes is the longest id a caller may name a handle by.
const handleIDBytes = 128

// handleID reports whether id is 1 to handleIDBytes of A-Z, a-z, 0-9, '.', '_' and '-'.
func handleID(id string) bool {
	if len(id) == 0 || len(id) > handleIDBytes {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '.' && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

// scope names the body's handles by org. A body naming none goes out byte for
// byte; a handle that is not a string is refused, and so is one handleID does
// not accept, 422.
func scope(body []byte, org string) ([]byte, handles, *decisionRefusal) {
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
			return nil, handles{}, decline(http.StatusBadRequest, fmt.Sprintf("'%s' must be a string", key))
		}
		if !handleID(*id) {
			return nil, handles{}, decline(http.StatusUnprocessableEntity,
				fmt.Sprintf("'%s' must be 1 to %d characters of A-Z, a-z, 0-9, '.', '_' and '-'", key, handleIDBytes))
		}
		fields[key], _ = json.Marshal(org + "/" + *id)
		named = true
	}
	if !named {
		return body, handles{}, nil
	}
	if h.observe != "" {
		h.asked = coarseTokenEstimate(fields["questions"])
	}
	// An org with a slash in its name would make <org>/<id> ambiguous. IAM names
	// never carry one; a principal whose does is refused rather than guessed at.
	if strings.Contains(org, "/") {
		return nil, handles{}, decline(http.StatusForbidden, "this organization cannot hold a handle")
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if enc.Encode(fields) != nil {
		return nil, handles{}, decline(http.StatusBadRequest, "body: not a JSON object")
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

// handled remembers, per org, what a decision over each of its handles bills: its
// observed questions' coarse size, then each such decision's bill. Each org keeps at
// most handledPerOrg, evicting its own; past handledOrgs orgs an arbitrary org's go.
var handled handleCosts

const (
	handledOrgs   = 1 << 8
	handledPerOrg = 1 << 8
)

type handleCosts struct {
	mu   sync.Mutex
	orgs map[string]map[[sha256.Size]byte]int
}

func (h *handleCosts) cost(org, id string) int {
	k := sha256.Sum256([]byte(id))
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.orgs[org][k]
}

func (h *handleCosts) note(org, id string, tokens int) {
	k := sha256.Sum256([]byte(id))
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.orgs == nil {
		h.orgs = make(map[string]map[[sha256.Size]byte]int)
	}
	own, ok := h.orgs[org]
	if !ok {
		if len(h.orgs) >= handledOrgs {
			for other := range h.orgs {
				delete(h.orgs, other)
				break
			}
		}
		own = make(map[[sha256.Size]byte]int)
		h.orgs[org] = own
	}
	if _, known := own[k]; !known && len(own) >= handledPerOrg {
		for old := range own {
			delete(own, old)
			break
		}
	}
	own[k] = tokens
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

// consult sends body to the decision service, naming model by the id its route
// names upstream, and returns what the service said: a 200 names the model asked
// for, with the usage that answer reports. A refusal is ai's own: the service is
// not configured, or could not be reached.
func consult(ctx context.Context, kai *object.Provider, model, version, org, rid string, body []byte) decided {
	if kai == nil {
		return decided{fault: decline(http.StatusServiceUnavailable, "the decision service is not configured")}
	}
	up := model
	if r := resolveModelRoute(model); r != nil && r.upstreamModel != "" {
		up = r.upstreamModel
	}
	if version != "" {
		// A versioned id reaches the service as asked, and its answer names it.
		up, model = version, version
	}
	// The service reads the model the call was priced for, whatever spelling it was
	// asked in: the body's model is set to that id unless it already is it, byte for
	// byte. fieldsOf has refused every body with a second spelling of the key.
	var fields map[string]json.RawMessage
	named, _ := json.Marshal(up)
	if json.Unmarshal(body, &fields) != nil || string(fields["model"]) != string(named) {
		b, ok := WithModel(body, up)
		if !ok {
			return decided{fault: decline(http.StatusBadRequest, "body: not a JSON object")}
		}
		body = b
	}
	d := recall(ctx, kai, up, org, rid, body)
	if d.status == http.StatusOK && up != model {
		if named, ok := WithModel(d.body, model); ok {
			d.body = named
		}
	}
	return d
}

// send posts body to the decision service's /v1/decisions, under the caller's
// request id, and reads its reply.
func send(ctx context.Context, kai *object.Provider, rid string, body []byte) decided {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(kai.ProviderUrl, "/")+decisionsPath, bytes.NewReader(body))
	if err != nil {
		log.Error("decisions: build request to the decision service request_id=%s: %v", rid, err)
		return decided{fault: decline(http.StatusInternalServerError, "the decision request could not be built")}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-Id", rid)
	upstream.Authorize(req, kai)
	// Where the service lives is ours to know: a failure to reach it is logged with
	// its address and answered without one.
	resp, err := decisionsClient.Do(req)
	if err != nil {
		log.Error("decisions: the decision service did not answer request_id=%s: %v", rid, err)
		return decided{fault: decline(http.StatusBadGateway, "the decision service could not be reached")}
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Error("decisions: the decision service's answer broke off request_id=%s: %v", rid, err)
		return decided{fault: decline(http.StatusBadGateway, "the decision service's answer broke off")}
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

// decisionCall is one decision a principal asked for, resolved: the model, the
// body as written, who is asking, and the org that pays — the one org the
// reservation, the debit and every handle name.
type decisionCall struct {
	model string
	// version is Kai's versioned id when the call asked by one: the call is priced
	// and filed as model (kai), and the service is sent this.
	version string
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

// decisionReply is what /v1/decisions answers: a status, a body, and the headers
// every answer carries.
type decisionReply struct {
	status int
	body   []byte
	header map[string]string
}

// refused is a refusal as a reply, under request id rid.
func refused(rid string, r *decisionRefusal) decisionReply {
	header := map[string]string{}
	body, _ := Restate(r.status, r.body, header, rid)
	return decisionReply{status: r.status, body: body, header: header}
}

// decide answers one resolved decision. The price is reserved against the paying
// org before anything is sent, the in-process ledger is settled before the reply,
// and the debit — the call to the books — is filed once the reply has gone.
func decide(ctx context.Context, d decisionCall) decisionReply {
	if d.user == nil || d.ledger == "" {
		return refused(d.rid, decline(http.StatusForbidden, "no organization pays for this call"))
	}
	body, h, bad := scope(d.body, d.ledger)
	if bad != nil {
		return refused(d.rid, bad)
	}

	// Reserve the call's price before anything is sent, and never less than a cent:
	// the ledger holds whole cents and a decision costs a fraction of one, so a hold
	// rounded to zero would admit every call against a balance of one cent. A cent
	// covers a whole wire's worth of state at either model's price. Whatever way this
	// ends, the hold is released; a served call settles it first, in nano, at the
	// tokens its answer reports.
	//
	// A decision over a handle bills the questions it was observed with, which its
	// own body does not carry: it holds what handled remembers of them.
	tokens := coarseTokenEstimate(body)
	if h.handle != "" {
		tokens = max(tokens, handled.cost(d.ledger, h.handle))
	}
	est := (decisionCostNano(d.model, tokens) + nanoPerCent - 1) / nanoPerCent
	hold, admitted := reserveBudget(d.user.PayerSubject(d.ledger), max(est, 1))
	if !admitted {
		return refused(d.rid, decline(http.StatusPaymentRequired, object.InsufficientBalance(d.host, d.ledger, "cost").Message))
	}
	defer hold.settle(0)

	kai := object.KaiProvider()
	got := consult(ctx, kai, d.model, d.version, d.ledger, d.rid, body)
	if got.fault != nil {
		return refused(d.rid, got.fault)
	}
	out := unscope(got.status, got.body, d.ledger, h)
	header := make(map[string]string, len(got.header))
	for k, v := range got.header {
		header[k] = v
	}
	out, _ = Restate(got.status, out, header, d.rid)
	if got.status == http.StatusOK {
		hold.settleNano(decisionCostNano(d.model, got.usage.InputTokens))
		if h.observe != "" {
			handled.note(d.ledger, h.observe, min(h.asked, got.usage.InputTokens))
		}
		if h.handle != "" {
			handled.note(d.ledger, h.handle, got.usage.InputTokens)
		}
		rec := decisionRecord(d.ctx, d.ledger, d.user, d.model, kai, d.premium, got.usage)
		rec.ClientIP = d.ip
		// The row carries the id the caller was answered under, so a bill and the
		// answer it is for are found by one id; the row's own id stays ours.
		rec.ClientRequestID = header["X-Request-Id"]
		settleAfter(d.ctx, rec, d.start)
	}
	return decisionReply{status: got.status, body: out, header: header}
}

// A debit filed after its reply is handed to a fixed set of settlers through a
// bounded queue, so a burst of answers cannot become a burst of goroutines. A debit
// the ledger refuses or does not answer in usageTimeout is tried again, with
// backoff, up to settleTries times — the billing queue's rule for the same failure.
// A queue that is full files the debit on the request path instead: that answer
// waits, and no debit is dropped.
//
// A retry after the ledger took a debit and its answer was lost is the same debit:
// the record carries one Ref across every try (object.UsageEvent.Ref), and the host
// keys the ledger on it.
const (
	settleWorkers = 16
	settleDepth   = 1024
	settleTries   = 3
)

// settleBackoff is the first wait before a debit is tried again; it doubles.
var settleBackoff = 500 * time.Millisecond

// SettleBudget is how long a stopping process must give Settled: every try of a
// debit at its full timeout, and the waits between them.
func SettleBudget() time.Duration {
	return settleTries*usageTimeout + settleBackoff*(1<<(settleTries-1)) + time.Second
}

type settleJob struct {
	ctx   context.Context
	rec   *usageRecord
	start time.Time
}

// settling is every debit filed after its reply that has not reached the ledger.
var settling struct {
	mu   sync.Mutex
	n    int
	idle chan struct{} // closed when n falls to zero
	once sync.Once
	jobs chan settleJob
}

// settleAfter files a served call's debit off the request path, so the reply never
// waits on the books. Every filing is counted until it lands, and Settled waits on
// the count: a debit is not lost to a process that stops.
func settleAfter(ctx context.Context, rec *usageRecord, start time.Time) {
	settling.once.Do(func() {
		settling.jobs = make(chan settleJob, settleDepth)
		for range settleWorkers {
			go func() {
				for j := range settling.jobs {
					settle(j)
				}
			}()
		}
	})
	settling.mu.Lock()
	if settling.n == 0 {
		settling.idle = make(chan struct{})
	}
	settling.n++
	settling.mu.Unlock()
	select {
	case settling.jobs <- settleJob{ctx: ctx, rec: rec, start: start}:
	default:
		settle(settleJob{ctx: ctx, rec: rec, start: start})
	}
}

// settle files one debit, trying again while the ledger refuses it, then its trace.
func settle(j settleJob) {
	defer func() {
		if r := recover(); r != nil {
			log.Error("billing: debit filed after its reply panicked request_id=%s", j.rec.RequestID)
		}
		settling.mu.Lock()
		settling.n--
		if settling.n == 0 {
			close(settling.idle)
		}
		settling.mu.Unlock()
	}()
	// One goroutine for both, in this order: recordUsage stamps the honesty flag the
	// span then reads.
	for try := 1; ; try++ {
		err := recordUsage(j.rec)
		if err == nil {
			break
		}
		if try == settleTries {
			log.Error("billing: debit filed after its reply did not land after %d tries request_id=%s: %v", try, j.rec.RequestID, err)
			break
		}
		time.Sleep(settleBackoff << (try - 1))
	}
	recordTrace(j.ctx, j.rec, j.start)
}

// Settled waits until every debit filed after its reply has been handed to the
// ledger, or ctx ends. A stopping process calls it once it has stopped taking
// requests, with at least SettleBudget.
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

// DecisionPath reports whether path is /v1/decisions, whose answers Restate words,
// spelled any way the router matches it: any case, a trailing slash or not.
func DecisionPath(path string) bool {
	return strings.TrimSuffix(strings.ToLower(path), "/") == decisionsPath
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

// Restate is how /v1/decisions says an answer, whoever wrote it: a refusal in the
// service's error shape, {"error":{"code","message"}}, a 402, 429 or 529 in both
// Retry-After and Retry-After-Ms, and every answer under X-Request-Id, rid when
// nobody set one. header holds the answer's headers by canonical name and is
// completed in place; changed reports whether body was reworded.
func Restate(status int, body []byte, header map[string]string, rid string) (_ []byte, changed bool) {
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
	for _, k := range []string{"error", "msg", "message"} {
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
// model is kai, Kai's versioned id kai-<12 hex of the weights' sha256> — priced as
// kai and sent as asked — or Jev by OpenRouter's vendor ids, typesafe/jev-1.13 and
// ~typesafe/jev-latest, which reach Jev itself and bill at Jev's list price. No Jev
// id is ever answered by Kai: a bare one, such as jev-latest, is an unknown model.
// model is required; state and questions are required unless the request names a
// handle, which carries neither. instructions is optional and any JSON. A choice
// names at least 2 labels and a score at least 1 level, bounded by the token
// budget rather than a count; questions holds 1 to 100.
//
// observe holds the state under an id, and a later request naming that id as its
// handle decides over it again. An id is 1 to 128 characters of A-Z, a-z, 0-9, '.',
// '_' and '-'. A handle belongs to the org that observed it: no other org's request
// can name it.
//
// Response: {"id","model","provider","answers":{"<name>":{"type",...}},
// "usage":{"input_tokens","output_tokens"},"routing","state_hash","latency_ms"}.
// usage.input_tokens is the billed count — the request's text counted once, the
// state once and each question's instructions and options once; a decision over a
// handle bills the state once, when it was observed.
//
// The body may be sent gzip, deflate, br or zstd encoded; decoded, it is bounded as
// sent.
//
// Refusals are {"error":{"code","message"}}: 400 malformed JSON or unknown model,
// 401 no valid credential, 402 insufficient balance, 403 a key kind that may not
// call this (pk-), 415 any other Content-Encoding, 422 an invalid question or handle
// id, a state beyond the checkpoint's reach (code state_too_long) or a body past 16
// MiB (code request_too_long), 429 rate limited or queue full, 502 the service
// failed, 503 the model is known and not served, 529 overloaded. 402, 429 and 529
// carry Retry-After and Retry-After-Ms, and every answer carries X-Request-Id.
// Billed on the answer's input tokens at the model's price.
func (c *ApiController) Decisions() { c.decision() }

// decision answers /v1/decisions over HTTP: authenticate, resolve who pays, and
// decide.
func (c *ApiController) decision() {
	rid := RequestID(c.Header("X-Request-Id"))
	token, ok := strings.CutPrefix(c.Header("Authorization"), "Bearer ")
	if !ok || strings.TrimSpace(token) == "" {
		c.decisionReply(refused(rid, decline(http.StatusUnauthorized, "a Bearer credential is required")))
		return
	}
	if isPublishableKey(token) {
		c.decisionReply(refused(rid, decline(http.StatusForbidden,
			"Publishable keys (pk-) can only access read-only endpoints. Use a secret key (sk-) for this endpoint.")))
		return
	}

	// The size bound is the one thing asked of a body before its sender is known:
	// it costs the length of the bytes as sent. Everything that decodes or reads the
	// body waits on the credential.
	if n := len(c.Fiber().Request().Body()); n > decisionBodyBytes {
		c.decisionReply(refused(rid, tooLong(n)))
		return
	}
	if err := vouched(token, c.GetAcceptLanguage()); err != nil {
		c.decisionReply(refused(rid, decline(statusOf(err), err.Error())))
		return
	}
	body, err := DecisionBody(c.Ctx)
	if err != nil {
		status, out, header := Refusing(err, rid)
		c.decisionReply(decisionReply{status: status, body: out, header: header})
		return
	}
	model, version, bad := readModel(body)
	if bad != nil {
		c.decisionReply(refused(rid, bad))
		return
	}

	start := time.Now().UTC()
	_, authUser, _, isPremium, err := c.authResolveProvider(token, model, c.GetOrg())
	if err != nil {
		c.decisionReply(refused(rid, decline(statusOf(err), err.Error())))
		return
	}
	// Jev is bought per call; Kai is ours. With the paid lane off, Jev is not asked.
	if FreeOnly() && jevNamed(model) {
		err := paidLaneOff(model)
		c.decisionReply(refused(rid, decline(statusOf(err), err.Error())))
		return
	}
	c.decisionReply(decide(c.Context(), decisionCall{
		model: model, version: version, body: body,
		user: authUser, ledger: c.billingOrg(authUser), premium: isPremium,
		host: c.Host(), ip: strings.Clone(c.Fiber().IP()), rid: rid, start: start,
		ctx: context.WithoutCancel(c.Context()),
	}))
}

// decisionReply writes a decision's reply and disables the router's
// auto-render.
func (c *ApiController) decisionReply(r decisionReply) {
	for k, v := range r.header {
		c.SetHeader(k, v)
	}
	c.SetHeader("Content-Type", "application/json")
	c.Bytes(r.status, r.body)
}
