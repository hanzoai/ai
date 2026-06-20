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
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// newTestGate builds a BalanceGate wired to the given fetch stub, without the
// background cleanup loop, so checkBalance can be exercised deterministically.
func newTestGate(fetch func(userKey string) (int64, error)) *BalanceGate {
	bg := &BalanceGate{
		entries:      make(map[string]*balanceCacheEntry),
		userKeyCache: make(map[string]*userKeyCacheEntry),
		inflight:     make(map[string]struct{}),
		client:       &http.Client{},
		fetch:        fetch,
	}
	return bg
}

// waitInflightDrained waits until all async refreshes have completed so a test
// can assert on post-refresh cache state without racing the goroutine.
func waitInflightDrained(t *testing.T, bg *BalanceGate) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		bg.inflightMu.Lock()
		n := len(bg.inflight)
		bg.inflightMu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("async refresh did not drain in time")
}

func TestCheckBalance_Matrix(t *testing.T) {
	const user = "hanzo/alice"
	errUnreachable := fmt.Errorf("commerce unreachable")

	tests := []struct {
		name string
		// seed, if non-nil, pre-populates the cache entry for `user`.
		seed *balanceCacheEntry
		// fetch is the stubbed Commerce lookup.
		fetch func(userKey string) (int64, error)
		// failOpen sets BILLING_FAIL_OPEN for the case.
		failOpen bool

		wantAllowed bool
		wantUnknown bool
	}{
		{
			name:        "fresh positive -> allow",
			seed:        &balanceCacheEntry{balanceCents: 500, fetchedAt: time.Now()},
			fetch:       func(string) (int64, error) { t.Fatal("fetch must not be called on fresh hit"); return 0, nil },
			wantAllowed: true,
			wantUnknown: false,
		},
		{
			name:        "fresh zero -> deny 402 (not unknown)",
			seed:        &balanceCacheEntry{balanceCents: 0, fetchedAt: time.Now()},
			fetch:       func(string) (int64, error) { t.Fatal("fetch must not be called on fresh hit"); return 0, nil },
			wantAllowed: false,
			wantUnknown: false,
		},
		{
			name:        "miss + reachable positive -> allow",
			fetch:       func(string) (int64, error) { return 250, nil },
			wantAllowed: true,
			wantUnknown: false,
		},
		{
			name:        "miss + reachable zero -> deny 402 (not unknown)",
			fetch:       func(string) (int64, error) { return 0, nil },
			wantAllowed: false,
			wantUnknown: false,
		},
		{
			name:        "miss + unreachable + fail-closed -> deny + unknown (503)",
			fetch:       func(string) (int64, error) { return 0, errUnreachable },
			wantAllowed: false,
			wantUnknown: true,
		},
		{
			name:        "miss + unreachable + fail-open -> allow",
			fetch:       func(string) (int64, error) { return 0, errUnreachable },
			failOpen:    true,
			wantAllowed: true,
			wantUnknown: false,
		},
		{
			name:        "stale-within-grace positive + unreachable -> allow (serve-stale)",
			seed:        &balanceCacheEntry{balanceCents: 700, fetchedAt: time.Now().Add(-(balanceCacheTTL + balanceStaleGrace/2))},
			fetch:       func(string) (int64, error) { return 0, errUnreachable },
			wantAllowed: true,
			wantUnknown: false,
		},
		{
			name:        "stale-beyond-grace positive + unreachable + fail-closed -> deny + unknown",
			seed:        &balanceCacheEntry{balanceCents: 700, fetchedAt: time.Now().Add(-(balanceCacheTTL + balanceStaleGrace + time.Second))},
			fetch:       func(string) (int64, error) { return 0, errUnreachable },
			wantAllowed: false,
			wantUnknown: true,
		},
		{
			name:        "stale-beyond-grace positive + unreachable + fail-open -> allow",
			seed:        &balanceCacheEntry{balanceCents: 700, fetchedAt: time.Now().Add(-(balanceCacheTTL + balanceStaleGrace + time.Second))},
			fetch:       func(string) (int64, error) { return 0, errUnreachable },
			failOpen:    true,
			wantAllowed: true,
			wantUnknown: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.failOpen {
				t.Setenv("BILLING_FAIL_OPEN", "true")
			} else {
				t.Setenv("BILLING_FAIL_OPEN", "")
			}

			bg := newTestGate(tt.fetch)
			if tt.seed != nil {
				bg.entries[user] = tt.seed
			}

			allowed, _, unknown := bg.checkBalance(user)
			if allowed != tt.wantAllowed || unknown != tt.wantUnknown {
				t.Errorf("checkBalance() = (allowed=%v, unknown=%v), want (allowed=%v, unknown=%v)",
					allowed, unknown, tt.wantAllowed, tt.wantUnknown)
			}

			// Any past-TTL path triggers an async refresh; let it drain so the
			// stub goroutine cannot outlive the test and trip the race detector.
			waitInflightDrained(t, bg)
		})
	}
}

