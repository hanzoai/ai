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
	"net/http"
	"testing"
	"time"
)

func TestRateLimiterAllow(t *testing.T) {
	// zen-free tier: 60 req/min => burst of 12 (60/5).
	rl := NewRateLimiter(func(string) Tier { return TierZenFree }, time.Hour)
	defer rl.Stop()

	key := "sk-test-key-free"

	// Burst of 12 should all succeed.
	for i := 0; i < 12; i++ {
		if !rl.Allow(key) {
			t.Fatalf("request %d should have been allowed (within burst)", i)
		}
	}

	// After exhausting burst, the next immediate request should be denied
	// because refill rate is 1/sec and no time has passed.
	if rl.Allow(key) {
		t.Fatal("request after burst should have been denied")
	}
}

func TestRateLimiterTierLimits(t *testing.T) {
	// Use a uniform small RPM for all tiers so the test can exhaust the burst
	// bucket quickly. The production tierLimits map has entries up to 100k RPM
	// whose burst (20k) is too large to loop through in a unit test without
	// tokens refilling mid-loop. We temporarily override every tier to a small
	// value, verify burst-then-deny behavior, and restore the original limits.
	const testRPM = 50 // burst = testRPM/5 = 10
	const testBurst = testRPM / 5

	origLimits := make(map[Tier]int, len(tierLimits))
	for k, v := range tierLimits {
		origLimits[k] = v
	}
	defer func() {
		for k, v := range origLimits {
			tierLimits[k] = v
		}
	}()

	tiers := []Tier{
		TierZenFree,
		TierZenPro,
		TierZenTeam,
		TierZenEnterprise,
		TierZenCustom,
	}

	for _, tier := range tiers {
		t.Run(string(tier), func(t *testing.T) {
			// Override just this tier to the small test value.
			tierLimits[tier] = testRPM

			rl := NewRateLimiter(func(string) Tier { return tier }, time.Hour)
			defer rl.Stop()

			key := "sk-tier-test"

			// All burst requests should succeed.
			for i := 0; i < testBurst; i++ {
				if !rl.Allow(key) {
					t.Fatalf("request %d should have been allowed (burst=%d)", i, testBurst)
				}
			}

			// Next request (immediately after burst) should be denied.
			if rl.Allow(key) {
				t.Fatalf("request after burst should be denied for tier %s", tier)
			}

			// Restore original limit for this tier before next iteration.
			tierLimits[tier] = origLimits[tier]
		})
	}
}

func TestRateLimiterMetrics(t *testing.T) {
	rl := NewRateLimiter(func(string) Tier { return TierZenFree }, time.Hour)
	defer rl.Stop()

	key := "sk-metrics-test"

	// 12 allowed (burst), then 1 denied.
	for i := 0; i < 12; i++ {
		rl.Allow(key)
	}
	rl.Allow(key) // should be denied

	allowed, denied := rl.Metrics()
	if allowed != 12 {
		t.Errorf("expected 12 allowed, got %d", allowed)
	}
	if denied != 1 {
		t.Errorf("expected 1 denied, got %d", denied)
	}
}

func TestRateLimiterRetryAfter(t *testing.T) {
	rl := NewRateLimiter(func(string) Tier { return TierZenFree }, time.Hour)
	defer rl.Stop()

	key := "sk-retry-test"

	// Exhaust burst (12 for zen-free) plus 1 more to trigger denial.
	for i := 0; i < 13; i++ {
		rl.Allow(key)
	}

	retryAfter := rl.RetryAfter(key)
	if retryAfter < 1 {
		t.Errorf("expected retry_after >= 1, got %d", retryAfter)
	}
}

func TestRateLimiterUnknownKeyRetryAfter(t *testing.T) {
	rl := NewRateLimiter(nil, time.Hour)
	defer rl.Stop()

	// Key that was never seen should return 1.
	retryAfter := rl.RetryAfter("sk-unknown")
	if retryAfter != 1 {
		t.Errorf("expected retry_after=1 for unknown key, got %d", retryAfter)
	}
}

func TestRateLimiterCleanup(t *testing.T) {
	rl := NewRateLimiter(func(string) Tier { return TierZenFree }, 50*time.Millisecond)
	defer rl.Stop()

	key := "sk-cleanup-test"
	rl.Allow(key)

	// Manually set lastSeen to the past so cleanup will evict it.
	rl.mu.Lock()
	rl.keys[key].lastSeen = time.Now().Add(-15 * time.Minute)
	rl.mu.Unlock()

	// Wait for cleanup tick.
	time.Sleep(150 * time.Millisecond)

	rl.mu.RLock()
	_, exists := rl.keys[key]
	rl.mu.RUnlock()

	if exists {
		t.Error("stale entry should have been evicted by cleanup")
	}
}

func TestRateLimiterSeparateKeys(t *testing.T) {
	rl := NewRateLimiter(func(string) Tier { return TierZenFree }, time.Hour)
	defer rl.Stop()

	keyA := "sk-user-a"
	keyB := "sk-user-b"

	// Exhaust key A's burst (12 for zen-free) plus 1 more.
	for i := 0; i < 13; i++ {
		rl.Allow(keyA)
	}

	// Key B should still be allowed — separate bucket.
	if !rl.Allow(keyB) {
		t.Error("key B should be allowed independently of key A")
	}
}

