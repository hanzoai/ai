// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/object"
)

// errPartiallyWritten stops a retry that can no longer be made safely: bytes are
// already on the wire, so replaying the call would duplicate the client's
// output. Deliberately NOT transient — retryTransient sees a permanent error and
// stops immediately rather than sleeping first.
var errPartiallyWritten = errors.New("response partially written; cannot retry")

// served names the provider that answered: our route label for it, the host it
// answered from, and the row whose credential was spent. Three projections of
// ONE fact travelling together, so a caller cannot record the label while losing
// the address — or, worse, bill against a row that did not serve.
//
// A failed attempt carries a name and nothing else: nothing answered, and saying
// nothing is the honest form of that.
type served struct {
	name   string
	origin string
	// row is the provider that actually spent a credential. providerBYO must
	// read THIS and not the row auth resolved before the request moved: a
	// customer's own key is only "bring your own" if it is the key that paid.
	row *object.Provider
}

// ask is one text completion waiting to be served: what to send, where the
// answer goes, and who has already refused it.
//
// A struct rather than eleven positional arguments because two of them are
// []*model.RawMessage — history and knowledge — and in money code a silent
// argument swap between two same-typed slices is not a hazard worth keeping for
// the sake of a shorter call.
type ask struct {
	ctx   context.Context
	route *modelRoute
	org   string // ledger org, so a customer's own provider row wins where it exists
	model string // caller-facing model name, for the honest error

	// primary is the row auth already resolved for route.providerName. Reusing
	// it keeps the first attempt on exactly the credential the auth path chose;
	// re-resolving by name would quietly swap a customer's connected key for
	// the platform's.
	primary *object.Provider

	question  string
	history   []*model.RawMessage
	knowledge []*model.RawMessage
	lang      string

	writer io.Writer
	// sent reports whether any byte has reached the client. Once it has, this
	// request is committed and can never be moved.
	sent func() bool
	// prior are providers that already refused this request elsewhere — the
	// family pipe, typically. They are not asked again and they count against
	// the attempt cap.
	prior []attempt
}

// serve offers the completion to each capable provider in turn and returns the
// first real answer.
//
// The loop is the whole failover policy in one place:
//
//	provider fault -> record it, demote the vendor, offer it to the next one
//	request fault  -> stop; every vendor refuses this identically
//	nobody left    -> exhausted(), which names who was asked and why they said no
//
// It returns every attempt made, successful or not, so the caller can record
// the ones that failed. A failover nobody can see is half a fix: the empty
// account that caused it stays empty.
func (a ask) serve() (*model.ModelResult, served, []attempt, error) {
	tried := a.prior
	for _, c := range candidates(a.org, a.route, a.prior) {
		// The caller hung up. Offering the request to another vendor would spend
		// money answering an empty room, and would file a healthy vendor's name
		// against the client's disconnect.
		if err := a.context().Err(); err != nil {
			return nil, served{}, tried, err
		}
		res, row, err := a.call(c)
		if err == nil {
			if len(tried) > 0 {
				log.Warn("failover: model=%s served by %s after %d refusal(s) — %s",
					a.model, c.provider, len(tried), reasons(tried))
			}
			// The row travels with the answer because it is the row whose
			// credential was spent, and that is what decides whether this call
			// was the customer's own key or ours.
			// No mark: this path builds the answer from the provider SDK rather
			// than relaying an envelope, so nothing disclosed an upstream and the
			// host we dialled is the whole answer.
			return res, served{c.provider, originOf(row, nil), row}, tried, nil
		}

		at := attempt{
			provider: c.provider,
			upstream: c.upstream,
			origin:   originOf(row, nil),
			status:   upstreamHTTPStatus(err),
			fault:    faultOf(err),
			err:      err,
		}
		tried = append(tried, at)
		announce(a.model, at)

		if d := cooled.rest(a.org, c.provider, err); d > 0 {
			log.Warn("failover: demoting provider=%s for %s after status=%d", c.provider, d, at.status)
		}

		// The request is wrong, not the vendor. Every other vendor refuses it
		// the same way, so stop and hand the caller the real reason.
		if at.fault == faultRequest {
			return nil, served{name: c.provider}, tried, err
		}

		// Bytes reached the client during that attempt. Nothing can be replayed
		// now; report what we have rather than duplicating output.
		if a.sent != nil && a.sent() {
			log.Warn("failover: model=%s provider=%s failed after the stream opened — cannot move: %v",
				a.model, c.provider, err)
			return nil, served{name: c.provider}, tried, err
		}
	}
	return nil, served{}, tried, exhausted(a.model, tried)
}

