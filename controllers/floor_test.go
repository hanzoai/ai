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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/decimal"

	"github.com/hanzoai/ai/object"
)

// Every Enso id falls to enso-free and every Zen id to zen-free; a free model, the
// platform's own free id and anybody else's model have no floor.
func TestTheFloorIsTheFamilysFreeModel(t *testing.T) {
	for model, want := range map[string]string{
		"enso": "enso-free", "enso-auto": "enso-free", "enso-flash": "enso-free", "enso-pro": "enso-free",
		"enso-ultra": "enso-free", "hanzo/enso": "enso-free", "ENSO-PRO": "enso-free",
		"zen6": "zen-free", "zen6-flash": "zen-free", "zen5-coder": "zen-free",
		"enso-free": "", "zen-free": "", "free": "", "": "",
		"anthropic/claude-sonnet-5.5": "", "gpt-6.1-sol": "",
	} {
		if got := Floor(model); got != want {
			t.Errorf("Floor(%q) = %q, want %q", model, got, want)
		}
	}
}

// Want of payment and a model that cannot answer fall; what the caller set, the
// free lane's own allowance and an unreadable policy stand.
func TestWhatFallsAndWhatStands(t *testing.T) {
	for _, why := range []string{
		object.CodeInsufficientBalance, object.CodeBalanceUnavailable, object.CodePaidPlan, object.CodePlanAllowance,
		object.CodeModelCap, object.CodeFreePlanCap, object.CodeUsageCap, ReasonFull, codeSupply,
		onTimeout, onUnreachable, onBusy, onUnfunded, onCredential, onCapacity, onUpstream, onMasked,
		onUnpriced, onClosed, onMissing, onCut, onRoute,
	} {
		if to, ok := Fall("zen6", why); !ok || to != "zen-free" {
			t.Errorf("Fall(zen6, %q) = %q %v, want zen-free", why, to, ok)
		}
	}
	for _, why := range []string{"", "key_budget", "key_model", object.CodeAllowanceSpent, object.CodePoolBusy,
		object.CodePoolExhausted, object.CodeUsageUnavailable, "invalid_request"} {
		if to, ok := Fall("enso-pro", why); ok {
			t.Errorf("Fall(enso-pro, %q) = %q, want the refusal to stand", why, to)
		}
	}
	// A floor never falls, and nobody else's model has one: one hop at most.
	for _, model := range []string{"enso-free", "zen-free", "anthropic/claude-sonnet-5.5"} {
		if to, ok := Fall(model, object.CodeInsufficientBalance); ok {
			t.Errorf("Fall(%q) = %q, want none", model, to)
		}
	}
}

// Why a family could not serve, read from its error: the status first.
func TestCauseNamesWhyAFamilyCouldNotServe(t *testing.T) {
	for want, err := range map[string]error{
		onTimeout:     fmt.Errorf("Post: %w", context.DeadlineExceeded),
		onUnreachable: errors.New("dial tcp 10.0.0.1:443: connect: connection refused"),
		onBusy:        &apiError{status: http.StatusTooManyRequests, msg: "busy"},
		onUnfunded:    &apiError{status: http.StatusPaymentRequired, msg: "Insufficient credits"},
		onCapacity:    &apiError{status: http.StatusServiceUnavailable, msg: "this model is temporarily unavailable"},
		onUpstream:    &apiError{status: http.StatusBadGateway, msg: "bad gateway"},
		onMasked:      &apiError{status: http.StatusBadRequest, msg: "the model could not serve this request"},
	} {
		if got := cause(err); got != want {
			t.Errorf("cause(%v) = %q, want %q", err, got, want)
		}
	}
	// A refusal that describes the request is nobody's outage.
	if got := cause(&apiError{status: http.StatusBadRequest, msg: "messages: required"}); got != "" {
		t.Errorf("a malformed request reads as %q", got)
	}
}

