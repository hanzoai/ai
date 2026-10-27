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

// floor.go — an Enso or Zen model never stops.
//
// A chat request naming an Enso or Zen id that cannot be served as asked, for a
// reason that is not the caller's, is served as the same request by the free model
// of that id's family: enso-free for an Enso id, zen-free for a Zen id. That model
// is the id's floor. Fall is the one decision; every place a request can be
// refused or fail asks it — the balance gate in front of the handlers (routers),
// the handlers' own gate and reservation, and the family pipe when the family
// cannot answer before a byte reached the caller.
//
// What falls:
//
//	the wallet or the plan does not pay: insufficient_balance, balance_unavailable,
//	paid_plan_required, plan_allowance_used, model_cap, free_plan_cap,
//	usage_cap_exceeded, paid_lane_full, and our own cash breaker (supply_unavailable);
//	the model cannot answer: its vendor failed (5xx), is rate limited (429), timed
//	out or could not be reached, our account with it is spent (402), our credential
//	for it was refused, its route is unpriced, the paid lane is closed to the
//	caller, its family is not configured, or our family answered with its masked
//	"could not serve this request".
//
// What stands, as the caller's: a bad credential (401), a malformed request (400),
// a model the caller may not use (403), a limit the caller set on its own key
// (key_budget, key_model), the free lane's own allowance (allowance_spent), a
// policy that cannot be read (usage_unavailable), and any refusal the floor itself
// meets. A floor never falls, so a request falls at most once: asked → floor.
//
// What the caller is told: X-Hanzo-Fallback names the floor, X-Hanzo-Fallback-Reason
// names why, and the answer's `model` is the floor. It bills nothing.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	metric "github.com/luxfi/metric"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
	"github.com/zap-proto/zip"
)

// The headers a request served on its floor carries.
const (
	FallbackHeader       = "X-Hanzo-Fallback"
	FallbackReasonHeader = "X-Hanzo-Fallback-Reason"
)

// paidByHeader says who paid for an answer (the gate's usage); a floor's is nobody.
const paidByHeader = "X-Hanzo-Paid-By"

// ReasonRoute is why a request falls when every rung of its model refused inside the
// family serving it, and the family's own catalog answered on the floor: the family
// says so by naming the floor in X-Hanzo-Served.
const ReasonRoute = "route_refused"

// The reasons a request falls when no refusal code names one: what the model
// could not do. The gate's own codes (object.Code*) name the rest.
const (
	onTimeout     = "timeout"           // the vendor did not answer in time
	onUnreachable = "unreachable"       // no answer at all: a dial, a reset
	onBusy        = "rate_limited"      // 429 from every account asked
	onUnfunded    = "upstream_unfunded" // our account with the vendor is spent (402)
	onCredential  = "credential"        // the vendor refused our credential
	onCapacity    = "capacity"          // 503: no rung or key could take it
	onUpstream    = "upstream_error"    // any other 5xx
	onMasked      = "masked"            // our family: the model asked cannot take the request (masks)
	onUnpriced    = "unpriced"          // its route costs more than it bills, or cannot be read
	onClosed      = "lane_closed"       // the paid lane is closed to the caller
	onMissing     = "unconfigured"      // its family is not configured
	onCut         = "upstream_cut"      // the answer broke off before a byte was relayed
	onRoute       = ReasonRoute
	onSupply      = codeSupply // our own cash breaker holds paid inference
	onFull        = ReasonFull // the paid lane has no room for the call
	onBalance     = object.CodeInsufficientBalance
)

// fallen counts the requests served on their floor, by floor and reason. It is the
// rule's own meter: an operator reads how often each family stops, and why, from
// rate(cloud_fallback[5m]) on GET /v1/metrics.
var fallen = metric.NewCounterVec(metric.CounterOpts{
	Name: "cloud_fallback",
	Help: "Requests served by their family's free model because the model asked could not serve them, by floor and reason",
}, []string{"floor", "reason"})

// Floor is the free model of model's Hanzo family — enso-free for an Enso id,
// zen-free for a Zen id — or "" when it has none: a third-party id, a decision
// route, or an id that already is a free model's name.
func Floor(model string) string {
	f := familyNamed(FamilyOf(model))
	if f == nil || f.freeName == "" {
		return ""
	}
	m := strings.ToLower(strings.TrimSpace(model))
	if m == strings.ToLower(f.freeName) || m == freeID {
		return ""
	}
	return f.freeName
}

