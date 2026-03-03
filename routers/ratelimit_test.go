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
	"testing"
	"time"
)

func TestRateLimiterAllow(t *testing.T) {
	// Free tier: 10 req/min => burst of 2 (10/5).
	rl := NewRateLimiter(func(string) Tier { return TierFree }, time.Hour)
	defer rl.Stop()

	key := "hk-test-key-free"

	// Burst of 2 should all succeed.
	for i := 0; i < 2; i++ {
		if !rl.Allow(key) {
			t.Fatalf("request %d should have been allowed (within burst)", i)
		}
	}

	// After exhausting burst, the next immediate request should be denied
	// because refill rate is ~0.17/sec and no time has passed.
	if rl.Allow(key) {
		t.Fatal("request after burst should have been denied")
	}
}

func TestRateLimiterTierLimits(t *testing.T) {
	tests := []struct {
		tier      Tier
		burstSize int // reqPerMin / 5
	}{
		{TierFree, 2},
		{TierStarter, 12},
		{TierPro, 60},
		{TierEnterprise, 200},
	}

	for _, tt := range tests {
		t.Run(string(tt.tier), func(t *testing.T) {
			rl := NewRateLimiter(func(string) Tier { return tt.tier }, time.Hour)
			defer rl.Stop()

			key := "hk-tier-test"

			// All burst requests should succeed.
			for i := 0; i < tt.burstSize; i++ {
				if !rl.Allow(key) {
					t.Fatalf("request %d should have been allowed (burst=%d)", i, tt.burstSize)
				}
			}

			// Next request (immediately after burst) should be denied.
			if rl.Allow(key) {
				t.Fatalf("request after burst should be denied for tier %s", tt.tier)
			}
		})
	}
}

func TestRateLimiterMetrics(t *testing.T) {
	rl := NewRateLimiter(func(string) Tier { return TierFree }, time.Hour)
	defer rl.Stop()

	key := "hk-metrics-test"

	// 2 allowed (burst), then 1 denied.
	rl.Allow(key)
	rl.Allow(key)
	rl.Allow(key) // should be denied

	allowed, denied := rl.Metrics()
	if allowed != 2 {
		t.Errorf("expected 2 allowed, got %d", allowed)
	}
	if denied != 1 {
		t.Errorf("expected 1 denied, got %d", denied)
	}
}

func TestRateLimiterRetryAfter(t *testing.T) {
	rl := NewRateLimiter(func(string) Tier { return TierFree }, time.Hour)
	defer rl.Stop()

	key := "hk-retry-test"

	// Exhaust burst.
	for i := 0; i < 3; i++ {
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
	retryAfter := rl.RetryAfter("hk-unknown")
	if retryAfter != 1 {
		t.Errorf("expected retry_after=1 for unknown key, got %d", retryAfter)
	}
}

func TestRateLimiterCleanup(t *testing.T) {
	rl := NewRateLimiter(func(string) Tier { return TierFree }, 50*time.Millisecond)
	defer rl.Stop()

	key := "hk-cleanup-test"
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
	rl := NewRateLimiter(func(string) Tier { return TierFree }, time.Hour)
	defer rl.Stop()

	keyA := "hk-user-a"
	keyB := "hk-user-b"

	// Exhaust key A's burst.
	for i := 0; i < 3; i++ {
		rl.Allow(keyA)
	}

	// Key B should still be allowed — separate bucket.
	if !rl.Allow(keyB) {
		t.Error("key B should be allowed independently of key A")
	}
}

func TestIsRateLimitExempt(t *testing.T) {
	exemptPaths := []string{
		"/api/health",
		"/health",
		"/api/metrics",
		"/metrics",
		"/api/models",
		"/api/get-version-info",
		"/api/get-system-info",
	}
	for _, p := range exemptPaths {
		if !isRateLimitExempt(p) {
			t.Errorf("expected %q to be exempt", p)
		}
	}

	nonExemptPaths := []string{
		"/api/chat/completions",
		"/api/messages",
		"/v1/messages",
		"/api/get-chats",
	}
	for _, p := range nonExemptPaths {
		if isRateLimitExempt(p) {
			t.Errorf("expected %q to NOT be exempt", p)
		}
	}
}

func TestDefaultTierFuncUnset(t *testing.T) {
	// With no RATE_LIMIT_TIERS set, everything should be free tier.
	tier := DefaultTierFunc("hk-anything")
	if tier != TierFree {
		t.Errorf("expected TierFree, got %q", tier)
	}
}

func TestRateLimiterConcurrent(t *testing.T) {
	rl := NewRateLimiter(func(string) Tier { return TierStarter }, time.Hour)
	defer rl.Stop()

	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				rl.Allow("hk-concurrent")
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