// call offers the completion to ONE provider, riding out a transient refusal
// (a 429 "overloaded", a 503, a timeout) rather than giving up on it.
//
// A 429 does not mean "this cannot be served", it means "ask again shortly" —
// and we are better placed to do that than a client who can neither pick a
// different vendor nor stagger itself against every other client doing the
// same. We wait it out here; when waiting stops helping, serve() moves on.
func (a ask) call(c candidate) (*model.ModelResult, *object.Provider, error) {
	var res *model.ModelResult
	var row *object.Provider
	err := retryTransient(a.context(), currentRetryPolicy(), func() error {
		// Committed: bytes are on the wire and this can no longer be replayed.
		if a.sent != nil && a.sent() {
			return errPartiallyWritten
		}
		// Discard whatever a failed attempt left in the writer. Both writers
		// APPEND to an internal buffer, so without this the next provider's
		// answer is served glued to the dead one's half-sentence — a silent
		// corruption that reads like a model going mad.
		if r, ok := a.writer.(interface{ Reset() }); ok {
			r.Reset()
		}
		var e error
		res, row, e = callProvider(a.org, a.rowFor(c), c, a.question, a.writer, a.history, a.knowledge, a.lang)
		if e != nil && isTransientError(e) {
			log.Warn("retry: provider=%s is busy (%v) — holding the request rather than bouncing it to the client", c.provider, e)
		}
		return wrapUpstreamError(e)
	})
	return res, row, err
}

// context returns the caller's context, so a retry never outlives the request
// that asked for it. Background would make retryPolicy.waitBeforeRetry's
// deadline check vacuous and let a dead client's request keep sleeping.
func (a ask) context() context.Context {
	if a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}

// rowFor returns the pre-resolved provider row when the candidate IS the one
// auth resolved, and nil otherwise so callProvider resolves it for the org.
func (a ask) rowFor(c candidate) *object.Provider {
	if a.primary != nil && a.route != nil && c.provider == a.route.providerName {
		return a.primary
	}
	return nil
}

// announce puts a refusal where someone will see it. Level is chosen by who has
// to act on it: a busy vendor is routine, an empty account needs a human with a
// credit card, and a rejected credential needs one now.
func announce(model string, a attempt) {
	switch {
	case a.status == http.StatusUnauthorized:
		log.Error("provider=%s rejected our credential (401) serving model=%s — this key is dead or revoked, and the request was NOT sent elsewhere: %v",
			a.provider, model, a.err)
	case a.status == http.StatusPaymentRequired:
		log.Error("provider=%s is out of money (402) serving model=%s — top it up; failing over: %v",
			a.provider, model, a.err)
	default:
		log.Warn("failover: model=%s provider=%s refused status=%d fault=%s: %v",
			model, a.provider, a.status, a.fault, a.err)
	}
}

// reasons renders the refusals so far for one log line.
func reasons(tried []attempt) string {
	out := ""
	for i, a := range tried {
		if i > 0 {
			out += "; "
		}
		out += fmt.Sprintf("%s status=%d", a.provider, a.status)
	}
	return out
}

// callProvider asks one provider row for a completion and reports the row that
// answered. The row is resolved FOR THE ORG, so a customer that connected its
// own key spends its own key — on a fallback exactly as on the primary.
// Resolving globally here is how a request billed as "bring your own" comes to
// be served on the platform's credential.
//
// Indirected through a var so the failover loop can be exercised against
// scripted providers without a live DB or a live upstream, the same way
// orgAutoRoutingLookup is.
var callProvider = func(
	org string,
	row *object.Provider,
	c candidate,
	question string,
	writer io.Writer,
	history []*model.RawMessage,
	knowledge []*model.RawMessage,
	lang string,
) (*model.ModelResult, *object.Provider, error) {
	if row == nil {
		var err error
		row, err = object.GetModelProviderByNameForOrg(org, c.provider)
		if err != nil {
			return nil, nil, err
		}
	}
	if row == nil {
		// GetModelProviderByNameForOrg returns nil for BOTH a missing provider
		// and one an admin switched off. Word it "unavailable" so faultOfText
		// reads it as a provider fault and the next candidate is tried — this
		// is how a route's fallbacks stay honoured when the primary is toggled
		// off from /v1/admin/providers.
		return nil, nil, fmt.Errorf("provider %q is unavailable (disabled or not configured)", c.provider)
	}

	p := *row // copy: SubType is per-request, the row may be shared
	p.SubType = c.upstream

	modelProvider, err := p.GetModelProvider(lang)
	if err != nil {
		return nil, &p, err
	}

	result, err := modelProvider.QueryText(question, writer, history, "", knowledge, nil, lang)
	return result, &p, err
}

// recordRefusals writes one usage row per provider that refused, so a vendor
// that stopped serving appears in the ledger the team already reads rather than
// only in a log line nobody tails. That absence is the second half of the
// outage this exists to prevent: the failover worked, nobody noticed, and the
// account stayed empty.
//
// The rows bill nothing. recordUsage settles only "success" and is not called
// here; no budget hold is touched. The status is its own word — "failover", not
// "error" — so dashboards that count failures keep counting the same thing and
// a refused ATTEMPT on a request that ultimately succeeded is not filed as a
// failed request.
func (c *ApiController) recordRefusals(model string, tried []attempt, user *iam.User, premium, stream bool, requestId string, start time.Time) {
	if user == nil {
		return
	}
	for _, a := range tried {
		rec := &usageRecord{
			Owner:     c.billingOrg(user),
			Model:     model,
			Provider:  a.provider,
			Origin:    a.origin,
			Premium:   premium,
			Stream:    stream,
			Status:    "failover",
			ErrorMsg:  a.err.Error(),
			ClientIP:  c.Ctx.Request.RemoteAddr,
			RequestID: requestId,
		}
		rec.bind(c.Ctx.Request.Context(), user)
		recordTrace(c.Ctx.Request.Context(), rec, start)
	}
}