// falling are the reasons a request falls (see the file comment). Anything else is
// the caller's to clear and stands.
var falling = map[string]bool{
	object.CodeInsufficientBalance: true,
	object.CodeBalanceUnavailable:  true,
	object.CodePaidPlan:            true,
	object.CodePlanAllowance:       true,
	object.CodeModelCap:            true,
	object.CodeFreePlanCap:         true,
	object.CodeUsageCap:            true,
	onFull:                         true,
	onSupply:                       true,
	onTimeout:                      true,
	onUnreachable:                  true,
	onBusy:                         true,
	onUnfunded:                     true,
	onCredential:                   true,
	onCapacity:                     true,
	onUpstream:                     true,
	onMasked:                       true,
	onUnpriced:                     true,
	onClosed:                       true,
	onMissing:                      true,
	onCut:                          true,
	onRoute:                        true,
}

// Falls reports whether a request refused or failed for why is served on its
// floor rather than refused.
func Falls(why string) bool { return falling[why] }

// Fall is the rule: the model that serves a request for model that could not be
// served for why, and true when that is model's floor. False leaves the refusal
// standing — model has no floor, or why is the caller's.
func Fall(model, why string) (string, bool) {
	to := Floor(model)
	if to == "" || !Falls(why) {
		return "", false
	}
	return to, true
}

// Fell says on the answer that it is the floor's: the model that answers and why,
// that it was served on the free lane and paid for by nobody, and counts it. Set
// before a byte of the answer is written. The gate, which asks the floor again from
// the top, then states the floor's own lane and payer over these.
func Fell(c *zip.Ctx, from, to, why string) {
	c.SetHeader(FallbackHeader, to)
	c.SetHeader(FallbackReasonHeader, why)
	// The floor is served free, and a free route is free because its vendor may keep
	// what it carried: said on every fallen answer (openrouterTerms).
	c.SetHeader(headerCollection, collectionAllow)
	h := &c.Fiber().Response().Header
	if len(h.Peek(LaneHeader)) > 0 {
		c.SetHeader(LaneHeader, "free")
	}
	h.Del(LaneTokensHeader)
	if len(h.Peek(paidByHeader)) > 0 {
		c.SetHeader(paidByHeader, object.PaysFree)
	}
	fallen.WithLabelValues(to, why).Inc()
	log.Info("fallback: %s served by %s (%s)", from, to, why)
}

// cause names why a family could not serve a request, from the error it gave: the
// status first, the text only where there is no status, as faultOf reads them.
func cause(err error) string {
	st := upstreamHTTPStatus(err)
	msg := strings.ToLower(err.Error())
	switch {
	case st == 0 && has(msg, []string{"timeout", "deadline exceeded"}):
		return onTimeout
	case st == 0:
		return onUnreachable
	case st == http.StatusTooManyRequests:
		return onBusy
	case st == http.StatusPaymentRequired || broke(err, msg):
		return onUnfunded
	case st == http.StatusUnauthorized, st == http.StatusForbidden:
		return onCredential
	case st == http.StatusRequestTimeout, st == http.StatusGatewayTimeout:
		return onTimeout
	case st == http.StatusServiceUnavailable:
		return onCapacity
	case st >= 500:
		return onUpstream
	case masked(err):
		return onMasked
	}
	return ""
}

// masks are the sentences our own family service (hanzo-inc/engine, relay.rs and
// serve.rs) answers when the model asked cannot take a request that another model
// may: its vendor's refusal hidden (`could not serve`), the field it named (`does
// not take the request's tool_choice`), or a forced tool choice none of its rungs
// takes (`cannot be made to call a tool`).
var masks = []string{
	"the model could not serve this request",
	"this model does not take the request's",
	"cannot be made to call a tool",
}

// masked reports our family's refusal of a request the model asked cannot take: a
// 400 or 404 saying one of masks. A 403 or 422 so worded is a moderation or a content
// refusal, which every model would answer alike, so it stands like every other 4xx
// that describes the request.
func masked(err error) bool {
	switch upstreamHTTPStatus(err) {
	case http.StatusBadRequest, http.StatusNotFound:
		said := strings.ToLower(err.Error())
		for _, m := range masks {
			if strings.Contains(said, m) {
				return true
			}
		}
	}
	return false
}

// reason names why a handler's own gate refused a request, from its error: the
// code it carries, or "" for a refusal that is the caller's (401, 400, 403).
func reason(err error) string {
	switch code := codeOf(err); {
	case code != "":
		return code
	case statusOf(err) == http.StatusPaymentRequired:
		return onBalance
	}
	return ""
}

