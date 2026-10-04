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

package routers

import (
	stdcontext "context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/account"

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/controllers"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
	"github.com/zap-proto/zip"
)

// ── Balance gate configuration ──────────────────────────────────────────────
//
// There is NO exemption of any kind: no exempt keys, no exempt users, no
// fail-open override. Every priced request is gated on a positive prepaid
// balance, and a balance that cannot be read denies the request (fail-closed).
// AI is prepaid — nothing runs on credit it has not been funded for.

const (
	// balanceCacheTTL controls how long a cached balance result is considered
	// fresh. Stale entries are served immediately while an async refresh runs
	// in the background, so requests are never blocked on Commerce latency.
	balanceCacheTTL = 30 * time.Second

	// balanceCacheCleanupInterval is how often stale cache entries are evicted.
	balanceCacheCleanupInterval = 5 * time.Minute

	// balanceHTTPTimeout is the per-request timeout for Commerce balance lookups.
	balanceHTTPTimeout = 5 * time.Second

	// userKeyCacheTTL controls how long a resolved apiKey->userKey mapping is
	// cached. IAM key lookups are expensive (HTTP call); JWTs are cheap to
	// parse but we cache them too for consistency.
	userKeyCacheTTL = 5 * time.Minute
)

// ── Balance cache ───────────────────────────────────────────────────────────

// BalanceGate enforces a positive spendable balance before paid requests. The
// balance itself lives in the shared object.BalanceLedger — the ONE source of
// truth also used by the controller debit path to reserve (before a request) and
// settle (after). Reading the ledger here makes the gate reservation-aware and
// reflects local settles immediately, so the cache window can never serve a
// stale-positive balance after the funds are spent. The gate adds only its own
// freshness scheduling (async refresh from Commerce) and identity resolution.
type BalanceGate struct {
	// ledger is the shared balance+reservation store (defaults to
	// object.GlobalBalanceLedger; injectable for tests).
	ledger *object.BalanceLedger

	// userKeyCache maps Bearer token -> org slug (billing key) to avoid
	// re-parsing JWTs or re-calling IAM on every request for the same token.
	userKeyMu    sync.RWMutex
	userKeyCache map[string]*userKeyCacheEntry

	// inflight tracks what is being refreshed in the background — a subject's
	// balance, or a token's identity (recheckUserKey) — so concurrent stale reads
	// start one fetch.
	inflightMu sync.Mutex
	inflight   map[string]struct{}

	endpoint string       // Commerce base URL (e.g. "http://commerce:8001")
	token    string       // Bearer token for Commerce API
	client   *http.Client // shared HTTP client
}

// userKeyCacheEntry maps an API token to the resolved billing identity: the
// per-user subject (?user=), the org namespace (X-Org-Id), and the exact
// "owner/name" userKey used for per-user exemption matching.
type userKeyCacheEntry struct {
	subject   string
	namespace string
	userKey   string
	fetchedAt time.Time
}

// balanceGate is the package-level singleton, initialized by InitBalanceGate.
var balanceGate *BalanceGate

// InitBalanceGate reads Commerce connection parameters from app config and
// creates the balance gate. Must be called once during startup. If Commerce
// is not configured, the gate is not created and BalanceGateFilter is a no-op.
// IAM connection parameters are NOT read here: key resolution goes through the
// one resolver (controllers.GetUserByAccessKey), which owns that config.
func InitBalanceGate() {
	endpoint := conf.GetConfigString("commerceEndpoint")
	if endpoint == "" {
		log.Info("balance_gate: commerceEndpoint not configured, balance enforcement disabled")
		return
	}
	endpoint = strings.TrimRight(endpoint, "/")
	token := conf.GetConfigString("commerceToken")

	bg := &BalanceGate{
		ledger:       object.GlobalBalanceLedger,
		userKeyCache: make(map[string]*userKeyCacheEntry),
		inflight:     make(map[string]struct{}),
		endpoint:     endpoint,
		token:        token,
		client:       &http.Client{Timeout: balanceHTTPTimeout},
	}

	go bg.cleanupLoop()

	balanceGate = bg
	log.Info("balance_gate: initialized (endpoint=%s, ttl=%v)", endpoint, balanceCacheTTL)
}

// ── Filter function ─────────────────────────────────────────────────────────

