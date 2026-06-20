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
	"github.com/hanzoai/ai/util"
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

// ── Balance gate configuration ──────────────────────────────────────────────

const (
	// balanceCacheTTL controls how long a cached balance result is considered
	// fresh. Within TTL a cached result is served immediately with no refresh.
	balanceCacheTTL = 30 * time.Second

	// balanceStaleGrace bounds how long past balanceCacheTTL a cached result may
	// still be served while an async refresh runs (stale-while-revalidate). This
	// caps the free-spend window when Commerce is unreachable: a user who has
	// dropped to zero can keep being ALLOWED on a stale positive only until
	// balanceCacheTTL+balanceStaleGrace, after which the entry is treated as
	// unknown and the request is denied (unless BILLING_FAIL_OPEN). It is an
	// explicit, short, fail-closed-honoring bound — NOT the 2*TTL cleanup
	// interval, which exists only to evict memory, not to authorize spend.
	balanceStaleGrace = 15 * time.Second

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

// balanceCacheEntry holds a cached balance check result for a single user.
type balanceCacheEntry struct {
	balanceCents int64
	fetchedAt    time.Time
}

// BalanceGate caches user balance checks to avoid hitting Commerce on every
// request. Stale entries are served immediately while an async refresh runs
// in the background — the hot path never blocks on network I/O.
type BalanceGate struct {
	mu      sync.RWMutex
	entries map[string]*balanceCacheEntry

	// userKeyCache maps Bearer token -> "owner/name" to avoid re-parsing
	// JWTs or re-calling IAM on every request for the same token.
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

	// fetch resolves a user's balance in cents from Commerce. It defaults to
	// (*BalanceGate).fetchBalance; tests inject a stub to exercise checkBalance's
	// fresh/stale/miss/unreachable matrix without a live Commerce server.
	fetch func(userKey string) (int64, error)
}

// userKeyCacheEntry maps an API token to the resolved "owner/name" user key.
type userKeyCacheEntry struct {
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
		entries:      make(map[string]*balanceCacheEntry),
		userKeyCache: make(map[string]*userKeyCacheEntry),
		inflight:     make(map[string]struct{}),
		endpoint:     endpoint,
		token:        token,
		client:       &http.Client{Timeout: balanceHTTPTimeout},
		iamEndpoint:  iamEndpoint,
		clientId:     clientId,
		clientSecret: clientSecret,
	}
	bg.fetch = bg.fetchBalance

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
// Design: fail-closed by default. A positive balance allows; a non-positive
// balance returns 402; an unknown balance (Commerce unreachable) returns 503 —
// unless BILLING_FAIL_OPEN=true, which allows on unknown (see checkBalance).
// Unidentified callers are passed through to the downstream auth filters, which
// reject anonymous traffic; the controller-level check in getUserBalance is a
// defense-in-depth backstop.
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

	userKey := resolveUserKey(ctx)
	if userKey == "" {
		// Cannot identify user — let downstream auth filters handle rejection.
		return
	}

	allowed, balance, unknown := balanceGate.checkBalance(userKey)
	if allowed {
		return
	}

	ctx.ResponseWriter.Header().Set("Content-Type", "application/json")

	if unknown {
		// Balance could not be determined (Commerce unreachable) and fail-open is
		// off -> deny as temporarily unavailable, matching the metering client.
		logs.Warning("balance_gate: balance unknown, denying user=%s path=%s (fail-closed)", userKey, path)
		ctx.ResponseWriter.WriteHeader(http.StatusServiceUnavailable)
		ctx.ResponseWriter.Write([]byte(`{"error":{"message":"Billing temporarily unavailable","type":"billing_error","code":"balance_unavailable"}}`))
		return
	}

	logs.Info("balance_gate: insufficient balance user=%s balance_cents=%d path=%s",
		userKey, balance, path)
	ctx.ResponseWriter.WriteHeader(http.StatusPaymentRequired)
	ctx.ResponseWriter.Write([]byte(`{"error":{"message":"Insufficient balance. Please add credits at console.hanzo.ai","type":"billing_error","code":"insufficient_balance"}}`))
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

// resolveUserKey extracts the "owner/name" user key from the request context.
// It checks three sources in order:
//  1. Session user (set by AutoSigninFilter for legacy auth)
//  2. JWT Bearer token (parsed locally, no network call)
//  3. IAM API key (hk- prefix, resolved via cached IAM lookup)
//
// Returns "" if the user cannot be identified (fail-open: filter skips).
func resolveUserKey(ctx *context.Context) string {
	// Source 1: session user from AutoSigninFilter.
	user := GetSessionUser(ctx)
	if user != nil && user.Owner != "" && user.Name != "" {
		return user.Owner + "/" + user.Name
	}

	// Source 2/3: Bearer token.
	token := parseBearerToken(ctx)
	if token == "" {
		return ""
	}

	// Provider keys (sk-), publishable keys (pk-), and widget keys (hz_)
	// don't map to IAM users with Commerce balances — skip.
	if strings.HasPrefix(token, "sk-") || strings.HasPrefix(token, "pk-") || strings.HasPrefix(token, "hz_") {
		return ""
	}

	// Exempt service account keys (e.g. cloud agent internal keys).
	if _, exempt := balanceExemptKeys[token]; exempt {
		return ""
	}

	// Check user key cache first.
	if cached := balanceGate.getUserKeyCached(token); cached != "" {
		return cached
	}

	// JWT token: parse locally (cheap, no network).
	if isJwtTokenLike(token) {
		claims, err := iam.ParseJwtToken(token)
		if err != nil {
			return ""
		}
		userKey := claims.User.Owner + "/" + claims.User.Name
		if claims.User.Owner != "" && claims.User.Name != "" {
			balanceGate.setUserKeyCache(token, userKey)
			return userKey
		}
		return ""
	}

	// IAM API key (hk- prefix): resolve via IAM (cached).
	if strings.HasPrefix(token, "hk-") {
		userKey := balanceGate.resolveIAMKeyUser(token)
		if userKey != "" {
			balanceGate.setUserKeyCache(token, userKey)
		}
		return userKey
	}

	return ""
}

// isJwtTokenLike checks if a token looks like a JWT (3 dot-separated segments).
func isJwtTokenLike(token string) bool {
	parts := strings.Split(token, ".")
	return len(parts) == 3 && len(parts[0]) > 10 && len(parts[1]) > 10
}

// ── Balance checking ────────────────────────────────────────────────────────

// checkBalance reports whether the user may proceed. On a cache hit within TTL
// it returns the cached result immediately; within the bounded staleness grace
// (balanceCacheTTL < age <= balanceCacheTTL+balanceStaleGrace) it serves the
// stale result while refreshing asynchronously; beyond the grace it refreshes
// but treats the value as unknown (it is too old to authorize spend); on cache
// miss it fetches synchronously (with timeout) then caches.
//
// Fail-closed by default: when the balance is unknown — Commerce unreachable on
// a cache miss, or a cache entry stale beyond the grace — the request is DENIED
// (allowed=false, unknown=true), mapped by the caller to HTTP 503, matching the
// commerce metering client and the gateway prepaid gate. This bounds the
// free-spend window for a now-zero user during a Commerce outage to at most
// balanceStaleGrace. Set BILLING_FAIL_OPEN=true to invert unknown to allow
// (revenue leak) where availability deliberately outranks billing. The
// controller-level check in getUserBalance remains as defense-in-depth.
func (bg *BalanceGate) checkBalance(userKey string) (allowed bool, balanceCents int64, unknown bool) {
	bg.mu.RLock()
	entry, ok := bg.entries[userKey]
	bg.mu.RUnlock()

	if ok {
		age := time.Since(entry.fetchedAt)
		if age <= balanceCacheTTL {
			// Fresh cache hit.
			return entry.balanceCents > 0, entry.balanceCents, false
		}
		// Past TTL: always kick a background refresh.
		bg.refreshAsync(userKey)
		if age <= balanceCacheTTL+balanceStaleGrace {
			// Within the bounded grace: serve the stale result while revalidating
			// (graceful degradation). A prior successful fetch is authoritative
			// enough for this short window.
			return entry.balanceCents > 0, entry.balanceCents, false
		}
		// Beyond the grace: the stale value is too old to authorize spend. Do NOT
		// serve a stale positive — treat as unknown so a now-zero user cannot keep
		// spending free during a Commerce outage. Deny (503) unless fail-open.
		if util.BillingFailOpen() {
			logs.Warning("balance_gate: stale balance beyond grace for user=%s (age=%v, BILLING_FAIL_OPEN -> allow)", userKey, age)
			return true, entry.balanceCents, false
		}
		logs.Warning("balance_gate: stale balance beyond grace for user=%s (age=%v, fail-closed -> deny)", userKey, age)
		return false, 0, true
	}

	// Cache miss: fetch synchronously so the first request gets a real check.
	// The timeout is capped at balanceHTTPTimeout (5s) to avoid blocking too long.
	balance, err := bg.fetch(userKey)
	if err != nil {
		if util.BillingFailOpen() {
			logs.Warning("balance_gate: Commerce lookup failed for user=%s: %v (BILLING_FAIL_OPEN -> allow)", userKey, err)
			return true, 0, false
		}
		logs.Warning("balance_gate: Commerce lookup failed for user=%s: %v (fail-closed -> deny)", userKey, err)
		return false, 0, true
	}

	bg.mu.Lock()
	bg.entries[userKey] = &balanceCacheEntry{balanceCents: balance, fetchedAt: time.Now()}
	bg.mu.Unlock()

	return balance > 0, balance, false
}

// refreshAsync kicks off a background goroutine to refresh the cached balance
// for a user. Deduplicates concurrent refreshes for the same user key.
func (bg *BalanceGate) refreshAsync(userKey string) {
	bg.inflightMu.Lock()
	if _, running := bg.inflight[userKey]; running {
		bg.inflightMu.Unlock()
		return
	}
	bg.inflight[userKey] = struct{}{}
	bg.inflightMu.Unlock()

	go func() {
		defer func() {
			bg.inflightMu.Lock()
			delete(bg.inflight, userKey)
			bg.inflightMu.Unlock()
		}()

		balance, err := bg.fetch(userKey)
		if err != nil {
			logs.Warning("balance_gate: async refresh failed for user=%s: %v", userKey, err)
			return
		}

		bg.mu.Lock()
		bg.entries[userKey] = &balanceCacheEntry{balanceCents: balance, fetchedAt: time.Now()}
		bg.mu.Unlock()
	}()
}

// commerceBalanceResponse is the expected JSON shape from Commerce balance endpoint.
type commerceBalanceResponse struct {
	Available int64 `json:"available"`
}

// fetchBalance calls Commerce to get the current balance for a user.
// Per global rule: /v1/ only, never /api/. Commerce serves /v1/billing/balance.
func (bg *BalanceGate) fetchBalance(userKey string) (int64, error) {
	balanceURL := fmt.Sprintf("%s/v1/billing/balance?user=%s&currency=usd", bg.endpoint, url.QueryEscape(userKey))

	req, err := http.NewRequest(http.MethodGet, balanceURL, nil)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	util.SetCommerceAuthHeaders(req, bg.token, userKey)

	resp, err := bg.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("http: %w", err)
	}
	defer func() {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	// Commerce signals "no funds" with 402. Treat it as a definite zero balance
	// (available=0 -> caller returns 402 insufficient_balance), NOT a transport
	// error. 503 stays reserved for transport failures and 5xx (unknown balance).
	if resp.StatusCode == http.StatusPaymentRequired {
		return 0, nil
	}

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

// getUserKeyCached returns the cached userKey for a token, or "" on miss/stale.
func (bg *BalanceGate) getUserKeyCached(token string) string {
	bg.userKeyMu.RLock()
	entry, ok := bg.userKeyCache[token]
	bg.userKeyMu.RUnlock()

	if !ok || time.Since(entry.fetchedAt) > userKeyCacheTTL {
		return ""
	}
	return entry.userKey
}

// setUserKeyCache stores a token -> userKey mapping in the cache.
func (bg *BalanceGate) setUserKeyCache(token, userKey string) {
	bg.userKeyMu.Lock()
	bg.userKeyCache[token] = &userKeyCacheEntry{userKey: userKey, fetchedAt: time.Now()}
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

// resolveIAMKeyUser calls IAM to resolve an hk- API key to an "owner/name"
// user key. Returns "" on any error (fail-open).
func (bg *BalanceGate) resolveIAMKeyUser(apiKey string) string {
	if bg.iamEndpoint == "" {
		return ""
	}

	iamURL := fmt.Sprintf("%s/v1/iam/get-user?accessKey=%s", bg.iamEndpoint, url.QueryEscape(apiKey))
	if bg.clientId != "" && bg.clientSecret != "" {
		iamURL += "&clientId=" + url.QueryEscape(bg.clientId) + "&clientSecret=" + url.QueryEscape(bg.clientSecret)
	}

	req, err := http.NewRequest(http.MethodGet, iamURL, nil)
	if err != nil {
		logs.Warning("balance_gate: IAM request build failed for key=%s: %v", maskKey(apiKey), err)
		return ""
	}

	resp, err := bg.client.Do(req)
	if err != nil {
		logs.Warning("balance_gate: IAM request failed for key=%s: %v", maskKey(apiKey), err)
		return ""
	}
	defer func() {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		logs.Warning("balance_gate: IAM returned %d for key=%s", resp.StatusCode, maskKey(apiKey))
		return ""
	}

	var result iamUserResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		logs.Warning("balance_gate: IAM response decode failed for key=%s: %v", maskKey(apiKey), err)
		return ""
	}

	if result.Status != "ok" || result.Data == nil {
		return ""
	}

	if result.Data.Owner == "" || result.Data.Name == "" {
		return ""
	}

	return result.Data.Owner + "/" + result.Data.Name
}

// ── Cleanup ─────────────────────────────────────────────────────────────────

// cleanupLoop periodically evicts stale entries from both caches.
func (bg *BalanceGate) cleanupLoop() {
	ticker := time.NewTicker(balanceCacheCleanupInterval)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()

		bg.mu.Lock()
		for key, entry := range bg.entries {
			if now.Sub(entry.fetchedAt) > 2*balanceCacheTTL {
				delete(bg.entries, key)
			}
		}
		bg.mu.Unlock()

		bg.userKeyMu.Lock()
		for key, entry := range bg.userKeyCache {
			if now.Sub(entry.fetchedAt) > 2*userKeyCacheTTL {
				delete(bg.userKeyCache, key)
			}
		}
		bg.userKeyMu.Unlock()
	}
}