// A handler's own gate falls only for want of payment, only on a conversation, and
// only for a model with a floor; the body then names the floor and the answer says
// why.
func TestTheHandlersGateFallsOnlyForWantOfPayment(t *testing.T) {
	ask := func(path, model string) *ApiController {
		c := visit(http.MethodPost, path)
		c.Fiber().Request().SetBody([]byte(`{"model":"` + model + `","stream":true,"tools":[{"type":"function","function":{"name":"f"}}],"messages":[{"role":"user","content":"hi"}]}`))
		return c
	}
	unverified := &apiError{status: http.StatusInternalServerError, code: object.CodeBalanceUnavailable, msg: "failed to verify account balance"}
	for name, tc := range map[string]struct {
		err error
		why string
	}{
		"an empty wallet":          {billingError("requires a positive balance"), object.CodeInsufficientBalance},
		"a balance it cannot read": {unverified, object.CodeBalanceUnavailable},
		"our own cash breaker":     {supplyError("paid inference is temporarily unavailable"), codeSupply},
		"a bare 402 with no code":  {&apiError{status: http.StatusPaymentRequired, msg: "x"}, object.CodeInsufficientBalance},
	} {
		for _, path := range []string{"/v1/chat/completions", "/v1/messages", "/v1/responses"} {
			c := ask(path, "zen6")
			to, ok := c.fall("zen6", tc.err)
			if !ok || to != "zen-free" {
				t.Fatalf("%s on %s: fall = %q %v, want zen-free", name, path, to, ok)
			}
			var body map[string]any
			if err := json.Unmarshal(c.Body(), &body); err != nil || body["model"] != "zen-free" || body["stream"] != true || body["tools"] == nil {
				t.Errorf("%s on %s: body %s, want the same request naming zen-free", name, path, c.Body())
			}
			if got := string(c.Fiber().Response().Header.Peek(FallbackHeader)); got != "zen-free" {
				t.Errorf("%s: %s = %q", name, FallbackHeader, got)
			}
			if got := string(c.Fiber().Response().Header.Peek(FallbackReasonHeader)); got != tc.why {
				t.Errorf("%s: %s = %q, want %q", name, FallbackReasonHeader, got, tc.why)
			}
		}
	}
	for name, tc := range map[string]struct {
		path, model string
		err         error
	}{
		"a bad key":                {"/v1/chat/completions", "zen6", authError("invalid API key")},
		"a malformed request":      {"/v1/chat/completions", "zen6", modelError("model %q is not available", "zen6")},
		"an org it may not act in": {"/v1/chat/completions", "zen6", forbiddenError("forbidden")},
		"an embedding":             {"/v1/embeddings", "zen6", billingError("requires a positive balance")},
		"somebody else's model":    {"/v1/chat/completions", "anthropic/claude-sonnet-5.5", billingError("requires a positive balance")},
		"the floor itself":         {"/v1/chat/completions", "zen-free", billingError("requires a positive balance")},
		"no refusal":               {"/v1/chat/completions", "zen6", nil},
	} {
		c := ask(tc.path, tc.model)
		before := string(c.Body())
		if to, ok := c.fall(tc.model, tc.err); ok {
			t.Errorf("%s: fell to %q, want the refusal to stand", name, to)
		}
		if string(c.Body()) != before || string(c.Fiber().Response().Header.Peek(FallbackHeader)) != "" {
			t.Errorf("%s: the request was changed though it did not fall", name)
		}
	}
}

// floorVendor is one test vendor behind both the family under test and the free
// pool: the model asked gets status and body, the pool's route answers whole or as a
// stream, as the request asked, or as ask says when it is set.
type floorVendor struct {
	status int
	body   string
	header http.Header
	ask    func(map[string]json.RawMessage) (int, string)

	mu     sync.Mutex
	asked  []string
	bodies []map[string]json.RawMessage
}

const floorRoute = "vendor/big:free"