// BalanceGateFilter is a BeforeRouter filter that checks whether the
// requesting user has a positive Commerce balance before allowing paid API
// requests to proceed. It runs after AutoSigninFilter (which sets session
// users for legacy auth paths) and handles its own user resolution for
// JWT and IAM API key auth paths.
//
// Posture: fail-CLOSED on balance, never on money. When billing is unconfigured
// (no gate) or the billing subject cannot be identified, the request passes to the
// downstream auth/controller layer, which re-checks (identity is that layer's job).
// When the subject IS identified, a positive spendable balance is REQUIRED: a
// known-insufficient balance is denied 402 (add credits), and a balance that cannot
// be verified is denied 503 (retry) — both deny. AI is prepaid; a billing-backend
// outage becomes a retryable 503, never free inference.
func BalanceGateFilter(c *zip.Ctx) error {
	if balanceGate == nil {
		return c.Continue()
	}

	path := c.Path()

	if isBalanceExempt(path, c.Method()) {
		return c.Continue()
	}

	// Reads never spend — so a read is NEVER balance-gated; only mutating / metered
	// methods are. Every priced action (chat/completions, completions, messages,
	// embeddings, images/audio/video generations, crawl/scrape/ingest) is a POST, and
	// each keeps its OWN in-controller balance backstop (openai_api.resolveProviderForUser,
	// crawl/scraper/docs_ingest getUserBalance); a GET/HEAD/OPTIONS only lists or fetches
	// a resource. Gating reads on a positive balance 402s a $0-balance org merely trying
	// to VIEW its own account — its models, chats, usage, router stats, keys, buckets,
	// deployments — which is the "can't see my own resources when broke" outage a new
	// signup / expired trial hits on every product overview. Exempting reads can NEVER
	// free a metered call, because no metered call is a read.
	if isReadMethod(c.Method()) {
		return c.Continue()
	}

	// Only enforce on API and v1 routes. Folded: the router matches case-blind, so
	// a lowercase-literal test reads /V1/ as "not an API route".
	if !strings.HasPrefix(strings.ToLower(path), "/v1/") {
		return c.Continue()
	}

	subject, namespace, userKey := resolveBillingKey(c)
	if subject == "" {
		// Cannot identify the billing subject — let downstream auth filters handle rejection.
		return c.Continue()
	}

	// An inline upload spends nothing: /v1/ai/rag/ingest with source "upload" (or
	// unset, its default) indexes bytes the caller already has into the caller's
	// own store — no scrape, no crawl, no model. The controller's own gate prices
	// exactly the sources that DO spend (github/crawl/s3) and deliberately admits
	// upload; refusing it here made this gate and that one give different answers
	// to the same question. Same body requestedModel reads.
	if path == "/v1/ai/rag/ingest" {
		var req struct {
			Source string `json:"source"`
		}
		if err := json.Unmarshal(c.Body(), &req); err == nil {
			if req.Source == "" || req.Source == "upload" {
				return c.Continue()
			}
		}
	}

	// A model priced at zero has nothing for a WALLET gate to refuse.
	//
	// This gate runs before any controller, so the in-controller balance gate's
	// identical rule never gets the chance to speak: a $0 route was refused here
	// first, and a guest with no wallet at all — the caller a free tier exists for —
	// could not reach the very routes published for them. Two gates asking one
	// question have to give one answer.
	//
	// Fails closed the same way its twin does: only a model whose price is FOUND and
	// is zero skips. A body we cannot read, a model we cannot name, or a price we had
	// to synthesize all leave the gate in force.
	//
	// A decision body is decoded here, once its sender is known, and left plain —
	// unless the ledger already holds the wallet empty and no decision model is free
	// to it, when the refusal the decode would end in is given without it.
	//
	// Where the host decides who pays (object.Limits), an empty wallet is no answer on
	// its own — the plan may pay — so the body is decoded and the policy asked.
	if controllers.DecisionPath(path) {
		if avail, empty := balanceGate.empty(subject, namespace); empty && object.Limits() == nil && !decisionFree(namespace) {
			return denied(c, object.InsufficientBalance(c.Host(), namespace, ""), subject, namespace, avail, path)
		}
		if _, err := controllers.DecisionBody(c); err != nil {
			return err
		}
	}
	model := requestedModel(c)
	// A push to or the close of a live transcript names no model: it is billed as the
	// model its open named, for the caller who opened it, and was counted there.
	session := false
	if model == "" {
		if m, ok := sessionModel(c.Method(), path, c.Header("Authorization")); ok {
			model, session = m, true
		}
	}
	if sku, ok := depthRoute(model, c.Body()); ok && balanceGate.funds(c, subject, namespace, userKey, sku) {
		// The default free id, asked by a caller whose plan or bought credit pays for
		// the priced SKU router.depth names at this depth: the request is served, gated
		// and billed as that SKU. A caller it does not fund keeps the free id.
		if body, ok := controllers.WithModel(c.Body(), sku); ok {
			c.Fiber().Request().SetBody(body)
			c.Fiber().Request().Header.Del("Content-Encoding")
			c.SetHeader(controllers.RoutedModelHeader, sku)
			model = sku
		}
	}
	// WHO PAYS IS THE HOST'S CALL. Its usage policy puts every model in a class and
	// spends, in order: the plan's included usage for that class, then prepaid cash,
	// then granted credit where the model takes it, then a model's daily cap on a plan
	// that cannot pay; past all of them the caller is in limited mode. A covered request
	// — the plan or a free cap pays — meets no wallet below; one the wallet pays goes on
	// to it carrying what may pay; a refused one is answered here.
	//
	// LIMITED MODE KEEPS A CONVERSATION GOING. A chat request from a signed-in app
	// (or a client that asks for it) the plan can no longer pay for is answered by the
	// free model instead, saying so in X-Hanzo-Fallback; the free model is still held
	// to the plan's request windows, so the free lane is not a way around them.
	//
	// A policy that cannot be read decides nothing: the wallet and the free allowance
	// below gate the request exactly as they would with no plans at all.
	if limits := object.Limits(); limits != nil && model != "" && controllers.Entitled(path) {
		for try := 0; try < 3; try++ {
			priced := !costsNothing(model, namespace)
			grant, hit, err := limits(c.Context(), object.LimitAsk{
				Subject: subject, Namespace: namespace, Actor: userKey, Model: model,
				Family: controllers.FamilyOf(model), Class: controllers.ClassOf(model), Priced: priced,
				Apps: controllers.Apps(c), Spend: controllers.ChatPath(path), Session: session,
			})
			if err != nil {
				log.Warning("limits: unreadable, refusing subject=%s namespace=%s path=%s: %v", subject, namespace, path, err)
				return denied(c, object.UsageUnavailable(), subject, namespace, 0, path)
			}
			if hit != nil {
				log.Info("limits: %s %s subject=%s namespace=%s actor=%s path=%s model=%s", hit.Code, hit.Name, subject, namespace, userKey, path, model)
				// A capped model hands the conversation to its Hanzo fallback; a plan
				// that cannot pay at all hands it to the free model. Each hand-off is
				// asked again, so the model that answers is still the policy's to admit.
				to := controllers.FreeModel
				if hit.Code == object.CodeModelCap && hit.Fallback != "" {
					to = hit.Fallback
				}
				if priced && !strings.EqualFold(to, model) && fallback(c, path, hit.Code) && fallBack(c, to, hit.Code, namespace, hit.Upgrade) {
					model = to
					continue
				}
				return limitReached(c, hit, namespace)
			}
			if grant == nil {
				break
			}
			usage(c, grant)
			controllers.Cover(c, grant)
			// A request that was not served keeps nothing it was counted against.
			defer release(c, grant.Release)
			if !grant.Covered() {
				break // the wallet pays: on to it, carrying what may pay
			}
			err = c.Continue()
			// A whole answer is over once its handler is: the grant ends, at nothing
			// more than its usage record already settled. A streamed answer is
			// settled by its own writer when the stream ends.
			if !c.Fiber().Response().IsBodyStream() {
				grant.Settle(0)
			}
			return err
		}
	}

	if model != "" && costsNothing(model, namespace) {
		// Free is not unbounded. The wallet has nothing to refuse at zero, so the
		// plan's ALLOWANCE is what bounds this lane: a count of calls per person per
		// day, held by the host. It is the one gate a free caller meets, and the
		// moment to offer them a plan.
		//
		// THE HOST COUNTS THE CALL AS IT ADMITS IT. Asking is taking: the hook spends
		// one of the person's calls before any model is reached, and Spent says THIS
		// call was the one past the ceiling, so calls in flight cannot all slip past
		// the last unit because none of them waited to be counted. A call that was not
		// served — an answer of 400 or above — gives its unit back (Standing.Release).
		//
		// THE PERSON IS NAMED BY THE REQUEST (person). The host keys the count by the
		// caller's IAM home org and user, read with zip.CallerOf, and a raw handler's
		// own context carries no caller — handed c.Context(), the host could name
		// nobody and would count every member of an org as one. subject and namespace
		// still say which wallet sets the ceiling.
		//
		// Where the caller stands rides on the answer, admitted or refused, as
		// X-RateLimit-*: a client shows what is left without asking twice.
		//
		// An allowance that cannot be read refuses: a gate that cannot decide does
		// not admit (503, retryable). A priced route never reaches this branch.
		//
		// RETRIEVAL IS NOT A CALL THE ALLOWANCE COUNTS. An embedding or a rerank is
		// what the platform runs many of to answer one question — a file's chunks, a
		// search's documents — so counting each would spend a person's day on one
		// upload. Only a call that asks a model to answer is counted.
		if spent := object.Spent(); spent != nil && !retrieval(path) {
			out, err := spent(person(c), subject, namespace)
			if err != nil {
				log.Warning("allowance: unreadable, refusing subject=%s namespace=%s path=%s: %v", subject, namespace, path, err)
				return denied(c, object.UsageUnavailable(), subject, namespace, 0, path)
			}
			{
				standing(c, out)
				if out.Spent {
					log.Info("allowance: free calls spent subject=%s namespace=%s window=%s path=%s",
						subject, namespace, out.Window, path)
					return freeRefused(c, object.AllowanceSpent(c.Host(), namespace, out))
				}
				defer release(c, out.Release)
			}
		}
		return c.Continue()
	}

	sufficient, deny, balance := balanceGate.checkBalance(c.Host(), subject, namespace, userKey)
	if sufficient {
		return c.Continue()
	}
	// A conversation the wallet cannot pay for goes on in limited mode, like one the
	// plan cannot: the free model answers and says why. Asked again from the top, so
	// the free model is held to the plan's windows and the free allowance.
	if deny.Code == object.CodeInsufficientBalance && model != "" && !strings.EqualFold(model, controllers.FreeModel) &&
		fallback(c, path, deny.Code) && fallBack(c, controllers.FreeModel, deny.Code, namespace, "") {
		return BalanceGateFilter(c)
	}
	return denied(c, deny, subject, namespace, balance, path)
}