// fall serves a request its handler's own gate refused (err) on its floor: the body
// names the floor and the answer says so (Fell). It reports the model now asked
// for, and false when the refusal stands — a path that is not a conversation, a
// model with no floor, or a refusal that is the caller's.
func (c *ApiController) fall(model string, err error) (string, bool) {
	if err == nil || !ChatPath(c.Path()) {
		return "", false
	}
	why := reason(err)
	to, ok := Fall(model, why)
	if !ok {
		return "", false
	}
	body, ok := WithModel(c.Body(), to)
	if !ok {
		return "", false
	}
	c.Fiber().Request().SetBody(body)
	c.Fiber().Request().Header.Del("Content-Encoding")
	Fell(c.Ctx, model, to, why)
	c.Locals(fellKey{}, true)
	return to, true
}

// fellKey marks a request its handler's own gate moved to its floor (fall).
type fellKey struct{}

// held holds a request its handler moved to its floor to the free allowance, as the
// balance gate holds one it moved there: a floor costs nothing, so the allowance is
// its one bound, counted by the payer u's person as the gate counts it. It reports
// false when it answered the request itself, in dialect: the allowance is spent (429),
// or cannot be read (503). The unit is the admission's, as the allowance's own rule
// says (object.SpentFunc). Any other request is not asked about.
func (c *ApiController) held(u *iam.User, dialect string) bool {
	if fell, _ := c.Locals(fellKey{}).(bool); !fell {
		return true
	}
	return c.allowance(u, dialect)
}

// allowance takes one call of payer u's free allowance for a request about to be
// served on its floor, as the gate takes one for a free model. It reports false when
// it answered the request itself, in dialect: spent (429) or unreadable (503).
func (c *ApiController) allowance(u *iam.User, dialect string) bool {
	spent := object.Spent()
	if spent == nil || u == nil {
		return true
	}
	org := c.billingOrg(u)
	out, err := spent(zip.WithCaller(c.Context(), zip.CallerOf(c.Forward())), u.PayerSubject(org), org)
	status, msg, body := 0, "", []byte(nil)
	switch {
	case err != nil:
		log.Warn("allowance: unreadable for a fallen request org=%s: %v", org, err)
		n := object.UsageUnavailable()
		status, msg, body = n.Status, n.Message, n.ErrorJSON()
	case out.Spent:
		n := object.AllowanceSpent(c.Host(), org, out)
		if wait := n.RetryAfter(time.Now()); wait > 0 {
			c.SetHeader("Retry-After", strconv.FormatInt(wait, 10))
		}
		status, msg, body = n.Status, n.Message, n.ErrorJSON()
	default:
		return true
	}
	if dialect == "anthropic" {
		c.respondAnthropicError(anthropicErrorTypeForStatus(status), msg, status)
		return false
	}
	c.SetHeader("Content-Type", "application/json")
	_ = c.Bytes(status, body)
	return false
}

// toolsHeader says what a floor was asked in place of a forced tool choice it may
// not honor: "required" (the named tool, alone, must be called) or "auto" (the
// model was told in words to call a tool). Absent when the choice went as sent.
const toolsHeader = "X-Hanzo-Fallback-Tools"