func (v *floorVendor) serve(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var in map[string]json.RawMessage
		_ = json.Unmarshal(raw, &in)
		var model string
		_ = json.Unmarshal(in["model"], &model)
		var stream bool
		_ = json.Unmarshal(in["stream"], &stream)
		v.mu.Lock()
		v.asked = append(v.asked, model)
		v.bodies = append(v.bodies, in)
		v.mu.Unlock()
		if model != floorRoute {
			for k, vs := range v.header {
				w.Header()[k] = vs
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(v.status)
			_, _ = io.WriteString(w, v.body)
			return
		}
		if v.ask != nil {
			st, b := v.ask(in)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(st)
			_, _ = io.WriteString(w, b)
			return
		}
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, answerStream)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"gen-1","model":"`+floorRoute+`","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`)
	}))
	t.Cleanup(s.Close)
	return s
}

func (v *floorVendor) calls() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.asked...)
}

// floorPool points the free pool at url, with one vendor account and none of our
// own compute.
func floorPool(t *testing.T, url string) {
	t.Helper()
	restore(t, engineFam)
	engineFam.urlKey = "TEST_ENGINE_URL_UNSET"
	engineFam.providerFn = nil
	t.Setenv("OPENROUTER_API_KEY", "k1")
	t.Setenv("OPENROUTER_API_KEY_2", "")
	t.Setenv("OPENROUTER_API_KEY_3", "")
	forgetKeys()
	cooled.forget()
	spareFamily(t, url, floorRoute)
}

// zenAt is a Zen family served at url, as otherFamily is an Enso one.
func zenAt(url string) *modelFamily {
	return &modelFamily{
		name: "zen", prefix: "zen", owner: "zenlm", freeName: "zen-free",
		providerFn: func() *object.Provider {
			return &object.Provider{Owner: "admin", Name: "zen", Type: "Zen", ProviderUrl: url}
		},
		loaded: true, fetchedAt: time.Now(),
	}
}

// turn drives the real pipe for one request and returns the client's view of it.
func turn(t *testing.T, fam *modelFamily, model, dialect, body string, stream bool, start time.Time) (*ApiController, []attempt) {
	t.Helper()
	path, api := "/v1/chat/completions", "chat/completions"
	if dialect == "anthropic" {
		path, api = "/v1/messages", "messages"
	}
	c := visit(http.MethodPost, path)
	c.Fiber().Request().SetBody([]byte(body))
	return c, c.pipeToFamily(fam, api, dialect, model, []byte(body), stream, 0, "acme", nil, false, nil, start)
}

// AN ENSO OR ZEN MODEL THAT CANNOT SERVE IS ANSWERED BY ITS FLOOR, streamed or whole,
// for every failure that is not the caller's: the same request, one hop, named for
// what answered.
func TestAModelThatCannotServeIsAnsweredByItsFloor(t *testing.T) {
	fails := map[string]struct {
		status int
		body   string
		why    string
	}{
		"a server error":    {http.StatusInternalServerError, `{"error":{"message":"boom"}}`, onUpstream},
		"a bad gateway":     {http.StatusBadGateway, `{"error":{"message":"bad gateway"}}`, onUpstream},
		"no capacity":       {http.StatusServiceUnavailable, `{"error":{"message":"this model is temporarily unavailable"}}`, onCapacity},
		"a rate limit":      {http.StatusTooManyRequests, `{"error":{"message":"this model is busy"}}`, onBusy},
		"a spent account":   {http.StatusPaymentRequired, `{"error":{"message":"Insufficient credits."}}`, onUnfunded},
		"a masked refusal":  {http.StatusBadRequest, `{"error":{"message":"the model could not serve this request","type":"zen_error","code":400}}`, onMasked},
		"a gateway timeout": {http.StatusGatewayTimeout, `{"error":{"message":"upstream timed out"}}`, onTimeout},
	}
	for _, family := range []struct {
		model, floor string
		at           func(string) *modelFamily
	}{
		{"enso-pro", "enso-free", func(u string) *modelFamily { return otherFamily(t, u) }},
		{"zen6", "zen-free", zenAt},
	} {
		for name, f := range fails {
			for _, stream := range []bool{false, true} {
				label := fmt.Sprintf("%s %s stream=%v", family.model, name, stream)
				v := &floorVendor{status: f.status, body: f.body}
				s := v.serve(t)
				floorPool(t, s.URL)
				body := fmt.Sprintf(`{"model":%q,"stream":%v,"response_format":{"type":"json_object"},"messages":[{"role":"user","content":"2+2?"}]}`, family.model, stream)
				c, out := turn(t, family.at(s.URL), family.model, "openai", body, stream, time.Now())
				if out != nil || answered(c) != http.StatusOK {
					t.Fatalf("%s: attempts=%+v status %d body %s, want the floor's answer", label, out, answered(c), sent(c))
				}
				got := sent(c)
				if !strings.Contains(got, `"model":"`+family.floor+`"`) || strings.Contains(got, `"model":"`+family.model+`"`) || strings.Contains(got, floorRoute) {
					t.Errorf("%s: body names the wrong model:\n%s", label, got)
				}
				if stream && !strings.Contains(got, "data: ") {
					t.Errorf("%s: a streamed request was not answered as a stream:\n%s", label, got)
				}
				for h, want := range map[string]string{FallbackHeader: family.floor, FallbackReasonHeader: f.why, servedHeader: family.floor} {
					if got := header(c, h); got != want {
						t.Errorf("%s: %s = %q, want %q", label, h, got, want)
					}
				}
				calls := v.calls()
				if len(calls) != 2 || calls[0] != family.model || calls[1] != floorRoute {
					t.Errorf("%s: asked %v, want the model once and then its floor", label, calls)
				}
				// The same request: what shapes the answer goes to the floor as sent.
				if len(v.bodies) == 2 && string(v.bodies[1]["response_format"]) != `{"type":"json_object"}` {
					t.Errorf("%s: response_format reached the floor as %s", label, v.bodies[1]["response_format"])
				}
			}
		}
	}
}