// fallBack hands a chat request to model to and says so on the response: the lane
// that answers (X-Hanzo-Fallback), why (X-Hanzo-Usage-Reason, the refusal's own
// code), that the caller is in limited mode, and the two ways out — the wallet's
// top-up page and the plan that raises the limit (X-Hanzo-Topup-Url,
// X-Hanzo-Upgrade-Url). The model that actually serves is named by the handler in
// X-Hanzo-Served. It reports false when the body could not be rewritten.
func fallBack(c *zip.Ctx, to, code, org, upgrade string) bool {
	body, ok := controllers.WithModel(c.Body(), to)
	if !ok {
		return false
	}
	c.Fiber().Request().SetBody(body)
	c.Fiber().Request().Header.Del("Content-Encoding")
	pay := object.PayURL(c.Host(), org)
	c.SetHeader("X-Hanzo-Fallback", to)
	c.SetHeader("X-Hanzo-Usage-Reason", code)
	c.SetHeader("X-Hanzo-Usage", "limited")
	c.SetHeader("X-Hanzo-Topup-Url", pay)
	if upgrade != "" {
		c.SetHeader("X-Hanzo-Upgrade-Url", pay+"/cart?plan="+url.QueryEscape(upgrade))
	} else {
		c.SetHeader("X-Hanzo-Upgrade-Url", pay)
	}
	return true
}

// denied writes the gate's refusal. checkBalance decides WHICH: a known-insufficient
// balance (402, add credits) or a balance it could not verify (503, retry) — both
// fail-CLOSED.
func denied(c *zip.Ctx, deny object.BillingNotice, subject, namespace string, balance int64, path string) error {
	log.Info("balance_gate: deny subject=%s namespace=%s balance_cents=%d code=%s status=%d path=%s",
		subject, namespace, balance, deny.Code, deny.Status, path)
	c.SetHeader("Content-Type", "application/json")
	return c.Bytes(deny.Status, deny.ErrorJSON())
}

// costsNothing reports whether a model costs namespace nothing
// (controllers.ModelCostsNothing), indirected so the gate's tests state the prices
// directly.
var costsNothing = controllers.ModelCostsNothing

// decisionFree reports whether a decision model costs namespace nothing
// (controllers.DecisionFree), indirected so the gate's tests state the prices directly.
var decisionFree = controllers.DecisionFree

// depthRoute names the priced SKU a free default id is served at for a funded caller
// (controllers.DepthRoute), indirected so the gate's tests state the table directly.
var depthRoute = controllers.DepthRoute

// funds reports whether the caller pays for a priced call to model: no plans are
// installed and the balance admits it. Any refusal or unreadable answer is no, which
// leaves the caller on the free id it sent.
func (g *BalanceGate) funds(c *zip.Ctx, subject, namespace, userKey, model string) bool {
	// Where plans are installed a Hanzo SKU is a plan's, never a per-call charge: a
	// plan reaches paid upstream through its family's paid rungs, within its budget,
	// so nothing lifts a free id onto a priced one.
	if object.Limits() != nil {
		return false
	}
	sufficient, _, _ := g.checkBalance(c.Host(), subject, namespace, userKey)
	return sufficient
}

