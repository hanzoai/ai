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

// forward.go — a chat completion relayed as the caller wrote it.
//
// The text pipeline this replaces for OpenAI-compatible routes rebuilt every request
// from two of its parts: the last system message and the last user message. Earlier
// user turns were dropped without a word, assistant turns became a history the
// pipeline trimmed to fit, and every sampling parameter the caller set — temperature
// 0, a seed, a response format, stop sequences — gave way to the provider row's
// defaults, a default system prompt and a fixed token ceiling. What reached the model
// was a different conversation from the one the caller sent.
//
// A tool call or an image took a second path, which marshalled the request back out
// of go-openai's struct: `omitempty` dropped temperature 0 and top_p 0, every field
// the struct does not name was lost, and a refusal ended the request at the one
// provider it was sent to.
//
// So a request whose route speaks OpenAI's dialect is sent the caller's own body. Four
// things in it are ours to write, and only these: the model (each provider's own id
// for the SKU), the completion ceiling where the reservation holds less than the
// caller asked for, include_usage on a stream so the answer can be billed, and — when
// the caller asked for retrieval — the retrieved knowledge as a leading system
// message. A short list of fields is not sent on (withheld): our own API's, and the
// ones that would buy something on our account the SKU's price does not cover.
// Everything else arrives upstream as it left the caller.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"time"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/upstream"
	openai "github.com/hanzoai/go-openai"
)

// chatRequest is a chat request as the handler reads it. Two fields that go-openai
// types more narrowly than the API does are held raw: a response_format carrying a
// json_schema (its schema is a json.Marshaler, which nothing can be decoded into) and
// a stop given as one string. Typed, either refused a request every vendor accepts
// with a 400 before it was routed. The handler reads neither; the relay sends the
// caller's own bytes for both.
type chatRequest struct {
	openai.ChatCompletionRequest
	ResponseFormat json.RawMessage `json:"response_format,omitempty"`
	Stop           json.RawMessage `json:"stop,omitempty"`
}

// withheld are the request fields the relay does not send on, for one of two reasons.
//
// Three are this API's own, not any vendor's: fast mode (fast.go) and retrieval
// (chat_retrieval.go). This handler has read them by the time the body leaves, and a
// vendor that checks its parameters — OpenAI does — refuses a name it does not define.
//
// The rest change what a vendor charges US without changing what the SKU is sold
// for: another model (models), another endpoint (provider, transforms, route), a paid
// tool (plugins, web_search_options), a dearer queue (service_tier). Sent on, each
// would be bought on our account at a price nobody quoted the caller. The family pipe
// keeps the same fields back for the same reason (familyFields).
var withheld = []string{
	"fast", "retrieval", "retrieval_store",
	"models", "provider", "transforms", "route", "plugins", "web_search_options", "service_tier",
}

// pass is one chat completion on its way through the relay: what the caller sent,
// what was decided for it, and who pays.
type pass struct {
	// req is the request as this handler decided it — the SKU after auto-routing and
	// the public lane, and the completion ceiling the hold was reserved for. body is
	// the request as the caller WROTE it, and is what goes upstream.
	req  *openai.ChatCompletionRequest
	body []byte

	// primary is the row auth resolved for the route's own provider. route is nil for
	// a model the route table does not carry, which primary serves alone.
	primary *object.Provider
	route   *modelRoute
	// prior are providers that already refused this request — the family pipe. They
	// are not asked again, and their refusals are already recorded.
	prior []attempt

	knowledge []*model.RawMessage
	// prompt is the prompt as the reservation measured it, over every message. It is
	// read only when an upstream answers without saying what it used.
	prompt int

	user    *iam.User
	premium bool
	hold    *budgetHold
	id      string
	start   time.Time
	// judge scores a finished answer for the router, or is nil for an answer it does
	// not score. It is called from inside a stream writer, so it closes over values.
	judge func(answer string)
}

// relays reports whether a provider row can be sent a chat body as the caller wrote
// it: it has an OpenAI-compatible chat address, and it does not speak Anthropic's own
// dialect, which the relay does not translate.
func relays(p *object.Provider) bool {
	return model.Upstream(p.Type) != model.Anthropic && upstream.Endpoint(p, "chat/completions") != ""
}

