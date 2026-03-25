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
	"math"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/beego/beego/context"
	"github.com/beego/beego/logs"
	"github.com/hanzoai/cloud/conf"
	"golang.org/x/time/rate"
)

// Tier represents an API usage tier with associated rate limits.
// All tiers follow the "zen-*" naming convention as the canonical identifier.
type Tier string

const (
	TierZenFree       Tier = "zen-free"
	TierZenPro        Tier = "zen-pro"
	TierZenTeam       Tier = "zen-team"
	TierZenEnterprise Tier = "zen-enterprise"
	TierZenCustom     Tier = "zen-custom"
)

// tierLimits maps each tier to its per-minute request allowance.
var tierLimits = map[Tier]int{
	TierZenFree:       60,
	TierZenPro:        500,
	TierZenTeam:       2000,
	TierZenEnterprise: 50000,
	TierZenCustom:     100000,
}

// keyEntry holds the rate limiter and last-seen time for a single API key.
type keyEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
	tier     Tier
}

// RateLimiter tracks per-key rate limiters with automatic cleanup of stale entries.
type RateLimiter struct {
	mu       sync.RWMutex
	keys     map[string]*keyEntry
	tierFunc func(apiKey string) Tier
	stopCh   chan struct{}

	// Metrics counters — accessed atomically.
	totalAllowed uint64
	totalDenied  uint64
}

// NewRateLimiter creates a RateLimiter that starts a background goroutine to
// evict stale entries every cleanupInterval. The tierFunc callback resolves an
// API key to its Tier; pass nil to always use TierZenFree.
func NewRateLimiter(tierFunc func(string) Tier, cleanupInterval time.Duration) *RateLimiter {
	if tierFunc == nil {
		tierFunc = func(string) Tier { return TierZenFree }
	}

	rl := &RateLimiter{
		keys:     make(map[string]*keyEntry),
		tierFunc: tierFunc,
		stopCh:   make(chan struct{}),
	}

	go rl.cleanup(cleanupInterval)
	return rl
}

// Allow checks whether a request from the given API key should be permitted.
// It returns true if the request is within the rate limit.
func (rl *RateLimiter) Allow(apiKey string) bool {
	entry := rl.getOrCreate(apiKey)

	rl.mu.Lock()
	entry.lastSeen = time.Now()
	rl.mu.Unlock()

	if entry.limiter.Allow() {
		atomic.AddUint64(&rl.totalAllowed, 1)
		return true
	}

	atomic.AddUint64(&rl.totalDenied, 1)
	return false
}

// RetryAfter returns the number of seconds until the next token is available
// for the given API key. Returns 0 if the key has no entry.
func (rl *RateLimiter) RetryAfter(apiKey string) int {
	rl.mu.RLock()
	entry, ok := rl.keys[apiKey]
	rl.mu.RUnlock()

	if !ok {
		return 1
	}

	reservation := entry.limiter.Reserve()
	delay := reservation.Delay()
	reservation.Cancel()

	seconds := int(math.Ceil(delay.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	return seconds
}

// Metrics returns the current rate limit hit/pass counters.
func (rl *RateLimiter) Metrics() (allowed, denied uint64) {
	return atomic.LoadUint64(&rl.totalAllowed), atomic.LoadUint64(&rl.totalDenied)
}

// Stop terminates the background cleanup goroutine.
func (rl *RateLimiter) Stop() {
	close(rl.stopCh)
}

// getOrCreate returns an existing entry or creates a new one for the given key.
func (rl *RateLimiter) getOrCreate(apiKey string) *keyEntry {
	rl.mu.RLock()
	entry, ok := rl.keys[apiKey]
	rl.mu.RUnlock()

	if ok {
		return entry
	}

	tier := rl.tierFunc(apiKey)
	reqPerMin := tierLimits[tier]
	if reqPerMin == 0 {
		reqPerMin = tierLimits[TierZenFree]
	}

	// rate.Limit is events per second; burst allows short spikes up to 20%
	// of the per-minute allowance (minimum burst of 1).
	perSecond := rate.Limit(float64(reqPerMin) / 60.0)
	burst := reqPerMin / 5
	if burst < 1 {
		burst = 1
	}

	entry = &keyEntry{
		limiter:  rate.NewLimiter(perSecond, burst),
		lastSeen: time.Now(),
		tier:     tier,
	}

	rl.mu.Lock()
	// Double-check: another goroutine may have inserted while we upgraded the lock.
	if existing, ok := rl.keys[apiKey]; ok {
		rl.mu.Unlock()
		return existing
	}
	rl.keys[apiKey] = entry
	rl.mu.Unlock()

	return entry
}

// cleanup periodically evicts entries not seen for staleThreshold (10 minutes).
func (rl *RateLimiter) cleanup(interval time.Duration) {
	const staleThreshold = 10 * time.Minute
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-rl.stopCh:
			return
		case now := <-ticker.C:
			rl.mu.Lock()
			for key, entry := range rl.keys {
				if now.Sub(entry.lastSeen) > staleThreshold {
					delete(rl.keys, key)
				}
			}
			rl.mu.Unlock()
		}
	}
}