// limitReached writes the host's refusal, named by its code and never by a figure:
// 429 usage_cap_exceeded when a window of the plan is spent, 429 free_plan_cap when a
// free plan's daily cap on the model is used, 402 plan_allowance_used when the plan's
// included usage of the class is used and nothing else may pay, 402
// paid_plan_required when the model needs a paid plan or prepaid balance, 402
// model_cap when the model has used its share of the plan's allowance. It names
// when it lifts (Retry-After where it lifts by itself) and what lifts it sooner.
func limitReached(c *zip.Ctx, hit *object.LimitHit, org string) error {
	type action struct {
		Kind  string `json:"kind"`
		Label string `json:"label"`
		URL   string `json:"url,omitempty"`
		Plan  string `json:"plan,omitempty"`
		Model string `json:"model,omitempty"`
	}
	body := struct {
		Error struct {
			Message    string   `json:"message"`
			Type       string   `json:"type"`
			Code       string   `json:"code"`
			Class      string   `json:"class,omitempty"`
			Model      string   `json:"model,omitempty"`
			Fallback   string   `json:"fallback,omitempty"`
			Limit      string   `json:"limit,omitempty"`
			ResetsAt   string   `json:"resets_at,omitempty"`
			UpgradeURL string   `json:"upgrade_url,omitempty"`
			Actions    []action `json:"actions,omitempty"`
		} `json:"error"`
	}{}
	pay := object.PayURL(c.Host(), org)
	code := hit.Code
	if code == "" {
		code = object.CodeUsageCap
	}
	status := http.StatusPaymentRequired
	body.Error.Type = "billing_error"
	if code == object.CodeUsageCap || code == object.CodeFreePlanCap {
		status = http.StatusTooManyRequests
		body.Error.Type = "rate_limit_error"
	}
	msg := hit.Message
	if msg == "" && code == object.CodeUsageCap {
		msg = fmt.Sprintf("You've used %s requests on your plan.", limitNoun(hit.Name))
		body.Error.Limit = hit.Name
	}
	if msg == "" {
		msg = "This request is outside what your plan includes."
	}
	if !hit.ResetsAt.IsZero() {
		reset := hit.ResetsAt.UTC()
		body.Error.ResetsAt = reset.Format(time.RFC3339)
		if code == object.CodeUsageCap {
			msg += " They reset at " + body.Error.ResetsAt + "."
		}
		if wait := int64(time.Until(reset).Seconds()); wait > 0 && status == http.StatusTooManyRequests {
			c.SetHeader("Retry-After", fmt.Sprint(wait))
		}
	}
	if hit.Upgrade != "" {
		body.Error.UpgradeURL = pay + "/cart?plan=" + url.QueryEscape(hit.Upgrade)
		body.Error.Actions = append(body.Error.Actions, action{Kind: "upgrade", Label: "Upgrade your plan", URL: body.Error.UpgradeURL, Plan: hit.Upgrade})
	}
	if code == object.CodeModelCap && hit.Fallback != "" {
		body.Error.Actions = append(body.Error.Actions, action{Kind: "switch", Label: "Try " + modelName(hit.Fallback), Model: hit.Fallback})
	}
	switch {
	case code == object.CodeUsageCap:
	case hit.Credits:
		// The payer holds what could pay and has not chosen to: the action turns
		// the choice on (PUT /v1/ai/limits), it moves no money.
		body.Error.Actions = append(body.Error.Actions, action{Kind: "credits", Label: "Continue with credits", URL: "/v1/ai/limits"})
	default:
		body.Error.Actions = append(body.Error.Actions, action{Kind: "topup", Label: "Add prepaid credit", URL: pay})
	}
	switch {
	case body.Error.UpgradeURL != "" && code == object.CodeUsageCap:
		msg += " Upgrade for more at " + body.Error.UpgradeURL
	case code != object.CodeUsageCap:
		msg += " " + pay
	}
	body.Error.Message = msg
	body.Error.Code = code
	body.Error.Class = hit.Class
	body.Error.Model = hit.Model
	body.Error.Fallback = hit.Fallback
	raw, _ := json.Marshal(body)
	c.SetHeader("X-Hanzo-Usage", "limited")
	if hit.Class != "" {
		c.SetHeader("X-Hanzo-Usage-Class", hit.Class)
	}
	c.SetHeader("Content-Type", "application/json")
	return c.Bytes(status, raw)
}

// sessionModel is controllers.TranscriptModel, indirected so the gate's tests state
// a live session directly.
var sessionModel = controllers.TranscriptModel

// release hands back what a request was counted against when it was not served:
// any answer of 400 or above, the gate's own refusals below included. A stream is
// served from its first byte, and stands. give is nil when nothing was counted.
func release(c *zip.Ctx, give func()) {
	if give != nil && c.Fiber().Response().StatusCode() >= http.StatusBadRequest {
		give()
	}
}

// usage says on the response who paid and where the class stands, never a figure.
func usage(c *zip.Ctx, g *object.LimitGrant) {
	if g.State != "" {
		c.SetHeader("X-Hanzo-Usage", g.State)
	}
	if g.Class != "" {
		c.SetHeader("X-Hanzo-Usage-Class", g.Class)
	}
	if g.Pays != "" {
		pays := g.Pays
		if pays == object.PaysPrepaid {
			pays = object.PaysCredits // to a customer, prepaid and granted are both credits
		}
		c.SetHeader("X-Hanzo-Paid-By", pays)
	}
}

// modelName is how a fallback model is named in an action label.
func modelName(id string) string {
	m := strings.ToLower(strings.TrimSpace(id))
	switch {
	case strings.HasPrefix(m, "enso") || strings.HasPrefix(m, "hanzo/enso"):
		return "Enso"
	case strings.HasPrefix(m, "zen"):
		return "Zen"
	}
	return id
}

// fallback reports whether a refused request is answered by another model instead.
// It is the one rule for every refusal for want of payment — the plan's allowance
// used, a paid plan required, a model past its share, an empty wallet — and it holds
// for a conversation from a signed-in app or a client that sent X-Hanzo-Fallback:
// allow. An API key gets the refusal unless it asks: a program is told, not silently
// answered by another model.
func fallback(c *zip.Ctx, path, code string) bool {
	if !controllers.ChatPath(path) {
		return false
	}
	switch code {
	case object.CodePlanAllowance, object.CodePaidPlan, object.CodeModelCap, object.CodeInsufficientBalance:
	default:
		return false
	}
	return len(controllers.Apps(c)) > 0 || strings.EqualFold(strings.TrimSpace(c.Header("X-Hanzo-Fallback")), "allow")
}

// standing says on the response where a bounded caller's free calls stand: the
// ceiling, what is left of it, and when it resets, in unix seconds. A refused call
// has nothing left, whatever the count says. A caller no window bounds (Limit 0)
// gets none of the three.
func standing(c *zip.Ctx, s object.Standing) {
	if s.Limit <= 0 {
		return
	}
	left := max(s.Limit-s.Used, 0)
	if s.Spent {
		left = 0
	}
	c.SetHeader("X-RateLimit-Limit", fmt.Sprint(s.Limit))
	c.SetHeader("X-RateLimit-Remaining", fmt.Sprint(left))
	if !s.Resets.IsZero() {
		c.SetHeader("X-RateLimit-Reset", fmt.Sprint(s.Resets.Unix()))
	}
}