func TestIsRateLimitExempt(t *testing.T) {
	exemptPaths := []string{
		"/v1/health",
		"/health",
		"/v1/metrics",
		"/metrics",
		"/v1/ai/version",
		"/v1/ai/system",
	}
	for _, p := range exemptPaths {
		if !isRateLimitExempt(p) {
			t.Errorf("expected %q to be exempt", p)
		}
	}

	nonExemptPaths := []string{
		"/v1/chat/completions",
		"/v1/messages",
		"/v1/ai/chats",
		"/v1/models",
	}
	for _, p := range nonExemptPaths {
		if isRateLimitExempt(p) {
			t.Errorf("expected %q to NOT be exempt", p)
		}
	}
}

func TestDefaultTierFuncUnset(t *testing.T) {
	// With no RATE_LIMIT_TIERS set, everything should be zen-free tier.
	tier := DefaultTierFunc("sk-anything")
	if tier != TierZenFree {
		t.Errorf("expected TierZenFree, got %q", tier)
	}
}

func TestRateLimiterConcurrent(t *testing.T) {
	rl := NewRateLimiter(func(string) Tier { return TierZenPro }, time.Hour)
	defer rl.Stop()

	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				rl.Allow("sk-concurrent")
			}
			done <- struct{}{}
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	allowed, denied := rl.Metrics()
	total := allowed + denied
	if total != 1000 {
		t.Errorf("expected 1000 total operations, got %d", total)
	}
}

func TestMapPlanToTier(t *testing.T) {
	tests := []struct {
		plan     string
		expected Tier
	}{
		// Canonical zen-* names
		{"zen-free", TierZenFree},
		{"zen-pro", TierZenPro},
		{"zen-team", TierZenTeam},
		{"zen-enterprise", TierZenEnterprise},
		{"zen-custom", TierZenCustom},
		// Legacy names (backward compat)
		{"free", TierZenFree},
		{"developer", TierZenFree},
		{"starter", TierZenPro},
		{"pro", TierZenPro},
		{"team", TierZenTeam},
		{"enterprise", TierZenEnterprise},
		{"scale", TierZenEnterprise},
		{"custom", TierZenCustom},
		// Case insensitivity
		{"ZEN-PRO", TierZenPro},
		{"Enterprise", TierZenEnterprise},
		// Unknown defaults to zen-free
		{"", TierZenFree},
		{"unknown", TierZenFree},
	}

	for _, tt := range tests {
		t.Run(tt.plan, func(t *testing.T) {
			got := mapPlanToTier(tt.plan)
			if got != tt.expected {
				t.Errorf("mapPlanToTier(%q) = %q, want %q", tt.plan, got, tt.expected)
			}
		})
	}
}

// A page key IAM issued is throttled on the KEY, not on the org that pays for it.
//
// It is public by construction — it ships in the source of a page anybody can view
// — so the traffic behind one is every visitor at once rather than one tenant.
// Bucketed with the org, a single griefed page spends the whole org's ceiling and
// takes down that org's own API traffic and every other page it publishes. Two
// surfaces holding two keys must also fail independently, which is the entire
// reason for issuing two.
//
// Who PAYS is unchanged — the org still does. Who is THROTTLED is a different
// question, and this is the one place the two are allowed to differ.
//
// ISSUED is what earns the key its own lane. A string beginning pk- is something
// anyone can type, and a lane per typed string is no lane at all — so IAM is asked,
// and a key it does not know falls back to the caller's address like any other
// unnamed traffic.
func TestAPageKeyIsThrottledOnItsOwnKey(t *testing.T) {
	const site, cli, typed = "pk-live-site-key", "pk-live-cli-key", "pk-live-not-a-key"
	iamDoor(t, nil, map[string]string{site: "acme", cli: "acme"})
	billing(t)
	ceilings(t)

	// The limiter admits a burst of a fifth of the per-minute allowance, then
	// refills; the burst is what a page's visitors hit at once.
	burst := tierLimits[TierZenFree] / 5

	drive := func(key string) int {
		served := 0
		for i := 0; i < burst+5; i++ {
			p := ask("POST", "/v1/chat/completions").
				body([]byte(`{}`)).
				with("Authorization", "Bearer "+key)
			p = p.through(RateLimitFilter)
			if p.status() != http.StatusTooManyRequests {
				served++
			}
		}
		return served
	}

	siteServed := drive(site)
	if siteServed != burst {
		t.Errorf("site key served %d of %d before throttling", siteServed, burst)
	}

	// THE POINT: the second page key still has its whole budget. Exhausting one
	// surface cannot take the other down, which is the only thing that makes
	// issuing two keys mean anything.
	cliServed := drive(cli)
	if cliServed != burst {
		t.Errorf("a second page key served %d of %d — the two share a bucket", cliServed, burst)
	}

	// A key IAM never issued buys nothing: the lane it lands in is the caller's
	// address, which the two real keys have already left untouched.
	if typedServed := drive(typed); typedServed != burst {
		t.Errorf("an unissued page key served %d of %d", typedServed, burst)
	}
	if again := drive("pk-live-also-not-a-key"); again != 0 {
		t.Errorf("a second unissued page key served %d — typing a new string minted a new lane", again)
	}
}