// errUnrelayable is a candidate whose row the relay cannot send an OpenAI chat body
// to. It is not a refusal — nothing was asked — so it is passed over rather than
// recorded, and a route whose alternates are all of this kind ends at exhausted().
var errUnrelayable = errors.New("provider speaks no OpenAI chat dialect")

// dial sends one attempt to one provider: the row the candidate names, resolved for
// the org the way callProvider resolves it — so a customer that connected its own
// key spends its own key, on a fallback exactly as on the primary — and the body
// built for that row.
//
// The client is zenPipeClient's: a bound on the time to the response HEADERS and none
// on the body, because a streamed answer is long by design and a whole-request
// timeout cuts it off mid-sentence.
//
// A var so the failover loop can be driven against scripted providers, the way
// callProvider is.
var dial = func(ctx context.Context, org string, row *object.Provider, c candidate, body func(*object.Provider) []byte) (*http.Response, *object.Provider, error) {
	if row == nil {
		var err error
		if row, err = object.GetModelProviderByNameForOrg(org, c.provider); err != nil {
			return nil, nil, err
		}
		if row == nil {
			return nil, nil, unavailable(c.provider)
		}
	}
	p := *row // copy: SubType is per request, and Azure addresses its deployment by it
	p.SubType = c.upstream
	if !relays(&p) {
		return nil, &p, errUnrelayable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream.Endpoint(&p, "chat/completions"), bytes.NewReader(body(&p)))
	if err != nil {
		return nil, &p, err
	}
	req.Header.Set("Content-Type", "application/json")
	upstream.Authorize(req, &p)
	resp, err := zenPipeClient.Do(req)
	return resp, &p, err
}

// draft is the caller's request as it will leave: their fields, with ours written
// over the few the relay decides. Each provider is sent it under its own id for the
// model (body).
type draft struct {
	fields map[string]json.RawMessage
	// ceiling is the completion ceiling the hold covers, written only when the caller
	// named none — which key a vendor reads for it is a fact about the vendor.
	ceiling int
}

// outbound reads the caller's body into the draft every attempt is built from.
func outbound(raw []byte, req *openai.ChatCompletionRequest, knowledge []*model.RawMessage) (draft, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return draft{}, err
	}
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	for _, k := range withheld {
		delete(fields, k)
	}

	// THE CEILING IS LOWERED, NEVER RAISED, AND UNDER THE CALLER'S OWN KEY. The hold
	// was reserved for req.MaxTokens completion tokens (clampMaxTokens, and the public
	// lane's cap), so an upstream allowed more could settle past it. A caller who
	// asked for less keeps what they asked for. A caller who named neither key gets
	// the reserved ceiling under the key the vendor reads (body).
	d := draft{fields: fields}
	if limit := req.MaxTokens; limit > 0 {
		named := false
		for _, k := range []string{"max_tokens", "max_completion_tokens"} {
			v, ok := fields[k]
			if !ok {
				continue
			}
			named = true
			var n int
			if json.Unmarshal(v, &n) != nil || n <= 0 || n > limit {
				fields[k] = json.RawMessage(strconv.Itoa(limit))
			}
		}
		if !named {
			d.ceiling = limit
		}
	}

	// A streamed answer is billed from the usage chunk the vendor sends only when it
	// is asked to. Whether the CALLER sees that chunk is decided on the way out
	// (streamCaptureUsage); every other stream option they set is theirs.
	if req.Stream {
		opts := map[string]json.RawMessage{}
		if v, ok := fields["stream_options"]; ok {
			_ = json.Unmarshal(v, &opts)
			if opts == nil {
				opts = map[string]json.RawMessage{}
			}
		}
		opts["include_usage"] = json.RawMessage("true")
		b, err := encode(opts)
		if err != nil {
			return draft{}, err
		}
		fields["stream_options"] = b
	}

	// Retrieval, when the caller asked for it, arrives as one system message ahead of
	// their conversation — which is otherwise sent exactly as they wrote it.
	if len(knowledge) > 0 {
		var msgs []json.RawMessage
		if raw, ok := fields["messages"]; ok {
			if err := json.Unmarshal(raw, &msgs); err != nil {
				return draft{}, err
			}
		}
		parts := make([]string, 0, len(knowledge))
		for i, k := range knowledge {
			parts = append(parts, fmt.Sprintf("Knowledge %d: %s", i+1, k.Text))
		}
		sys, err := encode(map[string]string{"role": "system", "content": strings.Join(parts, "\n\n")})
		if err != nil {
			return draft{}, err
		}
		b, err := encode(append([]json.RawMessage{sys}, msgs...))
		if err != nil {
			return draft{}, err
		}
		fields["messages"] = b
	}
	return d, nil
}