// ── Beego filter ────────────────────────────────────────────────────────────

// rateLimiterInstance is the singleton initialized by InitRateLimiter.
var rateLimiterInstance *RateLimiter

// InitRateLimiter creates the global rate limiter. Must be called once during
// startup (before beego.Run). Returns the instance so the caller can call
// Stop() on shutdown.
func InitRateLimiter(tierFunc func(string) Tier) *RateLimiter {
	rateLimiterInstance = NewRateLimiter(tierFunc, 10*time.Minute)
	return rateLimiterInstance
}

// RateLimitFilter is a Beego BeforeRouter filter that enforces per-key rate
// limits on API endpoints. It extracts the API key from the Authorization
// header (Bearer token) or X-API-Key header.
//
// Rate-limited paths: /api/chat/completions, /api/messages, /v1/messages,
// and other API endpoints that carry a bearer token.
// Excluded: health, metrics, models (read-only), static, UI routes.
func RateLimitFilter(ctx *context.Context) {
	if rateLimiterInstance == nil {
		return
	}

	path := ctx.Request.URL.Path

	// Skip paths that should never be rate-limited.
	if isRateLimitExempt(path) {
		return
	}

	// Only rate-limit API routes.
	if !strings.HasPrefix(path, "/api/") && !strings.HasPrefix(path, "/v1/") {
		return
	}

	apiKey := extractAPIKey(ctx)
	if apiKey == "" {
		// No key means unauthenticated — other filters will handle auth errors.
		return
	}

	if rateLimiterInstance.Allow(apiKey) {
		return
	}

	// Rate limit exceeded — log and respond with 429.
	retryAfter := rateLimiterInstance.RetryAfter(apiKey)
	allowed, denied := rateLimiterInstance.Metrics()

	// Mask the key for structured logging (show first 6 chars).
	maskedKey := apiKey
	if len(maskedKey) > 6 {
		maskedKey = maskedKey[:6] + "..."
	}

	logs.Info("rate_limit_exceeded key=%s path=%s retry_after=%d total_allowed=%d total_denied=%d",
		maskedKey, path, retryAfter, allowed, denied)

	ctx.ResponseWriter.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
	ctx.ResponseWriter.Header().Set("X-RateLimit-Remaining", "0")
	ctx.ResponseWriter.Header().Set("Content-Type", "application/json")
	ctx.ResponseWriter.WriteHeader(http.StatusTooManyRequests)

	body := fmt.Sprintf(
		`{"error":{"message":"Rate limit exceeded. Retry after %d seconds.","type":"rate_limit_error","code":429}}`,
		retryAfter,
	)
	ctx.ResponseWriter.Write([]byte(body))
}

// isRateLimitExempt returns true for paths that should bypass rate limiting.
func isRateLimitExempt(path string) bool {
	switch {
	case path == "/api/health" || path == "/health":
		return true
	case path == "/api/metrics" || path == "/metrics":
		return true
	case strings.HasPrefix(path, "/api/get-version-info"):
		return true
	case strings.HasPrefix(path, "/api/get-system-info"):
		return true
	default:
		return false
	}
}

// extractAPIKey pulls the API key from the request. Supports:
//   - Authorization: Bearer <token>
//   - X-API-Key: <token>
//   - api_key query parameter
func extractAPIKey(ctx *context.Context) string {
	// Bearer token
	authHeader := ctx.Request.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		return strings.TrimPrefix(authHeader, "Bearer ")
	}

	// X-API-Key header
	if key := ctx.Request.Header.Get("X-API-Key"); key != "" {
		return key
	}

	// Query parameter fallback
	if key := ctx.Input.Query("api_key"); key != "" {
		return key
	}

	return ""
}

// ── Tier resolution ─────────────────────────────────────────────────────────

