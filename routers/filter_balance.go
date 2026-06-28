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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/beego/beego/context"
	"github.com/beego/beego/logs"
	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/object"
	iam "github.com/hanzoai/iam"
)

// ── Service key exemption ────────────────────────────────────────────────────

// balanceExemptKeys holds API keys that bypass balance checks (e.g. internal
// service accounts). Populated once at init from the BALANCE_EXEMPT_KEYS env
// var (comma-separated list of keys).
var balanceExemptKeys map[string]struct{}

func init() {
	balanceExemptKeys = make(map[string]struct{})
	if raw := os.Getenv("BALANCE_EXEMPT_KEYS"); raw != "" {
		for _, k := range strings.Split(raw, ",") {
			k = strings.TrimSpace(k)
			if k != "" {
				balanceExemptKeys[k] = struct{}{}
			}
		}
	}
}

// parseExemptOrgs extracts the org slugs (owner-part) from a comma-separated
// BALANCE_EXEMPT_USERS list of "owner/name" entries (e.g. "admin/hanzo-cloud,
// hanzo/z" -> {admin, hanzo}). These orgs are never fail-closed on a
// Commerce-error hard miss, mirroring the controller-level metering exemption
// (controllers/openai_api.go) so internal/house traffic survives an outage.
func parseExemptOrgs(raw string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, e := range strings.Split(raw, ",") {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		owner := e
		if i := strings.IndexByte(e, '/'); i >= 0 {
			owner = e[:i]
		}
		owner = strings.ToLower(strings.TrimSpace(owner))
		if owner != "" {
			out[owner] = struct{}{}
		}
	}
	return out
}

// isExemptOrg reports whether an org slug is exempt from the fail-closed gate.
func (bg *BalanceGate) isExemptOrg(orgKey string) bool {
	if len(bg.exemptOrgs) == 0 {
		return false
	}
	_, ok := bg.exemptOrgs[strings.ToLower(orgKey)]
	return ok
}

// ── Balance gate configuration ──────────────────────────────────────────────

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

	// failOpenOnError, when true, restores the legacy behavior of allowing a
	// request through when a hard cache-miss balance lookup errors (Commerce
	// unreachable). Default false = fail CLOSED for non-exempt orgs, so a
	// Commerce outage cannot become an unmetered bleed. Toggled at runtime via
	// BALANCE_GATE_FAIL_OPEN_ON_ERROR (the no-rebuild escape hatch).
	failOpenOnError bool

	// exemptOrgs holds org slugs that ALWAYS fail OPEN on a Commerce-error hard
	// miss (never blocked), derived from the owner-part of BALANCE_EXEMPT_USERS
	// (e.g. "admin/hanzo-cloud" -> "admin", "hanzo/z" -> "hanzo"). These mirror
	// the controller-level metering exemption (openai_api.go) so internal/house
	// traffic is never blocked by an outage.
	exemptOrgs map[string]struct{}
}

// userKeyCacheEntry maps an API token to the resolved billing identity: the
// per-user subject (?user=) and the org namespace (X-Hanzo-Org).
type userKeyCacheEntry struct {
	subject   string
	namespace string
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

		failOpenOnError: strings.EqualFold(strings.TrimSpace(os.Getenv("BALANCE_GATE_FAIL_OPEN_ON_ERROR")), "true"),
		exemptOrgs:      parseExemptOrgs(os.Getenv("BALANCE_EXEMPT_USERS")),
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

	subject, namespace := resolveBillingKey(ctx)
	if subject == "" {
		// Cannot identify the billing subject — let downstream auth filters handle rejection.
		return
	}

	sufficient, balance := balanceGate.checkBalance(subject, namespace)
	if sufficient {
		return
	}

	logs.Info("balance_gate: insufficient balance subject=%s namespace=%s balance_cents=%d path=%s",
		subject, namespace, balance, path)

	ctx.ResponseWriter.Header().Set("Content-Type", "application/json")
	ctx.ResponseWriter.WriteHeader(http.StatusPaymentRequired)

	body := `{"error":{"message":"Insufficient balance. Please add credits at console.hanzo.ai","type":"billing_error","code":"insufficient_balance"}}`
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
	// /api/models and /v1/models require authentication (R-04).
	// Removed from balance exemption — callers must have a valid token.
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
	default:
		return false
	}
}