// body is the draft as one provider is sent it: under that provider's own id for the
// model, and with the reserved ceiling under the key it reads when the caller named
// none. OpenAI refuses max_tokens on its reasoning models and reads
// max_completion_tokens on every chat model; the vendors that copy its dialect read
// max_tokens.
func (d draft) body(upstreamModel string, row *object.Provider) []byte {
	out := make(map[string]json.RawMessage, len(d.fields)+2)
	maps.Copy(out, d.fields)
	out["model"] = text(upstreamModel)
	if d.ceiling > 0 {
		key := "max_tokens"
		if model.Upstream(row.Type) == model.OpenAI {
			key = "max_completion_tokens"
		}
		out[key] = json.RawMessage(strconv.Itoa(d.ceiling))
	}
	// Every value came out of json.Unmarshal or was written above, so this encodes.
	b, _ := encode(out)
	return b
}

// declined is an upstream's non-200 as the error the cascade reads. The status leads
// its text the way the provider SDK's own errors carry it, because that text is what
// retryTransient reads to decide a 429 or a 5xx is worth waiting out.
//
// Our own spend gate, relayed by a service that fronts for us, keeps its code and its
// words: that refusal is the caller's debt, and it reaches them as it was written.
func declined(status int, body []byte) error {
	said := upstreamErrorMessage(body)
	if billingNotice(body) {
		return &apiError{status: status, msg: said, code: object.CodeInsufficientBalance}
	}
	return &apiError{status: status, msg: fmt.Sprintf("%d %s", status, said)}
}

// call offers the request to one provider, riding out a transient refusal as
// ask.call does, and hands back an answer only when it is one: a 200 whose stream has
// begun (opening), or a buffered body read to its end. Anything else comes back as
// the refusal it is, with nothing written — which is what keeps it movable.
func (p pass) call(ctx context.Context, org string, row *object.Provider, c candidate, d draft) (resp *http.Response, whole []byte, used *object.Provider, err error) {
	body := func(u *object.Provider) []byte { return d.body(c.upstream, u) }
	err = retryTransient(ctx, currentRetryPolicy(), func() error {
		r, u, e := dial(ctx, org, row, c, body)
		used = u
		if e != nil {
			return wrapUpstreamError(e)
		}
		if p.req.Stream {
			r = opening(r)
		}
		if r.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(r.Body)
			r.Body.Close()
			return declined(r.StatusCode, b)
		}
		if !p.req.Stream {
			b, e := io.ReadAll(r.Body)
			r.Body.Close()
			if e != nil {
				// Cut off on the way to us. Nothing has reached the caller, so this is
				// still movable.
				return e
			}
			whole = b
		}
		resp = r
		return nil
	})
	return resp, whole, used, err
}

// rowFor is ask.rowFor: the row auth resolved, for the provider it was resolved for,
// and nil for every other candidate so dial resolves that one for the org.
func (p pass) rowFor(c candidate) *object.Provider {
	if p.route == nil || c.provider == p.route.providerName {
		return p.primary
	}
	return nil
}