// freeRefused writes a Free-plan refusal with Retry-After when it says when it
// clears.
func freeRefused(c *zip.Ctx, n object.FreeNotice) error {
	if wait := n.RetryAfter(time.Now()); wait > 0 {
		c.SetHeader("Retry-After", fmt.Sprint(wait))
	}
	c.SetHeader("Content-Type", "application/json")
	return c.Bytes(n.Status, n.ErrorJSON())
}

// limitNoun is how a window is named to a person.
func limitNoun(name string) string {
	if name == "day" {
		return "today's"
	}
	return "this " + name + "'s"
}

// isReadMethod reports whether an HTTP method only READS — it lists or fetches a
// resource and can never spend, so the balance gate skips it. Every priced action is
// a POST (chat/completions, embeddings, images/audio/video, crawl/scrape/ingest), so a
// GET/HEAD/OPTIONS is always safe to admit at $0 balance; this is the "gate writes, not
// reads" rule in one place.
func isReadMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

// balanceExemptNames are exemptions stated as POLICY NAMES rather than URLs, so
// they survive a resource moving to a different route. The usage reads are here
// for the reason the comment below spells out: a $0-balance org must be able to
// see the usage panel that tells it to add credit. Keying those on literal
// paths is what caused that outage once already, and moving the routes to
// /v1/ai/usages would have caused it again.
var balanceExemptNames = map[string]struct{}{
	"get-usages": {}, "get-range-usages": {}, "get-cloud-usages": {},
}

// requestedModel reads the model a metered POST is asking for, or "" when the body
// does not name one. The router caches the body, so this reads the cached copy and
// never consumes the stream the handler is about to read.
func requestedModel(c *zip.Ctx) string {
	body := c.Body()
	if len(body) == 0 {
		return ""
	}
	// A transcription is a form: its model is a field of it.
	if strings.HasPrefix(strings.ToLower(c.Header("Content-Type")), "multipart/form-data") {
		return strings.TrimSpace(c.Fiber().FormValue("model"))
	}
	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}
	return strings.TrimSpace(req.Model)
}

// isBalanceExempt returns true for requests that should bypass balance checking
// (free/public endpoints, health checks, account + usage metadata).
func isBalanceExempt(path, method string) bool {
	if name, ok := normalizedControllerName(path, method); ok {
		if _, exempt := balanceExemptNames[name]; exempt {
			return true
		}
	}
	switch {
	// The public lane bills nobody and bounds itself, so this gate has nothing to
	// add and two ways to get it wrong. It resolves a billing subject from an
	// AMBIENT SESSION as readily as from a bearer, and the visitor widget runs on
	// our own pages — so a signed-in reader asking an anonymous question would have
	// their plan allowance spent for a call they did not make as themselves, on top
	// of the visitor count the lane spends: one request, charged twice, to two
	// counters. It also reads the model from the BODY, which is the one thing that
	// lane promises to ignore. Exempt from BALANCE, never from bounds — the ceiling
	// lives in the handler and is taken there exactly once.
	case path == "/v1/chat/public", path == "/v1/audio/transcriptions/public":
		return true
	case path == "/v1/health" || path == "/health":
		return true
	case path == "/v1/metrics" || path == "/metrics":
		return true
	// Public marketing aggregate — the world.hanzo.ai live-traffic globe. It exposes
	// only country/region counts + throughput rates (no IPs, no org/user), needs no
	// auth and no balance. Same class as /v1/ai/router/stats?scope=platform.
	case strings.HasPrefix(path, "/v1/ai/traffic/"):
		return true
	// Public marketing aggregate — the router flywheel stats the world.hanzo.ai /
	// console dashboards render (model/throughput/country counts; no org/user rows).
	// Same class as /v1/ai/traffic/: no balance needed. Explicitly exempt so it is 200 on
	// BOTH the anon and the authed path — a $0-balance org's dashboard must never 402
	// where an anon viewer sees 200. (The read-method skip in BalanceGateFilter covers
	// it too; this states the intent at the source and is method-independent.)
	case path == "/v1/ai/router/stats":
		return true
	// Model + pricing CATALOG listings are metadata, not metered inference:
	// a caller must still be authenticated (the auth filters enforce that —
	// balance-exemption is NOT auth-exemption), but must never need a positive
	// balance just to READ the available-models list. Gating /v1/models on
	// balance 402s a funded-but-zero or M2M caller browsing the catalog — the
	// "402 on free /v1/models" console-wide outage class.
	case path == "/v1/models" || strings.HasPrefix(path, "/v1/models/"):
		return true
	case path == "/v1/ai/version" || path == "/v1/ai/system":
		return true
	case strings.HasPrefix(path, "/v1/ai/signin"):
		return true
	case path == "/v1/ai/signout":
		return true
	case path == "/v1/ai/account":
		return true
	// Usage/spend READS are account metadata, not metered inference. A caller
	// must ALWAYS be able to SEE its own usage — especially to learn it needs
	// credits — so a $0-balance org never 402s on the usage view (same class as
	// /v1/models + /v1/get-account; the auth filters still require a principal).
	// Gating these was the "insufficient balance on the usage panel" outage.
	// The reward/feedback signal is training metadata, not metered inference: a
	// caller must be able to score a past request even at $0 balance (the outcome
	// label is exactly how the enso loop learns). Auth still required (the handler
	// self-auths); only the balance 402 is skipped.
	case path == "/v1/ai/feedback":
		return true
	// The router-config surface (/v1/ai/router/{policy,defaults,ledger,rewards,artifact-meta}
	// + /v1/ai/org/settings) is routing METADATA — per-org policy/allowlist/dial, the export
	// endpoints, the org settings — NOT metered inference. It is served over the router via
	// RouterConfigBridge → the ONE native ZAP handler, so it DOES traverse this filter and
	// must be balance-exempt exactly like /v1/ai/router/stats + /v1/ai/feedback: a $0-balance org
	// has to read/write its own router config from the console (auth is still enforced — the
	// handler self-auths). Dropping these from the exempt list would 402 every unfunded
	// org's Router → Policy tab.
	case path == "/v1/ai/router/policy" ||
		path == "/v1/ai/router/defaults" ||
		path == "/v1/ai/router/ledger" ||
		path == "/v1/ai/router/rewards" ||
		path == "/v1/ai/router/artifact-meta" ||
		strings.HasPrefix(path, "/v1/ai/org/settings"):
		return true
	// Router observability READS — the savings/quality aggregate + the improvement
	// time-series. Marketing/metadata, not metered inference (same class as
	// /v1/ai/router/defaults): the public platform scope is unauthenticated (the
	// no-subject path already passes), and an authenticated org-scope read must not
	// 402 a $0-balance org either.
	case path == "/v1/ai/router/stats" || path == "/v1/ai/router/history" || path == "/v1/ai/router/judge-panel":
		return true
	default:
		return false
	}
}