// TestCheckBalance_StaleBeyondGraceDeniesNowZeroUser is the explicit
// revenue-leak regression: a user whose cached balance was positive but is now
// zero must NOT keep being allowed once the entry ages past the grace while
// Commerce is unreachable.
func TestCheckBalance_StaleBeyondGraceDeniesNowZeroUser(t *testing.T) {
	t.Setenv("BILLING_FAIL_OPEN", "")
	const user = "hanzo/bob"

	bg := newTestGate(func(string) (int64, error) {
		return 0, fmt.Errorf("commerce unreachable")
	})
	// Last successful fetch saw a positive balance, but it is now well past the
	// staleness grace.
	bg.entries[user] = &balanceCacheEntry{
		balanceCents: 1000,
		fetchedAt:    time.Now().Add(-(balanceCacheTTL + balanceStaleGrace + 5*time.Second)),
	}

	allowed, _, unknown := bg.checkBalance(user)
	if allowed {
		t.Fatal("now-zero user must be denied once stale beyond grace during outage")
	}
	if !unknown {
		t.Fatal("stale-beyond-grace denial must be reported as unknown (-> 503), not 402")
	}
	waitInflightDrained(t, bg)
}

// TestCheckBalance_RefreshAsyncRepopulates verifies the stale-within-grace path
// kicks a background refresh that updates the cache through the fetch seam.
func TestCheckBalance_RefreshAsyncRepopulates(t *testing.T) {
	t.Setenv("BILLING_FAIL_OPEN", "")
	const user = "hanzo/carol"

	var mu sync.Mutex
	calls := 0
	bg := newTestGate(func(string) (int64, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return 4242, nil
	})
	// Stale within grace: serve stale (300) now, refresh in background to 4242.
	bg.entries[user] = &balanceCacheEntry{
		balanceCents: 300,
		fetchedAt:    time.Now().Add(-(balanceCacheTTL + balanceStaleGrace/2)),
	}

	allowed, balance, unknown := bg.checkBalance(user)
	if !allowed || unknown || balance != 300 {
		t.Fatalf("serve-stale: got (allowed=%v, balance=%d, unknown=%v), want (true, 300, false)", allowed, balance, unknown)
	}

	waitInflightDrained(t, bg)

	bg.mu.RLock()
	got := bg.entries[user].balanceCents
	bg.mu.RUnlock()
	if got != 4242 {
		t.Errorf("after async refresh balance = %d, want 4242", got)
	}

	mu.Lock()
	gotCalls := calls
	mu.Unlock()
	if gotCalls != 1 {
		t.Errorf("fetch called %d times, want exactly 1", gotCalls)
	}
}

// TestFetchBalance_PaymentRequiredMapsToZero verifies a commerce 402 is treated
// as a definite zero balance (-> caller 402), not a transport error (-> 503).
func TestFetchBalance_PaymentRequiredMapsToZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer srv.Close()

	bg := newTestGate(nil)
	bg.endpoint = srv.URL
	bg.token = "svc-token"

	balance, err := bg.fetchBalance("hanzo/dave")
	if err != nil {
		t.Fatalf("402 must NOT surface as an error (would map to 503); got %v", err)
	}
	if balance != 0 {
		t.Errorf("402 must map to balance 0 (-> caller 402); got %d", balance)
	}
}

// TestFetchBalance_ServerErrorIsTransportError verifies a 5xx remains an error
// so the caller treats it as unknown (-> 503), never as a zero balance.
func TestFetchBalance_ServerErrorIsTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	bg := newTestGate(nil)
	bg.endpoint = srv.URL

	if _, err := bg.fetchBalance("hanzo/dave"); err == nil {
		t.Fatal("commerce 5xx must surface as an error (unknown -> 503)")
	}
}

// TestFetchBalance_OKDecodesAvailable verifies the happy path decodes the
// available-cents field.
func TestFetchBalance_OKDecodesAvailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"available":1234}`)
	}))
	defer srv.Close()

	bg := newTestGate(nil)
	bg.endpoint = srv.URL

	balance, err := bg.fetchBalance("hanzo/dave")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if balance != 1234 {
		t.Errorf("balance = %d, want 1234", balance)
	}
}