// A model whose family could not be reached at all, or answered only after the
// request's whole wait, is still answered by its floor: one hop is always asked.
func TestAFloorIsAskedHoweverLongTheModelTookToFail(t *testing.T) {
	v := &floorVendor{status: http.StatusGatewayTimeout, body: `{"error":{"message":"timed out"}}`}
	s := v.serve(t)
	floorPool(t, s.URL)
	body := `{"model":"enso-ultra","messages":[{"role":"user","content":"2+2?"}]}`
	c, out := turn(t, otherFamily(t, s.URL), "enso-ultra", "openai", body, false, time.Now().Add(-2*zenPipeHeader))
	if out != nil || answered(c) != http.StatusOK || header(c, FallbackHeader) != "enso-free" {
		t.Fatalf("attempts=%+v status %d fallback %q body %s, want the floor's answer", out, answered(c), header(c, FallbackHeader), sent(c))
	}

	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	c, out = turn(t, otherFamily(t, deadURL), "enso-ultra", "openai", body, false, time.Now())
	if out != nil || answered(c) != http.StatusOK || header(c, FallbackReasonHeader) != onUnreachable {
		t.Fatalf("unreachable: attempts=%+v status %d reason %q body %s", out, answered(c), header(c, FallbackReasonHeader), sent(c))
	}
}

// A refusal that describes the request is the caller's: answered as it is, never by
// another model.
func TestARefusalThatIsTheCallersIsNotMoved(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"a malformed request": {http.StatusBadRequest, `{"error":{"message":"messages: at least one is required"}}`},
		"an unknown model":    {http.StatusNotFound, `{"error":{"message":"unknown model"}}`},
		"refused content":     {http.StatusUnprocessableEntity, `{"error":{"message":"content refused"}}`},
		"our own billing":     {http.StatusPaymentRequired, `{"error":{"message":"add funds","code":"` + object.CodeInsufficientBalance + `"}}`},
	} {
		v := &floorVendor{status: tc.status, body: tc.body}
		s := v.serve(t)
		floorPool(t, s.URL)
		c, _ := turn(t, otherFamily(t, s.URL), "enso-pro", "openai", `{"model":"enso-pro","messages":[{"role":"user","content":"hi"}]}`, false, time.Now())
		if got := v.calls(); len(got) != 1 {
			t.Errorf("%s: asked %v, want the model alone", name, got)
		}
		if header(c, FallbackHeader) != "" || strings.Contains(sent(c), "enso-free") {
			t.Errorf("%s: moved to the floor: %s %s", name, header(c, FallbackHeader), sent(c))
		}
	}
}

