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
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/beego/context"
	"github.com/hanzoai/beego/logs"
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

	iamEndpoint  string // IAM base URL for hk- key resolution
	clientId     string // IAM application client ID
	clientSecret string // IAM application client secret
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

// InitBalanceGate reads Commerce and IAM connection parameters from app config
// and creates the balance gate. Must be called once during startup. If Commerce
// is not configured, the gate is not created and BalanceGateFilter is a no-op.
func InitBalanceGate() {
	endpoint := conf.GetConfigString("commerceEndpoint")
	if endpoint == "" {
		logs.Info("balance_gate: commerceEndpoint not configured, balance enforcement disabled")
		return
	}
	endpoint = strings.TrimRight(endpoint, "/")
	token := conf.GetConfigString("commerceToken")

	iamEndpoint := conf.GetConfigString("IAM_URL")
	if iamEndpoint != "" {
		iamEndpoint = strings.TrimRight(iamEndpoint, "/")
	}
	clientId := conf.GetConfigString("IAM_CLIENT_ID")
	clientSecret := conf.GetConfigString("IAM_CLIENT_SECRET")

	bg := &BalanceGate{
		ledger:       object.GlobalBalanceLedger,
		userKeyCache: make(map[string]*userKeyCacheEntry),
		inflight:     make(map[string]struct{}),
		endpoint:     endpoint,
		token:        token,
		client:       &http.Client{Timeout: balanceHTTPTimeout},
		iamEndpoint:  iamEndpoint,
		clientId:     clientId,
		clientSecret: clientSecret,
	}

	go bg.cleanupLoop()

	balanceGate = bg
	logs.Info("balance_gate: initialized (endpoint=%s, ttl=%v)", endpoint, balanceCacheTTL)
}

// ── Filter function ─────────────────────────────────────────────────────────

// BalanceGateFilter is a Beego BeforeRouter filter that checks whether the
// requesting user has a positive Commerce balance before allowing paid API
// requests to proceed. It runs after AutoSigninFilter (which sets session
// users for legacy auth paths) and handles its own user resolution for
// JWT and IAM API key auth paths.
//
// Design: fail-open. If Commerce is unreachable or the user cannot be
// identified, the request is allowed through. The controller-level balance
// check in resolveProviderForUser remains as a defense-in-depth backstop.
func BalanceGateFilter(ctx *context.Context) {
	if balanceGate == nil {
		return
	}

	path := ctx.Request.URL.Path

	if isBalanceExempt(path) {
		return
	}

	// Only enforce on API and v1 routes.
	if !strings.HasPrefix(path, "/v1/") {
		return
	}

	subject, namespace, userKey := resolveBillingKey(ctx)
	if subject == "" {
		// Cannot identify the billing subject — let downstream auth filters handle rejection.
		return
	}

	sufficient, balance := balanceGate.checkBalance(subject, namespace, userKey)
	if sufficient {
		return
	}

	logs.Info("balance_gate: insufficient balance subject=%s namespace=%s balance_cents=%d path=%s",
		subject, namespace, balance, path)

	ctx.ResponseWriter.Header().Set("Content-Type", "application/json")
	ctx.ResponseWriter.WriteHeader(http.StatusPaymentRequired)

	body := `{"error":{"message":"Insufficient balance. Please add credits to your wallet at https://pay.hanzo.ai","type":"billing_error","code":"insufficient_balance"}}`
	ctx.ResponseWriter.Write([]byte(body))
}

