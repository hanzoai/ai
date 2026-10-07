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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hanzoai/ai/address"
	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/controllers"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
	"github.com/zap-proto/zip"
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
	totalAllowed atomic.Uint64
	totalDenied  atomic.Uint64
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
	return rl.Admit(one(apiKey))
}

// Admit charges one request to the lanes it is counted on, narrowest first, each at
// its tier's rate times the lane's scale, and reports whether every lane had room.
//
// A WIDER LANE IS SPENT ONLY BY WHAT A NARROWER ONE ADMITTED. The walk stops at the
// first lane that refuses, so one /64 hammering past its own ceiling spends nothing
// of the /48 its neighbours share: a site is emptied by sixteen callers' worth of
// admitted traffic, never by one caller's refusals.
//
// One request in Metrics, however many lanes it is counted on.
//
// On a host with shared counts (object.SetCounters) every lane is a bucket every
// replica spends from, so a restart or a second replica hands nobody a fresh rate.
func (rl *RateLimiter) Admit(lanes []address.Bucket) bool {
	if _, take := object.Counts(); take != nil {
		ok := true
		for _, l := range lanes {
			if !take(context.Background(), "rate:"+l.Key, rl.perMin(l.Key, l.Scale)) {
				ok = false
				break
			}
		}
		if ok {
			rl.totalAllowed.Add(1)
		} else {
			rl.totalDenied.Add(1)
		}
		return ok
	}
	ok := true
	for _, l := range lanes {
		entry := rl.getOrCreate(l.Key, l.Scale)
		rl.mu.Lock()
		entry.lastSeen = time.Now()
		rl.mu.Unlock()
		if !entry.limiter.Allow() {
			ok = false
			break
		}
	}
	if ok {
		rl.totalAllowed.Add(1)
	} else {
		rl.totalDenied.Add(1)
	}
	return ok
}

// one is a caller counted on one lane: their own name.
func one(key string) []address.Bucket { return []address.Bucket{{Key: key, Scale: 1}} }

// Open reports whether a lane still has room, spending nothing.
//
// It is what a caller this process cannot yet NAME has to clear before the round
// trip that names them. Allow would spend a token on the address lane and then the
// named request would spend a second one from its own; Open asks the same question
// and leaves the accounting to whichever lane ends up carrying the request.
func (rl *RateLimiter) Open(apiKey string) bool {
	if _, take := object.Counts(); take != nil {
		return true // the shared bucket is spent at Admit; asking it would spend a token
	}
	rl.mu.RLock()
	entry, ok := rl.keys[apiKey]
	rl.mu.RUnlock()

	// A lane nobody has spent from is open, and asking must not bring it into
	// being: a question about a bucket is not a request against it.
	if !ok {
		return true
	}
	return entry.limiter.Tokens() >= 1
}

// RetryAfter returns the number of seconds until every lane has a token again: the
// longest wait among them.
func (rl *RateLimiter) RetryAfter(lanes []address.Bucket) int {
	wait := 1
	for _, l := range lanes {
		if _, take := object.Counts(); take != nil {
			wait = max(wait, int(math.Ceil(60/float64(rl.perMin(l.Key, l.Scale)))))
			continue
		}
		wait = max(wait, rl.retryAfter(l.Key))
	}
	return wait
}

// perMin is a lane's rate: its tier's requests a minute times the lane's scale.
func (rl *RateLimiter) perMin(key string, scale int) int {
	n := tierLimits[rl.tierFunc(key)]
	if n == 0 {
		n = tierLimits[TierZenFree]
	}
	return n * max(scale, 1)
}

// retryAfter returns the number of seconds until the next token is available for
// one key, and 1 when the key has no entry.
//
// READ OFF THE TOKENS, never reserved. A reservation on a lane that has a token takes
// it, and cancelling one already due gives nothing back — so asking every lane how
// long to wait spent the open ones, and each refusal emptied a /48 by one.
func (rl *RateLimiter) retryAfter(apiKey string) int {
	rl.mu.RLock()
	entry, ok := rl.keys[apiKey]
	rl.mu.RUnlock()

	if !ok {
		return 1
	}
	need := 1 - entry.limiter.Tokens()
	if need <= 0 || entry.limiter.Limit() <= 0 {
		return 1
	}
	return max(int(math.Ceil(need/float64(entry.limiter.Limit()))), 1)
}

