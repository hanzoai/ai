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
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// A refused completion has exactly one interesting property: whose fault it is.
// Every other decision — cascade or stop, demote or not, what the client is
// told — follows from that one value, which is why it is one value and not a
// substring check re-derived at each branch.
//
//	provider — the request is fine; THIS vendor cannot serve it right now.
//	           Out of money, rate limited, broken, unreachable. Somebody else
//	           can serve it, so ask somebody else.
//	request  — the request itself is wrong. Malformed body, unknown model, our
//	           credential rejected, content refused. Every vendor refuses it
//	           identically, so asking four more buys four more round trips,
//	           four more log lines, four more bills on anything metered at
//	           input, and the same answer. Stop.
type fault int

const (
	faultRequest fault = iota
	faultProvider
)

func (f fault) String() string {
	if f == faultProvider {
		return "provider"
	}
	return "request"
}

// maxProviders bounds how many vendors one completion may be offered to.
//
// Three, because three is the width of a declared route: object.ModelRoute
// carries a primary plus Fallback1 and Fallback2, so the bound is the schema's
// own shape rather than a number picked from the air. It matters because the
// per-provider retry budget (retryPolicy.attempts, 3 by default with backoff)
// multiplies: without a cap, a model configured with six fallbacks turns one
// slow afternoon into eighteen upstream calls and a minute of held request. The
// cap makes worst-case latency a property of this constant instead of a
// property of whatever an operator typed into models.yaml.
const maxProviders = 3

// How long a vendor that just refused is sorted to the back of the queue.
//
// Two values, because there are two kinds of refusal and they heal on different
// clocks. A 429 or a 503 clears in seconds — the vendor is busy, not broken.
// An empty account clears when a human notices and tops it up, which is minutes
// at best; re-asking it every request in the meantime means every request pays
// its round trip before getting a real answer, which is precisely the tax this
// exists to avoid.
const (
	coolBusy  = 15 * time.Second
	coolBroke = 5 * time.Minute
)

// credential is the thing whose health is being remembered: ONE tenant's account
// with one vendor.
//
// Not the vendor. "openai is out of money" is not a fact anybody can state —
// only "THIS account at openai is out of money" is, and which account a request
// spends is decided by its org, because that is what selects the row: an org
// that connected its own key spends its own key, everyone else spends ours.
// Keyed by vendor alone, one customer's empty connected account teaches this
// process that the vendor is unwell and every other tenant inherits the
// penalty — a lesson about a credential they do not hold and cannot top up.
//
// The org is therefore part of the key rather than a filter applied to it,
// which makes the leak unrepresentable instead of merely unlikely.
type credential struct {
	org      string
	provider string
}

// cooldown remembers, per credential, the instant it is worth asking again.
//
// In memory on purpose: it describes THIS process's recent experience of an
// upstream, it is worthless after a restart, and sharing it through a store
// would buy consistency nobody needs at the cost of a dependency on the request
// path. Each org learns its own lesson within one request of its own.
type cooldown struct {
	mu    sync.Mutex
	until map[credential]time.Time
}

var cooled = &cooldown{until: map[credential]time.Time{}}

// demote sorts one org's account with a provider to the back of its queue for d.
func (c *cooldown) demote(a credential, d time.Duration) {
	if a.provider == "" || d <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.until[a] = time.Now().Add(d)
}

// cooling reports whether this org's account with a provider refused recently
// enough that another provider should be preferred.
func (c *cooldown) cooling(a credential) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.until[a]
	if !ok {
		return false
	}
	if time.Now().Before(t) {
		return true
	}
	delete(c.until, a) // expired: forget it rather than grow forever
	return false
}

// rest records what one refusal says about the account that answered it, and
// reports how long that account now rests — 0 for a refusal that says nothing
// about it.
//
// One function because the failover loop and the family pipe ask the identical
// question, and two spellings of it are two places for the org to go missing.
// That is exactly how the penalty came to be filed against a vendor instead of
// against an account: the second copy simply did not mention the tenant.
func (c *cooldown) rest(org, provider string, err error) time.Duration {
	d := cooldownFor(err)
	c.demote(credential{org, provider}, d)
	return d
}

// forget clears the whole penalty box. Tests use it to start from a known
// state; nothing on the request path calls it.
func (c *cooldown) forget() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.until = map[credential]time.Time{}
}