// forward relays p to the first provider on its route that answers, and reports
// whether a stream took the hold with it — a streamed answer settles from inside its
// own writer, which runs after the handler has returned.
//
// It is the failover loop ask.cascade runs for the text pipeline, applied to a body
// rather than a question:
//
//	provider fault -> record it, rest the account, offer it to the next provider
//	request fault  -> stop, and relay the upstream's own status
//	nobody left    -> exhausted()
//
// Every one of those is decided on the status, before a byte reaches the caller. A
// stream is judged by its opening frames too (opening), because a vendor that
// streams answers 200 before it has an answer. Once the answer begins, the request
// belongs to that provider and nothing after can move it.
//
// THE RELAY DOES NOT RACE. Fast mode races the text pipeline's writers, which carry
// a beaten provider's partial answer to the ledger. A relayed attempt is an HTTP
// response committed at its status, and racing several would mean reading every
// loser to its end to learn what it cost. A fast request takes the cascade here, and
// the wider reservation widthFor made for it covers the one attempt it makes.
func (c *ApiController) forward(p pass) bool {
	sku := p.req.Model
	d, err := outbound(p.body, p.req, p.knowledge)
	if err != nil {
		c.ResponseFailure(modelError("Failed to parse request: %s", err.Error()))
		return false
	}
	// What the answer reads from the request, read now: a streamed answer is billed
	// after this handler returns and fiber has released the request (base.go).
	snap := c.takeSnapshot(p.user)
	ctx := c.Context()

	queue := candidates(snap.org, p.route, p.prior)
	if p.route == nil {
		queue = []candidate{{p.primary.Name, p.primary.SubType}}
	}

	tried := p.prior
	by := ""
	for _, cand := range queue {
		// The caller hung up. Offering the request to another vendor would spend
		// money answering an empty room.
		if err = ctx.Err(); err != nil {
			break
		}
		resp, whole, row, e := p.call(ctx, snap.org, p.rowFor(cand), cand, d)
		if e == nil {
			if len(tried) > 0 {
				log.Warn("failover: model=%s served by %s after %d refusal(s) — %s",
					sku, cand.provider, len(tried), reasons(tried))
			}
			if n := len(p.prior); len(tried) > n {
				recordRefusals(snap, sku, tried[n:], p.user, p.premium, p.req.Stream, p.id, p.start)
			}
			return c.deliver(p, cand, row, resp, whole, snap)
		}
		if errors.Is(e, errUnrelayable) {
			log.Warn("relay: model=%s provider=%s speaks no OpenAI chat dialect and is passed over", sku, cand.provider)
			continue
		}
		err, by = e, cand.provider
		// The caller's own debt, relayed back to us. No vendor refused anything and
		// none will fix it, so nobody is rested and nobody else is asked.
		if codeOf(e) == object.CodeInsufficientBalance {
			break
		}
		at := attempt{
			provider: cand.provider,
			upstream: cand.upstream,
			origin:   originOf(row, nil),
			status:   upstreamHTTPStatus(e),
			fault:    faultOf(e),
			err:      e,
		}
		tried = append(tried, at)
		announce(sku, at)
		if rest := cooled.rest(snap.org, cand.provider, e); rest > 0 {
			log.Warn("failover: demoting provider=%s for %s after status=%d", cand.provider, rest, at.status)
		}
		// The request is wrong, not the vendor. Every other vendor refuses it the
		// same way, so stop and hand the caller the real reason.
		if at.fault == faultRequest {
			break
		}
		err, by = nil, ""
	}
	if err == nil {
		err = exhausted(sku, tried)
	}
	if n := len(p.prior); len(tried) > n {
		recordRefusals(snap, sku, tried[n:], p.user, p.premium, p.req.Stream, p.id, p.start)
	}

	// The outcome's own row, as the text pipeline files one: named for the provider
	// that ended the request, if one did, and priced at what it ran (spent).
	if p.user != nil {
		rec := &usageRecord{
			Owner:     snap.org,
			Model:     sku,
			Provider:  by,
			Premium:   p.premium,
			Stream:    p.req.Stream,
			Status:    "error",
			ErrorMsg:  err.Error(),
			ClientIP:  snap.ip,
			RequestID: p.id,
		}
		rec.PromptTokens, rec.CompletionTokens = spent(nil, sku, p.prompt, "", err)
		rec.TotalTokens = rec.PromptTokens + rec.CompletionTokens
		rec.bind(snap.ctx, p.user)
		rec.BYO, rec.Account = providerBYO(p.primary, p.user)
		recordUsage(rec)
		recordTrace(snap.ctx, rec, p.start)
	}

	// Our own spend gate keeps its 402: the debt is the caller's (relay). Every other
	// failure is a model call nothing answered.
	if codeOf(err) == object.CodeInsufficientBalance {
		c.ResponseFailure(err)
	} else {
		c.ResponseModelFailure(err)
	}
	return false
}