// resolveBillingKey extracts the billing identity from the request context and
// returns (subject, namespace). The namespace is the IAM org slug (X-Hanzo-Org);
// the subject is object.BillingSubject(owner, name) — "owner/name" for a
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
// Returns ("", "") if the subject cannot be identified (fail-open: filter skips).
func resolveBillingKey(ctx *context.Context) (subject, namespace string) {
	// Source 1: session user from AutoSigninFilter.
	user := GetSessionUser(ctx)
	if user != nil && user.Owner != "" {
		return object.BillingSubject(user.Owner, user.Name), user.Owner
	}

	// Source 2/3: Bearer token.
	token := parseBearerToken(ctx)
	if token == "" {
		return "", ""
	}

	// Provider keys (sk-), publishable keys (pk-), and widget keys (hz_)
	// don't map to IAM orgs with Commerce balances — skip.
	if strings.HasPrefix(token, "sk-") || strings.HasPrefix(token, "pk-") || strings.HasPrefix(token, "hz_") {
		return "", ""
	}

	// Exempt service account keys (e.g. cloud agent internal keys).
	if _, exempt := balanceExemptKeys[token]; exempt {
		return "", ""
	}

	// Check key cache first.
	if s, ns, ok := balanceGate.getUserKeyCached(token); ok {
		return s, ns
	}

	// JWT token: parse locally (cheap, no network).
	if isJwtTokenLike(token) {
		claims, err := iam.ParseJwtToken(token)
		if err != nil {
			return "", ""
		}
		if claims.User.Owner != "" {
			subject = object.BillingSubject(claims.User.Owner, claims.User.Name)
			balanceGate.setUserKeyCache(token, subject, claims.User.Owner)
			return subject, claims.User.Owner
		}
		return "", ""
	}

	// IAM API key (hk- prefix): resolve via IAM (cached).
	if strings.HasPrefix(token, "hk-") {
		subject, namespace = balanceGate.resolveIAMKeySubject(token)
		if subject != "" {
			balanceGate.setUserKeyCache(token, subject, namespace)
		}
		return subject, namespace
	}

	return "", ""
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
// Fail posture on a COLD subject whose Commerce lookup errors: fail CLOSED for
// non-exempt orgs (an outage must not become an unmetered bleed); exempt/house
// orgs and the BALANCE_GATE_FAIL_OPEN_ON_ERROR override keep the legacy fail-open.
func (bg *BalanceGate) checkBalance(subject, namespace string) (sufficient bool, balanceCents int64) {
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
		if bg.failOpenOnError || bg.isExemptOrg(namespace) {
			logs.Warning("balance_gate: Commerce lookup failed for user=%s: %v (fail-open: exempt/override)", subject, err)
			return true, 0
		}
		logs.Warning("balance_gate: Commerce lookup failed for cold non-exempt subject=%s: %v (fail-CLOSED)", subject, err)
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
// The subject (?user=) is object.BillingSubject(owner, name) — per-user for a
// personal-billing org, the org slug for a pooled org — and the namespace
// (X-Hanzo-Org) is the org. Both must match what the deposit/usage writes use,
// so the credit a user receives is the balance read here and debited from.
// Per global rule: /v1/ only, never /api/. Commerce serves /v1/billing/balance.
func (bg *BalanceGate) fetchBalance(subject, namespace string) (int64, error) {
	balanceURL := fmt.Sprintf("%s/v1/billing/balance?user=%s&currency=usd", bg.endpoint, url.QueryEscape(subject))

	req, err := http.NewRequest(http.MethodGet, balanceURL, nil)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	if bg.token != "" {
		req.Header.Set("Authorization", "Bearer "+bg.token)
	}
	// Scope the service-token call to this org's namespace (commerce reads
	// X-Hanzo-Org on the service-token path; absent => "hanzo" default).
	req.Header.Set("X-Hanzo-Org", namespace)

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

// getUserKeyCached returns the cached (subject, namespace) for a token. The
// bool is false on miss/stale.
func (bg *BalanceGate) getUserKeyCached(token string) (subject, namespace string, ok bool) {
	bg.userKeyMu.RLock()
	entry, found := bg.userKeyCache[token]
	bg.userKeyMu.RUnlock()

	if !found || time.Since(entry.fetchedAt) > userKeyCacheTTL {
		return "", "", false
	}
	return entry.subject, entry.namespace, true
}

// setUserKeyCache stores a token -> (subject, namespace) mapping in the cache.
func (bg *BalanceGate) setUserKeyCache(token, subject, namespace string) {
	bg.userKeyMu.Lock()
	bg.userKeyCache[token] = &userKeyCacheEntry{subject: subject, namespace: namespace, fetchedAt: time.Now()}
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
// identity: (subject, namespace). The namespace is the org `owner`; the subject
// is object.BillingSubject(owner, name) — so a personal-org key bills per-user.
// Returns ("", "") on any error (fail-open).
func (bg *BalanceGate) resolveIAMKeySubject(apiKey string) (subject, namespace string) {
	if bg.iamEndpoint == "" {
		return "", ""
	}

	iamURL := fmt.Sprintf("%s/v1/iam/get-user?accessKey=%s", bg.iamEndpoint, url.QueryEscape(apiKey))
	if bg.clientId != "" && bg.clientSecret != "" {
		iamURL += "&clientId=" + url.QueryEscape(bg.clientId) + "&clientSecret=" + url.QueryEscape(bg.clientSecret)
	}

	req, err := http.NewRequest(http.MethodGet, iamURL, nil)
	if err != nil {
		logs.Warning("balance_gate: IAM request build failed for key=%s: %v", maskKey(apiKey), err)
		return "", ""
	}

	resp, err := bg.client.Do(req)
	if err != nil {
		logs.Warning("balance_gate: IAM request failed for key=%s: %v", maskKey(apiKey), err)
		return "", ""
	}
	defer func() {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		logs.Warning("balance_gate: IAM returned %d for key=%s", resp.StatusCode, maskKey(apiKey))
		return "", ""
	}

	var result iamUserResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		logs.Warning("balance_gate: IAM response decode failed for key=%s: %v", maskKey(apiKey), err)
		return "", ""
	}

	if result.Status != "ok" || result.Data == nil {
		return "", ""
	}

	if result.Data.Owner == "" {
		return "", ""
	}

	return object.BillingSubject(result.Data.Owner, result.Data.Name), result.Data.Owner
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