// faultOf attributes a failed upstream call.
//
// The status the vendor answered with is the evidence, read from the TYPED
// error — go-openai returns *openai.APIError carrying HTTPStatusCode, and
// wrapUpstreamError restates it as *apiError. The text is consulted only for
// failures that never reached HTTP and so have no status to read: a dial error,
// a reset, a deadline. That ordering is the whole point — a status is a fact,
// a substring is a guess, and the guess is only allowed where the fact is
// absent.
func faultOf(err error) fault {
	if err == nil {
		return faultRequest
	}
	// Bytes are already on the wire. Whatever went wrong, this request can no
	// longer be moved, so the only honest answer is to stop.
	if errors.Is(err, errPartiallyWritten) {
		return faultRequest
	}
	if status := upstreamHTTPStatus(err); status != 0 {
		return faultOfStatus(status, err)
	}
	return faultOfText(err.Error())
}

// faultOfStatus attributes a refusal that arrived with an HTTP status.
func faultOfStatus(status int, err error) fault {
	switch status {
	case http.StatusPaymentRequired, // 402 — this account is out of money
		http.StatusRequestTimeout,  // 408 — the vendor gave up waiting on itself
		http.StatusTooManyRequests: // 429 — busy
		return faultProvider

	case http.StatusUnauthorized: // 401
		// OUR credential for this vendor is bad, revoked, or wrong-scoped.
		// Cascading would paper over it: traffic silently drains onto whichever
		// vendor still works, the dead key is never noticed, and the first
		// person to find out is whoever reads the bill. A revoked key and a
		// stolen key look identical from here, which is the other reason this
		// must surface rather than route around.
		return faultRequest

	case http.StatusForbidden: // 403
		// 403 splits, and the split is not cosmetic.
		//
		// "you may not use this key" is our misconfiguration — request fault,
		// same argument as 401.
		//
		// "this account may not use this MODEL" is a fact about the VENDOR's
		// catalogue, not about our credential. DigitalOcean answers exactly
		// this for the agreement-gated Anthropic models, and it is the reason
		// the do-ai -> anthropic fallback exists at all. The key is fine; the
		// shop does not stock the item. Provider fault.
		if lacksModel(strings.ToLower(err.Error())) {
			return faultProvider
		}
		return faultRequest

	case http.StatusBadRequest, // 400 — malformed; every vendor says the same
		http.StatusNotFound,              // 404 — the route names a model this vendor has not got
		http.StatusRequestEntityTooLarge, // 413
		http.StatusUnprocessableEntity:   // 422 — content refused
		return faultRequest
	}

	if status >= 500 {
		return faultProvider
	}

	// An unrecognised 4xx. Fail closed on cascading: we do not know what this
	// is, and finding out by paying four more vendors to tell us the same thing
	// is the expensive way to learn.
	return faultRequest
}

// moneyText, busyText and reachText are the refusals that arrive with no HTTP
// status to read — a vendor whose client stringifies its errors, or a transport
// failure that never got a response at all.
//
// Kept narrow and literal, because a substring is a guess and a wide guess is
// how "insufficient" comes to match "insufficient permissions" and quietly
// converts an authorization bug into a five-vendor spending spree. Each entry
// below is a phrase actually observed from an upstream, not a family of things
// one might say.
var (
	moneyText = []string{
		"402", "payment required",
		"insufficient credit",  // OpenRouter: "Insufficient credits."
		"insufficient balance", // DeepSeek
		"insufficient_quota",   // OpenAI
		"exceeded your current quota",
		"billing hard limit",
	}
	busyText = []string{
		"429", "rate limit", "too many requests", "overloaded",
		"500", "internal server error",
		"502", "bad gateway",
		"503", "service unavailable",
		"504", "gateway timeout",
	}
	reachText = []string{
		"connection refused", "connection reset", "no such host",
		"timeout", "deadline exceeded",
		"eof",         // upstream closed mid-response
		"unavailable", // callProvider's own word for a disabled or unconfigured row
	}
)

// faultOfText attributes a refusal that carries no status.
//
// The default is faultRequest: an error nobody here recognises does not get to
// spend money at three vendors on the theory that it might be transient.
func faultOfText(msg string) fault {
	msg = strings.ToLower(msg)
	// A vendor that does not carry the model says so in prose as often as it
	// says so with a status — DigitalOcean's agreement gate reaches us both
	// ways — so the same question is asked here as in the 403 branch, from the
	// same definition.
	if lacksModel(msg) {
		return faultProvider
	}
	for _, set := range [][]string{moneyText, busyText, reachText} {
		if containsAny(msg, set) {
			return faultProvider
		}
	}
	return faultRequest
}