// NO LOOP: a floor that cannot serve either answers with its own refusal — its
// status, said as the floor's — and nothing is asked after it.
func TestAFloorThatFailsTooAnswersItsOwnRefusal(t *testing.T) {
	v := &floorVendor{status: http.StatusServiceUnavailable, body: `{"error":{"message":"down"}}`,
		ask: func(map[string]json.RawMessage) (int, string) {
			return http.StatusBadGateway, `{"error":{"message":"Provider returned error from SomeVendor"}}`
		}}
	s := v.serve(t)
	floorPool(t, s.URL)
	c, out := turn(t, otherFamily(t, s.URL), "enso-pro", "openai", `{"model":"enso-pro","messages":[{"role":"user","content":"hi"}]}`, false, time.Now())
	if out != nil {
		t.Fatalf("attempts=%+v, want the floor's refusal answered here and nothing handed on", out)
	}
	if answered(c) != http.StatusBadGateway {
		t.Errorf("status %d, want the floor's own 502 (%s)", answered(c), sent(c))
	}
	if !strings.Contains(sent(c), "enso-free is temporarily unavailable") || strings.Contains(sent(c), "SomeVendor") {
		t.Errorf("body %s, want the floor named and no vendor's words", sent(c))
	}
	if header(c, FallbackHeader) != "enso-free" || header(c, FallbackReasonHeader) != onCapacity {
		t.Errorf("fallback %q reason %q, want the floor that was tried and why", header(c, FallbackHeader), header(c, FallbackReasonHeader))
	}
	if got := v.calls(); len(got) != 2 || got[0] != "enso-pro" || got[1] != floorRoute {
		t.Errorf("asked %v, want the model and its floor once each", got)
	}
}

// A FORCED TOOL IS NEVER DROPPED. The floor is asked for it as "required" over that
// tool alone; a floor that refuses the shape is asked once more with "auto" and a
// sentence asking for the call, and the answer says which.
func TestAForcedToolIsAskedOfTheFloorAsRequiredThenAuto(t *testing.T) {
	var choices []string
	v := &floorVendor{status: http.StatusServiceUnavailable, body: `{"error":{"message":"down"}}`,
		ask: func(in map[string]json.RawMessage) (int, string) {
			choices = append(choices, string(in["tool_choice"]))
			if string(in["tool_choice"]) != `"auto"` {
				return http.StatusNotFound, `{"error":{"message":"No endpoints found that support the provided 'tool_choice' value."}}`
			}
			var msgs []map[string]any
			_ = json.Unmarshal(in["messages"], &msgs)
			var tools []map[string]any
			_ = json.Unmarshal(in["tools"], &tools)
			if len(msgs) == 0 || msgs[0]["role"] != "system" || msgs[0]["content"] != "Answer by calling the tool weather." || len(tools) != 1 {
				return http.StatusBadRequest, `{"error":{"message":"not the request the floor was promised"}}`
			}
			return http.StatusOK, `{"id":"g","model":"x","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":null,` +
				`"tool_calls":[{"id":"c1","type":"function","function":{"name":"weather","arguments":"{}"}}]}}]}`
		}}
	s := v.serve(t)
	floorPool(t, s.URL)
	body := `{"model":"zen6","tool_choice":{"type":"function","function":{"name":"weather"}},` +
		`"tools":[{"type":"function","function":{"name":"weather"}},{"type":"function","function":{"name":"news"}}],` +
		`"messages":[{"role":"user","content":"weather?"}]}`
	c, out := turn(t, zenAt(s.URL), "zen6", "openai", body, false, time.Now())
	if out != nil || answered(c) != http.StatusOK || !strings.Contains(sent(c), `"name":"weather"`) {
		t.Fatalf("attempts=%+v status %d body %s, want the floor's tool call", out, answered(c), sent(c))
	}
	if strings.Join(choices, " ") != `"required" "auto"` {
		t.Errorf("the floor was asked %v, want required then auto", choices)
	}
	if got := header(c, toolsHeader); got != "auto" {
		t.Errorf("%s = %q, want auto", toolsHeader, got)
	}
}