// relax is body with its tool choice loosened one step, for a floor that may not
// force a tool, in the request's dialect ("openai" chat or "anthropic" messages).
// It never drops a tool the request could still call:
//
//	step 1: a choice naming one tool becomes "required" ("any") over that tool alone
//	        — the same demand, since only that tool can then be called;
//	step 2: "required" ("any") becomes "auto", with a sentence telling the model to
//	        answer by calling the tool (or one of the tools).
//
// did is the choice the floor is now asked ("required" or "auto"); ok is false when
// there is nothing to loosen at that step, and body is then returned as it came.
func relax(body []byte, dialect string, step int) ([]byte, string, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil || m == nil || len(m["tool_choice"]) == 0 {
		return body, "", false
	}
	choice := m["tool_choice"]
	var word string
	_ = json.Unmarshal(choice, &word)
	var named struct {
		Type     string `json:"type"`
		Name     string `json:"name"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	_ = json.Unmarshal(choice, &named)
	anthropic := dialect == "anthropic"
	set := func(k string, v any) {
		b, _ := json.Marshal(v)
		m[k] = b
	}
	switch {
	case step == 1 && !anthropic && named.Type == "function" && named.Function.Name != "":
		if !only(m, named.Function.Name, false) {
			return body, "", false
		}
		set("tool_choice", "required")
		set("parallel_tool_calls", false)
	case step == 1 && anthropic && named.Type == "tool" && named.Name != "":
		if !only(m, named.Name, true) {
			return body, "", false
		}
		set("tool_choice", map[string]any{"type": "any", "disable_parallel_tool_use": true})
	case step == 2 && !anthropic && word == "required":
		set("tool_choice", "auto")
		var msgs []json.RawMessage
		if json.Unmarshal(m["messages"], &msgs) != nil {
			return body, "", false
		}
		set("messages", after(msgs, instruct(m, false)))
	case step == 2 && anthropic && named.Type == "any":
		set("tool_choice", map[string]any{"type": "auto"})
		said := instruct(m, true)
		var text string
		var blocks []json.RawMessage
		switch {
		case len(m["system"]) == 0:
			set("system", said)
		case json.Unmarshal(m["system"], &text) == nil:
			set("system", text+"\n\n"+said)
		case json.Unmarshal(m["system"], &blocks) == nil:
			tail, _ := json.Marshal(map[string]string{"type": "text", "text": said})
			set("system", append(blocks, tail))
		default:
			return body, "", false
		}
	default:
		return body, "", false
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body, "", false
	}
	if step == 1 {
		return out, "required", true
	}
	return out, "auto", true
}

// after is msgs with said added after the caller's own instructions: at the end of
// its last system or developer message, so nothing of ours comes ahead of what the
// caller said. A request with none is given one system message, which precedes no
// instruction of the caller's.
func after(msgs []json.RawMessage, said string) []json.RawMessage {
	for i := len(msgs) - 1; i >= 0; i-- {
		var turn map[string]json.RawMessage
		if json.Unmarshal(msgs[i], &turn) != nil {
			continue
		}
		var role string
		_ = json.Unmarshal(turn["role"], &role)
		if role != "system" && role != "developer" {
			continue
		}
		var text string
		var parts []json.RawMessage
		switch {
		case json.Unmarshal(turn["content"], &text) == nil:
			turn["content"], _ = json.Marshal(text + "\n\n" + said)
		case json.Unmarshal(turn["content"], &parts) == nil:
			tail, _ := json.Marshal(map[string]string{"type": "text", "text": said})
			turn["content"], _ = json.Marshal(append(parts, tail))
		default:
			continue
		}
		out := append([]json.RawMessage(nil), msgs...)
		out[i], _ = json.Marshal(turn)
		return out
	}
	lead, _ := json.Marshal(map[string]string{"role": "system", "content": said})
	return append([]json.RawMessage{lead}, msgs...)
}

// only keeps, of the request's tools, the one named name, and reports whether the
// request offered it. anthropic says how a tool names itself: {"name"} there,
// {"function":{"name"}} in OpenAI's dialect.
func only(m map[string]json.RawMessage, name string, anthropic bool) bool {
	var tools []json.RawMessage
	if json.Unmarshal(m["tools"], &tools) != nil {
		return false
	}
	for _, t := range tools {
		if toolName(t, anthropic) == name {
			b, _ := json.Marshal([]json.RawMessage{t})
			m["tools"] = b
			return true
		}
	}
	return false
}

// toolName is the name one tool of a request declares.
func toolName(t json.RawMessage, anthropic bool) string {
	var d struct {
		Name     string `json:"name"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	_ = json.Unmarshal(t, &d)
	if anthropic {
		return d.Name
	}
	return d.Function.Name
}

// instruct is the sentence that asks a model, in words, for the tool call a
// request could no longer demand: the tool by name when there is one, else any of
// them.
func instruct(m map[string]json.RawMessage, anthropic bool) string {
	var tools []json.RawMessage
	_ = json.Unmarshal(m["tools"], &tools)
	if len(tools) == 1 {
		if n := toolName(tools[0], anthropic); n != "" {
			return "Answer by calling the tool " + n + "."
		}
	}
	return "Answer by calling one of the provided tools."
}

// shaped reports a refusal that describes the request's shape — malformed (400), a
// field the route does not take (404, as OpenRouter answers a tool choice no
// endpoint supports), or refused content (422) — which a looser tool choice may
// clear.
func shaped(err error) bool {
	switch upstreamHTTPStatus(err) {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity:
		return true
	}
	return false
}

// freeRoom is what a free route ai sends itself is given beyond a small output limit,
// as zen gives its free rungs: a model that reasons spends its first tokens thinking,
// and a probe's max_tokens (20, "reply OK") left it nothing to answer with, so the
// caller was handed an empty answer. A free route bills nothing.
const freeRoom = 1024

// room is body with each output limit it states under freeRoom lifted by freeRoom:
// the caller's limit stays the answer's, and the thinking before it gets its own.
func room(body []byte) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil || m == nil {
		return body
	}
	lifted := false
	for _, k := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		var v int
		if json.Unmarshal(m[k], &v) == nil && v > 0 && v < freeRoom {
			m[k], _ = json.Marshal(v + freeRoom)
			lifted = true
		}
	}
	if !lifted {
		return body
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}