// resolveBillingKey extracts the billing identity from the request context and
// returns (subject, namespace). The namespace is the IAM org slug (X-Org-Id);
// the subject is account.Payer(account.Credential{Owner: owner, Name: name}).Subject() — "owner/name" for a
// personal-billing org (e.g. the shared "hanzo" catch-all, so each individual is
// billed independently) or the org slug for a pooled org. This is the one place
// the billing identity is derived; recordUsage debits and the gate reads the same
// subject within the same namespace.
//
// It checks three sources in order:
//  1. Session user (set by AutoSigninFilter for legacy auth)
//  2. JWT (parsed locally, no network call)
//  3. IAM API key (sk- prefix, resolved via cached IAM lookup)
//
// Sources 2 and 3 read the credential through extractAPIKey, so a key names the
// same payer on every transport this estate accepts it on.
//
// Returns ("", "", "") if the subject cannot be identified (fail-open: filter
// skips). The userKey is the exact "owner/name" identity used for per-user
// exemption matching (mirrors the controller backstop), independent of whether
// the billing subject collapses to the org slug for a pooled org.
//
// Only source 3 leaves this process. billingKey below answers the other two and
// the cache, and says whether its answer is final, so a caller who has to be
// asked about can be told apart from one already known — and the ceiling that
// decides whether this request may spend a round trip is reachable without
// spending one.
func resolveBillingKey(c *zip.Ctx) (subject, namespace, userKey string) {
	subject, namespace, userKey, held := billingKey(c)
	if held {
		return subject, namespace, userKey
	}

	// IAM API key (sk- prefix): resolve via IAM. An API key carries no signed
	// membership set, so it never switches — it bills the org that owns it.
	//
	// The MISS is cached too, and deliberately: an upstream vendor key wears the
	// same sk- spelling and IAM will never own it, so caching only the hit would
	// buy a fresh IAM round-trip on every request such a caller makes. A miss
	// resolves no subject, which is the fail-open this gate already takes — the
	// controller still bills that call against its provider row.
	token := extractAPIKey(c)
	requested := strings.TrimSpace(c.Header("X-Org-Id"))
	subject, namespace, userKey = balanceGate.resolveIAMKeySubject(token)
	balanceGate.setUserKeyCache(token, requested, subject, namespace, userKey)
	return subject, namespace, userKey
}

// billingKey is resolveBillingKey without the round trip: it answers from a
// session, from a token this process can parse itself, and from a key it has
// already resolved.
//
// held says the answer is FINAL. A held answer includes a held MISS — "" is what a
// vendor key resolves to here, and re-asking IAM about one on every request is
// exactly what the cache exists to stop, so an empty subject that came from the
// cache is an answer and not an absence.
func billingKey(c *zip.Ctx) (subject, namespace, userKey string, held bool) {
	// Balance enforcement disabled ⇒ balanceGate is nil (InitBalanceGate returns
	// early when commerceEndpoint is unconfigured). RateLimitFilter calls this on
	// EVERY authenticated /v1 request BEFORE BalanceGateFilter's own nil guard, so
	// without this check `balanceGate.getUserKeyCached` below nil-derefs and panics
	// — a bare 500 on every authed request the moment Commerce is unwired. Resolve
	// no billing subject instead (fail-open): RateLimitFilter then bounds the caller
	// by the address they arrived from (its documented fallback) and nothing bills.
	// A disabled billing subsystem must never crash a request.
	if balanceGate == nil {
		return "", "", "", true
	}

	// The org the caller asked to act in. It is honored only where a signed `orgs`
	// claim proves membership, and it scopes the cache: the same token resolves to
	// a different wallet in a different org.
	requested := strings.TrimSpace(c.Header("X-Org-Id"))

	// Source 1: the signed token this request carries, as a bearer or as the
	// sign-in cookie (GetSessionClaims reads both, and they are the same token). Its
	// `orgs` claim decides an org switch by the two rules the controller's debit
	// applies (account.EffectiveOrg, then account.LedgerOrg), so the gate reads the
	// wallet and the plan of the org the caller is working in. An org the claim does
	// not cover is refused here, not resolved to the home wallet: "" is this
	// function's "no billing subject", which the controller refuses.
	if claims := (&controllers.ApiController{Ctx: c}).GetSessionClaims(); claims != nil && claims.User.Owner != "" {
		effective, orgErr := account.EffectiveOrg(claims.User.Owner, claims.Orgs, requested)
		if orgErr != nil {
			return "", "", "", true
		}
		ledger := account.LedgerOrg(effective, claims.User.Owner, util.IsSuperAdmin(&claims.User))
		return claims.User.PayerSubject(ledger), ledger, claims.User.Owner + "/" + claims.User.Name, true
	}

	// Source 2/3: the key the caller presented, on WHICHEVER transport this estate
	// accepts it — extractAPIKey names them, and naming them twice is how they drift.
	// It used to read the Authorization bearer alone, so an sk- key arriving on
	// x-api-key (the Anthropic wire protocol, /v1/messages, published in our own
	// docs) named no payer here at all: the same credential, the same tenant, and a
	// different answer depending on which header carried it.
	token := extractAPIKey(c)
	if token == "" {
		return "", "", "", true
	}

	// A publishable key identifies an org for ingest and authenticates nobody, so
	// there is no principal here to bill. Skip it in the router gate.
	if strings.HasPrefix(token, "pk-") {
		return "", "", "", true
	}

	// Check key cache first.
	if s, ns, uk, ok := balanceGate.getUserKeyCached(token, requested); ok {
		return s, ns, uk, true
	}

	// A JWT that did not verify above names nobody: signature and iss/aud are
	// checked by the one parse, so a foreign or forged token bills no subject and the
	// controller rejects it.
	if isJwtTokenLike(token) {
		return "", "", "", true
	}

	// An sk- this process has not resolved. Only IAM can say who holds it, so this
	// is the one answer billingKey does not have.
	if strings.HasPrefix(token, "sk-") {
		return "", "", "", false
	}

	return "", "", "", true
}

