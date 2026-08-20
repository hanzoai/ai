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

	// inflight tracks user keys currently being refreshed to deduplicate
	// concurrent async fetches.
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

	// Only enforce on API and v1 routes.
	if !strings.HasPrefix(path, "/v1/") {
		return c.Continue()
	}

	subject, namespace, userKey := resolveBillingKey(c)
	if subject == "" {
		// Cannot identify the billing subject — let downstream auth filters handle rejection.
		return c.Continue()
	}

	// An inline upload spends nothing: /v1/docs/ingest with source "upload" (or
	// unset, its default) indexes bytes the caller already has into the caller's
	// own store — no scrape, no crawl, no model. The controller's own gate prices
	// exactly the sources that DO spend (github/crawl/s3) and deliberately admits
	// upload; refusing it here made this gate and that one give different answers
	// to the same question. Same body requestedModel reads.
	if path == "/v1/docs/ingest" {
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
	if m := requestedModel(c); m != "" && controllers.ModelCostsNothing(m, namespace) {
		// Free is not unbounded. The wallet has nothing to refuse at zero, so the
		// plan's ALLOWANCE is what bounds this lane: a count of calls per subject per
		// period, held by the host. It is the one gate a free caller meets, and the
		// moment to offer them a plan.
		//
		// THIS ONLY READS IT. The ceiling bounds spend, and spend is incurred when a
		// model is reached — so the call is counted where it is served (recordUsage,
		// which runs for a success and nothing else) and this asks only whether the
		// caller is already out. A request refused here costs nothing, and so does one
		// that dies on an unresolvable route or a vendor that never answered: a caller
		// pays for answers, never for an outage of ours.
		//
		// An error is allowed through, and WHO gets that benefit is the host's call:
		// it holds the tenancy vocabulary, so it answers spent for a caller it cannot
		// name and returns the error only for one it can. A priced route never
		// reaches this branch, so nothing here can free a metered call.
		if spent := object.Spent(); spent != nil {
			if out, err := spent(c.Context(), subject, namespace); err == nil && out {
				log.Info("allowance: period allowance spent subject=%s namespace=%s path=%s",
					subject, namespace, path)
				c.SetHeader("Content-Type", "application/json")
				return c.Bytes(http.StatusPaymentRequired, []byte(
					`{"error":{"message":"You've used your free messages for today. They reset at midnight UTC — or pick a plan at https://hanzo.ai/pricing to keep going now.","type":"insufficient_quota","code":"allowance_spent"}}`))
			}
		}
		return c.Continue()
	}

	sufficient, deny, balance := balanceGate.checkBalance(c.Host(), subject, namespace, userKey)
	if sufficient {
		// Balance covers the call — but a plan also bounds how FAST a caller may burn
		// its budget: the rolling-window AI-spend cap (Anthropic-style, resets
		// continuously). This is distinct from insufficient_balance — the caller HAS
		// money; their plan's trailing-window budget is momentarily spent. Fails OPEN on
		// any error (see RollingCapReaderFunc): a finance blip must never 429 a paying
		// caller. Only a positively-computed over-cap denies, as HTTP 429.
		if reader := object.RollingCapReader(); reader != nil {
			if over, err := reader(c.Context(), subject, namespace); err == nil && over {
				log.Info("rolling_cap: window cap exceeded subject=%s namespace=%s path=%s",
					subject, namespace, path)
				c.SetHeader("Content-Type", "application/json")
				return c.Bytes(http.StatusTooManyRequests, []byte(
					`{"error":{"message":"You've reached your plan's usage limit for the moment. It resets shortly — or upgrade at https://hanzo.ai/pricing for a higher limit.","type":"rate_limit_error","code":"usage_cap_exceeded"}}`))
			}
		}
		return c.Continue()
	}

	// Denied. checkBalance decided WHICH denial: a known-insufficient balance (402,
	// add credits) or a balance it could not verify (503, retry) — both fail-CLOSED.
	log.Info("balance_gate: deny subject=%s namespace=%s balance_cents=%d code=%s status=%d path=%s",
		subject, namespace, balance, deny.Code, deny.Status, path)

	c.SetHeader("Content-Type", "application/json")
	return c.Bytes(deny.Status, deny.ErrorJSON())
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
	case path == "/v1/chat/public":
		return true
	case path == "/v1/health" || path == "/health":
		return true
	case path == "/v1/metrics" || path == "/metrics":
		return true
	// Public marketing aggregate — the world.hanzo.ai live-traffic globe. It exposes
	// only country/region counts + throughput rates (no IPs, no org/user), needs no
	// auth and no balance. Same class as /v1/router/stats?scope=platform.
	case strings.HasPrefix(path, "/v1/traffic/"):
		return true
	// Public marketing aggregate — the router flywheel stats the world.hanzo.ai /
	// console dashboards render (model/throughput/country counts; no org/user rows).
	// Same class as /v1/traffic/: no balance needed. Explicitly exempt so it is 200 on
	// BOTH the anon and the authed path — a $0-balance org's dashboard must never 402
	// where an anon viewer sees 200. (The read-method skip in BalanceGateFilter covers
	// it too; this states the intent at the source and is method-independent.)
	case path == "/v1/router/stats":
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
	case path == "/v1/feedback":
		return true
	// The router-config surface (/v1/router/{policy,defaults,ledger,rewards,artifact-meta}
	// + /v1/org/settings) is routing METADATA — per-org policy/allowlist/dial, the export
	// endpoints, the org settings — NOT metered inference. It is served over the router via
	// RouterConfigBridge → the ONE native ZAP handler, so it DOES traverse this filter and
	// must be balance-exempt exactly like /v1/router/stats + /v1/feedback: a $0-balance org
	// has to read/write its own router config from the console (auth is still enforced — the
	// handler self-auths). Dropping these from the exempt list would 402 every unfunded
	// org's Router → Policy tab.
	case path == "/v1/router/policy" ||
		path == "/v1/router/defaults" ||
		path == "/v1/router/ledger" ||
		path == "/v1/router/rewards" ||
		path == "/v1/router/artifact-meta" ||
		strings.HasPrefix(path, "/v1/org/settings"):
		return true
	// Router observability READS — the savings/quality aggregate + the improvement
	// time-series. Marketing/metadata, not metered inference (same class as
	// /v1/router/defaults): the public platform scope is unauthenticated (the
	// no-subject path already passes), and an authenticated org-scope read must not
	// 402 a $0-balance org either.
	case path == "/v1/router/stats" || path == "/v1/router/history" || path == "/v1/router/judge-panel":
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
//  2. JWT Bearer token (parsed locally, no network call)
//  3. IAM API key (sk- prefix, resolved via cached IAM lookup)
//
// Returns ("", "", "") if the subject cannot be identified (fail-open: filter
// skips). The userKey is the exact "owner/name" identity used for per-user
// exemption matching (mirrors the controller backstop), independent of whether
// the billing subject collapses to the org slug for a pooled org.
func resolveBillingKey(c *zip.Ctx) (subject, namespace, userKey string) {
	// Balance enforcement disabled ⇒ balanceGate is nil (InitBalanceGate returns
	// early when commerceEndpoint is unconfigured). RateLimitFilter calls this on
	// EVERY authenticated /v1 request BEFORE BalanceGateFilter's own nil guard, so
	// without this check `balanceGate.getUserKeyCached` below nil-derefs and panics
	// — a bare 500 on every authed request the moment Commerce is unwired. Resolve
	// no billing subject instead (fail-open): RateLimitFilter then buckets by the
	// raw key (its documented fallback) and nothing bills. A disabled billing
	// subsystem must never crash a request.
	if balanceGate == nil {
		return "", "", ""
	}

	// Source 1: session user from AutoSigninFilter. A cookie session carries no
	// signed `orgs` claim, so it can never switch org — it always bills its home.
	user := GetSessionUser(c)
	if user != nil && user.Owner != "" {
		return user.PayerSubject(""), user.Owner, user.Owner + "/" + user.Name
	}

	// Source 2/3: Bearer token.
	token := parseBearerToken(c)
	if token == "" {
		return "", "", ""
	}

	// The org the caller asked to act in. Unvalidated here — only the JWT branch
	// below, which holds the signed membership set, may honor it. It scopes the
	// cache because the SAME token resolves to a DIFFERENT wallet in a different
	// org: keying the cache on the token alone would serve one org's billing
	// identity to a request made in another.
	requested := strings.TrimSpace(c.Header("X-Org-Id"))

	// A publishable key identifies an org for ingest and authenticates nobody, so
	// there is no principal here to bill. Skip it in the router gate.
	if strings.HasPrefix(token, "pk-") {
		return "", "", ""
	}

	// Check key cache first.
	if s, ns, uk, ok := balanceGate.getUserKeyCached(token, requested); ok {
		return s, ns, uk
	}

	// JWT token: parse locally (cheap, no network). Signature + iss/aud
	// validated (R3) — a foreign-aud/wrong-issuer token resolves no billing
	// subject here, so it is not billed and the controller rejects it (401).
	if isJwtTokenLike(token) {
		claims, err := object.ParseAndValidateJWT(token)
		if err != nil {
			return "", "", ""
		}
		if claims.User.Owner != "" {
			// This is the ONE auth path that can honor an org switch, because it is
			// the only one holding the signed `orgs` claim that proves membership. The
			// gate must read the wallet the controller's debit will land on, so it
			// resolves the ledger by the same two rules (account.EffectiveOrg, then
			// account.LedgerOrg) the controller applies — check one balance and drain
			// another and a funded org 402s while an unfunded one runs free.
			// An org the signed claim does not cover is REFUSED here, not resolved
			// to the caller's home wallet. Falling back would make this gate read a
			// balance nobody selected and then let the controller debit it — the
			// gate and the debit would agree, and both would be wrong. "" is this
			// function's existing "no billing subject", which the controller
			// rejects rather than serving free.
			effective, orgErr := account.EffectiveOrg(claims.User.Owner, claims.Orgs, requested)
			if orgErr != nil {
				return "", "", ""
			}
			ledger := account.LedgerOrg(
				effective,
				claims.User.Owner,
				util.IsSuperAdmin(&claims.User),
			)
			// The token NAMES its payer: IAM signs `billing_account` from the real
			// grant context, so the gate reads who pays instead of inferring it from
			// User.Type — a field the user can set on themselves. Machine stays as the
			// fallback for tokens minted before the claim shipped; when the claim is
			// present Payer ignores it, so a forged Type can no longer point this gate
			// at the signup org's pooled balance. A claim naming the home org is
			// discarded on a switched request (Payer honors it only within the
			// credential's own org), which is right: the selected org's wallet pays.
			subject = claims.User.PayerSubject(ledger)
			// userKey stays the caller's IDENTITY ("<home>/<name>"), never the ledger:
			// it keys per-user exemption matching, not money.
			userKey = claims.User.Owner + "/" + claims.User.Name
			balanceGate.setUserKeyCache(token, requested, subject, ledger, userKey)
			return subject, ledger, userKey
		}
		return "", "", ""
	}

	// IAM API key (sk- prefix): resolve via IAM (cached). An API key carries no
	// signed membership set, so it never switches — it bills the org that owns it.
	//
	// The MISS is cached too, and deliberately: an upstream vendor key wears the
	// same sk- spelling and IAM will never own it, so caching only the hit would
	// buy a fresh IAM round-trip on every request such a caller makes. A miss
	// resolves no subject, which is the fail-open this gate already takes — the
	// controller still bills that call against its provider row.
	if strings.HasPrefix(token, "sk-") {
		subject, namespace, userKey = balanceGate.resolveIAMKeySubject(token)
		balanceGate.setUserKeyCache(token, requested, subject, namespace, userKey)
		return subject, namespace, userKey
	}

	return "", "", ""
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
// acting in org. The bool is false on miss/stale.
func (bg *BalanceGate) getUserKeyCached(token, org string) (subject, namespace, userKey string, ok bool) {
	bg.userKeyMu.RLock()
	entry, found := bg.userKeyCache[userKeyCacheKey(token, org)]
	bg.userKeyMu.RUnlock()

	if !found || time.Since(entry.fetchedAt) > userKeyCacheTTL {
		return "", "", "", false
	}
	return entry.subject, entry.namespace, entry.userKey, true
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