// relax loosens a tool choice one step in either dialect and never drops a tool the
// request could still call.
func TestRelaxLoosensAToolChoiceOneStep(t *testing.T) {
	oai := `{"model":"m","tool_choice":{"type":"function","function":{"name":"a"}},"tools":[{"type":"function","function":{"name":"a"}},{"type":"function","function":{"name":"b"}}],"messages":[{"role":"user","content":"x"}]}`
	b, did, ok := relax([]byte(oai), "openai", 1)
	if !ok || did != "required" || !strings.Contains(string(b), `"tool_choice":"required"`) || strings.Contains(string(b), `"name":"b"`) || !strings.Contains(string(b), `"parallel_tool_calls":false`) {
		t.Fatalf("openai forced → %s %q %v", b, did, ok)
	}
	b, did, ok = relax(b, "openai", 2)
	var asked struct {
		Messages []struct{ Role, Content string }
		Tools    []json.RawMessage
	}
	_ = json.Unmarshal(b, &asked)
	if !ok || did != "auto" || !strings.Contains(string(b), `"tool_choice":"auto"`) || len(asked.Messages) != 2 || len(asked.Tools) != 1 ||
		asked.Messages[0].Role != "system" || asked.Messages[0].Content != "Answer by calling the tool a." {
		t.Fatalf("openai required → %s %q %v", b, did, ok)
	}
	if _, _, ok := relax(b, "openai", 2); ok {
		t.Error("auto was loosened again")
	}
	if _, _, ok := relax([]byte(`{"tool_choice":{"type":"function","function":{"name":"gone"}},"tools":[{"type":"function","function":{"name":"a"}}]}`), "openai", 1); ok {
		t.Error("a choice naming a tool the request does not offer was rewritten")
	}

	ant := `{"model":"m","system":"be brief","tool_choice":{"type":"tool","name":"a"},"tools":[{"name":"a","input_schema":{}},{"name":"b","input_schema":{}}],"messages":[{"role":"user","content":"x"}]}`
	b, did, ok = relax([]byte(ant), "anthropic", 1)
	if !ok || did != "required" || !strings.Contains(string(b), `"type":"any"`) || strings.Contains(string(b), `"name":"b"`) {
		t.Fatalf("anthropic forced → %s %q %v", b, did, ok)
	}
	b, did, ok = relax(b, "anthropic", 2)
	if !ok || did != "auto" || !strings.Contains(string(b), `"tool_choice":{"type":"auto"}`) || !strings.Contains(string(b), `"system":"Answer by calling the tool a.\n\nbe brief"`) {
		t.Fatalf("anthropic any → %s %q %v", b, did, ok)
	}
	blocks := `{"tool_choice":{"type":"any"},"system":[{"type":"text","text":"be brief"}],"tools":[{"name":"a"},{"name":"b"}],"messages":[]}`
	if b, _, ok := relax([]byte(blocks), "anthropic", 2); !ok || !strings.Contains(string(b), `"system":[{"text":"Answer by calling one of the provided tools.","type":"text"},{"type":"text","text":"be brief"}]`) {
		t.Fatalf("anthropic system blocks → %s", b)
	}
}

// An answer its floor wrote bills nothing, whatever the model asked costs; the same
// model answered as asked bills its price.
func TestAFallenAnswerBillsNothing(t *testing.T) {
	enso := otherFamily(t, "http://enso.invalid")
	enso.byID = map[string]zenModel{"enso-pro": {ID: "enso-pro", Base: zenTier{In: decimal.New(3, 0), Out: decimal.New(15, 0)}}}
	enso.ids = []string{"enso-pro"}
	c := visit(http.MethodPost, "/v1/x")
	use := tokens{fresh: 1_000_000, completion: 1_000_000}
	w := whence{ledger: c.billingOrg(nil), ip: c.Fiber().IP(), ctx: c.Context(), asked: enso}
	if nano := recordFamilyUsage(w, enso, "enso-pro", "", nil, &mark{}, nil, true, false, "r1", use, serving{}, time.Now(), nil, "success", ""); nano != 18_000_000_000 {
		t.Fatalf("enso-pro as asked billed %d nano, want $18", nano)
	}
	w.fell = true
	hold := &budgetHold{est: 2000}
	if nano := recordFamilyUsage(w, enso, "enso-pro", "enso-pro", nil, &mark{}, nil, true, false, "r2", use, serving{}, time.Now(), hold, "success", ""); nano != 0 {
		t.Errorf("an answer its floor wrote billed %d nano", nano)
	}
}