// isJwtTokenLike checks if a token looks like a JWT (3 dot-separated segments).
func isJwtTokenLike(token string) bool {
	parts := strings.Split(token, ".")
	return len(parts) == 3 && len(parts[0]) > 10 && len(parts[1]) > 10
}

// ── Balance checking ────────────────────────────────────────────────────────

// checkBalance reports whether the subject has a positive SPENDABLE balance
// (ledger balance minus outstanding reservations). When it does NOT, deny carries
// the ready-to-emit BillingNotice distinguishing the two denial reasons — because a
// funded caller behind a transient billing blip must never be told to add credits:
//
//   - KNOWN balance ≤ 0 (or fully reserved): genuine insufficiency → object.InsufficientBalance
//     (402, "add credits"). Applies to a fresh/stale ledger entry AND a cold subject
//     whose lookup SUCCEEDED with no spendable funds.
//   - COLD subject whose lookup ERRORS: the balance could not be verified →
//     object.BalanceUnavailable (503, "retry"). Still fail-CLOSED — the request is
//     denied — only the message/code/status differ.
//
// On a fresh ledger entry it returns immediately; on a stale entry it serves the
// (settle-adjusted) stale value and refreshes asynchronously; on a cold subject it
// fetches synchronously and seeds the ledger. Reading the ledger (not a private
// cache) makes the gate reservation-aware and reflects every local settle, so the
// cache window can never serve a stale-positive balance once the funds are spent.
//
// Fail posture is fail-CLOSED for BOTH denial reasons — an outage never becomes an
// unmetered bleed, and there is no exempt or fail-open escape. deny is the zero
// BillingNotice when sufficient. userKey is retained for caller-signature parity.
func (bg *BalanceGate) checkBalance(host, subject, namespace, userKey string) (sufficient bool, deny object.BillingNotice, balanceCents int64) {
	_ = userKey
	bal, reserved, fresh, known := bg.ledger.Snapshot(subject)
	if known {
		if !fresh {
			// Stale: serve the settle-adjusted value, refresh asynchronously.
			bg.refreshAsync(subject, namespace)
		}
		avail := bal - reserved
		if avail > 0 {
			return true, object.BillingNotice{}, avail
		}
		return false, object.InsufficientBalance(host, namespace, ""), avail
	}

	// Cold subject: fetch synchronously so the first request gets a real check.
	balance, err := bg.fetchBalance(subject, namespace)
	if err != nil {
		// Balance UNVERIFIABLE (transient billing-backend failure) → DENY, fail-CLOSED:
		// a balance we cannot read is never spent, and there is no exempt/fail-open
		// escape. DISTINCT from insufficiency — the caller is asked to retry (503), not
		// told to add credits, so a funded caller behind a blip is not misdirected.
		log.Warning("balance_gate: balance unverifiable for cold subject=%s: %v (fail-CLOSED, retryable)", subject, err)
		return false, object.BalanceUnavailable(), 0
	}

	bg.ledger.SetBalance(subject, balance)
	avail, _ := bg.ledger.Available(subject)
	if avail > 0 {
		return true, object.BillingNotice{}, avail
	}
	return false, object.InsufficientBalance(host, namespace, ""), avail
}

// empty reports a subject the ledger already holds with nothing to spend — the
// answer checkBalance gives it without a fetch, a stale entry refreshed behind it
// as there — and its spendable balance. A subject the ledger does not hold is not
// empty: only a fetch can say.
func (bg *BalanceGate) empty(subject, namespace string) (int64, bool) {
	bal, reserved, fresh, known := bg.ledger.Snapshot(subject)
	if !known || bal-reserved > 0 {
		return 0, false
	}
	if !fresh {
		bg.refreshAsync(subject, namespace)
	}
	return bal - reserved, true
}

// refreshAsync kicks off a background goroutine to refresh the ledger balance
// from Commerce. Deduplicates concurrent refreshes for the same subject.
func (bg *BalanceGate) refreshAsync(subject, namespace string) {
	bg.inflightMu.Lock()
	if _, running := bg.inflight[subject]; running {
		bg.inflightMu.Unlock()
		return
	}
	bg.inflight[subject] = struct{}{}
	bg.inflightMu.Unlock()

	go func() {
		defer func() {
			bg.inflightMu.Lock()
			delete(bg.inflight, subject)
			bg.inflightMu.Unlock()
		}()

		balance, err := bg.fetchBalance(subject, namespace)
		if err != nil {
			log.Warning("balance_gate: async refresh failed for user=%s: %v", subject, err)
			return
		}

		// SetBalance resets the balance from Commerce but PRESERVES outstanding
		// reservations for in-flight requests.
		bg.ledger.SetBalance(subject, balance)
	}()
}

// commerceBalanceResponse is the expected JSON shape from Commerce balance endpoint.
type commerceBalanceResponse struct {
	Available int64 `json:"available"`
}

// fetchBalance calls Commerce to get the current balance for a billing subject.
// The subject (?user=) is account.Payer(account.Credential{Owner: owner, Name: name}).Subject() — per-user for a
// personal-billing org, the org slug for a pooled org — and the namespace
// (X-Org-Id) is the org. Both must match what the deposit/usage writes use,
// so the credit a user receives is the balance read here and debited from.
// Per global rule: /v1/ only, never /api/. Commerce serves /v1/billing/balance.
func (bg *BalanceGate) fetchBalance(subject, namespace string) (int64, error) {
	// Native path: read the wallet balance DIRECTLY from the host's in-process
	// finance ledger (cloud), no HTTP. Falls back to the S2S HTTP read standalone.
	if r := object.BalanceReader(); r != nil {
		return r(stdcontext.Background(), subject, namespace, "usd")
	}
	balanceURL := fmt.Sprintf("%s/v1/billing/balance?user=%s&currency=usd", bg.endpoint, url.QueryEscape(subject))

	req, err := http.NewRequest(http.MethodGet, balanceURL, nil)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	if bg.token != "" {
		req.Header.Set("Authorization", "Bearer "+bg.token)
	}
	// Scope the service-token call to this org's namespace (commerce reads
	// X-Org-Id on the service-token path; absent => "hanzo" default).
	req.Header.Set("X-Org-Id", namespace)

	resp, err := bg.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("http: %w", err)
	}
	defer func() {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("commerce returned %d", resp.StatusCode)
	}

	var balanceResp commerceBalanceResponse
	if err := json.NewDecoder(resp.Body).Decode(&balanceResp); err != nil {
		return 0, fmt.Errorf("decode response: %w", err)
	}

	return balanceResp.Available, nil
}