// DefaultTierFunc resolves an API key to a Tier using a three-level lookup:
//
//  1. Static env-var overrides (RATE_LIMIT_TIERS) -- highest priority, for
//     operator-managed key-to-tier mappings. Supports exact and prefix matching.
//  2. Commerce tier cache -- backed by async lookups to Commerce billing API.
//     On cache hit, the cached tier is returned immediately. On cache miss,
//     TierZenFree is returned and a background goroutine populates the cache
//     so the next request for this key uses the correct tier.
//  3. TierZenFree -- default when no override or cache entry exists.
//
// This function never blocks on network I/O. Commerce lookups happen
// asynchronously; the worst case is that a new key's first few requests
// are rate-limited at the free tier until the cache is populated.
func DefaultTierFunc(apiKey string) Tier {
	// Level 1: static env-var overrides (highest priority).
	tierMap := parseTierConfig()
	if tierMap != nil {
		// Exact match first.
		if t, ok := tierMap[apiKey]; ok {
			return t
		}
		// Prefix match: "hk-0d2eb=zen-enterprise" matches "hk-0d2eb9cfafd0...".
		for prefix, t := range tierMap {
			if strings.HasPrefix(apiKey, prefix) {
				return t
			}
		}
	}

	// Level 2: Commerce-backed tier cache.
	if tierCache != nil {
		if tier, ok := tierCache.get(apiKey); ok {
			return tier
		}
		// Cache miss: return TierZenFree now, populate cache asynchronously.
		tierCache.refreshAsync(apiKey)
	}

	// Level 3: default.
	return TierZenFree
}

// parseTierConfig reads RATE_LIMIT_TIERS from env (or Beego app.conf).
// Format: "prefix1=tier1,prefix2=tier2"
// Accepts both canonical zen-* names and legacy names (mapped automatically).
func parseTierConfig() map[string]Tier {
	raw := strings.TrimSpace(conf.GetConfigString("RATE_LIMIT_TIERS"))
	if raw == "" {
		return nil
	}

	result := make(map[string]Tier)
	for _, entry := range strings.Split(raw, ",") {
		parts := strings.SplitN(strings.TrimSpace(entry), "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		rawTier := strings.TrimSpace(parts[1])

		// Map legacy tier names to canonical zen-* names, then validate.
		tier := mapPlanToTier(rawTier)
		result[key] = tier
	}
	return result
}

// ── Commerce-backed tier cache ──────────────────────────────────────────────

const (
	// tierCacheTTL is how long a Commerce tier lookup remains valid.
	tierCacheTTL = 5 * time.Minute

	// tierCacheCleanupInterval is how often stale cache entries are evicted.
	tierCacheCleanupInterval = 10 * time.Minute

	// commerceHTTPTimeout is the per-request timeout for Commerce tier lookups.
	commerceHTTPTimeout = 5 * time.Second
)

// tierCacheEntry holds a cached tier mapping for a single API key.
type tierCacheEntry struct {
	tier      Tier
	fetchedAt time.Time
}

// TierCache caches apiKey-to-tier mappings resolved from Commerce. Stale
// entries (older than tierCacheTTL) are lazily ignored on read and periodically
// evicted by a background goroutine.
type TierCache struct {
	mu          sync.RWMutex
	entries     map[string]*tierCacheEntry
	lastCleanup time.Time

	endpoint string       // Commerce base URL (e.g. "http://commerce:8001")
	token    string       // Bearer token for Commerce API
	client   *http.Client // shared HTTP client for tier lookups

	// inflight tracks keys currently being fetched to avoid duplicate goroutines.
	inflightMu sync.Mutex
	inflight   map[string]struct{}
}

// tierCache is the package-level singleton, initialized by InitTierCache.
var tierCache *TierCache

// InitTierCache reads Commerce connection parameters from app config and
// creates the tier cache. Must be called once during startup. If Commerce
// is not configured (no commerceEndpoint), the cache is not created and
// DefaultTierFunc falls back to env-var overrides or TierZenFree.
func InitTierCache() {
	endpoint := conf.GetConfigString("commerceEndpoint")
	if endpoint == "" {
		logs.Info("tier_cache: commerceEndpoint not configured, Commerce tier lookup disabled")
		return
	}
	endpoint = strings.TrimRight(endpoint, "/")
	token := conf.GetConfigString("commerceToken")

	tc := &TierCache{
		entries:     make(map[string]*tierCacheEntry),
		lastCleanup: time.Now(),
		endpoint:    endpoint,
		token:       token,
		client:      &http.Client{Timeout: commerceHTTPTimeout},
		inflight:    make(map[string]struct{}),
	}

	go tc.cleanupLoop()

	tierCache = tc
	logs.Info("tier_cache: initialized (endpoint=%s, ttl=%v)", endpoint, tierCacheTTL)
}

// get returns a cached tier for the given key, or ("", false) on cache miss
// or stale entry.
func (tc *TierCache) get(apiKey string) (Tier, bool) {
	tc.mu.RLock()
	entry, ok := tc.entries[apiKey]
	tc.mu.RUnlock()

	if !ok {
		return "", false
	}
	if time.Since(entry.fetchedAt) > tierCacheTTL {
		return "", false
	}
	return entry.tier, true
}

// set stores a tier mapping in the cache.
func (tc *TierCache) set(apiKey string, tier Tier) {
	tc.mu.Lock()
	tc.entries[apiKey] = &tierCacheEntry{
		tier:      tier,
		fetchedAt: time.Now(),
	}
	tc.mu.Unlock()
}

// refreshAsync kicks off a background goroutine to fetch the tier from Commerce
// and populate the cache. If a fetch for the same key is already in flight,
// this is a no-op. This ensures rate limiting never blocks on Commerce latency.
func (tc *TierCache) refreshAsync(apiKey string) {
	tc.inflightMu.Lock()
	if _, running := tc.inflight[apiKey]; running {
		tc.inflightMu.Unlock()
		return
	}
	tc.inflight[apiKey] = struct{}{}
	tc.inflightMu.Unlock()

	go func() {
		defer func() {
			tc.inflightMu.Lock()
			delete(tc.inflight, apiKey)
			tc.inflightMu.Unlock()
		}()

		tier, err := tc.commerceTierLookup(apiKey)
		if err != nil {
			logs.Warning("tier_cache: Commerce lookup failed for key=%s: %v (defaulting to zen-free)", maskKey(apiKey), err)
			tier = TierZenFree
		}
		tc.set(apiKey, tier)
	}()
}

// cleanupLoop periodically removes stale entries from the cache.
func (tc *TierCache) cleanupLoop() {
	ticker := time.NewTicker(tierCacheCleanupInterval)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()
		tc.mu.Lock()
		for key, entry := range tc.entries {
			if now.Sub(entry.fetchedAt) > tierCacheTTL {
				delete(tc.entries, key)
			}
		}
		tc.lastCleanup = now
		tc.mu.Unlock()
	}
}