// Metrics returns the current rate limit hit/pass counters.
func (rl *RateLimiter) Metrics() (allowed, denied uint64) {
	return rl.totalAllowed.Load(), rl.totalDenied.Load()
}

// Stop terminates the background cleanup goroutine.
func (rl *RateLimiter) Stop() {
	close(rl.stopCh)
}

// getOrCreate returns an existing entry or creates a new one for the given key, at
// scale times its tier's rate.
func (rl *RateLimiter) getOrCreate(apiKey string, scale int) *keyEntry {
	rl.mu.RLock()
	entry, ok := rl.keys[apiKey]
	rl.mu.RUnlock()

	// A FREE ENTRY ASKS AGAIN. Free is the one tier a payment changes, and the
	// lookup behind tierFunc never holds it, so an org that has just paid is
	// rated as what it bought as soon as the lookup has its answer — not after
	// ten idle minutes, which an org that keeps sending never reaches. A paid
	// entry is kept: its tier does not move under it.
	tier := TierZenFree
	if ok {
		if entry.tier != TierZenFree {
			return entry
		}
		if tier = rl.tierFunc(apiKey); tier == TierZenFree || tierLimits[tier] == 0 {
			return entry
		}
	} else {
		tier = rl.tierFunc(apiKey)
	}
	reqPerMin := tierLimits[tier]
	if reqPerMin == 0 {
		reqPerMin = tierLimits[TierZenFree]
	}
	reqPerMin *= max(scale, 1)

	// rate.Limit is events per second; burst allows short spikes up to 20%
	// of the per-minute allowance (minimum burst of 1).
	perSecond := rate.Limit(float64(reqPerMin) / 60.0)
	burst := max(reqPerMin/5, 1)

	entry = &keyEntry{
		limiter:  rate.NewLimiter(perSecond, burst),
		lastSeen: time.Now(),
		tier:     tier,
	}

	rl.mu.Lock()
	// Double-check: another goroutine may have inserted while we upgraded the lock.
	// A free entry it finds is the one being replaced, so only a paid one wins.
	if existing, ok := rl.keys[apiKey]; ok && existing.tier != TierZenFree {
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

// ── the router filter ────────────────────────────────────────────────────────────

// rateLimiterInstance is the singleton initialized by InitRateLimiter.
var rateLimiterInstance *RateLimiter

// InitRateLimiter creates the global rate limiter. Must be called once during
// startup (before web.Run). Returns the instance so the caller can call
// Stop() on shutdown.
//
// It says what it armed, and says it by READING tierLimits. Boot used to print the
// tiers as prose from where it called this, and prose does not follow a table: the
// line named a free tier of 10/min next to two tiers that no longer exist, and a
// reader took it for the configuration. A boot line is evidence of what was armed,
// so it comes from the thing that armed it.
func InitRateLimiter(tierFunc func(string) Tier) *RateLimiter {
	rateLimiterInstance = NewRateLimiter(tierFunc, 10*time.Minute)
	// The quota is armed from the same call, on the same tierFunc, so a caller's
	// rate and their ceiling can never be resolved from two different tiers.
	quotaInstance = NewQuota(tierFunc, time.Hour)
	log.Info("Per-key rate limiter initialized (tiers: %s)", tierRates())
	return rateLimiterInstance
}

// tierRates renders the tier table cheapest-first, for the boot line.
func tierRates() string {
	tiers := make([]Tier, 0, len(tierLimits))
	for t := range tierLimits {
		tiers = append(tiers, t)
	}
	sort.Slice(tiers, func(i, j int) bool { return tierLimits[tiers[i]] < tierLimits[tiers[j]] })
	rates := make([]string, 0, len(tiers))
	for _, t := range tiers {
		rates = append(rates, fmt.Sprintf("%s=%d/min", t, tierLimits[t]))
	}
	return strings.Join(rates, ", ")
}

// continues answers whether a request continues a session counted at its open
// (controllers.TranscriptModel names one), indirected so the filter's tests state
// the sessions directly.
var continues = func(method, path, auth string) bool {
	_, ok := controllers.TranscriptModel(method, path, auth)
	return ok
}

// RateLimitFilter holds every /v1 request to a rate and a quota.
//
// EVERY request, because the ceilings are asked about the CALLER rather than about
// the credential they happened to carry — see limitSubject. The exceptions are the
// paths isRateLimitExempt names and anything outside /v1, which is not this API.
func RateLimitFilter(c *zip.Ctx) error {
	if rateLimiterInstance == nil {
		return c.Continue()
	}

	path := c.Path()

	// Skip paths that should never be rate-limited.
	if isRateLimitExempt(path) {
		return c.Continue()
	}

	// Only rate-limit API routes. Folded: the router matches case-blind, so a
	// lowercase-literal test reads /V1/ as "not an API route".
	if !strings.HasPrefix(strings.ToLower(path), "/v1/") {
		return c.Continue()
	}

	// A push or close on a live transcript the caller's own credential opened was
	// counted once, at its open — a push every chunk_ms would spend the free rate's
	// burst in three seconds and its 8h quota in a minute of speech. The session's
	// own bounds hold it instead: max_bytes a push, max_seconds of audio, the idle
	// timeout (controllers.TranscriptModel).
	if continues(c.Method(), path, c.Header("Authorization")) {
		return c.Continue()
	}

	lanes := limitSubject(c)
	limitKey := lanes[0].Key

	if rateLimiterInstance.Admit(lanes) {
		if catalogRead(c.Method(), path) {
			return c.Continue()
		}
		// Two ceilings, asked in the order a caller meets them. If the quota refuses,
		// it has already answered and this must not answer over the top of it.
		if proceed, err := charge(c, limitKey, path); !proceed {
			return err
		}
		return c.Continue()
	}

	// Rate limit exceeded — log and respond with 429.
	retryAfter := rateLimiterInstance.RetryAfter(lanes)
	allowed, denied := rateLimiterInstance.Metrics()

	log.Info("rate_limit_exceeded key=%s path=%s retry_after=%d total_allowed=%d total_denied=%d",
		maskKey(limitKey), path, retryAfter, allowed, denied)

	c.SetHeader("Retry-After", fmt.Sprintf("%d", retryAfter))
	c.SetHeader("X-RateLimit-Remaining", "0")
	c.SetHeader("Content-Type", "application/json")
	return c.Bytes(http.StatusTooManyRequests, []byte(fmt.Sprintf(
		`{"error":{"message":"Rate limit exceeded. Retry after %d seconds.","type":"rate_limit_error","code":429}}`,
		retryAfter,
	)))
}

// unaddressed is the one bucket for a caller with neither a name nor an address —
// a socket peer with nothing in front of it saying who reached us. There is no axis
// left to tell such callers apart, so they share a lane and it closes for all of
// them together. Sharing is the honest answer here; the alternative is a lane with
// no bottom.
const unaddressed = "visitor:unaddressed"

// limitSubject is who a request is counted against, and every request has one: the
// lanes it is counted on, narrowest first, the first of them being who it is.
//
// It used to be the credential, and only when the credential arrived on one of three
// transports — so a caller who authenticated any other way was counted against
// nothing at all. A cookie session is the plain case: it carries none of the three,
// and the balance gate resolves a real payer from it, so the console and the chat
// surface ran with no rate and no quota while the money side knew exactly who they
// were. A ceiling has to be asked about the CALLER, never about the shape of the
// string they happened to carry.
//
// Three answers, in the order of what a request PROVES — and the first is asked
// twice, because half of it is free and half of it costs an IAM round trip:
//
//  1. The billing subject — resolveBillingKey, the one place identity is derived.
//     Cookie session, JWT, IAM key: whoever pays for a call is who is throttled by
//     it, so rate, quota and money name one tenant. For a pooled org that is the org
//     slug and the org shares one bucket; for a personal-billing org it is
//     "owner/name", so individuals in the shared "hanzo" catch-all cannot exhaust
//     each other. A session, a token this process parses itself and a key it has
//     already resolved all answer for nothing (billingKey) and are asked FIRST; the
//     key it has not seen is asked LAST, behind the lane in (3).
//
//  2. A publishable key IAM confirms. It authenticates nobody, so there is no
//     subject to bill — but IAM ISSUED it, which is what a caller cannot do, and a
//     page needs its own ceiling: bucketed with its org, one griefed page spends the
//     whole org's rate and takes down that org's API traffic and every other page it
//     publishes. Two surfaces holding two keys fail independently, which is the whole
//     reason for issuing two. Who PAYS is unchanged — the org still does. Who is
//     THROTTLED is a different question, and this is the one place the two differ.
//     Asking IAM costs nothing new: the tenant resolver asks it about the same
//     key on the same request, through the same memory.
//
//  3. The address the caller arrived from. Nobody picks their own peer and nobody
//     mints a fresh one per request, which is what a bucket must be for a ceiling to
//     mean anything — a bucket the caller names is an empty allowance every time, and
//     an unbounded map behind it. It is the same address, through the same function,
//     that the public lane already counts an anonymous visitor by: the peer decides,
//     and a stated address is believed only from a peer of our own. Hashed there, so
//     no address reaches a log line.
//
// An unnamed caller is therefore never pooled with the deployment and never pooled
// with a customer: junk arriving beside a paying caller is a different address than
// the customer's name, and a paying caller whose key IAM does not own falls to their
// own address rather than into a crowd.
func limitSubject(c *zip.Ctx) []address.Bucket {
	if subject, _, _, _ := billingKey(c); subject != "" {
		return one(subject)
	}

	// Nothing this process already holds names the caller, so the answer costs an
	// IAM round trip — and a round trip is what admission BUYS, never what decides
	// it. The lane below is where such a caller is counted until they are named, so
	// a flood of keys nobody owns drains it and is then refused having asked IAM
	// nothing at all — and takes no place in the resolver's bounded memory, which
	// keys that name no one would otherwise fill.
	//
	// A caller who IS named pays this nothing: their answer was held, and they
	// returned above.
	//
	// ONLY THE NARROWEST LANE DECIDES THE ROUND TRIP. An IPv6 caller's /48 is shared
	// by every /64 inside it, so a neighbour can close it, and a paying key is never
	// refused for a neighbour's junk before IAM names it. The /64 is the caller's own:
	// junk keys from it drain it and are then refused unasked. Admit charges the wider
	// lanes only to a caller who stays anonymous.
	lanes := controllers.Lanes(c)
	if len(lanes) == 0 {
		lanes = one(unaddressed)
	}
	if !rateLimiterInstance.Open(lanes[0].Key) {
		return lanes
	}

	if subject, _, _ := resolveBillingKey(c); subject != "" {
		return one(subject)
	}
	if key := extractAPIKey(c); strings.HasPrefix(key, "pk-") {
		if _, err := controllers.PublishableOrg(key); err == nil {
			return one(key)
		}
	}
	return lanes
}

// isRateLimitExempt returns true for paths that should bypass rate limiting.
// catalogRead reports whether a request reads the model catalog. The rate holds it
// like any request, so it cannot be used to flood the API; the quota does not count
// it, because the quota is how much a caller may USE and listing what can be used is
// not using it. A client that lists the models before each call would otherwise spend
// its quota twice as fast as it called them.
func catalogRead(method, path string) bool {
	if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	p := strings.ToLower(strings.TrimRight(path, "/"))
	return p == "/v1/models" || strings.HasPrefix(p, "/v1/models/")
}

func isRateLimitExempt(path string) bool {
	switch {
	case path == "/v1/health" || path == "/health":
		return true
	case path == "/v1/metrics" || path == "/metrics":
		return true
	case strings.HasPrefix(path, "/v1/ai/version"):
		return true
	case strings.HasPrefix(path, "/v1/ai/system"):
		return true
	default:
		return false
	}
}

// extractAPIKey is the key a request presents, and the list below is the whole of
// what this estate accepts it on:
//   - Authorization: Bearer <token>
//   - X-API-Key: <token>  (the Anthropic wire protocol's x-api-key, /v1/messages)
//   - api_key query parameter
//
// One list, read by both the identity this API bills (resolveBillingKey) and the
// bucket it counts. A second list somewhere else is a transport where a credential
// means one thing to the money and another to the ceiling.
func extractAPIKey(c *zip.Ctx) string {
	// Bearer token
	authHeader := c.Header("Authorization")
	if after, ok := strings.CutPrefix(authHeader, "Bearer "); ok {
		return after
	}

	// X-API-Key header
	if key := c.Header("X-API-Key"); key != "" {
		return key
	}

	// Query parameter fallback
	if key := c.Query("api_key"); key != "" {
		return key
	}

	return ""
}

// ── Tier resolution ─────────────────────────────────────────────────────────

// DefaultTierFunc resolves a rate-limit key to a Tier. The key is whatever
// limitSubject named the caller: an IAM org slug when a billing subject resolved, a
// confirmed page key, or a digest of the address the caller arrived from when
// neither. The tier is the caller's plan as commerce records it, and nothing else.
//
// Only an ACCOUNT has a plan, so only a key that could name one is asked about: the
// buckets limitSubject gives callers it cannot name carry a colon, which an org slug
// cannot, so a lookup for one is a question about nobody — and asking it once per
// address seen would make a flood at this API a flood at commerce.
//
// A co-resident host's reader (object.TierReader) is asked on every call. It answers
// from its own cache and refreshes in the background (cloud tier_peer.go), so the
// read costs a map lookup; only a payer it has never seen costs one plane round trip.
// A second cache in front of it would expire on its own clock and answer Free for
// the call that found it empty, holding a paying caller to Free's quota once per
// expiry.
//
// A standalone ai has the HTTP route instead, behind the TierCache: an expired entry
// is served while it is read again, so it never blocks on the network once a key has
// been read. A key never read is held to Free until its first answer lands.
//
// An unread tier is not Free. Whichever transport fails, the last tier read for the
// key stands, so a commerce blip never moves a paying caller down.
//
// There is no operator override: a tier names a recorded subscription, so no
// configuration can grant one.
func DefaultTierFunc(key string) Tier {
	if strings.Contains(key, ":") {
		return TierZenFree
	}
	if r := object.TierReader(); r != nil {
		return readTier(r, key)
	}
	if tierCache == nil {
		return TierZenFree
	}
	tier, fresh, known := tierCache.read(key)
	if !fresh {
		tierCache.refreshAsync(key)
	}
	if !known {
		return TierZenFree
	}
	return tier
}

// tierReadTimeout bounds one read through the host's reader: a payer it has never
// seen is one plane round trip, and a request does not wait on more than that.
const tierReadTimeout = 2 * time.Second

// readTier is the host reader's answer for key, or the last tier read for it when
// the reader cannot say. A blank name is the reader saying it does not know.
func readTier(r object.TierReaderFunc, key string) Tier {
	ctx, cancel := context.WithTimeout(context.Background(), tierReadTimeout)
	defer cancel()
	name, err := r(ctx, key, key)
	if err != nil || strings.TrimSpace(name) == "" {
		if err != nil {
			log.Warning("tier: reading key=%s failed: %v (keeping the last tier read)", maskKey(key), err)
		}
		if tierCache != nil {
			if tier, _, known := tierCache.read(key); known {
				return tier
			}
		}
		return TierZenFree
	}
	tier := mapPlanToTier(name)
	if tierCache != nil {
		tierCache.set(key, tier)
	}
	return tier
}

// ── Commerce-backed tier cache ──────────────────────────────────────────────

const (
	// tierCacheTTL is how long a Commerce tier lookup remains valid.
	tierCacheTTL = 5 * time.Minute

	// tierCacheCleanupInterval is how often entries nobody asked about are dropped.
	tierCacheCleanupInterval = 10 * time.Minute

	// tierCacheKeep is how long an entry stays readable after it was fetched. Past its
	// TTL it is served while it is read again; past this it is dropped, so a key that
	// stopped calling stops costing memory.
	tierCacheKeep = 24 * time.Hour

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
// creates the tier cache. Must be called once during startup.
//
// The cache exists when a tier can be READ, by either route: the reader a
// co-resident host installs, or the HTTP endpoint a standalone ai is given.
// Keying its existence on the endpoint alone would leave a host that installs a
// reader and configures no endpoint able to read every tier and asking for none.
// With neither, there is nothing to ask and DefaultTierFunc falls back to
// env-var overrides or TierZenFree.
func InitTierCache() {
	endpoint := conf.GetConfigString("commerceEndpoint")
	if endpoint == "" && object.TierReader() == nil {
		log.Info("tier_cache: no commerce reader and no commerceEndpoint, Commerce tier lookup disabled")
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
	log.Info("tier_cache: initialized (endpoint=%s, ttl=%v)", endpoint, tierCacheTTL)
}

// read returns the tier held for key, whether it is still fresh, and whether any
// tier is held at all. A stale tier is still an answer: it is what this process last
// read, and it stands until a new read replaces it.
func (tc *TierCache) read(apiKey string) (tier Tier, fresh, known bool) {
	tc.mu.RLock()
	entry, ok := tc.entries[apiKey]
	tc.mu.RUnlock()

	if !ok {
		return "", false, false
	}
	return entry.tier, time.Since(entry.fetchedAt) <= tierTTL(entry.tier), true
}

// freeTierTTL is how long a FREE answer is trusted. Free is the one answer a
// payment changes, so it is held for seconds rather than minutes: an org that has
// just paid is rated as what it bought by the time checkout has brought it back,
// while a free org's burst still costs commerce one lookup.
const freeTierTTL = 10 * time.Second

func tierTTL(t Tier) time.Duration {
	if t == TierZenFree {
		return freeTierTTL
	}
	return tierCacheTTL
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
			// A failed read replaces nothing: the tier last read stands. A key never
			// read is held to Free, briefly, so a failing commerce is not asked again
			// on every request.
			if _, _, known := tc.read(apiKey); known {
				log.Warning("tier_cache: Commerce lookup failed for key=%s: %v (keeping the last tier read)", maskKey(apiKey), err)
				return
			}
			log.Warning("tier_cache: Commerce lookup failed for key=%s: %v (zen-free until a read succeeds)", maskKey(apiKey), err)
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
			if now.Sub(entry.fetchedAt) > tierCacheKeep {
				delete(tc.entries, key)
			}
		}
		tc.lastCleanup = now
		tc.mu.Unlock()
	}
}

// commerceTierResponse mirrors the JSON shape of GET /v1/billing/tier
// (commerce api/billing/tier.go GetTier): the plan name lives at tier.name.
type commerceTierResponse struct {
	Tier struct {
		Name string `json:"name"`
	} `json:"tier"`
}

// commerceTierLookup calls Commerce to resolve the billing plan for an IAM org
// and maps the plan name to a rate limit Tier. The orgKey is the IAM org slug
// (the `owner` claim) — the SAME key the balance gate bills against — so rate
// limiting and billing share one identity and one namespace. Commerce keys tier
// by ?user=<org>; passing anything else returns 400 "user query parameter is
// required" and silently falls back to the free tier (the bug this fixes).
// Returns TierZenFree on any error (fail-open: rate limiting must never deny
// service because Commerce is down).
func (tc *TierCache) commerceTierLookup(orgKey string) (Tier, error) {
	// Native path FIRST, the same order controllers.commerceFamilyTier reads in: a
	// co-resident host (cloud) installs object.TierReader, and the plan is then read
	// straight through the in-process commerce transport.
	//
	// The HTTP route below is answered by the cloud edge, which resolves the payer
	// from a verified IAM identity. A service token names no payer, so in-cluster
	// that route can only answer "sign in" — and every caller then reads as the
	// lowest tier regardless of what they pay for. So the reader is asked when it is
	// there, and the HTTP route is what a STANDALONE ai has instead of one.
	//
	// The plan name lands in the same mapPlanToTier as the decoded HTTP body: one
	// spelling of a tier, whichever transport carried it.
	if r := object.TierReader(); r != nil {
		// An org slug is its own namespace — subject and tenant are one value here,
		// which is why the HTTP path below sends the same string as both.
		name, err := r(context.Background(), orgKey, orgKey)
		if err != nil {
			return TierZenFree, fmt.Errorf("native tier read: %w", err)
		}
		return mapPlanToTier(name), nil
	}

	if tc.endpoint == "" {
		return TierZenFree, fmt.Errorf("no commerce reader and no commerceEndpoint")
	}

	// All commerce endpoints live under /v1/. Canonical path is /billing/tier,
	// keyed by the org slug as the `user` query parameter.
	endpoint := fmt.Sprintf("%s/v1/billing/tier?user=%s", tc.endpoint, url.QueryEscape(orgKey))

	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return TierZenFree, fmt.Errorf("build request: %w", err)
	}
	if tc.token != "" {
		req.Header.Set("Authorization", "Bearer "+tc.token)
	}
	// Scope commerce to this org's namespace (matches the balance gate's
	// X-Org-Id stamping so the tier is read from the right tenant).
	req.Header.Set("X-Org-Id", orgKey)

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

	var tierResp commerceTierResponse
	if err := json.NewDecoder(resp.Body).Decode(&tierResp); err != nil {
		return TierZenFree, fmt.Errorf("decode response: %w", err)
	}

	return mapPlanToTier(tierResp.Tier.Name), nil
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
