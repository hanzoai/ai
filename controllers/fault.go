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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/ai/object"
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
		// OUR credential for this vendor is dead, revoked or wrong-scoped — which
		// is a fact about the VENDOR, exactly as an empty account is, so it fails
		// over like one. The caller's own credential was checked at our edge long
		// before any vendor was dialled; nothing about their request is wrong.
		//
		// This used to stop the cascade, on the reasoning that failing over would
		// paper over a dead key and the first to notice would be whoever reads the
		// bill. The intent was right and the mechanism was the wrong one: SURFACING
		// and SERVING are separate jobs, and the surfacing job already has its own
		// machinery — announce() counts cloud_supply_refused{reason="credential"}
		// and logs at ERROR, on the very next line, whichever way this classifies.
		// Measured, the coupling cost what it was meant to prevent: DO_AI_API_KEY
		// died and 93 routes answered 401 to CUSTOMERS for days, which is louder
		// than any alert and aimed at the wrong people, while the metric that was
		// supposed to raise a hand had been sitting there the whole time.
		return faultProvider

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
		// "you may not use this key" — our misconfiguration, and a fact about this
		// vendor rather than about the request, so it moves on for the same reason
		// 401 does and is counted by the same announce().
		return faultProvider

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

// ran reports whether a vendor processed the request before it failed.
//
// This is the money's question, and it is NOT faultOf's. faultOf asks where the
// request goes next; this asks whether anyone did work we were invoiced for, and
// the two come apart in both directions: a 401 moves the request along (provider
// fault) having run nothing, while a stream that breaks with bytes already on the
// wire stops the cascade (request fault) having run plenty.
//
// An error carrying no status at all never got a response: the request either
// never left this process or the transport failed before any vendor answered.
func ran(err error) bool {
	if err == nil {
		return true
	}
	// Bytes are already on the wire, so something at the other end produced them.
	if errors.Is(err, errPartiallyWritten) {
		return true
	}
	if status := upstreamHTTPStatus(err); status != 0 {
		return ranStatus(status)
	}
	return false
}

// ranStatus reports whether a vendor that answered with this status had
// processed the request when it did.
//
// The listed ones are refusals it can decide from the request alone, so it spent
// nothing on us and sends no invoice for them. Everything else counts as run,
// which keeps a 5xx and a timeout billed as they are today: those can consume the
// prompt before failing, and the vendor's own meter is what settles it.
func ranStatus(status int) bool {
	switch status {
	case http.StatusBadRequest, // 400 — malformed, never parsed
		http.StatusUnauthorized,          // 401 — our key is not honoured
		http.StatusPaymentRequired,       // 402 — the account has no money
		http.StatusForbidden,             // 403 — key or catalogue, neither ran
		http.StatusNotFound,              // 404 — this vendor has not got the model
		http.StatusRequestEntityTooLarge, // 413 — longer than the context
		http.StatusUnprocessableEntity,   // 422 — content refused
		http.StatusTooManyRequests:       // 429 — turned away before a slot
		return false
	}
	return true
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
		"eof", // upstream closed mid-response
	}
	// absentText is "this vendor does not carry this model for this account", as
	// distinct from "this credential is not welcome". Measured phrasings only.
	absentText = []string{
		"not available for your account",
		"model is not available",
		"does not have access to model",
	}
)

// has reports whether msg contains one of the phrases AS a phrase, rather than
// as a run of characters that happens to lie inside a longer word or number.
//
// A phrase must begin where a token begins. A phrase ending in a DIGIT must also
// end where the token ends, because a status code is an exact identifier — where
// a word is not, and a vendor that says "rate limited" is saying "rate limit".
//
// Plain substring matching is not a smaller version of this, it is a wrong one,
// and all three of these were measured:
//
//	"402" inside claude-3-sonnet-2024*022*9  -> a typo'd model id read as an
//	                                            empty account: 3 vendors x 3
//	                                            retries, plus a 5-minute
//	                                            demotion of a healthy vendor
//	"503" inside "you requested *850*3 tokens" -> a request-size error read as
//	                                            a broken vendor
//	"eof" inside "g*eof*ence"                 -> anything at all read as a
//	                                            dropped connection
//
// msg must already be lowercased.
func has(msg string, phrases []string) bool {
	for _, p := range phrases {
		for i := 0; i+len(p) <= len(msg); i++ {
			if msg[i:i+len(p)] != p || !boundary(msg, i-1) {
				continue
			}
			if digit(p[len(p)-1]) && !boundary(msg, i+len(p)) {
				continue
			}
			return true
		}
	}
	return false
}