// commercePlanResponse is the expected JSON shape from the Commerce tier endpoint.
type commercePlanResponse struct {
	Plan string `json:"plan"`
}

// commerceTierLookup calls Commerce to resolve the billing plan for an API key
// and maps the plan name to a rate limit Tier. Returns TierZenFree on any error
// (fail-open: rate limiting should never deny service because Commerce is down).
func (tc *TierCache) commerceTierLookup(apiKey string) (Tier, error) {
	url := fmt.Sprintf("%s/api/v1/billing/tier?apiKey=%s", tc.endpoint, apiKey)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return TierZenFree, fmt.Errorf("build request: %w", err)
	}
	if tc.token != "" {
		req.Header.Set("Authorization", "Bearer "+tc.token)
	}

	resp, err := tc.client.Do(req)
	if err != nil {
		return TierZenFree, fmt.Errorf("http: %w", err)
	}
	defer func() {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return TierZenFree, fmt.Errorf("commerce returned %d", resp.StatusCode)
	}

	var planResp commercePlanResponse
	if err := json.NewDecoder(resp.Body).Decode(&planResp); err != nil {
		return TierZenFree, fmt.Errorf("decode response: %w", err)
	}

	return mapPlanToTier(planResp.Plan), nil
}

// mapPlanToTier converts a Commerce plan name or legacy tier name to a
// canonical zen-* rate limit Tier. Supports both old and new naming conventions.
func mapPlanToTier(plan string) Tier {
	switch strings.ToLower(strings.TrimSpace(plan)) {
	case "zen-free", "free", "developer":
		return TierZenFree
	case "zen-pro", "pro", "starter":
		return TierZenPro
	case "zen-team", "team":
		return TierZenTeam
	case "zen-enterprise", "enterprise", "scale":
		return TierZenEnterprise
	case "zen-custom", "custom":
		return TierZenCustom
	default:
		return TierZenFree
	}
}

// maskKey returns an API key truncated to its first 6 characters for safe logging.
func maskKey(apiKey string) string {
	if len(apiKey) > 6 {
		return apiKey[:6] + "..."
	}
	return apiKey
}