// deliver hands the caller the answer one provider gave, stamped as ours, and bills
// it: to the SKU the caller asked for, at the tokens the upstream reported, with a
// cached prompt priced as cached.
func (c *ApiController) deliver(p pass, cand candidate, row *object.Provider, resp *http.Response, whole []byte, snap snapshot) bool {
	sku := p.req.Model
	mk := &mark{id: "chatcmpl-" + p.id, model: sku, seller: seller(row, p.user)}

	// settle runs where the answer is finished — for a stream, inside its writer — so
	// it reads the snapshot and p, and never the controller.
	settle := func(t tokens, text string) {
		if t.prompt() == 0 && t.completion == 0 {
			// The upstream said nothing about what it used. The prompt was measured on
			// the way in and the answer is the text it sent, so neither is invented.
			t.fresh = p.prompt
			t.completion, _ = model.GetTokenSize(sku, text)
		}
		if p.user != nil {
			rec := &usageRecord{
				Owner:            snap.org,
				Organization:     p.user.Owner,
				Model:            sku,
				Provider:         cand.provider,
				Origin:           originOf(row, mk),
				CostNanoExact:    mk.cogs(),
				PromptTokens:     t.fresh,
				CacheReadTokens:  t.cached,
				CompletionTokens: t.completion,
				TotalTokens:      t.prompt() + t.completion,
				ReasoningTokens:  t.reasoning,
				Currency:         "USD",
				Premium:          p.premium,
				Stream:           p.req.Stream,
				Status:           "success",
				ClientIP:         snap.ip,
				RequestID:        p.id,
			}
			rec.bind(snap.ctx, p.user)
			// Whose key paid is a property of the row that served, not of the one
			// auth resolved before the request moved.
			rec.BYO, rec.Account = providerBYO(row, p.user)
			recordUsage(rec)
			recordTrace(snap.ctx, rec, p.start)
		}
		// The same model and the same split the row above is priced from, so the
		// hold settles what the ledger debits.
		p.hold.settle(calculateCostCentsWithCache(sku, t.fresh, t.completion, t.cached, 0))
		if p.judge != nil {
			p.judge(text)
		}
	}

	// A reasoning-inlining upstream (DeepSeek) has its leading <think></think> block
	// stripped from what is forwarded; billing counts what it sent.
	var strip *model.ReasoningStripper
	if model.InlinesReasoning(cand.upstream) {
		strip = &model.ReasoningStripper{}
	}

	if p.req.Stream {
		c.SetHeader("Content-Type", "text/event-stream")
		c.SetHeader("Cache-Control", "no-cache")
		c.SetHeader("Connection", "keep-alive")
		c.Status(http.StatusOK)
		wants := p.req.StreamOptions != nil && p.req.StreamOptions.IncludeUsage
		// The upstream body travels into the writer, which outlives this call, and is
		// closed there. The hold travels with it: released on the way out if the
		// writer ends without an answer, a no-op once settle has run.
		upstreamBody := resp.Body
		_ = c.SendStreamWriter(func(w *bufio.Writer) {
			defer upstreamBody.Close()
			defer p.hold.settle(0)
			settle(streamCaptureUsage(upstreamBody, w, func() { _ = w.Flush() }, wants, strip, mk))
		})
		return true
	}

	// What goes out is ours (envelope.go); what came in is what the billing reads.
	out := mk.stamp(whole)
	if strip != nil {
		out = stripReasoningBody(out)
	}
	var t tokens
	sniffZenUsage(whole, &t)
	settle(t, answerText(whole))
	c.answerBody(out)
	return false
}

// answerText is what a buffered chat answer said: its content and its tool-call
// arguments, the same text a streamed answer accumulates for the tokenizer and the
// judge.
func answerText(body []byte) string {
	var r struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					Function struct {
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(body, &r) != nil {
		return ""
	}
	var sb strings.Builder
	for _, ch := range r.Choices {
		sb.WriteString(ch.Message.Content)
		for _, tc := range ch.Message.ToolCalls {
			sb.WriteString(tc.Function.Arguments)
		}
	}
	return sb.String()
}
