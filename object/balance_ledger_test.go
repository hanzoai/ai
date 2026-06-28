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

package object

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestSinglePodReplicaHint documents/asserts the in-pod ledger's single-pod
// invariant: the optional CLOUD_API_REPLICAS hint is parsed so the boot path can
// refuse to start at >1 replica (the in-pod reservation ledger double-spends
// across pods). Unset/garbage => ok=false (invariant logged, not enforced).
func TestSinglePodReplicaHint(t *testing.T) {
	t.Setenv("CLOUD_API_REPLICAS", "")
	if _, ok := SinglePodReplicaHint(); ok {
		t.Error("unset hint must be ok=false (invariant logged, not hard-enforced)")
	}
	t.Setenv("CLOUD_API_REPLICAS", "1")
	if n, ok := SinglePodReplicaHint(); !ok || n != 1 {
		t.Errorf("CLOUD_API_REPLICAS=1 => (1,true), got (%d,%v)", n, ok)
	}
	t.Setenv("CLOUD_API_REPLICAS", "2")
	if n, ok := SinglePodReplicaHint(); !ok || n != 2 {
		t.Errorf("CLOUD_API_REPLICAS=2 => (2,true) so boot can refuse, got (%d,%v)", n, ok)
	}
	t.Setenv("CLOUD_API_REPLICAS", "garbage")
	if _, ok := SinglePodReplicaHint(); ok {
		t.Error("unparseable hint must be ok=false")
	}
}

// TestLedgerColdSubjectCannotReserve: an unknown subject (no Commerce balance
// cached) can never be reserved against — the gate must fetch first.
func TestLedgerColdSubjectCannotReserve(t *testing.T) {
	l := NewBalanceLedger(BalanceLedgerTTL)
	if l.Reserve("cold", 1) {
		t.Fatal("cold subject must not be reservable")
	}
	if _, known := l.Available("cold"); known {
		t.Fatal("cold subject must be unknown")
	}
}

// TestLedgerReserveGatesOnAvailable: reservations succeed only while balance
// minus outstanding holds covers the estimate.
func TestLedgerReserveGatesOnAvailable(t *testing.T) {
	l := NewBalanceLedger(BalanceLedgerTTL)
	l.SetBalance("acme", 100)

	if !l.Reserve("acme", 60) {
		t.Fatal("first reserve of 60 against 100 must pass")
	}
	if l.Reserve("acme", 60) {
		t.Fatal("second reserve of 60 must fail (only 40 available)")
	}
	if !l.Reserve("acme", 40) {
		t.Fatal("reserve of remaining 40 must pass")
	}
	if l.Reserve("acme", 1) {
		t.Fatal("reserve of 1 with 0 available must fail")
	}
	if avail, _ := l.Available("acme"); avail != 0 {
		t.Fatalf("available=%d, want 0", avail)
	}
}

// TestLedgerSettleAppliesActualAndReleasesHold: settle releases the hold and
// decrements the cached balance by the real cost (closing the stale window).
func TestLedgerSettleAppliesActualAndReleasesHold(t *testing.T) {
	l := NewBalanceLedger(BalanceLedgerTTL)
	l.SetBalance("acme", 100)

	if !l.Reserve("acme", 50) { // hold 50 (upper bound)
		t.Fatal("reserve must pass")
	}
	if avail, _ := l.Available("acme"); avail != 50 {
		t.Fatalf("available after reserve=%d, want 50", avail)
	}
	l.Settle("acme", 50, 10) // actual spend was only 10
	// Hold released; balance reduced by the actual 10 => available 90.
	if avail, _ := l.Available("acme"); avail != 90 {
		t.Fatalf("available after settle=%d, want 90", avail)
	}
	bal, reserved, _, _ := l.Snapshot("acme")
	if bal != 90 || reserved != 0 {
		t.Fatalf("snapshot bal=%d reserved=%d, want 90/0", bal, reserved)
	}
}

// TestLedgerStaleWindowClosedByLocalDebit: even though the cached balance is not
// refreshed from Commerce, local settles make later reserves see the reduced
// balance — the 30s stale window can no longer over-grant.
func TestLedgerStaleWindowClosedByLocalDebit(t *testing.T) {
	l := NewBalanceLedger(BalanceLedgerTTL)
	l.SetBalance("acme", 100)
	// Spend 100 in ten settled requests of 10 each (reserve 10, settle 10).
	for i := 0; i < 10; i++ {
		if !l.Reserve("acme", 10) {
			t.Fatalf("reserve %d must pass", i)
		}
		l.Settle("acme", 10, 10)
	}
	// Balance is now locally 0 despite never re-fetching Commerce.
	if l.Reserve("acme", 1) {
		t.Fatal("after spending the full balance, no further reserve may pass within the stale window")
	}
}

// TestLedgerConcurrentNoOverdraw is the core race assertion: hammer Reserve from
// many goroutines against a fixed balance and prove the sum of granted holds
// never exceeds the balance and the balance never goes negative.
func TestLedgerConcurrentNoOverdraw(t *testing.T) {
	l := NewBalanceLedger(BalanceLedgerTTL)
	const balance = 1000
	const cost = 10 // each request costs exactly 10 => at most 100 may succeed
	l.SetBalance("race", balance)

	var granted int64
	var wg sync.WaitGroup
	for i := 0; i < 500; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.Reserve("race", cost) {
				atomic.AddInt64(&granted, 1)
				// Settle the full actual cost (worst case == estimate).
				l.Settle("race", cost, cost)
			}
		}()
	}
	wg.Wait()

	// At most balance/cost requests may ever be granted.
	if granted > balance/cost {
		t.Fatalf("overdraw: granted=%d, max allowed=%d", granted, balance/cost)
	}
	// Balance must be exactly drained to 0 (every grant settled cost), never negative.
	bal, reserved, _, _ := l.Snapshot("race")
	if bal < 0 {
		t.Fatalf("balance went negative: %d", bal)
	}
	if reserved != 0 {
		t.Fatalf("reservations leaked: reserved=%d", reserved)
	}
	if want := int64(balance) - granted*cost; bal != want {
		t.Fatalf("balance=%d, want %d (granted=%d)", bal, want, granted)
	}
}

// TestLedgerReserveThenConcurrentSettleNoNegative interleaves reserves and
// settles concurrently with -race to surface any unsynchronized access.
func TestLedgerReserveThenConcurrentSettleNoNegative(t *testing.T) {
	l := NewBalanceLedger(BalanceLedgerTTL)
	l.SetBalance("mix", 10000)
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.Reserve("mix", 50) {
				time.Sleep(time.Microsecond)
				l.Settle("mix", 50, 25)
			}
		}()
	}
	wg.Wait()
	if bal, _, _, _ := l.Snapshot("mix"); bal < 0 {
		t.Fatalf("balance negative under concurrency: %d", bal)
	}
}