// lacksModel reports the narrow refusal that means "this vendor does not carry
// this model for this account", as opposed to "this credential is not welcome".
// Measured phrasings only; msg must already be lowercased.
func lacksModel(msg string) bool {
	return strings.Contains(msg, "not available for your account") ||
		strings.Contains(msg, "model is not available") ||
		strings.Contains(msg, "does not have access to model")
}

// cooldownFor says how long the provider that answered this way should be
// sorted to the back, or 0 to leave it where it is.
//
// Separate from faultOf on purpose, because "should I ask somebody else THIS
// time" and "is this vendor unwell" are different questions with different
// answers. A vendor that does not stock a model is perfectly healthy: cascade
// this request, but do not push it down the queue for every other model it
// serves fine. A vendor that is out of money is unwell for everything.
func cooldownFor(err error) time.Duration {
	if faultOf(err) != faultProvider {
		return 0 // not the provider's fault; nothing to hold against it
	}
	msg := strings.ToLower(err.Error())
	if lacksModel(msg) {
		return 0 // healthy vendor, absent item
	}
	if upstreamHTTPStatus(err) == http.StatusPaymentRequired || containsAny(msg, moneyText) {
		return coolBroke
	}
	return coolBusy
}

func containsAny(msg string, set []string) bool {
	for _, s := range set {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// candidate is one provider that could serve this model, and the id to ask it
// for.
type candidate struct {
	provider string
	upstream string
}

// attempt is what one provider answered. status is 0 when the failure never
// reached HTTP.
type attempt struct {
	provider string
	upstream string
	origin   string
	status   int
	fault    fault
	err      error
}

// candidates orders the providers a route may be offered to, best first, and
// caps the queue.
//
// Two rules, in order:
//
//  1. A provider that already refused THIS request is dropped. It answered once;
//     asking it again inside the same request is the definition of waste.
//  2. A provider still cooling is moved to the BACK, not removed. Removing it
//     would mean a model whose only vendor is cooling gets refused outright,
//     trading a slow answer for no answer — and nothing would ever re-probe a
//     recovered vendor. Demotion is a latency preference, never an
//     authorization.
//
// The relative order of the declared route is otherwise preserved, so an
// operator's intent survives.
//
// org is whose queue this is. It selects which account's recent experience the
// resting rule reads, so a customer's own empty account never reorders anybody
// else's request.
func candidates(org string, route *modelRoute, prior []attempt) []candidate {
	if route == nil {
		return nil
	}
	refused := make(map[string]bool, len(prior))
	for _, a := range prior {
		refused[a.provider] = true
	}

	var ready, resting []candidate
	add := func(provider, upstream string) {
		if provider == "" || refused[provider] {
			return
		}
		c := candidate{provider, upstream}
		if cooled.cooling(credential{org, provider}) {
			resting = append(resting, c)
			return
		}
		ready = append(ready, c)
	}
	add(route.providerName, route.upstreamModel)
	for _, fb := range route.fallbacks {
		add(fb.providerName, fb.upstreamModel)
	}

	queue := append(ready, resting...)

	// The cap counts vendors asked across the WHOLE request, so a family pipe
	// that already refused spends one of the three.
	budget := maxProviders - len(prior)
	if budget < 0 {
		budget = 0
	}
	if len(queue) > budget {
		queue = queue[:budget]
	}
	return queue
}

// exhausted is what the client is told when every capable provider refused.
//
// It names each vendor asked and quotes the last reason, because "the model is
// unavailable" sends whoever reads it to the wrong place, and a bare 500 sends
// them nowhere.
//
// The status is 503 and deliberately NOT the upstream's own. An upstream 402
// means OUR account with that vendor is empty; forwarding a 402 to the customer
// tells them THEY owe money, which is false, and clients that act on 402 will
// go and try to fix a bill that is not theirs. 503 is the true statement: we
// could not serve this, it is our problem, try again.
func exhausted(model string, tried []attempt) error {
	if len(tried) == 0 {
		return &apiError{http.StatusServiceUnavailable,
			fmt.Sprintf("model %q: no provider is configured to serve it", model)}
	}
	names := make([]string, 0, len(tried))
	for _, a := range tried {
		if a.status != 0 {
			names = append(names, fmt.Sprintf("%s (%d)", a.provider, a.status))
			continue
		}
		names = append(names, a.provider)
	}
	last := tried[len(tried)-1]
	return &apiError{http.StatusServiceUnavailable, fmt.Sprintf(
		"model %q: every provider refused — tried %s; last was %s: %s",
		model, strings.Join(names, ", "), last.provider, last.err)}
}