// boundary reports whether i falls outside msg or on a character that cannot be
// part of a word or a number.
func boundary(msg string, i int) bool {
	if i < 0 || i >= len(msg) {
		return true
	}
	c := msg[i]
	return !(c >= 'a' && c <= 'z' || digit(c))
}

func digit(c byte) bool { return c >= '0' && c <= '9' }

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
		if has(msg, set) {
			return faultProvider
		}
	}
	return faultRequest
}

// lacksModel reports the narrow refusal that means "this vendor does not carry
// this model for this account", as opposed to "this credential is not welcome".
// msg must already be lowercased.
func lacksModel(msg string) bool { return has(msg, absentText) }

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
	// A DEAD CREDENTIAL RESTS AS LONG AS AN EMPTY ACCOUNT, because it is the same
	// operational class: broke() is "only a human with a credit card clears it",
	// and this is "only a human with a new key clears it". Neither clears itself in
	// seconds, so resting it briefly would put the whole fleet back on a vendor
	// that cannot answer, every few seconds, for as long as the key stays dead.
	if credentialRefusal(err) || broke(err, msg) {
		return coolBroke
	}
	return coolBusy
}

// credentialRefusal reports the vendor refusing OUR key rather than the request.
// Read from the STATUS only, never the text, for the reason broke() gives: an
// upstream quotes the request back, so a caller asking about being unauthorized
// must not be able to rest a healthy vendor for five minutes by asking it.
//
// A 403 that means "this account does not carry that MODEL" is excluded — that
// vendor is perfectly well and stocks everything else, which is why lacksModel is
// consulted before this in both callers.
func credentialRefusal(err error) bool {
	switch upstreamHTTPStatus(err) {
	case http.StatusUnauthorized, http.StatusForbidden:
		return !lacksModel(strings.ToLower(err.Error()))
	}
	return false
}

// broke reports the refusal that only a human with a credit card clears, as
// opposed to the one that clears itself in seconds.
//
// Same ordering as faultOf: the status is the evidence, the text is a guess, and
// the guess is consulted only where the fact is absent — or where the fact is
// genuinely two-valued. 429 is the one such status: a vendor answers it both for
// a busy platform and for an exhausted account, and OpenAI's exhausted account
// says so only in words.
//
// Everywhere else the status decides alone, and that is not tidiness. An
// upstream's error text can quote the request that provoked it, so a caller who
// asks a question about insufficient credit would otherwise be able to rest
// their own vendor for five minutes by asking it.
func broke(err error, msg string) bool {
	switch upstreamHTTPStatus(err) {
	case http.StatusPaymentRequired:
		return true
	case 0, http.StatusTooManyRequests:
		return has(msg, moneyText)
	}
	return false
}

// down reports that a vendor cannot serve this request at all: its account with
// us is spent, or it answered with its own failure.
//
// Both are facts about the vendor, and a route it charges nothing for is subject
// to neither — a spent account still serves what it never bills for, and a 5xx is
// one shard of a fleet rather than the whole of it. Busy (429) is deliberately
// outside this: it clears in seconds and is better waited out than answered with
// a smaller model.
func down(err error, msg string) bool {
	return broke(err, msg) || upstreamHTTPStatus(err) >= 500
}