// A family whose own catalog fell to the floor says so in X-Hanzo-Served: the answer
// is then the floor's, in its body as in its headers, and bills nothing.
func TestAFamilysOwnFallbackIsNamedAsTheFloor(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(servedHeader, "zen-free")
		w.Header().Set(freeHeader, "true")
		_, _ = io.WriteString(w, `{"id":"g","model":"zen6","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(s.Close)
	c, out := turn(t, zenAt(s.URL), "zen6", "openai", `{"model":"zen6","messages":[{"role":"user","content":"hi"}]}`, false, time.Now())
	if out != nil || answered(c) != http.StatusOK {
		t.Fatalf("attempts=%+v status %d", out, answered(c))
	}
	if !strings.Contains(sent(c), `"model":"zen-free"`) || header(c, FallbackHeader) != "zen-free" || header(c, FallbackReasonHeader) != onRoute {
		t.Errorf("body %s fallback %q reason %q, want the floor named", sent(c), header(c, FallbackHeader), header(c, FallbackReasonHeader))
	}
}

// A STREAM THAT BREAKS OFF AFTER ITS FIRST BYTES IS NEVER ASKED AGAIN, and never
// ends as if it were complete: it ends with an error event in its dialect. A stream
// that ends as it should gets nothing added.
func TestAStreamThatBreaksOffEndsWithAnError(t *testing.T) {
	cut := `data: {"id":"gen-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"half an ans"}}]}` + "\n\n"
	to := toStream()
	tk, _, _, _ := relayZenStream(to.w, strings.NewReader(cut), ourMark(), nil)
	got := to.String()
	if !strings.Contains(got, `"code":"upstream_cut"`) || !strings.Contains(got, "half an ans") {
		t.Fatalf("a cut chat stream ended:\n%s", got)
	}
	if tk.completion == 0 {
		t.Error("the text relayed before the cut is not billed")
	}

	whole := told(t, cut+"data: [DONE]\n\n", ourMark())
	if strings.Contains(whole, "upstream_cut") {
		t.Errorf("a complete chat stream was marked cut:\n%s", whole)
	}

	mk := ourMark()
	mk.speaks = messageShape
	anthropicCut := "event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"half"}}` + "\n\n"
	got = told(t, anthropicCut, mk)
	if !strings.Contains(got, "event: error\n") || !strings.Contains(got, `"type":"api_error"`) {
		t.Fatalf("a cut messages stream ended:\n%s", got)
	}
	if got := told(t, anthropicCut+"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", mk); strings.Contains(got, "event: error") {
		t.Errorf("a complete messages stream was marked cut:\n%s", got)
	}
	// One whose model said why it stopped is whole, though its [DONE] never came.
	stopped := cut + `data: {"id":"gen-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n"
	if got := told(t, stopped, ourMark()); strings.Contains(got, "upstream_cut") {
		t.Errorf("a stream cut after its model stopped was marked cut:\n%s", got)
	}
	// One the family ended with its own error is ended: nothing is added to it.
	if got := told(t, cut+`data: {"error":{"message":"upstream failed"}}`+"\n\n", ourMark()); strings.Contains(got, "upstream_cut") {
		t.Errorf("a stream the family ended with an error was marked cut again:\n%s", got)
	}
}

// told relays body through relayZenStream and returns what the client read.
func told(t *testing.T, body string, mk *mark) string {
	t.Helper()
	var out strings.Builder
	w := bufio.NewWriter(&out)
	relayZenStream(w, strings.NewReader(body), mk, nil)
	_ = w.Flush()
	return out.String()
}