// ── User key cache ──────────────────────────────────────────────────────────

// userKeyCacheKey is the identity of a cached billing answer: the bearer token
// AND the org the request asked to act in. One token resolves to one billing
// identity PER ORG, so the token alone is not a key — caching by it would serve
// one org's wallet to a request made in another. The cache composes the key
// itself so no caller can forget the org half.
func userKeyCacheKey(token, org string) string { return token + "\x00" + org }

// getUserKeyCached returns the cached (subject, namespace, userKey) for a token
// acting in org. The bool is false on a miss.
//
// A STALE ANSWER IS SERVED WHILE ONE RE-CHECK RUNS. Past userKeyCacheTTL, up to the
// 2×TTL cleanupLoop evicts at, the held answer is returned and recheckUserKey
// replaces it in the background, so a key in steady use never drops back behind the
// address lanes limitSubject asks before a round trip. It names a payer and nothing
// more: the controller authenticates the key on its own one-minute answer.
func (bg *BalanceGate) getUserKeyCached(token, org string) (subject, namespace, userKey string, ok bool) {
	bg.userKeyMu.RLock()
	entry, found := bg.userKeyCache[userKeyCacheKey(token, org)]
	bg.userKeyMu.RUnlock()

	if !found {
		return "", "", "", false
	}
	age := time.Since(entry.fetchedAt)
	if age > 2*userKeyCacheTTL {
		return "", "", "", false
	}
	if age > userKeyCacheTTL {
		bg.recheckUserKey(token, org)
	}
	return entry.subject, entry.namespace, entry.userKey, true
}

// recheckUserKey resolves token afresh in the background and holds IAM's answer,
// whatever it is — a revoked key comes back a miss. One re-check per (token, org) at
// a time; inflight dedupes it beside the balance refreshes, under a key no subject
// can spell.
func (bg *BalanceGate) recheckUserKey(token, org string) {
	id := "\x00" + userKeyCacheKey(token, org)
	bg.inflightMu.Lock()
	if _, running := bg.inflight[id]; running {
		bg.inflightMu.Unlock()
		return
	}
	bg.inflight[id] = struct{}{}
	bg.inflightMu.Unlock()

	go func() {
		defer func() {
			bg.inflightMu.Lock()
			delete(bg.inflight, id)
			bg.inflightMu.Unlock()
		}()
		subject, namespace, userKey := bg.resolveIAMKeySubject(token)
		bg.setUserKeyCache(token, org, subject, namespace, userKey)
	}()
}

// setUserKeyCache stores a (token, org) -> (subject, namespace, userKey) mapping.
func (bg *BalanceGate) setUserKeyCache(token, org, subject, namespace, userKey string) {
	bg.userKeyMu.Lock()
	bg.userKeyCache[userKeyCacheKey(token, org)] = &userKeyCacheEntry{subject: subject, namespace: namespace, userKey: userKey, fetchedAt: time.Now()}
	bg.userKeyMu.Unlock()
}

// ── IAM key resolution ──────────────────────────────────────────────────────

// resolveIAMKeySubject resolves an sk- API key to its billing identity:
// (subject, namespace, userKey). The namespace is the org `owner`; the subject
// is account.Payer(account.Credential{Owner: owner, Name: name}).Subject() — so a
// personal-org key bills per-user; the userKey is the exact "owner/name" for
// exemption matching. Returns ("", "", "") on any error (fail-open).
//
// The lookup itself is controllers.GetUserByAccessKey — the ONE key resolver, so
// the billing subject and the authz principal are by construction the same
// identity resolved over the same authenticated IAM transport. This file used to
// carry a second copy that passed clientId/clientSecret as QUERY PARAMS; IAM
// derives no principal from those, so it 401'd and this gate silently fail-opened
// on every key. One resolver means that cannot drift again.
func (bg *BalanceGate) resolveIAMKeySubject(apiKey string) (subject, namespace, userKey string) {
	u, err := controllers.GetUserByAccessKey(apiKey)
	if err != nil {
		log.Warning("balance_gate: IAM key resolve failed for key=%s: %v", maskKey(apiKey), err)
		return "", "", ""
	}
	if u == nil || u.Owner == "" {
		return "", "", ""
	}
	return u.PayerSubject(""), u.Owner, u.Owner + "/" + u.Name
}

// ── Cleanup ─────────────────────────────────────────────────────────────────

// cleanupLoop periodically evicts stale entries from both caches.
func (bg *BalanceGate) cleanupLoop() {
	ticker := time.NewTicker(balanceCacheCleanupInterval)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()

		// Evict idle ledger entries (no holds, balance older than 2×TTL).
		bg.ledger.EvictIdle(2 * balanceCacheTTL)

		bg.userKeyMu.Lock()
		for key, entry := range bg.userKeyCache {
			if now.Sub(entry.fetchedAt) > 2*userKeyCacheTTL {
				delete(bg.userKeyCache, key)
			}
		}
		bg.userKeyMu.Unlock()
	}
}

// retrieval reports whether path is an embedding or a rerank.
func retrieval(path string) bool {
	switch strings.ToLower(strings.TrimRight(path, "/")) {
	case "/v1/embeddings", "/v1/rerank":
		return true
	}
	return false
}

// person is the context the allowance hook is handed: the request's caller, STATED
// on a context with no request behind it. Stated, because the host names the person
// with zip.CallerOf and a raw handler's context carries none. With no request behind
// it, because a context carrying the request forwards that request's headers on the
// host's own plane call, and a request with no X-Org-Id — a sibling's completion,
// a machine token — would reach the allowance naming no org, be refused, and be
// admitted uncounted. Stated, the host addresses its call to the wallet's org.
func person(c *zip.Ctx) stdcontext.Context {
	return zip.WithCaller(c.Context(), zip.CallerOf(c.Forward()))
}