// billingNotice reports that a refusal is OUR OWN spend gate speaking, relayed
// back to us by a service that fronts for us.
//
// "The customer is out of credit" and "our account with the vendor is out of
// credit" arrive as the same status and are opposite facts. One is the customer's
// to fix and has to reach them; the other is ours and must never be shown to them
// as a bill — and must never be answered by quietly serving a smaller model,
// which would tell somebody their payment problem had solved itself.
//
// The CODE is what tells them apart, and it is a fact rather than a phrase:
// object.BillingNotice stamps one on every denial we make, and no vendor writes
// it. Read as a raw value because ours is a string where a vendor's is the status
// number repeated, so decoding into a string would simply fail on theirs.
func billingNotice(body []byte) bool {
	var e struct {
		Error struct {
			Code json.RawMessage `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil {
		return false
	}
	switch string(e.Error.Code) {
	case `"` + object.CodeInsufficientBalance + `"`, `"` + object.CodeBalanceUnavailable + `"`:
		return true
	}
	return false
}

// relay is the refusal a caller is answered with when a PROXIED upstream refuses,
// and the point at which an operator learns the upstream refused at all.
//
// Three cases, two of which wear the same 402 and mean opposite things:
//
//	our own spend gate, relayed by a service that fronts for us — the CUSTOMER
//	owes money. It is theirs to fix, so it reaches them with its status and its
//	code intact.
//
//	the vendor refusing US — our account with them is spent, or they are down.
//	The caller owes nothing and has no action that helps, so it answers 503 the
//	way an exhausted route does. Forwarding the 402 tells a funded customer they
//	are out of credit and sends every client that acts on one to fix a bill that
//	is not theirs.
//
//	the request itself — malformed, unknown model, refused content. Every vendor
//	answers it identically, so the vendor's own status is the honest one and the
//	caller is the only party who can clear it.
//
// It is the rule the failover loop and the family pipe apply, asked here on behalf
// of the surfaces that never reach them: a request carrying tools or images skips
// the completion pipeline, because that pipeline is text-only and would drop both.
//
// The upstream's own body never travels — only the sentence read out of it — for
// the same reason exhausted() quotes one line rather than the whole envelope.
func relay(model, provider string, status int, body []byte) error {
	err := &apiError{status: status, msg: upstreamErrorMessage(body)}

	// Ours, spoken through a service that fronts for us. Nobody upstream refused,
	// so there is nothing here for an operator to act on.
	if billingNotice(body) {
		err.code = object.CodeInsufficientBalance
		return err
	}

	// Somebody upstream refused, whoever it turns out to be at fault. That is
	// worth knowing before a customer reports it, so it is recorded first and
	// the caller's answer is decided after.
	at := attempt{provider: provider, status: status, fault: faultOf(err), err: err}
	announce(model, at)

	if at.fault != faultProvider {
		return err
	}
	return exhausted(model, []attempt{at})
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
	// prompt and completion are what this attempt SPENT, and are set only where
	// spending happened without serving: a provider raced and beaten, which was
	// cancelled mid-generation and charged us for the tokens it had reached. A
	// refusal leaves them zero, which is the true count for an attempt that
	// produced nothing, and is what keeps its ledger row free.
	prompt     int
	completion int
	// row is the provider whose credential was spent, carried for the same
	// reason served.row is: whether this call was the customer's own key is a
	// property of the row that PAID, and a beaten provider is billed from its
	// own row rather than the one auth resolved before the race began.
	row *object.Provider
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
	for _, c := range openrouterTail(route) {
		add(c.provider, c.upstream)
	}

	queue := append(ready, resting...)

	// The cap bounds THIS route, and a refusal from outside the route does not
	// spend one of its places.
	//
	// The two are different quantities and are not netted against each other. A
	// declared route is exactly as wide as the cap — primary, Fallback1,
	// Fallback2 — so subtracting the family pipe's one refusal would leave a model
	// served by the family unable to reach its own last alternate: an operator
	// declared three and got two, silently, and only on the requests where the
	// alternates were the entire point.
	//
	// Nothing is unbounded by this. What the cap exists to bound is a route an
	// operator can lengthen by typing into models.yaml; the family is one fixed
	// provider that no configuration can multiply. Worst case is its one attempt
	// plus this queue, which is a constant.
	if len(queue) > maxProviders {
		queue = queue[:maxProviders]
	}
	return queue
}

// openrouterTail is the alternate EVERY route ends in, derived rather than typed
// out. The route table declares 121 routes and, before this, not one `fallbacks:`
// entry — so a vendor that could not serve simply ended the request, which is what
// a dead DO_AI_API_KEY turned into 93 models refusing customers.
//
// Two rungs, and the second is the one that always exists:
//
//   - THE SAME MODEL ON OPENROUTER, and only where OpenRouter's own discovered
//     catalog confirms the id. The id is derived from the upstream one — do-ai
//     spells `openai-gpt-4o` where OpenRouter spells `openai/gpt-4o` — but a
//     derivation is a guess, and the vendors disagree often enough (`claude-4.5-sonnet`
//     against `claude-3.5-sonnet`) that guessing would add a hop that 404s. lookup()
//     is the catalog as last discovered, so a wrong guess contributes nothing and a
//     right one needs no table to be maintained.
//   - THE FREE POOL, which needs no mapping at all: freeRoutes() is the platform's
//     one curated list of routes that cost nothing and answer, best first, and its
//     ids are OpenRouter's own. That is the floor under every model.
//
// It is skipped for a route already served BY openrouter — the family's own
// `spared` already offers the pool there, and a second offer would be two answers
// to one question.
//
// A PREMIUM route gets the first rung and NOT the free pool. Substituting a free
// model for one somebody chose and paid for trades terms that are not ours to
// trade, and the caller would learn of it from a header they would have to
// remember their own request to compare against — the same argument `spared`
// already makes when it refuses a `collectionDeny` route. Same model elsewhere is
// not that trade; a smaller free one is.
func openrouterTail(route *modelRoute) []candidate {
	if route == nil || route.providerName == freeFamily().name || !freeFamily().enabled() {
		return nil
	}
	var out []candidate
	if id, ok := openrouterEquivalent(route.upstreamModel); ok {
		out = append(out, candidate{freeFamily().name, id})
	}
	if route.premium {
		return out
	}
	for _, sp := range freeRoutes() {
		if sp.fam == freeFamily() {
			out = append(out, candidate{freeFamily().name, sp.id})
			break // ONE floor: the queue is capped anyway, and the pool's own walk is spared's job
		}
	}
	return out
}

// openrouterEquivalent maps an upstream id onto OpenRouter's spelling of the same
// model, and answers false unless the discovered catalog actually carries it.
// `openai-gpt-4o` -> `openai/gpt-4o`; an id that is already vendor-qualified is
// tried as-is first, which is what makes this work for a route whose upstream
// happens to be spelled OpenRouter's way already.
func openrouterEquivalent(upstream string) (string, bool) {
	u := strings.ToLower(strings.TrimSpace(upstream))
	if u == "" {
		return "", false
	}
	tries := []string{u}
	if i := strings.Index(u, "-"); i > 0 && !strings.Contains(u, "/") {
		tries = append(tries, u[:i]+"/"+u[i+1:])
	}
	for _, t := range tries {
		if _, ok := freeFamily().lookup(t); ok {
			return t, true
		}
	}
	return "", false
}

// codeExhausted is the machine name of "we could not serve this". It is OUR
// failure, and a client must be able to tell it from the caller's own wallet
// running out — both are refusals a person reads as "it did not work", and only
// one of them is fixed by adding credits.
const codeExhausted = "providers_exhausted"

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
		return &apiError{status: http.StatusServiceUnavailable, code: codeExhausted,
			msg: fmt.Sprintf("model %q: no provider is configured to serve it", model)}
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
	return &apiError{status: http.StatusServiceUnavailable, code: codeExhausted, msg: fmt.Sprintf(
		"model %q: every provider refused — tried %s; last was %s: %s",
		model, strings.Join(names, ", "), last.provider, last.err)}
}