// isBalanceExempt returns true for paths that should bypass balance checking
// (free/public endpoints, health checks, etc.).
func isBalanceExempt(path string) bool {
	switch {
	case path == "/v1/health" || path == "/health":
		return true
	case path == "/v1/metrics" || path == "/metrics":
		return true
	// Model + pricing CATALOG listings are metadata, not metered inference:
	// a caller must still be authenticated (the auth filters enforce that —
	// balance-exemption is NOT auth-exemption), but must never need a positive
	// balance just to READ the available-models list. Gating /v1/models on
	// balance 402s a funded-but-zero or M2M caller browsing the catalog — the
	// "402 on free /v1/models" console-wide outage class.
	case path == "/v1/models" || strings.HasPrefix(path, "/v1/models/"):
		return true
	case strings.HasPrefix(path, "/v1/get-version-info"):
		return true
	case strings.HasPrefix(path, "/v1/get-system-info"):
		return true
	case strings.HasPrefix(path, "/v1/signin"):
		return true
	case path == "/v1/signout":
		return true
	case path == "/v1/get-account":
		return true
	// Usage/spend READS are account metadata, not metered inference. A caller
	// must ALWAYS be able to SEE its own usage — especially to learn it needs
	// credits — so a $0-balance org never 402s on the usage view (same class as
	// /v1/models + /v1/get-account; the auth filters still require a principal).
	// Gating these was the "insufficient balance on the usage panel" outage.
	case path == "/v1/get-cloud-usages" ||
		path == "/v1/get-usages" ||
		path == "/v1/get-range-usages":
		return true
	// The reward/feedback signal is training metadata, not metered inference: a
	// caller must be able to score a past request even at $0 balance (the outcome
	// label is exactly how the enso loop learns). Auth still required (the handler
	// self-auths); only the balance 402 is skipped.
	case path == "/v1/feedback" || path == "/v1/add-routing-reward":
		return true
	// Routing configuration is metadata, not metered inference — same class as
	// /v1/models. Every client fetches its org's effective defaults on boot, so
	// gating the READ 402s an unfunded org's apps before any priced request
	// exists; the org-settings CRUD + ledger export are platform administration
	// (RequireGlobalAdmin-gated) — an operator flipping routing must never
	// depend on a wallet balance. Auth filters still apply to all of them.
	case path == "/v1/get-routing-defaults" ||
		path == "/v1/get-org-settings" ||
		path == "/v1/add-org-settings" ||
		path == "/v1/update-org-settings" ||
		path == "/v1/delete-org-settings" ||
		path == "/v1/export-routing-ledger":
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
//  3. IAM API key (hk- prefix, resolved via cached IAM lookup)
//
// Returns ("", "", "") if the subject cannot be identified (fail-open: filter
// skips). The userKey is the exact "owner/name" identity used for per-user
// exemption matching (mirrors the controller backstop), independent of whether
// the billing subject collapses to the org slug for a pooled org.
func resolveBillingKey(ctx *context.Context) (subject, namespace, userKey string) {
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

	// Source 1: session user from AutoSigninFilter.
	user := GetSessionUser(ctx)
	if user != nil && user.Owner != "" {
		return account.Payer(account.Credential{Owner: user.Owner, Name: user.Name, Machine: account.IsMachine(user.Type)}).Subject(), user.Owner, user.Owner + "/" + user.Name
	}

	// Source 2/3: Bearer token.
	token := parseBearerToken(ctx)
	if token == "" {
		return "", "", ""
	}

	// Widget keys (hz_) bill the OWNER ORG that minted the key (mirrors the
	// controller's authResolveProvider): resolve that org here so the router
	// balance gate applies to widget traffic too. Type "application" (empty name)
	// collapses the subject to the org ledger. An unattributable widget key (no
	// owner mapping / default) resolves no subject — the controller refuses it
	// (fail-secure), so it is never billed-free here either.
	if strings.HasPrefix(token, "hz_") {
		owner := strings.TrimSpace(object.WidgetKeyOwner(token))
		if owner == "" {
			return "", "", ""
		}
		return account.Payer(account.Credential{Owner: owner, Name: "", Machine: account.IsMachine("application")}).Subject(), owner, owner + "/widget"
	}

	// Provider keys (sk-) and publishable keys (pk-) don't map to IAM orgs with
	// Commerce balances here — the controller attributes sk- to its provider
	// owner; pk- is read-only. Skip in the router gate.
	if strings.HasPrefix(token, "sk-") || strings.HasPrefix(token, "pk-") {
		return "", "", ""
	}

	// Check key cache first.
	if s, ns, uk, ok := balanceGate.getUserKeyCached(token); ok {
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
			// The token NAMES its payer: IAM signs `billing_account` from the real
			// grant context, so the gate reads who pays instead of inferring it from
			// User.Type — a field the user can set on themselves. Machine stays as the
			// fallback for tokens minted before the claim shipped; when the claim is
			// present Payer ignores it, so a forged Type can no longer point this gate
			// at the signup org's pooled balance.
			subject = account.Payer(account.Credential{Owner: claims.User.Owner, Name: claims.User.Name, Account: claims.BillingAccount, Machine: account.IsMachine(claims.User.Type)}).Subject()
			userKey = claims.User.Owner + "/" + claims.User.Name
			balanceGate.setUserKeyCache(token, subject, claims.User.Owner, userKey)
			return subject, claims.User.Owner, userKey
		}
		return "", "", ""
	}

	// IAM API key (hk- prefix): resolve via IAM (cached).
	if strings.HasPrefix(token, "hk-") {
		subject, namespace, userKey = balanceGate.resolveIAMKeySubject(token)
		if subject != "" {
			balanceGate.setUserKeyCache(token, subject, namespace, userKey)
		}
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

// checkBalance returns whether the subject has a positive SPENDABLE balance
// (ledger balance minus outstanding reservations). On a fresh ledger entry it
// returns immediately; on a stale entry it serves the (settle-adjusted) stale
// value and refreshes asynchronously; on a cold subject it fetches synchronously
// and seeds the ledger.
//
// Reading the ledger (not a private cache) makes the gate reservation-aware and
// reflects every local settle, so the cache window can never serve a
// stale-positive balance once the funds are spent.
//
// Fail posture on a COLD subject whose Commerce lookup errors: fail CLOSED,
// always — an outage must never become an unmetered bleed, and there is no
// exempt or fail-open escape. userKey is retained for caller-signature parity.
func (bg *BalanceGate) checkBalance(subject, namespace, userKey string) (sufficient bool, balanceCents int64) {
	_ = userKey
	bal, reserved, fresh, known := bg.ledger.Snapshot(subject)
	if known {
		if !fresh {
			// Stale: serve the settle-adjusted value, refresh asynchronously.
			bg.refreshAsync(subject, namespace)
		}
		avail := bal - reserved
		return avail > 0, avail
	}

	// Cold subject: fetch synchronously so the first request gets a real check.
	balance, err := bg.fetchBalance(subject, namespace)
	if err != nil {
		// Balance unknown → DENY. A Commerce outage must never become an
		// unmetered bleed, and there is no exempt/fail-open escape.
		logs.Warning("balance_gate: Commerce lookup failed for cold subject=%s: %v (fail-CLOSED)", subject, err)
		return false, 0
	}

	bg.ledger.SetBalance(subject, balance)
	avail, _ := bg.ledger.Available(subject)
	return avail > 0, avail
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
			logs.Warning("balance_gate: async refresh failed for user=%s: %v", subject, err)
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

// getUserKeyCached returns the cached (subject, namespace, userKey) for a token.
// The bool is false on miss/stale.
func (bg *BalanceGate) getUserKeyCached(token string) (subject, namespace, userKey string, ok bool) {
	bg.userKeyMu.RLock()
	entry, found := bg.userKeyCache[token]
	bg.userKeyMu.RUnlock()

	if !found || time.Since(entry.fetchedAt) > userKeyCacheTTL {
		return "", "", "", false
	}
	return entry.subject, entry.namespace, entry.userKey, true
}

// setUserKeyCache stores a token -> (subject, namespace, userKey) mapping.
func (bg *BalanceGate) setUserKeyCache(token, subject, namespace, userKey string) {
	bg.userKeyMu.Lock()
	bg.userKeyCache[token] = &userKeyCacheEntry{subject: subject, namespace: namespace, userKey: userKey, fetchedAt: time.Now()}
	bg.userKeyMu.Unlock()
}

// ── IAM key resolution ──────────────────────────────────────────────────────

// iamUserResponse matches the IAM API response shape for get-user.
type iamUserResponse struct {
	Status string `json:"status"`
	Msg    string `json:"msg"`
	Data   *struct {
		Owner string `json:"owner"`
		Name  string `json:"name"`
	} `json:"data"`
}

// resolveIAMKeySubject calls IAM to resolve an hk- API key to its billing
// identity: (subject, namespace, userKey). The namespace is the org `owner`; the
// subject is account.Payer(account.Credential{Owner: owner, Name: name}).Subject() — so a personal-org key bills
// per-user; the userKey is the exact "owner/name" for exemption matching.
// Returns ("", "", "") on any error (fail-open).
func (bg *BalanceGate) resolveIAMKeySubject(apiKey string) (subject, namespace, userKey string) {
	if bg.iamEndpoint == "" {
		return "", "", ""
	}

	iamURL := fmt.Sprintf("%s/v1/iam/get-user?accessKey=%s", bg.iamEndpoint, url.QueryEscape(apiKey))
	if bg.clientId != "" && bg.clientSecret != "" {
		iamURL += "&clientId=" + url.QueryEscape(bg.clientId) + "&clientSecret=" + url.QueryEscape(bg.clientSecret)
	}

	req, err := http.NewRequest(http.MethodGet, iamURL, nil)
	if err != nil {
		logs.Warning("balance_gate: IAM request build failed for key=%s: %v", maskKey(apiKey), err)
		return "", "", ""
	}

	resp, err := bg.client.Do(req)
	if err != nil {
		logs.Warning("balance_gate: IAM request failed for key=%s: %v", maskKey(apiKey), err)
		return "", "", ""
	}
	defer func() {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		logs.Warning("balance_gate: IAM returned %d for key=%s", resp.StatusCode, maskKey(apiKey))
		return "", "", ""
	}

	var result iamUserResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		logs.Warning("balance_gate: IAM response decode failed for key=%s: %v", maskKey(apiKey), err)
		return "", "", ""
	}

	if result.Status != "ok" || result.Data == nil {
		return "", "", ""
	}

	if result.Data.Owner == "" {
		return "", "", ""
	}

	return account.Payer(account.Credential{Owner: result.Data.Owner, Name: result.Data.Name}).Subject(), result.Data.Owner, result.Data.Owner + "/" + result.Data.Name
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
