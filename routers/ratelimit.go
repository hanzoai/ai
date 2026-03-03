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
	"fmt"
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
type Tier string

const (
	TierFree       Tier = "free"
	TierStarter    Tier = "starter"
	TierPro        Tier = "pro"
	TierEnterprise Tier = "enterprise"
)

// tierLimits maps each tier to its per-minute request allowance.
var tierLimits = map[Tier]int{
	TierFree:       10,
	TierStarter:    60,
	TierPro:        300,
	TierEnterprise: 1000,
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
// API key to its Tier; pass nil to always use TierFree.
func NewRateLimiter(tierFunc func(string) Tier, cleanupInterval time.Duration) *RateLimiter {
	if tierFunc == nil {
		tierFunc = func(string) Tier { return TierFree }
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
		reqPerMin = tierLimits[TierFree]
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
	case path == "/api/models":
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

// DefaultTierFunc resolves an API key to a Tier. It checks the RATE_LIMIT_TIERS
// environment variable (or app.conf key) for a comma-separated mapping of
// "key_prefix=tier" entries. Unmatched keys default to TierFree.
//
// Example env: RATE_LIMIT_TIERS=hk-0d2eb=enterprise,hk-feb5b=pro
//
// Production systems should replace this with a database or IAM lookup;
// this env-based approach provides a working baseline without external deps.
func DefaultTierFunc(apiKey string) Tier {
	tierMap := parseTierConfig()
	if tierMap == nil {
		return TierFree
	}

	// Exact match first.
	if t, ok := tierMap[apiKey]; ok {
		return t
	}

	// Prefix match: allows configuring "hk-0d2eb=enterprise" to match
	// the full key "hk-0d2eb9cfafd049389f2904cad770a9d8".
	for prefix, t := range tierMap {
		if strings.HasPrefix(apiKey, prefix) {
			return t
		}
	}

	return TierFree
}

// parseTierConfig reads RATE_LIMIT_TIERS from env (or Beego app.conf).
// Format: "prefix1=tier1,prefix2=tier2"
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
		tierStr := Tier(strings.TrimSpace(parts[1]))

		// Validate tier name.
		if _, ok := tierLimits[tierStr]; !ok {
			logs.Warn("rate_limit: unknown tier %q for key prefix %q, skipping", tierStr, key)
			continue
		}
		result[key] = tierStr
	}
	return result
}
